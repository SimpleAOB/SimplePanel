package deploy

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SimpleAOB/SimplePanel/internal/docker"
	"github.com/SimpleAOB/SimplePanel/internal/store"
)

// fakeSwarm serves /tasks and /tasks/{id}/logs like the Docker API.
type fakeSwarm struct {
	mu    sync.Mutex
	tasks []map[string]any
	logs  map[string]string
	asked map[string]int
}

func (f *fakeSwarm) task(id, state string, created time.Time, exit int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, t := range f.tasks {
		if t["ID"] == id {
			t["Status"].(map[string]any)["State"] = state
			return
		}
	}
	f.tasks = append(f.tasks, map[string]any{
		"ID": id, "CreatedAt": created, "Slot": 1, "DesiredState": "running",
		"Status": map[string]any{"State": state, "Timestamp": created.Add(time.Minute),
			"ContainerStatus": map[string]any{"ContainerID": "c-" + id, "ExitCode": exit}},
		"Spec": map[string]any{"ContainerSpec": map[string]any{"Image": "nginx:1"}},
	})
}

func (f *fakeSwarm) handler(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p := r.URL.Path[strings.Index(r.URL.Path, "/tasks"):]
	if p == "/tasks" {
		json.NewEncoder(w).Encode(f.tasks)
		return
	}
	id := strings.TrimSuffix(strings.TrimPrefix(p, "/tasks/"), "/logs")
	f.asked[id]++
	log, ok := f.logs[id]
	if !ok {
		w.WriteHeader(404)
		w.Write([]byte(`{"message":"no such task"}`))
		return
	}
	for _, l := range strings.SplitAfter(log, "\n") {
		if l == "" {
			continue
		}
		hdr := make([]byte, 8)
		hdr[0] = 1
		binary.BigEndian.PutUint32(hdr[4:], uint32(len(l)))
		w.Write(hdr)
		w.Write([]byte(l))
	}
}

