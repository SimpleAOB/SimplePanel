package deploy

import (
	"strings"
	"testing"
)

func TestDatabaseApp(t *testing.T) {
	n := 0
	rnd := func(int) string { n++; return strings.Repeat("x", n) + "p@ss/word" }
	for _, e := range Engines {
		a := NewDatabaseApp(&e, 1, "shop", "orders-db", DBCredentials{}, rnd)
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
	if DatabaseInfo(NewDatabaseApp(&Engines[0], 1, "p", "d", DBCredentials{}, rnd)) == nil {
		t.Fatal("expected info")
	}
}

func TestDatabaseCredentials(t *testing.T) {
	pg, my := EngineByKey("postgres"), EngineByKey("mysql")
	a := NewDatabaseApp(my, 1, "shop", "orders-db", DBCredentials{User: "shop_app", Password: "S3cure-pass!"}, func(int) string { return "gen" })
	info := DatabaseInfo(a)
	if info.User != "shop_app" || info.Password != "S3cure-pass!" || info.Database != "orders_db" || info.RootPassword != "gen" {
		t.Fatalf("custom credentials not used: %+v", info)
	}
	for _, c := range []DBCredentials{{User: "app"}, {Password: "longenough"}, {User: "_x1", Password: "a#b%c&d*e(f)"}} {
		if err := c.Validate(my); err != nil {
			t.Errorf("%+v rejected: %v", c, err)
		}
	}
	for _, c := range []DBCredentials{{User: "Root"}, {User: "root"}, {User: "1abc"}, {User: "a-b"}, {User: "sp_ai_readonly"},
		{User: strings.Repeat("a", 33)}, {Password: "short"}, {Password: "has space"}, {Password: "quo'te12"}, {Password: "dollar$12"}, {Password: "pässwörd"}} {
		if c.Validate(my) == nil {
			t.Errorf("%+v accepted", c)
		}
	}
	if err := (DBCredentials{User: "root"}).Validate(pg); err != nil {
		t.Errorf("postgres root user rejected: %v", err)
	}
}
