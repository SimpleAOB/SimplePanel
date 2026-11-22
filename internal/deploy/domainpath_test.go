package deploy

import "testing"

func TestDomainPath(t *testing.T) {
	for in, want := range map[string]string{
		"https://staging3.bakkesplugins.com/api": "/api",
		"staging3.bakkesplugins.com/api/":        "/api",
		"example.com":                            "",
		"example.com/":                           "",
		"https://example.com/":                   "",
		"example.com/API//v2/?x=1#top":           "/API/v2",
		"example.com:8080/api":                   "/api",
		"example.com?x=/api":                     "",
	} {
		if got := DomainPath(in); got != want {
			t.Errorf("DomainPath(%q) = %q, want %q", in, got, want)
		}
	}
	if h := CleanHost("https://Staging3.Example.com/API"); h != "staging3.example.com" {
		t.Errorf("CleanHost = %q", h)
	}
}

func TestValidPath(t *testing.T) {
	for _, ok := range []string{"", "/api", "/api/v2", "/.well-known", "/a-b_c.d~e", "/%20x"} {
		if err := ValidPath(ok); err != nil {
			t.Errorf("ValidPath(%q) = %v", ok, err)
		}
	}
	for _, bad := range []string{"/a`b", "/..", "/a/./b", "/a b", "/a\"b", "/a'b", "/a)b", "/" + string(make([]byte, 201))} {
		if err := ValidPath(CleanPath(bad)); err == nil && CleanPath(bad) != "" {
			t.Errorf("ValidPath(%q) should fail", bad)
		}
	}
}
