package deploy

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SimpleAOB/SimplePanel/internal/docker"
	"github.com/SimpleAOB/SimplePanel/internal/store"
)

// fakeEngine is just enough of Docker for an image deploy. It records the
// registry login sent with each pull and service change.
type fakeEngine struct {
	mu      sync.Mutex
	created bool
	pulled  []string // X-Registry-Auth of each pull
	service []string // X-Registry-Auth of service create/update
	pullErr string
}

func (f *fakeEngine) handler(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	io.Copy(io.Discard, r.Body)
	p := r.URL.Path[strings.Index(r.URL.Path[1:], "/")+1:]
	notFound := func() { w.WriteHeader(404); w.Write([]byte(`{"message":"not found"}`)) }
	switch {
	case p == "/images/create":
		f.pulled = append(f.pulled, r.Header.Get("X-Registry-Auth"))
		if f.pullErr != "" {
			w.Write([]byte(`{"error":"` + f.pullErr + `"}` + "\n"))
			return
		}
		w.Write([]byte(`{"status":"Downloaded newer image"}` + "\n"))
	case strings.HasPrefix(p, "/images/"):
		if f.pullErr != "" {
			notFound()
			return
		}
		w.Write([]byte(`{"RepoDigests":["ghcr.io/acme/app@sha256:abc"]}`))
	case strings.HasPrefix(p, "/networks/"):
		if p == "/networks/create" {
			w.Write([]byte(`{}`))
			return
		}
		notFound()
	case p == "/services/create":
		f.service = append(f.service, r.Header.Get("X-Registry-Auth"))
		f.created = true
		w.Write([]byte(`{"ID":"s1"}`))
	case strings.HasPrefix(p, "/services/"):
		if !f.created {
			notFound()
			return
		}
		w.Write([]byte(`{"ID":"s1","Version":{"Index":1}}`))
	case p == "/tasks":
		w.Write([]byte(`[{"ID":"t1","Status":{"State":"running"},"Spec":{"ContainerSpec":{"Image":"ghcr.io/acme/app@sha256:abc"}}}]`))
	default:
		notFound()
	}
}

func newEngine(t *testing.T) (*fakeEngine, *Manager, *store.Store) {
	t.Helper()
	sock := filepath.Join(t.TempDir(), "d.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeEngine{}
	srv := &httptest.Server{Listener: ln, Config: &http.Server{Handler: http.HandlerFunc(f.handler)}}
	srv.Start()
	t.Cleanup(srv.Close)
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	m := NewManager(Config{Network: "simplepanel", ProxyDir: t.TempDir(), WorkDir: t.TempDir()}, st, docker.New(sock), slog.New(slog.NewTextHandler(io.Discard, nil)))
	return f, m, st
}

func deployAndWait(t *testing.T, m *Manager, st *store.Store, appID int64) *store.Deployment {
	t.Helper()
	d, err := m.Deploy(t.Context(), appID, "test")
	if err != nil {
		t.Fatal(err)
	}
	for range 200 {
		time.Sleep(50 * time.Millisecond)
		if d, _ = st.Deployment(t.Context(), d.ID); d != nil && d.Status != "running" && d.Status != "queued" {
			return d
		}
	}
	t.Fatal("deployment did not finish")
	return nil
}

func TestPrivateImageDeploy(t *testing.T) {
	f, m, st := newEngine(t)
	ctx := t.Context()
	p, err := st.CreateProject(ctx, "acme")
	if err != nil {
		t.Fatal(err)
	}
	reg := &store.Registry{Name: "GitHub", Server: "ghcr.io", Username: "octo", Password: "ghp_s3cret_token"}
	if err := st.CreateRegistry(ctx, reg); err != nil {
		t.Fatal(err)
	}
	app := &store.App{ProjectID: p.ID, ProjectName: p.Name, Name: "web", Source: "image", Image: "ghcr.io/acme/app:1", Replicas: 1, RegistryID: reg.ID}
	if err := st.CreateApp(ctx, app); err != nil {
		t.Fatal(err)
	}

	d := deployAndWait(t, m, st, app.ID)
	if d.Status != "succeeded" {
		t.Fatalf("deploy %s:\n%s", d.Status, d.Log)
	}
	for _, h := range append(append([]string{}, f.pulled...), f.service...) {
		b, _ := base64.URLEncoding.DecodeString(h)
		var a map[string]string
		json.Unmarshal(b, &a)
		if a["username"] != "octo" || a["password"] != "ghp_s3cret_token" || a["serveraddress"] != "ghcr.io" {
			t.Errorf("login sent to Docker = %v", a)
		}
	}
	if len(f.pulled) != 1 || len(f.service) != 1 {
		t.Errorf("pulls %d, service creates %d", len(f.pulled), len(f.service))
	}
	if !strings.Contains(d.Log, "signed in to ghcr.io as octo") || strings.Contains(d.Log, "ghp_s3cret_token") {
		t.Errorf("log:\n%s", d.Log)
	}

	// The image moves to another registry: the login must not follow it.
	app.Image = "evil.example.com/acme/app:1"
	if err := st.UpdateApp(ctx, app); err != nil {
		t.Fatal(err)
	}
	d = deployAndWait(t, m, st, app.ID)
	if d.Status != "failed" || !strings.Contains(d.Log, "is for ghcr.io") || len(f.pulled) != 1 {
		t.Errorf("mismatched registry: %s, %d pulls\n%s", d.Status, len(f.pulled), d.Log)
	}

	// A token echoed in a registry error is masked in the log.
	app.Image = "ghcr.io/acme/app:2"
	st.UpdateApp(ctx, app)
	f.pullErr = "denied: token ghp_s3cret_token has no access"
	d = deployAndWait(t, m, st, app.ID)
	if d.Status != "failed" || strings.Contains(d.Log, "ghp_s3cret_token") {
		t.Errorf("failed pull: %s\n%s", d.Status, d.Log)
	}
}

func TestAnonymousPullHint(t *testing.T) {
	f, m, st := newEngine(t)
	ctx := t.Context()
	p, _ := st.CreateProject(ctx, "acme")
	app := &store.App{ProjectID: p.ID, ProjectName: p.Name, Name: "web", Source: "image", Image: "ghcr.io/acme/private:1", Replicas: 1}
	if err := st.CreateApp(ctx, app); err != nil {
		t.Fatal(err)
	}
	f.pullErr = "Head \\\"https://ghcr.io/v2/acme/private/manifests/1\\\": unauthorized"
	d := deployAndWait(t, m, st, app.ID)
	if d.Status != "failed" || !strings.Contains(d.Log, "Settings > Registries") {
		t.Errorf("%s\n%s", d.Status, d.Log)
	}
	if f.pulled[0] != "" {
		t.Errorf("an app without a login sent one: %q", f.pulled[0])
	}
}
