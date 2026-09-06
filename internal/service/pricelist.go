package service

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/shopspring/decimal"

	"github.com/anandam/api-migration/internal/config"
	"github.com/anandam/api-migration/internal/normalize"
	"github.com/anandam/api-migration/internal/sheet"
)

// pricelistRow is one parsed product from the Google Sheet. Money fields are
// kept as parsed decimals; Spesifikasi is nil when the cell is empty.
type pricelistRow struct {
	Normalized     string // normalize.NormalizeItemName(ItemName) — row key
	ItemName       string
	Spesifikasi    *string
	Modal          *decimal.Decimal
	FinalPricelist *decimal.Decimal
}

// PricelistSyncer keeps public.pricelist in sync with the "PRICELIST&MODAL"
// tab of a Google Spreadsheet (columns A=spesifikasi, B=item_name, C=modal,
// D=final_pricelist). Every successful run makes the table mirror the sheet:
// rows present in the sheet are inserted/updated, rows no longer in the sheet
// (and legacy rows without a normalized_item_name) are deleted. Concurrent
// runs are serialized with a mutex so two overlapping ticks never interleave.
type PricelistSyncer struct {
	pg     *sql.DB
	cfg    config.GoogleSheet
	logger *slog.Logger
	mu     sync.Mutex
	fetch  func(ctx context.Context, spreadsheetID, rng string) ([][]any, error)
}

// NewPricelistSyncer creates a syncer. cfg must be the resolved GoogleSheet
// configuration; it is considered enabled (and only then constructed) when
// cfg.ServiceAccountFile is non-empty.
func NewPricelistSyncer(pg *sql.DB, cfg config.GoogleSheet, logger *slog.Logger) *PricelistSyncer {
	s := &PricelistSyncer{pg: pg, cfg: cfg, logger: logger}
	s.fetch = func(ctx context.Context, spreadsheetID, rng string) ([][]any, error) {
		c, err := sheet.NewClient(ctx, cfg.ServiceAccountFile)
		if err != nil {
			return nil, err
		}
		return c.ReadRange(ctx, spreadsheetID, rng)
	}
	return s
}

// Sync pulls the sheet and mirrors it into pricelist inside one transaction.
// It returns an error (and leaves the table untouched) on any failure; when
// the sheet yields no data rows it logs a warning and does nothing, so a
// transient blank response can never wipe the table.
func (s *PricelistSyncer) Sync(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	log := s.logger
	start := time.Now()

	values, err := s.fetch(ctx, s.cfg.SpreadsheetID, s.cfg.TabRange)
	if err != nil {
		return fmt.Errorf("read google sheet: %w", err)
	}

	rows, skipped := parsePricelistRows(values)
	if len(rows) == 0 {
		log.Warn("Pricelist sync skipped: sheet has no data rows with item_name (table NOT modified)",
			"raw_rows", len(values), "skipped", skipped)
		return nil
	}

	keys := make([]string, 0, len(rows))
	for k := range rows {
		keys = append(keys, k)
	}

	tx, err := s.pg.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback() // no-op after Commit

	upserted, err := upsertPricelistRows(ctx, tx, rows)
	if err != nil {
		return err
	}
	deleted, err := deleteStalePricelistRows(ctx, tx, keys)
	if err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit pricelist sync: %w", err)
	}

	d := time.Since(start).Milliseconds() / 1000
	log.Info("Pricelist sync selesai",
		"rows", len(rows), "upserted", upserted, "deleted", deleted, "skipped", skipped, "waktu_detik", d)
	return nil
}

// upsertPricelistRows inserts each parsed row or updates the row(s) already
// keyed by normalized_item_name. There is no unique constraint on that column
// (the live table only has a non-unique index), so a plain ON CONFLICT is not
// possible: we UPDATE first and INSERT when no row matched.
func upsertPricelistRows(ctx context.Context, tx *sql.Tx, rows map[string]pricelistRow) (int, error) {
	const updateSQL = `UPDATE pricelist
		SET item_name = $1, spesifikasi = $2, modal = $3, final_pricelist = $4
		WHERE normalized_item_name = $5`
	const insertSQL = `INSERT INTO pricelist (id, item_name, normalized_item_name, spesifikasi, modal, final_pricelist)
		VALUES (nextval('pricelist_seq'), $1, $2, $3, $4, $5)`

	count := 0
	for _, r := range rows {
		res, err := tx.ExecContext(ctx, updateSQL,
			r.ItemName, strOrNil(r.Spesifikasi), decOrNil(r.Modal), decOrNil(r.FinalPricelist), r.Normalized)
		if err != nil {
			return 0, fmt.Errorf("update pricelist %q: %w", r.Normalized, err)
		}
		if n, err := res.RowsAffected(); err == nil && n > 0 {
			count++
			continue
		}
		if _, err := tx.ExecContext(ctx, insertSQL,
			r.ItemName, r.Normalized, strOrNil(r.Spesifikasi), decOrNil(r.Modal), decOrNil(r.FinalPricelist)); err != nil {
			return 0, fmt.Errorf("insert pricelist %q: %w", r.Normalized, err)
		}
		count++
	}
	return count, nil
}

