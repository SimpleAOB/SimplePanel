package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"time"

	"github.com/SimpleAOB/SimplePanel/internal/store"
)

// v02Tools are the MCP tools for monitoring and backups. They go through
// the web API's handlers, so roles and scopes are checked the same way.
func v02Tools() []mcpTool {
	return []mcpTool{
		{
			name: "get_metrics", title: "Get CPU, memory, disk and network history", scope: store.ScopeRead, readOnly: true,
			description: "Resource usage for one service, or for the whole server when service is left out. Returns current, average and peak values plus up to 60 points over the range. CPU is percent of the whole server's CPU; memory and disk are bytes; network is bytes per second.",
			props: map[string]any{
				"service": prop("string", `Service as "project/name" or numeric id. Leave out for the server.`),
				"range":   enumProp("How far back to look. Default 1h.", "1h", "6h", "24h", "7d", "30d"),
			},
			run: func(c *mcpCall) (any, error) {
				var a struct {
					Service string `json:"service"`
					Range   string `json:"range"`
				}
				if err := c.bind(&a); err != nil {
					return nil, err
				}
				target := "/?range=" + a.Range
				var raw json.RawMessage
				var err error
				if a.Service != "" {
					app, err := c.service(a.Service)
					if err != nil {
						return nil, err
					}
					raw, err = c.invoke(c.s.appMetrics, "GET", target, map[string]string{"id": strconv.FormatInt(app.ID, 10)}, nil)
					if err != nil {
						return nil, err
					}
				} else if raw, err = c.invoke(c.s.serverMetrics, "GET", target, nil, nil); err != nil {
					return nil, err
				}
				var m struct {
					Range    string          `json:"range"`
					Samples  []store.Sample  `json:"samples"`
					Host     json.RawMessage `json:"host"`
					MemLimit int             `json:"memoryLimitMb"`
					CPULimit float64         `json:"cpuLimit"`
				}
				if err := json.Unmarshal(raw, &m); err != nil {
					return nil, err
				}
				out := summarizeSamples(m.Samples)
				out["range"] = m.Range
				if a.Service != "" {
					out["service"] = c.target
					out["memoryLimitMb"], out["cpuLimit"] = m.MemLimit, m.CPULimit
				} else {
					out["host"] = m.Host
				}
				if len(m.Samples) == 0 {
					out["note"] = "No samples yet. Monitoring records every 30 seconds; a service that isn't running has none."
				}
				return out, nil
			},
		},
		{
			name: "list_backups", title: "List a service's backups", scope: store.ScopeRead, readOnly: true,
			description: "Backups of a database or of an app's volumes, newest first, with schedules, recent restores and the destinations that can be used. Backup ids work with get_backup and restore_backup.",
			props:       map[string]any{"service": pService},
			required:    []string{"service"},
			run: func(c *mcpCall) (any, error) {
				var a struct {
					Service string `json:"service"`
				}
				if err := c.bind(&a); err != nil {
					return nil, err
				}
				app, err := c.service(a.Service)
				if err != nil {
					return nil, err
				}
				raw, err := c.invoke(c.s.appBackups, "GET", "/", map[string]string{"id": strconv.FormatInt(app.ID, 10)}, nil)
				if err != nil {
					return nil, err
				}
				var ov struct {
					Kind         string            `json:"kind"`
					Backups      []store.Backup    `json:"backups"`
					Jobs         []store.BackupJob `json:"jobs"`
					Restores     []store.Restore   `json:"restores"`
					Destinations []map[string]any  `json:"destinations"`
				}
				if err := json.Unmarshal(raw, &ov); err != nil {
					return nil, err
				}
				if ov.Kind == "" {
					return map[string]any{"service": c.target, "note": "This service has no database or volumes, so there is nothing to back up."}, nil
				}
				bs := []map[string]any{}
				for _, b := range ov.Backups {
					x := map[string]any{"id": b.ID, "status": b.Status, "destination": b.Destination, "size": b.Size,
						"encrypted": b.Encrypted, "trigger": b.Trigger, "createdAt": time.Unix(b.CreatedAt, 0).UTC().Format(time.RFC3339)}
					bs = append(bs, x)
				}
				jobs := []map[string]any{}
				for _, j := range ov.Jobs {
					x := map[string]any{"destination": j.Destination, "schedule": j.Schedule, "keep": j.Keep, "enabled": j.Enabled}
					if j.NextRunAt != nil {
						x["nextRun"] = time.Unix(*j.NextRunAt, 0).UTC().Format(time.RFC3339)
					}
					jobs = append(jobs, x)
				}
				rs := []map[string]any{}
				for _, r := range ov.Restores {
					rs = append(rs, map[string]any{"id": r.ID, "status": r.Status, "by": r.Username, "createdAt": time.Unix(r.CreatedAt, 0).UTC().Format(time.RFC3339)})
				}
				return map[string]any{"service": c.target, "kind": ov.Kind, "backups": bs, "schedules": jobs,
					"restores": rs, "destinations": ov.Destinations}, nil
			},
		},
		{
			name: "backup_now", title: "Back up a service now", scope: store.ScopeManage, audit: true,
			description: "Starts a backup of a database or an app's volumes. Uses the destination from the service's schedule, or the only destination, unless destination is given. Returns a backup id; use get_backup to follow it, or set wait.",
			props: map[string]any{
				"service":     pService,
				"destination": prop("string", "Destination name or id. Optional when there is only one, or the service has a schedule."),
				"wait":        prop("boolean", "Wait up to 5 minutes for the backup to finish. Default false."),
			},
			required: []string{"service"},
			run: func(c *mcpCall) (any, error) {
				var a struct {
					Service     string `json:"service"`
					Destination string `json:"destination"`
					Wait        bool   `json:"wait"`
				}
				if err := c.bind(&a); err != nil {
					return nil, err
				}
				app, err := c.service(a.Service)
				if err != nil {
					return nil, err
				}
				destID, err := c.pickDestination(app.ID, a.Destination)
				if err != nil {
					return nil, err
				}
				raw, err := c.invoke(c.s.runBackup, "POST", "/", map[string]string{"id": strconv.FormatInt(app.ID, 10)}, map[string]int64{"destinationId": destID})
				if err != nil {
					return nil, err
				}
				var b store.Backup
				if err := json.Unmarshal(raw, &b); err != nil {
					return nil, err
				}
				if !a.Wait {
					return map[string]any{"backupId": b.ID, "status": b.Status, "destination": b.Destination}, nil
				}
				return c.waitBackup(b.ID, 5*time.Minute)
			},
		},
		{
			name: "restore_backup", title: "Restore a backup", scope: store.ScopeManage, destructive: true, audit: true,
			description: "Restores a backup from list_backups. With new_name (databases only), it creates a new database in the same project and loads the backup there, leaving the current one untouched. Without new_name it REPLACES the service's current data: that needs a full-access token and confirm set to the service's name. Prefer new_name unless the user asked to overwrite.",
			props: map[string]any{
				"service":   pService,
				"backup_id": prop("integer", "Backup id from list_backups."),
				"new_name":  prop("string", "Name for a new database to restore into. Leave out to replace the current data."),
				"confirm":   prop("string", "The service's name, required when replacing the current data."),
				"wait":      prop("boolean", "Wait up to 10 minutes for the restore to finish. Default false."),
			},
			required: []string{"service", "backup_id"},
			run: func(c *mcpCall) (any, error) {
				var a struct {
					Service  string `json:"service"`
					BackupID int64  `json:"backup_id"`
					NewName  string `json:"new_name"`
					Confirm  string `json:"confirm"`
					Wait     bool   `json:"wait"`
				}
				if err := c.bind(&a); err != nil {
					return nil, err
				}
				app, err := c.service(a.Service)
				if err != nil {
					return nil, err
				}
				if a.NewName == "" && !c.full() {
					return nil, errors.New("replacing a service's data needs a full-access token. Pass new_name to restore into a new database instead, or ask the user to restore it in SimplePanel")
				}
				body := map[string]any{"backupId": a.BackupID, "newName": a.NewName, "confirm": a.Confirm}
				raw, err := c.invoke(c.s.restoreBackup, "POST", "/", map[string]string{"id": strconv.FormatInt(app.ID, 10)}, body)
				if err != nil {
					return nil, err
				}
				var rs store.Restore
				if err := json.Unmarshal(raw, &rs); err != nil {
					return nil, err
				}
				if !a.Wait {
					return map[string]any{"restoreId": rs.ID, "status": rs.Status, "note": "Use get_backup with restore_id to follow it."}, nil
				}
				return c.waitRestore(rs.ID, 10*time.Minute)
			},
		},
		{
			name: "get_backup", title: "Get a backup or restore", scope: store.ScopeRead, readOnly: true,
			description: "Status and log of one backup (backup_id) or one restore (restore_id).",
			props: map[string]any{
				"backup_id":  prop("integer", "Backup id."),
				"restore_id": prop("integer", "Restore id."),
			},
			run: func(c *mcpCall) (any, error) {
				var a struct {
					BackupID  int64 `json:"backup_id"`
					RestoreID int64 `json:"restore_id"`
				}
				if err := c.bind(&a); err != nil {
					return nil, err
				}
				switch {
				case a.BackupID > 0:
					return c.backupView(a.BackupID)
				case a.RestoreID > 0:
					return c.restoreView(a.RestoreID)
				}
				return nil, errors.New("pass backup_id or restore_id")
			},
		},
	}
}

