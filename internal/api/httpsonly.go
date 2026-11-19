package api

import (
	"context"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
)

// HTTPS only: once the panel has a domain with HTTPS, owners can turn off
// plain HTTP on the panel's port (3000). Passwords, session cookies and
// API/MCP tokens then never cross the internet unencrypted.
//
// It can't lock anyone out:
//   - it can only be turned on from the HTTPS address, so that works;
//   - clearing the panel domain turns it off again;
//   - requests from the server itself or its private network (an SSH
//     tunnel to 127.0.0.1:3000, the installer's health check) still work;
//   - SP_ALLOW_HTTP=1 on the simplepanel service turns it off.

const httpsOnlyKey = "https_only"

type httpsOnlyCache struct {
	mu     sync.Mutex
	loaded bool
	on     bool
}

func (s *Server) httpsOnly(ctx context.Context) bool {
	if os.Getenv("SP_ALLOW_HTTP") == "1" {
		return false
	}
	s.https.mu.Lock()
	defer s.https.mu.Unlock()
	if !s.https.loaded {
		v, err := s.store.Setting(ctx, httpsOnlyKey)
		if err != nil {
			return false
		}
		s.https.on, s.https.loaded = v == "1", true
	}
	return s.https.on
}

func (s *Server) setHTTPSOnly(ctx context.Context, on bool) error {
	v := ""
	if on {
		v = "1"
	}
	if err := s.store.SetSetting(ctx, httpsOnlyKey, v); err != nil {
		return err
	}
	s.https.mu.Lock()
	s.https.on, s.https.loaded = on, true
	s.https.mu.Unlock()
	return nil
}

// privatePeer reports a direct connection from this server or its private
// network: Traefik on the overlay network, an SSH tunnel, the installer.
func privatePeer(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(host)
	return ip != nil && (ip.IsPrivate() || ip.IsLoopback())
}

// viaProxyHTTPS reports a request that reached the panel over HTTPS
// through Traefik. X-Forwarded-Proto only counts from a private peer: a
// direct visitor could send the header themselves.
func viaProxyHTTPS(r *http.Request) bool {
	return r.TLS != nil || (privatePeer(r) && r.Header.Get("X-Forwarded-Proto") == "https")
}

func hostOnly(h string) string {
	if host, _, err := net.SplitHostPort(h); err == nil {
		return host
	}
	return h
}

func orEmpty(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

// enforceHTTPS answers a plain-HTTP request from the internet while HTTPS
// only is on, and reports whether it did.
func (s *Server) enforceHTTPS(w http.ResponseWriter, r *http.Request) bool {
	if r.URL.Path == "/api/health" || privatePeer(r) || !s.httpsOnly(r.Context()) {
		return false
	}
	pd, _ := s.store.Setting(r.Context(), "panel_domain")
	if pd == "" {
		return false
	}
	target := "https://" + pd + r.URL.RequestURI()
	if r.Method == http.MethodGet && !strings.HasPrefix(r.URL.Path, "/api/") && r.URL.Path != "/mcp" {
		http.Redirect(w, r, target, http.StatusPermanentRedirect)
		return true
	}
	writeErr(w, http.StatusForbidden, "plain HTTP is turned off for this panel; use https://"+pd)
	return true
}
