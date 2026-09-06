// Package db opens and configures the two database connections used by the
// migration worker (PostgreSQL target + legacy MySQL source), mirroring the
// Hikari pool settings from the original Spring configuration.
package db

import (
	"context"
	"database/sql"
	"fmt"
	"net"
	"net/url"
	"time"

	"github.com/go-sql-driver/mysql"
	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/anandam/api-migration/internal/config"
)

// OpenMySQL opens the legacy MyBiz MySQL connection.
//
// sql.Open is lazy: no network I/O happens here, so the app starts fine even
// when MySQL is down (mirrors Hikari's initializationFailTimeout=-1 in
// MysqlConfig). Connection limits reproduce spring.datasource.mysql.hikari.*.
func OpenMySQL(cfg config.MySQL) (*sql.DB, error) {
	mc := mysql.NewConfig()
	mc.User = cfg.User
	mc.Passwd = cfg.Password
	mc.Net = "tcp"
	mc.Addr = net.JoinHostPort(cfg.Host, cfg.Port)
	mc.DBName = cfg.DBName
	mc.ParseTime = true
	mc.Loc = time.UTC
	mc.Params = map[string]string{"charset": "utf8"}

	db, err := sql.Open("mysql", mc.FormatDSN())
	if err != nil {
		return nil, fmt.Errorf("open mysql: %w", err)
	}
	db.SetMaxOpenConns(cfg.MaxOpen)
	// minimum-idle=0: keep no idle MySQL connections parked, so SHOW
	// PROCESSLIST on MyBiz never shows Sleep connections (a stated goal of
	// the original code, which soft-evicted the pool after every cycle).
	db.SetMaxIdleConns(0)
	db.SetConnMaxIdleTime(cfg.IdleTimeout)
	db.SetConnMaxLifetime(cfg.MaxLifetime)
	return db, nil
}

// OpenPostgres opens the main PostgreSQL ("stok-anandam") connection using the
// pgx stdlib driver. sslmode=prefer matches the original JDBC url.
func OpenPostgres(cfg config.Postgres) (*sql.DB, error) {
	u := &url.URL{
		Scheme: "postgres",
		User:   url.UserPassword(cfg.User, cfg.Password),
		Host:   net.JoinHostPort(cfg.Host, cfg.Port),
		Path:   "/" + cfg.DBName,
	}
	q := u.Query()
	q.Set("sslmode", "prefer")
	u.RawQuery = q.Encode()

	db, err := sql.Open("pgx", u.String())
	if err != nil {
		return nil, fmt.Errorf("open postgres: %w", err)
	}
	db.SetMaxOpenConns(cfg.MaxOpen)
	db.SetMaxIdleConns(cfg.MaxIdle)
	db.SetConnMaxLifetime(cfg.MaxLifetime)
	return db, nil
}

// EvictMySQLIdle mirrors Hikari's softEvictConnections(): it closes idle
// connections so none linger on the MyBiz MySQL server. With MaxIdleConns(0)
// the pool already closes connections as soon as they are returned; this call
// is kept as an explicit, scheduler-visible step for parity.
func EvictMySQLIdle(db *sql.DB) {
	if db == nil {
		return
	}
	db.SetMaxIdleConns(0)
}

