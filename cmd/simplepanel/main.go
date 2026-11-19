// Command simplepanel runs the SimplePanel server.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/SimpleAOB/SimplePanel/internal/api"
	"github.com/SimpleAOB/SimplePanel/internal/backup"
	"github.com/SimpleAOB/SimplePanel/internal/cron"
	"github.com/SimpleAOB/SimplePanel/internal/deploy"
	"github.com/SimpleAOB/SimplePanel/internal/docker"
	"github.com/SimpleAOB/SimplePanel/internal/gitprovider"
	"github.com/SimpleAOB/SimplePanel/internal/monitor"
	"github.com/SimpleAOB/SimplePanel/internal/notify"
	"github.com/SimpleAOB/SimplePanel/internal/store"
	"github.com/SimpleAOB/SimplePanel/internal/update"
	"github.com/SimpleAOB/SimplePanel/internal/web"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func main() {
	log := slog.New(slog.NewTextHandler(os.Stdout, nil))
	if err := run(log); err != nil {
		log.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	listen := env("SP_LISTEN", ":3000")
	dataDir := env("SP_DATA_DIR", "/data")
	sock := env("SP_DOCKER_SOCKET", "/var/run/docker.sock")

	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return err
	}
	workDir := filepath.Join(dataDir, "builds")
	_ = os.RemoveAll(workDir) // leftovers from an interrupted build
	if err := os.MkdirAll(workDir, 0o700); err != nil {
		return err
	}

	st, err := store.Open(filepath.Join(dataDir, "simplepanel.db"))
	if err != nil {
		return err
	}
	defer st.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	dc := docker.New(sock)
	// Don't let a hung Docker stop the panel from starting: it serves the
	// UI and says "Docker is not responding" until Docker answers.
	nctx, ncancel := context.WithTimeout(ctx, 10*time.Second)
	defer ncancel()
	if err := dc.Negotiate(nctx); err != nil {
		log.Warn("docker not reachable yet; deploys will fail until it is", "err", err)
	} else if info, err := dc.Info(nctx); err == nil {
		log.Info("docker connected", "version", info.ServerVersion, "swarm", info.Swarm.LocalNodeState)
		if info.Swarm.LocalNodeState != "active" {
			log.Warn("Docker Swarm is not active; run `docker swarm init` (the installer does this for you)")
		}
	}

	if err := st.FailStaleDeployments(ctx); err != nil {
		return err
	}
	if err := st.FailStaleBackups(ctx); err != nil {
		return err
	}
	if err := st.FailStaleCronRuns(ctx); err != nil {
		return err
	}
	tmpDir := filepath.Join(dataDir, "tmp")
	_ = os.RemoveAll(tmpDir) // leftovers from an interrupted backup

	dm := deploy.NewManager(deploy.Config{
		Network:      env("SP_NETWORK", "simplepanel"),
		ProxyDir:     env("SP_TRAEFIK_DIR", "/etc/traefik/dynamic"),
		WorkDir:      workDir,
		PanelSvc:     env("SP_PANEL_SERVICE", "simplepanel"),
		PanelPort:    3000,
		EndpointMode: env("SP_ENDPOINT_MODE", "vip"),
		HelperImage:  env("SP_HELPER_IMAGE", deploy.DefaultHelperImage),
	}, st, dc, log)

	notifier := notify.New(st, log)
	notifier.BaseURL = func() string {
		if pd, _ := st.Setting(context.Background(), "panel_domain"); pd != "" {
			return "https://" + pd
		}
		if v := env("SP_PUBLIC_URL", ""); v != "" {
			return v
		}
		v, _ := st.Setting(context.Background(), "public_url")
		return v
	}
	gits := gitprovider.New()
	crons := cron.New(st, dm, dc, notifier, log)
	srv := api.New(st, dc, dm, web.FS(), log, version)
	srv.UseV03(gits, crons)
	dm.GitAuth = func(ctx context.Context, app *store.App) (string, error) {
		p, err := st.GitProvider(ctx, app.GitProviderID)
		if err != nil {
			return "", nil // disconnected: fall back to the app's own token
		}
		return gits.CloneToken(ctx, p, app.GitRepo)
	}
	dm.OnCommit = srv.GitOnCommit
	dm.OnDeploy = func(app *store.App, d *store.Deployment) {
		srv.GitOnDeploy(app, d)
		if ev, ok := api.PreviewEvent(app, d, srv.PreviewURL(app)); ok {
			notifier.Send(ev)
			return
		}
		ev := notify.Event{ProjectID: app.ProjectID, Project: app.ProjectName, Service: app.Name,
			Link: fmt.Sprintf("/#/p/%d/a/%d/deployments", app.ProjectID, app.ID)}
		if d.Status == "failed" {
			ev.Type, ev.Level = notify.DeployFailed, "error"
			ev.Title = fmt.Sprintf("Deploy of %s/%s failed", app.ProjectName, app.Name)
			ev.Message = fmt.Sprintf("Deployment #%d (%s): %s", d.ID, d.Trigger, d.Log)
		} else {
			ev.Type, ev.Level = notify.DeploySucceeded, "success"
			ev.Title = fmt.Sprintf("%s/%s deployed", app.ProjectName, app.Name)
			ev.Message = fmt.Sprintf("Deployment #%d (%s) is live.", d.ID, d.Trigger)
		}
		notifier.Send(ev)
	}
	watchDocker(dc, notifier)
	backups := backup.New(st, dm, notifier, log, tmpDir)
	backups.Start(ctx)
	mon := monitor.New(monitor.Config{ProcDir: os.Getenv("SP_PROC_DIR"), DiskPath: dataDir}, st, dc, dm, notifier, log)
	mon.Start(ctx)
	dm.Capacity = func() deploy.Capacity {
		mi, err := mon.Memory()
		if err != nil || mi.Total == 0 {
			return deploy.Capacity{}
		}
		return deploy.NewCapacity(int(mi.Total>>20), deploy.LoadResourceSettings(context.Background(), st))
	}
	if err := dm.SyncProxy(ctx); err != nil {
		log.Warn("could not write proxy config", "err", err)
	}

	crons.Start(ctx)
	dm.StartRunArchiver(ctx)
	srv.Use(backups, mon, notifier)
	srv.StartJanitor(ctx)

	updater := update.New(update.Config{
		Repo:    env("SP_UPDATE_REPO", update.DefaultRepo),
		Image:   env("SP_UPDATE_IMAGE", update.DefaultImage),
		Service: env("SP_PANEL_SERVICE", "simplepanel"),
		Version: version,
		DataDir: dataDir,
		APIBase: env("SP_UPDATE_API", ""), // GitHub API; for testing
	}, st, dc, notifier, log)
	updater.Run(ctx)
	srv.UseUpdater(updater)

	hs := &http.Server{
		Addr:              listen,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       60 * time.Second,
		WriteTimeout:      120 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    64 << 10,
	}
	errc := make(chan error, 1)
	go func() {
		log.Info("SimplePanel listening", "addr", listen, "version", version)
		errc <- hs.ListenAndServe()
	}()
	select {
	case err := <-errc:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case <-ctx.Done():
		log.Info("shutting down")
		sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = hs.Shutdown(sctx)
	}
	return nil
}

