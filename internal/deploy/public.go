package deploy

// Public access to one-click databases. See internal/publicproxy for the
// proxy itself; this file runs it as a Swarm service per database.

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/SimpleAOB/SimplePanel/internal/docker"
	"github.com/SimpleAOB/SimplePanel/internal/publicproxy"
	"github.com/SimpleAOB/SimplePanel/internal/store"
)

// PublicProxyMemoryMB is the public proxy's memory limit. It only copies
// bytes, so it stays far below this.
const PublicProxyMemoryMB = 48

// PublicServiceName is the Swarm service of a database's public proxy. It
// has two underscores, so it can never be an app's service name.
func PublicServiceName(a *store.App) string {
	n := a.ServiceName() + "_public"
	if len(n) > 63 {
		n = fmt.Sprintf("sp_public_%d", a.ID)
	}
	return n
}

// PublicProtocol is the wire protocol the proxy speaks for engine kind.
func PublicProtocol(kind string) string {
	if kind == "mariadb" {
		return "mysql"
	}
	return kind
}

// PublicProxyTerminatesTLS reports whether the proxy (rather than the
// database) holds the TLS certificate for engine kind.
func PublicProxyTerminatesTLS(kind string) bool { return PublicProtocol(kind) != "mysql" }

// publicImage is the image the proxy runs: the panel's own.
func (m *Manager) publicImage(ctx context.Context) (string, error) {
	if m.cfg.PublicImage != "" {
		return m.cfg.PublicImage, nil
	}
	svc, err := m.docker.ServiceInspect(ctx, m.cfg.PanelSvc)
	if err != nil {
		return "", fmt.Errorf("find SimplePanel's own image: %w", err)
	}
	return svc.Spec.TaskTemplate.ContainerSpec.Image, nil
}

// PublicSpec is the Swarm spec of a's public proxy.
func (m *Manager) PublicSpec(a *store.App, image string) (docker.ServiceSpec, error) {
	e := EngineByKey(a.Kind)
	if e == nil {
		return docker.ServiceSpec{}, errors.New("only databases can be made public")
	}
	if a.PublicPort <= 0 {
		return docker.ServiceSpec{}, errors.New("public access is off")
	}
	proto := PublicProtocol(a.Kind)
	allow, err := publicproxy.ParseAllow(a.PublicAllow)
	if err != nil {
		return docker.ServiceSpec{}, err
	}
	var cidrs []string
	for _, n := range allow {
		cidrs = append(cidrs, n.String())
	}
	env := []string{
		publicproxy.EnvListen + "=:" + strconv.Itoa(a.PublicPort),
		publicproxy.EnvTarget + "=" + fmt.Sprintf("%s:%d", a.ServiceName(), e.Port),
		publicproxy.EnvProtocol + "=" + proto,
		publicproxy.EnvAllow + "=" + strings.Join(cidrs, ","),
		publicproxy.EnvRequireTLS + "=" + map[bool]string{true: "1", false: "0"}[a.PublicTLS],
		"GOMEMLIMIT=32MiB",
	}
	if PublicProxyTerminatesTLS(a.Kind) {
		if a.PublicCert == "" || a.PublicKey == "" {
			return docker.ServiceSpec{}, errors.New("the database has no certificate yet")
		}
		env = append(env, publicproxy.EnvCert+"="+a.PublicCert, publicproxy.EnvKey+"="+a.PublicKey)
	}
	labels := map[string]string{
		"simplepanel.managed":    "true",
		"simplepanel.project":    a.ProjectName,
		"simplepanel.kind":       "public-proxy",
		"simplepanel.public-for": strconv.FormatInt(a.ID, 10),
	}
	return docker.ServiceSpec{
		Name:   PublicServiceName(a),
		Labels: labels,
		TaskTemplate: docker.TaskSpec{
			ContainerSpec: docker.ContainerSpec{
				Image: image, Labels: labels, Args: []string{"public-proxy"}, Env: env,
				// The image's health check asks the panel's web server,
				// which the proxy doesn't run.
				Healthcheck: &docker.Healthcheck{Test: []string{"NONE"}},
			},
			Resources:     &docker.Resources{Limits: &docker.Limits{MemoryBytes: PublicProxyMemoryMB << 20}},
			RestartPolicy: &docker.RestartPolicy{Condition: "any", Delay: int64(5 * time.Second)},
			Networks:      []docker.NetworkAttach{{Target: ProjectNetwork(a.ProjectName)}},
			LogDriver:     &docker.LogDriver{Name: "json-file", Options: map[string]string{"max-size": "1m", "max-file": "2"}},
		},
		Mode: docker.ServiceMode{Replicated: &docker.Replicated{Replicas: 1}},
		// A host-mode port can't be held by two tasks at once, so the old
		// proxy stops before the new one starts (open public connections
		// drop; the database and its apps aren't touched). A new proxy
		// that can't start rolls back to the old one.
		UpdateConfig:   &docker.UpdateConfig{Parallelism: 1, Order: "stop-first", FailureAction: "rollback", Monitor: int64(5 * time.Second)},
		RollbackConfig: &docker.UpdateConfig{Parallelism: 1, Order: "stop-first", FailureAction: "pause"},
		EndpointSpec: &docker.EndpointSpec{
			Mode: orDefault(m.cfg.EndpointMode, "vip"),
			// Host mode, not the routing mesh: the mesh hides the
			// client's address, which the allowlist needs.
			Ports: []docker.PortSpec{{Protocol: "tcp", TargetPort: uint32(a.PublicPort), PublishedPort: uint32(a.PublicPort), PublishMode: "host"}},
		},
	}, nil
}

