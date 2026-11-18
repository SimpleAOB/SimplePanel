package api

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
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

	"github.com/SimpleAOB/SimplePanel/internal/cron"
	"github.com/SimpleAOB/SimplePanel/internal/deploy"
	"github.com/SimpleAOB/SimplePanel/internal/docker"
	"github.com/SimpleAOB/SimplePanel/internal/gitprovider"
	"github.com/SimpleAOB/SimplePanel/internal/store"
)

// fakeGitHub answers the GitHub API calls SimplePanel makes.
type fakeGitHub struct {
	mu       sync.Mutex
	pem      string
	statuses []string
	comments []string
}

func (f *fakeGitHub) handler(t *testing.T) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /app-manifests/{code}/conversions", func(w http.ResponseWriter, r *http.Request) {
		if r.PathValue("code") != "good-code" {
			http.Error(w, `{"message":"Not Found"}`, 404)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"id": 42, "slug": "simplepanel-test", "name": "SimplePanel test",
			"client_id": "Iv1.x", "client_secret": "cs", "webhook_secret": "whsec", "pem": f.pem,
			"html_url": "https://github.com/apps/simplepanel-test", "owner": map[string]string{"login": "acme"}})
	})
	app := func(h http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ey") {
				http.Error(w, `{"message":"Bad credentials"}`, 401)
				return
			}
			h(w, r)
		}
	}
	inst := func(h http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "Bearer ghs_test" {
				http.Error(w, `{"message":"Bad credentials"}`, 401)
				return
			}
			h(w, r)
		}
	}
	mux.HandleFunc("GET /repos/acme/web/installation", app(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"id":7}`))
	}))
	mux.HandleFunc("GET /app/installations", app(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`[{"id":7,"html_url":"https://github.com/settings/installations/7","account":{"login":"acme"}}]`))
	}))
	mux.HandleFunc("POST /app/installations/7/access_tokens", app(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"token": "ghs_test", "expires_at": time.Now().Add(time.Hour)})
	}))
	mux.HandleFunc("GET /installation/repositories", inst(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"total_count":1,"repositories":[{"full_name":"acme/web","default_branch":"main","private":true,"clone_url":"https://github.com/acme/web.git","html_url":"https://github.com/acme/web"}]}`))
	}))
	mux.HandleFunc("GET /repos/acme/web", inst(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"full_name":"acme/web","default_branch":"main","private":true,"clone_url":"https://github.com/acme/web.git","html_url":"https://github.com/acme/web"}`))
	}))
	mux.HandleFunc("POST /repos/acme/web/statuses/{sha}", inst(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.statuses = append(f.statuses, string(b))
		f.mu.Unlock()
		w.Write([]byte(`{}`))
	}))
	mux.HandleFunc("POST /repos/acme/web/issues/{n}/comments", inst(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.comments = append(f.comments, string(b))
		f.mu.Unlock()
		w.Write([]byte(`{"id":99}`))
	}))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		t.Logf("fake GitHub: unexpected %s %s", r.Method, r.URL.Path)
		http.Error(w, `{"message":"Not Found"}`, 404)
	})
	return mux
}

type v03 struct {
	ts  *httptest.Server
	c   *http.Client
	st  *store.Store
	gh  *fakeGitHub
	srv *Server
}

func newV03Server(t *testing.T) *v03 {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	// A Docker stand-in that knows no services.
	sock := filepath.Join(t.TempDir(), "d.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	fd := &httptest.Server{Listener: ln, Config: &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(404)
		w.Write([]byte(`{"message":"not found"}`))
	})}}
	fd.Start()
	t.Cleanup(fd.Close)
	dc := docker.New(sock)
	dm := deploy.NewManager(deploy.Config{Network: "simplepanel", ProxyDir: t.TempDir(), WorkDir: t.TempDir()}, st, dc, log)
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	gh := &fakeGitHub{pem: string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))}
	ghs := httptest.NewServer(gh.handler(t))
	t.Cleanup(ghs.Close)
	g := gitprovider.New()
	g.GitHubAPI, g.GitHubWeb = ghs.URL, "https://github.example"
	srv := New(st, dc, dm, fstest.MapFS{"index.html": {Data: []byte("ui")}}, log, "test")
	srv.UseV03(g, cron.New(st, dm, dc, nil, log))
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	jar, _ := cookiejar.New(nil)
	c := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	if code, b := call(t, c, "POST", ts.URL+"/api/setup", `{"username":"admin","password":"longenough1"}`, true); code != 201 {
		t.Fatalf("setup: %d %s", code, b)
	}
	if code, b := call(t, c, "POST", ts.URL+"/api/projects", `{"name":"shop"}`, true); code != 201 {
		t.Fatalf("project: %d %s", code, b)
	}
	return &v03{ts, c, st, gh, srv}
}

func bearer(t *testing.T, method, url, token, body string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest(method, url, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(b)
}

func TestAPITokens(t *testing.T) {
	v := newV03Server(t)
	u := v.ts.URL + "/api"
	mk := func(body string) string {
		code, b := call(t, v.c, "POST", u+"/tokens", body, true)
		if code != 201 {
			t.Fatalf("create token: %d %s", code, b)
		}
		var out struct{ Secret string }
		json.Unmarshal([]byte(b), &out)
		return out.Secret
	}
	ai := mk(`{"name":"claude","scope":"manage"}`)
	api := mk(`{"name":"ci","scope":"manage","api":true}`)

	if code, _ := bearer(t, "GET", u+"/projects", ai, ""); code != 403 {
		t.Errorf("AI-only token on REST: %d, want 403", code)
	}
	if code, _ := bearer(t, "GET", u+"/projects", "sp_nope", ""); code != 401 {
		t.Errorf("bad token: %d, want 401", code)
	}
	if code, b := bearer(t, "GET", u+"/projects", api, ""); code != 200 || !strings.Contains(b, "shop") {
		t.Errorf("API token: %d %s", code, b)
	}
	// No CSRF header needed with a bearer token.
	if code, b := bearer(t, "POST", u+"/projects/1/apps", api, `{"name":"web","source":"image","image":"nginx:1.27"}`); code != 201 {
		t.Fatalf("create app with token: %d %s", code, b)
	}
	if code, b := bearer(t, "GET", u+"/services/shop/web", api, ""); code != 200 || !strings.Contains(b, `"name":"web"`) {
		t.Errorf("service by name: %d %s", code, b)
	}
	if code, b := bearer(t, "PATCH", u+"/apps/1/env", api, `{"set":{"A":"1","B":"two words"}}`); code != 200 {
		t.Errorf("patch env: %d %s", code, b)
	}
	if code, b := bearer(t, "PATCH", u+"/apps/1/env", api, `{"set":{"A":"3"},"unset":["B"]}`); code != 200 {
		t.Errorf("patch env: %d %s", code, b)
	}
	if a, _ := v.st.App(t.Context(), 1); a.Env != "A=3\n" && a.Env != "A=3" {
		t.Errorf("env = %q", a.Env)
	}
	// Manage scope can't delete; nothing can make tokens or invite users.
	if code, _ := bearer(t, "DELETE", u+"/apps/1", api, ""); code != 403 {
		t.Errorf("delete with manage token: %d", code)
	}
	if code, _ := bearer(t, "POST", u+"/tokens", api, `{"name":"x","scope":"read"}`); code != 403 {
		t.Errorf("token creating tokens: %d", code)
	}
	if code, _ := bearer(t, "POST", u+"/users", api, `{"username":"eve","role":"admin"}`); code != 403 {
		t.Errorf("token inviting users: %d", code)
	}
	es, _ := v.st.Audit(t.Context(), 50, 0, "api")
	if len(es) == 0 || es[0].TokenName != "ci" {
		t.Errorf("audit entries for API calls: %+v", es)
	}
}

func sign(secret string, body []byte) string {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write(body)
	return "sha256=" + hex.EncodeToString(m.Sum(nil))
}

func (v *v03) hook(t *testing.T, event string, body string, secret string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest("POST", v.ts.URL+"/api/hooks/github", strings.NewReader(body))
	req.Header.Set("X-GitHub-Event", event)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Hub-Signature-256", sign(secret, []byte(body)))
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(b)
}

func TestGitHubAppFlow(t *testing.T) {
	v := newV03Server(t)
	u := v.ts.URL + "/api"

	code, b := call(t, v.c, "POST", u+"/git/github/manifest", `{"name":"SimplePanel test"}`, true)
	if code != 200 {
		t.Fatalf("manifest: %d %s", code, b)
	}
	var m struct{ URL, Manifest string }
	json.Unmarshal([]byte(b), &m)
	state := m.URL[strings.Index(m.URL, "state=")+6:]
	if !strings.HasPrefix(m.URL, "https://github.example/settings/apps/new?state=") || !strings.Contains(m.Manifest, `"pull_requests":"write"`) {
		t.Fatalf("manifest response: %s", b)
	}
	// A wrong state is refused; the right one (once) creates the provider.
	if code, _ := call(t, http.DefaultClient, "GET", u+"/git/github/callback?state=nope&code=good-code", "", false); code != 200 {
		// (redirects are followed by the default client to the SPA)
		t.Fatalf("bad state: %d", code)
	}
	if ps, _ := v.st.GitProviders(t.Context()); len(ps) != 0 {
		t.Fatal("provider created with a bad state")
	}
	req, _ := http.NewRequest("GET", u+"/git/github/callback?state="+state+"&code=good-code", nil)
	res, err := v.c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 303 || res.Header.Get("Location") != "https://github.example/apps/simplepanel-test/installations/new" {
		t.Fatalf("callback: %d %s", res.StatusCode, res.Header.Get("Location"))
	}
	ps, _ := v.st.GitProviders(t.Context())
	if len(ps) != 1 || ps[0].AppID != 42 || ps[0].WebhookSecret != "whsec" {
		t.Fatalf("providers: %+v", ps)
	}
	code, b = call(t, v.c, "GET", u+"/git/providers", "", false)
	if code != 200 || strings.Contains(b, "whsec") || strings.Contains(b, "PRIVATE") {
		t.Fatalf("providers leak secrets: %s", b)
	}
	if code, b := call(t, v.c, "GET", u+"/git/providers/1/repos", "", false); code != 200 || !strings.Contains(b, "acme/web") {
		t.Fatalf("repos: %d %s", code, b)
	}

	// Link an app to the repository.
	code, b = call(t, v.c, "POST", u+"/projects/1/apps", `{"name":"web","source":"git","gitUrl":"https://github.com/x/y","gitProviderId":1,"gitRepo":"acme/web"}`, true)
	if code != 201 || !strings.Contains(b, `"gitRepo":"acme/web"`) || !strings.Contains(b, `"gitUrl":"https://github.example/acme/web.git"`) {
		t.Fatalf("linked app: %d %s", code, b)
	}
	// Bad signature.
	if code, _ := v.hook(t, "push", `{"ref":"refs/heads/main"}`, "wrong"); code != 401 {
		t.Fatalf("bad signature: %d", code)
	}
	push := `{"ref":"refs/heads/main","after":"0123456789abcdef","repository":{"full_name":"acme/web","default_branch":"main"},"head_commit":{"message":"fix"}}`
	if code, b := v.hook(t, "push", push, "whsec"); code != 200 || !strings.Contains(b, "deploying") {
		t.Fatalf("push: %d %s", code, b)
	}
	other := strings.Replace(push, "refs/heads/main", "refs/heads/dev", 1)
	if code, b := v.hook(t, "push", other, "whsec"); code != 200 || !strings.Contains(b, "ignored") {
		t.Fatalf("push to other branch: %d %s", code, b)
	}
	// Turn auto deploy off: pushes are skipped.
	if code, b := call(t, v.c, "PUT", u+"/apps/1", `{"source":"git","gitUrl":"https://github.example/acme/web.git","autoDeploy":false,"previews":true}`, true); code != 200 {
		t.Fatalf("update: %d %s", code, b)
	}
	if code, b := v.hook(t, "push", push, "whsec"); code != 200 || !strings.Contains(b, "automatic deploys are off") {
		t.Fatalf("push with auto deploy off: %d %s", code, b)
	}
	a, _ := v.st.App(t.Context(), 1)
	if a.GitProviderID != 1 || a.GitRepo != "acme/web" {
		t.Fatalf("PUT without provider fields unlinked the app: %+v", a)
	}

	// Pull request previews.
	pr := func(action, headRepo string) string {
		return `{"action":"` + action + `","number":7,"pull_request":{"title":"New header","html_url":"https://github.com/acme/web/pull/7",
			"head":{"ref":"feature/header","sha":"abcdef0123","repo":{"full_name":"` + headRepo + `"}},"base":{"ref":"main"}},
			"repository":{"full_name":"acme/web","default_branch":"main"}}`
	}
	if code, b := v.hook(t, "pull_request", pr("opened", "evil/web"), "whsec"); code != 200 || !strings.Contains(b, "forks") {
		t.Fatalf("fork PR: %d %s", code, b)
	}
	if code, b := v.hook(t, "pull_request", pr("opened", "acme/web"), "whsec"); code != 200 || !strings.Contains(b, "deploying preview web-pr-7") {
		t.Fatalf("PR opened: %d %s", code, b)
	}
	p, err := v.st.Preview(t.Context(), 1, 7)
	if err != nil || p.GitBranch != "feature/header" || p.Name != "web-pr-7" || len(p.Ports) != 0 || p.Replicas != 1 {
		t.Fatalf("preview: %+v %v", p, err)
	}
	code, b = call(t, v.c, "GET", u+"/apps/1/previews", "", false)
	if code != 200 || !strings.Contains(b, `"prNumber":7`) {
		t.Fatalf("list previews: %d %s", code, b)
	}
	if code, _ := call(t, v.c, "PUT", u+"/apps/"+itoa(p.ID), `{"source":"git","gitUrl":"https://github.example/acme/web.git"}`, true); code != 409 {
		t.Fatalf("editing a preview should be refused: %d", code)
	}
	if code, b := v.hook(t, "pull_request", pr("closed", "acme/web"), "whsec"); code != 200 || !strings.Contains(b, "removed") {
		t.Fatalf("PR closed: %d %s", code, b)
	}
	if _, err := v.st.Preview(t.Context(), 1, 7); err == nil {
		t.Fatal("preview still exists after the PR closed")
	}

	// Commit statuses go to GitHub with the installation token.
	v.srv.GitOnDeploy(a, &store.Deployment{ID: 1, Status: "succeeded", CommitSHA: "0123456789abcdef"})
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		v.gh.mu.Lock()
		n := len(v.gh.statuses)
		v.gh.mu.Unlock()
		if n > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	v.gh.mu.Lock()
	defer v.gh.mu.Unlock()
	if len(v.gh.statuses) == 0 || !strings.Contains(v.gh.statuses[0], `"state":"success"`) || !strings.Contains(v.gh.statuses[0], "SimplePanel: shop/web") {
		t.Fatalf("statuses: %v", v.gh.statuses)
	}
}

func TestAppWebhookPreviewsAndDeployKey(t *testing.T) {
	v := newV03Server(t)
	u := v.ts.URL + "/api"
	if code, b := call(t, v.c, "POST", u+"/projects/1/apps", `{"name":"api","source":"git","gitUrl":"git@github.com:acme/api.git","previews":true}`, true); code != 201 {
		t.Fatalf("ssh app: %d %s", code, b)
	}
	code, b := call(t, v.c, "POST", u+"/apps/1/deploy-key", "", true)
	if code != 200 || !strings.Contains(b, "ssh-ed25519 ") || !strings.Contains(b, "github.com/acme/api/settings/keys/new") {
		t.Fatalf("deploy key: %d %s", code, b)
	}
	code, b = call(t, v.c, "GET", u+"/apps/1", "", false)
	if strings.Contains(b, "PRIVATE KEY") || !strings.Contains(b, "deployKeyPub") {
		t.Fatalf("app leaks the private key: %s", b)
	}
	a, _ := v.st.App(t.Context(), 1)
	// The app's own webhook URL handles GitHub pull request events too.
	body := `{"action":"opened","number":3,"pull_request":{"title":"x","head":{"ref":"fix","sha":"aa","repo":{"full_name":"acme/api"}},"base":{"ref":"main"}},"repository":{"full_name":"acme/api","default_branch":"main"}}`
	req, _ := http.NewRequest("POST", v.ts.URL+"/api/hooks/deploy/"+a.WebhookToken, strings.NewReader(body))
	req.Header.Set("X-GitHub-Event", "pull_request")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	rb, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if !strings.Contains(string(rb), "deploying preview api-pr-3") {
		t.Fatalf("webhook PR: %s", rb)
	}
	p, err := v.st.Preview(t.Context(), 1, 3)
	if err != nil || p.DeployKey == "" {
		t.Fatalf("preview should reuse the deploy key: %v", err)
	}
	// Deleting the app removes its previews.
	if code, b := call(t, v.c, "DELETE", u+"/apps/1", "", true); code != 200 {
		t.Fatalf("delete: %d %s", code, b)
	}
	if _, err := v.st.Preview(t.Context(), 1, 3); err == nil {
		t.Fatal("preview survived its app")
	}
}

func TestCronJobsAPI(t *testing.T) {
	v := newV03Server(t)
	u := v.ts.URL + "/api"
	call(t, v.c, "POST", u+"/projects/1/apps", `{"name":"web","source":"image","image":"nginx:1.27"}`, true)
	if code, _ := call(t, v.c, "POST", u+"/apps/1/cron", `{"name":"x","schedule":"61 * * * *","command":"true"}`, true); code != 400 {
		t.Errorf("bad schedule: %d", code)
	}
	if code, _ := call(t, v.c, "POST", u+"/apps/1/cron", `{"name":"x","schedule":"@daily","command":""}`, true); code != 400 {
		t.Errorf("empty command: %d", code)
	}
	code, b := call(t, v.c, "POST", u+"/apps/1/cron", `{"name":"Clear cache","schedule":"*/15 * * * *","command":"php artisan cache:clear"}`, true)
	if code != 201 || !strings.Contains(b, `"nextRunAt":`) || !strings.Contains(b, `"timeout":3600`) {
		t.Fatalf("create cron: %d %s", code, b)
	}
	if code, b := call(t, v.c, "PUT", u+"/cron/1", `{"name":"Clear cache","schedule":"@hourly","command":"php artisan cache:clear","enabled":false}`, true); code != 200 || !strings.Contains(b, `"nextRunAt":null`) {
		t.Fatalf("disable cron: %d %s", code, b)
	}
	code, b = call(t, v.c, "POST", u+"/cron/1/run", "", true)
	if code != 202 {
		t.Fatalf("run: %d %s", code, b)
	}
	// Docker isn't there, so the run fails, and the failure is recorded.
	var runs []store.CronRun
	for i := 0; i < 100; i++ {
		_, b = call(t, v.c, "GET", u+"/cron/1/runs", "", false)
		json.Unmarshal([]byte(b), &runs)
		if len(runs) == 1 && runs[0].Status != "running" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(runs) != 1 || runs[0].Status != "failed" {
		t.Fatalf("runs: %s", b)
	}
	if code, b := call(t, v.c, "GET", u+"/cron-runs/"+itoa(runs[0].ID), "", false); code != 200 || !strings.Contains(b, "output") {
		t.Fatalf("run detail: %d %s", code, b)
	}
	if code, _ := call(t, v.c, "DELETE", u+"/cron/1", "", true); code != 200 {
		t.Fatalf("delete cron: %d", code)
	}
}
