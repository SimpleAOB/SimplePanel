package api

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"
)

// A deleted service's volumes must not end up under a new service with the
// same name (a new MariaDB on an old MySQL data directory crash-loops).
func TestLeftoverData(t *testing.T) {
	u, c, _, fs := newPublicServer(t)
	must := func(method, path, body string, want int) string {
		t.Helper()
		code, b := call(t, c, method, u+path, body, true)
		if code != want {
			t.Fatalf("%s %s: got %d want %d: %s", method, path, code, want, b)
		}
		return b
	}
	vol := func(name string, want bool) {
		t.Helper()
		fs.mu.Lock()
		defer fs.mu.Unlock()
		if fs.volumes[name] != want {
			t.Fatalf("volume %s exists=%v, want %v", name, fs.volumes[name], want)
		}
	}
	id := func(body string) string {
		t.Helper()
		var v struct {
			ID  int64 `json:"id"`
			App struct {
				ID int64 `json:"id"`
			} `json:"app"`
		}
		if err := json.Unmarshal([]byte(body), &v); err != nil {
			t.Fatal(err)
		}
		return strconv.FormatInt(max(v.ID, v.App.ID), 10)
	}
	setVol := func(name string) {
		fs.mu.Lock()
		fs.volumes[name] = true
		fs.mu.Unlock()
	}

	// A MySQL database named db was deleted earlier and kept its data.
	setVol("shop_db_data")
	b := must("POST", "/projects/1/databases", `{"name":"db","engine":"mariadb"}`, 409)
	if !strings.Contains(b, `"code":"leftover_data"`) || !strings.Contains(b, "shop_db_data") || !strings.Contains(b, `"canReplace":true`) {
		t.Fatalf("leftover answer: %s", b)
	}
	vol("shop_db_data", true)
	db := id(must("POST", "/projects/1/databases", `{"name":"db","engine":"mariadb","replaceData":true}`, 201))
	vol("shop_db_data", false)

	// The name is taken by a live database: that's a name conflict, and
	// its volume is never touched.
	setVol("shop_db_data")
	if b := must("POST", "/projects/1/databases", `{"name":"db","engine":"mysql","replaceData":true}`, 409); strings.Contains(b, "leftover") {
		t.Fatalf("live database treated as leftover: %s", b)
	}
	vol("shop_db_data", true)

	// Deleting with deleteData=1 deletes the data; without it, it stays.
	must("DELETE", "/apps/"+db+"?deleteData=1", "", 200)
	vol("shop_db_data", false)
	pg := id(must("POST", "/projects/1/databases", `{"name":"pg","engine":"postgres"}`, 201))
	setVol("shop_pg_data")
	must("DELETE", "/apps/"+pg, "", 200)
	vol("shop_pg_data", true)

	// Apps: refused, then either kept or replaced.
	setVol("shop_web_files")
	app := `{"name":"web","source":"image","image":"nginx:1.27","volumes":[{"name":"files","path":"/srv"}]`
	must("POST", "/projects/1/apps", app+`}`, 409)
	must("POST", "/projects/1/apps", app+`,"keepData":true}`, 201)
	vol("shop_web_files", true)

	// A volume added to an existing app.
	api := id(must("POST", "/projects/1/apps", `{"name":"api","source":"image","image":"nginx:1.27"}`, 201))
	setVol("shop_api_cache")
	upd := `{"source":"image","image":"nginx:1.27","volumes":[{"name":"cache","path":"/cache"}]`
	must("PUT", "/apps/"+api, upd+`}`, 409)
	must("PUT", "/apps/"+api, upd+`,"replaceData":true}`, 200)
	vol("shop_api_cache", false)
	// Saving again (the volume is now the app's own) is fine.
	setVol("shop_api_cache")
	must("PUT", "/apps/"+api, upd+`}`, 200)

	// Deleting the project with its data.
	must("DELETE", "/projects/1?deleteData=1", "", 200)
	vol("shop_web_files", false)
	vol("shop_api_cache", false)
}
