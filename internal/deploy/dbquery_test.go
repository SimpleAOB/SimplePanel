package deploy

import (
	"strings"
	"testing"
	"time"
)

func TestParseMongoQuery(t *testing.T) {
	q, err := ParseMongoQuery(`db.orders.find({ status: 'paid', total: { $gt: 20 }, "a.b": [1, -2.5, .5,], id: ObjectId("000000000000000000000001"), at: ISODate("2026-01-01"), n: /^ab\/c[/]/i, }).sort({ createdAt: -1, _id: 1 }).limit(5).skip(2);`)
	if err != nil {
		t.Fatal(err)
	}
	if q.Collection != "orders" || q.Method != "find" || q.Limit != 5 || q.Skip != 2 {
		t.Fatalf("%+v", q)
	}
	want := `{"status":"paid","total":{"$gt":20},"a.b":[1,-2.5,0.5],"id":{"$oid":"000000000000000000000001"},"at":{"$date":"2026-01-01"},"n":{"$regularExpression":{"pattern":"^ab\/c[/]","options":"i"}}}`
	if string(q.Args[0]) != strings.ReplaceAll(want, `\/`, `\\/`) {
		t.Fatalf("args:\n%s\n%s", q.Args[0], want)
	}
	if string(q.Sort) != `{"createdAt":-1,"_id":1}` { // key order kept
		t.Fatalf("sort %s", q.Sort)
	}

	for in, coll := range map[string]string{
		`db.getCollection("my coll").find()`:       "my coll",
		`db["x-y"].countDocuments()`:               "x-y",
		`db.app.events.find({})`:                   "app.events",
		`db.getSiblingDB("other").users.findOne()`: "users",
	} {
		q, err := ParseMongoQuery(in)
		if err != nil || q.Collection != coll {
			t.Fatalf("%s: %+v %v", in, q, err)
		}
	}
	if q, _ := ParseMongoQuery(`db.getSiblingDB("other").users.find()`); q.DB != "other" {
		t.Fatalf("db %q", q.DB)
	}
	if q, _ := ParseMongoQuery(`db.users.count({})`); q.Method != "countDocuments" {
		t.Fatal("count alias")
	}

	for _, bad := range []string{
		`db.orders.insertOne({a: 1})`,
		`db.orders.deleteMany({})`,
		`db.orders.find({}).forEach(x => print(x))`,
		`db.orders.find(require("fs"))`,
		`db.orders.find({ a: function() { return 1 } })`,
		`db.orders.aggregate([{ $out: "copy" }])`,
		`db.orders.aggregate([{ "$merge": { into: "copy" } }])`,
		`db.system.users.find()`,
		`db.getSiblingDB("admin").getCollection("system.users").find()`,
		`db.orders.find(); db.orders.drop()`,
		`db.orders.find({}).limit(-1)`,
		`db.orders.distinct()`,
		`show dbs`,
		`db.orders.find({a: "x`,
	} {
		if _, err := ParseMongoQuery(bad); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}

func TestParseMongoOutput(t *testing.T) {
	r, err := parseMongoOutput(`warning line
{"docs":[{"_id":{"$oid":"6afd"},"a":1,"d":{"$date":"2026-01-01T00:00:00Z"}},{"_id":{"$oid":"6afe"},"b":{"x":[1,2]},"a":null}]}`, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(r.Columns, ",") != "_id,a,d,b" {
		t.Fatalf("columns %v", r.Columns)
	}
	if r.Rows[0][0] != "6afd" || r.Rows[0][1] != "1" || r.Rows[0][2] != "2026-01-01T00:00:00Z" || r.Rows[0][3] != nil {
		t.Fatalf("row 0 %#v", r.Rows[0])
	}
	if r.Rows[1][3] != `{"x":[1,2]}` || r.Rows[1][1] != nil {
		t.Fatalf("row 1 %#v", r.Rows[1])
	}
	r, _ = parseMongoOutput(`{"docs":[{"a":1},{"a":2},{"a":3}]}`, 2)
	if r.RowCount != 2 || !r.Truncated {
		t.Fatalf("cap %+v", r)
	}
}

func TestRedis(t *testing.T) {
	args, err := SplitRedisCommand(`HGET "user:1 x" 'it''s' "a\"b\n"`)
	if err != nil || len(args) != 4 || args[1] != "user:1 x" || args[2] != "its" || args[3] != "a\"b\n" {
		t.Fatalf("%q %v", args, err)
	}
	for _, bad := range []string{"", `GET "x`, "GET a\nSET a b"} {
		if _, err := SplitRedisCommand(bad); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
	r, _ := parseRedisReply("ZRANGE", []string{"ZRANGE", "z", "0", "-1", "WITHSCORES"}, `[["a",1],["b",2.5]]`, 100)
	if strings.Join(r.Columns, ",") != "member,score" || r.Rows[1][1] != "2.5" {
		t.Fatalf("zrange %+v", r)
	}
	r, _ = parseRedisReply("LRANGE", []string{"LRANGE"}, `["x","y"]`, 1)
	if r.RowCount != 1 || !r.Truncated || r.Rows[0][1] != "x" {
		t.Fatalf("lrange %+v", r)
	}
	if msg, ok := redisReplyError(`error:"NOPERM no"`); !ok || msg != "NOPERM no" {
		t.Fatal(msg)
	}
	if !isAuthFailure("AUTH failed: WRONGPASS invalid username-password pair") || isAuthFailure("NOPERM User sp_query_ro has no permissions") {
		t.Fatal("auth failure detection")
	}
}

func TestQueryOptions(t *testing.T) {
	o := QueryOptions{}
	o.Normalize()
	if o.Timeout != 30*time.Second || o.MaxRows != 1000 {
		t.Fatalf("%+v", o)
	}
	o = QueryOptions{Timeout: time.Hour, MaxRows: 1 << 30}
	o.Normalize()
	if o.Timeout != QueryMaxTimeout || o.MaxRows != QueryMaxRows {
		t.Fatalf("%+v", o)
	}
	if explainSQL("select 1") != "EXPLAIN select 1" || explainSQL("EXPLAIN select 1") != "EXPLAIN select 1" {
		t.Fatal("explain")
	}
	if got := cleanClientError("ERROR 1142 (42000) at line 2: DELETE command denied"); got != "ERROR 1142 (42000): DELETE command denied" {
		t.Fatal(got)
	}
}
