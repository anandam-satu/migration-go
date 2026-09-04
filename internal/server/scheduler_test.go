package server

import (
	"testing"
	"time"
)

func TestInQuietWindow(t *testing.T) {
	at := func(hhmm string) time.Time {
		tm, err := time.Parse("2006-01-02 15:04", "2024-01-01 "+hhmm)
		if err != nil {
			t.Fatalf("parse %q: %v", hhmm, err)
		}
		return tm
	}

	cases := []struct {
		time string
		want bool // true = inside quiet window (skip migration)
	}{
		{"06:00", true},  // before 07:55
		{"07:54", true},  // before 07:55
		{"07:55", false}, // boundary: not before -> run
		{"12:00", false}, // working hours
		{"21:14", false}, // not after 21:15 -> run
		{"21:15", false}, // boundary: not after -> run (matches isAfter)
		{"21:16", true},  // after 21:15 -> skip
		{"23:59", true},
		{"00:00", true},
	}
	for _, c := range cases {
		if got := inQuietWindow(at(c.time)); got != c.want {
			t.Errorf("inQuietWindow(%s) = %v, want %v", c.time, got, c.want)
		}
	}
}
