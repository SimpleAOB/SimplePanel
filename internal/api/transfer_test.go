package api

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestExportImportRoundTrip(t *testing.T) {
	ts, c := newTestServer(t)
	u := ts.URL + "/api"
	mustCall := func(method, path, body string, want int) string {
		t.Helper()
		code, b := call(t, c, method, u+path, body, true)
		if code != want {
			t.Fatalf("%s %s: got %d, want %d: %s", method, path, code, want, b)
		}
		return b
	}
	mustCall("POST", "/setup", `{"username":"admin","password":"longenough1"}`, 201)
	mustCall("POST", "/projects", `{"name":"shop"}`, 201)
	mustCall("POST", "/projects/1/apps", `{"name":"web","source":"git","gitUrl":"https://github.com/a/b","gitBranch":"main","gitToken":"supersecrettoken","env":"API_KEY=hunter2"}`, 201)
	mustCall("POST", "/apps/1/domains", `{"host":"shop.example.com","port":3000,"https":true}`, 201)
	// Databases are created directly in the store-backed API; deploy fails
	// without Docker but creation still succeeds.
	mustCall("POST", "/projects/1/databases", `{"name":"db","engine":"postgres"}`, 201)

	// Without env=1 the app's env is left out; tokens and DB passwords never appear.
	exp := mustCall("GET", "/projects/1/export", "", 200)
	for _, secret := range []string{"supersecrettoken", "hunter2", "POSTGRES_PASSWORD", "webhook"} {
		if strings.Contains(exp, secret) {
			t.Errorf("export leaks %q: %s", secret, exp)
		}
	}
	if !strings.Contains(exp, `"gitPrivate":true`) || !strings.Contains(exp, "shop.example.com") {
		t.Errorf("export missing expected fields: %s", exp)
	}
	withEnv := mustCall("GET", "/projects/1/export?env=1", "", 200)
	if !strings.Contains(withEnv, "hunter2") {
		t.Error("env=1 should include app env")
	}

	// Importing under the same name conflicts; under a new name it works,
	// and the domain conflict becomes a warning.
	req := func(doc, extra string) string {
		b, _ := json.Marshal(doc)
		return `{"document":` + string(b) + extra + `}`
	}
	mustCall("POST", "/import", req(withEnv, ""), 409)
	out := mustCall("POST", "/import", req(withEnv, `,"name":"shop-copy"`), 201)
	var res importResult
	json.Unmarshal([]byte(out), &res)
	if len(res.AppIDs) != 2 || len(res.Warnings) != 2 {
		t.Fatalf("unexpected import result: %s", out)
	}

	// The imported database got a fresh password.
	db1 := mustCall("GET", "/apps/2", "", 200)
	db2 := mustCall("GET", "/apps/4", "", 200)
	pw := func(s string) string {
		var v struct{ Database struct{ Password string } }
		json.Unmarshal([]byte(s), &v)
		return v.Database.Password
	}
	if pw(db1) == "" || pw(db1) == pw(db2) {
		t.Errorf("database password should be regenerated: %q vs %q", pw(db1), pw(db2))
	}

	// Single service into an existing project, renamed.
	svc := mustCall("GET", "/apps/1/export", "", 200)
	mustCall("POST", "/import", req(svc, `,"projectId":1`), 409)
	mustCall("POST", "/import", req(svc, `,"projectId":1,"name":"web2"`), 201)
}

func TestImportRejectsBadDocuments(t *testing.T) {
	ts, c := newTestServer(t)
	u := ts.URL + "/api"
	call(t, c, "POST", u+"/setup", `{"username":"admin","password":"longenough1"}`, true)
	call(t, c, "POST", u+"/projects", `{"name":"shop"}`, true)
	bad := []string{
		``,
		`not json`,
		`{"simplepanel":2,"kind":"project","project":{"name":"x","services":[]}}`,
		`{"simplepanel":1,"kind":"project","project":{"name":"x","services":[]},"extra":1}`,
		`{"simplepanel":1,"kind":"project","project":{"name":"Bad Name","services":[]}}`,
		`{"simplepanel":1,"kind":"project","project":{"name":"x","services":[{"name":"a","type":"app","source":"git","git":{"url":"file:///etc/passwd"}}]}}`,
		`{"simplepanel":1,"kind":"project","project":{"name":"x","services":[{"name":"a","type":"app","source":"image","image":"nginx; rm -rf /"}]}}`,
		`{"simplepanel":1,"kind":"project","project":{"name":"x","services":[{"name":"a","type":"app","source":"image","image":"nginx","volumes":[{"name":"d","path":"/","host":"/etc"}]}]}}`,
		`{"simplepanel":1,"kind":"project","project":{"name":"x","services":[{"name":"a","type":"app","source":"image","image":"nginx"},{"name":"a","type":"app","source":"image","image":"nginx"}]}}`,
		`{"simplepanel":1,"kind":"project","project":{"name":"x","services":[{"name":"d","type":"postgres","domains":[{"host":"a.example.com","port":80}]}]}}`,
		`{"simplepanel":1,"kind":"service"}`,
		`{"simplepanel":1,"kind":"project","project":{"name":"x","services":[]}} {}`,
	}
	for _, doc := range bad {
		b, _ := json.Marshal(doc)
		code, body := call(t, c, "POST", u+"/import", `{"document":`+string(b)+`}`, true)
		if code != 400 {
			t.Errorf("document %q: got %d (%s), want 400", doc, code, body)
		}
	}
	// Nothing was created by the rejected imports.
	_, list := call(t, c, "GET", u+"/projects", "", false)
	if strings.Count(list, `"name"`) != 1 {
		t.Errorf("rejected imports must not create projects: %s", list)
	}
}
