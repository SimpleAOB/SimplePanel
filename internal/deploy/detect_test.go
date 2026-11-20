package deploy

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFiles(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestDetectProject(t *testing.T) {
	cases := []struct {
		name  string
		files map[string]string
		kind  string
		tool  string
	}{
		{"plain html", map[string]string{"index.html": "<h1>hi</h1>"}, "", ""},
		{"vite", map[string]string{"index.html": "", "package.json": `{"scripts":{"build":"vite build"},"devDependencies":{"vite":"^5"}}`}, "static", "Vite"},
		{"cra", map[string]string{"public/index.html": "", "package.json": `{"scripts":{"start":"react-scripts start","build":"react-scripts build"},"dependencies":{"react-scripts":"5"}}`}, "static", "Create React App"},
		{"express", map[string]string{"package.json": `{"scripts":{"start":"node server.js"},"dependencies":{"express":"4"}}`}, "server", "Node.js"},
		{"node start", map[string]string{"package.json": `{"scripts":{"start":"node index.js"}}`}, "server", "Node.js"},
		{"next server", map[string]string{"package.json": `{"scripts":{"build":"next build"},"dependencies":{"next":"15"}}`}, "server", "Next.js"},
		{"next export", map[string]string{"next.config.js": "module.exports = { output: 'export' }", "package.json": `{"scripts":{"build":"next build"},"dependencies":{"next":"15"}}`}, "static", "Next.js (static export)"},
		{"python", map[string]string{"requirements.txt": "flask", "app.py": ""}, "server", "Python"},
		{"php", map[string]string{"index.php": "<?php"}, "server", "PHP"},
		{"python with index.html", map[string]string{"requirements.txt": "", "index.html": ""}, "", ""},
		{"hugo", map[string]string{"hugo.toml": "", "content/_index.md": ""}, "static", "Hugo"},
	}
	for _, c := range cases {
		d := DetectProject(writeFiles(t, c.files))
		if d.Kind != c.kind || d.Name != c.tool {
			t.Errorf("%s: got %q %q, want %q %q", c.name, d.Kind, d.Name, c.kind, c.tool)
		}
	}
}

func TestCleanHost(t *testing.T) {
	for in, want := range map[string]string{
		"https://WWW.Example.com/": "www.example.com",
		"http://a.b.c:8080/path?q": "a.b.c",
		" app.example.com. ":       "app.example.com",
		"user@shop.example.com":    "shop.example.com",
		"app.example.com":          "app.example.com",
	} {
		if got := CleanHost(in); got != want {
			t.Errorf("CleanHost(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPickPort(t *testing.T) {
	for _, c := range []struct {
		in   []int
		want int
	}{{nil, 0}, {[]int{5678}, 5678}, {[]int{443, 80}, 80}, {[]int{3000, 9229}, 3000}, {[]int{9000, 9001}, 9000}} {
		if got := PickPort(c.in); got != c.want {
			t.Errorf("PickPort(%v) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestFailureReason(t *testing.T) {
	log := "==> Pulling x\nstuff\n==> Deployment failed: pull x: not found\n"
	if got := FailureReason(log); got != "pull x: not found" {
		t.Errorf("got %q", got)
	}
	if FailureReason("==> Deployment succeeded\n") != "" {
		t.Error("expected no reason")
	}
}

func TestCloneError(t *testing.T) {
	base := errors.New("exit status 128")
	e := cloneError("fatal: could not read Username for 'https://github.com': terminal prompts disabled", cloneOpts{}, false, base)
	if !strings.Contains(e.Error(), "private") {
		t.Errorf("auth: %v", e)
	}
	e = cloneError("warning: Could not find remote branch dev to clone.\nfatal: Remote branch dev not found in upstream origin", cloneOpts{Branch: "dev"}, false, base)
	if !strings.Contains(e.Error(), `"dev" doesn't exist`) {
		t.Errorf("branch: %v", e)
	}
}

func TestCleanGitURL(t *testing.T) {
	for in, want := range map[string]string{
		"github.com/a/b":           "https://github.com/a/b",
		" https://github.com/a/b ": "https://github.com/a/b",
		"http://gitlab.com/a/b":    "https://gitlab.com/a/b",
		"git@github.com:a/b.git":   "git@github.com:a/b.git",
		"foo":                      "foo",
	} {
		if got := CleanGitURL(in); got != want {
			t.Errorf("CleanGitURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseEnvDotenv(t *testing.T) {
	got, err := ParseEnv("export A=\"hello world\"\nB='x y'\nC=\"a\\\"b\\nc\"\nD=plain \"q\"\nE=\n")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"A=hello world", "B=x y", "C=a\"b\nc", "D=plain \"q\"", "E="}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("got %q", got)
	}
	if _, err := ParseEnv("MY KEY=1"); err == nil || !strings.Contains(err.Error(), "no spaces") {
		t.Fatalf("err %v", err)
	}
}
