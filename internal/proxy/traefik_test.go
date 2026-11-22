package proxy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestRender(t *testing.T) {
	b, err := Render([]Route{
		{Key: "d1", Host: "app.example.com", Service: "proj_app", Port: 8080, HTTPS: true},
		{Key: "d2", Host: "plain.example.com", Service: "proj_web", Port: 80},
	})
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		HTTP struct {
			Routers  map[string]map[string]any `yaml:"routers"`
			Services map[string]struct {
				LoadBalancer struct {
					Servers []struct{ URL string } `yaml:"servers"`
				} `yaml:"loadBalancer"`
			} `yaml:"services"`
		} `yaml:"http"`
	}
	if err := yaml.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	if got := doc.HTTP.Services["sp-d1"].LoadBalancer.Servers[0].URL; got != "http://proj_app:8080" {
		t.Errorf("service url = %q", got)
	}
	r := doc.HTTP.Routers["sp-d1-https"]
	if r["rule"] != "Host(`app.example.com`)" {
		t.Errorf("rule = %v", r["rule"])
	}
	if r["tls"] == nil {
		t.Error("https router missing tls")
	}
	if _, ok := doc.HTTP.Routers["sp-d2-https"]; ok {
		t.Error("http-only domain should not get an https router")
	}
	if mws, _ := doc.HTTP.Routers["sp-d1-http"]["middlewares"].([]any); len(mws) != 1 || mws[0] != redirectMW {
		t.Errorf("http router for https domain should redirect, got %v", mws)
	}
}

func TestRenderEmptyAndWrite(t *testing.T) {
	dir := t.TempDir()
	if err := Write(dir, nil); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, managedFile))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "routers") {
		t.Error("empty config should not contain routers")
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Errorf("expected only the managed file, got %d entries", len(entries))
	}
}

func TestRenderPathRoutes(t *testing.T) {
	b, err := Render([]Route{
		{Key: "d1", Host: "app.example.com", Service: "proj_web", Port: 80, HTTPS: true},
		{Key: "d2", Host: "app.example.com", Path: "/api", Service: "proj_api", Port: 3000, HTTPS: true},
		{Key: "d3", Host: "app.example.com", Path: "/api/v2", StripPath: true, Service: "proj_api2", Port: 3000, HTTPS: true},
		{Key: "d4", Host: "plain.example.com", Path: "/x", StripPath: true, Service: "proj_x", Port: 80},
		{Key: "check", Path: "/.well-known/simplepanel-check/", Service: "panel", Port: 3000},
	})
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		HTTP struct {
			Middlewares map[string]map[string]any `yaml:"middlewares"`
			Routers     map[string]struct {
				Rule        string   `yaml:"rule"`
				Middlewares []string `yaml:"middlewares"`
				Priority    int      `yaml:"priority"`
				TLS         any      `yaml:"tls"`
			} `yaml:"routers"`
		} `yaml:"http"`
	}
	if err := yaml.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	rs := doc.HTTP.Routers
	if got := rs["sp-d2-https"].Rule; got != "Host(`app.example.com`) && (Path(`/api`) || PathPrefix(`/api/`))" {
		t.Errorf("path rule = %q", got)
	}
	if rs["sp-d2-https"].TLS == nil {
		t.Error("path route should keep HTTPS with the host's certificate")
	}
	root, api, v2, chk := rs["sp-d1-https"].Priority, rs["sp-d2-https"].Priority, rs["sp-d3-https"].Priority, rs["sp-check-http"].Priority
	if !(root < api && api < v2 && v2 < chk) {
		t.Errorf("priorities root %d < /api %d < /api/v2 %d < check %d", root, api, v2, chk)
	}
	if rs["sp-d2-http"].Priority != api {
		t.Error("the HTTP redirect router needs the same priority as its HTTPS router")
	}
	if mws := rs["sp-d2-https"].Middlewares; len(mws) != 1 {
		t.Errorf("/api keeps its path by default, middlewares %v", mws)
	}
	if mws := rs["sp-d3-https"].Middlewares; len(mws) != 2 || mws[1] != "sp-d3-strip" {
		t.Errorf("strip middleware missing: %v", mws)
	}
	if mws := rs["sp-d4-http"].Middlewares; len(mws) != 1 || mws[0] != "sp-d4-strip" {
		t.Errorf("HTTP-only strip middleware missing: %v", mws)
	}
	sp, _ := doc.HTTP.Middlewares["sp-d3-strip"]["stripPrefix"].(map[string]any)
	if ps, _ := sp["prefixes"].([]any); len(ps) != 1 || ps[0] != "/api/v2" {
		t.Errorf("strip prefixes = %v", sp)
	}
	if _, ok := doc.HTTP.Middlewares["sp-d2-strip"]; ok {
		t.Error("no strip middleware when stripping is off")
	}
}
