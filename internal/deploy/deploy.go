// Package deploy turns an app's desired state into a running Swarm service.
package deploy

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/SimpleAOB/SimplePanel/internal/docker"
	"github.com/SimpleAOB/SimplePanel/internal/proxy"
	"github.com/SimpleAOB/SimplePanel/internal/store"
)

// Config holds the deployer's environment.
type Config struct {
	Network   string // overlay network apps join, e.g. "simplepanel"
	ProxyDir  string // Traefik dynamic config folder
	WorkDir   string // scratch space for git clones
	PanelHost string // optional: route this domain to the panel itself
	PanelSvc  string // panel service name
	PanelPort int
	// EndpointMode is "vip" (default, uses IPVS) or "dnsrr" for kernels
	// without IPVS support (some LXC/OpenVZ VPSes).
	EndpointMode string
	// HelperImage runs short-lived volume backup and restore containers.
	HelperImage string
}

type Manager struct {
	cfg    Config
	store  *store.Store
	docker *docker.Client
	log    *slog.Logger

	mu       sync.Mutex
	appLocks map[int64]*sync.Mutex
	builds   chan struct{} // limits concurrent builds
	proxyMu  sync.Mutex
	netMu    sync.Mutex // serialises project network creation

	// OnDeploy, if set, is called when a deployment finishes.
	OnDeploy func(app *store.App, d *store.Deployment)
}

func NewManager(cfg Config, st *store.Store, dc *docker.Client, log *slog.Logger) *Manager {
	return &Manager{cfg: cfg, store: st, docker: dc, log: log, appLocks: map[int64]*sync.Mutex{}, builds: make(chan struct{}, 2)}
}

func (m *Manager) lockApp(id int64) *sync.Mutex {
	m.mu.Lock()
	defer m.mu.Unlock()
	l, ok := m.appLocks[id]
	if !ok {
		l = &sync.Mutex{}
		m.appLocks[id] = l
	}
	return l
}

// Deploy queues a deployment and runs it in the background.
func (m *Manager) Deploy(ctx context.Context, appID int64, trigger string) (*store.Deployment, error) {
	d, err := m.store.CreateDeployment(ctx, appID, trigger)
	if err != nil {
		return nil, err
	}
	go m.run(appID, d.ID)
	return d, nil
}

// logBuf collects deployment output, masks secrets and periodically saves it.
type logBuf struct {
	mu      sync.Mutex
	buf     bytes.Buffer
	secrets []string
}

const maxLog = 512 << 10

func (l *logBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.buf.Len() < maxLog {
		l.buf.Write(p)
	}
	return len(p), nil
}

func (l *logBuf) Printf(format string, a ...any) { fmt.Fprintf(l, "==> "+format+"\n", a...) }

func (l *logBuf) String() string {
	l.mu.Lock()
	s := l.buf.String()
	l.mu.Unlock()
	for _, sec := range l.secrets {
		if sec != "" {
			s = strings.ReplaceAll(s, sec, "********")
		}
	}
	if len(s) >= maxLog {
		s += "\n[log truncated]\n"
	}
	return s
}

func (m *Manager) run(appID, depID int64) {
	lock := m.lockApp(appID)
	lock.Lock()
	defer lock.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	bg := context.Background()

	lb := &logBuf{}
	image := ""
	save := func(status string, done bool) {
		if err := m.store.UpdateDeployment(bg, depID, status, image, lb.String(), done); err != nil {
			m.log.Error("save deployment", "id", depID, "err", err)
		}
	}
	save("running", false)

	stop := make(chan struct{})
	go func() { // stream logs to the DB so the UI can follow along
		t := time.NewTicker(time.Second)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				save("running", false)
			}
		}
	}()

	err := func() error {
		app, err := m.store.App(ctx, appID)
		if err != nil {
			return err
		}
		lb.secrets = []string{app.GitToken}
		switch app.Source {
		case "image":
			image, err = m.pullImage(ctx, app, lb)
		case "git":
			image, err = m.buildGit(ctx, app, depID, lb)
		default:
			err = fmt.Errorf("unknown source %q", app.Source)
		}
		if err != nil {
			return err
		}
		if err := m.applyService(ctx, app, image, lb); err != nil {
			return err
		}
		return m.SyncProxy(ctx)
	}()
	close(stop)
	status := "succeeded"
	if err != nil {
		lb.Printf("Deployment failed: %v", err)
		status = "failed"
	} else {
		lb.Printf("Deployment succeeded")
	}
	save(status, true)
	if m.OnDeploy != nil {
		if app, aerr := m.store.App(bg, appID); aerr == nil {
			d, _ := m.store.Deployment(bg, depID)
			if d == nil {
				d = &store.Deployment{ID: depID, Status: status}
			}
			if err != nil {
				d.Log = err.Error() // the reason, not the whole log
			}
			m.OnDeploy(app, d)
		}
	}
}

