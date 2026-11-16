package deploy

import (
	"strings"
	"testing"
)

func TestDatabaseApp(t *testing.T) {
	n := 0
	rnd := func(int) string { n++; return strings.Repeat("x", n) + "p@ss/word" }
	for _, e := range Engines {
		a := NewDatabaseApp(&e, 1, "shop", "orders-db", rnd)
		if a.Kind != e.Key || len(a.Volumes) != 1 || a.Volumes[0].Path != e.DataPath {
			t.Fatalf("%s: bad app %+v", e.Key, a)
		}
		if _, err := ParseEnv(a.Env); err != nil {
			t.Fatalf("%s: env invalid: %v", e.Key, err)
		}
		info := DatabaseInfo(a)
		if info.Host != "shop_orders-db" || info.User != "orders_db" || info.Database != "orders_db" || info.Password == "" {
			t.Fatalf("%s: info %+v", e.Key, info)
		}
		if e.Key != "postgres" && info.RootPassword == "" {
			t.Errorf("%s: missing root password", e.Key)
		}
		if !strings.Contains(info.URL, "p%40ss%2Fword@shop_orders-db:") {
			t.Errorf("%s: password not escaped in URL: %s", e.Key, info.URL)
		}
	}
	if DatabaseInfo(NewDatabaseApp(&Engines[0], 1, "p", "d", rnd)) == nil {
		t.Fatal("expected info")
	}
}
