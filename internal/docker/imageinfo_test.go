package docker

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
)

func digestOf(b []byte) string { return fmt.Sprintf("sha256:%x", sha256.Sum256(b)) }

// fakeRegistry serves one multi-arch image at /v2/team/app behind a token
// service that wants user "bob" / "s3cret".
func fakeRegistry(t *testing.T, requireLogin bool) (*httptest.Server, *int32) {
	cfg, _ := json.Marshal(map[string]any{"config": map[string]any{
		"Env":          []string{"PATH=/usr/bin", "DATABASE_URL=", "LOG_LEVEL=info", "bad key=x"},
		"ExposedPorts": map[string]any{"8080/tcp": map[string]any{}, "53/udp": map[string]any{}},
	}})
	man, _ := json.Marshal(map[string]any{"schemaVersion": 2, "mediaType": "application/vnd.oci.image.manifest.v1+json", "config": map[string]any{"digest": digestOf(cfg)}})
	idx, _ := json.Marshal(map[string]any{"schemaVersion": 2, "mediaType": "application/vnd.oci.image.index.v1+json", "manifests": []any{
		map[string]any{"digest": digestOf([]byte("attestation")), "platform": map[string]any{"os": "unknown", "architecture": "unknown"}},
		map[string]any{"digest": digestOf(man), "platform": map[string]any{"os": "linux", "architecture": runtime.GOARCH}},
	}})
	var hits int32
	var srv *httptest.Server
	srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		if r.URL.Path == "/token" {
			u, p, ok := r.BasicAuth()
			if requireLogin && (!ok || u != "bob" || p != "s3cret") {
				w.WriteHeader(401)
				return
			}
			if r.URL.Query().Get("scope") != "repository:team/app:pull" {
				t.Errorf("scope = %q", r.URL.Query().Get("scope"))
			}
			json.NewEncoder(w).Encode(map[string]string{"token": "tok"})
			return
		}
		if r.Header.Get("Authorization") != "Bearer tok" {
			w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Bearer realm="%s/token",service="fake",scope="repository:team/app:pull"`, srv.URL))
			w.WriteHeader(401)
			return
		}
		switch r.URL.Path {
		case "/v2/team/app/manifests/1.0":
			w.Header().Set("Content-Type", "application/vnd.oci.image.index.v1+json")
			w.Write(idx)
		case "/v2/team/app/manifests/" + digestOf(man):
			w.Header().Set("Content-Type", "application/vnd.oci.image.manifest.v1+json")
			w.Write(man)
		case "/v2/team/app/blobs/" + digestOf(cfg):
			w.Write(cfg)
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

func TestRegistryImageInfo(t *testing.T) {
	srv, hits := fakeRegistry(t, true)
	host := strings.TrimPrefix(srv.URL, "https://")
	rc := newRegistryClient(srv.Client())
	ctx := context.Background()

	if _, err := rc.ImageInfo(ctx, host+"/team/app:1.0", nil); err != ErrRegistryDenied {
		t.Fatalf("anonymous: err = %v, want ErrRegistryDenied", err)
	}
	auth := &RegistryAuth{Server: host, Username: "bob", Password: "s3cret"}
	info, err := rc.ImageInfo(ctx, host+"/team/app:1.0", auth)
	if err != nil {
		t.Fatal(err)
	}
	want := []EnvVar{{"PATH", "/usr/bin"}, {"DATABASE_URL", ""}, {"LOG_LEVEL", "info"}}
	if fmt.Sprint(info.Env) != fmt.Sprint(want) {
		t.Errorf("env = %v, want %v", info.Env, want)
	}
	if fmt.Sprint(info.Ports) != "[8080]" {
		t.Errorf("ports = %v", info.Ports)
	}
	before := atomic.LoadInt32(hits)
	if _, err := rc.ImageInfo(ctx, host+"/team/app:1.0", auth); err != nil {
		t.Fatal(err)
	}
	if atomic.LoadInt32(hits) != before {
		t.Error("second lookup should come from the cache")
	}
	if _, err := rc.ImageInfo(ctx, host+"/team/app:2.0", auth); err != ErrImageNotFound {
		t.Errorf("missing tag: err = %v", err)
	}
	if _, err := rc.ImageInfo(ctx, host+"/team/app:1.0", &RegistryAuth{Server: "ghcr.io", Username: "bob", Password: "s3cret"}); err == nil {
		t.Error("a login for another registry must not be used")
	}
}

func TestParseImageRef(t *testing.T) {
	for in, want := range map[string]string{
		"nginx":                         "docker.io registry-1.docker.io library/nginx latest",
		"nginx:1.27-alpine":             "docker.io registry-1.docker.io library/nginx 1.27-alpine",
		"user/app:v2":                   "docker.io registry-1.docker.io user/app v2",
		"ghcr.io/Org/App:main":          "ghcr.io ghcr.io org/app main",
		"registry.example.com:5000/a/b": "registry.example.com:5000 registry.example.com:5000 a/b latest",
		"ghcr.io/o/a:t@sha256:" + strings.Repeat("a", 64): "ghcr.io ghcr.io o/a sha256:" + strings.Repeat("a", 64),
	} {
		r, err := parseImageRef(in)
		if err != nil {
			t.Errorf("%s: %v", in, err)
			continue
		}
		if got := strings.Join([]string{r.host, r.apiHost, r.repo, r.reference}, " "); got != want {
			t.Errorf("%s: got %q, want %q", in, got, want)
		}
	}
	for _, bad := range []string{"", "a b", "ghcr.io/", "ghcr.io/../x", "x?y"} {
		if _, err := parseImageRef(bad); err == nil {
			t.Errorf("%q should be refused", bad)
		}
	}
}

func TestParseChallenge(t *testing.T) {
	s, p := parseChallenge(`Bearer realm="https://auth.docker.io/token",service="registry.docker.io",scope="repository:library/nginx:pull,push"`)
	if s != "bearer" || p["realm"] != "https://auth.docker.io/token" || p["service"] != "registry.docker.io" || p["scope"] != "repository:library/nginx:pull,push" {
		t.Errorf("got %s %v", s, p)
	}
}

func TestRefuseInternal(t *testing.T) {
	for _, a := range []string{"127.0.0.1:443", "[::1]:443", "169.254.169.254:80", "0.0.0.0:443"} {
		if refuseInternal("tcp", a, nil) == nil {
			t.Errorf("%s should be refused", a)
		}
	}
	for _, a := range []string{"140.82.112.33:443", "10.0.0.5:5000"} {
		if err := refuseInternal("tcp", a, nil); err != nil {
			t.Errorf("%s: %v", a, err)
		}
	}
}
