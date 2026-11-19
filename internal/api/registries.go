package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode"

	"github.com/SimpleAOB/SimplePanel/internal/docker"
	"github.com/SimpleAOB/SimplePanel/internal/store"
)

// Private container registries. Owners and admins save logins (a username
// and an access token) under Settings > Registries; anyone who can deploy
// picks one for an image app. Tokens are write-only: they never appear in
// API responses, exports or deployment logs, and a login is only ever
// sent to its own registry.

type registryInput struct {
	Name     string `json:"name"`
	Server   string `json:"server"`
	Username string `json:"username"`
	Password string `json:"password"` // blank on update: keep the saved one
	// SkipCheck saves without signing in to the registry first.
	SkipCheck bool `json:"skipCheck"`
}

func (in *registryInput) apply(g *store.Registry) error {
	name := strings.TrimSpace(in.Name)
	if name == "" || len(name) > 60 || strings.IndexFunc(name, unicode.IsControl) >= 0 {
		return errors.New("give the login a name of up to 60 characters")
	}
	server, err := docker.NormalizeRegistry(in.Server)
	if err != nil {
		return err
	}
	user := strings.TrimSpace(in.Username)
	if user == "" || len(user) > 200 || strings.IndexFunc(user, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0 {
		return errors.New("enter the username you sign in to the registry with")
	}
	pw := strings.TrimSpace(in.Password)
	if pw == "" && g.Password == "" {
		return errors.New("enter an access token or password")
	}
	if len(pw) > 4096 || strings.IndexFunc(pw, unicode.IsControl) >= 0 {
		return errors.New("the access token looks invalid")
	}
	g.Name, g.Server, g.Username = name, server, user
	if pw != "" {
		g.Password = pw
	}
	return nil
}

// registryView is what the API shows of a login: never the token.
func registryView(g *store.Registry, apps int, admin bool) map[string]any {
	out := map[string]any{"id": g.ID, "name": g.Name, "server": g.Server}
	if admin {
		out["username"], out["apps"], out["updatedAt"] = g.Username, apps, g.UpdatedAt
	}
	return out
}

// listRegistries is open to every signed-in user so the app form can
// offer the logins; only owners and admins see usernames and usage.
func (s *Server) listRegistries(w http.ResponseWriter, r *http.Request, u *store.User) {
	regs, err := s.store.Registries(r.Context())
	if err != nil {
		s.storeErr(w, err)
		return
	}
	admin := u.IsAdmin()
	usage := map[int64]int{}
	if admin {
		if usage, err = s.store.RegistryUsage(r.Context()); err != nil {
			s.storeErr(w, err)
			return
		}
	}
	out := make([]map[string]any, len(regs))
	for i := range regs {
		out[i] = registryView(&regs[i], usage[regs[i].ID], admin)
	}
	writeJSON(w, 200, out)
}

// checkLogin signs in to the registry through Docker.
func (s *Server) checkLogin(ctx context.Context, g *store.Registry) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	err := s.docker.Login(ctx, docker.RegistryAuth{Server: g.Server, Username: g.Username, Password: g.Password})
	if err == nil {
		return nil
	}
	msg := err.Error()
	var ae *docker.APIError
	if errors.As(err, &ae) {
		msg = ae.Message
	}
	// Docker's message can repeat what was sent; never echo the token.
	msg = strings.ReplaceAll(msg, g.Password, "********")
	return fmt.Errorf("signing in to %s failed: %s", g.Server, msg)
}

