package repository

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/shopspring/decimal"

	"github.com/anandam/api-migration/internal/model"
)

// NOTE: StockRepository.findDistinctItemCodesSortedByPricelist() is NOT ported:
// it belongs to the removed pricelist feature.

const stockColumns = `id, item_code, item_name, normalized_item_name, kategori_nama, kategori_itemcode,
	final_stok, harga_hpp::text AS harga_hpp, grand_total::text AS grand_total, warehouse, last_synced`

func scanStock(rows *sql.Rows) (model.Stock, error) {
	var (
		s          model.Stock
		normName   sql.NullString
		katNama    sql.NullString
		katItem    sql.NullString
		finalStok  sql.NullInt64
		hpp        sql.NullString
		grandTotal sql.NullString
		warehouse  sql.NullString
		lastSynced sql.NullTime
	)
	err := rows.Scan(&s.ID, &s.ItemCode, &s.ItemName, &normName, &katNama, &katItem,
		&finalStok, &hpp, &grandTotal, &warehouse, &lastSynced)
	if err != nil {
		return s, err
	}
	s.NormalizedItemName = strPtr(normName)
	s.KategoriNama = strPtr(katNama)
	s.KategoriItemcode = strPtr(katItem)
	if finalStok.Valid {
		v := int(finalStok.Int64)
		s.FinalStok = &v
	}
	s.HargaHpp = decPtr(hpp)
	s.GrandTotal = decPtr(grandTotal)
	s.Warehouse = strPtr(warehouse)
	if lastSynced.Valid {
		t := lastSynced.Time
		s.LastSynced = &t
	}
	return s, nil
}

// stockCategoryClause reproduces the three-way category filter of
// findDistinctItemCodes (kategori_itemcode / kategori_nama / LCD magic).
// The caller must have already appended `categories` to args.
func stockCategoryClause(args *[]any) string {
	idx := len(*args)
	return fmt.Sprintf(`(s.kategori_itemcode = ANY($%[1]d) OR s.kategori_nama = ANY($%[1]d) OR
		('LCD' = ANY($%[1]d) AND (LOWER(TRIM(s.item_code)) LIKE 'lcd%%' OR
		LOWER(TRIM(s.kategori_itemcode)) LIKE '%%mon%%' OR LOWER(TRIM(s.kategori_nama)) LIKE '%%monitor%%')))`, idx)
}

// StockTruncate ↔ StockRepository.truncateTable().
func StockTruncate(ctx context.Context, db *sql.DB) error {
	_, err := db.ExecContext(ctx, "TRUNCATE TABLE stok RESTART IDENTITY")
	return err
}

// StockFindByItemNameContainingIgnoreCase ↔ derived query of the same name.
func StockFindByItemNameContainingIgnoreCase(ctx context.Context, db *sql.DB, itemName string, page, size int) (*Page[model.Stock], error) {
	var args []any
	where := "LOWER(item_name) LIKE LOWER('%%' || $" + argIdx(&args, itemName) + " || '%%')"
	return stockPaged(ctx, db, where, args, page, size)
}

// StockFindByItemCodeContainingIgnoreCase ↔ derived query of the same name.
func StockFindByItemCodeContainingIgnoreCase(ctx context.Context, db *sql.DB, itemCode string, page, size int) (*Page[model.Stock], error) {
	var args []any
	where := "LOWER(item_code) LIKE LOWER('%%' || $" + argIdx(&args, itemCode) + " || '%%')"
	return stockPaged(ctx, db, where, args, page, size)
}

// StockFindByNameOrCodeContainingIgnoreCase ↔ derived global-search query.
func StockFindByNameOrCodeContainingIgnoreCase(ctx context.Context, db *sql.DB, itemName, itemCode string, page, size int) (*Page[model.Stock], error) {
	var args []any
	where := "(" +
		"LOWER(item_name) LIKE LOWER('%%' || $" + argIdx(&args, itemName) + " || '%%') OR " +
		"LOWER(item_code) LIKE LOWER('%%' || $" + argIdx(&args, itemCode) + " || '%%'))"
	return stockPaged(ctx, db, where, args, page, size)
}

// stockSearchBasic renders the findByFilters WHERE clause.
func stockSearchBasic(args *[]any, search string, categories []string, warehouse string) string {
	var conds []string
	if search != "" {
		conds = append(conds, "("+like(args, "s.item_name", search)+" OR "+like(args, "s.item_code", search)+")")
	}
	if len(categories) > 0 {
		*args = append(*args, categories)
		conds = append(conds, fmt.Sprintf("s.kategori_itemcode = ANY($%d)", len(*args)))
	}
	if warehouse != "" {
		conds = append(conds, like(args, "COALESCE(s.warehouse,'')", warehouse))
	}
	conds = append(conds, "s.final_stok >= 1")
	return strings.Join(conds, " AND ")
}

