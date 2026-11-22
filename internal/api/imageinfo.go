package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/SimpleAOB/SimplePanel/internal/deploy"
	"github.com/SimpleAOB/SimplePanel/internal/docker"
	"github.com/SimpleAOB/SimplePanel/internal/store"
)

// imageInfo tells the New App form what an image declares before it is
// deployed: its ENV variables (to fill in ahead of time) and EXPOSE ports.
// An image already on this server is read locally; otherwise its config
// is read from the registry, signed in with the matching saved login.
//
// GET /api/projects/{id}/image-info?image=ghcr.io/me/app:1&registryId=3
// registryId: absent picks the saved login for the image's registry (as
// creating the app does), 0 means none.
func (s *Server) imageInfo(w http.ResponseWriter, r *http.Request, u *store.User) {
	pid, ok := pathID(r)
	if !ok {
		writeErr(w, 400, "bad id")
		return
	}
	if !s.allow(w, r, u, pid, store.LevelDeploy) {
		return
	}
	image := strings.TrimSpace(r.URL.Query().Get("image"))
	if image == "" || len(image) > 300 {
		writeErr(w, 400, "enter an image name")
		return
	}
	if !s.imageLimit.Allow(strconv.FormatInt(u.ID, 10)) {
		writeErr(w, 429, "too many image lookups; wait a minute")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()

	info, err := s.docker.ImageConfig(ctx, image)
	source := "server"
	if err != nil {
		var auth *docker.RegistryAuth
		if auth, err = s.imageLogin(ctx, image, r.URL.Query().Get("registryId")); err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		source = "registry"
		info, err = s.registry.ImageInfo(ctx, image, auth)
		if err != nil {
			msg := err.Error()
			switch {
			case errors.Is(err, docker.ErrRegistryDenied) && auth == nil:
				msg = "the registry didn't find this image or wants a login. Check the name, or if the image is private, add a login under Settings › Registries"
			case errors.Is(err, docker.ErrRegistryDenied):
				msg = "the registry refused the saved login for this image"
			case errors.Is(err, context.DeadlineExceeded):
				msg = "the registry took too long to answer"
			}
			if auth != nil {
				msg = strings.ReplaceAll(msg, auth.Password, "********")
			}
			writeJSON(w, 200, map[string]any{"found": false, "error": msg})
			return
		}
	}
	ports := info.Ports
	if ports == nil {
		ports = []int{}
	}
	writeJSON(w, 200, map[string]any{
		"found":  true,
		"source": source,
		"env":    info.Env,
		"ports":  ports,
		"port":   deploy.PickPort(ports),
	})
}

// imageLogin returns the saved login to read an image with, following the
// same rules as resolveRegistry: a login only ever goes to its registry.
func (s *Server) imageLogin(ctx context.Context, image, want string) (*docker.RegistryAuth, error) {
	host := docker.RegistryHost(image)
	var g *store.Registry
	if want != "" && want != "null" {
		id, err := strconv.ParseInt(want, 10, 64)
		if err != nil || id < 0 {
			return nil, errors.New("bad registryId")
		}
		if id == 0 {
			return nil, nil
		}
		if g, err = s.store.Registry(ctx, id); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return nil, errors.New("that registry login doesn't exist")
			}
			return nil, err
		}
		if g.Server != host {
			return nil, fmt.Errorf("the registry login %q is for %s, but this image is on %s", g.Name, g.Server, host)
		}
	} else {
		regs, err := s.store.Registries(ctx)
		if err != nil {
			return nil, err
		}
		for i := range regs {
			if regs[i].Server == host {
				g = &regs[i]
				break
			}
		}
	}
	if g == nil {
		return nil, nil
	}
	return &docker.RegistryAuth{Server: g.Server, Username: g.Username, Password: g.Password}, nil
}