// watchDocker sends a notification when Docker has stopped answering for a
// minute, and another once it has answered again for a minute. Short
// hiccups stay quiet, and a daemon that keeps dropping out and coming back
// still counts as down.
func watchDocker(dc *docker.Client, n *notify.Notifier) {
	var mu sync.Mutex
	var down, stable *time.Timer // down: first failure +1 min; stable: recovery +1 min
	sent := false
	var fire func()
	fire = func() {
		mu.Lock()
		defer mu.Unlock()
		down = nil
		if sent {
			return
		}
		if dc.Health().OK {
			if stable != nil { // back, but not for long yet: look again shortly
				down = time.AfterFunc(30*time.Second, fire)
			}
			return
		}
		sent = true
		n.Send(notify.Event{Type: notify.DockerDown, Level: "error", Title: "Docker is not responding",
			Message: "SimplePanel can't reach Docker on this server (" + dc.Health().Error + "). Apps may still be running. " +
				"This often means the server ran out of memory; SimplePanel keeps retrying and tells you when Docker answers again.",
			Link: "/#/settings/monitoring"})
	}
	dc.OnHealthChange(func(h docker.Health) {
		mu.Lock()
		defer mu.Unlock()
		if !h.OK {
			if stable != nil {
				stable.Stop()
				stable = nil
			}
			if down == nil && !sent {
				down = time.AfterFunc(time.Minute, fire)
			}
			return
		}
		if (down == nil && !sent) || stable != nil {
			return
		}
		stable = time.AfterFunc(time.Minute, func() {
			mu.Lock()
			defer mu.Unlock()
			stable = nil
			if !dc.Health().OK {
				return
			}
			if down != nil {
				down.Stop()
				down = nil
			}
			if sent {
				sent = false
				n.Send(notify.Event{Type: notify.DockerRecovered, Level: "success", Title: "Docker is responding again",
					Message: "SimplePanel is connected to Docker again.", Link: "/#/"})
			}
		})
	})
}
