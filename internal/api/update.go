package api

import (
	"errors"
	"net/http"

	"github.com/SimpleAOB/SimplePanel/internal/store"
	"github.com/SimpleAOB/SimplePanel/internal/update"
)

// In-panel updates. Everything here is for owners and admins only.

// UseUpdater attaches the self-updater.
func (s *Server) UseUpdater(u *update.Updater) *Server {
	s.updater = u
	return s
}

func (s *Server) updaterReady(w http.ResponseWriter, u *store.User) bool {
	if !requireAdmin(w, u) {
		return false
	}
	if s.updater == nil {
		writeErr(w, 503, "updates are not available")
		return false
	}
	return true
}

func (s *Server) getUpdate(w http.ResponseWriter, r *http.Request, u *store.User) {
	if !s.updaterReady(w, u) {
		return
	}
	writeJSON(w, 200, s.updater.Status(r.Context()))
}

func (s *Server) checkUpdate(w http.ResponseWriter, r *http.Request, u *store.User) {
	if !s.updaterReady(w, u) {
		return
	}
	s.updater.CheckNow(r.Context(), false)
	writeJSON(w, 200, s.updater.Status(r.Context()))
}

func (s *Server) startUpdate(w http.ResponseWriter, r *http.Request, u *store.User) {
	if !s.updaterReady(w, u) {
		return
	}
	var body struct {
		Version string `json:"version"`
		Force   bool   `json:"force"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	by := update.Actor{UserID: &u.ID, Username: u.Username, Source: "ui"}
	if u.Token != nil {
		by.Source, by.TokenID, by.TokenName = "api", &u.Token.ID, u.Token.Name
	}
	_, err := s.updater.Start(r.Context(), body.Version, body.Force, by)
	switch {
	case errors.Is(err, update.ErrJobsRunning):
		writeJSON(w, 409, map[string]any{"error": "deployments, backups or cron jobs are running; updating restarts SimplePanel and interrupts them", "jobsRunning": true})
		return
	case errors.Is(err, update.ErrBusy):
		writeErr(w, 409, err.Error())
		return
	case err != nil:
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 202, s.updater.Status(r.Context()))
}

func (s *Server) putUpdateSettings(w http.ResponseWriter, r *http.Request, u *store.User) {
	if !s.updaterReady(w, u) {
		return
	}
	var in update.Settings
	if !readJSON(w, r, &in) {
		return
	}
	if err := s.updater.SaveSettings(r.Context(), in); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, in)
}
