// Package cron runs scheduled commands inside services' containers.
package cron

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/SimpleAOB/SimplePanel/internal/backup"
	"github.com/SimpleAOB/SimplePanel/internal/deploy"
	"github.com/SimpleAOB/SimplePanel/internal/docker"
	"github.com/SimpleAOB/SimplePanel/internal/notify"
	"github.com/SimpleAOB/SimplePanel/internal/store"
)

// Limits for jobs.
const (
	MaxJobsPerService = 20
	MaxCommand        = 4000
	DefaultTimeout    = 3600
	MaxTimeout        = 24 * 3600
	maxOutput         = 64 << 10 // the last 64 KB of output are kept
	maxConcurrent     = 4
)

// ErrRunning is returned when a job is started while it is still running.
var ErrRunning = errors.New("this job is still running")

// Service schedules and runs cron jobs.
type Service struct {
	store  *store.Store
	deploy *deploy.Manager
	docker *docker.Client
	notify *notify.Notifier
	log    *slog.Logger
	clock  func() time.Time

	mu      sync.Mutex
	running map[int64]bool
	slots   chan struct{}
}

func New(st *store.Store, dm *deploy.Manager, dc *docker.Client, n *notify.Notifier, log *slog.Logger) *Service {
	return &Service{store: st, deploy: dm, docker: dc, notify: n, log: log, clock: time.Now,
		running: map[int64]bool{}, slots: make(chan struct{}, maxConcurrent)}
}

// NextRun is when a schedule next fires after now.
func (s *Service) NextRun(schedule string) (*int64, error) {
	return backup.NextRun(schedule, s.clock())
}

// Validate checks a job's fields and fills in defaults.
func Validate(j *store.CronJob) error {
	j.Name = strings.TrimSpace(j.Name)
	if j.Name == "" || len(j.Name) > 60 || strings.ContainsAny(j.Name, "\r\n\t") {
		return errors.New("give the job a name of up to 60 characters")
	}
	j.Schedule = strings.TrimSpace(j.Schedule)
	if _, err := backup.ParseSchedule(j.Schedule); err != nil {
		return err
	}
	j.Command = strings.TrimSpace(j.Command)
	if j.Command == "" {
		return errors.New("enter a command to run")
	}
	if len(j.Command) > MaxCommand || strings.ContainsRune(j.Command, 0) {
		return fmt.Errorf("the command must be under %d characters", MaxCommand)
	}
	if j.Timeout == 0 {
		j.Timeout = DefaultTimeout
	}
	if j.Timeout < 1 || j.Timeout > MaxTimeout {
		return errors.New("timeout must be between 1 second and 24 hours")
	}
	return nil
}

// Start runs due jobs every 20 seconds until ctx ends.
func (s *Service) Start(ctx context.Context) {
	go func() {
		t := time.NewTicker(20 * time.Second)
		defer t.Stop()
		for {
			s.runDue(ctx)
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}()
}

func (s *Service) runDue(ctx context.Context) {
	now := s.clock()
	jobs, err := s.store.DueCronJobs(ctx, now.Unix())
	if err != nil {
		s.log.Error("cron scheduler", "err", err)
		return
	}
	for _, j := range jobs {
		next, err := s.NextRun(j.Schedule)
		if err != nil {
			next = nil
		}
		_ = s.store.SetCronNext(ctx, j.ID, next)
		if j.NextRunAt == nil {
			continue // newly scheduled (e.g. after a restart): wait for its time
		}
		if _, err := s.Run(ctx, j.ID, "schedule"); errors.Is(err, ErrRunning) {
			_, _ = s.store.CreateCronRun(ctx, j.ID, "skipped", "schedule")
		}
	}
}

// Run starts a job now in the background and returns its run.
func (s *Service) Run(ctx context.Context, jobID int64, trigger string) (*store.CronRun, error) {
	s.mu.Lock()
	if s.running[jobID] {
		s.mu.Unlock()
		return nil, ErrRunning
	}
	s.running[jobID] = true
	s.mu.Unlock()
	r, err := s.store.CreateCronRun(ctx, jobID, "running", trigger)
	if err != nil {
		s.done(jobID)
		return nil, err
	}
	go s.exec(jobID, r.ID)
	return r, nil
}

func (s *Service) done(jobID int64) {
	s.mu.Lock()
	delete(s.running, jobID)
	s.mu.Unlock()
}

// tail keeps the last maxOutput bytes written to it.
type tail struct {
	mu   sync.Mutex
	b    []byte
	cut  bool
	dirt bool
}

func (t *tail) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.b = append(t.b, p...)
	if len(t.b) > maxOutput {
		t.b = append(t.b[:0:0], t.b[len(t.b)-maxOutput:]...)
		t.cut = true
	}
	t.dirt = true
	return len(p), nil
}

