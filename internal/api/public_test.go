package api

import (
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/SimpleAOB/SimplePanel/internal/deploy"
	"github.com/SimpleAOB/SimplePanel/internal/docker"
	"github.com/SimpleAOB/SimplePanel/internal/store"
)

// fakeSwarm is just enough of Docker's API for public proxies: services
// can be created, inspected, updated and removed, and each one has a
// running task. Services named in "taken" publish a port already.
type fakeSwarm struct {
	mu       sync.Mutex
	services map[string]docker.ServiceSpec
	failNext string // the next task fails with this error
}

func (f *fakeSwarm) handler(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	p := r.URL.Path
	if i := strings.Index(p[1:], "/"); strings.HasPrefix(p, "/v") && i > 0 {
		p = p[i+1:]
	}
	switch {
	case p == "/_ping":
		w.Write([]byte("OK"))
	case p == "/version":
		w.Write([]byte(`{"ApiVersion":"1.45"}`))
	case strings.HasPrefix(p, "/networks"):
		w.Write([]byte(`{}`))
	case p == "/services" && r.Method == "GET":
		var out []docker.Service
		for name, spec := range f.services {
			out = append(out, docker.Service{ID: name, Spec: spec})
		}
		json.NewEncoder(w).Encode(out)
	case p == "/services/create":
		var spec docker.ServiceSpec
		json.NewDecoder(r.Body).Decode(&spec)
		f.services[spec.Name] = spec
		w.Write([]byte(`{"ID":"` + spec.Name + `"}`))
	case strings.HasPrefix(p, "/services/") && strings.HasSuffix(p, "/update"):
		var spec docker.ServiceSpec
		json.NewDecoder(r.Body).Decode(&spec)
		f.services[spec.Name] = spec
		w.Write([]byte(`{}`))
	case strings.HasPrefix(p, "/services/") && r.Method == "DELETE":
		delete(f.services, strings.TrimPrefix(p, "/services/"))
	case strings.HasPrefix(p, "/services/"):
		name := strings.TrimPrefix(p, "/services/")
		spec, ok := f.services[name]
		if !ok {
			w.WriteHeader(404)
			w.Write([]byte(`{"message":"no such service"}`))
			return
		}
		json.NewEncoder(w).Encode(docker.Service{ID: name, Spec: spec})
	case p == "/tasks":
		var flt struct{ Service []string }
		json.Unmarshal([]byte(r.URL.Query().Get("filters")), &flt)
		var out []map[string]any
		for _, s := range flt.Service {
			if _, ok := f.services[s]; !ok {
				continue
			}
			st := map[string]any{"State": "running"}
			if f.failNext != "" {
				st = map[string]any{"State": "rejected", "Err": f.failNext}
			}
			out = append(out, map[string]any{"ID": "t-" + s, "ServiceID": s, "CreatedAt": time.Now(), "DesiredState": "running", "Status": st})
		}
		json.NewEncoder(w).Encode(out)
	default:
		w.WriteHeader(404)
		w.Write([]byte(`{"message":"not found"}`))
	}
}

func newPublicServer(t *testing.T) (string, *http.Client, *store.Store, *fakeSwarm) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	sock := filepath.Join(t.TempDir(), "d.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	fs := &fakeSwarm{services: map[string]docker.ServiceSpec{
		"other_thing": {Name: "other_thing", EndpointSpec: &docker.EndpointSpec{Ports: []docker.PortSpec{{PublishedPort: 6000, TargetPort: 6000}}}},
	}}
	fd := &httptest.Server{Listener: ln, Config: &http.Server{Handler: http.HandlerFunc(fs.handler)}}
	fd.Start()
	t.Cleanup(fd.Close)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	dc := docker.New(sock)
	dm := deploy.NewManager(deploy.Config{Network: "simplepanel", ProxyDir: t.TempDir(), WorkDir: t.TempDir(), PublicImage: "ghcr.io/simpleaob/simplepanel:test"}, st, dc, log)
	ts := httptest.NewServer(New(st, dc, dm, fstest.MapFS{"index.html": {Data: []byte("ui")}}, log, "test").Handler())
	t.Cleanup(ts.Close)
	jar, _ := cookiejar.New(nil)
	c := &http.Client{Jar: jar}
	if code, b := call(t, c, "POST", ts.URL+"/api/setup", `{"username":"admin","password":"longenough1"}`, true); code != 201 {
		t.Fatalf("setup: %d %s", code, b)
	}
	if code, b := call(t, c, "POST", ts.URL+"/api/projects", `{"name":"shop"}`, true); code != 201 {
		t.Fatalf("project: %d %s", code, b)
	}
	return ts.URL + "/api", c, st, fs
}

