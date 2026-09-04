package service

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"strings"
)

// qtyInt converts a nullable MySQL numeric (DECIMAL from SUM/CASE) into an
// int64 exactly like Java's ResultSet.getInt(): NULL -> 0 and fractional
// values truncate toward zero.
func qtyInt(ns sql.NullString) int64 {
	if !ns.Valid {
		return 0
	}
	s := strings.TrimSpace(ns.String)
	if i := strings.IndexByte(s, '.'); i >= 0 {
		s = s[:i]
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0
	}
	return n
}

// nullStr converts a nullable string into an interface{} bind value (nil = SQL NULL).
func nullStr(ns sql.NullString) any {
	if !ns.Valid {
		return nil
	}
	return ns.String
}

// dateString converts a nullable MySQL DATE into a "2006-01-02" string bind
// value (nil = SQL NULL).
func dateString(nt sql.NullTime) any {
	if !nt.Valid {
		return nil
	}
	return nt.Time.Format("2006-01-02")
}

// logEstimate runs the "SELECT COUNT(*)" estimate logged at the start of a job.
func (s *Service) logEstimate(ctx context.Context, sourceSQL, label string) {
	q := "SELECT COUNT(*) FROM (" + sourceSQL + ") as total"
	var n sql.NullInt64
	if err := s.mysql.QueryRowContext(ctx, q).Scan(&n); err != nil {
		s.logger.Warn("Gagal menghitung total data source", "source", label, "err", err)
		return
	}
	s.logger.Info("ESTIMASI TOTAL DATA "+label+" DARI SOURCE", "total", n.Int64)
}

// rootCause unwraps an error chain down to the deepest cause.
func rootCause(err error) string {
	if err == nil {
		return ""
	}
	var out []string
	for e := err; e != nil; e = errors.Unwrap(e) {
		out = append(out, e.Error())
		if len(out) > 10 {
			break
		}
	}
	return strings.Join(out, " <- ")
}