func (t *tail) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.dirt = false
	if t.cut {
		return "[earlier output cut]\n" + string(t.b)
	}
	return string(t.b)
}

func (t *tail) changed() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.dirt
}

// wrapper runs the command with sh, under `timeout` when the image has it.
// The command is passed as an argument, so it needs no quoting.
const wrapper = `if command -v timeout >/dev/null 2>&1; then exec timeout "$1" sh -c "$2"; else exec sh -c "$2"; fi`

func (s *Service) exec(jobID, runID int64) {
	defer s.done(jobID)
	bg := context.Background()
	select {
	case s.slots <- struct{}{}:
		defer func() { <-s.slots }()
	case <-time.After(time.Hour):
		_ = s.store.FinishCronRun(bg, runID, "failed", nil, "[too many jobs were running; gave up waiting]")
		return
	}
	job, err := s.store.CronJob(bg, jobID)
	if err != nil {
		_ = s.store.FinishCronRun(bg, runID, "failed", nil, err.Error())
		return
	}
	app, err := s.store.App(bg, job.AppID)
	if err != nil {
		_ = s.store.FinishCronRun(bg, runID, "failed", nil, err.Error())
		return
	}
	out := &tail{}
	code, err := func() (int, error) {
		ctx, cancel := context.WithTimeout(bg, time.Duration(job.Timeout)*time.Second+30*time.Second)
		defer cancel()
		id, err := s.deploy.RunningContainer(ctx, app)
		if err != nil {
			return -1, err
		}
		stop := make(chan struct{})
		defer close(stop)
		go func() { // save output as it arrives, so the UI can follow along
			t := time.NewTicker(2 * time.Second)
			defer t.Stop()
			for {
				select {
				case <-stop:
					return
				case <-t.C:
					if out.changed() {
						_ = s.store.UpdateCronOutput(bg, runID, out.String())
					}
				}
			}
		}()
		cmd := []string{"sh", "-c", wrapper, "simplepanel-cron", strconv.Itoa(job.Timeout), job.Command}
		code, err := s.docker.ExecStream(ctx, id, cmd, nil, nil, out, out)
		if err != nil && ctx.Err() != nil {
			return -1, fmt.Errorf("timed out after %ds", job.Timeout)
		}
		return code, err
	}()
	text := out.String()
	status := "succeeded"
	var exit *int
	switch {
	case err != nil:
		status = "failed"
		if strings.Contains(err.Error(), "executable file not found") || strings.Contains(err.Error(), "no such file") {
			err = errors.New("this service's image has no /bin/sh, so it can't run commands")
		}
		text += "\n[" + err.Error() + "]"
	default:
		exit = &code
		if code != 0 {
			status = "failed"
			if code == 124 {
				text += fmt.Sprintf("\n[timed out after %ds]", job.Timeout)
			}
		}
	}
	if err := s.store.FinishCronRun(bg, runID, status, exit, text); err != nil {
		s.log.Error("saving cron run", "job", job.Name, "err", err)
	}
	if status == "failed" && s.notify != nil {
		reason := "exit code " + strconv.Itoa(code)
		if err != nil {
			reason = err.Error()
		} else if code == 124 {
			reason = "timed out"
		}
		s.notify.Send(notify.Event{
			Type: notify.CronFailed, Level: "error", ProjectID: app.ProjectID, Project: app.ProjectName, Service: app.Name,
			Title:   fmt.Sprintf("Cron job %q on %s/%s failed", job.Name, app.ProjectName, app.Name),
			Message: fmt.Sprintf("%s: %s", job.Command, reason),
			Link:    fmt.Sprintf("/#/p/%d/a/%d/cron", app.ProjectID, app.ID),
		})
	}
}