func (m *Manager) pullImage(ctx context.Context, app *store.App, lb *logBuf) (string, error) {
	lb.Printf("Pulling %s", app.Image)
	if err := m.docker.ImagePull(ctx, app.Image, lb); err != nil {
		// Fall back to an image that already exists on this server (for
		// example one built by hand or loaded with `docker load`).
		if _, ierr := m.docker.ImageDigestRef(ctx, app.Image); ierr == nil {
			lb.Printf("Pull failed (%v); using the image already on this server", err)
			return app.Image, nil
		}
		return "", fmt.Errorf("pull %s: %w", app.Image, err)
	}
	ref, err := m.docker.ImageDigestRef(ctx, app.Image)
	if err != nil {
		return app.Image, nil
	}
	if ref != app.Image {
		lb.Printf("Pinned to %s", ref)
	}
	return ref, nil
}

func (m *Manager) buildGit(ctx context.Context, app *store.App, depID int64, lb *logBuf) (string, error) {
	select {
	case m.builds <- struct{}{}:
		defer func() { <-m.builds }()
	case <-ctx.Done():
		return "", ctx.Err()
	}
	dir, err := os.MkdirTemp(m.cfg.WorkDir, "build-*")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(dir)

	repo := filepath.Join(dir, "repo")
	lb.Printf("Cloning %s (branch: %s)", app.GitURL, orDefault(app.GitBranch, "default"))
	if err := gitClone(ctx, app.GitURL, app.GitBranch, app.GitToken, repo, filepath.Join(dir, "home"), lb); err != nil {
		return "", err
	}
	if out, err := exec.CommandContext(ctx, "git", "-C", repo, "log", "-1", "--format=%h %s").Output(); err == nil {
		lb.Printf("Commit %s", strings.TrimSpace(string(out)))
	}

	ctxDir := repo
	if app.BuildPath != "" {
		ctxDir = filepath.Join(repo, filepath.FromSlash(app.BuildPath))
	}
	if fi, err := os.Stat(ctxDir); err != nil || !fi.IsDir() {
		return "", fmt.Errorf("build path %s does not exist in the repository", app.BuildPath)
	}
	dockerfile := orDefault(app.Dockerfile, "Dockerfile")
	switch ResolveBuildType(app.BuildType, ctxDir, app.Dockerfile) {
	case "dockerfile":
		if fi, err := os.Lstat(filepath.Join(ctxDir, filepath.FromSlash(dockerfile))); err != nil || !fi.Mode().IsRegular() {
			return "", fmt.Errorf("no %s found in %s; add one, or set the build type to Static site", dockerfile, orDefault(app.BuildPath, "the repository root"))
		}
		lb.Printf("Building with %s", dockerfile)
	case "static":
		if app.BuildType == "auto" || app.BuildType == "" {
			lb.Printf("No %s found: building a static site", dockerfile)
		}
		name, _, err := WriteStaticDockerfile(ctxDir, StaticOptions{
			BuildImage: app.BuildImage, Command: app.BuildCommand, OutputDir: app.OutputDir, SPA: app.SPA,
		})
		if err != nil {
			return "", err
		}
		if app.BuildCommand != "" {
			lb.Printf("Static site: running %q in %s, serving %s/", app.BuildCommand, orDefault(app.BuildImage, DefaultStaticBuildImage), orDefault(app.OutputDir, "."))
		} else {
			lb.Printf("Static site: serving %s/ as-is", orDefault(app.OutputDir, "."))
		}
		if app.SPA {
			lb.Printf("Single-page app fallback: unknown paths serve index.html")
		}
		dockerfile = name
	}

	tag := fmt.Sprintf("simplepanel/%s:%d", app.ServiceName(), depID)
	lb.Printf("Building image %s", tag)
	build := func(noPull bool) error {
		pr, pw := io.Pipe()
		go func() { pw.CloseWithError(tarDir(ctxDir, pw)) }()
		err := m.docker.ImageBuild(ctx, pr, docker.BuildOptions{Tag: tag, Dockerfile: dockerfile, NoPull: noPull}, lb)
		pr.Close()
		return err
	}
	err = build(false)
	if err != nil && isRegistryPullError(err) {
		// Base images are refreshed on every build for security updates.
		// If the registry refuses (e.g. Docker Hub rate limits), fall back
		// to the base images already on this server.
		lb.Printf("Could not refresh base images (%v); retrying with the cached ones", err)
		err = build(true)
	}
	if err != nil {
		return "", fmt.Errorf("build: %w", err)
	}
	return tag, nil
}