func (s *Server) saveRegistry(w http.ResponseWriter, r *http.Request, u *store.User) {
	if !requireAdmin(w, u) {
		return
	}
	ctx := r.Context()
	g := &store.Registry{}
	if r.PathValue("id") != "" {
		id, ok := pathID(r)
		if !ok {
			writeErr(w, 400, "bad id")
			return
		}
		var err error
		if g, err = s.store.Registry(ctx, id); err != nil {
			s.storeErr(w, err)
			return
		}
	}
	var in registryInput
	if !readJSON(w, r, &in) {
		return
	}
	prevServer := g.Server
	if err := in.apply(g); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if g.ID != 0 && prevServer != g.Server && strings.TrimSpace(in.Password) == "" {
		// Never send a token to a registry it wasn't made for.
		writeErr(w, 400, "enter the access token again when changing the registry address")
		return
	}
	if !in.SkipCheck {
		if err := s.checkLogin(ctx, g); err != nil {
			writeJSON(w, 400, map[string]any{"error": err.Error(), "checkFailed": true})
			return
		}
	}
	var err error
	code := 200
	if g.ID == 0 {
		err, code = s.store.CreateRegistry(ctx, g), 201
	} else {
		err = s.store.UpdateRegistry(ctx, g)
		if err == nil && prevServer != g.Server {
			// Apps pulling from the old registry can't use this login now.
			err = s.store.ClearRegistryFromApps(ctx, g.ID)
		}
	}
	if errors.Is(err, store.ErrConflict) {
		writeErr(w, 409, "a registry login with that name already exists")
		return
	}
	if err != nil {
		s.storeErr(w, err)
		return
	}
	usage, _ := s.store.RegistryUsage(ctx)
	writeJSON(w, code, registryView(g, usage[g.ID], true))
}

// testRegistry checks a login without saving it. With an id and a blank
// token it checks the saved token.
func (s *Server) testRegistry(w http.ResponseWriter, r *http.Request, u *store.User) {
	if !requireAdmin(w, u) {
		return
	}
	var in struct {
		registryInput
		ID int64 `json:"id"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	g := &store.Registry{}
	if in.ID > 0 {
		var err error
		if g, err = s.store.Registry(r.Context(), in.ID); err != nil {
			s.storeErr(w, err)
			return
		}
	}
	prevServer := g.Server
	if strings.TrimSpace(in.Name) == "" {
		in.Name = "test"
	}
	if err := in.registryInput.apply(g); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if in.ID > 0 && prevServer != g.Server && strings.TrimSpace(in.Password) == "" {
		writeErr(w, 400, "enter the access token again when changing the registry address")
		return
	}
	if err := s.checkLogin(r.Context(), g); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (s *Server) deleteRegistry(w http.ResponseWriter, r *http.Request, u *store.User) {
	if !requireAdmin(w, u) {
		return
	}
	id, ok := pathID(r)
	if !ok {
		writeErr(w, 400, "bad id")
		return
	}
	if _, err := s.store.Registry(r.Context(), id); err != nil {
		s.storeErr(w, err)
		return
	}
	if err := s.store.DeleteRegistry(r.Context(), id); err != nil {
		s.storeErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// resolveRegistry sets the registry login an app pulls its image with.
// want is what the request asked for: nil keeps the current login while
// it still fits the image's registry, and otherwise picks the only saved
// login for that registry (like `docker login`, which is per registry);
// 0 means none. A login must belong to the image's registry, so it can
// never be sent anywhere else.
func (s *Server) resolveRegistry(ctx context.Context, a *store.App, want *int64) error {
	if a.Source != "image" {
		a.RegistryID = 0
		return nil
	}
	host := docker.RegistryHost(a.Image)
	if want != nil {
		if *want == 0 {
			a.RegistryID = 0
			return nil
		}
		g, err := s.store.Registry(ctx, *want)
		if errors.Is(err, store.ErrNotFound) {
			return errors.New("that registry login doesn't exist")
		}
		if err != nil {
			return err
		}
		if g.Server != host {
			return fmt.Errorf("the registry login %q is for %s, but this image is on %s", g.Name, g.Server, host)
		}
		a.RegistryID = g.ID
		return nil
	}
	if a.RegistryID != 0 {
		if g, err := s.store.Registry(ctx, a.RegistryID); err == nil && g.Server == host {
			return nil
		}
	}
	a.RegistryID = 0
	regs, err := s.store.Registries(ctx)
	if err != nil {
		return err
	}
	var match []int64
	for _, g := range regs {
		if g.Server == host {
			match = append(match, g.ID)
		}
	}
	if len(match) == 1 {
		a.RegistryID = match[0]
	}
	return nil
}
