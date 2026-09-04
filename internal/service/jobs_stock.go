package service

import (
	"context"
	"database/sql"
	"strings"
	"time"

	"github.com/anandam/api-migration/internal/normalize"
)

// sqlStockSource mirrors MigrationService.SQL_STOCK (legacy MySQL).
const sqlStockSource = `
	SELECT
		s.item_code,
		s.item_name,
		SUBSTRING_INDEX(s.item_code, ' ', 1) AS kategori_itemcode,
		SUBSTRING_INDEX(s.item_name, ' ', 1) AS kategori_nama,
		s.final_stock,
		COALESCE(h.price_avg, 0) AS harga_hpp,
		(s.final_stock * COALESCE(h.price_avg, 0)) AS grand_total,
		s.warehouse_name
	FROM (
		SELECT
			combined.item_code,
			combined.item_name,
			SUM(combined.qty_movement) AS final_stock,
			w.name AS warehouse_name
		FROM (
			SELECT
				d.war_id,
				COALESCE(m.code, NULLIF(TRIM(t.ite_code),''), TRIM(t.ite_name)) AS item_code,
				COALESCE(m.name, TRIM(t.ite_name)) AS item_name,
				CASE UPPER(LEFT(TRIM(d.doc_no),2))
					WHEN 'BL' THEN COALESCE(t.qty_def,0)
					WHEN 'RB' THEN -COALESCE(t.qty_def,0)
					WHEN 'KM' THEN COALESCE(t.qty_def,0)
					WHEN 'KK' THEN COALESCE(t.qty_def,0)
					ELSE 0
				END AS qty_movement
			FROM anandamid26.dbtpurchasedoc d
			LEFT JOIN anandamid26.dbtpurchasetrans t ON d.id = t.doc_id
			LEFT JOIN anandamid26.dbmitem m ON t.ite_id = m.id

			UNION ALL

			SELECT
				d.war_id,
				COALESCE(m.code, NULLIF(TRIM(t.ite_code),''), TRIM(t.ite_name)) AS item_code,
				COALESCE(m.name, TRIM(t.ite_name)) AS item_name,
				CASE UPPER(LEFT(TRIM(d.doc_no),2))
					WHEN 'II' THEN COALESCE(t.qty_def,0)
					WHEN 'IO' THEN -COALESCE(t.qty_def,0)
					WHEN 'KM' THEN COALESCE(t.qty_def,0)
					WHEN 'KK' THEN COALESCE(t.qty_def,0)
					ELSE 0
				END AS qty_movement
			FROM anandamid26.dbtitemtransferdoc d
			LEFT JOIN anandamid26.dbtitemtransfertrans t ON d.id = t.doc_id
			LEFT JOIN anandamid26.dbmitem m ON t.ite_id = m.id

			UNION ALL

			SELECT
				d.war_id,
				COALESCE(m.code, NULLIF(TRIM(t.ite_code),''), TRIM(t.ite_name)) AS item_code,
				COALESCE(m.name, TRIM(t.ite_name)) AS item_name,
				CASE UPPER(LEFT(TRIM(d.doc_no),2))
					WHEN 'JL' THEN -COALESCE(t.qty_def,0)
					WHEN 'RJ' THEN COALESCE(t.qty_def,0)
					WHEN 'KM' THEN COALESCE(t.qty_def,0)
					WHEN 'KK' THEN COALESCE(t.qty_def,0)
					ELSE 0
				END AS qty_movement
			FROM anandamid26.dbtsalesdoc d
			LEFT JOIN anandamid26.dbtsalestrans t ON d.id = t.doc_id
			LEFT JOIN anandamid26.dbmitem m ON t.ite_id = m.id
		) AS combined
		LEFT JOIN anandamid26.dbmwarehouse w ON combined.war_id = w.id
		WHERE w.name IS NOT NULL
		AND TRIM(w.name) <> ''
		GROUP BY
			w.id,
			w.name,
			combined.item_code,
			combined.item_name
		HAVING SUM(combined.qty_movement) > 0
	) AS s

	LEFT JOIN (
		SELECT
			i.code AS item_code,
			sa.price_avg
		FROM anandamid26.dbtstockavg sa
		JOIN anandamid26.dbmitem i ON sa.ite_id = i.id
		JOIN (
			SELECT
				ite_id,
				MAX(id) AS max_id
			FROM anandamid26.dbtstockavg
			GROUP BY ite_id
		) last_sa
			ON sa.ite_id = last_sa.ite_id
			AND sa.id = last_sa.max_id
	) AS h
		ON s.item_code = h.item_code

	ORDER BY s.item_name`

