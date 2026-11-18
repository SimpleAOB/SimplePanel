package gitprovider

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"strings"
	"testing"
	"time"

	"github.com/SimpleAOB/SimplePanel/internal/store"
)

func TestAppJWT(t *testing.T) {
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	p := &store.GitProvider{AppID: 42, PrivateKey: string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))}
	now := time.Unix(1_700_000_000, 0)
	tok, err := appJWT(p, now)
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		t.Fatalf("jwt %q", tok)
	}
	sig, _ := base64.RawURLEncoding.DecodeString(parts[2])
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(&key.PublicKey, crypto.SHA256, sum[:], sig); err != nil {
		t.Fatal("signature doesn't verify:", err)
	}
	var claims map[string]any
	b, _ := base64.RawURLEncoding.DecodeString(parts[1])
	json.Unmarshal(b, &claims)
	if claims["iss"] != "42" || claims["exp"].(float64)-claims["iat"].(float64) > 600 {
		t.Fatalf("claims %v", claims)
	}
	if _, err := appJWT(&store.GitProvider{PrivateKey: "nope"}, now); err == nil {
		t.Fatal("bad key accepted")
	}
}

func TestParseGitHub(t *testing.T) {
	ev, err := ParseGitHub("push", []byte(`{"ref":"refs/heads/main","after":"abc","repository":{"full_name":"a/b","default_branch":"main"},"head_commit":{"message":"Fix it\n\nmore"}}`))
	if err != nil || ev.Kind != "push" || ev.Branch != "main" || ev.Message != "Fix it" || ev.Repo != "a/b" {
		t.Fatalf("%+v %v", ev, err)
	}
	ev, _ = ParseGitHub("push", []byte(`{"ref":"refs/tags/v1","repository":{"full_name":"a/b"}}`))
	if !ev.Ignored() {
		t.Fatal("tag push should be ignored")
	}
	ev, _ = ParseGitHub("pull_request", []byte(`{"action":"synchronize","number":5,"pull_request":{"head":{"ref":"f","sha":"s","repo":{"full_name":"A/B"}},"base":{"ref":"main"}},"repository":{"full_name":"a/b"}}`))
	if ev.Kind != "pr" || ev.PRAction != "update" || ev.Fork || ev.PR != 5 {
		t.Fatalf("%+v", ev)
	}
	ev, _ = ParseGitHub("pull_request", []byte(`{"action":"opened","number":5,"pull_request":{"head":{"ref":"f","repo":null},"base":{"ref":"main"}},"repository":{"full_name":"a/b"}}`))
	if !ev.Fork {
		t.Fatal("a PR whose head repo was deleted counts as a fork")
	}
	ev, _ = ParseGitHub("issues", []byte(`{}`))
	if !ev.Ignored() {
		t.Fatal("issues should be ignored")
	}
}

func TestParseGitLab(t *testing.T) {
	ev, err := ParseGitLab("Push Hook", []byte(`{"ref":"refs/heads/main","after":"abc","checkout_sha":"abc","project":{"path_with_namespace":"g/p","default_branch":"main"},"commits":[{"id":"abc","message":"Hi"}]}`))
	if err != nil || ev.Kind != "push" || ev.Deleted || ev.Message != "Hi" || ev.Repo != "g/p" {
		t.Fatalf("%+v %v", ev, err)
	}
	ev, _ = ParseGitLab("Push Hook", []byte(`{"ref":"refs/heads/old","after":"0000000000000000000000000000000000000000","checkout_sha":null,"project":{"path_with_namespace":"g/p"}}`))
	if !ev.Deleted {
		t.Fatal("branch deletion not detected")
	}
	ev, _ = ParseGitLab("Merge Request Hook", []byte(`{"project":{"path_with_namespace":"g/p"},"object_attributes":{"iid":9,"action":"update","oldrev":"x","source_branch":"f","target_branch":"main","source_project_id":1,"target_project_id":1,"last_commit":{"id":"c"}}}`))
	if ev.Kind != "pr" || ev.PRAction != "update" || ev.PR != 9 || ev.Fork {
		t.Fatalf("%+v", ev)
	}
	ev, _ = ParseGitLab("Merge Request Hook", []byte(`{"project":{"path_with_namespace":"g/p"},"object_attributes":{"iid":9,"action":"update","source_project_id":2,"target_project_id":1}}`))
	if ev.PRAction != "edit" || !ev.Fork {
		t.Fatalf("%+v", ev)
	}
	ev, _ = ParseGitLab("Merge Request Hook", []byte(`{"object_attributes":{"action":"merge"}}`))
	if ev.PRAction != "close" {
		t.Fatalf("%+v", ev)
	}
}
