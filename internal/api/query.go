package api

// The database Query tab (read-only). See internal/deploy/dbquery.go for
// how the database enforces read-only access.
//
// Who may use it: owners and admins always; developers and viewers only
// with the "Query databases" permission (users.db_query), and only in
// projects they can see. Every query and table browse is written to the
// activity log with its text.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/SimpleAOB/SimplePanel/internal/deploy"
	"github.com/SimpleAOB/SimplePanel/internal/store"
)

// queryLocks serializes setting up the read-only user per database.
var queryLocks sync.Map // app id -> *sync.Mutex

// loadQueryDB loads the {id} database and checks the caller may query it.
func (s *Server) loadQueryDB(w http.ResponseWriter, r *http.Request, u *store.User) (*store.App, bool) {
	a, ok := s.loadApp(w, r, u, store.LevelView)
	if !ok {
		return nil, false
	}
	if !a.IsDatabase() || !deploy.QuerySupported(a.Kind) {
		writeErr(w, 400, "only databases have a Query tab")
		return nil, false
	}
	if !u.CanQueryDatabases() {
		writeErr(w, 403, "your account doesn't have the Query databases permission; ask an admin")
		return nil, false
	}
	return a, true
}

// withQueryUser runs fn with the read-only user's password, creating the
// user the first time, and once more after re-creating it if the database
// says it's missing (Redis restarted, a backup was restored…).
func (s *Server) withQueryUser(ctx context.Context, a *store.App, fn func(pw string) error) error {
	mu, _ := queryLocks.LoadOrStore(a.ID, &sync.Mutex{})
	setup := func(force bool) (string, error) {
		mu.(*sync.Mutex).Lock()
		defer mu.(*sync.Mutex).Unlock()
		pw, err := s.store.QueryPassword(ctx, a.ID)
		if err != nil {
			return "", err
		}
		if pw != "" && !force {
			return pw, nil
		}
		if pw == "" {
			pw = store.RandomToken(24)
		}
		if err := s.deploy.EnableQueryUser(ctx, a, pw); err != nil {
			return "", err
		}
		return pw, s.store.SetQueryPassword(ctx, a.ID, pw)
	}
	pw, err := setup(false)
	if err != nil {
		return err
	}
	err = fn(pw)
	if errors.Is(err, deploy.ErrQueryAuth) {
		if pw, err = setup(true); err != nil {
			return err
		}
		err = fn(pw)
	}
	return err
}

// logQuery writes a query to the activity log.
func (s *Server) logQuery(r *http.Request, u *store.User, a *store.App, action, query string, err error) {
	args, _ := json.Marshal(map[string]string{"query": truncate(query, 2000)})
	e := &store.AuditEntry{Source: "ui", UserID: &u.ID, Username: u.Username, Action: action,
		Target: a.ProjectName + "/" + a.Name, Args: string(args), OK: err == nil}
	if u.Token != nil {
		e.Source, e.TokenID, e.TokenName = "api", &u.Token.ID, u.Token.Name
	}
	if err != nil {
		e.Error = truncate(err.Error(), 500)
	}
	if err := s.store.AddAudit(context.WithoutCancel(r.Context()), e); err != nil {
		s.log.Error("activity log", "err", err)
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + "…"
}

// queryInfo tells the Query tab what it's talking to.
func (s *Server) queryInfo(w http.ResponseWriter, r *http.Request, u *store.User) {
	a, ok := s.loadQueryDB(w, r, u)
	if !ok {
		return
	}
	info := map[string]any{
		"engine":         a.Kind,
		"language":       deploy.QueryLanguage(a.Kind),
		"defaultTimeout": int(deploy.QueryDefaultTimeout.Seconds()),
		"maxTimeout":     int(deploy.QueryMaxTimeout.Seconds()),
		"defaultRows":    deploy.QueryDefaultRows,
		"maxRows":        deploy.QueryMaxRows,
		"explain":        a.Kind != "redis",
	}
	if a.Kind == "mongo" {
		if db := deploy.DatabaseInfo(a); db != nil {
			info["database"] = db.Database
		}
	}
	writeJSON(w, 200, info)
}

// queryTables lists tables (or collections, or Redis keys).
func (s *Server) queryTables(w http.ResponseWriter, r *http.Request, u *store.User) {
	a, ok := s.loadQueryDB(w, r, u)
	if !ok {
		return
	}
	var res *deploy.QueryResult
	err := s.withQueryUser(r.Context(), a, func(pw string) (err error) {
		res, err = s.deploy.QueryTables(r.Context(), a, pw, r.URL.Query().Get("pattern"))
		return err
	})
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, res)
}

// queryColumns describes one SQL table.
func (s *Server) queryColumns(w http.ResponseWriter, r *http.Request, u *store.User) {
	a, ok := s.loadQueryDB(w, r, u)
	if !ok {
		return
	}
	var res *deploy.QueryResult
	err := s.withQueryUser(r.Context(), a, func(pw string) (err error) {
		res, err = s.deploy.QueryColumns(r.Context(), a, pw, r.URL.Query().Get("table"))
		return err
	})
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, res)
}

