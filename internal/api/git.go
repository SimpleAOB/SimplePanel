package api

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/SimpleAOB/SimplePanel/internal/deploy"
	"github.com/SimpleAOB/SimplePanel/internal/gitprovider"
	"github.com/SimpleAOB/SimplePanel/internal/notify"
	"github.com/SimpleAOB/SimplePanel/internal/store"
)

// Git integration (0.3): GitHub Apps created with the manifest flow, GitLab
// tokens, push-to-deploy and pull request previews.

// publicURL is the address GitHub and GitLab reach the panel at, and the
// base of links in commit statuses: the panel domain, else SP_PUBLIC_URL,
// else the address the admin used when connecting a provider, else the
// address of the current request.
func (s *Server) publicURL(r *http.Request) string {
	ctx := context.Background()
	if pd, _ := s.store.Setting(ctx, "panel_domain"); pd != "" {
		return "https://" + pd
	}
	if v := strings.TrimRight(os.Getenv("SP_PUBLIC_URL"), "/"); v != "" {
		return v
	}
	if v, _ := s.store.Setting(ctx, "public_url"); v != "" {
		return v
	}
	if r != nil {
		scheme := "http"
		if isHTTPS(r) {
			scheme = "https"
		}
		return scheme + "://" + r.Host
	}
	return ""
}

func (s *Server) rememberPublicURL(r *http.Request) {
	if pd, _ := s.store.Setting(r.Context(), "panel_domain"); pd != "" || os.Getenv("SP_PUBLIC_URL") != "" {
		return
	}
	_ = s.store.SetSetting(r.Context(), "public_url", s.publicURL(r))
}

// ---- Providers ----

type providerView struct {
	store.GitProvider
	Apps       int    `json:"apps"`
	InstallURL string `json:"installUrl,omitempty"`
	HookURL    string `json:"hookUrl,omitempty"`
}

func (s *Server) providerView(r *http.Request, p *store.GitProvider) providerView {
	v := providerView{GitProvider: *p}
	v.Apps, _ = s.store.ProviderAppCount(r.Context(), p.ID)
	if p.Kind == store.ProviderGitHub {
		v.InstallURL = s.git.InstallURL(p)
	}
	return v
}

func (s *Server) listProviders(w http.ResponseWriter, r *http.Request, u *store.User) {
	ps, err := s.store.GitProviders(r.Context())
	if err != nil {
		s.storeErr(w, err)
		return
	}
	out := make([]providerView, len(ps))
	for i := range ps {
		out[i] = s.providerView(r, &ps[i])
		if !u.IsAdmin() {
			out[i].InstallURL = ""
		}
	}
	writeJSON(w, 200, out)
}

func (s *Server) loadProvider(w http.ResponseWriter, r *http.Request) (*store.GitProvider, bool) {
	id, ok := pathID(r)
	if !ok {
		writeErr(w, 400, "bad id")
		return nil, false
	}
	p, err := s.store.GitProvider(r.Context(), id)
	if err != nil {
		s.storeErr(w, err)
		return nil, false
	}
	return p, true
}

// getProvider shows a provider and, for GitHub, where the app is installed.
func (s *Server) getProvider(w http.ResponseWriter, r *http.Request, u *store.User) {
	if !requireAdmin(w, u) {
		return
	}
	p, ok := s.loadProvider(w, r)
	if !ok {
		return
	}
	out := map[string]any{"provider": s.providerView(r, p)}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	if insts, err := s.git.Installations(ctx, p); err != nil {
		out["error"] = err.Error()
	} else {
		out["installations"] = insts
	}
	writeJSON(w, 200, out)
}

// canPickRepos: anyone who can deploy somewhere may list repositories.
func (s *Server) canPickRepos(w http.ResponseWriter, u *store.User) bool {
	if u.Role == store.RoleViewer || (u.Scope != "" && store.ScopeRank(u.Scope) < store.ScopeRank(store.ScopeManage)) {
		writeErr(w, 403, "your role can't connect repositories")
		return false
	}
	return true
}

