// Package server ports the Spring web layer: MigrationController, the
// MigrationScheduler scheduling loop, and the startup ConfigurationRunners
// (constraint fix + schema bootstrap).
package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/anandam/api-migration/internal/config"
	"github.com/anandam/api-migration/internal/db"
	"github.com/anandam/api-migration/internal/service"
)

const disabledMessage = "Migrasi tidak aktif. Set app.mysql.enabled=true di application.properties lalu restart aplikasi."

// Run starts the HTTP API (and, when MySQL is enabled, the background
// scheduler) and blocks until ctx is cancelled or the server fails.
func Run(ctx context.Context, cfg config.Config, svc *service.Service, pg, mysql *sql.DB, logger *slog.Logger) error {
	// Startup runners (port of DatabaseMigrationConfig + Hibernate ddl-auto).
	startupCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	runStartupTasks(startupCtx, pg, logger)
	cancel()

	// Scheduler (MigrationScheduler): fixedDelay = 30s when MySQL is enabled.
	if cfg.MySQL.Enabled {
		go runScheduler(ctx, svc, logger)
	}

	// Pricelist sync: pulls the Google Sheet "PRICELIST&MODAL" into
	// public.pricelist every cfg.GoogleSheet.SyncInterval (default 1 minute).
	// Independent from MySQL — it only needs PostgreSQL + the service-account
	// file configured via GOOGLE_SERVICE_ACCOUNT_FILE.
	var pl *service.PricelistSyncer
	if cfg.GoogleSheet.Enabled() {
		pl = service.NewPricelistSyncer(pg, cfg.GoogleSheet, logger)
		go runPricelistScheduler(ctx, pl, logger, cfg.GoogleSheet.SyncInterval)
	}

	mux := http.NewServeMux()
	h := &handler{svc: svc, enabled: cfg.MySQL.Enabled, logger: logger}

	mux.HandleFunc("POST /api/v1/migration/purchase", h.trigger(h.migratePurchase, "Purchase", "Proses migrasi Purchase berjalan di background. Cek log server."))
	mux.HandleFunc("POST /api/v1/migration/sales", h.trigger(h.migrateSales, "Sales", "Migrasi Sales berjalan di background..."))
	mux.HandleFunc("POST /api/v1/migration/stock", h.trigger(h.migrateStock, "Stock", "Migrasi Stok berjalan di background..."))
	mux.HandleFunc("POST /api/v1/migration/sn", h.trigger(h.migrateSN, "SN", "Migrasi Serial Number berjalan di background..."))
	mux.HandleFunc("POST /api/v1/migration/pricelist", func(w http.ResponseWriter, r *http.Request) {
		if pl == nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"status": 503, "message": "Pricelist sync tidak aktif. Set GOOGLE_SERVICE_ACCOUNT_FILE lalu restart aplikasi."})
			return
		}
		// @Async -> fire-and-forget; do not tie the job to the request ctx.
		go func() {
			if err := pl.Sync(context.Background()); err != nil {
				logger.Error("Pricelist sync gagal (HTTP trigger)", "err", err)
			}
		}()
		logger.Info("Pricelist sync triggered via HTTP")
		writeJSON(w, http.StatusOK, map[string]any{"status": 200, "message": "Sinkronisasi pricelist dari Google Sheet berjalan di background..."})
	})

	srv := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		logger.Info("HTTP server listening", "addr", srv.Addr)
		errCh <- srv.ListenAndServe()
	}()

	select {
	case err := <-errCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	}
}

// runStartupTasks runs the schema bootstrap (DatabaseMigrationConfig
// constraint fix + Hibernate ddl-auto equivalent, which now also creates the
// pricelist table/sequence). Both are best-effort: PostgreSQL may be
// reachable later or already provisioned.
func runStartupTasks(ctx context.Context, pg *sql.DB, logger *slog.Logger) {
	if err := db.EnsurePostgresSchema(ctx, pg); err != nil {
		logger.Warn("PostgreSQL schema bootstrap skipped (will rely on existing schema)", "err", err)
	} else {
		logger.Info("PostgreSQL schema bootstrap completed")
	}

	// DatabaseMigrationConfig: drop old check constraint for new enum value.
	if err := db.DropLegacyStatusConstraint(ctx, pg); err != nil {
		logger.Warn("Could not drop constraint (it might not exist)", "err", err)
	} else {
		logger.Info("Constraint 'penjadwalan_konfirmasi_status_jadwal_check' has been dropped successfully.")
	}
}

// handler serves the migration trigger endpoints.
type handler struct {
	svc     *service.Service
	enabled bool
	logger  *slog.Logger
}

// trigger builds a POST handler that starts a migration in the background and
// returns 200, mirroring @Async on MigrationController.
func (h *handler) trigger(run func(context.Context) string, job, message string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !h.enabled {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"status": 503, "message": disabledMessage})
			return
		}
		// @Async -> fire-and-forget; do not tie the job to the request ctx.
		go run(context.Background())
		h.logger.Info("Migration triggered via HTTP", "job", job)
		writeJSON(w, http.StatusOK, map[string]any{"status": 200, "message": message})
	}
}

func (h *handler) migratePurchase(ctx context.Context) string { return h.svc.MigratePurchase(ctx) }
func (h *handler) migrateSales(ctx context.Context) string    { return h.svc.MigrateSales(ctx) }
func (h *handler) migrateStock(ctx context.Context) string    { return h.svc.MigrateStock(ctx) }
func (h *handler) migrateSN(ctx context.Context) string       { return h.svc.MigrateSN(ctx) }

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
