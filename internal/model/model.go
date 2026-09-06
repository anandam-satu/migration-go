// Package model mirrors the JPA entities under core/postgres/model. Money
// columns map to shopspring decimal; nullable values use pointers,
// dates/timestamps use time.Time pointers.
package model

import (
	"time"

	"github.com/shopspring/decimal"
)

// Purchase ↔ table "purchases", unique (doc_no_p, item_code).
type Purchase struct {
	ID          int64
	DocDate     *time.Time // DATE column
	DocNoP      string
	ParName     *string
	DepCode     *string
	DepName     *string
	ItemCode    *string
	ItemName    *string
	Qty         int
	Price       *decimal.Decimal
	GrandTotal  *decimal.Decimal
	PaidTotal   *decimal.Decimal
	StatusBayar *string
	LastSynced  *time.Time
}

// Sales ↔ table "sales", unique (doc_no, item_name).
type Sales struct {
	ID          int64
	DocDate     *time.Time
	DocNo       string
	Code        *string // partner code
	ParName     *string
	DepCode     *string
	DepName     *string
	IteCode     *string
	ItemName    string
	Qty         int
	Price       *decimal.Decimal
	GrandTotal  *decimal.Decimal
	HppSatuan   *decimal.Decimal
	TotalHpp    *decimal.Decimal
	LabaKotor   *decimal.Decimal
	EmpCode     *string
	EmpName     *string
	PaidTotal   *decimal.Decimal
	StatusBayar *string
	LastSynced  *time.Time
}

// Stock ↔ table "stok", unique (item_code, warehouse).
// The original entity's pricelist lookup fields (spesifikasi/modal/
// finalPricelist) live in the separate public.pricelist table (see
// PricelistSyncer), not on Stock; stok joins pricelist on normalized_item_name.
type Stock struct {
	ID                 int64
	ItemCode           string
	ItemName           string
	NormalizedItemName *string
	KategoriNama       *string
	KategoriItemcode   *string
	FinalStok          *int
	HargaHpp           *decimal.Decimal
	GrandTotal         *decimal.Decimal
	Warehouse          *string
	LastSynced         *time.Time

	// @Transient fields (populated by other consumers of the dataset).
	LastSalesDate    *time.Time
	LastPurchaseDate *time.Time
	ParName          *string
}

// ItemSerialNumber ↔ table "item_serial_numbers", unique (sn, doc_id, type).
type ItemSerialNumber struct {
	ID         int64
	Tanggal    *time.Time
	DocID      *string
	UserName   *string
	ItemName   *string
	SN         *string
	Type       *string // MASUK / KELUAR
	LastSynced *time.Time
}

// SyncSettings ↔ table "sync_settings" (sync_key PK).
type SyncSettings struct {
	SyncKey   string
	SyncValue string
}

// Pricelist ↔ table "pricelist": the Google Sheet "PRICELIST&MODAL" snapshot
// kept in sync by service.PricelistSyncer. item_name is the raw sheet name
// (kept for display), normalized_item_name is the join key to
// Stock.normalized_item_name. id has no DB default: inserts use
// nextval('pricelist_seq').
type Pricelist struct {
	ID                 int64
	ItemName           *string
	NormalizedItemName *string
	Spesifikasi        *string
	Modal              *decimal.Decimal
	FinalPricelist     *decimal.Decimal
}
