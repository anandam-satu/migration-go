package service

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// sqlSNSource mirrors MigrationService.migrateSnData()'s combined source query.
const sqlSNSource = `
	SELECT MAX(p.doc_date) AS tanggal, p.doc_no AS doc_id, MAX(p.par_name) AS user_name, MAX(m.name) AS item_name, sn.sn, 'MASUK' as type
	FROM dbtitemsn sn
	LEFT JOIN dbmitem m ON sn.ite_id = m.id
	LEFT JOIN dbtpurchasedoc p ON sn.doc_id = p.id AND sn.doc_type = p.doc_type
	WHERE sn.doc_type IN (42,43,44) AND sn.sn IS NOT NULL AND TRIM(sn.sn) <> ''
	GROUP BY p.doc_no, sn.sn
	UNION ALL
	SELECT MAX(s.doc_date) AS tanggal, s.doc_no AS doc_id, MAX(s.par_name) AS user_name, MAX(m.name) AS item_name, sn.sn, 'KELUAR' as type
	FROM dbtitemsn sn
	LEFT JOIN dbmitem m ON sn.ite_id = m.id
	LEFT JOIN dbtsalesdoc s ON sn.doc_id = s.id AND sn.doc_type = s.doc_type
	WHERE sn.doc_type IN (32,33) AND sn.sn IS NOT NULL AND TRIM(sn.sn) <> ''
	GROUP BY s.doc_no, sn.sn`

// MigrateSN ports MigrationService.migrateSnData().
func (s *Service) MigrateSN(ctx context.Context) string {
	log := s.logger
	syncTS := nowTimestamp()
	start := time.Now()
	log.Info("=== START MIGRASI SERIAL NUMBER (UPSERT) ===")

	rows, err := s.mysql.QueryContext(ctx, sqlSNSource)
	if err != nil {
		return s.fatal("SN", err)
	}
	defer rows.Close()

	const perRow = 7 // tanggal, doc_id, user_name, item_name, sn, type, last_synced
	buffer := make([][]any, 0, batchSize)
	total := 0
	flush := func() error {
		if len(buffer) == 0 {
			return nil
		}
		total += len(buffer)
		if err := s.saveSnBatch(ctx, buffer); err != nil {
			return err
		}
		buffer = buffer[:0]
		return nil
	}

	for rows.Next() {
		vals, ok := s.scanSNRow(rows, syncTS)
		if !ok {
			continue
		}
		buffer = append(buffer, vals)
		if len(buffer) >= batchSize {
			if err := flush(); err != nil {
				return s.fatal("SN", err)
			}
		}
	}
	if err := rows.Err(); err != nil {
		return s.fatal("SN", err)
	}
	if err := flush(); err != nil {
		return s.fatal("SN", err)
	}

	log.Info("Cleaning up old SN data...")
	res, err := s.pg.ExecContext(ctx, "DELETE FROM item_serial_numbers WHERE last_synced < $1", syncTS)
	if err != nil {
		return s.fatal("SN", err)
	}
	if n, err := res.RowsAffected(); err == nil {
		log.Info("Cleaned up stale SN records", "deleted", n)
	}
	return s.summary("SN", total, start)
}

func (s *Service) scanSNRow(rows *sql.Rows, syncTS string) ([]any, bool) {
	var (
		tanggal  sql.NullTime
		docID    sql.NullString
		userName sql.NullString
		itemName sql.NullString
		sn       sql.NullString
		typ      sql.NullString
	)
	if err := rows.Scan(&tanggal, &docID, &userName, &itemName, &sn, &typ); err != nil {
		s.logger.Warn("Error processing SN row", "err", err)
		return nil, false
	}
	row := make([]any, 7)
	row[0] = dateString(tanggal) // MAX(p.doc_date) is a DATE column
	row[1] = nullStr(docID)
	row[2] = nullStr(userName)
	row[3] = nullStr(itemName)
	row[4] = nullStr(sn)
	row[5] = nullStr(typ)
	row[6] = syncTS
	return row, true
}

// saveSnBatch upserts up to batchSize SN rows in one statement.
func (s *Service) saveSnBatch(ctx context.Context, rows [][]any) error {
	const perRow = 7
	var b strings.Builder
	b.WriteString(`INSERT INTO item_serial_numbers (tanggal, doc_id, user_name, item_name, sn, type, last_synced) VALUES `)
	for i := range rows {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString("(")
		for c := 0; c < perRow; c++ {
			if c > 0 {
				b.WriteString(", ")
			}
			fmt.Fprintf(&b, "$%d", i*perRow+c+1)
		}
		b.WriteString(")")
	}
	b.WriteString(` ON CONFLICT (sn, doc_id, type) DO UPDATE SET
		tanggal = EXCLUDED.tanggal,
		user_name = EXCLUDED.user_name,
		item_name = EXCLUDED.item_name,
		last_synced = EXCLUDED.last_synced`)
	_, err := s.pg.ExecContext(ctx, b.String(), flatten(rows)...)
	if err != nil {
		s.logger.Error("CRITICAL SQL ERROR in SN Migration (Schema Mismatch?)", "err", err)
		s.logger.Warn("Pastikan UNIQUE CONSTRAINT (sn, doc_id, type) tersedia di tabel item_serial_numbers.")
	}
	return err
}