func (s *Server) providerRepos(w http.ResponseWriter, r *http.Request, u *store.User) {
	if !s.canPickRepos(w, u) {
		return
	}
	p, ok := s.loadProvider(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	repos, err := s.git.Repos(ctx, p)
	if err != nil {
		writeErr(w, 502, "listing repositories failed: "+err.Error())
		return
	}
	writeJSON(w, 200, repos)
}

var repoRe = regexp.MustCompile(`^[A-Za-z0-9_.-]+(/[A-Za-z0-9_.-]+){1,20}$`)

func validRepo(repo string) error {
	if !repoRe.MatchString(repo) || strings.Contains(repo, "..") || len(repo) > 255 {
		return errors.New("repository must look like owner/name")
	}
	return nil
}

func (s *Server) providerBranches(w http.ResponseWriter, r *http.Request, u *store.User) {
	if !s.canPickRepos(w, u) {
		return
	}
	p, ok := s.loadProvider(w, r)
	if !ok {
		return
	}
	repo := r.URL.Query().Get("repo")
	if err := validRepo(repo); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	bs, err := s.git.Branches(ctx, p, repo)
	if err != nil {
		writeErr(w, 502, "listing branches failed: "+err.Error())
		return
	}
	writeJSON(w, 200, bs)
}

func (s *Server) deleteProvider(w http.ResponseWriter, r *http.Request, u *store.User) {
	if !requireAdmin(w, u) {
		return
	}
	p, ok := s.loadProvider(w, r)
	if !ok {
		return
	}
	if err := s.store.DeleteGitProvider(r.Context(), p.ID); err != nil {
		s.storeErr(w, err)
		return
	}
	out := map[string]any{"ok": true}
	if p.Kind == store.ProviderGitHub {
		// The app itself lives on GitHub; only its owner can delete it.
		out["deleteUrl"] = p.HTMLURL
	}
	writeJSON(w, 200, out)
}

// ---- GitHub App manifest flow ----

type manifestState struct {
	userID  int64
	expires time.Time
}

type manifestStates struct {
	mu sync.Mutex
	m  map[string]manifestState
}

func (m *manifestStates) put(state string, st manifestState) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.m == nil {
		m.m = map[string]manifestState{}
	}
	for k, v := range m.m {
		if time.Now().After(v.expires) {
			delete(m.m, k)
		}
	}
	m.m[state] = st
}

// take returns and forgets a state (it can be used once).
func (m *manifestStates) take(state string) (manifestState, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	st, ok := m.m[state]
	delete(m.m, state)
	return st, ok && time.Now().Before(st.expires)
}

var ghOrgRe = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9-]{0,38})$`)

// githubManifest starts creating a GitHub App: it returns the manifest and
// the GitHub URL the browser posts it to. GitHub then sends the user back
// to githubCallback with a one-time code.
func (s *Server) githubManifest(w http.ResponseWriter, r *http.Request, u *store.User) {
	if !requireAdmin(w, u) {
		return
	}
	var body struct {
		Name string `json:"name"`
		Org  string `json:"org"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	body.Name = strings.TrimSpace(body.Name)
	if body.Name == "" || len(body.Name) > 34 {
		writeErr(w, 400, "GitHub App names are 1 to 34 characters")
		return
	}
	body.Org = strings.TrimSpace(body.Org)
	if body.Org != "" && !ghOrgRe.MatchString(body.Org) {
		writeErr(w, 400, "that isn't a valid GitHub organization name")
		return
	}
	base := s.publicURL(r)
	manifest := map[string]any{
		"name":            body.Name,
		"url":             base,
		"hook_attributes": map[string]any{"url": base + "/api/hooks/github", "active": true},
		"redirect_url":    base + "/api/git/github/callback",
		"setup_url":       base + "/api/git/github/setup",
		"setup_on_update": true,
		"public":          false,
		"default_permissions": map[string]string{
			"contents": "read", "metadata": "read", "pull_requests": "write", "statuses": "write",
		},
		"default_events": []string{"push", "pull_request"},
	}
	mj, _ := json.Marshal(manifest)
	state := store.RandomToken(24)
	s.manifests.put(state, manifestState{userID: u.ID, expires: time.Now().Add(time.Hour)})
	target := s.git.GitHubWeb + "/settings/apps/new"
	if body.Org != "" {
		target = s.git.GitHubWeb + "/organizations/" + url.PathEscape(body.Org) + "/settings/apps/new"
	}
	out := map[string]any{"url": target + "?state=" + state, "manifest": string(mj), "publicUrl": base}
	if !strings.HasPrefix(base, "https://") {
		out["warning"] = "GitHub will send events to " + base + " without HTTPS. Set a panel domain in Settings → General first if you can."
	}
	writeJSON(w, 200, out)
}

