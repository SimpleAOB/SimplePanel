package api

import (
	"errors"
	"net/http"
	"strings"

	"github.com/SimpleAOB/SimplePanel/internal/cron"
	"github.com/SimpleAOB/SimplePanel/internal/store"
)

// Cron jobs run a command in a running container of a service on a
// schedule. Seeing jobs and their output needs View; adding, changing,
// removing and running them needs Deploy (they run code, like a deploy).

type cronInput struct {
	Name     string `json:"name"`
	Schedule string `json:"schedule"`
	Command  string `json:"command"`
	Timeout  int    `json:"timeout"`
	Enabled  *bool  `json:"enabled"`
}

func (in *cronInput) apply(j *store.CronJob) error {
	j.Name, j.Schedule, j.Command, j.Timeout = in.Name, in.Schedule, in.Command, in.Timeout
	if in.Enabled != nil {
		j.Enabled = *in.Enabled
	} else if j.ID == 0 {
		j.Enabled = true
	}
	return cron.Validate(j)
}

func (s *Server) listCron(w http.ResponseWriter, r *http.Request, u *store.User) {
	a, ok := s.loadApp(w, r, u, store.LevelView)
	if !ok {
		return
	}
	js, err := s.store.CronJobs(r.Context(), a.ID)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	writeJSON(w, 200, js)
}

func (s *Server) createCron(w http.ResponseWriter, r *http.Request, u *store.User) {
	a, ok := s.loadApp(w, r, u, store.LevelDeploy)
	if !ok {
		return
	}
	if a.IsPreview() {
		writeErr(w, 400, "previews can't have cron jobs")
		return
	}
	var in cronInput
	if !readJSON(w, r, &in) {
		return
	}
	j := &store.CronJob{AppID: a.ID}
	if err := in.apply(j); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if n, _ := s.store.CountCronJobs(r.Context(), a.ID); n >= cron.MaxJobsPerService {
		writeErr(w, 400, "a service can have up to 20 cron jobs")
		return
	}
	j.NextRunAt, _ = s.cron.NextRun(j.Schedule)
	if err := s.store.CreateCronJob(r.Context(), j); err != nil {
		s.storeErr(w, err)
		return
	}
	writeJSON(w, 201, j)
}

// loadCron loads the job named by {id} and checks u's level on its service.
func (s *Server) loadCron(w http.ResponseWriter, r *http.Request, u *store.User, need int) (*store.CronJob, *store.App, bool) {
	id, ok := pathID(r)
	if !ok {
		writeErr(w, 400, "bad id")
		return nil, nil, false
	}
	j, err := s.store.CronJob(r.Context(), id)
	if err != nil {
		s.storeErr(w, err)
		return nil, nil, false
	}
	a, ok := s.loadAppID(w, r, u, j.AppID, need)
	return j, a, ok
}

func (s *Server) updateCron(w http.ResponseWriter, r *http.Request, u *store.User) {
	j, _, ok := s.loadCron(w, r, u, store.LevelDeploy)
	if !ok {
		return
	}
	var in cronInput
	if !readJSON(w, r, &in) {
		return
	}
	if in.Timeout == 0 {
		in.Timeout = j.Timeout
	}
	if err := in.apply(j); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	j.NextRunAt = nil
	if j.Enabled {
		j.NextRunAt, _ = s.cron.NextRun(j.Schedule)
	}
	if err := s.store.UpdateCronJob(r.Context(), j); err != nil {
		s.storeErr(w, err)
		return
	}
	writeJSON(w, 200, j)
}

func (s *Server) deleteCron(w http.ResponseWriter, r *http.Request, u *store.User) {
	j, _, ok := s.loadCron(w, r, u, store.LevelDeploy)
	if !ok {
		return
	}
	if err := s.store.DeleteCronJob(r.Context(), j.ID); err != nil {
		s.storeErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) runCron(w http.ResponseWriter, r *http.Request, u *store.User) {
	j, _, ok := s.loadCron(w, r, u, store.LevelDeploy)
	if !ok {
		return
	}
	trigger := "manual"
	if u.Scope != "" {
		trigger = "api"
	}
	run, err := s.cron.Run(r.Context(), j.ID, trigger)
	if errors.Is(err, cron.ErrRunning) {
		writeErr(w, 409, err.Error())
		return
	}
	if err != nil {
		s.storeErr(w, err)
		return
	}
	writeJSON(w, 202, run)
}

func (s *Server) listCronRuns(w http.ResponseWriter, r *http.Request, u *store.User) {
	j, _, ok := s.loadCron(w, r, u, store.LevelView)
	if !ok {
		return
	}
	runs, err := s.store.CronRuns(r.Context(), j.ID, 50)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	writeJSON(w, 200, runs)
}

func (s *Server) getCronRun(w http.ResponseWriter, r *http.Request, u *store.User) {
	id, ok := pathID(r)
	if !ok {
		writeErr(w, 400, "bad id")
		return
	}
	run, err := s.store.CronRun(r.Context(), id)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	j, err := s.store.CronJob(r.Context(), run.JobID)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	if _, ok := s.loadAppID(w, r, u, j.AppID, store.LevelView); !ok {
		return
	}
	writeJSON(w, 200, run)
}

// cronTarget names a cron job for the activity log.
func (s *Server) cronTarget(r *http.Request) string {
	id, ok := pathID(r)
	if !ok {
		return ""
	}
	j, err := s.store.CronJob(r.Context(), id)
	if err != nil {
		return ""
	}
	a, err := s.store.App(r.Context(), j.AppID)
	if err != nil {
		return j.Name
	}
	return a.ProjectName + "/" + a.Name + ": " + strings.TrimSpace(j.Name)
}
