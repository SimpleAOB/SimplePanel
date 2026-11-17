package api

import (
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"strings"
	"testing"
)

func TestRolesAndInvites(t *testing.T) {
	ts, owner := newTestServer(t)
	u := ts.URL + "/api"
	mustCall := func(c *http.Client, method, path, body string, want int) string {
		t.Helper()
		code, b := call(t, c, method, u+path, body, true)
		if code != want {
			t.Fatalf("%s %s: got %d want %d: %s", method, path, code, want, b)
		}
		return b
	}
	mustCall(owner, "POST", "/setup", `{"username":"boss","password":"longenough1"}`, 201)
	var alpha, beta struct{ ID int64 }
	_ = json.Unmarshal([]byte(mustCall(owner, "POST", "/projects", `{"name":"alpha"}`, 201)), &alpha)
	_ = json.Unmarshal([]byte(mustCall(owner, "POST", "/projects", `{"name":"beta"}`, 201)), &beta)
	mustCall(owner, "POST", "/projects/"+itoa(alpha.ID)+"/apps", `{"name":"web","source":"image","image":"nginx:1.27"}`, 201)

	// Bad input is refused.
	mustCall(owner, "POST", "/users", `{"username":"x","role":"owner","allProjects":true}`, 400)
	mustCall(owner, "POST", "/users", `{"username":"x","role":"developer"}`, 400) // no projects
	mustCall(owner, "POST", "/users", `{"username":"x","role":"developer","access":{"999":"developer"}}`, 400)

	// Invite a developer limited to alpha.
	b := mustCall(owner, "POST", "/users", `{"username":"Dev","role":"developer","access":{"`+itoa(alpha.ID)+`":"developer"}}`, 201)
	var inv struct {
		InvitePath string
		User       struct{ ID int64 }
	}
	_ = json.Unmarshal([]byte(b), &inv)
	tok := strings.TrimPrefix(inv.InvitePath, "/#/invite/")
	if tok == "" || tok == inv.InvitePath {
		t.Fatalf("invite path %q", inv.InvitePath)
	}
	mustCall(owner, "POST", "/users", `{"username":"dev","role":"viewer","allProjects":true}`, 409)

	jar, _ := cookiejar.New(nil)
	dev := &http.Client{Jar: jar}
	// A pending user can't sign in until the invite is used.
	mustCall(dev, "POST", "/login", `{"username":"dev","password":""}`, 401)
	if b := mustCall(dev, "GET", "/invite/"+tok, "", 200); !strings.Contains(b, `"username":"dev"`) {
		t.Fatalf("invite info: %s", b)
	}
	mustCall(dev, "POST", "/invite/"+tok, `{"password":"short"}`, 400)
	mustCall(dev, "POST", "/invite/"+tok, `{"password":"developer-pass-1"}`, 200)
	mustCall(dev, "POST", "/invite/"+tok, `{"password":"developer-pass-2"}`, 404) // one-time

	// The developer sees only alpha.
	b = mustCall(dev, "GET", "/projects", "", 200)
	if !strings.Contains(b, `"alpha"`) || strings.Contains(b, `"beta"`) {
		t.Fatalf("developer projects: %s", b)
	}
	mustCall(dev, "GET", "/projects/"+itoa(beta.ID)+"/apps", "", 404)
	mustCall(dev, "POST", "/projects", `{"name":"gamma"}`, 403)
	mustCall(dev, "DELETE", "/projects/"+itoa(alpha.ID), "", 403)
	mustCall(dev, "GET", "/users", "", 403)
	mustCall(dev, "PUT", "/settings", `{"wildcardDomain":"apps.example.com"}`, 403)
	mustCall(dev, "GET", "/notifications", "", 403)
	mustCall(dev, "POST", "/projects/"+itoa(alpha.ID)+"/apps", `{"name":"api","source":"image","image":"nginx:1.27"}`, 201)

	// Admins can't change the owner or themselves.
	var users []struct {
		ID       int64
		Username string
	}
	_ = json.Unmarshal([]byte(mustCall(owner, "GET", "/users", "", 200)), &users)
	var ownerID int64
	for _, x := range users {
		if x.Username == "boss" {
			ownerID = x.ID
		}
	}
	mustCall(owner, "PUT", "/users/"+itoa(ownerID), `{"role":"viewer","allProjects":true}`, 403)

	// Demote to viewer: deploy-level actions stop working.
	mustCall(owner, "PUT", "/users/"+itoa(inv.User.ID), `{"role":"viewer","access":{"`+itoa(alpha.ID)+`":"viewer"}}`, 200)
	mustCall(dev, "POST", "/projects/"+itoa(alpha.ID)+"/apps", `{"name":"api2","source":"image","image":"nginx:1.27"}`, 403)

	// A full-scope token can't exceed its owner's role over MCP.
	b = mustCall(dev, "POST", "/tokens", `{"name":"ai","scope":"full"}`, 201)
	var tk struct{ Secret string }
	_ = json.Unmarshal([]byte(b), &tk)
	callTool := func(name, args string) (bool, string) {
		_, out := mcpPost(t, ts.URL, tk.Secret, `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"`+name+`","arguments":`+args+`}}`, nil)
		r := out["result"].(map[string]any)
		isErr, _ := r["isError"].(bool)
		return isErr, r["content"].([]any)[0].(map[string]any)["text"].(string)
	}
	if isErr, txt := callTool("list_projects", `{}`); isErr || strings.Contains(txt, "beta") || !strings.Contains(txt, "alpha") {
		t.Fatalf("list_projects as viewer: %s", txt)
	}
	if isErr, txt := callTool("delete_project", `{"project":"alpha","confirm":"alpha"}`); !isErr || !strings.Contains(txt, "role") {
		t.Fatalf("viewer deleted a project: %s", txt)
	}
	if isErr, txt := callTool("get_service", `{"service":"beta/x"}`); !isErr || !strings.Contains(txt, "not found") {
		t.Fatalf("hidden project: %s", txt)
	}
	if isErr, txt := callTool("get_metrics", `{"service":"alpha/web"}`); isErr || !strings.Contains(txt, `"samples": 0`) {
		t.Fatalf("get_metrics: %s", txt)
	}
	if isErr, txt := callTool("backup_now", `{"service":"alpha/web"}`); !isErr {
		t.Fatalf("viewer started a backup: %s", txt)
	}

	// The developer's audit view holds only their own entries.
	b = mustCall(dev, "GET", "/audit", "", 200)
	if strings.Contains(b, `"username":"boss"`) {
		t.Fatalf("audit leaks other users: %s", b)
	}
	if b = mustCall(owner, "GET", "/audit?source=ui", "", 200); !strings.Contains(b, "dev") {
		t.Fatalf("owner audit missing UI activity: %s", b)
	}

	// Removing the user ends their session.
	mustCall(owner, "DELETE", "/users/"+itoa(inv.User.ID), "", 200)
	mustCall(dev, "GET", "/projects", "", 401)
}