func orDefault(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

// gitClone runs git with an argument list (never a shell). The token is
// passed via GIT_CONFIG_* environment variables so it never appears in the
// command line or in the cloned repo's config.
func gitClone(ctx context.Context, repoURL, branch, token, dst, home string, w io.Writer) error {
	if err := os.MkdirAll(home, 0o700); err != nil {
		return err
	}
	args := []string{"clone", "--depth", "1", "--single-branch", "--no-tags", "--recurse-submodules=no"}
	if branch != "" {
		args = append(args, "--branch", branch)
	}
	args = append(args, "--", repoURL, dst)
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Env = []string{
		"HOME=" + home,
		"PATH=" + os.Getenv("PATH"),
		"GIT_TERMINAL_PROMPT=0",
		"GIT_ALLOW_PROTOCOL=https",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=/dev/null",
	}
	// Respect outbound proxy / CA settings of the host environment.
	for _, k := range []string{"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy", "NO_PROXY", "no_proxy", "SSL_CERT_FILE", "SSL_CERT_DIR", "GIT_SSL_CAINFO"} {
		if v, ok := os.LookupEnv(k); ok {
			cmd.Env = append(cmd.Env, k+"="+v)
		}
	}
	if token != "" {
		u, _ := url.Parse(repoURL)
		user := "oauth2"
		if u.Host == "github.com" {
			user = "x-access-token"
		}
		cred := base64.StdEncoding.EncodeToString([]byte(user + ":" + token))
		cmd.Env = append(cmd.Env,
			"GIT_CONFIG_COUNT=1",
			"GIT_CONFIG_KEY_0=http.https://"+u.Host+"/.extraHeader",
			"GIT_CONFIG_VALUE_0=Authorization: Basic "+cred,
		)
	}
	cmd.Stdout, cmd.Stderr = w, w
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git clone failed (check the URL, branch and access token): %w", err)
	}
	return nil
}

// tarDir streams dir as a tar archive, skipping .git. Symlinks are stored as
// links (Docker resolves them inside the build context).
func tarDir(dir string, w io.Writer) error {
	tw := tar.NewWriter(w)
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil || rel == "." {
			return err
		}
		if d.IsDir() && d.Name() == ".git" {
			return filepath.SkipDir
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		link := ""
		if info.Mode()&fs.ModeSymlink != 0 {
			if link, err = os.Readlink(p); err != nil {
				return err
			}
		}
		hdr, err := tar.FileInfoHeader(info, link)
		if err != nil {
			return err
		}
		hdr.Name = filepath.ToSlash(rel)
		hdr.Uid, hdr.Gid, hdr.Uname, hdr.Gname = 0, 0, "", ""
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if info.Mode().IsRegular() {
			f, err := os.Open(p)
			if err != nil {
				return err
			}
			_, err = io.Copy(tw, f)
			f.Close()
			return err
		}
		return nil
	})
	if err != nil {
		return err
	}
	return tw.Close()
}

