// Package api is SimplePanel's HTTP layer: JSON API, auth and static UI.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/SimpleAOB/SimplePanel/internal/backup"
	"github.com/SimpleAOB/SimplePanel/internal/cron"
	"github.com/SimpleAOB/SimplePanel/internal/deploy"
	"github.com/SimpleAOB/SimplePanel/internal/docker"
	"github.com/SimpleAOB/SimplePanel/internal/gitprovider"
	"github.com/SimpleAOB/SimplePanel/internal/monitor"
	"github.com/SimpleAOB/SimplePanel/internal/notify"
	"github.com/SimpleAOB/SimplePanel/internal/store"
	"github.com/SimpleAOB/SimplePanel/internal/update"
)

const (
	sessionCookie = "sp_session"
	sessionTTL    = 7 * 24 * time.Hour
	csrfHeader    = "X-SimplePanel"
	maxBody       = 1 << 20
)

type Server struct {
	store   *store.Store
	docker  *docker.Client
	deploy  *deploy.Manager
	log     *slog.Logger
	ui      fs.FS
	version string

	backups *backup.Service
	monitor *monitor.Monitor
	notify  *notify.Notifier

	git       *gitprovider.Service
	cron      *cron.Service
	manifests manifestStates

	updater *update.Updater

	loginLimit   *limiter
	hookLimit    *limiter
	mcpAuthLimit *limiter // failed MCP token attempts per IP
}

func New(st *store.Store, dc *docker.Client, dm *deploy.Manager, ui fs.FS, log *slog.Logger, version string) *Server {
	return &Server{
		store: st, docker: dc, deploy: dm, ui: ui, log: log, version: version,
		loginLimit:   newLimiter(10, 5*time.Minute),
		hookLimit:    newLimiter(30, time.Minute),
		mcpAuthLimit: newLimiter(20, 10*time.Minute),
		git:          gitprovider.New(),
	}
}

// UseV03 attaches the 0.3 services: Git providers and cron jobs.
func (s *Server) UseV03(g *gitprovider.Service, c *cron.Service) *Server {
	s.git, s.cron = g, c
	return s
}

// Use attaches the v0.2 services: backups, monitoring and notifications.
func (s *Server) Use(b *backup.Service, m *monitor.Monitor, n *notify.Notifier) *Server {
	s.backups, s.monitor, s.notify = b, m, n
	return s
}