// deleteStalePricelistRows removes rows whose normalized_item_name is not part
// of the current sheet snapshot (mirror semantics), including legacy rows that
// never got one (normalized_item_name IS NULL).
func deleteStalePricelistRows(ctx context.Context, tx *sql.Tx, keys []string) (int64, error) {
	res, err := tx.ExecContext(ctx,
		`DELETE FROM pricelist
		 WHERE normalized_item_name IS NULL
		    OR NOT (normalized_item_name = ANY($1::text[]))`, keys)
	if err != nil {
		return 0, fmt.Errorf("delete stale pricelist rows: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// parsePricelistRows converts the rectangular sheet values into rows keyed by
// normalized item name. The sheet is read positionally (no header row): cell 0
// is spesifikasi, 1 is item_name, 2 is modal, 3 is final_pricelist. Rows that
// are entirely blank, or that carry no item_name (merged/odd rows), are
// skipped. When the same normalized name occurs more than once the first row
// wins and later rows only fill fields that were still empty — the "merge ke
// atas" behaviour of the original Java worker.
func parsePricelistRows(values [][]any) (map[string]pricelistRow, int) {
	out := make(map[string]pricelistRow)
	skipped := 0
	for _, row := range values {
		spec := cellStringAt(row, 0)
		name := cellStringAt(row, 1)
		modalStr := cellStringAt(row, 2)
		priceStr := cellStringAt(row, 3)

		if spec == "" && name == "" && modalStr == "" && priceStr == "" {
			continue
		}
		norm := normalize.NormalizeItemName(name)
		if norm == "" {
			skipped++
			continue
		}

		r := pricelistRow{Normalized: norm, ItemName: name}
		if spec != "" {
			v := spec
			r.Spesifikasi = &v
		}
		r.Modal = cleanDecimal(modalStr)
		r.FinalPricelist = cleanDecimal(priceStr)

		if existing, ok := out[norm]; ok {
			if existing.Spesifikasi == nil && r.Spesifikasi != nil {
				existing.Spesifikasi = r.Spesifikasi
			}
			if existing.Modal == nil && r.Modal != nil {
				existing.Modal = r.Modal
			}
			if existing.FinalPricelist == nil && r.FinalPricelist != nil {
				existing.FinalPricelist = r.FinalPricelist
			}
			out[norm] = existing
			continue
		}
		out[norm] = r
	}
	return out, skipped
}

// cellStringAt returns the trimmed text of row[idx] ("", never an error, when
// the cell is empty or past the end of the row). Sheets FORMATTED_VALUE
// rendering returns strings, but numbers are handled defensively.
func cellStringAt(row []any, idx int) string {
	if idx >= len(row) {
		return ""
	}
	switch v := row[idx].(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(v)
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(v)
	default:
		return strings.TrimSpace(fmt.Sprint(v))
	}
}

// reNonNumeric matches every character that is not a digit, dot, comma or
// minus sign (used by cleanDecimal after currency/space removal).
var reNonNumeric = regexp.MustCompile(`[^0-9.,\-]`)

// cleanDecimal ports MigrationService.cleanBigDecimal(...) from the Java
// worker: it parses Indonesian-style money text into a decimal, returning nil
// for empty/unparsable input.
//
//	"2.857.000"    (dots = thousand separators) -> 2857000
//	"2.857.000,50" (comma = decimal)            -> 2857000.50
//	"44.00"        (single dot, not 3-digit group) -> 44.00
//	"Rp 2857000"   (currency prefix stripped)   -> 2857000
func cleanDecimal(val string) *decimal.Decimal {
	if val == "" {
		return nil
	}
	// Remove non-breaking spaces, regular spaces and the currency prefix.
	s := strings.NewReplacer("\u00A0", "", " ", "", "Rp", "", "rp", "").Replace(val)
	s = reNonNumeric.ReplaceAllString(s, "")
	if s == "" {
		return nil
	}
	if strings.Contains(s, ",") {
		// Comma present: dots are thousand separators, comma is the decimal sep.
		s = strings.ReplaceAll(s, ".", "")
		s = strings.ReplaceAll(s, ",", ".")
	} else if strings.Contains(s, ".") {
		parts := strings.Split(s, ".")
		if len(parts) > 1 {
			allThreeDigits := true
			for _, p := range parts[1:] {
				if len(p) != 3 {
					allThreeDigits = false
					break
				}
			}
			if allThreeDigits {
				// Only 3-digit groups after the first dot -> thousand separators.
				s = strings.ReplaceAll(s, ".", "")
			}
		}
	}
	d, err := decimal.NewFromString(s)
	if err != nil {
		return nil
	}
	return &d
}

// strOrNil returns a bind value for a *string (nil = SQL NULL).
func strOrNil(p *string) any {
	if p == nil {
		return nil
	}
	return *p
}

// decOrNil returns a bind value for a *decimal.Decimal. Money travels as its
// text representation (never through float64), matching the rest of the worker.
func decOrNil(d *decimal.Decimal) any {
	if d == nil {
		return nil
	}
	return d.String()
}
