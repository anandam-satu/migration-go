package service

import (
	"database/sql"
	"testing"
	"time"
)

func TestQtyInt(t *testing.T) {
	cases := []struct {
		name string
		ns   sql.NullString
		want int64
	}{
		{"null", sql.NullString{Valid: false}, 0},
		{"whole", sql.NullString{String: "34", Valid: true}, 34},
		{"fractional truncates", sql.NullString{String: "34.5000", Valid: true}, 34},
		{"negative truncates toward zero", sql.NullString{String: "-3.7", Valid: true}, -3},
		{"unparsable", sql.NullString{String: "abc", Valid: true}, 0},
	}
	for _, c := range cases {
		if got := qtyInt(c.ns); got != c.want {
			t.Errorf("%s: qtyInt(%+v) = %d, want %d", c.name, c.ns, got, c.want)
		}
	}
}

func TestDateString(t *testing.T) {
	if got := dateString(sql.NullTime{Valid: false}); got != nil {
		t.Errorf("dateString(NULL) = %v, want nil", got)
	}
	d := time.Date(2024, 5, 6, 0, 0, 0, 0, time.UTC)
	if got := dateString(sql.NullTime{Time: d, Valid: true}); got != "2024-05-06" {
		t.Errorf("dateString = %v, want 2024-05-06", got)
	}
}
