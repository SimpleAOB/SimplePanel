package api

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/SimpleAOB/SimplePanel/internal/store"
)

// ---- API tokens (for the MCP endpoint) ----

func (s *Server) listTokens(w http.ResponseWriter, r *http.Request, _ *store.User) {
	ts, err := s.store.APITokens(r.Context())
	if err != nil {
		s.storeErr(w, err)
		return
	}
	writeJSON(w, 200, ts)
}

func (s *Server) createToken(w http.ResponseWriter, r *http.Request, _ *store.User) {
	var body struct {
		Name  string `json:"name"`
		Scope string `json:"scope"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	body.Name = strings.TrimSpace(body.Name)
	if body.Name == "" || len(body.Name) > 60 || strings.ContainsAny(body.Name, "\r\n\t") {
		writeErr(w, 400, "give the token a name of up to 60 characters")
		return
	}
	if store.ScopeRank(body.Scope) == 0 {
		writeErr(w, 400, `scope must be "read", "manage" or "full"`)
		return
	}
	t, secret, err := s.store.CreateAPIToken(r.Context(), body.Name, body.Scope)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	s.log.Info("API token created", "name", t.Name, "scope", t.Scope)
	// The secret is only ever returned here.
	writeJSON(w, 201, map[string]any{"token": t, "secret": secret})
}

func (s *Server) deleteToken(w http.ResponseWriter, r *http.Request, _ *store.User) {
	id, ok := pathID(r)
	if !ok {
		writeErr(w, 400, "bad id")
		return
	}
	if err := s.store.DeleteAPIToken(r.Context(), id); err != nil {
		s.storeErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) listAudit(w http.ResponseWriter, r *http.Request, _ *store.User) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	es, err := s.store.Audit(r.Context(), limit)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	writeJSON(w, 200, es)
}

// setAIAccess turns AI read-only access to a database on or off.
func (s *Server) setAIAccess(w http.ResponseWriter, r *http.Request, _ *store.User) {
	a, ok := s.loadApp(w, r)
	if !ok {
		return
	}
	if !a.IsDatabase() {
		writeErr(w, 400, "only databases have AI read-only access")
		return
	}
	var body struct {
		Enabled bool `json:"enabled"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	if body.Enabled {
		pw := store.RandomToken(24)
		if err := s.deploy.EnableAIAccess(r.Context(), a, pw); err != nil {
			writeErr(w, 409, "couldn't create the read-only user: "+err.Error())
			return
		}
		if err := s.store.SetAIAccess(r.Context(), a.ID, true, pw); err != nil {
			s.storeErr(w, err)
			return
		}
	} else {
		// Forget the password first: even if the database can't be reached
		// right now, the AI loses access immediately.
		if err := s.store.SetAIAccess(r.Context(), a.ID, false, ""); err != nil {
			s.storeErr(w, err)
			return
		}
		if err := s.deploy.DisableAIAccess(r.Context(), a); err != nil {
			s.log.Warn("removing AI read-only user", "service", a.ServiceName(), "err", err)
			writeJSON(w, 200, map[string]any{"enabled": false,
				"warning": "AI access is off. The read-only user couldn't be removed from the database (" + err.Error() + "); it has no known password and is removed next time you turn access on and off."})
			return
		}
	}
	s.log.Info("AI read-only access changed", "service", a.ServiceName(), "enabled", body.Enabled)
	writeJSON(w, 200, map[string]bool{"enabled": body.Enabled})
}