func TestNotificationSecretsMasked(t *testing.T) {
	ts, c := newTestServer(t)
	u := ts.URL + "/api"
	if code, b := call(t, c, "POST", u+"/setup", `{"username":"boss","password":"longenough1"}`, true); code != 201 {
		t.Fatalf("setup: %d %s", code, b)
	}
	code, b := call(t, c, "POST", u+"/notifications", `{"name":"hook","type":"webhook","config":{"url":"https://hooks.example.com/abc/secret-path","secret":"s3cret"},"events":["deploy.failed"]}`, true)
	if code != 201 {
		t.Fatalf("create channel: %d %s", code, b)
	}
	var ch struct{ ID int64 }
	_ = json.Unmarshal([]byte(b), &ch)
	_, b = call(t, c, "GET", u+"/notifications", "", false)
	if strings.Contains(b, "s3cret") {
		t.Fatalf("secrets leaked: %s", b)
	}
	// Saving with empty secrets keeps the saved ones.
	code, b = call(t, c, "PUT", u+"/notifications/"+itoa(ch.ID), `{"name":"hook2","type":"webhook","config":{"url":"https://hooks.example.com/abc/secret-path","secret":""},"events":["deploy.failed"]}`, true)
	if code != 200 {
		t.Fatalf("update channel: %d %s", code, b)
	}
	if code, b := call(t, c, "POST", u+"/notifications", `{"name":"bad","type":"webhook","config":{"url":"ftp://x"},"events":["deploy.failed"]}`, true); code != 400 {
		t.Fatalf("bad url accepted: %d %s", code, b)
	}
}
