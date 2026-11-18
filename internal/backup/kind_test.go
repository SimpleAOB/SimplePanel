package backup

import (
	"testing"

	"github.com/SimpleAOB/SimplePanel/internal/store"
)

func TestKindsRoundTrip(t *testing.T) {
	for _, k := range []string{"postgres", "mysql", "mariadb", "mongo", "redis", "volumes", "panel"} {
		for _, suffix := range []string{"", ".age"} {
			key := "f/p/s/20261117-030000-" + k + extension(k) + suffix
			if got := KindFromKey(key); got != k {
				t.Errorf("KindFromKey(%q) = %q", key, got)
			}
		}
	}
	if extension("mongo") != ".archive.gz" || extension("redis") != ".rdb.gz" {
		t.Error("unexpected extensions")
	}
}

func TestCompatible(t *testing.T) {
	cases := []struct {
		kind, app string
		ok        bool
	}{
		{"mongo", "mongo", true}, {"redis", "redis", true}, {"mongo", "redis", false},
		{"redis", "postgres", false}, {"postgres", "mongo", false}, {"mysql", "mariadb", true},
	}
	for _, c := range cases {
		if got := Compatible(c.kind, &store.App{Kind: c.app}); got != c.ok {
			t.Errorf("Compatible(%s, %s) = %v", c.kind, c.app, got)
		}
	}
}
