package api

import (
	"errors"
	"net/http"
	"strings"

	"golang.org/x/crypto/bcrypt"

	"github.com/SimpleAOB/SimplePanel/internal/deploy"
	"github.com/SimpleAOB/SimplePanel/internal/store"
)

const bcryptCost = 12

// dummyHash is compared against when a username doesn't exist, so login
// timing doesn't reveal which usernames are valid.
var dummyHash, _ = bcrypt.GenerateFromPassword([]byte("simplepanel-dummy-password"), bcryptCost)

func validPassword(p string) error {
	if len(p) < 10 {
		return errors.New("password must be at least 10 characters")
	}
	if len(p) > 72 {
		return errors.New("password must be at most 72 bytes")
	}
	return nil
}

func (s *Server) setSession(w http.ResponseWriter, r *http.Request, userID int64) error {
	tok, err := s.store.CreateSession(r.Context(), userID, sessionTTL)
	if err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: tok, Path: "/", HttpOnly: true, Secure: isHTTPS(r),
		SameSite: http.SameSiteStrictMode, MaxAge: int(sessionTTL.Seconds()),
	})
	return nil
}

func (s *Server) requireAuth(h func(http.ResponseWriter, *http.Request, *store.User)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(sessionCookie)
		if err != nil || c.Value == "" {
			writeErr(w, 401, "not signed in")
			return
		}
		u, err := s.store.SessionUser(r.Context(), c.Value)
		if err != nil {
			writeErr(w, 401, "not signed in")
			return
		}
		h(w, r, u)
	})
}

func (s *Server) getSetup(w http.ResponseWriter, r *http.Request) {
	n, err := s.store.UserCount(r.Context())
	if err != nil {
		s.storeErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"needsSetup": n == 0})
}

type credentials struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

func (s *Server) postSetup(w http.ResponseWriter, r *http.Request) {
	if !s.loginLimit.Allow("setup:" + clientIP(r)) {
		writeErr(w, 429, "too many attempts, try again later")
		return
	}
	var c credentials
	if !readJSON(w, r, &c) {
		return
	}
	c.Username = strings.TrimSpace(strings.ToLower(c.Username))
	if err := deploy.ValidName(c.Username); err != nil {
		writeErr(w, 400, "username: "+err.Error())
		return
	}
	if err := validPassword(c.Password); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(c.Password), bcryptCost)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	u, err := s.store.CreateFirstUser(r.Context(), c.Username, string(hash))
	if errors.Is(err, store.ErrConflict) {
		writeErr(w, 409, "setup is already complete")
		return
	}
	if err != nil {
		s.storeErr(w, err)
		return
	}
	if err := s.setSession(w, r, u.ID); err != nil {
		s.storeErr(w, err)
		return
	}
	s.log.Info("admin account created", "user", u.Username)
	writeJSON(w, 201, u)
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	ip := clientIP(r)
	if !s.loginLimit.Allow("login:"+ip) || !s.loginLimit.Allow("login:*global*") {
		writeErr(w, 429, "too many attempts, try again in a few minutes")
		return
	}
	var c credentials
	if !readJSON(w, r, &c) {
		return
	}
	u, hash, err := s.store.UserByName(r.Context(), strings.TrimSpace(strings.ToLower(c.Username)))
	if err != nil {
		_ = bcrypt.CompareHashAndPassword(dummyHash, []byte(c.Password))
		s.log.Warn("failed login", "ip", ip)
		writeErr(w, 401, "wrong username or password")
		return
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(c.Password)) != nil {
		s.log.Warn("failed login", "ip", ip)
		writeErr(w, 401, "wrong username or password")
		return
	}
	if err := s.setSession(w, r, u.ID); err != nil {
		s.storeErr(w, err)
		return
	}
	writeJSON(w, 200, u)
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request, _ *store.User) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		_ = s.store.DeleteSession(r.Context(), c.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteStrictMode})
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) me(w http.ResponseWriter, r *http.Request, u *store.User) {
	writeJSON(w, 200, u)
}

func (s *Server) changePassword(w http.ResponseWriter, r *http.Request, u *store.User) {
	var body struct {
		Current string `json:"current"`
		New     string `json:"new"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	if !s.loginLimit.Allow("pw:" + clientIP(r)) {
		writeErr(w, 429, "too many attempts, try again later")
		return
	}
	hash, err := s.store.UserPasswordHash(r.Context(), u.ID)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(body.Current)) != nil {
		writeErr(w, 400, "current password is wrong")
		return
	}
	if err := validPassword(body.New); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	nh, err := bcrypt.GenerateFromPassword([]byte(body.New), bcryptCost)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	if err := s.store.SetUserPassword(r.Context(), u.ID, string(nh)); err != nil {
		s.storeErr(w, err)
		return
	}
	// Sign out every other session.
	c, _ := r.Cookie(sessionCookie)
	_ = s.store.DeleteUserSessions(r.Context(), u.ID, c.Value)
	writeJSON(w, 200, map[string]bool{"ok": true})
}
