package repository

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/shopspring/decimal"

	"github.com/anandam/api-migration/internal/model"
)

// purchaseSearch renders the WHERE clause shared by all Purchase queries
// (port of PurchaseRepository JPQL). Arguments are appended in order.
func purchaseSearch(args *[]any, startDate, endDate *time.Time, categories []string, search, searchColumn string) string {
	var conds []string

	if startDate != nil {
		*args = append(*args, startDate.Format("2006-01-02"))
		conds = append(conds, fmt.Sprintf("doc_date >= $%d", len(*args)))
	}
	if endDate != nil {
		*args = append(*args, endDate.Format("2006-01-02"))
		conds = append(conds, fmt.Sprintf("doc_date <= $%d", len(*args)))
	}

	if len(categories) > 0 {
		*args = append(*args, categories)
		conds = append(conds, fmt.Sprintf("dep_code = ANY($%d)", len(*args)))
	}

	if search != "" {
		var fields []string
		switch searchColumn {
		case "noNota":
			fields = []string{"doc_no_p"}
		case "distributor":
			fields = []string{"par_name"}
		case "barang":
			fields = []string{"item_name"}
		case "dept":
			fields = []string{"dep_code"}
		default: // "", "ALL", nil -> search every text column
			fields = []string{"doc_no_p", "par_name", "item_name", "dep_code"}
		}
		parts := make([]string, 0, len(fields))
		for _, f := range fields {
			parts = append(parts, like(args, f, search))
		}
		conds = append(conds, "("+strings.Join(parts, " OR ")+")")
	}

	if len(conds) == 0 {
		return "TRUE"
	}
	return strings.Join(conds, " AND ")
}

const purchaseColumns = `id, doc_date, doc_no_p, par_name, dep_code, dep_name, item_code, item_name, qty,
	price::text AS price, grand_total::text AS grand_total, last_synced`

func scanPurchase(rows *sql.Rows) (model.Purchase, error) {
	var (
		p          model.Purchase
		docDate    sql.NullTime
		parName    sql.NullString
		depCode    sql.NullString
		depName    sql.NullString
		itemCode   sql.NullString
		itemName   sql.NullString
		qty        sql.NullInt64
		price      sql.NullString
		grandTotal sql.NullString
		lastSynced sql.NullTime
	)
	err := rows.Scan(&p.ID, &docDate, &p.DocNoP, &parName, &depCode, &depName, &itemCode, &itemName,
		&qty, &price, &grandTotal, &lastSynced)
	if err != nil {
		return p, err
	}
	if docDate.Valid {
		t := docDate.Time
		p.DocDate = &t
	}
	p.ParName = strPtr(parName)
	p.DepCode = strPtr(depCode)
	p.DepName = strPtr(depName)
	p.ItemCode = strPtr(itemCode)
	p.ItemName = strPtr(itemName)
	if qty.Valid {
		p.Qty = int(qty.Int64)
	}
	p.Price = decPtr(price)
	p.GrandTotal = decPtr(grandTotal)
	if lastSynced.Valid {
		t := lastSynced.Time
		p.LastSynced = &t
	}
	return p, nil
}

// PurchaseTruncate ↔ PurchaseRepository.truncateTable().
func PurchaseTruncate(ctx context.Context, db *sql.DB) error {
	_, err := db.ExecContext(ctx, "TRUNCATE TABLE purchases RESTART IDENTITY")
	return err
}

// PurchaseFindByDateRangeAndSearch ↔ PurchaseRepository.findByDateRangeAndSearch (paged).
func PurchaseFindByDateRangeAndSearch(ctx context.Context, db *sql.DB, startDate, endDate *time.Time, categories []string, search, searchColumn string, page, size int) (*Page[model.Purchase], error) {
	var args []any
	where := purchaseSearch(&args, startDate, endDate, categories, search, searchColumn)

	var total int64
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM purchases WHERE "+where, args...).Scan(&total); err != nil {
		return nil, err
	}

	q := "SELECT " + purchaseColumns + " FROM purchases WHERE " + where +
		" ORDER BY doc_date DESC NULLS LAST, id DESC " + pageClause(page, size)
	rows, err := db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	content := make([]model.Purchase, 0)
	for rows.Next() {
		row, err := scanPurchase(rows)
		if err != nil {
			return nil, err
		}
		content = append(content, row)
	}
	return newPage(content, total, page, size), rows.Err()
}