// publicWait is how long ApplyPublic waits for the proxy to start.
var publicWait = 25 * time.Second

// ApplyPublic starts or updates a's public proxy and waits until it runs.
func (m *Manager) ApplyPublic(ctx context.Context, a *store.App) error {
	defer m.InvalidateStatus()
	image, err := m.publicImage(ctx)
	if err != nil {
		return err
	}
	spec, err := m.PublicSpec(a, image)
	if err != nil {
		return err
	}
	m.netMu.Lock()
	err = m.docker.NetworkEnsure(ctx, ProjectNetwork(a.ProjectName), map[string]string{
		"simplepanel.managed": "true", "simplepanel.project": a.ProjectName,
	})
	m.netMu.Unlock()
	if err != nil {
		return fmt.Errorf("create project network: %w", err)
	}
	start := time.Now()
	existing, err := m.docker.ServiceInspect(ctx, spec.Name)
	switch {
	case errors.Is(err, docker.ErrNotFound):
		if _, err := m.docker.ServiceCreate(ctx, spec, nil); err != nil {
			return fmt.Errorf("start the public proxy: %w", err)
		}
	case err != nil:
		return err
	default:
		spec.TaskTemplate.ForceUpdate = existing.Spec.TaskTemplate.ForceUpdate + 1
		if err := m.docker.ServiceUpdate(ctx, existing.ID, existing.Version.Index, spec, nil, false); err != nil {
			return fmt.Errorf("update the public proxy: %w", err)
		}
	}
	return m.waitPublic(ctx, spec.Name, start)
}