// pickDestination resolves a destination name or id, or picks the one the
// service's schedule uses, or the only one there is.
func (c *mcpCall) pickDestination(appID int64, ref string) (int64, error) {
	ds, err := c.s.store.BackupDestinations(c.ctx)
	if err != nil {
		return 0, err
	}
	if len(ds) == 0 {
		return 0, errors.New("no backup destination is set up. Ask an admin to add one in SimplePanel's Settings, under Backups")
	}
	if ref != "" {
		for _, d := range ds {
			if d.Name == ref || strconv.FormatInt(d.ID, 10) == ref {
				return d.ID, nil
			}
		}
		return 0, fmt.Errorf("destination %q not found", ref)
	}
	if jobs, err := c.s.store.BackupJobs(c.ctx, appID); err == nil && len(jobs) > 0 {
		return jobs[0].DestinationID, nil
	}
	if len(ds) == 1 {
		return ds[0].ID, nil
	}
	names := []string{}
	for _, d := range ds {
		names = append(names, d.Name)
	}
	return 0, fmt.Errorf("there are several destinations; pass destination as one of %q", names)
}

func (c *mcpCall) backupView(id int64) (map[string]any, error) {
	raw, err := c.invoke(c.s.getBackup, "GET", "/", map[string]string{"id": strconv.FormatInt(id, 10)}, nil)
	if err != nil {
		return nil, err
	}
	var b store.Backup
	if err := json.Unmarshal(raw, &b); err != nil {
		return nil, err
	}
	out := map[string]any{"backupId": b.ID, "status": b.Status, "service": b.Project + "/" + b.Service, "kind": b.Kind,
		"destination": b.Destination, "size": b.Size, "encrypted": b.Encrypted, "trigger": b.Trigger,
		"createdAt": time.Unix(b.CreatedAt, 0).UTC().Format(time.RFC3339), "log": tailText(b.Log, 40, 4000)}
	if b.FinishedAt != nil {
		out["finishedAt"] = time.Unix(*b.FinishedAt, 0).UTC().Format(time.RFC3339)
	}
	return out, nil
}

