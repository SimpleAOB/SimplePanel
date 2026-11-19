package deploy

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/SimpleAOB/SimplePanel/internal/docker"
	"github.com/SimpleAOB/SimplePanel/internal/store"
)

func TestCapacityOneGigServer(t *testing.T) {
	// A "1 GB" droplet reports about 960 MB.
	c := NewCapacity(961, ResourceSettings{})
	if c.ReserveMB != 384 || c.AvailableMB != 577 {
		t.Fatalf("reserve %d, available %d", c.ReserveMB, c.AvailableMB)
	}
	want := map[string]int{"app": 128, "redis": 128, "postgres": 288, "mariadb": 288, "mysql": 384, "mongo": 512}
	for k, v := range want {
		if c.Defaults[k] != v {
			t.Errorf("%s default = %d, want %d", k, c.Defaults[k], v)
		}
	}
	for k, v := range c.Defaults {
		if v > c.AvailableMB {
			t.Errorf("%s default %d exceeds available %d", k, v, c.AvailableMB)
		}
	}
}

func TestCapacityBigServers(t *testing.T) {
	c := NewCapacity(3931, ResourceSettings{}) // "4 GB"
	if c.ReserveMB != 589 || c.Defaults["app"] != 512 || c.Defaults["postgres"] != 1024 {
		t.Fatalf("4 GB: %+v", c)
	}
	c = NewCapacity(64000, ResourceSettings{})
	if c.ReserveMB != 2048 {
		t.Fatalf("reserve should cap at 2 GB, got %d", c.ReserveMB)
	}
	c = NewCapacity(2000, ResourceSettings{ReserveMB: 1000})
	if c.AutoReserve || c.AvailableMB != 1000 {
		t.Fatalf("manual reserve ignored: %+v", c)
	}
}

func TestEffectiveAndCommitted(t *testing.T) {
	c := NewCapacity(961, ResourceSettings{})
	apps := []store.App{
		{ID: 1, Kind: "app", Replicas: 2},                    // auto 128 × 2
		{ID: 2, Kind: "mysql", Replicas: 1},                  // auto 384
		{ID: 3, Kind: "app", Replicas: 1, MemoryMB: 200},     // own limit
		{ID: 4, Kind: "app", Replicas: 1, MemoryMB: -1},      // no limit
		{ID: 5, Kind: "app", Replicas: 0},                    // stopped
		{ID: 6, Kind: "postgres", Replicas: 3, MemoryMB: 64}, // databases run 1
	}
	u := c.Committed(apps, 0)
	if u.CommittedMB != 256+384+200+64 || u.Unlimited != 1 {
		t.Fatalf("committed %+v", u)
	}
	if got := c.Committed(apps, 2).CommittedMB; got != 256+200+64 {
		t.Fatalf("skip: %d", got)
	}
	off := NewCapacity(961, ResourceSettings{NoAutoLimits: true})
	if off.EffectiveMB(&apps[0]) != 0 || off.EffectiveMB(&apps[2]) != 200 {
		t.Fatal("auto limits off should only keep explicit limits")
	}
}

func TestServiceSpecMemoryLimit(t *testing.T) {
	m := &Manager{cfg: Config{Network: "simplepanel"}}
	app := &store.App{ID: 1, ProjectName: "p", Name: "web", Kind: "app", Replicas: 1}
	spec, err := m.ServiceSpec(app, "nginx")
	if err != nil {
		t.Fatal(err)
	}
	if spec.TaskTemplate.Resources != nil {
		t.Fatal("no capacity known: no limit expected")
	}
	m.Capacity = func() Capacity { return NewCapacity(961, ResourceSettings{}) }
	spec, _ = m.ServiceSpec(app, "nginx")
	if spec.TaskTemplate.Resources == nil || spec.TaskTemplate.Resources.Limits.MemoryBytes != 128<<20 {
		t.Fatalf("want the automatic 128 MB limit, got %+v", spec.TaskTemplate.Resources)
	}
	app.MemoryMB = NoMemoryLimit
	spec, _ = m.ServiceSpec(app, "nginx")
	if spec.TaskTemplate.Resources != nil {
		t.Fatal("-1 must mean no limit")
	}
}

func TestDeployCoalescesQueued(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	m := NewManager(Config{ProxyDir: t.TempDir(), WorkDir: t.TempDir()}, st, docker.New(filepath.Join(t.TempDir(), "none.sock")), slog.New(slog.NewTextHandler(io.Discard, nil)))
	p, _ := st.CreateProject(ctx, "shop")
	app := &store.App{ProjectID: p.ID, Name: "web", Source: "image", Image: "nginx:1", Replicas: 1}
	if err := st.CreateApp(ctx, app); err != nil {
		t.Fatal(err)
	}
	// A deployment is running (it holds the app's lock): new requests queue
	// one deployment, not one each.
	lock := m.lockApp(app.ID)
	lock.Lock()
	ids := map[int64]bool{}
	for i := 0; i < 5; i++ {
		d, err := m.Deploy(ctx, app.ID, "webhook")
		if err != nil {
			t.Fatal(err)
		}
		ids[d.ID] = true
	}
	lock.Unlock()
	if len(ids) != 1 {
		t.Fatalf("5 requests queued %d deployments, want 1", len(ids))
	}
	for id := range ids {
		if _, err := m.WaitDeployment(ctx, id); err != nil {
			t.Fatal(err)
		}
	}
	// Once it has started, the next request queues a new one.
	d, err := m.Deploy(ctx, app.ID, "manual")
	if err != nil || ids[d.ID] {
		t.Fatalf("new deployment after the queued one started: %v %v", d, err)
	}
	m.WaitDeployment(ctx, d.ID)
}
