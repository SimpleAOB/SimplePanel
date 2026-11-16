package deploy

import (
	"reflect"
	"testing"
)

func TestValidName(t *testing.T) {
	for _, ok := range []string{"a", "blog", "my-app", "app1"} {
		if ValidName(ok) != nil {
			t.Errorf("%q should be valid", ok)
		}
	}
	for _, bad := range []string{"", "-a", "a-", "A", "a_b", "a--b", "a.b", "abcdefghijabcdefghijabcdefghijk"} {
		if ValidName(bad) == nil {
			t.Errorf("%q should be invalid", bad)
		}
	}
}

func TestValidImage(t *testing.T) {
	for _, ok := range []string{"nginx", "nginx:1.27", "ghcr.io/org/app:latest", "localhost:5000/a/b", "traefik:v3.3",
		"nginx@sha256:" + "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"} {
		if err := ValidImage(ok); err != nil {
			t.Errorf("%q should be valid: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "Nginx", "nginx:", "-nginx", "nginx; rm -rf /", "a b"} {
		if ValidImage(bad) == nil {
			t.Errorf("%q should be invalid", bad)
		}
	}
}

func TestValidGitURL(t *testing.T) {
	if ValidGitURL("https://github.com/user/repo") != nil {
		t.Error("github https should be valid")
	}
	for _, bad := range []string{"http://github.com/u/r", "git@github.com:u/r.git", "file:///etc", "https://u:p@github.com/u/r", "https://github.com", "ext::sh -c id", "--upload-pack=x"} {
		if ValidGitURL(bad) == nil {
			t.Errorf("%q should be invalid", bad)
		}
	}
}

func TestValidBranch(t *testing.T) {
	for _, bad := range []string{"-x", "--upload-pack=x", "a..b", "a b"} {
		if ValidBranch(bad) == nil {
			t.Errorf("%q should be invalid", bad)
		}
	}
	if ValidBranch("feature/x-1") != nil {
		t.Error("feature/x-1 should be valid")
	}
}

func TestCleanRelPath(t *testing.T) {
	cases := map[string]string{"": "", ".": "", "app": "app", "./app/": "app", "a/../b": "b"}
	for in, want := range cases {
		got, err := CleanRelPath(in)
		if err != nil || got != want {
			t.Errorf("CleanRelPath(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"/etc", "..", "../x", "a/../../x"} {
		if _, err := CleanRelPath(bad); err == nil {
			t.Errorf("%q should be rejected", bad)
		}
	}
}

func TestValidHost(t *testing.T) {
	for _, ok := range []string{"example.com", "app.example.co.uk", "a-b.example.com"} {
		if ValidHost(ok) != nil {
			t.Errorf("%q should be valid", ok)
		}
	}
	for _, bad := range []string{"localhost", "1.2.3.4", "*.example.com", "Example.com", "a..com", "ex`ample.com", "example.com:80"} {
		if ValidHost(bad) == nil {
			t.Errorf("%q should be invalid", bad)
		}
	}
}

func TestParseEnv(t *testing.T) {
	got, err := ParseEnv("# comment\nA=1\n\nB = two=2\nC=\n")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"A=1", "B= two=2", "C="}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %q want %q", got, want)
	}
	if _, err := ParseEnv("NOEQUALS"); err == nil {
		t.Error("expected error")
	}
	if _, err := ParseEnv("1BAD=x"); err == nil {
		t.Error("expected error")
	}
}

func TestSplitCommand(t *testing.T) {
	got, err := SplitCommand(`node server.js --name "hello world" 'a b' c\ d`)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"node", "server.js", "--name", "hello world", "a b", "c d"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %q want %q", got, want)
	}
	if _, err := SplitCommand(`echo "unterminated`); err == nil {
		t.Error("expected error")
	}
	if got, _ := SplitCommand(""); len(got) != 0 {
		t.Error("empty command should give no args")
	}
}
