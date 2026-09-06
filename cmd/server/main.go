// Command server runs the api-migration worker: a PostgreSQL sink + legacy
// MySQL source migration service (purchase/sales/stock/SN), an HTTP trigger
// API (POST /api/v1/migration/{purchase|sales|stock|sn|pricelist}) and two
// background schedulers (a 30-second auto-sync and a 1-minute Google Sheet
// pricelist sync). This is the Go port of the Spring Boot application.
package main

import (
	"context"
	"database/sql"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/anandam/api-migration/internal/config"
	"github.com/anandam/api-migration/internal/db"
	"github.com/anandam/api-migration/internal/server"
	"github.com/anandam/api-migration/internal/service"
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	slog.SetDefault(logger)

	// Load .env (if present) before reading configuration. Process env vars
	// already set still win over the file. Fail fast on an explicitly named
	// ENV_FILE or a malformed .env: continuing on default DB credentials
	// (which point at real hosts) would be worse than not starting.
	if err := config.LoadDotEnv(); err != nil {
		logger.Error("Failed to load .env configuration", "err", err)
		os.Exit(1)
	}

	cfg := config.Load()

	pg, err := db.OpenPostgres(cfg.Postgres)
	if err != nil {
		logger.Error("Failed to configure PostgreSQL", "err", err)
		os.Exit(1)
	}

	var mysql *sql.DB
	if cfg.MySQL.Enabled {
		mysql, err = db.OpenMySQL(cfg.MySQL)
		if err != nil {
			logger.Error("Failed to configure MySQL", "err", err)
			os.Exit(1)
		}
	}

	svc := service.New(pg, mysql, logger)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := server.Run(ctx, cfg, svc, pg, mysql, logger); err != nil {
		logger.Error("Server stopped with error", "err", err)
		os.Exit(1)
	}
	logger.Info("Server stopped cleanly")
}
