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
	"syscall"
	"time"

	"github.com/SimpleAOB/SimplePanel/internal/api"
	"github.com/SimpleAOB/SimplePanel/internal/backup"
	"github.com/SimpleAOB/SimplePanel/internal/deploy"
	"github.com/SimpleAOB/SimplePanel/internal/docker"
	"github.com/SimpleAOB/SimplePanel/internal/monitor"
	"github.com/SimpleAOB/SimplePanel/internal/notify"
	"github.com/SimpleAOB/SimplePanel/internal/store"
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
	if err := dc.Negotiate(ctx); err != nil {
		log.Warn("docker not reachable yet; deploys will fail until it is", "err", err)
	} else if info, err := dc.Info(ctx); err == nil {
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
		return env("SP_PUBLIC_URL", "")
	}
	dm.OnDeploy = func(app *store.App, d *store.Deployment) {
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
	backups := backup.New(st, dm, notifier, log, tmpDir)
	backups.Start(ctx)
	mon := monitor.New(monitor.Config{ProcDir: os.Getenv("SP_PROC_DIR"), DiskPath: dataDir}, st, dc, dm, notifier, log)
	mon.Start(ctx)
	if err := dm.SyncProxy(ctx); err != nil {
		log.Warn("could not write proxy config", "err", err)
	}

	srv := api.New(st, dc, dm, web.FS(), log, version).Use(backups, mon, notifier)
	srv.StartJanitor(ctx)

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