func TestPublicAccess(t *testing.T) {
	u, c, st, fs := newPublicServer(t)
	ctx := t.Context()
	must := func(method, path, body string, want int) string {
		t.Helper()
		code, b := call(t, c, method, u+path, body, true)
		if code != want {
			t.Fatalf("%s %s: got %d want %d: %s", method, path, code, want, b)
		}
		return b
	}
	ps, _ := st.Projects(ctx)
	p := ps[0]
	weak := deploy.NewDatabaseApp(deploy.EngineByKey("postgres"), p.ID, "shop", "weak", deploy.DBCredentials{Password: "short-pw1"}, store.RandomToken)
	if err := st.CreateApp(ctx, weak); err != nil {
		t.Fatal(err)
	}
	db := deploy.NewDatabaseApp(deploy.EngineByKey("postgres"), p.ID, "shop", "db", deploy.DBCredentials{}, store.RandomToken)
	if err := st.CreateApp(ctx, db); err != nil {
		t.Fatal(err)
	}
	wpath, dpath := "/apps/"+itoa(weak.ID), "/apps/"+itoa(db.ID)

	// Off by default, with the usual port suggested.
	var info publicInfo
	json.Unmarshal([]byte(must("GET", dpath+"/public", "", 200)), &info)
	if info.Enabled || info.Suggested != 5432 || info.Weak != "" || !info.CanChange || info.ServerTLS {
		t.Fatalf("initial info: %+v", info)
	}
	json.Unmarshal([]byte(must("GET", wpath+"/public", "", 200)), &info)
	if !strings.Contains(info.Weak, "9 characters") {
		t.Fatalf("weak password not reported: %+v", info)
	}

	// A weak password, reserved, taken and malformed ports are refused.
	b := must("PUT", wpath+"/public", `{"enabled":true,"port":5432}`, 400)
	if !strings.Contains(b, "password") {
		t.Errorf("weak password: %s", b)
	}
	for port, want := range map[string]string{"22": "between 1024", "3000": "SimplePanel", "2377": "Docker Swarm", "6000": "other_thing", "70000": "between"} {
		b = must("PUT", dpath+"/public", `{"enabled":true,"port":`+port+`}`, 400)
		if !strings.Contains(b, want) {
			t.Errorf("port %s: %s", port, b)
		}
	}
	b = must("GET", dpath+"/public/port?port=6000", "", 200)
	if !strings.Contains(b, `"ok":false`) {
		t.Errorf("port check: %s", b)
	}
	must("PUT", dpath+"/public", `{"enabled":true,"port":5432,"allow":"not-an-ip"}`, 400)

	// Turn it on: a proxy service with a host-mode port and a certificate.
	b = must("PUT", dpath+"/public", `{"enabled":true,"port":5432,"allow":"203.0.113.7, 198.51.100.0/24"}`, 200)
	json.Unmarshal([]byte(b), &info)
	if !info.Enabled || info.Port != 5432 || !info.RequireTLS || len(info.Allow) != 2 || info.Fingerprint == "" || info.State != "running" {
		t.Fatalf("enabled info: %+v", info)
	}
	fp := info.Fingerprint
	spec, ok := fs.services["shop_db_public"]
	if !ok {
		t.Fatalf("no proxy service: %v", fs.services)
	}
	env := strings.Join(spec.TaskTemplate.ContainerSpec.Env, "\n")
	for _, want := range []string{"SP_PUBLIC_TARGET=shop_db:5432", "SP_PUBLIC_PROTOCOL=postgres", "SP_PUBLIC_ALLOW=203.0.113.7/32,198.51.100.0/24", "SP_PUBLIC_REQUIRE_TLS=1", "BEGIN CERTIFICATE"} {
		if !strings.Contains(env, want) {
			t.Errorf("env lacks %q:\n%s", want, env)
		}
	}
	if pp := spec.EndpointSpec.Ports; len(pp) != 1 || pp[0].PublishMode != "host" || pp[0].PublishedPort != 5432 {
		t.Errorf("ports: %+v", pp)
	}
	if spec.TaskTemplate.ContainerSpec.Args[0] != "public-proxy" || spec.TaskTemplate.Networks[0].Target != "sp_shop" {
		t.Errorf("spec: %+v", spec.TaskTemplate)
	}
	// The secrets stay out of the app's JSON, and the port is now taken.
	b = must("GET", dpath, "", 200)
	if strings.Contains(b, "PRIVATE KEY") || !strings.Contains(b, `"publicPort":5432`) {
		t.Errorf("app JSON: %s", b)
	}
	info = publicInfo{}
	json.Unmarshal([]byte(must("GET", wpath+"/public", "", 200)), &info)
	if info.Suggested != 5433 {
		t.Errorf("second suggestion = %d", info.Suggested)
	}
	if b = must("GET", dpath+"/public/cert", "", 200); !strings.HasPrefix(b, "-----BEGIN CERTIFICATE") {
		t.Errorf("cert: %s", b)
	}

	// A proxy that can't start is rolled back and explained.
	fs.failNext = "host-mode port already in use on 1 node"
	b = must("PUT", dpath+"/public", `{"enabled":true,"port":5440}`, 400)
	fs.failNext = ""
	if !strings.Contains(b, "already using that port") {
		t.Errorf("failed start: %s", b)
	}
	if got, _ := st.App(ctx, db.ID); got.PublicPort != 5432 {
		t.Errorf("port after failed change = %d", got.PublicPort)
	}

	// Off removes the proxy; the certificate is kept for next time.
	must("PUT", dpath+"/public", `{"enabled":false}`, 200)
	if _, ok := fs.services["shop_db_public"]; ok {
		t.Error("proxy still running")
	}
	b = must("PUT", dpath+"/public", `{"enabled":true,"port":15432,"requireTls":false}`, 200)
	info = publicInfo{}
	json.Unmarshal([]byte(b), &info)
	if info.Fingerprint != fp || info.RequireTLS || len(info.Allow) != 2 {
		t.Errorf("re-enabled: %+v", info)
	}

	// Deleting the database removes its proxy too.
	must("DELETE", dpath, "", 200)
	if _, ok := fs.services["shop_db_public"]; ok {
		t.Error("proxy outlived its database")
	}
}

