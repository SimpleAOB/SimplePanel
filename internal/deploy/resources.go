package deploy

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/SimpleAOB/SimplePanel/internal/store"
)

// Memory limits keep one busy service from starving the whole server.
//
// A service's memory limit (App.MemoryMB) is one of:
//
//	 0  Automatic: a default sized to this server (see Capacity.DefaultMB)
//	-1  No limit (Advanced view only)
//	>0  that many MB
//
// The server keeps some memory back for the system itself (the kernel,
// SSH, Docker and Swarm). Running out of it is what locks a server up:
// Docker stops answering, Swarm's internal database stalls and even SSH
// hangs. Services share what is left ("available for services").

// NoMemoryLimit is the MemoryMB value for "no limit".
const NoMemoryLimit = -1

// MinMemoryMB is the smallest limit Docker accepts (6 MB), rounded up to
// something that can actually run a process.
const MinMemoryMB = 16

// ResourceSettings are the server-wide resource options.
type ResourceSettings struct {
	// ReserveMB is the memory kept for the system; 0 picks it
	// automatically (15% of RAM, at least 384 MB and at most 2 GB).
	ReserveMB int `json:"reserveMb"`
	// NoAutoLimits turns off automatic limits: services without their own
	// limit then have none (the behaviour before automatic limits).
	NoAutoLimits bool `json:"noAutoLimits"`
}

const resourceKey = "resources"

// LoadResourceSettings reads the resource settings.
func LoadResourceSettings(ctx context.Context, st *store.Store) ResourceSettings {
	var r ResourceSettings
	if v, _ := st.Setting(ctx, resourceKey); v != "" {
		_ = json.Unmarshal([]byte(v), &r)
	}
	return r
}

// SaveResourceSettings validates and stores the resource settings.
func SaveResourceSettings(ctx context.Context, st *store.Store, r ResourceSettings, memTotalMB int) error {
	if r.ReserveMB < 0 {
		return fmt.Errorf("reserved memory can't be negative")
	}
	if r.ReserveMB > 0 && r.ReserveMB < 128 {
		return fmt.Errorf("keep at least 128 MB for the system")
	}
	if memTotalMB > 0 && r.ReserveMB > memTotalMB-PanelOverheadMB-128 {
		return fmt.Errorf("that leaves less than 128 MB for services; this server has %d MB", memTotalMB)
	}
	b, _ := json.Marshal(r)
	return st.SetSetting(ctx, resourceKey, string(b))
}

// Capacity is how this server's memory is shared out.
type Capacity struct {
	MemTotalMB  int  `json:"memTotalMb"`  // the server's RAM
	ReserveMB   int  `json:"reserveMb"`   // kept for the system
	AutoReserve bool `json:"autoReserve"` // ReserveMB was picked automatically
	PanelMB     int  `json:"panelMb"`     // SimplePanel and Traefik themselves
	AvailableMB int  `json:"availableMb"` // for services: RAM minus the reserve and PanelMB
	AutoLimits  bool `json:"autoLimits"`  // services without a limit get DefaultMB
	// Defaults is the automatic limit per kind ("app", "postgres", ...).
	Defaults map[string]int `json:"defaults"`
}

// AutoReserveMB is the memory kept for the system on a server with
// totalMB of RAM: 15%, at least 384 MB, at most 2 GB.
//
// 384 MB covers the kernel, systemd, SSH, Docker and Swarm on a small
// server; less than that and a 1 GB server swaps Docker itself out.
func AutoReserveMB(totalMB int) int {
	return min(max(totalMB*15/100, 384), 2048)
}

// PanelOverheadMB is what SimplePanel and Traefik use themselves (about
// 40 MB each when idle, more while building or serving traffic). They run
// inside the same server-wide cap as services, so it isn't offered to them.
const PanelOverheadMB = 96