func (c *mcpCall) restoreView(id int64) (map[string]any, error) {
	raw, err := c.invoke(c.s.getRestore, "GET", "/", map[string]string{"id": strconv.FormatInt(id, 10)}, nil)
	if err != nil {
		return nil, err
	}
	var r store.Restore
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, err
	}
	out := map[string]any{"restoreId": r.ID, "status": r.Status, "by": r.Username,
		"createdAt": time.Unix(r.CreatedAt, 0).UTC().Format(time.RFC3339), "log": tailText(r.Log, 40, 4000)}
	if r.TargetAppID != nil && (r.AppID == nil || *r.TargetAppID != *r.AppID) {
		if t, err := c.s.store.App(c.ctx, *r.TargetAppID); err == nil {
			out["restoredInto"] = t.ProjectName + "/" + t.Name
		}
	}
	if r.FinishedAt != nil {
		out["finishedAt"] = time.Unix(*r.FinishedAt, 0).UTC().Format(time.RFC3339)
	}
	return out, nil
}

func (c *mcpCall) waitBackup(id int64, timeout time.Duration) (map[string]any, error) {
	return c.poll(timeout, func() (map[string]any, error) { return c.backupView(id) })
}

func (c *mcpCall) waitRestore(id int64, timeout time.Duration) (map[string]any, error) {
	return c.poll(timeout, func() (map[string]any, error) { return c.restoreView(id) })
}

