package api

import (
	"strings"
	"testing"
)

func TestPathDomains(t *testing.T) {
	ts, c := newTestServer(t)
	u := ts.URL + "/api"
	mustCall := func(method, url, body string, want int) string {
		t.Helper()
		code, b := call(t, c, method, url, body, true)
		if code != want {
			t.Fatalf("%s %s %s: %d %s", method, url, body, code, b)
		}
		return b
	}
	mustCall("POST", u+"/setup", `{"username":"admin","password":"longenough1"}`, 201)
	mustCall("POST", u+"/projects", `{"name":"shop"}`, 201)
	mustCall("POST", u+"/projects/1/apps", `{"name":"web","source":"image","image":"nginx:1.27"}`, 201)
	mustCall("POST", u+"/projects/1/apps", `{"name":"api","source":"image","image":"nginx:1.27"}`, 201)

	mustCall("POST", u+"/apps/1/domains", `{"host":"staging3.example.com","https":true}`, 201)
	// Pasting a URL with a path keeps the path (it used to be dropped).
	b := mustCall("POST", u+"/apps/2/domains", `{"host":"https://Staging3.Example.com/api/","https":true}`, 201)
	if !strings.Contains(b, `"host":"staging3.example.com"`) || !strings.Contains(b, `"path":"/api"`) || !strings.Contains(b, `"stripPath":false`) {
		t.Fatalf("path domain: %s", b)
	}
	// Same host and path on another app: a clear conflict.
	if code, b := call(t, c, "POST", u+"/apps/1/domains", `{"host":"staging3.example.com","path":"api"}`, true); code != 409 || !strings.Contains(b, "staging3.example.com/api") || !strings.Contains(b, "api in project shop") {
		t.Fatalf("conflict: %d %s", code, b)
	}
	// The whole host taken by another app suggests a path.
	if code, b := call(t, c, "POST", u+"/apps/2/domains", `{"host":"staging3.example.com"}`, true); code != 409 || !strings.Contains(b, "/api") {
		t.Fatalf("root conflict: %d %s", code, b)
	}
	mustCall("POST", u+"/apps/2/domains", `{"host":"staging3.example.com","path":"/api/v2","stripPath":true}`, 201)
	mustCall("POST", u+"/apps/2/domains", "{\"host\":\"staging3.example.com\",\"path\":\"/a`b\"}", 400)
	// Strip can't be turned on for a domain without a path.
	mustCall("PUT", u+"/domains/1", `{"stripPath":true}`, 400)
	if b := mustCall("PUT", u+"/domains/2", `{"stripPath":true}`, 200); !strings.Contains(b, `"stripPath":true`) || !strings.Contains(b, `"port":0`) {
		t.Fatalf("strip toggle: %s", b)
	}
	// Changing only the port leaves stripping alone.
	if b := mustCall("PUT", u+"/domains/2", `{"port":3000}`, 200); !strings.Contains(b, `"stripPath":true`) || !strings.Contains(b, `"port":3000`) {
		t.Fatalf("port change: %s", b)
	}
	b = mustCall("GET", u+"/apps/2", "", 200)
	if !strings.Contains(b, `"path":"/api"`) || !strings.Contains(b, `"path":"/api/v2"`) {
		t.Fatalf("app detail domains: %s", b)
	}
}