// ServiceSpec builds the Swarm spec for an app running image.
func (m *Manager) ServiceSpec(app *store.App, image string) (docker.ServiceSpec, error) {
	env, err := ParseEnv(app.Env)
	if err != nil {
		return docker.ServiceSpec{}, err
	}
	args, err := SplitCommand(app.Command)
	if err != nil {
		return docker.ServiceSpec{}, err
	}
	labels := map[string]string{
		"simplepanel.managed": "true",
		"simplepanel.app":     fmt.Sprint(app.ID),
		"simplepanel.project": app.ProjectName,
	}
	var mounts []docker.Mount
	for _, v := range app.Volumes {
		mounts = append(mounts, docker.Mount{Type: "volume", Source: app.ServiceName() + "_" + v.Name, Target: v.Path})
	}
	replicas := uint64(max(app.Replicas, 0))
	// Every app joins its project's private network (with its short name as
	// an alias). Only regular apps join the shared proxy network; databases
	// stay private to the project.
	networks := []docker.NetworkAttach{{Target: ProjectNetwork(app.ProjectName), Aliases: []string{app.Name}}}
	if !app.IsDatabase() {
		networks = append(networks, docker.NetworkAttach{Target: m.cfg.Network})
	}
	labels["simplepanel.kind"] = orDefault(app.Kind, "app")
	spec := docker.ServiceSpec{
		Name:   app.ServiceName(),
		Labels: labels,
		TaskTemplate: docker.TaskSpec{
			ContainerSpec: docker.ContainerSpec{Image: image, Labels: labels, Env: env, Args: args, Mounts: mounts},
			RestartPolicy: &docker.RestartPolicy{Condition: "any", Delay: int64(5 * time.Second)},
			Networks:      networks,
			LogDriver:     &docker.LogDriver{Name: "json-file", Options: map[string]string{"max-size": "10m", "max-file": "3"}},
		},
		Mode: docker.ServiceMode{Replicated: &docker.Replicated{Replicas: replicas}},
		UpdateConfig: &docker.UpdateConfig{
			Parallelism: 1, Order: "start-first", FailureAction: "rollback", Monitor: int64(15 * time.Second),
		},
		RollbackConfig: &docker.UpdateConfig{Parallelism: 1, Order: "start-first", FailureAction: "pause"},
		EndpointSpec:   &docker.EndpointSpec{Mode: orDefault(m.cfg.EndpointMode, "vip")},
	}
	if app.IsDatabase() {
		// Two database processes must never share a data directory, so
		// stop the old task before starting the new one.
		replicas = min(replicas, 1)
		spec.Mode.Replicated.Replicas = replicas
		spec.UpdateConfig.Order = "stop-first"
		spec.RollbackConfig.Order = "stop-first"
	}
	if app.CPULimit > 0 || app.MemoryMB > 0 {
		spec.TaskTemplate.Resources = &docker.Resources{Limits: &docker.Limits{
			NanoCPUs:    int64(app.CPULimit * 1e9),
			MemoryBytes: int64(app.MemoryMB) << 20,
		}}
	}
	publishMode := "ingress"
	if spec.EndpointSpec.Mode == "dnsrr" {
		publishMode = "host" // the routing mesh requires VIP mode
	}
	for _, p := range app.Ports {
		spec.EndpointSpec.Ports = append(spec.EndpointSpec.Ports, docker.PortSpec{
			Protocol: orDefault(p.Protocol, "tcp"), TargetPort: p.Target, PublishedPort: p.Published, PublishMode: publishMode,
		})
	}
	return spec, nil
}

func (m *Manager) applyService(ctx context.Context, app *store.App, image string, lb *logBuf) error {
	spec, err := m.ServiceSpec(app, image)
	if err != nil {
		return err
	}
	m.netMu.Lock()
	err = m.docker.NetworkEnsure(ctx, ProjectNetwork(app.ProjectName), map[string]string{
		"simplepanel.managed": "true", "simplepanel.project": app.ProjectName,
	})
	m.netMu.Unlock()
	if err != nil {
		return fmt.Errorf("create project network: %w", err)
	}
	existing, err := m.docker.ServiceInspect(ctx, spec.Name)
	switch {
	case errors.Is(err, docker.ErrNotFound):
		lb.Printf("Creating service %s", spec.Name)
		if _, err := m.docker.ServiceCreate(ctx, spec); err != nil {
			return fmt.Errorf("create service: %w", err)
		}
	case err != nil:
		return err
	default:
		if spec.UpdateConfig.Order == "start-first" {
			lb.Printf("Rolling update of %s (start-first, zero downtime)", spec.Name)
		} else {
			lb.Printf("Updating %s (stop-first: databases restart briefly)", spec.Name)
		}
		spec.TaskTemplate.ForceUpdate = existing.Spec.TaskTemplate.ForceUpdate + 1
		if err := m.docker.ServiceUpdate(ctx, existing.ID, existing.Version.Index, spec); err != nil {
			return fmt.Errorf("update service: %w", err)
		}
	}
	if app.Replicas == 0 {
		return nil
	}
	return m.waitHealthy(ctx, spec.Name, image, lb)
}

