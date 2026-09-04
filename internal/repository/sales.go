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

// salesSearch renders the WHERE clause shared by Sales queries
// (port of SalesRepository JPQL).
func salesSearch(args *[]any, startDate, endDate *time.Time, empCode string, categories []string, search, searchColumn string) string {
	var conds []string

	if startDate != nil {
		*args = append(*args, startDate.Format("2006-01-02"))
		conds = append(conds, fmt.Sprintf("doc_date >= $%d", len(*args)))
	}
	if endDate != nil {
		*args = append(*args, endDate.Format("2006-01-02"))
		conds = append(conds, fmt.Sprintf("doc_date <= $%d", len(*args)))
	}

	if empCode != "" {
		*args = append(*args, empCode)
		conds = append(conds, fmt.Sprintf("emp_code = $%d", len(*args)))
	}

	if len(categories) > 0 {
		*args = append(*args, categories)
		conds = append(conds, fmt.Sprintf("dep_code = ANY($%d)", len(*args)))
	}

	if search != "" {
		var fields []string
		switch searchColumn {
		case "code":
			fields = []string{"code"}
		case "noNota":
			fields = []string{"doc_no"}
		case "distributor":
			fields = []string{"par_name"}
		case "barang":
			fields = []string{"item_name"}
		case "dept":
			fields = []string{"dep_code"}
		default: // "", "ALL", nil -> search every text column
			fields = []string{"doc_no", "par_name", "item_name", "dep_code", "code"}
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

const salesColumns = `id, doc_date, doc_no, code, par_name, dep_code, dep_name, ite_code, item_name, qty,
	price::text AS price, grand_total::text AS grand_total, hpp_satuan::text AS hpp_satuan,
	total_hpp::text AS total_hpp, laba_kotor::text AS laba_kotor, emp_code, emp_name, last_synced`

func scanSales(rows *sql.Rows) (model.Sales, error) {
	var (
		s          model.Sales
		docDate    sql.NullTime
		code       sql.NullString
		parName    sql.NullString
		depCode    sql.NullString
		depName    sql.NullString
		iteCode    sql.NullString
		qty        sql.NullInt64
		price      sql.NullString
		grandTotal sql.NullString
		hpp        sql.NullString
		totalHpp   sql.NullString
		laba       sql.NullString
		empCode    sql.NullString
		empName    sql.NullString
		lastSynced sql.NullTime
	)
	err := rows.Scan(&s.ID, &docDate, &s.DocNo, &code, &parName, &depCode, &depName, &iteCode, &s.ItemName,
		&qty, &price, &grandTotal, &hpp, &totalHpp, &laba, &empCode, &empName, &lastSynced)
	if err != nil {
		return s, err
	}
	if docDate.Valid {
		t := docDate.Time
		s.DocDate = &t
	}
	s.Code = strPtr(code)
	s.ParName = strPtr(parName)
	s.DepCode = strPtr(depCode)
	s.DepName = strPtr(depName)
	s.IteCode = strPtr(iteCode)
	if qty.Valid {
		s.Qty = int(qty.Int64)
	}
	s.Price = decPtr(price)
	s.GrandTotal = decPtr(grandTotal)
	s.HppSatuan = decPtr(hpp)
	s.TotalHpp = decPtr(totalHpp)
	s.LabaKotor = decPtr(laba)
	s.EmpCode = strPtr(empCode)
	s.EmpName = strPtr(empName)
	if lastSynced.Valid {
		t := lastSynced.Time
		s.LastSynced = &t
	}
	return s, nil
}

// SalesTruncate ↔ SalesRepository.truncateTable().
func SalesTruncate(ctx context.Context, db *sql.DB) error {
	_, err := db.ExecContext(ctx, "TRUNCATE TABLE sales RESTART IDENTITY")
	return err
}

// SalesFindByFilters ↔ SalesRepository.findByFilters (paged).
func SalesFindByFilters(ctx context.Context, db *sql.DB, startDate, endDate *time.Time, empCode string, categories []string, search, searchColumn string, page, size int) (*Page[model.Sales], error) {
	var args []any
	where := salesSearch(&args, startDate, endDate, empCode, categories, search, searchColumn)

	var total int64
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM sales WHERE "+where, args...).Scan(&total); err != nil {
		return nil, err
	}

	q := "SELECT " + salesColumns + " FROM sales WHERE " + where +
		" ORDER BY doc_date DESC NULLS LAST, id DESC " + pageClause(page, size)
	rows, err := db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	content := make([]model.Sales, 0)
	for rows.Next() {
		row, err := scanSales(rows)
		if err != nil {
			return nil, err
		}
		content = append(content, row)
	}
	return newPage(content, total, page, size), rows.Err()
}

// SalesFindAllByFilters ↔ findAllByFilters (ordered by docDate DESC).
func SalesFindAllByFilters(ctx context.Context, db *sql.DB, startDate, endDate *time.Time, empCode string, categories []string, search, searchColumn string) ([]model.Sales, error) {
	var args []any
	where := salesSearch(&args, startDate, endDate, empCode, categories, search, searchColumn)
	rows, err := db.QueryContext(ctx, "SELECT "+salesColumns+" FROM sales WHERE "+where+" ORDER BY doc_date DESC NULLS LAST, id DESC", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []model.Sales
	for rows.Next() {
		row, err := scanSales(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

// SalesSumGrandTotalByFilters ↔ sumGrandTotalByFilters.
func SalesSumGrandTotalByFilters(ctx context.Context, db *sql.DB, startDate, endDate *time.Time, empCode string, categories []string, search, searchColumn string) (decimal.Decimal, error) {
	var args []any
	where := salesSearch(&args, startDate, endDate, empCode, categories, search, searchColumn)
	return sumDecimal(ctx, db, "SELECT COALESCE(SUM(grand_total),0)::text FROM sales WHERE "+where, args...)
}

// SalesSumTotalByDate ↔ sumTotalByDate.
func SalesSumTotalByDate(ctx context.Context, db *sql.DB, today *time.Time) (decimal.Decimal, error) {
	return sumDecimal(ctx, db, "SELECT COALESCE(SUM(grand_total),0)::text FROM sales WHERE doc_date = $1", today.Format("2006-01-02"))
}

// SalesSumQtyByFilters ↔ sumQtyByFilters.
func SalesSumQtyByFilters(ctx context.Context, db *sql.DB, startDate, endDate *time.Time, empCode string, categories []string, search, searchColumn string) (int64, error) {
	var args []any
	where := salesSearch(&args, startDate, endDate, empCode, categories, search, searchColumn)
	var sum sql.NullInt64
	err := db.QueryRowContext(ctx, "SELECT COALESCE(SUM(qty),0) FROM sales WHERE "+where, args...).Scan(&sum)
	return sum.Int64, err
}

// SalesFindLatestDocDateByItemName ↔ findLatestDocDateByItemName.
func SalesFindLatestDocDateByItemName(ctx context.Context, db *sql.DB, itemName string) (*time.Time, error) {
	var t sql.NullTime
	err := db.QueryRowContext(ctx, "SELECT MAX(doc_date) FROM sales WHERE item_name = $1", itemName).Scan(&t)
	if err != nil {
		return nil, err
	}
	if !t.Valid {
		return nil, nil
	}
	return &t.Time, nil
}

// LatestDocDateByItem is one row of findLatestDocDatesByItemNames.
type LatestDocDateByItem struct {
	ItemName string
	DocDate  *time.Time
}

// SalesFindLatestDocDatesByItemNames ↔ findLatestDocDatesByItemNames.
func SalesFindLatestDocDatesByItemNames(ctx context.Context, db *sql.DB, itemNames []string) ([]LatestDocDateByItem, error) {
	if len(itemNames) == 0 {
		return nil, nil
	}
	rows, err := db.QueryContext(ctx,
		"SELECT item_name, MAX(doc_date) FROM sales WHERE item_name = ANY($1) GROUP BY item_name", itemNames)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []LatestDocDateByItem
	for rows.Next() {
		var (
			d  LatestDocDateByItem
			dt sql.NullTime
		)
		if err := rows.Scan(&d.ItemName, &dt); err != nil {
			return nil, err
		}
		if dt.Valid {
			t := dt.Time
			d.DocDate = &t
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// EmpSalesRow is a row of sumSalesByEmployeeToday / sumSalesByEmployeeMonth.
type EmpSalesRow struct {
	EmpCode    *string
	EmpName    *string
	GrandTotal decimal.Decimal
}

func scanEmpSales(rows *sql.Rows) ([]EmpSalesRow, error) {
	var out []EmpSalesRow
	for rows.Next() {
		var (
			r   EmpSalesRow
			ec  sql.NullString
			en  sql.NullString
			sum sql.NullString
		)
		if err := rows.Scan(&ec, &en, &sum); err != nil {
			return nil, err
		}
		r.EmpCode = strPtr(ec)
		r.EmpName = strPtr(en)
		if sum.Valid {
			if d, err := decimal.NewFromString(sum.String); err == nil {
				r.GrandTotal = d
			}
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// SalesSumByEmployeeToday ↔ sumSalesByEmployeeToday.
func SalesSumByEmployeeToday(ctx context.Context, db *sql.DB, today *time.Time) ([]EmpSalesRow, error) {
	q := `SELECT emp_code, MIN(emp_name), COALESCE(SUM(grand_total),0)::text
		FROM sales WHERE doc_date = $1
		GROUP BY emp_code ORDER BY SUM(grand_total) DESC`
	rows, err := db.QueryContext(ctx, q, today.Format("2006-01-02"))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanEmpSales(rows)
}

// SalesSumByEmployeeMonth ↔ sumSalesByEmployeeMonth.
func SalesSumByEmployeeMonth(ctx context.Context, db *sql.DB, start, end *time.Time) ([]EmpSalesRow, error) {
	q := `SELECT emp_code, MIN(emp_name), COALESCE(SUM(grand_total),0)::text
		FROM sales WHERE doc_date BETWEEN $1 AND $2
		GROUP BY emp_code ORDER BY SUM(grand_total) DESC`
	rows, err := db.QueryContext(ctx, q, start.Format("2006-01-02"), end.Format("2006-01-02"))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanEmpSales(rows)
}

// DistinctEmp is one row of findDistinctEmpCodeAndName.
type DistinctEmp struct {
	Code string
	Name *string
}

// SalesFindDistinctEmpCodeAndName ↔ findDistinctEmpCodeAndName.
func SalesFindDistinctEmpCodeAndName(ctx context.Context, db *sql.DB) ([]DistinctEmp, error) {
	rows, err := db.QueryContext(ctx,
		"SELECT DISTINCT emp_code, emp_name FROM sales WHERE emp_code IS NOT NULL AND TRIM(emp_code) <> '' ORDER BY emp_code")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []DistinctEmp
	for rows.Next() {
		var (
			d  DistinctEmp
			en sql.NullString
		)
		if err := rows.Scan(&d.Code, &en); err != nil {
			return nil, err
		}
		d.Name = strPtr(en)
		out = append(out, d)
	}
	return out, rows.Err()
}

// SalesFindDistinctEmpCodeOrderByEmpCode ↔ findDistinctEmpCodeOrderByEmpCode.
func SalesFindDistinctEmpCodeOrderByEmpCode(ctx context.Context, db *sql.DB) ([]string, error) {
	return scanStrings(ctx, db,
		"SELECT DISTINCT emp_code FROM sales WHERE emp_code IS NOT NULL AND TRIM(emp_code) <> '' ORDER BY emp_code")
}

// SalesFindDistinctDepCodes ↔ findDistinctDepCodes.
func SalesFindDistinctDepCodes(ctx context.Context, db *sql.DB) ([]string, error) {
	return scanStrings(ctx, db,
		"SELECT DISTINCT dep_code FROM sales WHERE dep_code IS NOT NULL AND TRIM(dep_code) <> '' ORDER BY dep_code")
}
