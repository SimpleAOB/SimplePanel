package backup

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Schedule is a parsed five-field cron expression:
// minute hour day-of-month month day-of-week.
type Schedule struct {
	min, hour, dom, month, dow [64]bool
	domStar, dowStar           bool
}

var cronAliases = map[string]string{
	"@hourly":  "0 * * * *",
	"@daily":   "0 3 * * *",
	"@weekly":  "0 3 * * 0",
	"@monthly": "0 3 1 * *",
}

var monthNames = []string{"jan", "feb", "mar", "apr", "may", "jun", "jul", "aug", "sep", "oct", "nov", "dec"}
var dayNames = []string{"sun", "mon", "tue", "wed", "thu", "fri", "sat"}

// ParseSchedule parses a cron expression such as "30 2 * * *" or "@daily".
func ParseSchedule(expr string) (*Schedule, error) {
	expr = strings.TrimSpace(strings.ToLower(expr))
	if a, ok := cronAliases[expr]; ok {
		expr = a
	}
	f := strings.Fields(expr)
	if len(f) != 5 {
		return nil, errors.New("schedule needs five fields: minute hour day month weekday (e.g. 0 3 * * *)")
	}
	s := &Schedule{domStar: f[2] == "*", dowStar: f[4] == "*"}
	specs := []struct {
		set      *[64]bool
		lo, hi   int
		names    []string
		nameBase int
		label    string
	}{
		{&s.min, 0, 59, nil, 0, "minute"},
		{&s.hour, 0, 23, nil, 0, "hour"},
		{&s.dom, 1, 31, nil, 0, "day of month"},
		{&s.month, 1, 12, monthNames, 1, "month"},
		{&s.dow, 0, 7, dayNames, 0, "weekday"},
	}
	for i, sp := range specs {
		if err := parseField(f[i], sp.set, sp.lo, sp.hi, sp.names, sp.nameBase); err != nil {
			return nil, fmt.Errorf("%s: %v", sp.label, err)
		}
	}
	if s.dow[7] {
		s.dow[0] = true // 7 is Sunday too
	}
	return s, nil
}

func parseField(field string, set *[64]bool, lo, hi int, names []string, nameBase int) error {
	num := func(v string) (int, error) {
		for i, n := range names {
			if v == n {
				return i + nameBase, nil
			}
		}
		n, err := strconv.Atoi(v)
		if err != nil || n < lo || n > hi {
			return 0, fmt.Errorf("%q is not between %d and %d", v, lo, hi)
		}
		return n, nil
	}
	for _, part := range strings.Split(field, ",") {
		step := 1
		if r, st, ok := strings.Cut(part, "/"); ok {
			n, err := strconv.Atoi(st)
			if err != nil || n < 1 || n > hi {
				return fmt.Errorf("bad step %q", st)
			}
			step, part = n, r
		}
		a, b := lo, hi
		switch {
		case part == "*":
		case strings.Contains(part, "-"):
			x, y, _ := strings.Cut(part, "-")
			var err error
			if a, err = num(x); err != nil {
				return err
			}
			if b, err = num(y); err != nil {
				return err
			}
			if a > b {
				return fmt.Errorf("range %q is backwards", part)
			}
		default:
			n, err := num(part)
			if err != nil {
				return err
			}
			a, b = n, n
			if step > 1 {
				b = hi
			}
		}
		for i := a; i <= b; i += step {
			set[i] = true
		}
	}
	return nil
}

func (s *Schedule) dayMatches(t time.Time) bool {
	dom, dow := s.dom[t.Day()], s.dow[int(t.Weekday())]
	switch {
	case s.domStar && s.dowStar:
		return true
	case s.domStar:
		return dow
	case s.dowStar:
		return dom
	}
	return dom || dow // cron: either matches when both are restricted
}

// Next returns the first time after t that matches, or the zero time if
// nothing matches within five years (e.g. February 30th).
func (s *Schedule) Next(t time.Time) time.Time {
	t = t.Truncate(time.Minute).Add(time.Minute)
	end := t.AddDate(5, 0, 0)
	for t.Before(end) {
		if !s.month[int(t.Month())] {
			t = time.Date(t.Year(), t.Month()+1, 1, 0, 0, 0, 0, t.Location())
			continue
		}
		if !s.dayMatches(t) {
			t = time.Date(t.Year(), t.Month(), t.Day()+1, 0, 0, 0, 0, t.Location())
			continue
		}
		if !s.hour[t.Hour()] {
			t = time.Date(t.Year(), t.Month(), t.Day(), t.Hour()+1, 0, 0, 0, t.Location())
			continue
		}
		if !s.min[t.Minute()] {
			t = t.Add(time.Minute)
			continue
		}
		return t
	}
	return time.Time{}
}
