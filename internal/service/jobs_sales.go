package service

import (
	"context"
	"database/sql"
	"strings"
	"time"
)

// sqlSalesSource retrieves aggregated sales data from the legacy MySQL
// database. Do not add a trailing semicolon: logEstimate may wrap this
// query in a derived table for COUNT(*).
const sqlSalesSource = `
SELECT
    d.doc_date,
    d.doc_no,
    p.code,
    dep.code AS dep_code,
    dep.name AS dep_name,
    d.par_name,
    i.code AS ite_code,
    t.ite_name,

    CASE
        WHEN d.doc_no LIKE '%RJ%' THEN -t.qty
        ELSE t.qty
    END AS qty_def,

    t.price,

    CASE
        WHEN d.doc_no LIKE '%RJ%' THEN -t.grand_total
        ELSE t.grand_total
    END AS grand_total,

    COALESCE(sa.price_avg, t.price) AS hpp_satuan,

    (
        CASE
            WHEN d.doc_no LIKE '%RJ%' THEN -t.qty
            ELSE t.qty
        END
    ) * COALESCE(sa.price_avg, t.price) AS total_hpp,

    (
        CASE
            WHEN d.doc_no LIKE '%RJ%' THEN -t.grand_total
            ELSE t.grand_total
        END
    ) -
    (
        CASE
            WHEN d.doc_no LIKE '%RJ%' THEN -t.qty
            ELSE t.qty
        END
    ) * COALESCE(sa.price_avg, t.price) AS laba_kotor,

    e.code AS emp_code,
    e.name AS emp_name,
    d.paid_total,

    CASE
        WHEN d.paid_total > 0 THEN 'PAID'
        ELSE 'UNPAID'
    END AS status_bayar

FROM anandamid26.dbtsalesdoc d

INNER JOIN (
    SELECT
        doc_id,
        ite_id,
        ite_name,
        SUM(qty_def) AS qty,
        SUM(qty_def * price) AS grand_total,
        MAX(price) AS price
    FROM anandamid26.dbtsalestrans
    GROUP BY
        doc_id,
        ite_id,
        ite_name
) t
    ON t.doc_id = d.id

LEFT JOIN dbmemployee e
    ON e.id = d.emp_id

LEFT JOIN dbmpartner p
    ON p.id = d.par_id

LEFT JOIN anandamid26.dbmitem i
    ON i.id = t.ite_id

LEFT JOIN anandamid26.dbmdepartment dep
    ON dep.id = i.dep_id

LEFT JOIN (
    SELECT
        doc_id,
        ite_id,
        MAX(price_avg) AS price_avg
    FROM anandamid26.dbtstockavg
    WHERE doc_type IN (32, 33)
      AND avg_type = 1
    GROUP BY
        doc_id,
        ite_id
) sa
    ON sa.doc_id = d.id
   AND sa.ite_id = t.ite_id

ORDER BY
    d.doc_date DESC,
    d.id DESC
`

// MigrateSales migrates sales data from MySQL to PostgreSQL.
// Existing sales rows are updated through UPSERT.
func (s *Service) MigrateSales(ctx context.Context) string {
	log := s.logger
	syncTS := nowTimestamp()
	start := time.Now()

	log.Info("=== START MIGRASI SALES (UPSERT) ===")

	// The SQL source intentionally has no trailing semicolon.
	s.logEstimate(ctx, sqlSalesSource, "SALES")

	rows, err := s.mysql.QueryContext(ctx, sqlSalesSource)
	if err != nil {
		return s.fatal("Sales", err)
	}
	defer rows.Close()

	buffer := make([][]any, 0, batchSize)
	total := 0

	flush := func() error {
		if len(buffer) == 0 {
			return nil
		}

		if err := s.saveSalesBatch(ctx, buffer); err != nil {
			return err
		}

		total += len(buffer)
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

	// Delete records not encountered in this successful migration.
	// Only execute this after all source rows have been processed.
	log.Info("Cleaning up old Sales data...")

	res, err := s.pg.ExecContext(
		ctx,
		`DELETE FROM sales WHERE last_synced < $1`,
		syncTS,
	)
	if err != nil {
		return s.fatal("Sales", err)
	}

	if n, err := res.RowsAffected(); err == nil {
		log.Info("Cleaned up stale Sales records", "deleted", n)
	}

	return s.summary("SALES", total, start)
}

// scanSalesRow scans the 18 columns returned by sqlSalesSource
// and appends last_synced as the 19th PostgreSQL value.
func (s *Service) scanSalesRow(
	rows *sql.Rows,
	syncTS string,
) ([]any, bool) {
	var (
		docDate     sql.NullTime
		docNo       sql.NullString
		code        sql.NullString
		depCode     sql.NullString
		depName     sql.NullString
		parName     sql.NullString
		iteCode     sql.NullString
		iteName     sql.NullString
		qtyDef      sql.NullString
		price       sql.NullString
		grandTotal  sql.NullString
		hppSatuan   sql.NullString
		totalHpp    sql.NullString
		labaKotor   sql.NullString
		empCode     sql.NullString
		empName     sql.NullString
		paidTotal   sql.NullString
		statusBayar sql.NullString
	)

	err := rows.Scan(
		&docDate,
		&docNo,
		&code,
		&depCode,
		&depName,
		&parName,
		&iteCode,
		&iteName,
		&qtyDef,
		&price,
		&grandTotal,
		&hppSatuan,
		&totalHpp,
		&labaKotor,
		&empCode,
		&empName,
		&paidTotal,
		&statusBayar,
	)
	if err != nil {
		s.logger.Warn(
			"Error processing Sales row",
			"err", err,
		)
		return nil, false
	}

	row := make([]any, 19)

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
	row[16] = nullStr(paidTotal)
	row[17] = nullStr(statusBayar)
	row[18] = syncTS

	return row, true
}

// saveSalesBatch upserts sales rows in a single PostgreSQL statement.
func (s *Service) saveSalesBatch(
	ctx context.Context,
	rows [][]any,
) error {
	if len(rows) == 0 {
		return nil
	}

	const perRow = 19

	var b strings.Builder

	b.WriteString(`
INSERT INTO sales (
    id,
    doc_date,
    doc_no,
    code,
    par_name,
    dep_code,
    dep_name,
    ite_code,
    item_name,
    qty,
    price,
    grand_total,
    hpp_satuan,
    total_hpp,
    laba_kotor,
    emp_code,
    emp_name,
    paid_total,
    status_bayar,
    last_synced
) VALUES `)

	for i := range rows {
		if len(rows[i]) != perRow {
			returnErr := len(rows[i])
			s.logger.Error(
				"Invalid Sales row length",
				"expected", perRow,
				"actual", returnErr,
				"row", i,
			)
			return sql.ErrNoRows
		}

		if i > 0 {
			b.WriteString(", ")
		}

		valuesTuple(&b, i, perRow, "sales_seq")
	}

	b.WriteString(`
ON CONFLICT (doc_no, item_name) DO UPDATE SET
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
    paid_total = EXCLUDED.paid_total,
    status_bayar = EXCLUDED.status_bayar,
    last_synced = EXCLUDED.last_synced
`)

	_, err := s.pg.ExecContext(
		ctx,
		b.String(),
		flatten(rows)...,
	)
	if err != nil {
		s.logger.Error(
			"CRITICAL SQL ERROR in Sales Migration",
			"err", err,
		)
		s.logger.Warn(
			"Check the PostgreSQL sales schema, sequence, and UNIQUE constraint (doc_no, item_name).",
		)
	}

	return err
}
