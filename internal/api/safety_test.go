package api

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/SimpleAOB/SimplePanel/internal/deploy"
	"github.com/SimpleAOB/SimplePanel/internal/docker"
	"github.com/SimpleAOB/SimplePanel/internal/monitor"
	"github.com/SimpleAOB/SimplePanel/internal/store"
)

func TestSetupCode(t *testing.T) {
	t.Setenv("SP_SETUP_CODE", "ABCDEF123456")
	ts, c := newTestServer(t)
	u := ts.URL + "/api"
	if _, b := call(t, c, "GET", u+"/setup", "", false); !strings.Contains(b, `"needsCode":true`) {
		t.Fatalf("setup should ask for the code: %s", b)
	}
	if code, _ := call(t, c, "POST", u+"/setup", `{"username":"admin","password":"longenough1"}`, true); code != 403 {
		t.Fatalf("setup without the code: %d", code)
	}
	if code, _ := call(t, c, "POST", u+"/setup", `{"username":"admin","password":"longenough1","code":"WRONG"}`, true); code != 403 {
		t.Fatalf("setup with a wrong code: %d", code)
	}
	if code, b := call(t, c, "POST", u+"/setup", `{"username":"admin","password":"longenough1","code":" abcdef123456 "}`, true); code != 201 {
		t.Fatalf("setup with the code: %d %s", code, b)
	}
	if _, b := call(t, c, "GET", u+"/setup", "", false); strings.Contains(b, `"needsCode":true`) {
		t.Fatalf("no code needed once set up: %s", b)
	}
}

func TestReservedPorts(t *testing.T) {
	ts, c := newTestServer(t)
	u := ts.URL + "/api"
	call(t, c, "POST", u+"/setup", `{"username":"admin","password":"longenough1"}`, true)
	call(t, c, "POST", u+"/projects", `{"name":"shop"}`, true)
	for _, p := range []string{"22", "2377", "7946", "4789"} {
		body := `{"name":"x` + p + `","source":"image","image":"nginx","ports":[{"published":` + p + `,"target":80,"protocol":"tcp"}]}`
		if code, b := call(t, c, "POST", u+"/projects/1/apps", body, true); code != 400 {
			t.Errorf("publishing port %s: %d %s", p, code, b)
		}
	}
	body := `{"name":"ok","source":"image","image":"nginx","ports":[{"published":8080,"target":80,"protocol":"tcp"}],"memoryMb":-1}`
	if code, b := call(t, c, "POST", u+"/projects/1/apps", body, true); code != 201 {
		t.Errorf("port 8080 with no memory limit: %d %s", code, b)
	}
	if code, _ := call(t, c, "POST", u+"/projects/1/apps", `{"name":"tiny","source":"image","image":"nginx","memoryMb":8}`, true); code != 400 {
		t.Errorf("an 8 MB limit should be refused: %d", code)
	}
}

func TestHTTPSOnly(t *testing.T) {
	ts, c := newTestServer(t)
	u := ts.URL + "/api"
	call(t, c, "POST", u+"/setup", `{"username":"admin","password":"longenough1"}`, true)
	// Turning it on over plain HTTP is refused: that wouldn't prove HTTPS works.
	if code, _ := call(t, c, "PUT", u+"/settings", `{"panelDomain":"panel.example.com","httpsOnly":true}`, true); code != 400 {
		t.Fatalf("enabling over HTTP: %d", code)
	}
	// Through Traefik (private peer, X-Forwarded-Proto https, the domain as Host) it works.
	req, _ := http.NewRequest("PUT", u+"/settings", strings.NewReader(`{"panelDomain":"panel.example.com","httpsOnly":true}`))
	req.Host = "panel.example.com"
	req.Header.Set("X-Forwarded-Proto", "https")
	req.Header.Set(csrfHeader, "1")
	for _, ck := range c.Jar.Cookies(req.URL) {
		req.AddCookie(ck)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 200 {
		b, _ := io.ReadAll(res.Body)
		t.Fatalf("enabling via HTTPS: %d %s %v", res.StatusCode, b, req.Header)
	}
	// The test client is on loopback, which always works (SSH tunnels).
	if code, _ := call(t, c, "GET", u+"/me", "", false); code != 200 {
		t.Fatalf("loopback should still work: %d", code)
	}
	// A visitor from the internet on plain HTTP is turned away.
	srv := ts.Config.Handler
	for _, tc := range []struct{ path, proto string }{{"/", ""}, {"/api/me", ""}, {"/api/me", "https"}} {
		r := httptest.NewRequest("GET", tc.path, nil)
		r.RemoteAddr = "203.0.113.9:5000"
		if tc.proto != "" {
			r.Header.Set("X-Forwarded-Proto", tc.proto) // spoofed: not from Traefik
		}
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, r)
		if w.Code != http.StatusPermanentRedirect && w.Code != http.StatusForbidden {
			t.Errorf("%s from the internet: %d", tc.path, w.Code)
		}
	}
	r := httptest.NewRequest("GET", "/api/health", nil)
	r.RemoteAddr = "203.0.113.9:5000"
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Errorf("health check: %d", w.Code)
	}
	// Clearing the domain turns it off again.
	if code, b := call(t, c, "PUT", u+"/settings", `{"panelDomain":""}`, true); code != 200 || !strings.Contains(b, `"httpsOnly":false`) {
		t.Fatalf("clearing the domain: %d %s", code, b)
	}
}

