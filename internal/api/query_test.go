package api

import (
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"strings"
	"testing"
)

func TestQueryPermission(t *testing.T) {
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
	var db, web, other struct{ App struct{ ID int64 } }
	_ = json.Unmarshal([]byte(mustCall(owner, "POST", "/projects/"+itoa(alpha.ID)+"/databases", `{"name":"main","engine":"postgres"}`, 201)), &db)
	_ = json.Unmarshal([]byte(mustCall(owner, "POST", "/projects/"+itoa(alpha.ID)+"/apps", `{"name":"web","source":"image","image":"nginx:1.27"}`, 201)), &web)
	_ = json.Unmarshal([]byte(mustCall(owner, "POST", "/projects/"+itoa(beta.ID)+"/databases", `{"name":"cache","engine":"redis"}`, 201)), &other)
	dbPath := "/apps/" + itoa(db.App.ID) + "/query"

	// The owner can always open the Query tab.
	if b := mustCall(owner, "GET", dbPath, "", 200); !strings.Contains(b, `"language":"sql"`) {
		t.Fatalf("query info: %s", b)
	}
	if b := mustCall(owner, "GET", "/apps/"+itoa(db.App.ID), "", 200); !strings.Contains(b, `"canQuery":true`) {
		t.Fatalf("owner canQuery: %s", b)
	}
	mustCall(owner, "GET", "/apps/"+itoa(web.App.ID)+"/query", "", 400) // not a database

	// A developer without the permission is refused, even on their project.
	b := mustCall(owner, "POST", "/users", `{"username":"dev","role":"developer","access":{"`+itoa(alpha.ID)+`":"developer"}}`, 201)
	var inv struct {
		InvitePath string
		User       struct {
			ID      int64
			DBQuery bool
		}
	}
	_ = json.Unmarshal([]byte(b), &inv)
	if inv.User.DBQuery {
		t.Fatal("new users must not get the query permission by default")
	}
	jar, _ := cookiejar.New(nil)
	dev := &http.Client{Jar: jar}
	mustCall(dev, "POST", "/invite/"+strings.TrimPrefix(inv.InvitePath, "/#/invite/"), `{"password":"developer-pass-1"}`, 200)
	mustCall(dev, "GET", dbPath, "", 403)
	mustCall(dev, "POST", dbPath, `{"query":"select 1"}`, 403)
	mustCall(dev, "GET", dbPath+"/tables", "", 403)
	mustCall(dev, "GET", dbPath+"/saved", "", 403)
	mustCall(dev, "POST", dbPath+"/saved", `{"name":"x","query":"select 1"}`, 403)
	if b := mustCall(dev, "GET", "/apps/"+itoa(db.App.ID), "", 200); !strings.Contains(b, `"canQuery":false`) {
		t.Fatalf("dev canQuery: %s", b)
	}

	// With the permission they get through (the query itself then fails
	// here because there is no Docker), but not into other projects.
	mustCall(owner, "PUT", "/users/"+itoa(inv.User.ID), `{"role":"viewer","access":{"`+itoa(alpha.ID)+`":"viewer"},"dbQuery":true}`, 200)
	if b := mustCall(dev, "GET", "/me", "", 200); !strings.Contains(b, `"dbQuery":true`) {
		t.Fatalf("me: %s", b)
	}
	mustCall(dev, "GET", dbPath, "", 200)
	if code, b := call(t, dev, "POST", u+dbPath, `{"query":"select 1"}`, true); code != 400 || strings.Contains(b, "permission") {
		t.Fatalf("permitted viewer: %d %s", code, b)
	}
	mustCall(dev, "GET", "/apps/"+itoa(other.App.ID)+"/query", "", 404)

	// The failed query is in the activity log with its text, and in history.
	if b := mustCall(owner, "GET", "/audit", "", 200); !strings.Contains(b, "run database query") || !strings.Contains(b, `select 1`) {
		t.Fatalf("activity: %s", b)
	}
	if b := mustCall(dev, "GET", dbPath+"/history", "", 200); !strings.Contains(b, `"query":"select 1"`) {
		t.Fatalf("history: %s", b)
	}
	if b := mustCall(owner, "GET", dbPath+"/history", "", 200); strings.Contains(b, "select 1") {
		t.Fatalf("history is per user: %s", b)
	}

	// Saved queries are shared; a viewer may remove only their own.
	var mine, boss struct{ ID int64 }
	_ = json.Unmarshal([]byte(mustCall(dev, "POST", dbPath+"/saved", `{"name":"Paid orders","query":"select * from orders where status = 'paid'"}`, 201)), &mine)
	_ = json.Unmarshal([]byte(mustCall(owner, "POST", dbPath+"/saved", `{"name":"All","query":"select 1"}`, 201)), &boss)
	mustCall(dev, "POST", dbPath+"/saved", `{"name":"","query":"select 1"}`, 400)
	if b := mustCall(dev, "GET", dbPath+"/saved", "", 200); !strings.Contains(b, "Paid orders") || !strings.Contains(b, `"All"`) {
		t.Fatalf("saved: %s", b)
	}
	mustCall(dev, "DELETE", "/saved-queries/"+itoa(boss.ID), "", 403)
	mustCall(dev, "DELETE", "/saved-queries/"+itoa(mine.ID), "", 200)
	mustCall(owner, "DELETE", "/saved-queries/"+itoa(boss.ID), "", 200)

	// Taking the permission away closes the tab again.
	mustCall(owner, "PUT", "/users/"+itoa(inv.User.ID), `{"role":"viewer","access":{"`+itoa(alpha.ID)+`":"viewer"}}`, 200)
	mustCall(dev, "GET", dbPath, "", 403)
	// Admins always have it; the flag isn't stored for them.
	b = mustCall(owner, "PUT", "/users/"+itoa(inv.User.ID), `{"role":"admin","dbQuery":true}`, 200)
	if !strings.Contains(b, `"dbQuery":false`) {
		t.Fatalf("admin flag: %s", b)
	}
	mustCall(dev, "GET", dbPath, "", 200)
}
