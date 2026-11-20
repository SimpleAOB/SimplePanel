package deploy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStaticDockerfile(t *testing.T) {
	plain, err := StaticDockerfile(StaticOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(plain, "FROM alpine") || !strings.Contains(plain, "FROM "+StaticServerImage) || strings.Contains(plain, "npm") {
		t.Errorf("plain site Dockerfile unexpected:\n%s", plain)
	}
	// Every instruction must be on one line (no raw newlines inside RUN).
	for _, line := range strings.Split(plain, "\n") {
		if line != "" && !strings.HasPrefix(line, "#") && !strings.HasPrefix(line, "FROM") &&
			!strings.HasPrefix(line, "RUN") && !strings.HasPrefix(line, "COPY") && !strings.HasPrefix(line, "WORKDIR") &&
			!strings.HasPrefix(line, "EXPOSE") {
			t.Errorf("unexpected line %q", line)
		}
	}

	built, err := StaticDockerfile(StaticOptions{Command: `npm run build && echo "it's done"`, OutputDir: "dist", SPA: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"FROM " + DefaultStaticBuildImage + " AS build", "npm run build", "it's done", "/src/dist/", "/index.html;"} {
		if !strings.Contains(built, want) {
			t.Errorf("missing %q in:\n%s", want, built)
		}
	}
	if _, err := StaticDockerfile(StaticOptions{OutputDir: "../etc"}); err == nil {
		t.Error("output dir escaping the repo must be rejected")
	}
	if _, err := StaticDockerfile(StaticOptions{Command: "x", BuildImage: "bad image"}); err == nil {
		t.Error("invalid build image must be rejected")
	}
}

func TestResolveBuildType(t *testing.T) {
	dir := t.TempDir()
	if got := ResolveBuildType("auto", dir, ""); got != "static" {
		t.Errorf("no Dockerfile: got %s", got)
	}
	os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte("FROM scratch"), 0o644)
	if got := ResolveBuildType("auto", dir, ""); got != "dockerfile" {
		t.Errorf("with Dockerfile: got %s", got)
	}
	if got := ResolveBuildType("static", dir, ""); got != "static" {
		t.Errorf("explicit static: got %s", got)
	}
}

func TestInsideRepo(t *testing.T) {
	repo := t.TempDir()
	outside := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, "web", "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(repo, "escape")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("web", filepath.Join(repo, "alias")); err != nil {
		t.Fatal(err)
	}
	if _, err := insideRepo(repo, filepath.Join(repo, "web", "src")); err != nil {
		t.Errorf("plain folder refused: %v", err)
	}
	if _, err := insideRepo(repo, filepath.Join(repo, "alias")); err != nil {
		t.Errorf("symlink inside the repo refused: %v", err)
	}
	if _, err := insideRepo(repo, filepath.Join(repo, "escape")); err == nil {
		t.Error("symlink to outside the repo accepted")
	}
}