type queryInput struct {
	Query    string `json:"query"`
	Explain  bool   `json:"explain"`
	Timeout  int    `json:"timeout"` // seconds
	Limit    int    `json:"limit"`   // rows
	Database string `json:"database"`
	// Source is "editor" (kept in history) or "browse" (the table browser).
	Source string `json:"source"`
}

// runQuery runs a read-only query.
func (s *Server) runQuery(w http.ResponseWriter, r *http.Request, u *store.User) {
	a, ok := s.loadQueryDB(w, r, u)
	if !ok {
		return
	}
	var in queryInput
	if !readJSON(w, r, &in) {
		return
	}
	in.Query = strings.TrimSpace(in.Query)
	if in.Query == "" {
		writeErr(w, 400, "query is empty")
		return
	}
	opt := deploy.QueryOptions{Timeout: time.Duration(in.Timeout) * time.Second, MaxRows: in.Limit, Explain: in.Explain, Database: in.Database}
	opt.Normalize()
	start := time.Now()
	var res *deploy.QueryResult
	err := s.withQueryUser(r.Context(), a, func(pw string) (err error) {
		res, err = s.deploy.Query(r.Context(), a, pw, in.Query, opt)
		return err
	})
	ms := time.Since(start).Milliseconds()

	action := "run database query"
	switch {
	case in.Source == "browse":
		action = "browse table"
	case in.Explain:
		action = "explain database query"
	}
	s.logQuery(r, u, a, action, in.Query, err)
	if in.Source != "browse" {
		run := &store.QueryRun{Query: truncate(in.Query, 20000), OK: err == nil, MS: ms}
		if err != nil {
			run.Error = truncate(err.Error(), 500)
		} else {
			run.Rows = res.RowCount
		}
		if err := s.store.AddQueryRun(context.WithoutCancel(r.Context()), a.ID, u.ID, run); err != nil {
			s.log.Error("query history", "err", err)
		}
	}
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"result": res, "ms": ms, "limit": opt.MaxRows, "timeout": int(opt.Timeout.Seconds())})
}

func (s *Server) queryHistory(w http.ResponseWriter, r *http.Request, u *store.User) {
	a, ok := s.loadQueryDB(w, r, u)
	if !ok {
		return
	}
	h, err := s.store.QueryHistory(r.Context(), a.ID, u.ID)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	writeJSON(w, 200, h)
}

func (s *Server) clearQueryHistory(w http.ResponseWriter, r *http.Request, u *store.User) {
	a, ok := s.loadQueryDB(w, r, u)
	if !ok {
		return
	}
	if err := s.store.ClearQueryHistory(r.Context(), a.ID, u.ID); err != nil {
		s.storeErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) listSavedQueries(w http.ResponseWriter, r *http.Request, u *store.User) {
	a, ok := s.loadQueryDB(w, r, u)
	if !ok {
		return
	}
	qs, err := s.store.SavedQueries(r.Context(), a.ID)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	writeJSON(w, 200, qs)
}

func (s *Server) saveQuery(w http.ResponseWriter, r *http.Request, u *store.User) {
	a, ok := s.loadQueryDB(w, r, u)
	if !ok {
		return
	}
	var in struct {
		Name  string `json:"name"`
		Query string `json:"query"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	in.Name, in.Query = strings.TrimSpace(in.Name), strings.TrimSpace(in.Query)
	if in.Name == "" || utf8.RuneCountInString(in.Name) > 80 {
		writeErr(w, 400, "give the query a name of up to 80 characters")
		return
	}
	if in.Query == "" || len(in.Query) > 20000 {
		writeErr(w, 400, "the query is empty or too long")
		return
	}
	q := &store.SavedQuery{AppID: a.ID, UserID: &u.ID, Username: u.Username, Name: in.Name, Query: in.Query}
	if err := s.store.AddSavedQuery(r.Context(), q); err != nil {
		if errors.Is(err, store.ErrConflict) {
			writeErr(w, 409, "this database has too many saved queries; remove some first")
			return
		}
		s.storeErr(w, err)
		return
	}
	writeJSON(w, 201, q)
}

// deleteSavedQuery removes a saved query. Its author, admins and anyone who
// can change the project may remove it.
func (s *Server) deleteSavedQuery(w http.ResponseWriter, r *http.Request, u *store.User) {
	id, ok := pathID(r)
	if !ok {
		writeErr(w, 400, "bad id")
		return
	}
	q, err := s.store.SavedQuery(r.Context(), id)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	a, ok := s.loadAppID(w, r, u, q.AppID, store.LevelView)
	if !ok {
		return
	}
	if !u.CanQueryDatabases() {
		writeErr(w, 403, "your account doesn't have the Query databases permission; ask an admin")
		return
	}
	mine := q.UserID != nil && *q.UserID == u.ID
	if !mine && s.level(r.Context(), u, a.ProjectID) < store.LevelDeploy {
		writeErr(w, 403, "only its author or someone who can change this project can remove this query")
		return
	}
	if err := s.store.DeleteSavedQuery(r.Context(), id); err != nil {
		s.storeErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}