// StockFindByFilters ↔ StockRepository.findByFilters (paged).
func StockFindByFilters(ctx context.Context, db *sql.DB, search string, categories []string, warehouse string, page, size int) (*Page[model.Stock], error) {
	var args []any
	where := stockSearchBasic(&args, search, categories, warehouse)
	return stockPaged(ctx, db, where, args, page, size)
}

// stockPaged runs a paged Stock query + its COUNT(*). `where` may reference
// the table alias `s`, so the query always aliases stok as s.
func stockPaged(ctx context.Context, db *sql.DB, where string, args []any, page, size int) (*Page[model.Stock], error) {
	var total int64
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM stok s WHERE "+where, args...).Scan(&total); err != nil {
		return nil, err
	}
	q := "SELECT " + stockColumns + " FROM stok s WHERE " + where + " ORDER BY s.id " + pageClause(page, size)
	rows, err := db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	content := make([]model.Stock, 0)
	for rows.Next() {
		row, err := scanStock(rows)
		if err != nil {
			return nil, err
		}
		content = append(content, row)
	}
	return newPage(content, total, page, size), rows.Err()
}

// stockDistinctSearch renders the findDistinctItemCodes WHERE clause.
func stockDistinctSearch(args *[]any, search string, categories []string) string {
	var conds []string
	if search != "" {
		conds = append(conds, "("+
			like(args, "s.item_name", search)+" OR "+
			like(args, "s.item_code", search)+" OR "+
			like(args, "COALESCE(s.kategori_itemcode,'')", search)+" OR "+
			like(args, "COALESCE(s.kategori_nama,'')", search)+")")
	}
	if len(categories) > 0 {
		*args = append(*args, categories)
		conds = append(conds, stockCategoryClause(args))
	}
	conds = append(conds, "s.final_stok >= 1")
	return strings.Join(conds, " AND ")
}

// StockFindDistinctItemCodes ↔ StockRepository.findDistinctItemCodes (paged).
func StockFindDistinctItemCodes(ctx context.Context, db *sql.DB, search string, categories []string, page, size int) (*Page[string], error) {
	var args []any
	where := stockDistinctSearch(&args, search, categories)

	var total int64
	countQ := "SELECT COUNT(DISTINCT s.item_code) FROM stok s WHERE " + where
	if err := db.QueryRowContext(ctx, countQ, args...).Scan(&total); err != nil {
		return nil, err
	}

	q := "SELECT s.item_code FROM stok s WHERE " + where + " GROUP BY s.item_code ORDER BY s.item_code " + pageClause(page, size)
	rows, err := db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var content []string
	for rows.Next() {
		var c sql.NullString
		if err := rows.Scan(&c); err != nil {
			return nil, err
		}
		if c.Valid {
			content = append(content, c.String)
		}
	}
	return newPage(content, total, page, size), rows.Err()
}

