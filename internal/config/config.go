// Package config mirrors the settings previously declared in
// src/main/resources/application.properties. Every value can be overridden
// with the same environment variables the Spring Boot app used.
package config

import (
	"errors"
	"os"
	"strconv"
	"time"

	"github.com/joho/godotenv"
)

// MySQL holds connection/pool settings for the legacy MyBiz database
// (spring.datasource.mysql.* in the original application.properties).
type MySQL struct {
	Enabled     bool
	Host        string
	Port        string
	DBName      string
	User        string
	Password    string
	MaxOpen     int
	MaxIdle     int
	ConnTimeout time.Duration
	IdleTimeout time.Duration
	MaxLifetime time.Duration
}

// Postgres holds connection/pool settings for the main "stok-anandam"
// database (spring.datasource.pg.*).
type Postgres struct {
	Host        string
	Port        string
	DBName      string
	User        string
	Password    string
	MaxOpen     int
	MaxIdle     int
	MaxLifetime time.Duration
}

// GoogleSheet holds the settings for the pricelist sync that reads the
// "PRICELIST&MODAL" tab (columns A=spesifikasi, B=item_name, C=modal,
// D=final_pricelist) of a Google Spreadsheet into public.pricelist.
type GoogleSheet struct {
	// ServiceAccountFile is the path to the Google service-account JSON used
	// to authenticate (scope spreadsheets.readonly). Empty disables the
	// pricelist feature. Read from GOOGLE_SERVICE_ACCOUNT_FILE.
	ServiceAccountFile string
	// SpreadsheetID is the Google Spreadsheet id. Read from
	// PRICELIST_SPREADSHEET_ID; defaults to the Anandam Store pricelist sheet.
	SpreadsheetID string
	// TabRange is the values range to fetch. Read from PRICELIST_SHEET_RANGE.
	TabRange string
	// SyncInterval is how often the sheet is pulled (default 1 minute). Read
	// from PRICELIST_SYNC_INTERVAL (a Go duration like "1m", "60s").
	SyncInterval time.Duration
}

// Enabled reports whether the pricelist feature is switched on (a service
// account file has been configured).
func (g GoogleSheet) Enabled() bool { return g.ServiceAccountFile != "" }

// Config is the root runtime configuration.
type Config struct {
	MySQL        MySQL
	Postgres     Postgres
	GoogleSheet  GoogleSheet
	LegacySchema string // migration.legacy-schema (informational; SQL is ported verbatim)
	Port         string // server.port
}

func env(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return def
}

func envBool(key string, def bool) bool {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return def
	}
	return b
}

func envDuration(key string, def time.Duration) time.Duration {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return def
	}
	return d
}

// envFileKey optionally points LoadDotEnv at a non-default .env file.
const envFileKey = "ENV_FILE"

// LoadDotEnv reads key=value pairs from a .env file into the process
// environment so config.Load() can pick them up. It reads the file named by
// ENV_FILE; when ENV_FILE is unset it falls back to ".env" in the current
// working directory and a missing file is not an error (the built-in defaults
// in Load() apply). Variables already present in the process environment are
// never overridden (godotenv Load semantics), so shell/launcher config keeps
// precedence over the file. Any other failure — including an ENV_FILE that was
// explicitly set but does not exist, or a malformed .env — is returned so the
// caller can stop instead of silently booting on default credentials.
func LoadDotEnv() error {
	path := os.Getenv(envFileKey)
	explicit := path != ""
	if !explicit {
		path = ".env"
	}
	if err := godotenv.Load(path); err != nil {
		if errors.Is(err, os.ErrNotExist) && !explicit {
			return nil
		}
		return err
	}
	return nil
}

// Load reads the configuration from the environment. Defaults match the
// original application.properties exactly.
func Load() Config {
	return Config{
		MySQL: MySQL{
			Enabled:     envBool("APP_MYSQL_ENABLED", true),
			Host:        env("DB_MYSQL_HOST", "192.168.1.246"),
			Port:        env("DB_MYSQL_PORT", "3307"),
			DBName:      env("DB_MYSQL_NAME", "anandamid26"),
			User:        env("DB_MYSQL_USER", "root"),
			Password:    os.Getenv("DB_MYSQL_PASSWORD"), // default: empty
			MaxOpen:     5,
			MaxIdle:     0, // minimum-idle=0 (see app docs: avoid Sleep conns on MyBiz)
			ConnTimeout: 60 * time.Second,
			IdleTimeout: 60 * time.Second,
			MaxLifetime: 30 * time.Minute,
		},
		Postgres: Postgres{
			Host:        env("DB_PG_HOST", "192.168.1.176"),
			Port:        env("DB_PG_PORT", "5432"),
			DBName:      env("DB_PG_NAME", "stok-anandam"),
			User:        env("DB_PG_USER", "anandamstok"),
			Password:    env("DB_PG_PASSWORD", "Letmein99+"),
			MaxOpen:     10, // hikari.maximum-pool-size
			MaxIdle:     10, // Hikari default minimum-idle == maximum-pool-size
			MaxLifetime: 30 * time.Minute,
		},
		LegacySchema: env("MIGRATION_LEGACY_SCHEMA", "anandamid26"),
		Port:         env("SERVER_PORT", "9089"),
		GoogleSheet: GoogleSheet{
			// No default file: the pricelist sync only starts when the operator
			// points GOOGLE_SERVICE_ACCOUNT_FILE at a service-account JSON that
			// has been shared with the spreadsheet.
			ServiceAccountFile: os.Getenv("GOOGLE_SERVICE_ACCOUNT_FILE"),
			SpreadsheetID:      env("PRICELIST_SPREADSHEET_ID", "173w5Y8hynv8lOphrsjtCx0tc8CJIQThuLrMIttwtw30"),
			TabRange:           env("PRICELIST_SHEET_RANGE", "'PRICELIST&MODAL'!A:D"),
			SyncInterval:       envDuration("PRICELIST_SYNC_INTERVAL", time.Minute),
		},
	}
}
