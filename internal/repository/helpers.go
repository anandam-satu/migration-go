package repository

import (
	"context"
	"database/sql"
	"strings"

	"github.com/shopspring/decimal"
)

func strPtr(ns sql.NullString) *string {
	if !ns.Valid {
		return nil
	}
	s := ns.String
	return &s
}

// decPtr parses a numeric text value (produced by ::text casts in queries)
// into *decimal.Decimal, nil when SQL NULL or unparsable.
func decPtr(ns sql.NullString) *decimal.Decimal {
	if !ns.Valid {
		return nil
	}
	d, err := decimal.NewFromString(strings.TrimSpace(ns.String))
	if err != nil {
		return nil
	}
	return &d
}

// sumDecimal runs an aggregate query whose single column is
// COALESCE(SUM(...),0)::text and returns the parsed decimal.
func sumDecimal(ctx context.Context, db *sql.DB, query string, args ...any) (decimal.Decimal, error) {
	var s string
	if err := db.QueryRowContext(ctx, query, args...).Scan(&s); err != nil {
		return decimal.Zero, err
	}
	d, err := decimal.NewFromString(strings.TrimSpace(s))
	if err != nil {
		return decimal.Zero, err
	}
	return d, nil
}

func scanStrings(ctx context.Context, db *sql.DB, query string, args ...any) ([]string, error) {
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var v sql.NullString
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		if v.Valid {
			out = append(out, v.String)
		}
	}
	return out, rows.Err()
}