// EnsurePostgresSchema is the Go counterpart of Hibernate's
// spring.jpa.hibernate.ddl-auto=update for the tables this worker owns. It is
// idempotent (CREATE ... IF NOT EXISTS) and non-fatal: on an already
// provisioned database it does nothing.
func EnsurePostgresSchema(ctx context.Context, db *sql.DB) error {
	stmts := []string{
		// Sequences used by the raw batch INSERTs (nextval(...)).
		`CREATE SEQUENCE IF NOT EXISTS purchase_seq START 1`,
		`CREATE SEQUENCE IF NOT EXISTS sales_seq START 1`,
		`CREATE SEQUENCE IF NOT EXISTS stok_seq START 1`,
		`CREATE SEQUENCE IF NOT EXISTS pricelist_seq START 1`,

		`CREATE TABLE IF NOT EXISTS purchases (
			id BIGINT PRIMARY KEY DEFAULT nextval('purchase_seq'),
			doc_date DATE,
			doc_no_p VARCHAR(50),
			par_name VARCHAR(100),
			dep_code VARCHAR(50),
			dep_name TEXT,
			item_code VARCHAR(50),
			item_name TEXT,
			qty INTEGER,
			price NUMERIC(19,2),
			grand_total NUMERIC(19,2),
			paid_total NUMERIC(19,2),
			status_bayar VARCHAR(20),
			last_synced TIMESTAMP,
			UNIQUE (doc_no_p, item_code)
		)`,

		`CREATE TABLE IF NOT EXISTS sales (
			id BIGINT PRIMARY KEY DEFAULT nextval('sales_seq'),
			doc_date DATE,
			doc_no VARCHAR(255),
			code VARCHAR(50),
			par_name TEXT,
			dep_code VARCHAR(50),
			dep_name TEXT,
			ite_code VARCHAR(50),
			item_name TEXT,
			qty INTEGER,
			price NUMERIC(19,2),
			grand_total NUMERIC(19,2),
			hpp_satuan NUMERIC(19,6),
			total_hpp NUMERIC(19,6),
			laba_kotor NUMERIC(19,6),
			emp_code VARCHAR(50),
			emp_name TEXT,
			paid_total NUMERIC(19,2),
			status_bayar VARCHAR(20),
			last_synced TIMESTAMP,
			UNIQUE (doc_no, item_name)
		)`,

		// Upgrade path for already-provisioned databases: CREATE TABLE IF NOT
		// EXISTS is a no-op there, so add the new columns idempotently.
		`ALTER TABLE purchases ADD COLUMN IF NOT EXISTS paid_total NUMERIC(19,2)`,
		`ALTER TABLE purchases ADD COLUMN IF NOT EXISTS status_bayar VARCHAR(20)`,
		`ALTER TABLE sales ADD COLUMN IF NOT EXISTS paid_total NUMERIC(19,2)`,
		`ALTER TABLE sales ADD COLUMN IF NOT EXISTS status_bayar VARCHAR(20)`,

		`CREATE TABLE IF NOT EXISTS stok (
			id BIGINT PRIMARY KEY DEFAULT nextval('stok_seq'),
			item_code VARCHAR(100),
			item_name VARCHAR(500),
			normalized_item_name VARCHAR(500),
			kategori_nama VARCHAR(50),
			kategori_itemcode VARCHAR(50),
			final_stok INTEGER,
			harga_hpp NUMERIC(19,2),
			grand_total NUMERIC(19,2),
			warehouse VARCHAR(100),
			last_synced TIMESTAMP,
			UNIQUE (item_code, warehouse)
		)`,

		`CREATE TABLE IF NOT EXISTS item_serial_numbers (
			id BIGSERIAL PRIMARY KEY,
			tanggal TIMESTAMP,
			doc_id VARCHAR(255),
			user_name TEXT,
			item_name TEXT,
			sn VARCHAR(255),
			type VARCHAR(255),
			last_synced TIMESTAMP,
			UNIQUE (sn, doc_id, type)
		)`,

		`CREATE TABLE IF NOT EXISTS sync_settings (
			sync_key VARCHAR(100) PRIMARY KEY,
			sync_value VARCHAR(500)
		)`,

		// Pricelist is owned by the Google Sheet sync (see service
		// PricelistSyncer): it mirrors the "PRICELIST&MODAL" tab of the
		// spreadsheet. Columns match the JPA entity ported in model.Pricelist.
		`CREATE TABLE IF NOT EXISTS pricelist (
			id BIGINT PRIMARY KEY,
			final_pricelist NUMERIC(19,2),
			item_name VARCHAR(500),
			modal NUMERIC(19,2),
			spesifikasi VARCHAR(3000),
			normalized_item_name VARCHAR(500)
		)`,

		// Upgrade path for already-provisioned databases (CREATE TABLE IF NOT
		// EXISTS is a no-op there).
		`ALTER TABLE pricelist ADD COLUMN IF NOT EXISTS final_pricelist NUMERIC(19,2)`,
		`ALTER TABLE pricelist ADD COLUMN IF NOT EXISTS item_name VARCHAR(500)`,
		`ALTER TABLE pricelist ADD COLUMN IF NOT EXISTS modal NUMERIC(19,2)`,
		`ALTER TABLE pricelist ADD COLUMN IF NOT EXISTS spesifikasi VARCHAR(3000)`,
		`ALTER TABLE pricelist ADD COLUMN IF NOT EXISTS normalized_item_name VARCHAR(500)`,

		// Lookup indexes mirroring the @Index annotations on the entity. The
		// normalized column has no unique constraint, so the sync performs an
		// UPDATE-then-INSERT instead of ON CONFLICT.
		`CREATE INDEX IF NOT EXISTS idx_pricelist_item_name ON pricelist (item_name)`,
		`CREATE INDEX IF NOT EXISTS idx_pricelist_norm_item_name ON pricelist (normalized_item_name)`,

		// Align pricelist_seq with existing rows when the table was provisioned
		// by an earlier schema (e.g. Hibernate ddl-auto) whose sequence may not
		// cover current MAX(id) — otherwise nextval() would collide with the PK.
		// Idempotent: never moves the sequence backwards (GREATEST with the
		// current last_value).
		`SELECT setval('pricelist_seq', GREATEST(
			COALESCE((SELECT MAX(id) FROM pricelist), 0),
			COALESCE((SELECT last_value FROM pricelist_seq), 0)))`,
	}
	for _, s := range stmts {
		if _, err := db.ExecContext(ctx, s); err != nil {
			return fmt.Errorf("ensure schema (%s): %w", firstWords(s), err)
		}
	}
	return nil
}

// DropLegacyStatusConstraint mirrors DatabaseMigrationConfig: on startup it
// drops the old check constraint on penjadwalan_konfirmasi so the new enum
// value (DIBATALKAN) is accepted. Non-fatal when absent.
func DropLegacyStatusConstraint(ctx context.Context, db *sql.DB) error {
	const stmt = `ALTER TABLE penjadwalan_konfirmasi DROP CONSTRAINT IF EXISTS penjadwalan_konfirmasi_status_jadwal_check`
	_, err := db.ExecContext(ctx, stmt)
	if err != nil {
		return fmt.Errorf("drop constraint: %w", err)
	}
	return nil
}

func firstWords(s string) string {
	if len(s) > 60 {
		return s[:60]
	}
	return s
}
