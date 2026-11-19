package api

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"slices"
	"strconv"

	"github.com/SimpleAOB/SimplePanel/internal/deploy"
	"github.com/SimpleAOB/SimplePanel/internal/monitor"
	"github.com/SimpleAOB/SimplePanel/internal/store"
)

// PressurePct is the share of the server's memory in use at which deploys
// and starts are refused (owners and admins can override): starting more
// on a server this full is how it locks up.
const PressurePct = 90

// memoryNow reads the server's memory, if monitoring is running.
func (s *Server) memoryNow() (monitor.MemInfo, bool) {
	if s.monitor == nil {
		return monitor.MemInfo{}, false
	}
	mi, err := s.monitor.Memory()
	return mi, err == nil && mi.Total > 0
}

func (s *Server) capacity(ctx context.Context) (deploy.Capacity, bool) {
	mi, ok := s.memoryNow()
	if !ok {
		return deploy.Capacity{}, false
	}
	return deploy.NewCapacity(int(mi.Total>>20), deploy.LoadResourceSettings(ctx, s.store)), true
}

// getCapacity describes how the server's memory is shared out. ?app=ID
// also reports what the other services use, for that app's memory
// picker.
func (s *Server) getCapacity(w http.ResponseWriter, r *http.Request, u *store.User) {
	c, ok := s.capacity(r.Context())
	if !ok {
		writeJSON(w, 200, map[string]any{"known": false})
		return
	}
	mi, _ := s.memoryNow()
	apps, err := s.store.Apps(r.Context(), 0)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	var skip int64
	if v := r.URL.Query().Get("app"); v != "" {
		skip, _ = strconv.ParseInt(v, 10, 64)
	}
	all := c.Committed(apps, 0)
	others := c.Committed(apps, skip)
	out := map[string]any{
		"known":             true,
		"capacity":          c,
		"usedMb":            int((mi.Total - mi.Available) >> 20),
		"freeMb":            int(mi.Available >> 20),
		"swapMb":            int(mi.SwapTotal >> 20),
		"swapUsedMb":        int((mi.SwapTotal - mi.SwapFree) >> 20),
		"committedMb":       all.CommittedMB,
		"unlimited":         all.Unlimited,
		"othersCommittedMb": others.CommittedMB,
		"pressurePct":       PressurePct,
		"settings":          deploy.LoadResourceSettings(r.Context(), s.store),
		// The server-wide cap the installer set on all containers (0: none).
		"hostCapMb": hostCapMB(),
	}
	// The services the user can see, biggest allowance first.
	type svc struct {
		ID        int64  `json:"id"`
		ProjectID int64  `json:"projectId"`
		Project   string `json:"project"`
		Name      string `json:"name"`
		Kind      string `json:"kind"`
		LimitMB   int    `json:"limitMb"` // effective, per replica; 0 = no limit
		Auto      bool   `json:"auto"`
		Replicas  int    `json:"replicas"`
	}
	visible := map[int64]bool{}
	if ps, err := s.visibleProjects(r.Context(), u); err == nil {
		for _, p := range ps {
			visible[p.ID] = true
		}
	}
	list := []svc{}
	for i := range apps {
		a := &apps[i]
		if !visible[a.ProjectID] {
			continue
		}
		list = append(list, svc{ID: a.ID, ProjectID: a.ProjectID, Project: a.ProjectName, Name: a.Name, Kind: a.Kind,
			LimitMB: c.EffectiveMB(a), Auto: a.MemoryMB == 0 && c.AutoLimits, Replicas: deploy.ReplicasOf(a)})
	}
	slices.SortStableFunc(list, func(a, b svc) int { return b.LimitMB*max(b.Replicas, 1) - a.LimitMB*max(a.Replicas, 1) })
	out["services"] = list
	writeJSON(w, 200, out)
}

func (s *Server) putCapacitySettings(w http.ResponseWriter, r *http.Request, u *store.User) {
	if !requireAdmin(w, u) {
		return
	}
	var in deploy.ResourceSettings
	if !readJSON(w, r, &in) {
		return
	}
	total := 0
	if mi, ok := s.memoryNow(); ok {
		total = int(mi.Total >> 20)
	}
	if err := deploy.SaveResourceSettings(r.Context(), s.store, in, total); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, in)
}

// memoryGuard checks that the server has room before a deploy, start or
// restart of app. It refuses (409, code "memory_pressure") when the server
// is already nearly out of memory, unless an owner or admin passes
// ?force=1. Otherwise it returns warnings to show alongside the result.
func (s *Server) memoryGuard(w http.ResponseWriter, r *http.Request, u *store.User, app *store.App) ([]string, bool) {
	mi, ok := s.memoryNow()
	if !ok {
		return nil, true
	}
	usedPct := int((mi.Total - mi.Available) * 100 / mi.Total)
	if usedPct >= PressurePct && r.URL.Query().Get("force") != "1" {
		writeJSON(w, 409, map[string]any{
			"error": fmt.Sprintf("This server is using %d%% of its memory (%d MB free). Starting more now could make it stop responding. "+
				"Stop or shrink another service first, or give the server more memory.", usedPct, mi.Available>>20),
			"code":     "memory_pressure",
			"canForce": u.IsAdmin(),
		})
		return nil, false
	}
	if usedPct >= PressurePct && !u.IsAdmin() {
		writeErr(w, 403, "only owners and admins can deploy while the server is nearly out of memory")
		return nil, false
	}
	c, ok := s.capacity(r.Context())
	if !ok {
		return nil, true
	}
	var warn []string
	if usedPct >= PressurePct {
		warn = append(warn, fmt.Sprintf("Started anyway with the server at %d%% memory.", usedPct))
	}
	apps, err := s.store.Apps(r.Context(), 0)
	if err != nil {
		return warn, true
	}
	mb := c.EffectiveMB(app)
	after := c.Committed(apps, app.ID).CommittedMB + mb*max(deploy.ReplicasOf(app), 1)
	if after > c.AvailableMB*3/2 { // limits are ceilings: a little over is normal
		warn = append(warn, fmt.Sprintf("Services on this server are now allowed %d MB in total, much more than the %d MB set aside for them. "+
			"If several get busy at once, the biggest is restarted. Consider smaller limits or a bigger server.", after, c.AvailableMB))
	}
	if mb > 0 && !app.IsDatabase() && int(mi.Available>>20) < mb {
		warn = append(warn, fmt.Sprintf("Only %d MB is free. Updates start the new version before stopping the old one, so for a moment this service needs up to %d MB more.",
			mi.Available>>20, mb))
	}
	return warn, true
}

// memoryPressure refuses automated deploys (AI assistants) while the
// server is nearly out of memory. People can still override in the panel.
func (s *Server) memoryPressure() error {
	mi, ok := s.memoryNow()
	if !ok {
		return nil
	}
	if pct := int((mi.Total - mi.Available) * 100 / mi.Total); pct >= PressurePct {
		return fmt.Errorf("not deploying: this server is using %d%% of its memory (%d MB free), and starting more could make it stop responding. "+
			"Stop or shrink a service first, or ask an owner or admin to deploy from the panel", pct, mi.Available>>20)
	}
	return nil
}

// hostCapMB is the memory cap on all containers set by the installer
// (simplepanel.slice), 0 when there is none.
func hostCapMB() int {
	n, _ := strconv.Atoi(os.Getenv("SP_MEMORY_CAP_MB"))
	return max(n, 0)
}
