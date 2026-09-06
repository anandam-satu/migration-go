package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/joho/godotenv"
)

func writeDotEnv(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "test.env")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadDotEnvLoadsNewKeysAndKeepsExisting(t *testing.T) {
	p := writeDotEnv(t, "DB_PG_PORT=7777\n"+
		"GOOGLE_SERVICE_ACCOUNT_FILE=C:/secrets/service_account.json\n"+
		"PRICELIST_SYNC_INTERVAL=30s\n")
	t.Setenv("ENV_FILE", p)
	t.Setenv("DB_PG_PORT", "5432") // already in the process env -> file must NOT win
	t.Cleanup(func() {
		os.Unsetenv("GOOGLE_SERVICE_ACCOUNT_FILE")
		os.Unsetenv("PRICELIST_SYNC_INTERVAL")
	})

	if err := LoadDotEnv(); err != nil {
		t.Fatalf("LoadDotEnv: %v", err)
	}
	if got := os.Getenv("DB_PG_PORT"); got != "5432" {
		t.Errorf("DB_PG_PORT = %q, want 5432 (process env wins over .env)", got)
	}
	if got := os.Getenv("GOOGLE_SERVICE_ACCOUNT_FILE"); got != "C:/secrets/service_account.json" {
		t.Errorf("GOOGLE_SERVICE_ACCOUNT_FILE = %q, want value loaded from .env", got)
	}
	if got := os.Getenv("PRICELIST_SYNC_INTERVAL"); got != "30s" {
		t.Errorf("PRICELIST_SYNC_INTERVAL = %q, want 30s", got)
	}
}

// Unquoted Windows backslash paths are taken literally by godotenv, so the
// recommended .env form keeps them as-is (double quotes would eat the
// backslashes as escapes).
func TestLoadDotEnvWindowsBackslashPathUnquoted(t *testing.T) {
	p := writeDotEnv(t, "GOOGLE_SERVICE_ACCOUNT_FILE=C:\\secrets\\service_account.json\n")
	t.Setenv("ENV_FILE", p)
	t.Cleanup(func() { os.Unsetenv("GOOGLE_SERVICE_ACCOUNT_FILE") })

	if err := LoadDotEnv(); err != nil {
		t.Fatalf("LoadDotEnv: %v", err)
	}
	const want = `C:\secrets\service_account.json`
	if got := os.Getenv("GOOGLE_SERVICE_ACCOUNT_FILE"); got != want {
		t.Errorf("GOOGLE_SERVICE_ACCOUNT_FILE = %q, want %q (backslashes preserved)", got, want)
	}
}

func TestLoadDotEnvExplicitMissingFileErrors(t *testing.T) {
	t.Setenv("ENV_FILE", filepath.Join(t.TempDir(), "does-not-exist.env"))
	if err := LoadDotEnv(); err == nil {
		t.Fatal("explicitly set ENV_FILE that is missing must return an error")
	}
}

// Without ENV_FILE the loader falls back to ".env" in the working directory;
// when that default file is absent it is not an error (defaults apply).
func TestLoadDotEnvDefaultMissingIsIgnored(t *testing.T) {
	t.Setenv("ENV_FILE", "")
	if err := LoadDotEnv(); err != nil {
		t.Fatalf("missing default .env must not be an error, got %v", err)
	}
}

func TestLoadDotEnvMalformedReturnsError(t *testing.T) {
	// Unquoted single quotes with a trailing !A:D is a godotenv parse error and
	// discards the whole file — it must be surfaced, not silently ignored.
	p := writeDotEnv(t, "R='PRICELIST&MODAL'!A:D\n")
	t.Setenv("ENV_FILE", p)
	if err := LoadDotEnv(); err == nil {
		t.Fatal("malformed .env must return an error")
	}
}

func TestLoadDotEnvFeedsConfigLoad(t *testing.T) {
	for _, k := range []string{"SERVER_PORT", "DB_PG_NAME", "PRICELIST_SYNC_INTERVAL"} {
		if _, ok := os.LookupEnv(k); ok {
			t.Skipf("%s already set in the environment; cannot verify .env -> Load()", k)
		}
	}
	p := writeDotEnv(t, "SERVER_PORT=9123\nDB_PG_NAME=cfgdb\nPRICELIST_SYNC_INTERVAL=15s\n")
	t.Setenv("ENV_FILE", p)
	t.Cleanup(func() {
		os.Unsetenv("SERVER_PORT")
		os.Unsetenv("DB_PG_NAME")
		os.Unsetenv("PRICELIST_SYNC_INTERVAL")
	})

	if err := LoadDotEnv(); err != nil {
		t.Fatalf("LoadDotEnv: %v", err)
	}
	cfg := Load()
	if cfg.Port != "9123" {
		t.Errorf("cfg.Port = %q, want 9123 (loaded from .env)", cfg.Port)
	}
	if cfg.Postgres.DBName != "cfgdb" {
		t.Errorf("cfg.Postgres.DBName = %q, want cfgdb", cfg.Postgres.DBName)
	}
	if cfg.GoogleSheet.SyncInterval != 15*time.Second {
		t.Errorf("cfg.GoogleSheet.SyncInterval = %v, want 15s", cfg.GoogleSheet.SyncInterval)
	}
}

// The shipped .env.example must parse cleanly with godotenv (protects against
// someone introducing a quoting trap like the PRICELIST_SHEET_RANGE single
// quotes) and expose the documented keys.
func TestDotEnvExampleParses(t *testing.T) {
	example := filepath.Join("..", "..", ".env.example")
	env, err := godotenv.Read(example)
	if err != nil {
		t.Fatalf(".env.example must parse: %v", err)
	}
	for _, k := range []string{"APP_MYSQL_ENABLED", "DB_MYSQL_HOST", "DB_PG_HOST", "DB_PG_PASSWORD",
		"SERVER_PORT", "GOOGLE_SERVICE_ACCOUNT_FILE", "PRICELIST_SPREADSHEET_ID", "PRICELIST_SYNC_INTERVAL"} {
		if _, ok := env[k]; !ok {
			t.Errorf(".env.example is missing key %s", k)
		}
	}
	if got := env["GOOGLE_SERVICE_ACCOUNT_FILE"]; got != "C:/secrets/service_account.json" {
		t.Errorf("GOOGLE_SERVICE_ACCOUNT_FILE example = %q, want C:/secrets/service_account.json", got)
	}
	if _, ok := env["PRICELIST_SHEET_RANGE"]; ok {
		t.Error("PRICELIST_SHEET_RANGE should stay commented out (uses the built-in default)")
	}
}