// MigrateStock ports MigrationService.migrateStockData().
func (s *Service) MigrateStock(ctx context.Context) string {
	log := s.logger
	syncTS := nowTimestamp()
	start := time.Now()
	log.Info("=== START MIGRASI STOK (UPSERT) ===")

	rows, err := s.mysql.QueryContext(ctx, sqlStockSource)
	if err != nil {
		return s.fatal("Stock", err)
	}
	defer rows.Close()

	const perRow = 10 // item_code, item_name, kategori_nama, kategori_itemcode, final_stok, harga_hpp, grand_total, warehouse, last_synced, normalized_item_name
	buffer := make([][]any, 0, batchSize)
	total := 0
	flush := func() error {
		if len(buffer) == 0 {
			return nil
		}
		total += len(buffer)
		if err := s.saveStockBatch(ctx, buffer); err != nil {
			return err
		}
		log.Info("Stock Migrated", "total", total)
		buffer = buffer[:0]
		return nil
	}

	for rows.Next() {
		vals, ok := s.scanStockRow(rows, syncTS)
		if !ok {
			continue
		}
		buffer = append(buffer, vals)
		if len(buffer) >= batchSize {
			if err := flush(); err != nil {
				return s.fatal("Stock", err)
			}
		}
	}
	if err := rows.Err(); err != nil {
		return s.fatal("Stock", err)
	}
	if err := flush(); err != nil {
		return s.fatal("Stock", err)
	}

	log.Info("Cleaning up old Stock data...")
	res, err := s.pg.ExecContext(ctx, "DELETE FROM stok WHERE last_synced < $1", syncTS)
	if err != nil {
		return s.fatal("Stock", err)
	}
	if n, err := res.RowsAffected(); err == nil {
		log.Info("Cleaned up stale Stock records", "deleted", n)
	}
	return s.summary("STOK", total, start)
}

// scanStockRow maps a source row, reproducing MigrationService's kategori
// computation and normalized_item_name logic.
func (s *Service) scanStockRow(rows *sql.Rows, syncTS string) ([]any, bool) {
	var (
		itemCode      sql.NullString
		itemName      sql.NullString
		katItemcodeD  sql.NullString // SUBSTRING_INDEX columns in SQL are not used by Java logic
		katNamaD      sql.NullString
		finalStock    sql.NullString // DECIMAL from SUM; Java rs.getInt truncates
		hargaHpp      sql.NullString
		grandTotal    sql.NullString
		warehouseName sql.NullString
	)
	if err := rows.Scan(&itemCode, &itemName, &katItemcodeD, &katNamaD, &finalStock, &hargaHpp,
		&grandTotal, &warehouseName); err != nil {
		s.logger.Warn("Error processing Stock row", "err", err)
		return nil, false
	}

	var kategoriItemcode, kategoriNama *string
	if itemCode.Valid && itemCode.String != "" {
		code := itemCode.String
		var first string
		if strings.Contains(code, "-") {
			first = strings.Split(code, "-")[0]
		} else {
			first = strings.Split(code, " ")[0]
		}
		kategoriItemcode = &first
	}
	if itemName.Valid && itemName.String != "" {
		first := strings.Split(itemName.String, " ")[0]
		kategoriNama = &first
	}
	normalizedName := normalize.NormalizeItemName(itemName.String) // "" when name is NULL

	row := make([]any, 10)
	row[0] = nullStr(itemCode)
	row[1] = nullStr(itemName)
	row[2] = anyPtr(kategoriNama)
	row[3] = anyPtr(kategoriItemcode)
	row[4] = qtyInt(finalStock) // HAVING final_stock > 0, but NULL-safe
	row[5] = nullStr(hargaHpp)
	row[6] = nullStr(grandTotal)
	row[7] = nullStr(warehouseName)
	row[8] = syncTS
	row[9] = normalizedName
	return row, true
}

func anyPtr(p *string) any {
	if p == nil {
		return nil
	}
	return *p
}

// saveStockBatch upserts up to batchSize stock rows in one statement.
func (s *Service) saveStockBatch(ctx context.Context, rows [][]any) error {
	const perRow = 10
	var b strings.Builder
	b.WriteString(`INSERT INTO stok (id, item_code, item_name, kategori_nama, kategori_itemcode, final_stok, harga_hpp, grand_total, warehouse, last_synced, normalized_item_name) VALUES `)
	for i := range rows {
		if i > 0 {
			b.WriteString(", ")
		}
		valuesTuple(&b, i, perRow, "stok_seq")
	}
	b.WriteString(` ON CONFLICT (item_code, warehouse) DO UPDATE SET
		item_name = EXCLUDED.item_name,
		kategori_nama = EXCLUDED.kategori_nama,
		kategori_itemcode = EXCLUDED.kategori_itemcode,
		final_stok = EXCLUDED.final_stok,
		harga_hpp = EXCLUDED.harga_hpp,
		grand_total = EXCLUDED.grand_total,
		last_synced = EXCLUDED.last_synced,
		normalized_item_name = EXCLUDED.normalized_item_name`)
	_, err := s.pg.ExecContext(ctx, b.String(), flatten(rows)...)
	if err != nil {
		s.logger.Error("CRITICAL SQL ERROR in Stock Migration (Schema Mismatch?)", "err", err)
		s.logger.Warn("Pastikan UNIQUE CONSTRAINT (item_code, warehouse) tersedia di tabel stok.")
	}
	return err
}
