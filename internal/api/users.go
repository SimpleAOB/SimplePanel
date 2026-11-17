package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/SimpleAOB/SimplePanel/internal/deploy"
	"github.com/SimpleAOB/SimplePanel/internal/store"
)

// Invite and reset links are valid for a week.
const inviteTTL = 7 * 24 * time.Hour

type userView struct {
	store.User
	Access map[string]string `json:"access"` // project id -> role
}

func (s *Server) userView(r *http.Request, u store.User) userView {
	acc, _ := s.store.ProjectAccess(r.Context(), u.ID)
	out := userView{User: u, Access: map[string]string{}}
	for pid, role := range acc {
		out.Access[strconv.FormatInt(pid, 10)] = role
	}
	return out
}

func (s *Server) listUsers(w http.ResponseWriter, r *http.Request, u *store.User) {
	if !requireAdmin(w, u) {
		return
	}
	us, err := s.store.Users(r.Context())
	if err != nil {
		s.storeErr(w, err)
		return
	}
	out := make([]userView, len(us))
	for i := range us {
		out[i] = s.userView(r, us[i])
	}
	writeJSON(w, 200, out)
}

type userInput struct {
	Username    string            `json:"username"`
	Role        string            `json:"role"`
	AllProjects bool              `json:"allProjects"`
	Access      map[string]string `json:"access"`
}

// validate checks the role and per-project access and returns the access
// map keyed by project id.
func (s *Server) validateUserInput(r *http.Request, in *userInput) (map[int64]string, error) {
	switch in.Role {
	case store.RoleAdmin, store.RoleDeveloper, store.RoleViewer:
	case store.RoleOwner:
		return nil, errors.New("there is only one owner")
	default:
		return nil, errors.New(`role must be "admin", "developer" or "viewer"`)
	}
	access := map[int64]string{}
	if in.Role == store.RoleAdmin {
		return access, nil // admins see everything
	}
	for k, role := range in.Access {
		pid, err := strconv.ParseInt(k, 10, 64)
		if err != nil {
			return nil, errors.New("bad project id in access")
		}
		if role == "" || role == "none" {
			continue
		}
		if role != store.RoleDeveloper && role != store.RoleViewer {
			return nil, errors.New(`project access must be "developer" or "viewer"`)
		}
		if _, err := s.store.Project(r.Context(), pid); err != nil {
			return nil, errors.New("access lists a project that doesn't exist")
		}
		access[pid] = role
	}
	if !in.AllProjects && len(access) == 0 {
		return nil, errors.New("give the user access to all projects or to at least one project")
	}
	return access, nil
}

func (s *Server) createUser(w http.ResponseWriter, r *http.Request, u *store.User) {
	if !requireAdmin(w, u) {
		return
	}
	var in userInput
	if !readJSON(w, r, &in) {
		return
	}
	in.Username = strings.TrimSpace(strings.ToLower(in.Username))
	if err := deploy.ValidName(in.Username); err != nil {
		writeErr(w, 400, "username: "+err.Error())
		return
	}
	access, err := s.validateUserInput(r, &in)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	nu, err := s.store.CreateUser(r.Context(), in.Username, in.Role, in.AllProjects || in.Role == store.RoleAdmin)
	if errors.Is(err, store.ErrConflict) {
		writeErr(w, 409, "that username is taken")
		return
	}
	if err != nil {
		s.storeErr(w, err)
		return
	}
	if err := s.store.UpdateUserRole(r.Context(), nu.ID, nu.Role, nu.AllProjects, access); err != nil {
		s.storeErr(w, err)
		return
	}
	tok, err := s.store.CreateInvite(r.Context(), nu.ID, store.InviteKind, inviteTTL)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	s.log.Info("user invited", "user", nu.Username, "role", nu.Role, "by", u.Username)
	writeJSON(w, 201, map[string]any{"user": s.userView(r, *nu), "invitePath": "/#/invite/" + tok, "expiresIn": "7 days"})
}

