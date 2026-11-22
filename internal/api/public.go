package api

// Public access to one-click databases: let database tools connect from
// the internet through a proxy with an allowlist and TLS.

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/SimpleAOB/SimplePanel/internal/deploy"
	"github.com/SimpleAOB/SimplePanel/internal/publicproxy"
	"github.com/SimpleAOB/SimplePanel/internal/store"
)

// maxAllowEntries caps the allowlist; longer lists belong in a firewall.
const maxAllowEntries = 100

// reservedPort says why port can't be published ("" if it can).
func reservedPort(p int) string {
	switch {
	case p == 80 || p == 443 || p == 3000 || (p == publicPort() && p != 0):
		return fmt.Sprintf("port %d is used by SimplePanel", p)
	case p == 22:
		return "port 22 is the server's SSH port; publishing it would lock you out of the server"
	case p == 2377 || p == 7946 || p == 4789:
		return fmt.Sprintf("port %d is used by Docker Swarm itself", p)
	}
	return ""
}

// publicPortProblem checks a public database port: its range, the ports
// SimplePanel and Swarm need, and anything already using it.
func (s *Server) publicPortProblem(ctx context.Context, a *store.App, port int) (string, error) {
	if port < 1024 || port > 65535 {
		return "choose a port between 1024 and 65535", nil
	}
	if why := reservedPort(port); why != "" {
		return why, nil
	}
	return s.deploy.PortUser(ctx, port, a)
}

type publicInfo struct {
	Enabled     bool     `json:"enabled"`
	Port        int      `json:"port"`
	Allow       []string `json:"allow"`
	RequireTLS  bool     `json:"requireTls"`
	Host        string   `json:"host"`
	YourIP      string   `json:"yourIp,omitempty"`
	Suggested   int      `json:"suggestedPort,omitempty"`
	Weak        string   `json:"passwordWeak,omitempty"`
	MinPassword int      `json:"minPassword"`
	Fingerprint string   `json:"fingerprint,omitempty"`
	// ServerTLS: the database encrypts with its own certificate (MySQL,
	// MariaDB) and the proxy only passes it through.
	ServerTLS    bool   `json:"serverTls"`
	State        string `json:"state,omitempty"`
	StateMessage string `json:"stateMessage,omitempty"`
	CanChange    bool   `json:"canChange"`
}

// publicHost is the address database tools connect to: the server's IP
// when SimplePanel knows it, else the address the panel was opened at.
func (s *Server) publicHost(r *http.Request) string {
	if ip, _ := s.store.Setting(r.Context(), "server_ip"); ip != "" {
		return ip
	}
	h := hostOnly(r.Host)
	if net.ParseIP(h) != nil && !isLocalIP(net.ParseIP(h)) {
		return h
	}
	if pd, _ := s.store.Setting(r.Context(), "panel_domain"); pd != "" {
		return pd
	}
	return h
}

func isLocalIP(ip net.IP) bool {
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsUnspecified()
}

func splitAllow(s string) []string {
	out := []string{}
	for _, f := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' || r == '\n' || r == '\r' || r == '\t' }) {
		out = append(out, f)
	}
	return out
}

func (s *Server) publicInfo(r *http.Request, a *store.App, lvl int) publicInfo {
	ctx := r.Context()
	info := publicInfo{
		Enabled: a.PublicPort > 0, Port: a.PublicPort, Allow: splitAllow(a.PublicAllow), RequireTLS: a.PublicTLS,
		Host: s.publicHost(r), MinPassword: deploy.MinPublicPassword,
		ServerTLS: !deploy.PublicProxyTerminatesTLS(a.Kind), CanChange: lvl >= store.LevelManage,
	}
	if ip := net.ParseIP(clientIP(r)); ip != nil && !isLocalIP(ip) {
		info.YourIP = ip.String()
	}
	if a.PublicCert != "" {
		info.Fingerprint, _ = publicproxy.Fingerprint(a.PublicCert)
	}
	if lvl >= store.LevelManage {
		info.Weak, _ = deploy.PasswordCheck(a)
	}
	if info.Enabled {
		info.State, info.StateMessage = s.deploy.PublicState(ctx, a)
	} else if info.CanChange {
		info.Suggested = s.deploy.SuggestPublicPort(ctx, a, reservedPort)
	}
	return info
}

// getPublic returns a database's public access settings.
func (s *Server) getPublic(w http.ResponseWriter, r *http.Request, u *store.User) {
	a, ok := s.loadDatabase(w, r, u, store.LevelView)
	if !ok {
		return
	}
	writeJSON(w, 200, s.publicInfo(r, a, s.level(r.Context(), u, a.ProjectID)))
}

func (s *Server) loadDatabase(w http.ResponseWriter, r *http.Request, u *store.User, need int) (*store.App, bool) {
	a, ok := s.loadApp(w, r, u, need)
	if !ok {
		return nil, false
	}
	if !a.IsDatabase() || deploy.EngineByKey(a.Kind) == nil {
		writeErr(w, 400, "only one-click databases have public access")
		return nil, false
	}
	return a, true
}

