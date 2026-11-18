package deploy

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/SimpleAOB/SimplePanel/internal/docker"
	"github.com/SimpleAOB/SimplePanel/internal/store"
)

// Per-launch logs. Every Swarm task is one start of a service's container:
// a deploy, a restart, or a crash and reschedule. Docker only keeps the last
// few tasks of a service, so each one is recorded as an AppRun and, once it
// stops, its logs are copied into the database.
const (
	RunsKept      = 30      // launches kept per service
	RunLogMax     = 1 << 20 // bytes of log kept per launch (the end is kept)
	runLogLines   = 20000   // lines asked from Docker when archiving
	runSyncPeriod = 15 * time.Second
)

// finished reports whether a task state is final.
func finished(state string) bool {
	switch state {
	case "complete", "failed", "shutdown", "rejected", "orphaned", "remove":
		return true
	}
	return false
}

// SyncRuns records the service's current tasks as launches and archives the
// logs of launches that have stopped.
func (m *Manager) SyncRuns(ctx context.Context, app *store.App) error {
	tasks, err := m.docker.ServiceTasks(ctx, app.ServiceName())
	if errors.Is(err, docker.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	// Oldest first so numbers follow start order.
	sort.Slice(tasks, func(i, j int) bool { return tasks[i].CreatedAt.Before(tasks[j].CreatedAt) })
	m.runsMu.Lock()
	defer m.runsMu.Unlock()
	for _, t := range tasks {
		if err := m.syncTask(ctx, app, t); err != nil {
			return err
		}
	}
	return m.store.PruneAppRuns(ctx, app.ID, RunsKept)
}

func (m *Manager) syncTask(ctx context.Context, app *store.App, t docker.Task) error {
	r, err := m.store.AppRunByTask(ctx, t.ID)
	if errors.Is(err, store.ErrNotFound) {
		r = &store.AppRun{AppID: app.ID, TaskID: t.ID, Slot: t.Slot, Image: t.Spec.ContainerSpec.Image, StartedAt: t.CreatedAt.Unix()}
		r.DeploymentID, _ = m.store.DeploymentBefore(ctx, app.ID, r.StartedAt)
		applyTask(r, t)
		if err := m.store.CreateAppRun(ctx, r); err != nil {
			return err
		}
	} else if err != nil {
		return err
	} else if r.Archived {
		return nil
	} else if changed := applyTask(r, t); changed {
		if err := m.store.UpdateAppRunState(ctx, r); err != nil {
			return err
		}
	}
	if !finished(r.State) {
		return nil
	}
	return m.archiveRun(ctx, r)
}

// applyTask copies a task's state into r and reports whether anything changed.
func applyTask(r *store.AppRun, t docker.Task) bool {
	before := *r
	r.State = t.Status.State
	r.Message = t.Status.Err
	if r.Message == "" && finished(r.State) {
		r.Message = t.Status.Message
	}
	if finished(r.State) {
		end := t.Status.Timestamp.Unix()
		r.EndedAt = &end
		if t.Status.ContainerStatus.ContainerID != "" {
			code := t.Status.ContainerStatus.ExitCode
			r.ExitCode = &code
		}
	}
	return r.State != before.State || r.Message != before.Message || (r.EndedAt == nil) != (before.EndedAt == nil) ||
		(r.ExitCode == nil) != (before.ExitCode == nil)
}

func (m *Manager) archiveRun(ctx context.Context, r *store.AppRun) error {
	var log string
	if r.ExitCode != nil { // a container ran, so there may be logs
		var err error
		log, err = m.docker.TaskLogs(ctx, r.TaskID, runLogLines, 0, 4*RunLogMax)
		if err != nil && !errors.Is(err, docker.ErrNotFound) {
			return err // try again on the next sync
		}
	}
	log, truncated := trimLog(log, RunLogMax)
	return m.store.ArchiveAppRun(ctx, r.ID, log, truncated)
}

// trimLog keeps the last max bytes of s, starting at a line boundary.
func trimLog(s string, max int) (string, bool) {
	if len(s) <= max {
		return s, false
	}
	s = s[len(s)-max:]
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[i+1:]
	}
	return s, true
}

// RunLogs returns a launch's logs: from Docker while it runs, from the
// database once it has stopped.
func (m *Manager) RunLogs(ctx context.Context, r *store.AppRun, tail int, since int64) (string, error) {
	if !r.Archived {
		log, err := m.docker.TaskLogs(ctx, r.TaskID, tail, since, 8<<20)
		if errors.Is(err, docker.ErrNotFound) {
			return "", nil
		}
		return log, err
	}
	return filterLog(r.Log, tail, since), nil
}

// filterLog applies tail and since to a stored log. Lines start with an
// RFC 3339 timestamp; lines without one stay with the line before them.
func filterLog(s string, tail int, since int64) string {
	if since > 0 {
		cut := time.Unix(since, 0)
		lines := strings.SplitAfter(s, "\n")
		for i, l := range lines {
			v, _, _ := strings.Cut(l, " ")
			if ts, err := time.Parse(time.RFC3339Nano, v); err == nil && !ts.Before(cut) {
				s = strings.Join(lines[i:], "")
				break
			}
			if i == len(lines)-1 {
				s = ""
			}
		}
	}
	if tail > 0 {
		lines := strings.SplitAfter(s, "\n")
		if lines[len(lines)-1] == "" {
			lines = lines[:len(lines)-1]
		}
		if len(lines) > tail {
			s = strings.Join(lines[len(lines)-tail:], "")
		}
	}
	return s
}

// StartRunArchiver keeps launches and their logs recorded for every service.
func (m *Manager) StartRunArchiver(ctx context.Context) {
	go func() {
		t := time.NewTicker(runSyncPeriod)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				m.syncAllRuns(ctx)
			}
		}
	}()
}

func (m *Manager) syncAllRuns(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, runSyncPeriod*4)
	defer cancel()
	apps, err := m.store.Apps(ctx, 0)
	if err != nil {
		m.log.Error("runs: list apps", "err", err)
		return
	}
	for i := range apps {
		if err := m.SyncRuns(ctx, &apps[i]); err != nil && ctx.Err() == nil {
			m.log.Debug("runs: sync", "service", apps[i].ServiceName(), "err", err)
		}
	}
}