// poll calls get until its status is no longer "running" or time runs out.
func (c *mcpCall) poll(timeout time.Duration, get func() (map[string]any, error)) (map[string]any, error) {
	deadline := time.Now().Add(timeout)
	for {
		v, err := get()
		if err != nil || v["status"] != "running" || time.Now().After(deadline) {
			if err == nil && v["status"] == "running" {
				v["note"] = "Still running; check again with get_backup."
			}
			return v, err
		}
		select {
		case <-c.ctx.Done():
			return v, nil
		case <-time.After(2 * time.Second):
		}
	}
}

// summarizeSamples reduces a sample series to current/avg/peak figures
// and at most 60 evenly spaced points.
func summarizeSamples(ss []store.Sample) map[string]any {
	out := map[string]any{"samples": len(ss)}
	if len(ss) == 0 {
		return out
	}
	type stat struct{ cur, sum, max float64 }
	var cpu, mem, rx, tx, disk stat
	add := func(s *stat, v float64) {
		s.cur = v
		s.sum += v
		s.max = math.Max(s.max, v)
	}
	for _, x := range ss {
		add(&cpu, x.CPU)
		add(&mem, float64(x.Mem))
		add(&rx, x.NetRx)
		add(&tx, x.NetTx)
		add(&disk, float64(x.DiskUsed))
	}
	n := float64(len(ss))
	r2 := func(v float64) float64 { return math.Round(v*100) / 100 }
	view := func(s stat) map[string]float64 {
		return map[string]float64{"current": r2(s.cur), "average": r2(s.sum / n), "peak": r2(s.max)}
	}
	last := ss[len(ss)-1]
	out["cpu"], out["memory"], out["netRx"], out["netTx"] = view(cpu), view(mem), view(rx), view(tx)
	if last.MemLimit > 0 {
		out["memoryLimit"] = last.MemLimit
	}
	if last.DiskTotal > 0 {
		out["disk"] = map[string]any{"used": last.DiskUsed, "total": last.DiskTotal}
	} else if disk.max > 0 {
		out["disk"] = map[string]any{"used": last.DiskUsed}
	}
	step := max(1, len(ss)/60)
	pts := []map[string]any{}
	for i := 0; i < len(ss); i += step {
		x := ss[i]
		pts = append(pts, map[string]any{"t": time.Unix(x.TS, 0).UTC().Format(time.RFC3339), "cpu": r2(x.CPU), "mem": x.Mem})
	}
	out["points"] = pts
	out["from"] = time.Unix(ss[0].TS, 0).UTC().Format(time.RFC3339)
	out["to"] = time.Unix(last.TS, 0).UTC().Format(time.RFC3339)
	return out
}