// checkPublicPort says whether a port is free for a database.
func (s *Server) checkPublicPort(w http.ResponseWriter, r *http.Request, u *store.User) {
	a, ok := s.loadDatabase(w, r, u, store.LevelManage)
	if !ok {
		return
	}
	port, err := strconv.Atoi(r.URL.Query().Get("port"))
	if err != nil {
		writeJSON(w, 200, map[string]any{"ok": false, "message": "enter a port number"})
		return
	}
	why, err := s.publicPortProblem(r.Context(), a, port)
	if err != nil {
		writeErr(w, 502, "checking the port failed: "+err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": why == "", "message": why})
}

// setPublic turns public access on or off, or changes its settings.
func (s *Server) setPublic(w http.ResponseWriter, r *http.Request, u *store.User) {
	a, ok := s.loadDatabase(w, r, u, store.LevelManage)
	if !ok {
		return
	}
	var in struct {
		Enabled    bool    `json:"enabled"`
		Port       int     `json:"port"`
		Allow      *string `json:"allow"` // nil keeps the current list
		RequireTLS *bool   `json:"requireTls"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	ctx := r.Context()
	lvl := s.level(ctx, u, a.ProjectID)
	if !in.Enabled {
		if err := s.deploy.RemovePublic(ctx, a); err != nil {
			writeErr(w, 502, "stopping the public proxy failed: "+err.Error())
			return
		}
		if err := s.store.SetPublicAccess(ctx, a.ID, 0, a.PublicAllow, a.PublicTLS, a.PublicCert, a.PublicKey); err != nil {
			s.storeErr(w, err)
			return
		}
		a.PublicPort = 0
		writeJSON(w, 200, s.publicInfo(r, a, lvl))
		return
	}
	if weak, _ := deploy.PasswordCheck(a); weak != "" {
		writeErr(w, 400, "password: the database's password "+weak+" before it can be reached from the internet. Generate a new password first.")
		return
	}
	why, err := s.publicPortProblem(ctx, a, in.Port)
	if err != nil {
		writeErr(w, 502, "checking the port failed: "+err.Error())
		return
	}
	if why != "" {
		writeErr(w, 400, "port: "+why)
		return
	}
	if in.Allow == nil {
		in.Allow = &a.PublicAllow
	}
	nets, err := publicproxy.ParseAllow(*in.Allow)
	if err != nil {
		writeErr(w, 400, "allowed addresses: "+err.Error())
		return
	}
	if len(nets) > maxAllowEntries {
		writeErr(w, 400, fmt.Sprintf("allowed addresses: use at most %d entries; a firewall suits longer lists", maxAllowEntries))
		return
	}
	var allow []string
	for _, n := range nets {
		if ones, bits := n.Mask.Size(); ones == bits {
			allow = append(allow, n.IP.String())
		} else {
			allow = append(allow, n.String())
		}
	}
	prev := *a
	a.PublicPort, a.PublicAllow = in.Port, strings.Join(allow, "\n")
	if in.RequireTLS != nil {
		a.PublicTLS = *in.RequireTLS
	} else if prev.PublicPort == 0 {
		a.PublicTLS = true
	}
	if deploy.PublicProxyTerminatesTLS(a.Kind) && a.PublicCert == "" {
		var hosts []string
		for _, k := range []string{"server_ip", "panel_domain"} {
			if v, _ := s.store.Setting(ctx, k); v != "" {
				hosts = append(hosts, v)
			}
		}
		if h := s.publicHost(r); h != "" {
			hosts = append(hosts, h)
		}
		a.PublicCert, a.PublicKey, err = publicproxy.NewCert(a.ServiceName(), hosts)
		if err != nil {
			s.storeErr(w, err)
			return
		}
	}
	if err := s.deploy.ApplyPublic(ctx, a); err != nil {
		// Put the proxy back the way it was.
		if prev.PublicPort > 0 {
			_ = s.deploy.ApplyPublic(context.WithoutCancel(ctx), &prev)
		} else {
			_ = s.deploy.RemovePublic(context.WithoutCancel(ctx), a)
		}
		writeErr(w, 400, err.Error())
		return
	}
	if err := s.store.SetPublicAccess(ctx, a.ID, a.PublicPort, a.PublicAllow, a.PublicTLS, a.PublicCert, a.PublicKey); err != nil {
		s.storeErr(w, err)
		return
	}
	writeJSON(w, 200, s.publicInfo(r, a, lvl))
}

// publicCert downloads the certificate the public proxy presents, so
// tools can verify it.
func (s *Server) publicCert(w http.ResponseWriter, r *http.Request, u *store.User) {
	a, ok := s.loadDatabase(w, r, u, store.LevelView)
	if !ok {
		return
	}
	if a.PublicCert == "" {
		writeErr(w, 404, "this database has no certificate")
		return
	}
	w.Header().Set("Content-Type", "application/x-pem-file")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s-%s.crt"`, a.ProjectName, a.Name))
	_, _ = w.Write([]byte(a.PublicCert))
}

// publicLog returns the public proxy's recent connections.
func (s *Server) publicLog(w http.ResponseWriter, r *http.Request, u *store.User) {
	a, ok := s.loadDatabase(w, r, u, store.LevelManage)
	if !ok {
		return
	}
	lines, err := s.deploy.PublicLog(r.Context(), a, 200)
	if err != nil {
		writeErr(w, 502, err.Error())
		return
	}
	type entry struct {
		Time   string `json:"time"`
		Event  string `json:"event"`
		IP     string `json:"ip"`
		Detail string `json:"detail"`
	}
	out := []entry{}
	for _, l := range lines {
		// Docker puts its own timestamp in front of the proxy's line.
		if ts, rest, ok := strings.Cut(l, " "); ok && len(ts) >= 20 && ts[10] == 'T' {
			if ts2, _, _ := strings.Cut(rest, " "); len(ts2) >= 20 && ts2[10] == 'T' {
				l = rest
			}
		}
		f := strings.SplitN(l, " ", 4)
		if len(f) < 4 || (f[1] != "connected" && f[1] != "refused" && f[1] != "error") {
			continue
		}
		out = append(out, entry{Time: f[0], Event: f[1], IP: f[2], Detail: f[3]})
	}
	if len(out) > 50 {
		out = out[len(out)-50:]
	}
	writeJSON(w, 200, map[string]any{"entries": out})
}

// changeDBPassword gives a database's main user a new generated password
// and updates the apps in its project that use the old one.
func (s *Server) changeDBPassword(w http.ResponseWriter, r *http.Request, u *store.User) {
	a, ok := s.loadDatabase(w, r, u, store.LevelManage)
	if !ok {
		return
	}
	ctx := r.Context()
	old := deploy.DatabaseInfo(a)
	if old == nil || old.Password == "" {
		writeErr(w, 400, "this database has no password to change")
		return
	}
	pw := store.RandomToken(16)
	if err := s.deploy.ChangePassword(ctx, a, pw); err != nil {
		writeErr(w, 502, "changing the password failed: "+err.Error())
		return
	}
	a.Env = deploy.WithPassword(a, a.Env, pw)
	if err := s.store.UpdateApp(ctx, a); err != nil {
		s.storeErr(w, err)
		return
	}
	out := map[string]any{"updated": []string{}}
	if a.Kind == "redis" {
		// Redis reads its password from the service's environment when it
		// starts, so the service must carry the new one.
		if d, err := s.deploy.Deploy(ctx, a.ID, "password"); err == nil {
			out["deployment"] = d
		}
	}
	apps, err := s.store.Apps(ctx, a.ProjectID)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	var updated []string
	for i := range apps {
		x := &apps[i]
		if x.ID == a.ID || x.IsPreview() {
			continue
		}
		env, changed := replacePassword(x.Env, old.Password, pw, []string{a.Name, a.ServiceName()})
		if !changed {
			continue
		}
		x.Env = env
		if err := s.store.UpdateApp(ctx, x); err != nil {
			s.log.Error("updating app after password change", "app", x.ServiceName(), "err", err)
			continue
		}
		if _, err := s.deploy.Deploy(ctx, x.ID, "password"); err != nil {
			s.log.Error("redeploying app after password change", "app", x.ServiceName(), "err", err)
		}
		updated = append(updated, x.Name)
	}
	if updated != nil {
		out["updated"] = updated
	}
	writeJSON(w, 200, out)
}

// replacePassword swaps old for pw in variables that hold the password
// itself or a connection URL naming one of hosts. Other values are left
// alone, so a short password can't change unrelated settings.
func replacePassword(env, old, pw string, hosts []string) (string, bool) {
	enc := func(p string) string {
		s := url.UserPassword("x", p).String()
		return s[2:]
	}
	lines := strings.Split(env, "\n")
	changed := false
	for i, l := range lines {
		k, v, ok := strings.Cut(l, "=")
		if !ok || strings.HasPrefix(strings.TrimSpace(k), "#") {
			continue
		}
		val := strings.Trim(strings.TrimSpace(v), `"'`)
		switch {
		case val == old:
			lines[i] = k + "=" + strings.Replace(v, old, pw, 1)
			changed = true
		case strings.Contains(v, "@") && namesHost(v, hosts):
			nv := strings.ReplaceAll(v, ":"+old+"@", ":"+pw+"@")
			nv = strings.ReplaceAll(nv, ":"+enc(old)+"@", ":"+enc(pw)+"@")
			if nv != v {
				lines[i] = k + "=" + nv
				changed = true
			}
		}
	}
	return strings.Join(lines, "\n"), changed
}

func namesHost(v string, hosts []string) bool {
	for _, h := range hosts {
		if strings.Contains(v, "@"+h+":") || strings.Contains(v, "@"+h+"/") || strings.HasSuffix(v, "@"+h) {
			return true
		}
	}
	return false
}
