package service

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"strings"
	"time"
)

// MigrateSales ports MigrationService.migrateSalesData().
// Raw data is extracted from MySQL into PostgreSQL temp tables, then the
// joins, grouping and SUMs are computed in PostgreSQL. This avoids running
// a heavy 7-table Cartesian-prone query directly on MySQL (100k+ rows).
func (s *Service) MigrateSales(ctx context.Context) string {
	log := s.logger
	syncTS := nowTimestamp()
	start := time.Now()
	log.Info("=== START MIGRASI SALES (UPSERT) ===")

	tx, err := s.pg.BeginTx(ctx, nil)
	if err != nil {
		return s.fatal("Sales", err)
	}
	defer tx.Rollback()

	if err := createSalesTempTables(ctx, tx); err != nil {
		return s.fatal("Sales", err)
	}

	if err := loadSalesRawTables(ctx, s.mysql, tx); err != nil {
		return s.fatal("Sales", err)
	}

	rows, err := tx.QueryContext(ctx, sqlSalesAgg)
	if err != nil {
		return s.fatal("Sales", err)
	}

	// Collect all aggregation rows into memory first, then close rows,
	// before doing batch INSERTs. PostgreSQL doesn't allow nested queries
	// (iterating rows while inserting) on the same transaction connection.
	allRows := make([][]any, 0)
	for rows.Next() {
		vals, ok := s.scanSalesRow(rows, syncTS)
		if !ok {
			continue
		}
		allRows = append(allRows, vals)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return s.fatal("Sales", err)
	}

	const perRow = 19 // doc_date, doc_no, code, par_name, dep_code, dep_name, ite_code, item_name, qty, price, grand_total, hpp_satuan, total_hpp, laba_kotor, emp_code, emp_name, paid_total, status_bayar, last_synced
	var total int
	total = len(allRows)
	buffer := make([][]any, 0, batchSize)
	flush := func() error {
		if len(buffer) == 0 {
			return nil
		}
		if err := saveSalesBatchTx(ctx, tx, buffer); err != nil {
			return err
		}
		buffer = buffer[:0]
		return nil
	}

	for i, vals := range allRows {
		buffer = append(buffer, vals)
		if len(buffer) >= batchSize || i == len(allRows)-1 {
			if err := flush(); err != nil {
				return s.fatal("Sales", err)
			}
			log.Info("Sales Migrated", "total", i+1)
		}
	}

	log.Info("Cleaning up old Sales data...")
	res, err := tx.ExecContext(ctx, "DELETE FROM sales WHERE last_synced < $1", syncTS)
	if err != nil {
		return s.fatal("Sales", err)
	}
	if n, err := res.RowsAffected(); err == nil {
		log.Info("Cleaned up stale Sales records", "deleted", n)
	}

	if err := tx.Commit(); err != nil {
		return s.fatal("Sales", err)
	}
	return s.summary("SALES", total, start)
}

// sqlSalesAgg runs the aggregation in PostgreSQL on staged temp tables.
const sqlSalesAgg = `
	SELECT DISTINCT ON (d.doc_no, t.ite_name)
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
			WHEN d.paid_total::numeric > 0 THEN 'PAID'
			ELSE 'UNPAID'
		END AS status_bayar
	FROM tmp_salesdoc d
	INNER JOIN (
		SELECT
			doc_id,
			ite_id,
			ite_name,
			SUM(qty_def::numeric) AS qty,
			SUM((qty_def::numeric) * (price::numeric)) AS grand_total,
			MAX(price::numeric) AS price
		FROM tmp_salestrans
		GROUP BY doc_id, ite_id, ite_name
	) t ON t.doc_id = d.id
	LEFT JOIN tmp_employee e ON e.id = d.emp_id
	LEFT JOIN tmp_partner p ON p.id = d.par_id
	LEFT JOIN tmp_item i ON i.id = t.ite_id
	LEFT JOIN tmp_department dep ON dep.id = i.dep_id
	LEFT JOIN (
		SELECT doc_id, ite_id, MAX(price_avg::numeric) AS price_avg
		FROM tmp_stockavg
		WHERE doc_type IN (32, 33) AND avg_type = 1
		GROUP BY doc_id, ite_id
	) sa ON sa.doc_id = d.id AND sa.ite_id = t.ite_id
	ORDER BY d.doc_no, t.ite_name, d.doc_date DESC, d.id DESC`