// NewCapacity works out the capacity of a server with totalMB of RAM.
func NewCapacity(totalMB int, s ResourceSettings) Capacity {
	c := Capacity{MemTotalMB: totalMB, ReserveMB: s.ReserveMB, AutoLimits: !s.NoAutoLimits}
	if c.ReserveMB <= 0 {
		c.ReserveMB, c.AutoReserve = AutoReserveMB(totalMB), true
	}
	c.PanelMB = PanelOverheadMB
	c.AvailableMB = max(totalMB-c.ReserveMB-c.PanelMB, 128)
	c.Defaults = map[string]int{}
	for _, k := range []string{"app", "postgres", "mysql", "mariadb", "mongo", "redis"} {
		c.Defaults[k] = c.defaultFor(k)
	}
	return c
}

// defaultFor sizes the automatic limit of a kind of service.
//
// Apps get a quarter of what services may use (128 MB to 512 MB);
// databases get half (256 MB to 1 GB), and at least what the engine needs
// to start with its default settings. Nothing gets more than the whole
// space for services.
func (c Capacity) defaultFor(kind string) int {
	a := c.AvailableMB
	var mb int
	switch kind {
	case "redis":
		mb = clamp(a/4, 128, 512)
	case "postgres", "mariadb":
		mb = clamp(a/2, 256, 1024)
	case "mysql": // MySQL 8 needs about 350 MB to start
		mb = clamp(a/2, 384, 1024)
	case "mongo": // WiredTiger's cache alone wants 256 MB
		mb = clamp(a/2, 512, 1024)
	default:
		mb = clamp(a/4, 128, 512)
	}
	return max(min(mb, a), MinMemoryMB) / 32 * 32
}

func clamp(v, lo, hi int) int { return min(max(v, lo), hi) }

// DefaultMB is the automatic limit for app.
func (c Capacity) DefaultMB(app *store.App) int {
	k := app.Kind
	if _, ok := c.Defaults[k]; !ok {
		k = "app"
	}
	return c.Defaults[k]
}

// EffectiveMB is the memory limit app runs with: its own, the automatic
// default, or 0 for no limit.
func (c Capacity) EffectiveMB(app *store.App) int {
	switch {
	case app.MemoryMB > 0:
		return app.MemoryMB
	case app.MemoryMB == 0 && c.AutoLimits && c.MemTotalMB > 0:
		return c.DefaultMB(app)
	}
	return 0
}

// capacity returns the server's capacity, or false when it isn't known
// (then services only get the limits set on them).
func (m *Manager) capacity() (Capacity, bool) {
	if m.Capacity == nil {
		return Capacity{}, false
	}
	c := m.Capacity()
	return c, c.MemTotalMB > 0
}

// memoryLimitMB is the limit to put on app's containers, 0 for none.
func (m *Manager) memoryLimitMB(app *store.App) int {
	if app.MemoryMB > 0 {
		return app.MemoryMB
	}
	c, ok := m.capacity()
	if !ok {
		return 0
	}
	return c.EffectiveMB(app)
}

// buildMemoryMB caps image builds: a build (npm install, compilers) is
// often the hungriest thing a small server runs. Builds may use what
// services may use, at least 512 MB.
func (m *Manager) buildMemoryMB() int {
	c, ok := m.capacity()
	if !ok {
		return 0
	}
	return max(c.AvailableMB, 512)
}

// Usage is what services are set to use, for capacity checks.
type Usage struct {
	CommittedMB int // sum of limits of services that should be running
	Unlimited   int // services with no limit at all
}

// Committed adds up the memory limits of every service that should be
// running (limit × replicas). skip leaves one app out, so a change to it
// can be measured.
func (c Capacity) Committed(apps []store.App, skip int64) Usage {
	var u Usage
	for i := range apps {
		a := &apps[i]
		if a.ID == skip || a.Replicas <= 0 {
			continue
		}
		mb := c.EffectiveMB(a)
		if mb == 0 {
			u.Unlimited++
			continue
		}
		u.CommittedMB += mb * replicasOf(a)
	}
	return u
}

func replicasOf(a *store.App) int {
	if a.IsDatabase() {
		return min(max(a.Replicas, 0), 1)
	}
	return max(a.Replicas, 0)
}

// ReplicasOf is how many containers app runs when started.
func ReplicasOf(a *store.App) int { return replicasOf(a) }