// waitHealthy waits until the update completes or a task with the new image
// is running, and reports rollbacks as failures.
func (m *Manager) waitHealthy(ctx context.Context, name, image string, lb *logBuf) error {
	deadline := time.Now().Add(5 * time.Minute)
	lastMsg := ""
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
		svc, err := m.docker.ServiceInspect(ctx, name)
		if err != nil {
			return err
		}
		if us := svc.UpdateStatus; us != nil {
			if us.Message != lastMsg && us.Message != "" {
				lb.Printf("Swarm: %s", us.Message)
				lastMsg = us.Message
			}
			switch us.State {
			case "completed":
				return nil
			case "rollback_started", "rollback_completed", "rollback_paused", "paused":
				return fmt.Errorf("the new version failed to start and Swarm rolled back (%s); check the app logs", us.State)
			}
			continue
		}
		// Fresh service (no update status): look at its tasks.
		tasks, err := m.docker.ServiceTasks(ctx, name)
		if err != nil {
			return err
		}
		failed := 0
		for _, t := range tasks {
			if t.Spec.ContainerSpec.Image != image {
				continue
			}
			switch t.Status.State {
			case "running":
				return nil
			case "failed", "rejected":
				failed++
				if msg := t.Status.Err; msg != "" && msg != lastMsg {
					lb.Printf("Task %s: %s", t.Status.State, msg)
					lastMsg = msg
				}
			}
		}
		if failed >= 3 {
			return errors.New("the container keeps failing to start; check the app logs")
		}
	}
	return errors.New("timed out waiting for the app to start")
}

// Restart forces a rolling restart of the app's tasks without rebuilding.
func (m *Manager) Restart(ctx context.Context, app *store.App) error {
	svc, err := m.docker.ServiceInspect(ctx, app.ServiceName())
	if err != nil {
		return err
	}
	svc.Spec.TaskTemplate.ForceUpdate++
	return m.docker.ServiceUpdate(ctx, svc.ID, svc.Version.Index, svc.Spec)
}

// Scale sets the replica count on the running service (0 stops the app).
func (m *Manager) Scale(ctx context.Context, app *store.App, replicas uint64) error {
	svc, err := m.docker.ServiceInspect(ctx, app.ServiceName())
	if err != nil {
		return err
	}
	if svc.Spec.Mode.Replicated == nil {
		return errors.New("service is not replicated")
	}
	svc.Spec.Mode.Replicated.Replicas = replicas
	return m.docker.ServiceUpdate(ctx, svc.ID, svc.Version.Index, svc.Spec)
}

// Remove deletes the app's service. Named volumes are kept on purpose.
func (m *Manager) Remove(ctx context.Context, app *store.App) error {
	lock := m.lockApp(app.ID)
	lock.Lock()
	defer lock.Unlock()
	return m.docker.ServiceRemove(ctx, app.ServiceName())
}

// RemoveProjectNetwork deletes a project's network once its services are
// gone. Swarm needs a moment to detach tasks, so it retries in the background.
func (m *Manager) RemoveProjectNetwork(project string) {
	go func() {
		for i := 0; i < 10; i++ {
			time.Sleep(3 * time.Second)
			if err := m.docker.NetworkRemove(context.Background(), ProjectNetwork(project)); err == nil {
				return
			}
		}
		m.log.Warn("could not remove project network", "network", ProjectNetwork(project))
	}()
}

// Status summarises the app's Swarm tasks.
type Status struct {
	State   string `json:"state"` // not_deployed|running|starting|stopped|failing
	Running int    `json:"running"`
	Desired int    `json:"desired"`
	Message string `json:"message,omitempty"`
}

func (m *Manager) Status(ctx context.Context, app *store.App) Status {
	svc, err := m.docker.ServiceInspect(ctx, app.ServiceName())
	if errors.Is(err, docker.ErrNotFound) {
		return Status{State: "not_deployed"}
	}
	if err != nil {
		return Status{State: "unknown", Message: err.Error()}
	}
	st := Status{}
	if svc.Spec.Mode.Replicated != nil {
		st.Desired = int(svc.Spec.Mode.Replicated.Replicas)
	}
	tasks, _ := m.docker.ServiceTasks(ctx, app.ServiceName())
	var lastErr string
	var lastErrAt time.Time
	for _, t := range tasks {
		if t.DesiredState == "running" && t.Status.State == "running" {
			st.Running++
		}
		if t.Status.Err != "" && t.Status.Timestamp.After(lastErrAt) {
			lastErr, lastErrAt = t.Status.Err, t.Status.Timestamp
		}
	}
	switch {
	case st.Desired == 0:
		st.State = "stopped"
	case st.Running >= st.Desired:
		st.State = "running"
	case lastErr != "" && time.Since(lastErrAt) < 2*time.Minute:
		st.State, st.Message = "failing", lastErr
	default:
		st.State = "starting"
	}
	return st
}

