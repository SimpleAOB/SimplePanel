package api

import (
	"context"
	"net/http"
	"strings"

	"github.com/SimpleAOB/SimplePanel/internal/store"
)

// Access control. Every project-scoped handler asks for a level:
//
//	store.LevelView    see services, logs, metrics and backups (no secrets)
//	store.LevelDeploy  also create, configure, deploy, back up, restore into a new database
//	store.LevelManage  also delete and restore over existing data
//
// A user's level comes from their role (and per-project overrides); an API
// token can only lower it to its own scope. Server-wide settings, users,
// backup destinations and notifications need an Owner or Admin.

// level is what u may do on a project.
func (s *Server) level(ctx context.Context, u *store.User, projectID int64) int {
	l := s.store.ProjectLevel(ctx, u, projectID)
	if u != nil && u.Scope != "" {
		l = min(l, store.ScopeLevel(u.Scope))
	}
	return l
}

var levelNames = map[int]string{store.LevelView: "view", store.LevelDeploy: "change and deploy", store.LevelManage: "delete or overwrite"}

// allow checks u's level on a project and writes the error if it falls
// short. Projects the user can't see at all answer 404, so their names and
// ids don't leak.
func (s *Server) allow(w http.ResponseWriter, r *http.Request, u *store.User, projectID int64, need int) bool {
	l := s.level(r.Context(), u, projectID)
	if l >= need {
		return true
	}
	if l == store.LevelNone {
		writeErr(w, 404, "not found")
		return false
	}
	msg := "your role can't " + levelNames[need] + " in this project"
	if u != nil && u.Scope != "" && s.store.ProjectLevel(r.Context(), u, projectID) >= need {
		msg = "this API token's scope can't " + levelNames[need] + "; create a token with more access"
	}
	writeErr(w, 403, msg+"; ask an admin")
	return false
}

// requireAdmin checks that u is an Owner or Admin (and, for API tokens,
// that the token has full scope).
func requireAdmin(w http.ResponseWriter, u *store.User) bool {
	if u.IsAdmin() {
		return true
	}
	writeErr(w, 403, "only an owner or admin can do this")
	return false
}

// requireAdminScope is requireAdmin for actions an API token may take with
// a lower scope than full (creating and importing projects needs manage).
func requireAdminScope(w http.ResponseWriter, u *store.User, scope string) bool {
	if (u.Role == store.RoleOwner || u.Role == store.RoleAdmin) && (u.Scope == "" || store.ScopeRank(u.Scope) >= store.ScopeRank(scope)) {
		return true
	}
	writeErr(w, 403, "only an owner or admin can do this")
	return false
}

// visibleProjects lists the projects u can see.
func (s *Server) visibleProjects(ctx context.Context, u *store.User) ([]store.Project, error) {
	ps, err := s.store.Projects(ctx)
	if err != nil {
		return nil, err
	}
	if u.Role == store.RoleOwner || u.Role == store.RoleAdmin {
		return ps, nil
	}
	out := []store.Project{}
	for _, p := range ps {
		if s.level(ctx, u, p.ID) > store.LevelNone {
			out = append(out, p)
		}
	}
	return out, nil
}

// statusWriter records the status code for the activity log.
type statusWriter struct {
	http.ResponseWriter
	code int
}

func (w *statusWriter) WriteHeader(code int) {
	if w.code == 0 {
		w.code = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if w.code == 0 {
		w.code = 200
	}
	return w.ResponseWriter.Write(b)
}

func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// activityActions names the UI requests that are written to the activity
// log. Reads, sign-ins and the activity log itself are left out.
var activityActions = map[string]string{
	"PUT /api/settings":                       "change server settings",
	"POST /api/tokens":                        "create API token",
	"DELETE /api/tokens/{id}":                 "revoke API token",
	"POST /api/users":                         "invite user",
	"PUT /api/users/{id}":                     "change user role",
	"DELETE /api/users/{id}":                  "remove user",
	"POST /api/users/{id}/reset":              "create password reset link",
	"POST /api/projects":                      "create project",
	"DELETE /api/projects/{id}":               "delete project",
	"POST /api/projects/{id}/apps":            "create app",
	"POST /api/projects/{id}/databases":       "create database",
	"POST /api/import":                        "import",
	"PUT /api/apps/{id}":                      "change settings",
	"DELETE /api/apps/{id}":                   "delete service",
	"POST /api/apps/{id}/deploy":              "deploy",
	"POST /api/apps/{id}/restart":             "restart",
	"POST /api/apps/{id}/stop":                "stop",
	"POST /api/apps/{id}/start":               "start",
	"POST /api/apps/{id}/domains":             "add domain",
	"DELETE /api/domains/{id}":                "remove domain",
	"POST /api/apps/{id}/webhook/rotate":      "regenerate deploy webhook",
	"PUT /api/apps/{id}/ai-access":            "change AI database access",
	"POST /api/apps/{id}/backups":             "back up",
	"POST /api/apps/{id}/restore":             "restore backup",
	"POST /api/apps/{id}/backup-jobs":         "add backup schedule",
	"PUT /api/backup-jobs/{id}":               "change backup schedule",
	"DELETE /api/backup-jobs/{id}":            "remove backup schedule",
	"DELETE /api/backups/{id}":                "delete backup",
	"POST /api/backups/destinations":          "add backup destination",
	"PUT /api/backups/destinations/{id}":      "change backup destination",
	"DELETE /api/backups/destinations/{id}":   "remove backup destination",
	"POST /api/backups/panel":                 "back up SimplePanel",
	"POST /api/backups/panel/jobs":            "add backup schedule",
	"POST /api/notifications":                 "add notification channel",
	"PUT /api/notifications/{id}":             "change notification channel",
	"DELETE /api/notifications/{id}":          "remove notification channel",
	"PUT /api/monitoring/settings":            "change alert thresholds",
	"GET /api/backups/{id}/download":          "download backup",
	"GET /api/apps/{app}/backups/file":        "download backup",
	"GET /api/backups/destinations/{id}/key":  "reveal backup passphrase",
	"POST /api/backups/destinations/{id}/key": "reveal backup passphrase",
}

// activityTarget names what a request acts on, looked up before the
// handler runs (a deleted service can't be looked up afterwards).
func (s *Server) activityTarget(r *http.Request) string {
	ctx := r.Context()
	target := ""
	if id, ok := pathID(r); ok {
		switch {
		case strings.Contains(r.Pattern, "/apps/{id}"):
			if a, err := s.store.App(ctx, id); err == nil {
				target = a.ProjectName + "/" + a.Name
			}
		case strings.Contains(r.Pattern, "/projects/{id}"):
			if p, err := s.store.Project(ctx, id); err == nil {
				target = p.Name
			}
		case strings.Contains(r.Pattern, "/users/{id}"):
			if x, err := s.store.User(ctx, id); err == nil {
				target = x.Username
			}
		}
		if target == "" {
			target = "#" + r.PathValue("id")
		}
	}
	return target
}

// recordActivity writes a UI change to the activity log.
func (s *Server) recordActivity(r *http.Request, u *store.User, target string, code int) {
	action, ok := activityActions[r.Pattern]
	if !ok {
		return
	}
	ctx := context.WithoutCancel(r.Context())
	e := &store.AuditEntry{Source: "ui", UserID: &u.ID, Username: u.Username, Action: action, Target: target, OK: code < 400}
	if code >= 400 {
		e.Error = http.StatusText(code)
	}
	if err := s.store.AddAudit(ctx, e); err != nil {
		s.log.Error("activity log", "err", err)
	}
}