// waitPublic waits for a proxy task started after since to run, and
// explains a task that fails.
func (m *Manager) waitPublic(ctx context.Context, name string, since time.Time) error {
	deadline := time.Now().Add(publicWait)
	for {
		tasks, err := m.docker.ServiceTasks(ctx, name)
		if err != nil {
			return err
		}
		for _, t := range tasks {
			if t.CreatedAt.Before(since.Add(-2 * time.Second)) {
				continue
			}
			switch t.Status.State {
			case "running":
				if t.DesiredState == "running" {
					return nil
				}
			case "failed", "rejected":
				return publicTaskError(t.Status.Err)
			}
		}
		if time.Now().After(deadline) {
			return errors.New("the public proxy didn't start in time; check the server's memory and try again")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
}

func publicTaskError(msg string) error {
	if strings.Contains(msg, "port") && (strings.Contains(msg, "in use") || strings.Contains(msg, "allocated")) {
		return errors.New("another program on the server is already using that port; choose another port")
	}
	if msg == "" {
		msg = "it stopped right after starting"
	}
	return errors.New("the public proxy couldn't start: " + msg)
}

// RemovePublic stops a's public proxy, if it has one.
func (m *Manager) RemovePublic(ctx context.Context, a *store.App) error {
	defer m.InvalidateStatus()
	return m.docker.ServiceRemove(ctx, PublicServiceName(a))
}

// PublicState reports whether a's proxy is running: "running",
// "starting", "failing" (with the error) or "missing".
func (m *Manager) PublicState(ctx context.Context, a *store.App) (string, string) {
	tasks, err := m.docker.ServiceTasks(ctx, PublicServiceName(a))
	if errors.Is(err, docker.ErrNotFound) || (err == nil && len(tasks) == 0) {
		return "missing", ""
	}
	if err != nil {
		return "unknown", err.Error()
	}
	var lastErr string
	var lastAt time.Time
	for _, t := range tasks {
		if t.DesiredState == "running" && t.Status.State == "running" {
			return "running", ""
		}
		if t.Status.Err != "" && t.CreatedAt.After(lastAt) {
			lastErr, lastAt = t.Status.Err, t.CreatedAt
		}
	}
	if lastErr != "" {
		return "failing", publicTaskError(lastErr).Error()
	}
	return "starting", ""
}

// PublicLog returns the proxy's last log lines (newest last).
func (m *Manager) PublicLog(ctx context.Context, a *store.App, lines int) ([]string, error) {
	out, err := m.docker.ServiceLogs(ctx, PublicServiceName(a), lines, 0)
	if errors.Is(err, docker.ErrNotFound) {
		return []string{}, nil
	}
	if err != nil {
		return nil, err
	}
	var keep []string
	for _, l := range strings.Split(out, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			keep = append(keep, l)
		}
	}
	return keep, nil
}

// SyncPublic makes the public proxies match the saved settings: it starts
// missing ones, moves running ones to the panel's current image (after a
// panel update) and removes proxies of databases that are no longer
// public. It retries while Docker isn't answering.
func (m *Manager) SyncPublic(ctx context.Context) {
	for attempt := 0; attempt < 30; attempt++ {
		if err := m.syncPublic(ctx); err == nil {
			return
		} else {
			m.log.Warn("syncing public database proxies", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Minute):
		}
	}
}

func (m *Manager) syncPublic(ctx context.Context) error {
	apps, err := m.store.Apps(ctx, 0)
	if err != nil {
		return err
	}
	svcs, err := m.docker.ServicesByLabel(ctx, "simplepanel.public-for")
	if err != nil {
		return err
	}
	running := map[string]*docker.Service{}
	for i := range svcs {
		running[svcs[i].Spec.Name] = &svcs[i]
	}
	want := map[string]bool{}
	var image string
	for i := range apps {
		a := &apps[i]
		if !a.IsDatabase() || a.PublicPort == 0 {
			continue
		}
		name := PublicServiceName(a)
		want[name] = true
		if image == "" {
			if image, err = m.publicImage(ctx); err != nil {
				return err
			}
		}
		spec, err := m.PublicSpec(a, image)
		if err != nil {
			m.log.Warn("public proxy settings", "service", a.ServiceName(), "err", err)
			continue
		}
		cur := running[name]
		if cur != nil && cur.Spec.TaskTemplate.ContainerSpec.Image == image && slices.Equal(cur.Spec.TaskTemplate.ContainerSpec.Env, spec.TaskTemplate.ContainerSpec.Env) {
			continue
		}
		m.log.Info("updating public database proxy", "service", name)
		if err := m.ApplyPublic(ctx, a); err != nil {
			m.log.Warn("public proxy", "service", name, "err", err)
		}
	}
	for name, svc := range running {
		if !want[name] {
			m.log.Info("removing public proxy of a database that is no longer public", "service", name)
			_ = m.docker.ServiceRemove(ctx, svc.ID)
		}
	}
	return nil
}

// PortUser says what already uses a TCP port on the server ("" if
// nothing): another app or database, another Swarm service, or a program
// listening on the host. self's own public proxy doesn't count.
func (m *Manager) PortUser(ctx context.Context, port int, self *store.App) (string, error) {
	apps, err := m.store.Apps(ctx, 0)
	if err != nil {
		return "", err
	}
	for i := range apps {
		a := &apps[i]
		if a.ID != self.ID && a.PublicPort == port {
			return fmt.Sprintf("%s/%s already uses port %d for public access", a.ProjectName, a.Name, port), nil
		}
		for _, p := range a.Ports {
			if int(p.Published) == port {
				return fmt.Sprintf("%s/%s already publishes port %d", a.ProjectName, a.Name, port), nil
			}
		}
	}
	svcs, err := m.docker.Services(ctx)
	if err != nil {
		return "", err
	}
	own := PublicServiceName(self)
	for _, s := range svcs {
		if s.Spec.Name == own || s.Spec.EndpointSpec == nil {
			continue
		}
		for _, p := range s.Spec.EndpointSpec.Ports {
			if int(p.PublishedPort) == port {
				return fmt.Sprintf("the Docker service %s already uses port %d", s.Spec.Name, port), nil
			}
		}
	}
	if self.PublicPort == port {
		return "", nil // our own proxy is the listener
	}
	if m.cfg.HostProc != "" && hostListening(m.cfg.HostProc, port) {
		return fmt.Sprintf("a program on the server already listens on port %d", port), nil
	}
	return "", nil
}

// hostListening reports whether something on the host listens on TCP
// port, from the host's /proc (PID 1 is in the host's network namespace).
func hostListening(proc string, port int) bool {
	for _, f := range []string{"1/net/tcp", "1/net/tcp6"} {
		if listensOn(filepath.Join(proc, f), port) {
			return true
		}
	}
	return false
}

func listensOn(path string, port int) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 4 || fields[3] != "0A" { // 0A: LISTEN
			continue
		}
		_, hexPort, ok := strings.Cut(fields[1], ":")
		if !ok {
			continue
		}
		if p, err := strconv.ParseUint(hexPort, 16, 16); err == nil && int(p) == port {
			return true
		}
	}
	return false
}

// SuggestPublicPort picks a free port for a: the engine's usual port, or
// the next free one above it.
func (m *Manager) SuggestPublicPort(ctx context.Context, a *store.App, reserved func(int) string) int {
	e := EngineByKey(a.Kind)
	if e == nil {
		return 0
	}
	if a.PublicPort > 0 {
		return a.PublicPort
	}
	for p := e.Port; p < e.Port+50 && p <= 65535; p++ {
		if reserved(p) != "" {
			continue
		}
		if who, err := m.PortUser(ctx, p, a); err == nil && who == "" {
			return p
		}
	}
	return e.Port
}