// StockFindByItemCodeInAndFinalStokGreaterThanEqual ↔ derived query of the same name.
func StockFindByItemCodeInAndFinalStokGreaterThanEqual(ctx context.Context, db *sql.DB, itemCodes []string, minStok int) ([]model.Stock, error) {
	if len(itemCodes) == 0 {
		return nil, nil
	}
	rows, err := db.QueryContext(ctx,
		"SELECT "+stockColumns+" FROM stok WHERE item_code = ANY($1) AND final_stok >= $2 ORDER BY id",
		itemCodes, minStok)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []model.Stock
	for rows.Next() {
		row, err := scanStock(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

// StockCountByFinalStokLessThan ↔ countByFinalStokLessThan.
func StockCountByFinalStokLessThan(ctx context.Context, db *sql.DB, threshold int) (int64, error) {
	var n int64
	err := db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM stok WHERE final_stok < $1 AND final_stok >= 1", threshold).Scan(&n)
	return n, err
}

// StockFindTop5ByLowStock ↔ findTop5ByLowStock.
func StockFindTop5ByLowStock(ctx context.Context, db *sql.DB, threshold int) ([]model.Stock, error) {
	rows, err := db.QueryContext(ctx,
		"SELECT "+stockColumns+" FROM stok WHERE final_stok < $1 AND final_stok >= 1 ORDER BY final_stok ASC, id LIMIT 5",
		threshold)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []model.Stock
	for rows.Next() {
		row, err := scanStock(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

// KategoriSum is a row of sumGrandTotalByKategoriItemcode / ...KategoriNama.
type KategoriSum struct {
	Kategori   string
	GrandTotal decimal.Decimal
}

// StockSumGrandTotalByKategoriItemcode ↔ sumGrandTotalByKategoriItemcode.
func StockSumGrandTotalByKategoriItemcode(ctx context.Context, db *sql.DB) ([]KategoriSum, error) {
	return scanKategoriSums(ctx, db, `SELECT COALESCE(NULLIF(TRIM(s.kategori_itemcode),''), 'LAIN-LAIN'),
		COALESCE(SUM(s.grand_total),0)::text FROM stok s WHERE s.final_stok >= 1
		GROUP BY COALESCE(NULLIF(TRIM(s.kategori_itemcode),''), 'LAIN-LAIN')
		ORDER BY COALESCE(SUM(s.grand_total),0) DESC`)
}

// StockSumGrandTotalByKategoriNama ↔ sumGrandTotalByKategoriNama.
func StockSumGrandTotalByKategoriNama(ctx context.Context, db *sql.DB) ([]KategoriSum, error) {
	return scanKategoriSums(ctx, db, `SELECT COALESCE(NULLIF(TRIM(s.kategori_nama),''), 'LAIN-LAIN'),
		COALESCE(SUM(s.grand_total),0)::text FROM stok s WHERE s.final_stok >= 1
		GROUP BY COALESCE(NULLIF(TRIM(s.kategori_nama),''), 'LAIN-LAIN')
		ORDER BY COALESCE(SUM(s.grand_total),0) DESC`)
}

func scanKategoriSums(ctx context.Context, db *sql.DB, query string) ([]KategoriSum, error) {
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []KategoriSum
	for rows.Next() {
		var (
			k   sql.NullString
			sum sql.NullString
		)
		if err := rows.Scan(&k, &sum); err != nil {
			return nil, err
		}
		if !k.Valid {
			continue
		}
		var d decimal.Decimal
		if sum.Valid {
			if parsed, err := decimal.NewFromString(sum.String); err == nil {
				d = parsed
			}
		}
		out = append(out, KategoriSum{Kategori: k.String, GrandTotal: d})
	}
	return out, rows.Err()
}

// KategoriHierarchyRow is a row of sumGrandTotalByKategoriHierarchy.
type KategoriHierarchyRow struct {
	Itemcode   string
	Nama       string
	GrandTotal decimal.Decimal
}

// StockSumGrandTotalByKategoriHierarchy ↔ sumGrandTotalByKategoriHierarchy.
func StockSumGrandTotalByKategoriHierarchy(ctx context.Context, db *sql.DB) ([]KategoriHierarchyRow, error) {
	rows, err := db.QueryContext(ctx, `SELECT s.kategori_itemcode, s.kategori_nama, COALESCE(SUM(s.grand_total),0)::text
		FROM stok s WHERE s.kategori_itemcode IS NOT NULL AND s.kategori_nama IS NOT NULL AND s.final_stok >= 1
		GROUP BY s.kategori_itemcode, s.kategori_nama
		ORDER BY s.kategori_itemcode ASC, COALESCE(SUM(s.grand_total),0) DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []KategoriHierarchyRow
	for rows.Next() {
		var (
			r   KategoriHierarchyRow
			kc  sql.NullString
			kn  sql.NullString
			sum sql.NullString
		)
		if err := rows.Scan(&kc, &kn, &sum); err != nil {
			return nil, err
		}
		if !kc.Valid || !kn.Valid {
			continue
		}
		r.Itemcode, r.Nama = kc.String, kn.String
		if sum.Valid {
			if parsed, err := decimal.NewFromString(sum.String); err == nil {
				r.GrandTotal = parsed
			}
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// StockSumAllGrandTotal ↔ sumAllGrandTotal.
func StockSumAllGrandTotal(ctx context.Context, db *sql.DB) (decimal.Decimal, error) {
	return sumDecimal(ctx, db, "SELECT COALESCE(SUM(s.grand_total),0)::text FROM stok s WHERE s.final_stok >= 1")
}

// argIdx appends one arg and returns its 1-based placeholder index.
func argIdx(args *[]any, v any) string {
	*args = append(*args, v)
	return fmt.Sprintf("%d", len(*args))
}