// PurchaseFindAllByDateRangeAndSearch ↔ findAllByDateRangeAndSearch (unordered list; Java ordered by docDate DESC).
func PurchaseFindAllByDateRangeAndSearch(ctx context.Context, db *sql.DB, startDate, endDate *time.Time, categories []string, search, searchColumn string) ([]model.Purchase, error) {
	var args []any
	where := purchaseSearch(&args, startDate, endDate, categories, search, searchColumn)
	rows, err := db.QueryContext(ctx, "SELECT "+purchaseColumns+" FROM purchases WHERE "+where+" ORDER BY doc_date DESC NULLS LAST, id DESC", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []model.Purchase
	for rows.Next() {
		row, err := scanPurchase(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

// PurchaseSumGrandTotalByDateRange ↔ sumGrandTotalByDateRange.
func PurchaseSumGrandTotalByDateRange(ctx context.Context, db *sql.DB, startDate, endDate *time.Time, categories []string, search, searchColumn string) (decimal.Decimal, error) {
	var args []any
	where := purchaseSearch(&args, startDate, endDate, categories, search, searchColumn)
	return sumDecimal(ctx, db, "SELECT COALESCE(SUM(grand_total),0)::text FROM purchases WHERE "+where, args...)
}

// PurchaseSumTotalByDate ↔ sumTotalByDate.
func PurchaseSumTotalByDate(ctx context.Context, db *sql.DB, today *time.Time) (decimal.Decimal, error) {
	return sumDecimal(ctx, db, "SELECT COALESCE(SUM(grand_total),0)::text FROM purchases WHERE doc_date = $1", today.Format("2006-01-02"))
}

// PurchaseSumQtyByDateRange ↔ sumQtyByDateRange.
func PurchaseSumQtyByDateRange(ctx context.Context, db *sql.DB, startDate, endDate *time.Time, categories []string, search, searchColumn string) (int64, error) {
	var args []any
	where := purchaseSearch(&args, startDate, endDate, categories, search, searchColumn)
	var sum sql.NullInt64
	err := db.QueryRowContext(ctx, "SELECT COALESCE(SUM(qty),0) FROM purchases WHERE "+where, args...).Scan(&sum)
	return sum.Int64, err
}

// LatestPurchaseDetail is one row of findLatestPurchaseDetailsByItemNames.
type LatestPurchaseDetail struct {
	ItemName string
	DocDate  *time.Time
	ParName  *string
}

// PurchaseFindLatestPurchaseDetailsByItemNames ↔ findLatestPurchaseDetailsByItemNames.
func PurchaseFindLatestPurchaseDetailsByItemNames(ctx context.Context, db *sql.DB, itemNames []string) ([]LatestPurchaseDetail, error) {
	if len(itemNames) == 0 {
		return nil, nil
	}
	q := `SELECT p.item_name, p.doc_date, p.par_name FROM purchases p
		WHERE p.item_name = ANY($1)
		AND p.doc_date = (SELECT MAX(p2.doc_date) FROM purchases p2 WHERE p2.item_name = p.item_name)`
	rows, err := db.QueryContext(ctx, q, itemNames)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []LatestPurchaseDetail
	for rows.Next() {
		var (
			d  LatestPurchaseDetail
			dt sql.NullTime
			pn sql.NullString
		)
		if err := rows.Scan(&d.ItemName, &dt, &pn); err != nil {
			return nil, err
		}
		if dt.Valid {
			t := dt.Time
			d.DocDate = &t
		}
		d.ParName = strPtr(pn)
		out = append(out, d)
	}
	return out, rows.Err()
}

// PurchaseFindLatestPurchaseByItemName ↔ findLatestPurchaseByItemName.
func PurchaseFindLatestPurchaseByItemName(ctx context.Context, db *sql.DB, itemName string) (*model.Purchase, error) {
	rows, err := db.QueryContext(ctx, "SELECT "+purchaseColumns+" FROM purchases WHERE item_name = $1 ORDER BY doc_date DESC NULLS LAST, id DESC LIMIT 1", itemName)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, rows.Err()
	}
	p, err := scanPurchase(rows)
	if err != nil {
		return nil, err
	}
	return &p, rows.Err()
}

// PurchaseFindDistinctDepCodes ↔ findDistinctDepCodes.
func PurchaseFindDistinctDepCodes(ctx context.Context, db *sql.DB) ([]string, error) {
	return scanStrings(ctx, db, "SELECT DISTINCT dep_code FROM purchases WHERE dep_code IS NOT NULL AND TRIM(dep_code) <> '' ORDER BY dep_code")
}
