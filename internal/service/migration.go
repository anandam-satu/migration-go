// Package service ports service/MigrationService.java and
// service/MigrationScheduler.java. The pricelist/Google Sheets feature was
// intentionally removed, so this service only migrates purchase, sales, stock
// and serial-number data from the legacy MySQL DB into PostgreSQL.
package service

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/anandam/api-migration/internal/db"
	"github.com/anandam/api-migration/internal/repository"
)

// batchSize mirrors MigrationService.BATCH_SIZE.
const batchSize = 1000

// syncSettingsKey is the change-detection watermark key.
const syncSettingsKey = "last_max_id"

// Service runs the data migrations. Safe to construct once per process.
type Service struct {
	pg     *sql.DB
	mysql  *sql.DB
	logger *slog.Logger
}

// New creates a migration Service. pg and mysql must already be configured.
func New(pg, mysql *sql.DB, logger *slog.Logger) *Service {
	return &Service{pg: pg, mysql: mysql, logger: logger}
}

// CheckAndTriggerMigration ports MigrationService.checkAndTriggerMigration():
// it compares the highest dbslog id in the legacy MySQL with the persisted
// watermark in sync_settings and, when new rows exist, runs all four
// migrations sequentially and stores the new watermark. MySQL idle
// connections are evicted afterwards.
func (s *Service) CheckAndTriggerMigration(ctx context.Context) {
	log := s.logger
	log.Info("Checking for database changes in dbslog...")
	defer db.EvictMySQLIdle(s.mysql)

	var currentMaxID sql.NullInt64
	err := s.mysql.QueryRowContext(ctx, "SELECT MAX(id) FROM dbslog").Scan(&currentMaxID)
	if err != nil {
		log.Error("Error reading dbslog max id", "err", err)
		return
	}
	if !currentMaxID.Valid || currentMaxID.Int64 == 0 {
		log.Warn("dbslog empty or max(id)=0; nothing to compare")
		return
	}

	lastMaxID, err := s.readWatermark(ctx)
	if err != nil {
		log.Error("Error reading sync_settings watermark", "err", err)
		return
	}

	if currentMaxID.Int64 <= lastMaxID {
		log.Info("No changes in dbtjurnal", "max_id", currentMaxID.Int64)
		return
	}

	log.Info("Changes detected (current: %d > last: %d). Triggering migration...",
		"current", currentMaxID.Int64, "last", lastMaxID)

	s.MigrateStock(ctx)
	s.MigrateSales(ctx)
	s.MigratePurchase(ctx)
	s.MigrateSN(ctx)

	if err := s.writeWatermark(ctx, currentMaxID.Int64); err != nil {
		log.Error("Failed to update last_max_id watermark", "err", err)
		return
	}
	log.Info("Auto-sync migration completed. last_max_id updated", "last_max_id", currentMaxID.Int64)
}

func (s *Service) readWatermark(ctx context.Context) (int64, error) {
	val, err := repository.GetSyncSetting(ctx, s.pg, syncSettingsKey)
	if err != nil {
		return 0, err
	}
	if val == nil {
		return 0, nil
	}
	n, err := strconv.ParseInt(*val, 10, 64)
	if err != nil {
		s.logger.Warn("Unparsable watermark value; resetting to 0", "value", *val)
		return 0, nil
	}
	return n, nil
}

func (s *Service) writeWatermark(ctx context.Context, maxID int64) error {
	return repository.UpsertSyncSetting(ctx, s.pg, syncSettingsKey, strconv.FormatInt(maxID, 10))
}

// nowTimestamp renders a timestamp the same way for INSERT and the stale-row
// DELETE, so this run's rows (last_synced == syncTime) are never deleted.
func nowTimestamp() string {
	return time.Now().Format("2006-01-02 15:04:05.000000")
}

// flatten turns per-row value slices into one argument list for Exec.
func flatten(rows [][]any) []any {
	total := 0
	for _, r := range rows {
		total += len(r)
	}
	out := make([]any, 0, total)
	for _, r := range rows {
		out = append(out, r...)
	}
	return out
}

// valuesTuple writes "(nextval('seq'), $1, $2, ...)" for row i, where
// cols is the number of bound parameters per row.
func valuesTuple(b *strings.Builder, row, cols int, seq string) {
	b.WriteString("(nextval('")
	b.WriteString(seq)
	b.WriteString("')")
	for c := 0; c < cols; c++ {
		b.WriteString(fmt.Sprintf(", $%d", row*cols+c+1))
	}
	b.WriteString(")")
}

// summary logs and returns a human result string mirroring the Java
// "=== SELESAI === Total Data: N. Waktu: X detik." logs.
func (s *Service) summary(prefix string, total int, start time.Time) string {
	d := time.Since(start).Milliseconds() / 1000
	res := fmt.Sprintf("=== %s SELESAI === Total: %d. Waktu: %d detik.", prefix, total, d)
	s.logger.Info(res)
	return res
}
