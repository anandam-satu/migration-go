package service

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// sqlPurchaseSource mirrors MigrationService.SQL_PURCHASE (legacy MySQL).
// Note: the original used '%%RB%%'; a single '%' has identical LIKE semantics.
const sqlPurchaseSource = `
	SELECT
		MAX(d.doc_date) as doc_date, d.doc_no, MAX(d.par_name) as par_name,
		MAX(dept.code) AS dep_code, MAX(dept.name) AS dep_name, m.code AS item_code, MAX(m.name) AS item_name,
		SUM(CASE WHEN d.doc_no LIKE '%RB%' THEN -t.qty_def ELSE t.qty_def END) AS qty_def,
		MAX(t.price) as price,
		SUM(CASE WHEN d.doc_no LIKE '%RB%' THEN -t.qty_def ELSE t.qty_def END * t.price) AS grand_total
	FROM dbtpurchasedoc d
	LEFT JOIN dbtpurchasetrans t ON d.id = t.doc_id
	LEFT JOIN dbmitem m ON t.ite_id = m.id
	LEFT JOIN dbmdepartment dept ON m.dep_id = dept.id
	GROUP BY d.doc_no, m.code
	ORDER BY doc_date DESC, MAX(d.id) DESC`

// MigratePurchase ports MigrationService.migratePurchaseData().
func (s *Service) MigratePurchase(ctx context.Context) string {
	log := s.logger
	syncTS := nowTimestamp()
	start := time.Now()
	log.Info("=== START MIGRASI PURCHASE (UPSERT) ===")
	s.logEstimate(ctx, sqlPurchaseSource, "PURCHASE")

	rows, err := s.mysql.QueryContext(ctx, sqlPurchaseSource)
	if err != nil {
		return s.fatal("Purchase", err)
	}
	defer rows.Close()

	const perRow = 11 // doc_date, doc_no_p, par_name, dep_code, dep_name, item_code, item_name, qty, price, grand_total, last_synced
	buffer := make([][]any, 0, batchSize)
	total := 0
	flush := func() error {
		if len(buffer) == 0 {
			return nil
		}
		total += len(buffer)
		if err := s.savePurchaseBatch(ctx, buffer); err != nil {
			return err
		}
		log.Info("Migrated", "table", "purchases", "total", total)
		buffer = buffer[:0]
		return nil
	}

	for rows.Next() {
		vals, ok := s.scanPurchaseRow(rows, syncTS)
		if !ok {
			continue // per-row warning already logged
		}
		buffer = append(buffer, vals)
		if len(buffer) >= batchSize {
			if err := flush(); err != nil {
				return s.fatal("Purchase", err)
			}
		}
	}
	if err := rows.Err(); err != nil {
		return s.fatal("Purchase", err)
	}
	if err := flush(); err != nil {
		return s.fatal("Purchase", err)
	}

	// Cleanup of stale rows from previous runs.
	log.Info("Cleaning up old Purchase data...")
	res, err := s.pg.ExecContext(ctx, "DELETE FROM purchases WHERE last_synced < $1", syncTS)
	if err != nil {
		return s.fatal("Purchase", err)
	}
	if n, err := res.RowsAffected(); err == nil {
		log.Info("Cleaned up stale Purchase records", "deleted", n)
	}
	return s.summary("Purchase", total, start)
}

func (s *Service) scanPurchaseRow(rows *sql.Rows, syncTS string) ([]any, bool) {
	var (
		docDate    sql.NullTime
		docNo      sql.NullString
		parName    sql.NullString
		depCode    sql.NullString
		depName    sql.NullString
		itemCode   sql.NullString
		itemName   sql.NullString
		qtyDef     sql.NullString // DECIMAL from SUM; Java rs.getInt truncates
		price      sql.NullString
		grandTotal sql.NullString
	)
	if err := rows.Scan(&docDate, &docNo, &parName, &depCode, &depName, &itemCode, &itemName,
		&qtyDef, &price, &grandTotal); err != nil {
		s.logger.Warn("Error processing Purchase row", "err", err)
		return nil, false
	}
	row := make([]any, 11)
	row[0] = dateString(docDate) // doc_date
	row[1] = nullStr(docNo)      // doc_no_p
	row[2] = nullStr(parName)    // par_name
	row[3] = nullStr(depCode)    // dep_code
	row[4] = nullStr(depName)    // dep_name
	row[5] = nullStr(itemCode)   // item_code
	row[6] = nullStr(itemName)   // item_name
	row[7] = qtyInt(qtyDef)      // qty (getInt on NULL yields 0 in Java)
	row[8] = nullStr(price)      // price
	row[9] = nullStr(grandTotal) // grand_total
	row[10] = syncTS             // last_synced
	return row, true
}

// savePurchaseBatch upserts up to batchSize purchase rows in one statement.
func (s *Service) savePurchaseBatch(ctx context.Context, rows [][]any) error {
	const perRow = 11
	var b strings.Builder
	b.WriteString(`INSERT INTO purchases (id, doc_date, doc_no_p, par_name, dep_code, dep_name, item_code, item_name, qty, price, grand_total, last_synced) VALUES `)
	for i := range rows {
		if i > 0 {
			b.WriteString(", ")
		}
		valuesTuple(&b, i, perRow, "purchase_seq")
	}
	b.WriteString(` ON CONFLICT (doc_no_p, item_code) DO UPDATE SET
		doc_date = EXCLUDED.doc_date,
		par_name = EXCLUDED.par_name,
		dep_code = EXCLUDED.dep_code,
		dep_name = EXCLUDED.dep_name,
		item_name = EXCLUDED.item_name,
		qty = EXCLUDED.qty,
		price = EXCLUDED.price,
		grand_total = EXCLUDED.grand_total,
		last_synced = EXCLUDED.last_synced`)
	_, err := s.pg.ExecContext(ctx, b.String(), flatten(rows)...)
	if err != nil {
		s.logger.Error("CRITICAL SQL ERROR in Purchase Migration (Schema Mismatch?)", "err", err)
		s.logger.Warn("Lakukan eksekusi script fix_migration_schema.sql di database PostgreSQL segera.")
	}
	return err
}

// fatal logs a critical error and returns the Java-style "ERROR: ..." result.
func (s *Service) fatal(job string, err error) string {
	s.logger.Error("CRITICAL ERROR during "+job+" Data Migration", "err", err, "cause", rootCause(err))
	return fmt.Sprintf("ERROR: %v", err)
}