// Logs returns recent log lines for the app.
func (m *Manager) Logs(ctx context.Context, app *store.App, tail int, since int64) (string, error) {
	return m.docker.ServiceLogs(ctx, app.ServiceName(), tail, since)
}

// SyncProxy rewrites Traefik's dynamic config from the database.
func (m *Manager) SyncProxy(ctx context.Context) error {
	m.proxyMu.Lock()
	defer m.proxyMu.Unlock()
	routes, err := m.store.AllRoutes(ctx)
	if err != nil {
		return err
	}
	var rs []proxy.Route
	taken := map[string]bool{}
	firstPort := map[int64]int{}
	for _, r := range routes {
		rs = append(rs, proxy.Route{Key: fmt.Sprintf("d%d", r.ID), Host: r.Host, Service: r.Service, Port: r.Port, HTTPS: r.HTTPS})
		taken[r.Host] = true
		if _, ok := firstPort[r.AppID]; !ok {
			firstPort[r.AppID] = r.Port
		}
	}
	panelHost, _ := m.store.Setting(ctx, "panel_domain")
	if panelHost != "" && m.cfg.PanelSvc != "" {
		rs = append(rs, proxy.Route{Key: "panel", Host: panelHost, Service: m.cfg.PanelSvc, Port: m.cfg.PanelPort, HTTPS: true})
		taken[panelHost] = true
	}
	if base, _ := m.store.Setting(ctx, "wildcard_domain"); base != "" {
		apps, err := m.store.Apps(ctx, 0)
		if err != nil {
			return err
		}
		for i := range apps {
			a := &apps[i]
			host := AutoHost(a, base)
			if host == "" || taken[host] {
				continue
			}
			port := a.HTTPPort
			if port == 0 {
				port = firstPort[a.ID]
			}
			if port == 0 {
				port = 80
			}
			rs = append(rs, proxy.Route{Key: fmt.Sprintf("a%d", a.ID), Host: host, Service: a.ServiceName(), Port: port, HTTPS: true})
		}
	}
	return proxy.Write(m.cfg.ProxyDir, rs)
}

// AutoHost is the automatic subdomain of an app under the wildcard domain
// base ("" when the app has none): <app>-<project>.<base>.
func AutoHost(a *store.App, base string) string {
	if base == "" || !a.AutoDomain || a.IsDatabase() {
		return ""
	}
	return a.Name + "-" + a.ProjectName + "." + base
}

// AutoPort is the port an app's automatic subdomain routes to.
func (m *Manager) AutoPort(ctx context.Context, a *store.App) int {
	if a.HTTPPort > 0 {
		return a.HTTPPort
	}
	port, first := 80, int64(0)
	if ds, err := m.store.Domains(ctx, a.ID); err == nil {
		for _, d := range ds { // the oldest domain, as SyncProxy does
			if first == 0 || d.ID < first {
				port, first = d.Port, d.ID
			}
		}
	}
	return port
}

// SplitCommand splits a command line like a POSIX shell would for simple
// cases: whitespace separation with single/double quotes and backslashes.
func SplitCommand(s string) ([]string, error) {
	var out []string
	var cur strings.Builder
	inArg, quote, esc := false, rune(0), false
	for _, r := range s {
		switch {
		case esc:
			cur.WriteRune(r)
			esc, inArg = false, true
		case r == '\\' && quote != '\'':
			esc = true
		case quote != 0:
			if r == quote {
				quote = 0
			} else {
				cur.WriteRune(r)
			}
		case r == '\'' || r == '"':
			quote, inArg = r, true
		case r == ' ' || r == '\t' || r == '\n':
			if inArg {
				out = append(out, cur.String())
				cur.Reset()
				inArg = false
			}
		default:
			cur.WriteRune(r)
			inArg = true
		}
	}
	if quote != 0 || esc {
		return nil, errors.New("command has an unterminated quote or escape")
	}
	if inArg {
		out = append(out, cur.String())
	}
	return out, nil
}

// isRegistryPullError reports errors caused by the registry rather than the
// build itself, such as rate limits or an unreachable registry.
func isRegistryPullError(err error) bool {
	msg := err.Error()
	for _, s := range []string{"Too Many Requests", "toomanyrequests", "failed to resolve reference", "pull access denied", "i/o timeout", "no such host"} {
		if strings.Contains(msg, s) {
			return true
		}
	}
	return false
}