func TestSyncRuns(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	sock := filepath.Join(t.TempDir(), "d.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeSwarm{logs: map[string]string{}, asked: map[string]int{}}
	srv := &httptest.Server{Listener: ln, Config: &http.Server{Handler: http.HandlerFunc(f.handler)}}
	srv.Start()
	defer srv.Close()
	m := NewManager(Config{}, st, docker.New(sock), slog.New(slog.NewTextHandler(io.Discard, nil)))

	p, err := st.CreateProject(ctx, "shop")
	if err != nil {
		t.Fatal(err)
	}
	app := &store.App{ProjectID: p.ID, Name: "web", Source: "image", Image: "nginx:1", Replicas: 1}
	if err := st.CreateApp(ctx, app); err != nil {
		t.Fatal(err)
	}
	app, _ = st.App(ctx, app.ID)
	dep, _ := st.CreateDeployment(ctx, app.ID, "manual")

	t0 := time.Now().Add(time.Hour) // after the deployment
	f.task("aaa", "failed", t0, 1)
	f.logs["aaa"] = "2026-01-01T00:00:01.000000000Z boom\n2026-01-01T00:00:02.000000000Z ERROR crashed\n"
	f.task("bbb", "running", t0.Add(time.Minute), 0)
	f.logs["bbb"] = "2026-01-01T00:01:00.000000000Z up\n"

	if err := m.SyncRuns(ctx, app); err != nil {
		t.Fatal(err)
	}
	runs, _ := st.AppRuns(ctx, app.ID, 10)
	if len(runs) != 2 || runs[0].Number != 2 || runs[1].Number != 1 {
		t.Fatalf("runs: %+v", runs)
	}
	first, _ := st.AppRun(ctx, app.ID, 1)
	if !first.Archived || first.State != "failed" || first.ExitCode == nil || *first.ExitCode != 1 ||
		first.DeploymentID == nil || *first.DeploymentID != dep.ID || !strings.Contains(first.Log, "ERROR crashed") {
		t.Fatalf("first: %+v", first)
	}
	if runs[0].Archived || runs[0].State != "running" || runs[0].EndedAt != nil {
		t.Fatalf("second: %+v", runs[0])
	}

	// Docker forgets the failed task: its logs still come from the database,
	// and archived launches aren't asked about again.
	f.mu.Lock()
	f.tasks = f.tasks[1:]
	delete(f.logs, "aaa")
	f.mu.Unlock()
	asked := f.asked["aaa"]
	if err := m.SyncRuns(ctx, app); err != nil {
		t.Fatal(err)
	}
	if f.asked["aaa"] != asked {
		t.Fatal("archived launch was fetched again")
	}
	first, _ = st.AppRun(ctx, app.ID, 1)
	got, err := m.RunLogs(ctx, first, 1, 0)
	if err != nil || got != "2026-01-01T00:00:02.000000000Z ERROR crashed\n" {
		t.Fatalf("archived tail: %q %v", got, err)
	}
	second, _ := st.AppRun(ctx, app.ID, 2)
	if got, _ := m.RunLogs(ctx, second, 0, 0); !strings.Contains(got, " up") {
		t.Fatalf("live logs: %q", got)
	}

	// The running one stops and is archived.
	f.task("bbb", "shutdown", t0, 0)
	if err := m.SyncRuns(ctx, app); err != nil {
		t.Fatal(err)
	}
	second, _ = st.AppRun(ctx, app.ID, 2)
	if !second.Archived || second.State != "shutdown" || second.EndedAt == nil || second.Log != f.logs["bbb"] {
		t.Fatalf("stopped: %+v", second)
	}
}

func TestPruneRuns(t *testing.T) {
	ctx := context.Background()
	st, _ := store.Open(filepath.Join(t.TempDir(), "t.db"))
	defer st.Close()
	p, _ := st.CreateProject(ctx, "shop")
	app := &store.App{ProjectID: p.ID, Name: "web", Source: "image", Image: "nginx:1", Replicas: 1}
	st.CreateApp(ctx, app)
	for i := 0; i < 5; i++ {
		r := &store.AppRun{AppID: app.ID, TaskID: string(rune('a' + i)), State: "failed", StartedAt: int64(i)}
		st.CreateAppRun(ctx, r)
		if i != 1 { // launch 2 is still running
			st.ArchiveAppRun(ctx, r.ID, "x", false)
		}
	}
	if err := st.PruneAppRuns(ctx, app.ID, 2); err != nil {
		t.Fatal(err)
	}
	runs, _ := st.AppRuns(ctx, app.ID, 10)
	var nums []int64
	for _, r := range runs {
		nums = append(nums, r.Number)
	}
	if len(nums) != 3 || nums[0] != 5 || nums[1] != 4 || nums[2] != 2 {
		t.Fatalf("kept %v", nums)
	}
}

func TestFilterLog(t *testing.T) {
	log := "2026-01-01T00:00:00.5Z a\n  continued\n2026-01-01T00:01:00.5Z b\n2026-01-01T00:02:00.5Z c\n"
	since := time.Date(2026, 1, 1, 0, 1, 0, 0, time.UTC).Unix()
	if got := filterLog(log, 0, since); got != "2026-01-01T00:01:00.5Z b\n2026-01-01T00:02:00.5Z c\n" {
		t.Fatalf("since: %q", got)
	}
	if got := filterLog(log, 2, 0); got != "2026-01-01T00:01:00.5Z b\n2026-01-01T00:02:00.5Z c\n" {
		t.Fatalf("tail: %q", got)
	}
	if got := filterLog(log, 0, since+3600); got != "" {
		t.Fatalf("future since: %q", got)
	}
	if s, cut := trimLog("one\ntwo\nthree\n", 9); s != "three\n" || !cut {
		t.Fatalf("trim: %q", s)
	}
}
