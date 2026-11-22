package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func open(t *testing.T) *Store {
	s, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestFirstUserOnlyOnce(t *testing.T) {
	s, ctx := open(t), context.Background()
	if _, err := s.CreateFirstUser(ctx, "admin", "h"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateFirstUser(ctx, "other", "h"); !errors.Is(err, ErrConflict) {
		t.Fatalf("second setup should conflict, got %v", err)
	}
}

func TestSessions(t *testing.T) {
	s, ctx := open(t), context.Background()
	u, _ := s.CreateFirstUser(ctx, "admin", "h")
	tok, err := s.CreateSession(ctx, u.ID, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := s.SessionUser(ctx, tok); err != nil || got.Username != "admin" {
		t.Fatalf("session lookup: %v %v", got, err)
	}
	if _, err := s.SessionUser(ctx, tok+"x"); !errors.Is(err, ErrNotFound) {
		t.Fatal("wrong token should not match")
	}
	expired, _ := s.CreateSession(ctx, u.ID, -time.Second)
	if _, err := s.SessionUser(ctx, expired); !errors.Is(err, ErrNotFound) {
		t.Fatal("expired session should not match")
	}
	s.DeleteSession(ctx, tok)
	if _, err := s.SessionUser(ctx, tok); !errors.Is(err, ErrNotFound) {
		t.Fatal("deleted session should not match")
	}
}

func TestAppsDomainsCascade(t *testing.T) {
	s, ctx := open(t), context.Background()
	p, _ := s.CreateProject(ctx, "proj")
	a := &App{ProjectID: p.ID, Name: "web", Source: "image", Image: "nginx", Replicas: 1,
		Volumes: []Volume{{Name: "data", Path: "/data"}}}
	if err := s.CreateApp(ctx, a); err != nil {
		t.Fatal(err)
	}
	got, err := s.App(ctx, a.ID)
	if err != nil || got.ServiceName() != "proj_web" || len(got.Volumes) != 1 || len(got.WebhookToken) != 48 {
		t.Fatalf("app roundtrip: %+v %v", got, err)
	}
	if err := s.CreateDomain(ctx, &Domain{AppID: a.ID, Host: "x.example.com", Port: 80, HTTPS: true}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateDomain(ctx, &Domain{AppID: a.ID, Host: "x.example.com", Port: 80}); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate domain should conflict, got %v", err)
	}
	// The same host with a path is a separate domain; the same path twice isn't.
	if err := s.CreateDomain(ctx, &Domain{AppID: a.ID, Host: "x.example.com", Path: "/api", StripPath: true, Port: 3000, HTTPS: true}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateDomain(ctx, &Domain{AppID: a.ID, Host: "x.example.com", Path: "/api", Port: 80}); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate host+path should conflict, got %v", err)
	}
	ds, _ := s.Domains(ctx, a.ID)
	if len(ds) != 2 || ds[1].Path != "/api" || !ds[1].StripPath || ds[1].Label() != "x.example.com/api" {
		t.Fatalf("domains: %+v", ds)
	}
	if err := s.DeleteDomain(ctx, ds[1].ID); err != nil {
		t.Fatal(err)
	}
	routes, _ := s.AllRoutes(ctx)
	if len(routes) != 1 || routes[0].Service != "proj_web" {
		t.Fatalf("routes: %+v", routes)
	}
	s.DeleteProject(ctx, p.ID)
	if routes, _ := s.AllRoutes(ctx); len(routes) != 0 {
		t.Fatal("domains should cascade on project delete")
	}
}

func TestPruneDeployments(t *testing.T) {
	s, ctx := open(t), context.Background()
	p, err := s.CreateProject(ctx, "p")
	if err != nil {
		t.Fatal(err)
	}
	a := &App{ProjectID: p.ID, Name: "web", Kind: "app"}
	if err := s.CreateApp(ctx, a); err != nil {
		t.Fatal(err)
	}
	var last *Deployment
	for i := 0; i < KeepDeployments+10; i++ {
		d, err := s.CreateDeployment(ctx, a.ID, "test")
		if err != nil {
			t.Fatal(err)
		}
		if i < 3 { // old but unfinished: kept
			continue
		}
		_ = s.UpdateDeployment(ctx, d.ID, "success", "", "log", true)
		last = d
	}
	if err := s.PruneDeployments(ctx); err != nil {
		t.Fatal(err)
	}
	ds, err := s.Deployments(ctx, a.ID, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if len(ds) != KeepDeployments+3 || ds[0].ID != last.ID {
		t.Fatalf("kept %d deployments (newest %d), want %d", len(ds), ds[0].ID, KeepDeployments+3)
	}
}