// createSalesTempTables stages empty temp tables in PostgreSQL.
func createSalesTempTables(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `
		DROP TABLE IF EXISTS tmp_salesdoc;
		CREATE TEMP TABLE tmp_salesdoc (
			id BIGINT,
			doc_date DATE,
			doc_no TEXT,
			emp_id BIGINT,
			par_id BIGINT,
			paid_total TEXT,
			par_name TEXT
		);
		DROP TABLE IF EXISTS tmp_salestrans;
		CREATE TEMP TABLE tmp_salestrans (
			doc_id BIGINT,
			ite_id BIGINT,
			ite_name TEXT,
			qty_def TEXT,
			price TEXT
		);
		DROP TABLE IF EXISTS tmp_employee;
		CREATE TEMP TABLE tmp_employee (
			id BIGINT,
			code TEXT,
			name TEXT
		);
		DROP TABLE IF EXISTS tmp_partner;
		CREATE TEMP TABLE tmp_partner (
			id BIGINT,
			code TEXT
		);
		DROP TABLE IF EXISTS tmp_item;
		CREATE TEMP TABLE tmp_item (
			id BIGINT,
			code TEXT,
			dep_id BIGINT
		);
		DROP TABLE IF EXISTS tmp_department;
		CREATE TEMP TABLE tmp_department (
			id BIGINT,
			code TEXT,
			name TEXT
		);
		DROP TABLE IF EXISTS tmp_stockavg;
		CREATE TEMP TABLE tmp_stockavg (
			doc_id BIGINT,
			ite_id BIGINT,
			price_avg TEXT,
			doc_type INT,
			avg_type INT
		);
	`)
	return err
}

// loadSalesRawTables streams raw rows from MySQL into PostgreSQL temp tables.
func loadSalesRawTables(ctx context.Context, mysql *sql.DB, tx *sql.Tx) error {
	jobs := []struct {
		query string
		cols  []string
		table string
		scan  func(rows *sql.Rows) ([]any, error)
	}{
		{
			query: `SELECT id, doc_date, doc_no, emp_id, par_id, paid_total, par_name FROM anandamid26.dbtsalesdoc`,
			cols:  []string{"id", "doc_date", "doc_no", "emp_id", "par_id", "paid_total", "par_name"},
			table: "tmp_salesdoc",
			scan: func(rows *sql.Rows) ([]any, error) {
				var (
					id        sql.NullInt64
					docDate   sql.NullTime
					docNo     sql.NullString
					empID     sql.NullInt64
					parID     sql.NullInt64
					paidTotal sql.NullString
					parName   sql.NullString
				)
				if err := rows.Scan(&id, &docDate, &docNo, &empID, &parID, &paidTotal, &parName); err != nil {
					return nil, err
				}
				return []any{id, docDate, docNo, empID, parID, paidTotal, parName}, nil
			},
		},
		{
			query: `SELECT doc_id, ite_id, ite_name, qty_def, price FROM anandamid26.dbtsalestrans`,
			cols:  []string{"doc_id", "ite_id", "ite_name", "qty_def", "price"},
			table: "tmp_salestrans",
			scan: func(rows *sql.Rows) ([]any, error) {
				var (
					docID   sql.NullInt64
					iteID   sql.NullInt64
					iteName sql.NullString
					qtyDef  sql.NullString
					price   sql.NullString
				)
				if err := rows.Scan(&docID, &iteID, &iteName, &qtyDef, &price); err != nil {
					return nil, err
				}
				return []any{docID, iteID, iteName, qtyDef, price}, nil
			},
		},
		{
			query: `SELECT id, code, name FROM dbmemployee`,
			cols:  []string{"id", "code", "name"},
			table: "tmp_employee",
			scan: func(rows *sql.Rows) ([]any, error) {
				var (
					id   sql.NullInt64
					code sql.NullString
					name sql.NullString
				)
				if err := rows.Scan(&id, &code, &name); err != nil {
					return nil, err
				}
				return []any{id, code, name}, nil
			},
		},
		{
			query: `SELECT id, code FROM dbmpartner`,
			cols:  []string{"id", "code"},
			table: "tmp_partner",
			scan: func(rows *sql.Rows) ([]any, error) {
				var (
					id   sql.NullInt64
					code sql.NullString
				)
				if err := rows.Scan(&id, &code); err != nil {
					return nil, err
				}
				return []any{id, code}, nil
			},
		},
		{
			query: `SELECT id, code, dep_id FROM anandamid26.dbmitem`,
			cols:  []string{"id", "code", "dep_id"},
			table: "tmp_item",
			scan: func(rows *sql.Rows) ([]any, error) {
				var (
					id    sql.NullInt64
					code  sql.NullString
					depID sql.NullInt64
				)
				if err := rows.Scan(&id, &code, &depID); err != nil {
					return nil, err
				}
				return []any{id, code, depID}, nil
			},
		},
		{
			query: `SELECT id, code, name FROM anandamid26.dbmdepartment`,
			cols:  []string{"id", "code", "name"},
			table: "tmp_department",
			scan: func(rows *sql.Rows) ([]any, error) {
				var (
					id   sql.NullInt64
					code sql.NullString
					name sql.NullString
				)
				if err := rows.Scan(&id, &code, &name); err != nil {
					return nil, err
				}
				return []any{id, code, name}, nil
			},
		},
		{
			query: `SELECT doc_id, ite_id, price_avg, doc_type, avg_type FROM anandamid26.dbtstockavg`,
			cols:  []string{"doc_id", "ite_id", "price_avg", "doc_type", "avg_type"},
			table: "tmp_stockavg",
			scan: func(rows *sql.Rows) ([]any, error) {
				var (
					docID    sql.NullInt64
					iteID    sql.NullInt64
					priceAvg sql.NullString
					docType  sql.NullInt64
					avgType  sql.NullInt64
				)
				if err := rows.Scan(&docID, &iteID, &priceAvg, &docType, &avgType); err != nil {
					return nil, err
				}
				return []any{docID, iteID, priceAvg, docType, avgType}, nil
			},
		},
	}

	for _, job := range jobs {
		if err := loadRawTable(ctx, mysql, tx, job.query, job.cols, job.table, job.scan); err != nil {
			return err
		}
	}
	return nil
}

