package deploy

import (
	"strings"
	"testing"
)

func TestWrapLimit(t *testing.T) {
	if got := wrapLimit("SELECT * FROM t", 501); !strings.HasPrefix(got, "SELECT * FROM (\nSELECT * FROM t\n) AS sp_q LIMIT 501") {
		t.Fatalf("select not wrapped: %q", got)
	}
	if got := wrapLimit("with x as (select 1) select * from x", 10); !strings.Contains(got, "LIMIT 10") {
		t.Fatalf("CTE not wrapped: %q", got)
	}
	for _, q := range []string{"SHOW TABLES", "EXPLAIN SELECT 1", "selection"} {
		if got := wrapLimit(q, 10); got != q {
			t.Fatalf("%q was wrapped: %q", q, got)
		}
	}
}

func TestCleanQuery(t *testing.T) {
	if q, err := cleanQuery("  select 1 ;; \n"); err != nil || q != "select 1" {
		t.Fatalf("%q %v", q, err)
	}
	if _, err := cleanQuery(" ; "); err == nil {
		t.Fatal("empty query accepted")
	}
}

func TestParseResults(t *testing.T) {
	r, err := parseCSV("id,note\n1,\"a, \"\"b\"\"\"\n2," + pgNullMarker + "\n")
	if err != nil || len(r.Rows) != 2 || r.Rows[0][1] != `a, "b"` || r.Rows[1][1] != nil {
		t.Fatalf("csv: %+v %v", r, err)
	}
	r = parseTSV("id\tnote\n1\tx\\ty\\nz\n2\tNULL\n")
	if len(r.Rows) != 2 || r.Rows[0][1] != "x\ty\nz" || r.Rows[1][1] != nil {
		t.Fatalf("tsv: %+v", r)
	}
	var b strings.Builder
	b.WriteString("n\n")
	for i := 0; i < AIMaxRows+1; i++ {
		b.WriteString("1\n")
	}
	if r := parseTSV(b.String()); r.RowCount != AIMaxRows || !r.Truncated {
		t.Fatalf("not capped: %d %v", r.RowCount, r.Truncated)
	}
}

func TestSQLString(t *testing.T) {
	if got := sqlString(`a'b\c`, true); got != `'a''b\\c'` {
		t.Fatalf("mysql: %s", got)
	}
	if got := sqlString(`a'b\c`, false); got != `'a''b\c'` {
		t.Fatalf("postgres: %s", got)
	}
}
