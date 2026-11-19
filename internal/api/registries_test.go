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
	"testing"
	"testing/fstest"

	"github.com/SimpleAOB/SimplePanel/internal/deploy"
	"github.com/SimpleAOB/SimplePanel/internal/docker"
	"github.com/SimpleAOB/SimplePanel/internal/store"
)

// newRegistryServer starts a panel whose Docker accepts registry logins
// with the password "good-token" and echoes the password back otherwise.
func newRegistryServer(t *testing.T) (string, *http.Client, *store.Store) {
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
	fd := &httptest.Server{Listener: ln, Config: &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/auth") {
			var in struct{ Username, Password, ServerAddress string }
			json.NewDecoder(r.Body).Decode(&in)
			if in.Password == "good-token" {
				w.Write([]byte(`{"Status":"Login Succeeded"}`))
				return
			}
			w.WriteHeader(401)
			json.NewEncoder(w).Encode(map[string]string{"message": "login to " + in.ServerAddress + " with " + in.Password + ": unauthorized"})
			return
		}
		w.WriteHeader(404)
		w.Write([]byte(`{"message":"not found"}`))
	})}}
	fd.Start()
	t.Cleanup(fd.Close)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	dc := docker.New(sock)
	dm := deploy.NewManager(deploy.Config{Network: "simplepanel", ProxyDir: t.TempDir(), WorkDir: t.TempDir()}, st, dc, log)
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
	return ts.URL + "/api", c, st
}

func TestRegistries(t *testing.T) {
	u, c, st := newRegistryServer(t)
	must := func(method, path, body string, want int) string {
		t.Helper()
		code, b := call(t, c, method, u+path, body, true)
		if code != want {
			t.Fatalf("%s %s: got %d want %d: %s", method, path, code, want, b)
		}
		if strings.Contains(b, "good-token") || strings.Contains(b, "bad-token") {
			t.Fatalf("%s %s leaked a token: %s", method, path, b)
		}
		return b
	}

	// Bad input, then a login the registry refuses (the token is masked).
	must("POST", "/registries", `{"name":"GitHub","server":"not a host","username":"octo","password":"good-token"}`, 400)
	must("POST", "/registries", `{"name":"GitHub","server":"ghcr.io","username":"octo"}`, 400)
	b := must("POST", "/registries", `{"name":"GitHub","server":"ghcr.io","username":"octo","password":"bad-token"}`, 400)
	if !strings.Contains(b, "checkFailed") || !strings.Contains(b, "********") {
		t.Errorf("failed check: %s", b)
	}
	must("POST", "/registries/test", `{"server":"ghcr.io","username":"octo","password":"bad-token"}`, 400)
	must("POST", "/registries/test", `{"server":"ghcr.io","username":"octo","password":"good-token"}`, 200)

	b = must("POST", "/registries", `{"name":"GitHub","server":"https://GHCR.io/","username":"octo","password":"good-token"}`, 201)
	var gh struct {
		ID     int64
		Server string
	}
	json.Unmarshal([]byte(b), &gh)
	if gh.Server != "ghcr.io" {
		t.Errorf("server = %q", gh.Server)
	}
	must("POST", "/registries", `{"name":"github","server":"ghcr.io","username":"x","password":"p","skipCheck":true}`, 409)
	b = must("POST", "/registries", `{"name":"Hub","server":"docker.io","username":"me","password":"bad-token","skipCheck":true}`, 201)
	var hub struct{ ID int64 }
	json.Unmarshal([]byte(b), &hub)
	if g, _ := st.Registry(t.Context(), gh.ID); g.Password != "good-token" {
		t.Errorf("stored token = %q", g.Password)
	}

	// An app on ghcr.io picks the only GHCR login by itself.
	b = must("POST", "/projects/1/apps", `{"name":"web","source":"image","image":"ghcr.io/acme/web:1"}`, 201)
	var app store.App
	json.Unmarshal([]byte(b), &app)
	if app.RegistryID != gh.ID {
		t.Errorf("auto login = %d, want %d", app.RegistryID, gh.ID)
	}
	// A login for another registry is refused; none is fine.
	appPath := "/apps/" + itoa(app.ID)
	must("PUT", appPath, `{"source":"image","image":"ghcr.io/acme/web:1","registryId":`+itoa(hub.ID)+`}`, 400)
	must("PUT", appPath, `{"source":"image","image":"ghcr.io/acme/web:1","registryId":999}`, 400)
	b = must("PUT", appPath, `{"source":"image","image":"ghcr.io/acme/web:1","registryId":0}`, 200)
	if strings.Contains(b, `"registryId":`+itoa(gh.ID)) {
		t.Errorf("registryId 0 should clear the login: %s", b)
	}
	must("PUT", appPath, `{"source":"image","image":"ghcr.io/acme/web:1","registryId":`+itoa(gh.ID)+`}`, 200)
	// Moving the image to Docker Hub without saying which login switches
	// to the Hub login; the GHCR one never follows the image.
	b = must("PUT", appPath, `{"source":"image","image":"acme/web:1"}`, 200)
	if !strings.Contains(b, `"registryId":`+itoa(hub.ID)) {
		t.Errorf("image moved to Docker Hub: %s", b)
	}
	b = must("GET", appPath, "", 200)
	if !strings.Contains(b, `"registry":{"id":`+itoa(hub.ID)) {
		t.Errorf("app view: %s", b)
	}
	must("PUT", appPath, `{"source":"image","image":"ghcr.io/acme/web:1"}`, 200)

	// Admins see usage; the export names the registry, never the login.
	b = must("GET", "/registries", "", 200)
	if !strings.Contains(b, `"apps":1`) || !strings.Contains(b, `"username":"octo"`) {
		t.Errorf("list: %s", b)
	}
	b = must("GET", appPath+"/export", "", 200)
	if !strings.Contains(b, `"registry":"ghcr.io"`) || strings.Contains(b, "octo") {
		t.Errorf("export: %s", b)
	}
	doc := b
	imp, _ := json.Marshal(map[string]any{"document": doc, "projectId": 1, "name": "web2"})
	b = must("POST", "/import", string(imp), 201)
	if w := `private image on ghcr.io`; strings.Contains(b, w) {
		t.Errorf("import with a matching login warned: %s", b)
	}
	if a, err := st.AppByName(t.Context(), "shop", "web2"); err != nil || a.RegistryID != gh.ID {
		t.Errorf("imported app login: %+v %v", a, err)
	}

	// Editing keeps the token unless a new one is given; changing the
	// registry needs the token again and unlinks the apps.
	must("PUT", "/registries/"+itoa(gh.ID), `{"name":"GitHub org","server":"ghcr.io","username":"octo2"}`, 200)
	if g, _ := st.Registry(t.Context(), gh.ID); g.Password != "good-token" || g.Username != "octo2" {
		t.Errorf("after edit: %+v", g)
	}
	must("PUT", "/registries/"+itoa(gh.ID), `{"name":"GitHub org","server":"evil.example.com","username":"octo2"}`, 400)
	must("POST", "/registries/test", `{"id":`+itoa(gh.ID)+`,"server":"evil.example.com","username":"octo2"}`, 400)
	must("POST", "/registries/test", `{"id":`+itoa(gh.ID)+`,"server":"ghcr.io","username":"octo2"}`, 200)

	// Deleting a login unlinks its apps.
	must("DELETE", "/registries/"+itoa(gh.ID), "", 200)
	if a, _ := st.App(t.Context(), app.ID); a.RegistryID != 0 {
		t.Errorf("app still uses a deleted login: %d", a.RegistryID)
	}
	imp, _ = json.Marshal(map[string]any{"document": doc, "projectId": 1, "name": "web3", "deploy": true})
	b = must("POST", "/import", string(imp), 201)
	if !strings.Contains(b, "private image on ghcr.io; add a login") {
		t.Errorf("import without a login: %s", b)
	}
	a3, err := st.AppByName(t.Context(), "shop", "web3")
	if err != nil {
		t.Fatal(err)
	}
	if ds, _ := st.Deployments(t.Context(), a3.ID, 10); len(ds) != 0 {
		t.Errorf("an app that needs a login was deployed on import")
	}
}

