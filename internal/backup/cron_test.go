package backup

import (
	"testing"
	"time"
)

func TestScheduleNext(t *testing.T) {
	base := time.Date(2026, 3, 14, 10, 17, 42, 0, time.UTC) // a Saturday
	cases := []struct{ expr, want string }{
		{"@hourly", "2026-03-14T11:00"},
		{"@daily", "2026-03-15T03:00"},
		{"30 2 * * *", "2026-03-15T02:30"},
		{"*/15 * * * *", "2026-03-14T10:30"},
		{"0 9 * * mon-fri", "2026-03-16T09:00"},
		{"@weekly", "2026-03-15T03:00"},
		{"0 3 1 * *", "2026-04-01T03:00"},
		{"0 0 29 2 *", "2028-02-29T00:00"},
		{"0 12 1,15 jan,jul *", "2026-07-01T12:00"},
	}
	for _, c := range cases {
		s, err := ParseSchedule(c.expr)
		if err != nil {
			t.Fatalf("%s: %v", c.expr, err)
		}
		if got := s.Next(base).Format("2006-01-02T15:04"); got != c.want {
			t.Errorf("%s: got %s want %s", c.expr, got, c.want)
		}
	}
	for _, bad := range []string{"", "* * * *", "60 * * * *", "0 24 * * *", "0 0 0 * *", "0 0 * 13 *", "*/0 * * * *", "a b c d e", "5-1 * * * *"} {
		if _, err := ParseSchedule(bad); err == nil {
			t.Errorf("%q should be rejected", bad)
		}
	}
}
