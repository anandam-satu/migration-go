package service

import (
	"context"
	"database/sql"
	"strings"
	"time"
)

// sqlSalesSource mirrors MigrationService.SQL_SALES (legacy MySQL). The
// anandamid26. prefixes are kept verbatim, exactly as in the Java source.
const sqlSalesSource = `
	SELECT
		MAX(d.doc_date) AS doc_date,
		d.doc_no,
		MAX(p.code) AS code,
		MAX(dep.code) AS dep_code,
		MAX(dep.name) AS dep_name,
		MAX(d.par_name) AS par_name,
		MAX(i.code) AS ite_code,
		t.ite_name,
		SUM(
			CASE
				WHEN d.doc_no LIKE '%RJ%' THEN -t.qty_def
				ELSE t.qty_def
			END
		) AS qty_def,
		MAX(t.price) AS price,
		SUM(
			(
				CASE
					WHEN d.doc_no LIKE '%RJ%' THEN -t.qty_def
					ELSE t.qty_def
				END * t.price
			)
		) AS grand_total,
		COALESCE(
			MAX(sa.price_avg),
			MAX(t.price)
		) AS hpp_satuan,
		SUM(
			CASE
				WHEN d.doc_no LIKE '%RJ%' THEN -t.qty_def
				ELSE t.qty_def
			END
		) * COALESCE(MAX(sa.price_avg), MAX(t.price)) AS total_hpp,
		SUM(
			(
				CASE
					WHEN d.doc_no LIKE '%RJ%' THEN -t.qty_def
					ELSE t.qty_def
				END * t.price
			)
		)
		-
		SUM(
			CASE
				WHEN d.doc_no LIKE '%RJ%' THEN -t.qty_def
				ELSE t.qty_def
			END
		) * COALESCE(MAX(sa.price_avg), MAX(t.price)) AS laba_kotor,
		MAX(e.code) AS emp_code,
		MAX(e.name) AS emp_name
	FROM anandamid26.dbtsalesdoc d
	LEFT JOIN anandamid26.dbtsalestrans t ON d.id = t.doc_id
	LEFT JOIN dbmemployee e ON d.emp_id = e.id
	LEFT JOIN dbmpartner p ON d.par_id = p.id
	LEFT JOIN anandamid26.dbmitem i ON t.ite_id = i.id
	LEFT JOIN anandamid26.dbmdepartment dep ON i.dep_id = dep.id
	LEFT JOIN anandamid26.dbtstockavg sa
		ON sa.doc_id = d.id
		AND sa.ite_id = t.ite_id
		AND sa.doc_type IN (32, 33)
		AND sa.avg_type = 1
	GROUP BY
		d.doc_no,
		t.ite_name
	ORDER BY
		doc_date DESC,
		MAX(d.id) DESC`

// MigrateSales ports MigrationService.migrateSalesData().
func (s *Service) MigrateSales(ctx context.Context) string {
	log := s.logger
	syncTS := nowTimestamp()
	start := time.Now()
	log.Info("=== START MIGRASI SALES (UPSERT) ===")
	s.logEstimate(ctx, sqlSalesSource, "SALES")

	rows, err := s.mysql.QueryContext(ctx, sqlSalesSource)
	if err != nil {
		return s.fatal("Sales", err)
	}
	defer rows.Close()

	const perRow = 17 // doc_date, doc_no, code, par_name, dep_code, dep_name, ite_code, item_name, qty, price, grand_total, hpp_satuan, total_hpp, laba_kotor, emp_code, emp_name, last_synced
	buffer := make([][]any, 0, batchSize)
	total := 0
	flush := func() error {
		if len(buffer) == 0 {
			return nil
		}
		total += len(buffer)
		if err := s.saveSalesBatch(ctx, buffer); err != nil {
			return err
		}
		log.Info("Sales Migrated", "total", total)
		buffer = buffer[:0]
		return nil
	}

	for rows.Next() {
		vals, ok := s.scanSalesRow(rows, syncTS)
		if !ok {
			continue
		}
		buffer = append(buffer, vals)
		if len(buffer) >= batchSize {
			if err := flush(); err != nil {
				return s.fatal("Sales", err)
			}
		}
	}
	if err := rows.Err(); err != nil {
		return s.fatal("Sales", err)
	}
	if err := flush(); err != nil {
		return s.fatal("Sales", err)
	}

	log.Info("Cleaning up old Sales data...")
	res, err := s.pg.ExecContext(ctx, "DELETE FROM sales WHERE last_synced < $1", syncTS)
	if err != nil {
		return s.fatal("Sales", err)
	}
	if n, err := res.RowsAffected(); err == nil {
		log.Info("Cleaned up stale Sales records", "deleted", n)
	}
	return s.summary("SALES", total, start)
}

