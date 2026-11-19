// Package deploy turns an app's desired state into a running Swarm service.
package deploy

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
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
	runsMu   sync.Mutex // serialises launch bookkeeping (see runs.go)

	// OnDeploy, if set, is called when a deployment finishes.
	OnDeploy func(app *store.App, d *store.Deployment)
	// OnCommit, if set, is called once a Git build knows its commit.
	OnCommit func(app *store.App, depID int64, sha string)
	// GitAuth returns a clone token for apps linked to a Git provider.
	GitAuth func(ctx context.Context, app *store.App) (string, error)
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

func (l *logBuf) addSecret(s string) {
	l.mu.Lock()
	l.secrets = append(l.secrets, s)
	l.mu.Unlock()
}

func (l *logBuf) Printf(format string, a ...any) { fmt.Fprintf(l, "==> "+format+"\n", a...) }

func (l *logBuf) String() string {
	l.mu.Lock()
	s := l.buf.String()
	secrets := l.secrets
	l.mu.Unlock()
	for _, sec := range secrets {
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
		if app.IsPreview() {
			lb.Printf("Preview of pull request #%d: %s", app.PRNumber, app.PRTitle)
		}
		var auth *docker.RegistryAuth
		switch app.Source {
		case "image":
			if auth, err = m.RegistryAuth(ctx, app); err != nil {
				return err
			}
			if auth != nil {
				lb.addSecret(auth.Password)
			}
			image, err = m.pullImage(ctx, app, auth, lb)
		case "git":
			image, err = m.buildGit(ctx, app, depID, lb)
		default:
			err = fmt.Errorf("unknown source %q", app.Source)
		}
		if err != nil {
			return err
		}
		m.detectPort(ctx, app, image, lb)
		if err := m.applyService(ctx, app, image, auth, lb); err != nil {
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

// FailureReason is the reason a deployment log ends with ("" if none).
func FailureReason(log string) string {
	const marker = "==> Deployment failed: "
	i := strings.LastIndex(log, marker)
	if i < 0 {
		return ""
	}
	r := log[i+len(marker):]
	if j := strings.IndexByte(r, '\n'); j >= 0 {
		r = r[:j]
	}
	return strings.TrimSpace(r)
}

var ansiRe = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]`)

// StripANSI removes terminal colour codes.
func StripANSI(s string) string { return ansiRe.ReplaceAllString(s, "") }

// LogTail returns the last n meaningful lines of a build log before its
// "Deployment failed" line, skipping download progress, for showing next to
// the error.
func LogTail(log string, n int) []string {
	lines := strings.Split(StripANSI(log), "\n")
	var out []string
	for i := len(lines) - 1; i >= 0 && len(out) < n; i-- {
		l := strings.TrimSpace(lines[i])
		low := strings.ToLower(l)
		if l == "" || strings.HasPrefix(l, "==> Deployment failed") ||
			strings.Contains(low, "pulling fs layer") || strings.Contains(low, "download complete") ||
			strings.Contains(low, "extracting") || strings.Contains(low, "pull complete") ||
			strings.Contains(low, "waiting") || strings.Contains(low, "verifying checksum") || strings.HasPrefix(low, "downloading") {
			continue
		}
		out = append(out, l)
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

// PickPort chooses the port web traffic should go to from the ports an
// image exposes: a common web port if there is one, else the lowest.
func PickPort(ports []int) int {
	for _, want := range []int{80, 8080, 3000, 8000, 5000} {
		for _, p := range ports {
			if p == want {
				return p
			}
		}
	}
	if len(ports) > 0 {
		return ports[0]
	}
	return 0
}

// detectPort records the port the image listens on, so domains set to
// "automatic" route to it, and warns about domains pointing elsewhere.
func (m *Manager) detectPort(ctx context.Context, app *store.App, image string, lb *logBuf) {
	if app.IsDatabase() {
		return
	}
	ports, err := m.docker.ImageExposedPorts(ctx, image)
	if err != nil {
		return
	}
	p := PickPort(ports)
	if p != app.ImagePort {
		if err := m.store.SetImagePort(context.Background(), app.ID, p); err != nil {
			m.log.Warn("saving image port", "app", app.ServiceName(), "err", err)
		}
		app.ImagePort = p
	}
	list := make([]string, len(ports))
	for i, n := range ports {
		list[i] = strconv.Itoa(n)
	}
	if len(ports) > 0 {
		lb.Printf("The image listens on port %s", strings.Join(list, ", "))
	}
	domains, _ := m.store.Domains(ctx, app.ID)
	for _, d := range domains {
		switch {
		case d.Port == 0 && p == 0:
			lb.Printf("Note: the image doesn't say which port it listens on, so %s goes to port 80. If the site doesn't load, set the app port on the domain.", d.Host)
		case d.Port > 0 && len(ports) > 0 && !slices.Contains(ports, d.Port):
			lb.Printf("Warning: %s sends visitors to port %d, but the image listens on %s. If the site doesn't load, change the domain's port on the app's Overview.", d.Host, d.Port, strings.Join(list, ", "))
		}
	}
}

// RegistryAuth returns the registry login an image app pulls with, or nil
// when it has none. A login is only ever sent to its own registry: if the
// image moved to another registry, the deploy stops instead.
func (m *Manager) RegistryAuth(ctx context.Context, app *store.App) (*docker.RegistryAuth, error) {
	if app.RegistryID == 0 || app.Source != "image" {
		return nil, nil
	}
	reg, err := m.store.Registry(ctx, app.RegistryID)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if host := docker.RegistryHost(app.Image); host != reg.Server {
		return nil, fmt.Errorf("the registry login %q is for %s, but %s is on %s; choose another login in the app's settings", reg.Name, reg.Server, app.Image, host)
	}
	return &docker.RegistryAuth{Server: reg.Server, Username: reg.Username, Password: reg.Password}, nil
}

// buildRegistries returns the saved logins a build may use to pull private
// base images, one per registry (Docker sends each only to its registry).
func (m *Manager) buildRegistries(ctx context.Context) ([]docker.RegistryAuth, error) {
	regs, err := m.store.Registries(ctx)
	if err != nil {
		return nil, err
	}
	var out []docker.RegistryAuth
	seen := map[string]bool{}
	for _, r := range regs {
		if seen[r.Server] {
			continue
		}
		seen[r.Server] = true
		out = append(out, docker.RegistryAuth{Server: r.Server, Username: r.Username, Password: r.Password})
	}
	return out, nil
}

func (m *Manager) pullImage(ctx context.Context, app *store.App, auth *docker.RegistryAuth, lb *logBuf) (string, error) {
	if auth != nil {
		lb.Printf("Pulling %s (signed in to %s as %s)", app.Image, auth.Server, auth.Username)
	} else {
		lb.Printf("Pulling %s", app.Image)
	}
	if err := m.docker.ImagePull(ctx, app.Image, auth, lb); err != nil {
		if auth == nil && isAuthError(err) {
			lb.Printf("The registry refused an anonymous pull. If this image is private, add a login under Settings > Registries and choose it in the app's settings.")
		}
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
	opts := cloneOpts{URL: app.GitURL, Branch: app.GitBranch, Token: app.GitToken, DeployKey: app.DeployKey, KnownHosts: app.SSHKnownHosts}
	if app.GitProviderID != 0 && m.GitAuth != nil && !IsSSHURL(app.GitURL) {
		tok, err := m.GitAuth(ctx, app)
		if err != nil {
			return "", fmt.Errorf("getting a clone token from the Git provider: %w", err)
		}
		if tok != "" {
			opts.Token = tok
			lb.addSecret(tok)
		}
	}
	kh, err := gitClone(ctx, opts, repo, filepath.Join(dir, "home"), lb)
	if kh != "" && kh != app.SSHKnownHosts {
		// Remember host keys seen for the first time (trust on first use).
		if err := m.store.SetKnownHosts(context.Background(), app.ID, kh); err != nil {
			m.log.Warn("saving SSH known hosts", "app", app.ServiceName(), "err", err)
		}
	}
	if err != nil {
		return "", err
	}
	if out, err := exec.CommandContext(ctx, "git", "-C", repo, "log", "-1", "--format=%H%x00%s").Output(); err == nil {
		sha, msg, _ := strings.Cut(strings.TrimSpace(string(out)), "\x00")
		lb.Printf("Commit %.7s %s", sha, msg)
		_ = m.store.SetDeploymentCommit(context.Background(), depID, sha, msg)
		if m.OnCommit != nil {
			m.OnCommit(app, depID, sha)
		}
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
			if err := m.autoDetect(app, ctxDir, dockerfile, lb); err != nil {
				return "", err
			}
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
	regs, err := m.buildRegistries(ctx)
	if err != nil {
		return "", err
	}
	for _, r := range regs {
		lb.addSecret(r.Password)
	}
	build := func(noPull bool) error {
		pr, pw := io.Pipe()
		go func() { pw.CloseWithError(tarDir(ctxDir, pw)) }()
		err := m.docker.ImageBuild(ctx, pr, docker.BuildOptions{Tag: tag, Dockerfile: dockerfile, NoPull: noPull, Registries: regs}, lb)
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

// autoDetect handles a repository with no Dockerfile in automatic build
// mode: site generators get their build settings filled in (and saved, so
// they show in the app's settings), and server apps stop with an
// explanation instead of having their source files served as a website.
func (m *Manager) autoDetect(app *store.App, ctxDir, dockerfile string, lb *logBuf) error {
	if app.BuildCommand != "" || app.OutputDir != "" {
		lb.Printf("No %s found: building a static site", dockerfile)
		return nil
	}
	d := DetectProject(ctxDir)
	switch d.Kind {
	case "server":
		return fmt.Errorf("this looks like a %s app, not a static website (%s). Apps that run a server need a Dockerfile: add one to the repository (examples: %s). If it really is a static site, set the build type to Static site in the app's settings", d.Name, d.Reason, DockerfileHelpURL)
	case "static":
		app.BuildCommand, app.OutputDir, app.BuildImage, app.SPA = d.BuildCommand, d.OutputDir, d.BuildImage, d.SPA
		lb.Printf("No %s found. This looks like a %s site: building it with %q and serving %s/ (change this in the app's settings)", dockerfile, d.Name, d.BuildCommand, d.OutputDir)
		if err := m.store.SetStaticBuild(context.Background(), app.ID, d.BuildCommand, d.OutputDir, d.BuildImage, d.SPA); err != nil {
			m.log.Warn("saving detected build settings", "app", app.ServiceName(), "err", err)
		}
	default:
		lb.Printf("No %s found: serving the files as a static website", dockerfile)
	}
	return nil
}

func orDefault(s, d string) string {
	if s == "" {
		return d
	}
	return s
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

// applyService creates or updates the app's service. auth (nil for none)
// is handed to Swarm so every node can pull the image.
func (m *Manager) applyService(ctx context.Context, app *store.App, image string, auth *docker.RegistryAuth, lb *logBuf) error {
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
		if _, err := m.docker.ServiceCreate(ctx, spec, auth); err != nil {
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
		if err := m.docker.ServiceUpdate(ctx, existing.ID, existing.Version.Index, spec, auth, false); err != nil {
			return fmt.Errorf("update service: %w", err)
		}
	}
	if app.Replicas == 0 {
		return nil
	}
	return m.waitHealthy(ctx, spec.Name, image, lb)
}

// stableFor is how long a new service's container must stay up before a
// deploy counts as successful.
var stableFor = 10 * time.Second

// waitHealthy waits until the update completes or a task with the new image
// is running, and reports rollbacks as failures.
func (m *Manager) waitHealthy(ctx context.Context, name, image string, lb *logBuf) error {
	deadline := time.Now().Add(5 * time.Minute)
	lastMsg := ""
	// A crash-looping container is briefly "running" between restarts, so
	// a fresh service only counts as started once a task has stayed up for
	// stableFor with no new failures.
	var runningSince time.Time
	lastFailed := 0
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
		failed, running := 0, false
		for _, t := range tasks {
			if t.Spec.ContainerSpec.Image != image {
				continue
			}
			switch t.Status.State {
			case "running":
				running = true
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
		switch {
		case failed > lastFailed || !running:
			runningSince = time.Time{}
		case runningSince.IsZero():
			runningSince = time.Now()
			lb.Printf("Container started; checking that it stays up")
		case time.Since(runningSince) >= stableFor:
			return nil
		}
		lastFailed = failed
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
	return m.docker.ServiceUpdate(ctx, svc.ID, svc.Version.Index, svc.Spec, nil, true)
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
	return m.docker.ServiceUpdate(ctx, svc.ID, svc.Version.Index, svc.Spec, nil, true)
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
	if m.cfg.PanelSvc != "" {
		// Lets the panel check that a domain's DNS points at this server.
		rs = append(rs, proxy.Route{Key: "check", Path: CheckPath, Service: m.cfg.PanelSvc, Port: m.cfg.PanelPort})
	}
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
				port = a.RoutePort(0)
			}
			rs = append(rs, proxy.Route{Key: fmt.Sprintf("a%d", a.ID), Host: host, Service: a.ServiceName(), Port: port, HTTPS: true})
		}
	}
	return proxy.Write(m.cfg.ProxyDir, rs)
}

// CheckPath is served by the panel on every domain over plain HTTP, so a
// DNS check can tell whether a domain reaches this server.
const CheckPath = "/.well-known/simplepanel-check/"

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
	port, first := a.RoutePort(0), int64(0)
	if ds, err := m.store.Domains(ctx, a.ID); err == nil {
		for _, d := range ds { // the oldest domain, as SyncProxy does
			if first == 0 || d.ID < first {
				port, first = a.RoutePort(d.Port), d.ID
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

// isAuthError reports a registry refusing a pull for lack of a login.
func isAuthError(err error) bool {
	msg := strings.ToLower(err.Error())
	for _, s := range []string{"unauthorized", "denied", "authentication required", "no basic auth credentials"} {
		if strings.Contains(msg, s) {
			return true
		}
	}
	return false
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