func TestReplacePassword(t *testing.T) {
	env := strings.Join([]string{
		"DATABASE_URL=postgres://app:old-pass-1@db:5432/app",
		"OTHER_URL=postgres://app:old-pass-1@elsewhere:5432/app",
		"DB_PASSWORD=old-pass-1",
		`QUOTED="old-pass-1"`,
		"NOTE=contains old-pass-1 somewhere",
		"FULL=mysql://u:old-pass-1@shop_db:3306/x",
		"ENC=postgres://u:p%40ss@db/x",
	}, "\n")
	got, changed := replacePassword(env, "old-pass-1", "NEWPASS", []string{"db", "shop_db"})
	if !changed {
		t.Fatal("not changed")
	}
	want := strings.Join([]string{
		"DATABASE_URL=postgres://app:NEWPASS@db:5432/app",
		"OTHER_URL=postgres://app:old-pass-1@elsewhere:5432/app",
		"DB_PASSWORD=NEWPASS",
		`QUOTED="NEWPASS"`,
		"NOTE=contains old-pass-1 somewhere",
		"FULL=mysql://u:NEWPASS@shop_db:3306/x",
		"ENC=postgres://u:p%40ss@db/x",
	}, "\n")
	if got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
	got, _ = replacePassword("U=postgres://u:p%40ss@db/x", "p@ss", "N", []string{"db"})
	if got != "U=postgres://u:N@db/x" {
		t.Errorf("encoded: %s", got)
	}
}
