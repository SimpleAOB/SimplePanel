package api

import (
	"io"
	"log/slog"
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

func newTestServer(t *testing.T) (*httptest.Server, *http.Client) {
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	dc := docker.New(filepath.Join(t.TempDir(), "no.sock"))
	dm := deploy.NewManager(deploy.Config{Network: "simplepanel", ProxyDir: t.TempDir(), WorkDir: t.TempDir()}, st, dc, log)
	ui := fstest.MapFS{"index.html": {Data: []byte("<html>ui</html>")}}
	ts := httptest.NewServer(New(st, dc, dm, ui, log, "test").Handler())
	t.Cleanup(ts.Close)
	jar, _ := cookiejar.New(nil)
	return ts, &http.Client{Jar: jar}
}

func call(t *testing.T, c *http.Client, method, url, body string, csrf bool) (int, string) {
	t.Helper()
	req, _ := http.NewRequest(method, url, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if csrf {
		req.Header.Set(csrfHeader, "1")
	}
	res, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(b)
}

func TestAuthFlow(t *testing.T) {
	ts, c := newTestServer(t)
	u := ts.URL + "/api"

	if code, _ := call(t, c, "GET", u+"/projects", "", false); code != 401 {
		t.Fatalf("unauthenticated projects: %d", code)
	}
	if code, _ := call(t, c, "POST", u+"/setup", `{"username":"admin","password":"longenough1"}`, false); code != 403 {
		t.Fatalf("setup without CSRF header should be 403, got %d", code)
	}
	if code, _ := call(t, c, "POST", u+"/setup", `{"username":"admin","password":"short"}`, true); code != 400 {
		t.Fatalf("short password should be 400, got %d", code)
	}
	if code, b := call(t, c, "POST", u+"/setup", `{"username":"admin","password":"longenough1"}`, true); code != 201 {
		t.Fatalf("setup: %d %s", code, b)
	}
	if code, _ := call(t, c, "POST", u+"/setup", `{"username":"evil","password":"longenough1"}`, true); code != 409 {
		t.Fatalf("second setup should be 409, got %d", code)
	}
	if code, b := call(t, c, "POST", u+"/projects", `{"name":"shop"}`, true); code != 201 {
		t.Fatalf("create project: %d %s", code, b)
	}
	if code, _ := call(t, c, "POST", u+"/projects", `{"name":"Bad Name"}`, true); code != 400 {
		t.Fatalf("invalid project name should be 400, got %d", code)
	}
	if code, b := call(t, c, "POST", u+"/projects/1/apps", `{"name":"web","source":"git","gitUrl":"file:///etc"}`, true); code != 400 {
		t.Fatalf("file:// git URL should be rejected: %d %s", code, b)
	}
	if code, b := call(t, c, "POST", u+"/projects/1/apps", `{"name":"web","source":"image","image":"nginx:1.27","gitToken":"x"}`, true); code != 201 || strings.Contains(b, "webhook") {
		t.Fatalf("create app: %d %s", code, b)
	}
	if code, _ := call(t, c, "POST", u+"/apps/1/domains", `{"host":"bad host"}`, true); code != 400 {
		t.Fatal("invalid host should be rejected")
	}

	// Fresh client: wrong password, then right one.
	_, c2 := newTestServerClient(ts)
	if code, _ := call(t, c2, "POST", u+"/login", `{"username":"admin","password":"wrongwrong1"}`, true); code != 401 {
		t.Fatal("wrong password should be 401")
	}
	if code, _ := call(t, c2, "POST", u+"/login", `{"username":"admin","password":"longenough1"}`, true); code != 200 {
		t.Fatal("login should succeed")
	}
	if code, _ := call(t, c2, "POST", u+"/logout", ``, true); code != 200 {
		t.Fatal("logout failed")
	}
	if code, _ := call(t, c2, "GET", u+"/me", "", false); code != 401 {
		t.Fatal("session should be gone after logout")
	}
}

func newTestServerClient(ts *httptest.Server) (*httptest.Server, *http.Client) {
	jar, _ := cookiejar.New(nil)
	return ts, &http.Client{Jar: jar}
}

func TestSecurityHeadersAndSPA(t *testing.T) {
	ts, c := newTestServer(t)
	res, err := c.Get(ts.URL + "/p/1/a/2")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	if !strings.Contains(string(b), "ui") {
		t.Error("SPA fallback should serve index.html")
	}
	for _, h := range []string{"Content-Security-Policy", "X-Frame-Options", "X-Content-Type-Options"} {
		if res.Header.Get(h) == "" {
			t.Errorf("missing %s", h)
		}
	}
}

func TestWebhookUnknownToken(t *testing.T) {
	ts, c := newTestServer(t)
	if code, _ := call(t, c, "POST", ts.URL+"/api/hooks/deploy/"+strings.Repeat("a", 48), "", false); code != 404 {
		t.Fatalf("unknown token should be 404, got %d", code)
	}
}