// memServer is a test server whose monitor reads a fake /proc/meminfo.
func memServer(t *testing.T, totalMB, availMB int) (*httptest.Server, *http.Client, func(int)) {
	t.Helper()
	proc := t.TempDir()
	write := func(avail int) {
		s := "MemTotal: " + itoa(int64(totalMB*1024)) + " kB\nMemAvailable: " + itoa(int64(avail*1024)) + " kB\nSwapTotal: 0 kB\nSwapFree: 0 kB\n"
		if err := os.WriteFile(filepath.Join(proc, "meminfo"), []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(availMB)
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	dc := docker.New(filepath.Join(t.TempDir(), "no.sock"))
	dm := deploy.NewManager(deploy.Config{Network: "simplepanel", ProxyDir: t.TempDir(), WorkDir: t.TempDir()}, st, dc, log)
	mon := monitor.New(monitor.Config{ProcDir: proc}, st, dc, dm, nil, log)
	s := New(st, dc, dm, fstest.MapFS{"index.html": {Data: []byte("ui")}}, log, "test")
	s.Use(nil, mon, nil)
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	jar, _ := cookiejar.New(nil)
	return ts, &http.Client{Jar: jar}, write
}

func TestCapacityOneGig(t *testing.T) {
	ts, c, _ := memServer(t, 961, 600)
	u := ts.URL + "/api"
	call(t, c, "POST", u+"/setup", `{"username":"admin","password":"longenough1"}`, true)
	call(t, c, "POST", u+"/projects", `{"name":"shop"}`, true)
	call(t, c, "POST", u+"/projects/1/apps", `{"name":"web","source":"image","image":"nginx"}`, true)
	call(t, c, "POST", u+"/projects/1/apps", `{"name":"api","source":"image","image":"node","memoryMb":256}`, true)
	_, b := call(t, c, "GET", u+"/capacity?app=1", "", false)
	var out struct {
		Known    bool
		Capacity deploy.Capacity
		Others   int `json:"othersCommittedMb"`
		Total    int `json:"committedMb"`
		Services []struct {
			Name    string
			LimitMB int  `json:"limitMb"`
			Auto    bool `json:"auto"`
		}
	}
	if err := json.Unmarshal([]byte(b), &out); err != nil || !out.Known {
		t.Fatalf("capacity: %v %s", err, b)
	}
	if out.Capacity.AvailableMB != 577 || out.Capacity.Defaults["app"] != 128 {
		t.Fatalf("capacity: %+v", out.Capacity)
	}
	if out.Total != 128+256 || out.Others != 256 {
		t.Fatalf("committed %d, others %d", out.Total, out.Others)
	}
	if len(out.Services) != 2 || out.Services[0].Name != "api" || !out.Services[1].Auto {
		t.Fatalf("services: %+v", out.Services)
	}
}

func TestMemoryPressureGuard(t *testing.T) {
	ts, c, setAvail := memServer(t, 961, 50) // 95% used
	u := ts.URL + "/api"
	call(t, c, "POST", u+"/setup", `{"username":"admin","password":"longenough1"}`, true)
	call(t, c, "POST", u+"/projects", `{"name":"shop"}`, true)
	call(t, c, "POST", u+"/projects/1/apps", `{"name":"web","source":"image","image":"nginx"}`, true)
	code, b := call(t, c, "POST", u+"/apps/1/deploy", "", true)
	if code != 409 || !strings.Contains(b, `"code":"memory_pressure"`) || !strings.Contains(b, `"canForce":true`) {
		t.Fatalf("deploy at 95%%: %d %s", code, b)
	}
	if code, b := call(t, c, "POST", u+"/projects/1/databases", `{"name":"db","engine":"postgres"}`, true); code != 409 {
		t.Fatalf("new database at 95%%: %d %s", code, b)
	}
	if code, b := call(t, c, "POST", u+"/apps/1/deploy?force=1", "", true); code != 202 || !strings.Contains(b, "anyway") {
		t.Fatalf("forced deploy: %d %s", code, b)
	}
	setAvail(500)
	if code, b := call(t, c, "POST", u+"/apps/1/deploy", "", true); code != 202 {
		t.Fatalf("deploy with room: %d %s", code, b)
	}
}
