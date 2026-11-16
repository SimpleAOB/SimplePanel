package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func mcpPost(t *testing.T, url, token, body string, hdr map[string]string) (int, map[string]any) {
	t.Helper()
	req, _ := http.NewRequest("POST", url+"/mcp", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(res.Body).Decode(&out)
	return res.StatusCode, out
}

func TestMCP(t *testing.T) {
	ts, c := newTestServer(t)
	u := ts.URL + "/api"
	if code, b := call(t, c, "POST", u+"/setup", `{"username":"admin","password":"longenough1"}`, true); code != 201 {
		t.Fatalf("setup: %d %s", code, b)
	}
	tokens := map[string]string{}
	for _, sc := range []string{"read", "manage", "full"} {
		code, b := call(t, c, "POST", u+"/tokens", `{"name":"t-`+sc+`","scope":"`+sc+`"}`, true)
		if code != 201 {
			t.Fatalf("create token: %d %s", code, b)
		}
		var r struct{ Secret string }
		_ = json.Unmarshal([]byte(b), &r)
		if !strings.HasPrefix(r.Secret, "sp_") {
			t.Fatalf("secret %q", r.Secret)
		}
		tokens[sc] = r.Secret
	}
	if code, _ := call(t, c, "POST", u+"/tokens", `{"name":"x","scope":"root"}`, true); code != 400 {
		t.Fatalf("bad scope accepted: %d", code)
	}
	if _, b := call(t, c, "GET", u+"/tokens", "", false); strings.Contains(b, "sp_") {
		t.Fatal("token list leaks secrets")
	}

	init := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"t","version":"1"}}}`
	if code, _ := mcpPost(t, ts.URL, "", init, nil); code != 401 {
		t.Fatalf("no token: %d", code)
	}
	if code, _ := mcpPost(t, ts.URL, "sp_wrong", init, nil); code != 401 {
		t.Fatalf("wrong token: %d", code)
	}
	if code, _ := mcpPost(t, ts.URL, tokens["read"], init, map[string]string{"Origin": "https://evil.example"}); code != 403 {
		t.Fatalf("foreign origin: %d", code)
	}
	code, out := mcpPost(t, ts.URL, tokens["read"], init, nil)
	res, _ := out["result"].(map[string]any)
	if code != 200 || res["protocolVersion"] != "2025-06-18" {
		t.Fatalf("initialize: %d %v", code, out)
	}
	if code, _ := mcpPost(t, ts.URL, tokens["read"], `{"jsonrpc":"2.0","method":"notifications/initialized"}`, nil); code != 202 {
		t.Fatalf("notification: %d", code)
	}

	toolNames := func(tok string) []string {
		_, out := mcpPost(t, ts.URL, tok, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`, nil)
		var names []string
		for _, x := range out["result"].(map[string]any)["tools"].([]any) {
			names = append(names, x.(map[string]any)["name"].(string))
		}
		return names
	}
	has := func(names []string, n string) bool {
		for _, x := range names {
			if x == n {
				return true
			}
		}
		return false
	}
	read, manage, full := toolNames(tokens["read"]), toolNames(tokens["manage"]), toolNames(tokens["full"])
	if has(read, "deploy") || !has(read, "db_query") || has(manage, "delete_service") || !has(manage, "deploy") || !has(full, "delete_project") {
		t.Fatalf("scope filtering wrong:\nread=%v\nmanage=%v\nfull=%v", read, manage, full)
	}

	callTool := func(tok, name, args string) (bool, string) {
		_, out := mcpPost(t, ts.URL, tok, `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"`+name+`","arguments":`+args+`}}`, nil)
		r, ok := out["result"].(map[string]any)
		if !ok {
			t.Fatalf("%s: %v", name, out)
		}
		isErr, _ := r["isError"].(bool)
		return isErr, r["content"].([]any)[0].(map[string]any)["text"].(string)
	}
	if isErr, txt := callTool(tokens["read"], "create_project", `{"name":"x"}`); !isErr || !strings.Contains(txt, "manage scope") {
		t.Fatalf("read token created a project: %s", txt)
	}
	if isErr, txt := callTool(tokens["manage"], "create_project", `{"name":"shop"}`); isErr {
		t.Fatalf("create_project: %s", txt)
	}
	if isErr, txt := callTool(tokens["manage"], "create_project", `{"name":"shop","extra":1}`); !isErr || !strings.Contains(txt, "unknown field") {
		t.Fatalf("unknown argument accepted: %s", txt)
	}
	if isErr, txt := callTool(tokens["read"], "list_projects", `{}`); isErr || !strings.Contains(txt, `"shop"`) {
		t.Fatalf("list_projects: %s", txt)
	}
	if isErr, txt := callTool(tokens["full"], "delete_project", `{"project":"shop","confirm":"nope"}`); !isErr || !strings.Contains(txt, "confirm") {
		t.Fatalf("delete without confirm: %s", txt)
	}
	if _, out := mcpPost(t, ts.URL, tokens["read"], `{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"nope"}}`, nil); out["error"] == nil {
		t.Fatalf("unknown tool: %v", out)
	}

	_, b := call(t, c, "GET", u+"/audit", "", false)
	if !strings.Contains(b, `"action":"create_project"`) || !strings.Contains(b, `"tokenName":"t-manage"`) {
		t.Fatalf("audit log missing entries: %s", b)
	}

	// Revoked tokens stop working.
	var list []struct{ ID int64 }
	_, b = call(t, c, "GET", u+"/tokens", "", false)
	_ = json.Unmarshal([]byte(b), &list)
	if code, _ := call(t, c, "DELETE", u+"/tokens/"+itoa(list[0].ID), "", true); code != 200 {
		t.Fatalf("revoke: %d", code)
	}
	if code, _ := mcpPost(t, ts.URL, tokens["read"], init, nil); code != 401 {
		t.Fatalf("revoked token still works: %d", code)
	}
}

func itoa(n int64) string { b, _ := json.Marshal(n); return string(b) }

func TestRedactArgs(t *testing.T) {
	got := redactArgs(json.RawMessage(`{"service":"a/b","set":{"API_KEY":"s3cret"},"env":{"X":"y"},"git_token":"ghp_x"}`))
	for _, secret := range []string{"s3cret", `"y"`, "ghp_x"} {
		if strings.Contains(got, secret) {
			t.Fatalf("secret %s in audit args: %s", secret, got)
		}
	}
	if !strings.Contains(got, "API_KEY") || !strings.Contains(got, "a/b") {
		t.Fatalf("lost non-secret args: %s", got)
	}
}

func TestParseSince(t *testing.T) {
	for _, ok := range []string{"", "15m", "2h", "1d", "2026-01-02T03:04:05Z"} {
		if _, err := parseSince(ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	for _, bad := range []string{"yesterday", "-5m", "0d"} {
		if _, err := parseSince(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}
