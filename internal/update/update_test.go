package update

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SimpleAOB/SimplePanel/internal/docker"
	"github.com/SimpleAOB/SimplePanel/internal/store"
)

func TestVersions(t *testing.T) {
	for _, v := range []string{"main", "dev", "v1.2", "v1.2.3-rc.1", ""} {
		if _, ok := parseVersion(v); ok {
			t.Errorf("%q should not parse", v)
		}
	}
	a, _ := parseVersion("v0.10.0")
	b, _ := parseVersion("0.9.12")
	if !newer(a, b) || newer(b, a) || newer(a, a) {
		t.Error("version ordering is wrong")
	}
	if Display("0.4.0") != "v0.4.0" || Display("main") != "main" {
		t.Error("Display")
	}
}

func TestCheckLabels(t *testing.T) {
	rel := Release{Version: "0.4.0", Tag: "v0.4.0", Commit: strings.Repeat("a", 40)}
	good := map[string]string{
		"org.opencontainers.image.source":   "https://github.com/SimpleAOB/SimplePanel",
		"org.opencontainers.image.version":  "0.4.0",
		"org.opencontainers.image.revision": strings.Repeat("a", 40),
	}
	if err := checkLabels(good, "SimpleAOB/SimplePanel", rel); err != nil {
		t.Fatal(err)
	}
	for key, bad := range map[string]string{
		"org.opencontainers.image.source":   "https://github.com/evil/SimplePanel",
		"org.opencontainers.image.version":  "0.3.9",
		"org.opencontainers.image.revision": strings.Repeat("b", 40),
	} {
		l := map[string]string{}
		for k, v := range good {
			l[k] = v
		}
		l[key] = bad
		if err := checkLabels(l, "SimpleAOB/SimplePanel", rel); err == nil {
			t.Errorf("a wrong %s was accepted", key)
		}
	}
}

func TestPatchSpecKeepsUnknownFields(t *testing.T) {
	raw := json.RawMessage(`{"Name":"simplepanel","Labels":{"simplepanel.managed":"true"},
		"TaskTemplate":{"ContainerSpec":{"Image":"ghcr.io/simpleaob/simplepanel:edge@sha256:1","Hosts":["x"],
		"Mounts":[{"Type":"bind","Source":"/proc","Target":"/host/proc","ReadOnly":true,"BindOptions":{"Propagation":"rprivate"}}]},
		"ForceUpdate":18446744073709551615},
		"EndpointSpec":{"Ports":[{"PublishMode":"host","TargetPort":3000,"PublishedPort":3000}]}}`)
	out, err := patchSpec(raw, "ghcr.io/simpleaob/simplepanel:0.4.0@sha256:abc", "0.4.0")
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	for _, want := range []string{
		`"Image":"ghcr.io/simpleaob/simplepanel:0.4.0@sha256:abc"`,
		`"Hosts":["x"]`, `"BindOptions":{"Propagation":"rprivate"}`,
		`"ForceUpdate":18446744073709551615`, `"PublishMode":"host"`,
		`"FailureAction":"rollback"`, `"Order":"stop-first"`, `"StartInterval":2000000000`,
		`"simplepanel.managed":"true"`, `"simplepanel.version":"0.4.0"`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("patched spec lacks %s:\n%s", want, s)
		}
	}
}

func newTest(t *testing.T, version string, gh http.HandlerFunc) (*Updater, *store.Store) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	srv := httptest.NewServer(gh)
	t.Cleanup(srv.Close)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	u := New(Config{Version: version, DataDir: dir, APIBase: srv.URL}, st, docker.New(filepath.Join(dir, "no.sock")), nil, log)
	return u, st
}

func github(tag string, prerelease bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/SimpleAOB/SimplePanel/releases/latest":
			if tag == "" {
				http.NotFound(w, r)
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"tag_name": tag, "name": tag, "body": "## Fixes\n- one",
				"html_url": "https://github.com/SimpleAOB/SimplePanel/releases/tag/" + tag, "prerelease": prerelease})
		case r.URL.Path == "/repos/SimpleAOB/SimplePanel/commits/"+tag:
			io.WriteString(w, strings.Repeat("c", 40))
		default:
			http.NotFound(w, r)
		}
	}
}