// loadRawTable streams MySQL rows and batch-inserts them into a PostgreSQL temp table.
func loadRawTable(ctx context.Context, mysql *sql.DB, tx *sql.Tx, query string, cols []string, table string, scan func(*sql.Rows) ([]any, error)) error {
	rows, err := mysql.QueryContext(ctx, query)
	if err != nil {
		return err
	}
	defer rows.Close()

	insertSQL := fmt.Sprintf("INSERT INTO %s (%s)", table, strings.Join(cols, ", "))

	batch := make([][]any, 0, batchSize)
	for rows.Next() {
		vals, err := scan(rows)
		if err != nil {
			return err
		}
		batch = append(batch, vals)
		if len(batch) >= batchSize {
			if err := execBatchTx(tx, insertSQL, batch); err != nil {
				return err
			}
			batch = batch[:0]
		}
	}
	if len(batch) > 0 {
		if err := execBatchTx(tx, insertSQL, batch); err != nil {
			return err
		}
	}
	return rows.Err()
}

// execBatchTx executes a multi-row INSERT via a PostgreSQL transaction.
func execBatchTx(tx *sql.Tx, query string, rows [][]any) error {
	if len(rows) == 0 {
		return nil
	}
	perRow := len(rows[0])
	var b strings.Builder
	b.WriteString(query)
	b.WriteString(" VALUES ")
	for i := range rows {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString("(")
		for c := 0; c < perRow; c++ {
			if c > 0 {
				b.WriteString(", ")
			}
			b.WriteString(fmt.Sprintf("$%d", i*perRow+c+1))
		}
		b.WriteString(")")
	}
	var args []any
	for _, r := range rows {
		args = append(args, r...)
	}
	_, err := tx.ExecContext(context.Background(), b.String(), args...)
	return err
}

// saveSalesBatchTx upserts sales rows using an active PostgreSQL transaction.
func saveSalesBatchTx(ctx context.Context, tx *sql.Tx, rows [][]any) error {
	const perRow = 19
	var b strings.Builder
	b.WriteString(`INSERT INTO sales (id, doc_date, doc_no, code, par_name, dep_code, dep_name, ite_code, item_name, qty, price, grand_total, hpp_satuan, total_hpp, laba_kotor, emp_code, emp_name, paid_total, status_bayar, last_synced) VALUES `)
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
		paid_total = EXCLUDED.paid_total,
		status_bayar = EXCLUDED.status_bayar,
		last_synced = EXCLUDED.last_synced`)
	_, err := tx.ExecContext(ctx, b.String(), flatten(rows)...)
	if err != nil {
		slog.Error("CRITICAL SQL ERROR in Sales Migration (Schema Mismatch?)", "err", err)
		slog.Warn("Pastikan UNIQUE CONSTRAINT (doc_no, item_name) tersedia di tabel sales.")
	}
	return err
}

// scanSalesRow maps a PostgreSQL aggregation row to a sales insert row.
func (s *Service) scanSalesRow(rows *sql.Rows, syncTS string) ([]any, bool) {
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
	if err := rows.Scan(&docDate, &docNo, &code, &depCode, &depName, &parName, &iteCode, &iteName,
		&qtyDef, &price, &grandTotal, &hppSatuan, &totalHpp, &labaKotor, &empCode, &empName,
		&paidTotal, &statusBayar); err != nil {
		s.logger.Warn("Error processing Sales row", "err", err)
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