// Handler returns the root HTTP handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	// Public
	mux.HandleFunc("GET /api/health", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, map[string]string{"status": "ok"}) })
	mux.HandleFunc("GET /api/setup", s.getSetup)
	mux.HandleFunc("POST /api/setup", s.postSetup)
	mux.HandleFunc("POST /api/login", s.login)
	mux.HandleFunc("POST /api/hooks/deploy/{token}", s.webhookDeploy)
	mux.HandleFunc("POST /api/hooks/github", s.githubHook)
	mux.HandleFunc("POST /api/hooks/gitlab/{id}", s.gitlabHook)
	mux.HandleFunc("GET /api/git/github/callback", s.githubCallback)
	mux.HandleFunc("GET /api/git/github/setup", s.githubSetup)
	mux.HandleFunc("GET /api/invite/{token}", s.getInvite)
	mux.HandleFunc("POST /api/invite/{token}", s.acceptInvite)
	mux.HandleFunc("/mcp", s.mcp) // authenticated with API tokens, see mcp.go

	// Authenticated
	a := func(pattern string, h func(http.ResponseWriter, *http.Request, *store.User)) {
		mux.Handle(pattern, s.requireAuth(h))
	}
	a("POST /api/logout", s.logout)
	a("GET /api/me", s.me)
	a("POST /api/me/password", s.changePassword)
	a("GET /api/system", s.system)
	a("PUT /api/settings", s.putSettings)
	a("GET /api/tokens", s.listTokens)
	a("POST /api/tokens", s.createToken)
	a("DELETE /api/tokens/{id}", s.deleteToken)
	a("GET /api/audit", s.listAudit)

	a("GET /api/update", s.getUpdate)
	a("POST /api/update", s.startUpdate)
	a("POST /api/update/check", s.checkUpdate)
	a("PUT /api/update/settings", s.putUpdateSettings)

	a("GET /api/users", s.listUsers)
	a("POST /api/users", s.createUser)
	a("PUT /api/users/{id}", s.updateUser)
	a("DELETE /api/users/{id}", s.deleteUser)
	a("POST /api/users/{id}/reset", s.resetUser)

	a("GET /api/metrics/server", s.serverMetrics)
	a("GET /api/apps/{id}/metrics", s.appMetrics)
	a("GET /api/projects/{id}/metrics", s.projectMetrics)
	a("GET /api/monitoring/settings", s.getMonitoringSettings)
	a("PUT /api/monitoring/settings", s.putMonitoringSettings)

	a("GET /api/notifications", s.listChannels)
	a("POST /api/notifications", s.saveChannel)
	a("PUT /api/notifications/{id}", s.saveChannel)
	a("DELETE /api/notifications/{id}", s.deleteChannel)
	a("POST /api/notifications/test", s.testChannel)
	a("POST /api/notifications/{id}/test", s.testChannel)

	a("GET /api/backups/destinations", s.listDestinations)
	a("POST /api/backups/destinations", s.saveDestination)
	a("POST /api/backups/destinations/test", s.testDestination)
	a("PUT /api/backups/destinations/{id}", s.saveDestination)
	a("DELETE /api/backups/destinations/{id}", s.deleteDestination)
	a("POST /api/backups/destinations/{id}/key", s.revealPassphrase)
	a("GET /api/backups/panel", s.panelBackups)
	a("POST /api/backups/panel", s.runBackup)
	a("POST /api/backups/panel/jobs", s.createJob)
	a("GET /api/backups/panel/remote", s.remoteBackups)
	a("GET /api/backups/{id}", s.getBackup)
	a("DELETE /api/backups/{id}", s.deleteBackup)
	a("GET /api/backups/{id}/download", s.downloadBackup)
	a("PUT /api/backup-jobs/{id}", s.updateJob)
	a("DELETE /api/backup-jobs/{id}", s.deleteJob)
	a("GET /api/apps/{id}/backups", s.appBackups)
	a("POST /api/apps/{id}/backups", s.runBackup)
	a("GET /api/apps/{id}/backups/remote", s.remoteBackups)
	a("GET /api/apps/{app}/backups/file", s.downloadBackup)
	a("POST /api/apps/{id}/backup-jobs", s.createJob)
	a("POST /api/apps/{id}/restore", s.restoreBackup)
	a("GET /api/restores/{id}", s.getRestore)

	a("GET /api/projects", s.listProjects)
	a("POST /api/projects", s.createProject)
	a("DELETE /api/projects/{id}", s.deleteProject)
	a("GET /api/projects/{id}/apps", s.listApps)
	a("POST /api/projects/{id}/apps", s.createApp)
	a("POST /api/projects/{id}/databases", s.createDatabase)
	a("GET /api/engines", s.listEngines)
	a("GET /api/projects/{id}/export", s.exportProjectHandler)
	a("GET /api/apps/{id}/export", s.exportAppHandler)
	a("POST /api/import", s.importHandler)

	a("GET /api/apps/{id}", s.getApp)
	a("PUT /api/apps/{id}", s.updateApp)
	a("DELETE /api/apps/{id}", s.deleteApp)
	a("POST /api/apps/{id}/deploy", s.deployApp)
	a("POST /api/apps/{id}/restart", s.restartApp)
	a("POST /api/apps/{id}/stop", s.stopApp)
	a("POST /api/apps/{id}/start", s.startApp)
	a("GET /api/apps/{id}/logs", s.appLogs)
	a("GET /api/apps/{id}/runs", s.appRuns)
	a("GET /api/apps/{id}/deployments", s.listDeployments)
	a("POST /api/apps/{id}/domains", s.createDomain)
	a("POST /api/apps/{id}/webhook/rotate", s.rotateWebhook)
	a("PUT /api/apps/{id}/ai-access", s.setAIAccess)
	a("DELETE /api/domains/{id}", s.deleteDomain)
	a("GET /api/services/{project}/{name}", s.getAppByName)
	a("PATCH /api/apps/{id}/env", s.patchEnv)

	// 0.3: Git providers, previews, deploy keys, cron jobs.
	a("GET /api/git/providers", s.listProviders)
	a("GET /api/git/providers/{id}", s.getProvider)
	a("PUT /api/git/providers/{id}", s.updateProvider)
	a("DELETE /api/git/providers/{id}", s.deleteProvider)
	a("GET /api/git/providers/{id}/repos", s.providerRepos)
	a("GET /api/git/providers/{id}/branches", s.providerBranches)
	a("POST /api/git/github/manifest", s.githubManifest)
	a("POST /api/git/gitlab", s.connectGitLab)
	a("GET /api/apps/{id}/previews", s.listPreviews)
	a("POST /api/apps/{id}/deploy-key", s.createDeployKey)
	a("DELETE /api/apps/{id}/deploy-key", s.deleteDeployKey)
	a("GET /api/apps/{id}/cron", s.listCron)
	a("POST /api/apps/{id}/cron", s.createCron)
	a("PUT /api/cron/{id}", s.updateCron)
	a("DELETE /api/cron/{id}", s.deleteCron)
	a("POST /api/cron/{id}/run", s.runCron)
	a("GET /api/cron/{id}/runs", s.listCronRuns)
	a("GET /api/cron-runs/{id}", s.getCronRun)
	a("GET /api/deployments/{id}", s.getDeployment)

	// Private container registry logins.
	a("GET /api/registries", s.listRegistries)
	a("POST /api/registries", s.saveRegistry)
	a("POST /api/registries/test", s.testRegistry)
	a("PUT /api/registries/{id}", s.saveRegistry)
	a("DELETE /api/registries/{id}", s.deleteRegistry)

	// Database Query tab (read-only).
	a("GET /api/apps/{id}/query", s.queryInfo)
	a("POST /api/apps/{id}/query", s.runQuery)
	a("GET /api/apps/{id}/query/tables", s.queryTables)
	a("GET /api/apps/{id}/query/columns", s.queryColumns)
	a("GET /api/apps/{id}/query/history", s.queryHistory)
	a("DELETE /api/apps/{id}/query/history", s.clearQueryHistory)
	a("GET /api/apps/{id}/query/saved", s.listSavedQueries)
	a("POST /api/apps/{id}/query/saved", s.saveQuery)
	a("DELETE /api/saved-queries/{id}", s.deleteSavedQuery)

	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) { writeErr(w, 404, "not found") })
	mux.HandleFunc("/", s.static)

	return s.secure(mux)
}