// githubCallback is where GitHub sends the browser after the app is
// created. The session cookie is SameSite=Strict, so it isn't sent on this
// cross-site navigation: the one-time state (tied to the admin who started
// the flow) authenticates the request instead.
func (s *Server) githubCallback(w http.ResponseWriter, r *http.Request) {
	if !s.hookLimit.Allow("gh-callback:" + clientIP(r)) {
		writeErr(w, 429, "rate limited")
		return
	}
	st, ok := s.manifests.take(r.URL.Query().Get("state"))
	if !ok {
		http.Redirect(w, r, "/#/settings/git?error=expired", http.StatusSeeOther)
		return
	}
	user, err := s.store.User(r.Context(), st.userID)
	if err != nil || !user.IsAdmin() {
		http.Redirect(w, r, "/#/settings/git?error=expired", http.StatusSeeOther)
		return
	}
	code := r.URL.Query().Get("code")
	if code == "" || len(code) > 200 {
		http.Redirect(w, r, "/#/settings/git?error=code", http.StatusSeeOther)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	p, err := s.git.ConvertManifest(ctx, code)
	if err != nil {
		s.log.Error("GitHub App manifest conversion", "err", err)
		http.Redirect(w, r, "/#/settings/git?error=convert", http.StatusSeeOther)
		return
	}
	if err := s.store.CreateGitProvider(r.Context(), p); err != nil {
		s.storeErr(w, err)
		return
	}
	s.rememberPublicURL(r)
	_ = s.store.AddAudit(r.Context(), &store.AuditEntry{Source: "ui", UserID: &user.ID, Username: user.Username,
		Action: "connect GitHub", Target: p.Name, OK: true})
	s.log.Info("GitHub App created", "app", p.Slug, "owner", p.Owner, "user", user.Username)
	// Straight on to choosing which repositories the app can see.
	http.Redirect(w, r, s.git.InstallURL(p), http.StatusSeeOther)
}

// githubSetup is where GitHub sends the browser after the app is installed.
func (s *Server) githubSetup(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/#/settings/git", http.StatusSeeOther)
}

// ---- GitLab ----

func (s *Server) connectGitLab(w http.ResponseWriter, r *http.Request, u *store.User) {
	if !requireAdmin(w, u) {
		return
	}
	var body struct {
		Name    string `json:"name"`
		BaseURL string `json:"baseUrl"`
		Token   string `json:"token"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	base := strings.TrimRight(strings.TrimSpace(body.BaseURL), "/")
	if base == "" {
		base = "https://gitlab.com"
	}
	bu, err := url.Parse(base)
	if err != nil || bu.Scheme != "https" || bu.Host == "" || bu.User != nil || bu.RawQuery != "" {
		writeErr(w, 400, "GitLab URL must be https://, e.g. https://gitlab.com")
		return
	}
	tok := strings.TrimSpace(body.Token)
	if tok == "" || len(tok) > 256 || strings.ContainsAny(tok, " \r\n") {
		writeErr(w, 400, "paste a GitLab access token with the api scope")
		return
	}
	p := &store.GitProvider{Kind: store.ProviderGitLab, BaseURL: base, Token: tok, WebhookSecret: store.RandomToken(24), HTMLURL: base}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	user, err := s.git.CheckGitLab(ctx, p)
	if err != nil {
		writeErr(w, 400, "GitLab didn't accept the token: "+err.Error())
		return
	}
	p.Owner = user
	p.Name = strings.TrimSpace(body.Name)
	if p.Name == "" {
		p.Name = bu.Host + " (" + user + ")"
	}
	if len(p.Name) > 60 {
		p.Name = p.Name[:60]
	}
	if err := s.store.CreateGitProvider(r.Context(), p); err != nil {
		s.storeErr(w, err)
		return
	}
	s.rememberPublicURL(r)
	writeJSON(w, 201, s.providerView(r, p))
}

func (s *Server) updateProvider(w http.ResponseWriter, r *http.Request, u *store.User) {
	if !requireAdmin(w, u) {
		return
	}
	p, ok := s.loadProvider(w, r)
	if !ok {
		return
	}
	var body struct {
		Name  string `json:"name"`
		Token string `json:"token"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	name := strings.TrimSpace(body.Name)
	if name == "" || len(name) > 60 {
		writeErr(w, 400, "give it a name of up to 60 characters")
		return
	}
	tok := strings.TrimSpace(body.Token)
	if tok != "" {
		if p.Kind != store.ProviderGitLab {
			writeErr(w, 400, "only GitLab connections use a token")
			return
		}
		check := *p
		check.Token = tok
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()
		if _, err := s.git.CheckGitLab(ctx, &check); err != nil {
			writeErr(w, 400, "GitLab didn't accept the token: "+err.Error())
			return
		}
	}
	if err := s.store.UpdateGitProviderToken(r.Context(), p.ID, name, tok); err != nil {
		s.storeErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// ---- Linking apps to repositories ----

// linkRepo connects a to a provider's repository (or disconnects it when
// providerID is 0) and sets its clone URL.
func (s *Server) linkRepo(r *http.Request, a *store.App, providerID int64, repo string) error {
	if providerID == 0 {
		a.GitProviderID, a.GitRepo = 0, ""
		return nil
	}
	if a.IsDatabase() {
		return errors.New("databases can't be connected to a repository")
	}
	repo = strings.TrimSuffix(strings.TrimSpace(repo), ".git")
	if err := validRepo(repo); err != nil {
		return err
	}
	if a.GitProviderID == providerID && strings.EqualFold(a.GitRepo, repo) && a.Source == "git" {
		return nil // unchanged
	}
	p, err := s.store.GitProvider(r.Context(), providerID)
	if err != nil {
		return errors.New("that Git connection doesn't exist")
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	info, err := s.git.RepoInfo(ctx, p, repo)
	if err != nil {
		return fmt.Errorf("%s can't reach %s: %v", p.Name, repo, err)
	}
	if err := s.git.EnsureHook(ctx, p, info.FullName, s.publicURL(r)+fmt.Sprintf("/api/hooks/gitlab/%d", p.ID)); err != nil {
		return err
	}
	a.Source, a.GitProviderID, a.GitRepo = "git", p.ID, info.FullName
	a.GitURL = s.git.RepoCloneURL(p, info.FullName)
	return nil
}

// ---- Deploy keys ----

func (s *Server) createDeployKey(w http.ResponseWriter, r *http.Request, u *store.User) {
	a, ok := s.loadApp(w, r, u, store.LevelDeploy)
	if !ok {
		return
	}
	if a.IsDatabase() || a.IsPreview() {
		writeErr(w, 400, "only apps built from Git have deploy keys")
		return
	}
	priv, pub, err := deploy.NewDeployKey("simplepanel-" + a.ServiceName())
	if err != nil {
		s.storeErr(w, err)
		return
	}
	if err := s.store.SetDeployKey(r.Context(), a.ID, priv, pub); err != nil {
		s.storeErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]string{"publicKey": pub, "addUrl": deploy.DeployKeyURL(a.GitURL)})
}

func (s *Server) deleteDeployKey(w http.ResponseWriter, r *http.Request, u *store.User) {
	a, ok := s.loadApp(w, r, u, store.LevelDeploy)
	if !ok {
		return
	}
	if err := s.store.SetDeployKey(r.Context(), a.ID, "", ""); err != nil {
		s.storeErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// ---- Previews ----

func (s *Server) listPreviews(w http.ResponseWriter, r *http.Request, u *store.User) {
	a, ok := s.loadApp(w, r, u, store.LevelView)
	if !ok {
		return
	}
	ps, err := s.store.Previews(r.Context(), a.ID)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	wd, _ := s.store.Setting(r.Context(), "wildcard_domain")
	type row struct {
		ID       int64         `json:"id"`
		Name     string        `json:"name"`
		PRNumber int           `json:"prNumber"`
		PRTitle  string        `json:"prTitle"`
		PRURL    string        `json:"prUrl"`
		Branch   string        `json:"branch"`
		URL      string        `json:"url"`
		Status   deploy.Status `json:"status"`
		Updated  int64         `json:"updatedAt"`
	}
	out := make([]row, len(ps))
	for i := range ps {
		p := &ps[i]
		out[i] = row{ID: p.ID, Name: p.Name, PRNumber: p.PRNumber, PRTitle: p.PRTitle, PRURL: p.PRURL, Branch: p.GitBranch,
			Status: s.deploy.Status(r.Context(), p), Updated: p.UpdatedAt}
		if h := deploy.AutoHost(p, wd); h != "" {
			out[i].URL = "https://" + h
		}
	}
	writeJSON(w, 200, out)
}

// removeApp deletes a service and its previews.
func (s *Server) removeApp(ctx context.Context, a *store.App) error {
	previews, _ := s.store.Previews(ctx, a.ID)
	for i := range previews {
		if err := s.deploy.RemoveWithVolumes(ctx, &previews[i]); err != nil {
			return err
		}
	}
	if a.IsDatabase() {
		if err := s.deploy.RemovePublic(ctx, a); err != nil {
			return err
		}
	}
	var err error
	if a.IsPreview() {
		err = s.deploy.RemoveWithVolumes(ctx, a)
	} else {
		err = s.deploy.Remove(ctx, a)
	}
	return err
}

// ---- Webhooks ----

const maxHookBody = 5 << 20

func readHookBody(r *http.Request) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r.Body, maxHookBody+1))
	if err != nil {
		return nil, err
	}
	if len(b) > maxHookBody {
		return nil, errors.New("payload too large")
	}
	return b, nil
}

// githubPayload returns the JSON payload of a GitHub delivery, which may
// be form-encoded ("payload=…") depending on the webhook's content type.
func githubPayload(r *http.Request, body []byte) []byte {
	if strings.HasPrefix(r.Header.Get("Content-Type"), "application/x-www-form-urlencoded") {
		if v, err := url.ParseQuery(string(body)); err == nil {
			return []byte(v.Get("payload"))
		}
	}
	return body
}

func validGitHubSignature(secret string, body []byte, sig string) bool {
	hexSig, ok := strings.CutPrefix(sig, "sha256=")
	if !ok || secret == "" {
		return false
	}
	want, err := hex.DecodeString(hexSig)
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hmac.Equal(mac.Sum(nil), want)
}

// githubHook receives events for every GitHub App this panel created. The
// signature identifies (and authenticates) the app.
func (s *Server) githubHook(w http.ResponseWriter, r *http.Request) {
	if !s.hookLimit.Allow(clientIP(r)) {
		writeErr(w, 429, "rate limited")
		return
	}
	body, err := readHookBody(r)
	if err != nil {
		writeErr(w, 413, err.Error())
		return
	}
	ps, err := s.store.GitProviders(r.Context())
	if err != nil {
		s.storeErr(w, err)
		return
	}
	var p *store.GitProvider
	sig := r.Header.Get("X-Hub-Signature-256")
	for i := range ps {
		if ps[i].Kind == store.ProviderGitHub && validGitHubSignature(ps[i].WebhookSecret, body, sig) {
			p = &ps[i]
			break
		}
	}
	if p == nil {
		writeErr(w, 401, "bad signature")
		return
	}
	ev, err := gitprovider.ParseGitHub(r.Header.Get("X-GitHub-Event"), githubPayload(r, body))
	if err != nil {
		writeErr(w, 400, "invalid payload")
		return
	}
	s.providerEvent(w, r, p, ev)
}

// gitlabHook receives events from GitLab projects linked through a GitLab
// connection; the X-Gitlab-Token header carries the connection's secret.
func (s *Server) gitlabHook(w http.ResponseWriter, r *http.Request) {
	if !s.hookLimit.Allow(clientIP(r)) {
		writeErr(w, 429, "rate limited")
		return
	}
	id, ok := pathID(r)
	if !ok {
		writeErr(w, 404, "not found")
		return
	}
	p, err := s.store.GitProvider(r.Context(), id)
	if err != nil || p.Kind != store.ProviderGitLab ||
		subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Gitlab-Token")), []byte(p.WebhookSecret)) != 1 {
		writeErr(w, 401, "bad token")
		return
	}
	body, err := readHookBody(r)
	if err != nil {
		writeErr(w, 413, err.Error())
		return
	}
	ev, err := gitprovider.ParseGitLab(r.Header.Get("X-Gitlab-Event"), body)
	if err != nil {
		writeErr(w, 400, "invalid payload")
		return
	}
	s.providerEvent(w, r, p, ev)
}

func (s *Server) providerEvent(w http.ResponseWriter, r *http.Request, p *store.GitProvider, ev *gitprovider.Event) {
	if ev.Ignored() {
		writeJSON(w, 200, map[string]string{"skipped": "event " + ev.Name + " ignored"})
		return
	}
	apps, err := s.store.AppsByRepo(r.Context(), p.ID, ev.Repo)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	results := []string{}
	for i := range apps {
		results = append(results, apps[i].ServiceName()+": "+s.handleGitEvent(r, &apps[i], ev, true))
	}
	if len(results) == 0 {
		results = append(results, "no service is connected to "+ev.Repo)
	}
	writeJSON(w, 200, map[string]any{"results": results})
}

// appBranch is the branch an app deploys: its own, else the default.
func appBranch(a *store.App, ev *gitprovider.Event) string {
	if a.GitBranch != "" {
		return a.GitBranch
	}
	return ev.DefaultBranch
}

// handleGitEvent acts on a push or pull request for one app and says what
// it did. linked is false for the app's own secret webhook URL, which
// always deploys on a push (the URL is the opt-in).
func (s *Server) handleGitEvent(r *http.Request, a *store.App, ev *gitprovider.Event, linked bool) string {
	ctx := r.Context()
	switch ev.Kind {
	case "push":
		if ev.Deleted {
			return "branch deleted; nothing to deploy"
		}
		if b := appBranch(a, ev); b != "" && ev.Branch != b {
			return "push to " + ev.Branch + " ignored; deploys " + b
		}
		if linked && !a.AutoDeploy {
			return "automatic deploys are off"
		}
		trigger := "push"
		if ev.SHA != "" {
			trigger = "push " + shortSHA(ev.SHA)
		}
		d, err := s.deploy.Deploy(ctx, a.ID, trigger)
		if err != nil {
			return "deploy failed: " + err.Error()
		}
		return fmt.Sprintf("deploying (#%d)", d.ID)
	case "pr":
		return s.previewEvent(r, a, ev)
	}
	return "ignored"
}

func shortSHA(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

var branchSafe = regexp.MustCompile(`^[A-Za-z0-9._/-]{1,200}$`)

// previewEvent creates, updates or removes a pull request preview.
func (s *Server) previewEvent(r *http.Request, parent *store.App, ev *gitprovider.Event) string {
	ctx := r.Context()
	if parent.Source != "git" || parent.IsDatabase() {
		return "not a Git app"
	}
	existing, err := s.store.Preview(ctx, parent.ID, ev.PR)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return "error: " + err.Error()
	}
	if ev.PRAction == "close" {
		if existing == nil {
			return "no preview to remove"
		}
		if err := s.deletePreview(ctx, existing); err != nil {
			return "removing the preview failed: " + err.Error()
		}
		if p := s.providerFor(existing); p != nil && existing.PRCommentID != "" {
			go s.previewComment(p, parent, existing, "removed", "")
		}
		return fmt.Sprintf("preview of #%d removed", ev.PR)
	}
	if !parent.Previews {
		return "pull request previews are off"
	}
	if ev.Fork {
		return "pull requests from forks don't get previews"
	}
	if b := appBranch(parent, ev); b != "" && ev.PRBase != b {
		return "pull request targets " + ev.PRBase + "; previews are for " + b
	}
	if deploy.ValidBranch(ev.Branch) != nil || !branchSafe.MatchString(ev.Branch) {
		return "unsupported branch name"
	}
	if ev.PRAction == "edit" {
		if existing == nil {
			return "no preview yet"
		}
		existing.PRTitle = ev.PRTitle
		if err := s.store.UpdateApp(ctx, existing); err != nil {
			return "error: " + err.Error()
		}
		return "preview title updated"
	}
	p := existing
	if p == nil {
		n, _ := s.store.Previews(ctx, parent.ID)
		if len(n) >= deploy.MaxPreviews {
			return fmt.Sprintf("this app already has %d previews; close some pull requests first", deploy.MaxPreviews)
		}
		p = &store.App{Name: deploy.PreviewName(parent.Name, ev.PR)}
		deploy.SyncPreview(p, parent, ev.Branch, ev.PRTitle, ev.PRURL)
		if err := s.store.CreatePreview(ctx, p, parent.ID, ev.PR); err != nil {
			if errors.Is(err, store.ErrConflict) {
				return "a service called " + p.Name + " already exists"
			}
			return "error: " + err.Error()
		}
		_ = s.deploy.SyncProxy(ctx)
	} else {
		deploy.SyncPreview(p, parent, ev.Branch, ev.PRTitle, ev.PRURL)
		if err := s.store.UpdateApp(ctx, p); err != nil {
			return "error: " + err.Error()
		}
	}
	trigger := fmt.Sprintf("pr #%d", ev.PR)
	if ev.SHA != "" {
		trigger += " " + shortSHA(ev.SHA)
	}
	d, err := s.deploy.Deploy(ctx, p.ID, trigger)
	if err != nil {
		return "deploy failed: " + err.Error()
	}
	return fmt.Sprintf("deploying preview %s (#%d)", p.Name, d.ID)
}

func (s *Server) deletePreview(ctx context.Context, p *store.App) error {
	if err := s.deploy.RemoveWithVolumes(ctx, p); err != nil {
		return err
	}
	if err := s.store.DeleteApp(ctx, p.ID); err != nil {
		return err
	}
	return s.deploy.SyncProxy(ctx)
}

// ---- Commit statuses and preview comments ----

func (s *Server) providerFor(a *store.App) *store.GitProvider {
	if a.GitProviderID == 0 || a.GitRepo == "" {
		return nil
	}
	p, err := s.store.GitProvider(context.Background(), a.GitProviderID)
	if err != nil {
		return nil
	}
	return p
}

func (s *Server) statusName(a *store.App) string {
	if a.IsPreview() {
		if parent, err := s.store.App(context.Background(), a.ParentAppID); err == nil {
			return "SimplePanel: " + a.ProjectName + "/" + parent.Name + " (preview)"
		}
	}
	return "SimplePanel: " + a.ProjectName + "/" + a.Name
}

func (s *Server) deploymentLink(a *store.App) string {
	base := s.publicURL(nil)
	if base == "" {
		return ""
	}
	return fmt.Sprintf("%s/#/p/%d/a/%d/deployments", base, a.ProjectID, a.ID)
}

// GitOnCommit marks the commit a deployment builds as pending.
func (s *Server) GitOnCommit(a *store.App, depID int64, sha string) {
	p := s.providerFor(a)
	if p == nil {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if err := s.git.SetStatus(ctx, p, a.GitRepo, sha, gitprovider.StatePending, s.statusName(a), s.deploymentLink(a), "Deploying…"); err != nil {
			s.log.Warn("commit status", "repo", a.GitRepo, "err", err)
		}
	}()
}

// GitOnDeploy reports a finished deployment on its commit and, for a
// preview, comments the preview's address on the pull request.
func (s *Server) GitOnDeploy(a *store.App, d *store.Deployment) {
	p := s.providerFor(a)
	if p == nil || d.CommitSHA == "" {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		state, desc := gitprovider.StateSuccess, "Deployed"
		if d.Status != "succeeded" {
			state, desc = gitprovider.StateFailure, "Deploy failed"
		}
		if err := s.git.SetStatus(ctx, p, a.GitRepo, d.CommitSHA, state, s.statusName(a), s.deploymentLink(a), desc); err != nil {
			s.log.Warn("commit status", "repo", a.GitRepo, "err", err)
		}
		if a.IsPreview() {
			parent, err := s.store.App(ctx, a.ParentAppID)
			if err == nil {
				s.previewComment(p, parent, a, d.Status, d.CommitSHA)
			}
		}
	}()
}

// PreviewURL is the address of a preview, or "" without a wildcard domain.
func (s *Server) PreviewURL(a *store.App) string {
	wd, _ := s.store.Setting(context.Background(), "wildcard_domain")
	if h := deploy.AutoHost(a, wd); h != "" {
		return "https://" + h
	}
	return ""
}

func (s *Server) previewComment(p *store.GitProvider, parent, pv *store.App, status, sha string) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var b strings.Builder
	fmt.Fprintf(&b, "**SimplePanel preview** of `%s/%s`\n\n", parent.ProjectName, parent.Name)
	switch status {
	case "succeeded":
		if u := s.PreviewURL(pv); u != "" {
			fmt.Fprintf(&b, "✅ Ready: %s\n\n", u)
		} else {
			b.WriteString("✅ Deployed, but it has no address yet: set a wildcard domain in SimplePanel's settings.\n\n")
		}
	case "removed":
		b.WriteString("The preview was removed because the pull request was closed.\n")
	default:
		b.WriteString("❌ The deploy failed.\n\n")
	}
	if sha != "" {
		fmt.Fprintf(&b, "Commit `%s`", shortSHA(sha))
		if link := s.deploymentLink(pv); link != "" {
			fmt.Fprintf(&b, " · [build log](%s)", link)
		}
		b.WriteString("\n")
	}
	id, err := s.git.UpsertComment(ctx, p, pv.GitRepo, pv.PRNumber, pv.PRCommentID, b.String())
	if err != nil {
		s.log.Warn("preview comment", "repo", pv.GitRepo, "pr", pv.PRNumber, "err", err)
		return
	}
	if id != pv.PRCommentID && status != "removed" {
		if cur, err := s.store.App(ctx, pv.ID); err == nil {
			cur.PRCommentID = id
			_ = s.store.UpdateApp(ctx, cur)
		}
	}
}

// PreviewEvent picks the notification for a finished preview deploy.
func PreviewEvent(a *store.App, d *store.Deployment, url string) (notify.Event, bool) {
	if !a.IsPreview() || d.Status != "succeeded" {
		return notify.Event{}, false
	}
	msg := "Pull request #" + fmt.Sprint(a.PRNumber) + ": " + a.PRTitle
	if url != "" {
		msg += "\n" + url
	}
	return notify.Event{Type: notify.PreviewReady, Level: "success", ProjectID: a.ProjectID, Project: a.ProjectName,
		Service: a.Name, Title: fmt.Sprintf("Preview of %s/%s is ready", a.ProjectName, a.Name), Message: msg,
		Link: fmt.Sprintf("/#/p/%d/a/%d", a.ProjectID, a.ID)}, true
}