func TestCheck(t *testing.T) {
	ctx := context.Background()
	u, _ := newTest(t, "v0.3.0", github("v0.4.0", false))
	c := u.CheckNow(ctx, true)
	if c.Error != "" || c.Latest == nil || !c.Available || c.Latest.Version != "0.4.0" || c.Latest.Commit != strings.Repeat("c", 40) {
		t.Fatalf("check: %+v", c)
	}
	s := u.Status(ctx)
	if s.Check.Latest == nil || s.CanUpdate {
		// Not a Swarm service in tests, so updating is blocked.
		t.Fatalf("status: %+v", s)
	}
	if s.Blocker == "" {
		t.Fatal("expected a blocker without Docker")
	}
	if _, err := u.Start(ctx, "0.4.0", false, Actor{Username: "a"}); err == nil {
		t.Fatal("start should fail without a Swarm service")
	}

	u2, _ := newTest(t, "v0.4.0", github("v0.4.0", false))
	if c := u2.CheckNow(ctx, true); c.Available || c.Note == "" {
		t.Fatalf("same version: %+v", c)
	}
	u3, _ := newTest(t, "v0.3.0", github("", false))
	if c := u3.CheckNow(ctx, true); c.Latest != nil || c.Available {
		t.Fatalf("no releases: %+v", c)
	}
	u4, _ := newTest(t, "v0.3.0", github("v0.5.0-rc.1", true))
	if c := u4.CheckNow(ctx, true); c.Latest != nil || c.Available {
		t.Fatalf("prerelease offered: %+v", c)
	}
}

func TestSettings(t *testing.T) {
	ctx := context.Background()
	u, _ := newTest(t, "v0.3.0", github("", false))
	if s := u.LoadSettings(ctx); s.Auto || s.Hour != 3 {
		t.Fatalf("defaults: %+v", s)
	}
	if err := u.SaveSettings(ctx, Settings{Auto: true, Hour: 24}); err == nil {
		t.Fatal("hour 24 accepted")
	}
	if err := u.SaveSettings(ctx, Settings{Auto: true, Hour: -1}); err != nil {
		t.Fatal(err)
	}
	if s := u.LoadSettings(ctx); !s.Auto || s.Hour != -1 {
		t.Fatalf("saved: %+v", s)
	}
}

func TestReconcile(t *testing.T) {
	ctx := context.Background()
	u, st := newTest(t, "v0.4.0", github("", false))
	u.setJob(ctx, &Job{State: "restarting", From: "v0.3.0", To: "v0.4.0"})
	u.reconcile(ctx)
	if j := u.job(ctx); j.State != "succeeded" {
		t.Fatalf("job: %+v", j)
	}
	log, _ := st.Audit(ctx, 10, 0, "")
	if len(log) == 0 || log[0].Action != "SimplePanel updated" {
		t.Fatalf("activity: %+v", log)
	}

	u.setJob(ctx, &Job{State: "running", From: "v0.4.0", To: "v0.5.0"})
	u.reconcile(ctx)
	if j := u.job(ctx); j.State != "failed" {
		t.Fatalf("interrupted job: %+v", j)
	}
}

func TestSnapshotKeepsNewest(t *testing.T) {
	ctx := context.Background()
	u, _ := newTest(t, "v0.3.0", github("", false))
	dir := filepath.Join(u.cfg.DataDir, "updates")
	os.MkdirAll(dir, 0o700)
	for _, n := range []string{"simplepanel-v0.1.0-20250101-000000.db", "simplepanel-v0.2.0-20260101-000000.db", "simplepanel-main-20260601-000000.db"} {
		os.WriteFile(filepath.Join(dir, n), nil, 0o600)
	}
	p, err := u.snapshot(ctx, "v0.3.0")
	if err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(p); err != nil || fi.Size() == 0 {
		t.Fatalf("snapshot: %v", err)
	}
	ents, _ := os.ReadDir(dir)
	if len(ents) != keepCopies {
		t.Fatalf("kept %d copies", len(ents))
	}
	if _, err := os.Stat(filepath.Join(dir, "simplepanel-v0.1.0-20250101-000000.db")); err == nil {
		t.Fatal("oldest copy was kept")
	}
}