func (s *Service) scanSalesRow(rows *sql.Rows, syncTS string) ([]any, bool) {
	var (
		docDate    sql.NullTime
		docNo      sql.NullString
		code       sql.NullString
		depCode    sql.NullString
		depName    sql.NullString
		parName    sql.NullString
		iteCode    sql.NullString
		iteName    sql.NullString
		qtyDef     sql.NullString // DECIMAL from SUM; Java rs.getInt truncates
		price      sql.NullString
		grandTotal sql.NullString
		hppSatuan  sql.NullString
		totalHpp   sql.NullString
		labaKotor  sql.NullString
		empCode    sql.NullString
		empName    sql.NullString
	)
	if err := rows.Scan(&docDate, &docNo, &code, &depCode, &depName, &parName, &iteCode, &iteName,
		&qtyDef, &price, &grandTotal, &hppSatuan, &totalHpp, &labaKotor, &empCode, &empName); err != nil {
		s.logger.Warn("Error processing Sales row", "err", err)
		return nil, false
	}
	row := make([]any, 17)
	row[0] = dateString(docDate)
	row[1] = nullStr(docNo)
	row[2] = nullStr(code)
	row[3] = nullStr(parName)
	row[4] = nullStr(depCode)
	row[5] = nullStr(depName)
	row[6] = nullStr(iteCode)
	row[7] = nullStr(iteName)
	row[8] = qtyInt(qtyDef)
	row[9] = nullStr(price)
	row[10] = nullStr(grandTotal)
	row[11] = nullStr(hppSatuan)
	row[12] = nullStr(totalHpp)
	row[13] = nullStr(labaKotor)
	row[14] = nullStr(empCode)
	row[15] = nullStr(empName)
	row[16] = syncTS
	return row, true
}

// saveSalesBatch upserts up to batchSize sales rows in one statement.
func (s *Service) saveSalesBatch(ctx context.Context, rows [][]any) error {
	const perRow = 17
	var b strings.Builder
	b.WriteString(`INSERT INTO sales (id, doc_date, doc_no, code, par_name, dep_code, dep_name, ite_code, item_name, qty, price, grand_total, hpp_satuan, total_hpp, laba_kotor, emp_code, emp_name, last_synced) VALUES `)
	for i := range rows {
		if i > 0 {
			b.WriteString(", ")
		}
		valuesTuple(&b, i, perRow, "sales_seq")
	}
	b.WriteString(` ON CONFLICT (doc_no, item_name) DO UPDATE SET
		doc_date = EXCLUDED.doc_date,
		code = EXCLUDED.code,
		par_name = EXCLUDED.par_name,
		dep_code = EXCLUDED.dep_code,
		dep_name = EXCLUDED.dep_name,
		ite_code = EXCLUDED.ite_code,
		qty = EXCLUDED.qty,
		price = EXCLUDED.price,
		grand_total = EXCLUDED.grand_total,
		hpp_satuan = EXCLUDED.hpp_satuan,
		total_hpp = EXCLUDED.total_hpp,
		laba_kotor = EXCLUDED.laba_kotor,
		emp_code = EXCLUDED.emp_code,
		emp_name = EXCLUDED.emp_name,
		last_synced = EXCLUDED.last_synced`)
	_, err := s.pg.ExecContext(ctx, b.String(), flatten(rows)...)
	if err != nil {
		s.logger.Error("CRITICAL SQL ERROR in Sales Migration (Schema Mismatch?)", "err", err)
		s.logger.Warn("Pastikan UNIQUE CONSTRAINT (doc_no, item_name) tersedia di tabel sales.")
	}
	return err
}
