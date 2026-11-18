package deploy

import (
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"

	"github.com/SimpleAOB/SimplePanel/internal/store"
)

func TestPreviewName(t *testing.T) {
	if n := PreviewName("web", 12); n != "web-pr-12" {
		t.Fatal(n)
	}
	long := "abcdefghij-abcdefghij-abcdefgh" // 30 chars
	n := PreviewName(long, 1234)
	if len(n) > 30 || ValidName(n) != nil || !strings.HasSuffix(n, "-pr-1234") {
		t.Fatalf("%q", n)
	}
}

func TestMergeEnv(t *testing.T) {
	got := MergeEnv("# c\nA=1\nB=2\n", "B=preview\nC=3")
	if got != "# c\nA=1\nB=preview\nC=3\n" {
		t.Fatalf("%q", got)
	}
	if MergeEnv("A=1\n", "") != "A=1\n" {
		t.Fatal("empty override changed env")
	}
}

func TestSyncPreview(t *testing.T) {
	parent := &store.App{ID: 1, ProjectID: 2, ProjectName: "shop", Name: "web", Source: "git", GitURL: "https://x/y",
		Env: "A=1\n", PreviewEnv: "A=2", Replicas: 3, Ports: []store.Port{{Published: 8080, Target: 80}},
		Volumes: []store.Volume{{Name: "data", Path: "/data"}}, Previews: true}
	p := &store.App{Name: "web-pr-1"}
	SyncPreview(p, parent, "feat", "Title", "https://pr")
	if p.Replicas != 1 || len(p.Ports) != 0 || p.Env != "A=2\n" || p.GitBranch != "feat" || p.Previews || !p.AutoDomain || p.ProjectName != "shop" {
		t.Fatalf("%+v", p)
	}
	p.Volumes[0].Name = "changed"
	if parent.Volumes[0].Name != "data" {
		t.Fatal("preview shares the parent's volume slice")
	}
}

func TestNewDeployKey(t *testing.T) {
	priv, pub, err := NewDeployKey("simplepanel-shop_web")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ssh.ParsePrivateKey([]byte(priv)); err != nil {
		t.Fatal("private key doesn't parse:", err)
	}
	if _, _, _, _, err := ssh.ParseAuthorizedKey([]byte(pub)); err != nil || !strings.HasSuffix(pub, " simplepanel-shop_web") {
		t.Fatalf("public key %q: %v", pub, err)
	}
	for _, line := range strings.Split(strings.TrimSpace(KnownHosts), "\n") {
		if _, _, _, _, _, err := ssh.ParseKnownHosts([]byte(line)); err != nil {
			t.Fatalf("known host %q: %v", line, err)
		}
	}
}

func TestDeployKeyURL(t *testing.T) {
	for in, want := range map[string]string{
		"git@github.com:acme/web.git":                 "https://github.com/acme/web/settings/keys/new",
		"https://gitlab.com/g/sub/p.git":              "https://gitlab.com/g/sub/p/-/settings/repository#js-deploy-keys-settings",
		"ssh://git@git.example.com:2222/team/app.git": "",
	} {
		if got := DeployKeyURL(in); got != want {
			t.Errorf("%s: %q, want %q", in, got, want)
		}
	}
}
