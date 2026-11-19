package docker

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestRegistryHost(t *testing.T) {
	cases := map[string]string{
		"nginx":                                 "docker.io",
		"nginx:1.27":                            "docker.io",
		"acme/app:v1":                           "docker.io",
		"docker.io/acme/app":                    "docker.io",
		"index.docker.io/acme/app":              "docker.io",
		"ghcr.io/acme/app:main":                 "ghcr.io",
		"GHCR.io/acme/app":                      "ghcr.io",
		"registry.gitlab.com/g/p/img@sha256:ab": "registry.gitlab.com",
		"localhost/app":                         "localhost",
		"localhost:5000/app:v2":                 "localhost:5000",
		"registry.example.com:5000/team/app":    "registry.example.com:5000",
	}
	for in, want := range cases {
		if got := RegistryHost(in); got != want {
			t.Errorf("RegistryHost(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNormalizeRegistry(t *testing.T) {
	ok := map[string]string{
		"ghcr.io":                         "ghcr.io",
		" GHCR.IO ":                       "ghcr.io",
		"https://ghcr.io/":                "ghcr.io",
		"https://index.docker.io/v1/":     "docker.io",
		"registry-1.docker.io":            "docker.io",
		"registry.example.com:5000/v2/":   "registry.example.com:5000",
		"http://localhost:5000":           "localhost:5000",
		"registry.gitlab.com/group/proj/": "registry.gitlab.com",
	}
	for in, want := range ok {
		got, err := NormalizeRegistry(in)
		if err != nil || got != want {
			t.Errorf("NormalizeRegistry(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "https://", "ghcr.io:0", "ghcr.io:99999", "-bad.io", "bad..io", "bad_host.io", "user@ghcr.io", "ghcr.io:abc"} {
		if got, err := NormalizeRegistry(bad); err == nil {
			t.Errorf("NormalizeRegistry(%q) = %q, want an error", bad, got)
		}
	}
}

func decodeAuth(t *testing.T, h string) map[string]string {
	t.Helper()
	b, err := base64.URLEncoding.DecodeString(h)
	if err != nil {
		t.Fatalf("header %q is not base64url: %v", h, err)
	}
	m := map[string]string{}
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("header %q: %v", b, err)
	}
	return m
}

// TestRegistryHeaders checks what the client sends Docker: the login on
// pulls and service changes, an empty login to clear one, none at all to
// keep one, and per-registry logins on builds.
func TestRegistryHeaders(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "d.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	got := map[string][]string{}
	srv := &httptest.Server{Listener: ln, Config: &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		mu.Lock()
		p := r.URL.Path[strings.Index(r.URL.Path[1:], "/")+1:]
		got[p] = []string{r.Header.Get("X-Registry-Auth"), r.Header.Get("X-Registry-Config")}
		mu.Unlock()
		w.Write([]byte(`{"ID":"svc1"}`))
	})}}
	srv.Start()
	defer srv.Close()

	c := New(sock)
	ctx := context.Background()
	gh := &RegistryAuth{Server: "ghcr.io", Username: "octo", Password: "ghp_secret"}
	hub := RegistryAuth{Server: DockerHub, Username: "me", Password: "dckr_pat"}

	if err := c.ImagePull(ctx, "ghcr.io/acme/app:1", gh, nil); err != nil {
		t.Fatal(err)
	}
	if a := decodeAuth(t, got["/images/create"][0]); a["username"] != "octo" || a["password"] != "ghp_secret" || a["serveraddress"] != "ghcr.io" {
		t.Errorf("pull auth = %v", a)
	}
	if err := c.ImagePull(ctx, "nginx", nil, nil); err != nil {
		t.Fatal(err)
	}
	if h := got["/images/create"][0]; h != "" {
		t.Errorf("public pull sent a login: %q", h)
	}

	if _, err := c.ServiceCreate(ctx, ServiceSpec{Name: "x"}, gh); err != nil {
		t.Fatal(err)
	}
	if a := decodeAuth(t, got["/services/create"][0]); a["password"] != "ghp_secret" {
		t.Errorf("create auth = %v", a)
	}
	if err := c.ServiceUpdate(ctx, "svc1", 1, ServiceSpec{Name: "x"}, nil, false); err != nil {
		t.Fatal(err)
	}
	if a := decodeAuth(t, got["/services/svc1/update"][0]); len(a) != 0 {
		t.Errorf("update without a login should clear it with {}, sent %v", a)
	}
	if err := c.ServiceUpdate(ctx, "svc1", 2, ServiceSpec{Name: "x"}, nil, true); err != nil {
		t.Fatal(err)
	}
	if h := got["/services/svc1/update"][0]; h != "" {
		t.Errorf("restart should keep the service's login (no header), sent %q", h)
	}

	if err := c.ImageBuild(ctx, strings.NewReader(""), BuildOptions{Tag: "t", Registries: []RegistryAuth{*gh, hub}}, nil); err != nil {
		t.Fatal(err)
	}
	b, _ := base64.URLEncoding.DecodeString(got["/build"][1])
	var cfg map[string]map[string]string
	if err := json.Unmarshal(b, &cfg); err != nil {
		t.Fatalf("build config %q: %v", b, err)
	}
	if cfg["ghcr.io"]["password"] != "ghp_secret" || cfg["https://index.docker.io/v1/"]["username"] != "me" || len(cfg) != 2 {
		t.Errorf("build registry config = %v", cfg)
	}
}