func TestRegistriesForDevelopers(t *testing.T) {
	u, c, st := newRegistryServer(t)
	if code, b := call(t, c, "POST", u+"/registries", `{"name":"GitHub","server":"ghcr.io","username":"octo","password":"good-token"}`, true); code != 201 {
		t.Fatalf("%d %s", code, b)
	}
	code, b := call(t, c, "POST", u+"/users", `{"username":"dev","role":"developer","access":{"1":"developer"}}`, true)
	if code != 201 {
		t.Fatalf("invite: %d %s", code, b)
	}
	var inv struct{ InvitePath string }
	json.Unmarshal([]byte(b), &inv)
	jar, _ := cookiejar.New(nil)
	dev := &http.Client{Jar: jar}
	if code, b := call(t, dev, "POST", u+"/invite/"+strings.TrimPrefix(inv.InvitePath, "/#/invite/"), `{"password":"developer-pass-1"}`, true); code != 200 {
		t.Fatalf("accept: %d %s", code, b)
	}
	// Developers see which logins exist (to pick one), not who they sign in as.
	code, b = call(t, dev, "GET", u+"/registries", "", false)
	if code != 200 || !strings.Contains(b, `"server":"ghcr.io"`) || strings.Contains(b, "octo") {
		t.Errorf("developer list: %d %s", code, b)
	}
	for _, r := range [][3]string{
		{"POST", "/registries", `{"name":"x","server":"ghcr.io","username":"a","password":"good-token"}`},
		{"PUT", "/registries/1", `{"name":"x","server":"ghcr.io","username":"a"}`},
		{"POST", "/registries/test", `{"id":1,"server":"ghcr.io","username":"octo"}`},
		{"DELETE", "/registries/1", ""},
	} {
		if code, _ := call(t, dev, r[0], u+r[1], r[2], true); code != 403 {
			t.Errorf("developer %s %s: %d, want 403", r[0], r[1], code)
		}
	}
	// ...but can deploy private images with them.
	code, b = call(t, dev, "POST", u+"/projects/1/apps", `{"name":"web","source":"image","image":"ghcr.io/acme/web:1","registryId":1}`, true)
	if code != 201 || !strings.Contains(b, `"registryId":1`) {
		t.Errorf("developer app: %d %s", code, b)
	}
	_ = st
}