// secure adds security headers, request size limits and CSRF protection.
func (s *Server) secure(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self'; font-src 'self' data:; frame-ancestors 'none'; base-uri 'none'; form-action 'self' https://github.com; object-src 'none'")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "same-origin")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		if strings.HasPrefix(r.URL.Path, "/api/") {
			h.Set("Cache-Control", "no-store")
			limit := int64(maxBody)
			if strings.HasPrefix(r.URL.Path, "/api/hooks/") {
				limit = maxHookBody
			}
			r.Body = http.MaxBytesReader(w, r.Body, limit)
			// CSRF: browsers can't send custom headers cross-site without a
			// CORS preflight, which we never approve. Webhooks are exempt
			// because they authenticate with an unguessable URL token or a
			// signature, and API token requests because browsers never
			// attach an Authorization header on their own.
			if r.Method != http.MethodGet && r.Method != http.MethodHead && !strings.HasPrefix(r.URL.Path, "/api/hooks/") && !hasBearer(r) {
				if r.Header.Get(csrfHeader) != "1" {
					writeErr(w, http.StatusForbidden, "missing "+csrfHeader+" header")
					return
				}
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) static(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		writeErr(w, 405, "method not allowed")
		return
	}
	p := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
	if p == "" {
		p = "index.html"
	}
	if f, err := fs.Stat(s.ui, p); err != nil || f.IsDir() {
		p = "index.html" // SPA fallback
	}
	if p == "index.html" {
		w.Header().Set("Cache-Control", "no-cache")
	} else if strings.HasPrefix(p, "assets/") {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	}
	b, err := fs.ReadFile(s.ui, p)
	if err != nil {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(503)
		io.WriteString(w, "SimplePanel UI is not built. Run `make ui` or use the Docker image.\n")
		return
	}
	http.ServeContent(w, r, p, time.Time{}, strings.NewReader(string(b)))
}

// ---- helpers ----

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

func readJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		writeErr(w, 400, "invalid JSON body")
		return false
	}
	return true
}

func pathID(r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	return id, err == nil && id > 0
}

// storeErr maps store errors to HTTP responses.
func (s *Server) storeErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeErr(w, 404, "not found")
	case errors.Is(err, store.ErrConflict):
		writeErr(w, 409, "already exists")
	default:
		s.log.Error("request failed", "err", err)
		writeErr(w, 500, "internal error")
	}
}

// clientIP returns the caller's IP. X-Forwarded-For is only trusted when the
// direct peer is on a private network (i.e. Traefik on the overlay).
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(host)
	if ip != nil && (ip.IsPrivate() || ip.IsLoopback()) {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			parts := strings.Split(xff, ",")
			if last := strings.TrimSpace(parts[len(parts)-1]); net.ParseIP(last) != nil {
				return last
			}
		}
	}
	return host
}

func isHTTPS(r *http.Request) bool {
	return r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"
}

// limiter is a fixed-window rate limiter keyed by string.
type limiter struct {
	mu     sync.Mutex
	max    int
	window time.Duration
	hits   map[string]*window
}

type window struct {
	start time.Time
	n     int
}

func newLimiter(max int, d time.Duration) *limiter {
	return &limiter{max: max, window: d, hits: map[string]*window{}}
}

// Blocked reports whether key is over the limit, without counting a hit.
func (l *limiter) Blocked(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	w, ok := l.hits[key]
	return ok && time.Since(w.start) <= l.window && w.n >= l.max
}

func (l *limiter) Allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	if len(l.hits) > 10000 { // bound memory
		for k, w := range l.hits {
			if now.Sub(w.start) > l.window {
				delete(l.hits, k)
			}
		}
	}
	w, ok := l.hits[key]
	if !ok || now.Sub(w.start) > l.window {
		l.hits[key] = &window{start: now, n: 1}
		return true
	}
	w.n++
	return w.n <= l.max
}

// StartJanitor prunes expired sessions and invite links periodically.
func (s *Server) StartJanitor(ctx context.Context) {
	go func() {
		t := time.NewTicker(time.Hour)
		defer t.Stop()
		for {
			_ = s.store.PruneSessions(ctx)
			_ = s.store.PruneInvites(ctx)
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}()
}
