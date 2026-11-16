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