// targetUser loads the {id} user and refuses changes to the owner and to
// the caller's own account (so nobody locks themselves out).
func (s *Server) targetUser(w http.ResponseWriter, r *http.Request, u *store.User) (*store.User, bool) {
	if !requireAdmin(w, u) {
		return nil, false
	}
	id, ok := pathID(r)
	if !ok {
		writeErr(w, 400, "bad id")
		return nil, false
	}
	t, err := s.store.User(r.Context(), id)
	if err != nil {
		s.storeErr(w, err)
		return nil, false
	}
	if t.Role == store.RoleOwner {
		writeErr(w, 403, "the owner's account can't be changed here")
		return nil, false
	}
	if t.ID == u.ID {
		writeErr(w, 403, "you can't change your own role or remove yourself; ask another admin")
		return nil, false
	}
	return t, true
}

func (s *Server) updateUser(w http.ResponseWriter, r *http.Request, u *store.User) {
	t, ok := s.targetUser(w, r, u)
	if !ok {
		return
	}
	var in userInput
	if !readJSON(w, r, &in) {
		return
	}
	access, err := s.validateUserInput(r, &in)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if err := s.store.UpdateUserRole(r.Context(), t.ID, in.Role, in.AllProjects || in.Role == store.RoleAdmin, access); err != nil {
		s.storeErr(w, err)
		return
	}
	t, _ = s.store.User(r.Context(), t.ID)
	writeJSON(w, 200, s.userView(r, *t))
}

func (s *Server) deleteUser(w http.ResponseWriter, r *http.Request, u *store.User) {
	t, ok := s.targetUser(w, r, u)
	if !ok {
		return
	}
	if err := s.store.DeleteUser(r.Context(), t.ID); err != nil {
		s.storeErr(w, err)
		return
	}
	s.log.Info("user removed", "user", t.Username, "by", u.Username)
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// resetUser creates a new invite (for users who haven't joined yet) or a
// password reset link. It signs the user out everywhere once used.
func (s *Server) resetUser(w http.ResponseWriter, r *http.Request, u *store.User) {
	t, ok := s.targetUser(w, r, u)
	if !ok {
		return
	}
	kind := store.ResetKind
	if t.Pending {
		kind = store.InviteKind
	}
	tok, err := s.store.CreateInvite(r.Context(), t.ID, kind, inviteTTL)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"invitePath": "/#/invite/" + tok, "kind": kind, "expiresIn": "7 days"})
}

// getInvite tells the invite page whose account it is for.
func (s *Server) getInvite(w http.ResponseWriter, r *http.Request) {
	if !s.loginLimit.Allow("invite:" + clientIP(r)) {
		writeErr(w, 429, "too many attempts, try again later")
		return
	}
	tu, kind, err := s.store.InviteUser(r.Context(), r.PathValue("token"))
	if err != nil {
		writeErr(w, 404, "this link is invalid or has expired; ask an admin for a new one")
		return
	}
	writeJSON(w, 200, map[string]string{"username": tu.Username, "kind": kind, "role": tu.Role})
}

// acceptInvite sets the password and signs the user in.
func (s *Server) acceptInvite(w http.ResponseWriter, r *http.Request) {
	if !s.loginLimit.Allow("invite:" + clientIP(r)) {
		writeErr(w, 429, "too many attempts, try again later")
		return
	}
	var body struct {
		Password string `json:"password"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	if err := validPassword(body.Password); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(body.Password), bcryptCost)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	nu, err := s.store.UseInvite(r.Context(), r.PathValue("token"), string(hash))
	if err != nil {
		writeErr(w, 404, "this link is invalid or has expired; ask an admin for a new one")
		return
	}
	if err := s.setSession(w, r, nu.ID); err != nil {
		s.storeErr(w, err)
		return
	}
	s.log.Info("password set from link", "user", nu.Username)
	writeJSON(w, 200, nu)
}
