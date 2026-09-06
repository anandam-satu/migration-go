package server

import (
	"context"
	"log/slog"
	"time"

	"github.com/anandam/api-migration/internal/service"
)

const (
	schedulerInterval = 30 * time.Second
	quietStart        = "21:15" // istirahat malam mulai
	quietEnd          = "07:55" // istirahat malam selesai
)

// runScheduler mirrors @Scheduled(fixedDelay = 30000) on
// MigrationScheduler.scheduleMigration(): wait interval, then run unless the
// local time is inside the nightly quiet window (21:15 s/d 07:55).
func runScheduler(ctx context.Context, svc *service.Service, logger *slog.Logger) {
	ticker := time.NewTicker(schedulerInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if inQuietWindow(time.Now()) {
				continue
			}
			logger.Info("Menjalankan migrasi data...")
			svc.CheckAndTriggerMigration(ctx)
		}
	}
}

// inQuietWindow ports the Java guard on MigrationScheduler:
//
//	now.isAfter(21:15) || now.isBefore(07:55)
func inQuietWindow(now time.Time) bool {
	qs, _ := time.Parse("15:04", quietStart)
	qe, _ := time.Parse("15:04", quietEnd)
	cur, _ := time.Parse("15:04", now.Format("15:04"))
	return cur.After(qs) || cur.Before(qe)
}

// runPricelistScheduler pulls the Google Sheet into public.pricelist once at
// startup and then every interval (default 1 minute, PRICELIST_SYNC_INTERVAL).
// Unlike the MySQL migration scheduler this is not gated by the nightly quiet
// window: the pull is a lightweight read the operator asked to run
// unconditionally every minute.
func runPricelistScheduler(ctx context.Context, syncer *service.PricelistSyncer, logger *slog.Logger, interval time.Duration) {
	if interval <= 0 {
		interval = time.Minute
	}
	run := func() {
		logger.Info("Menjalankan sinkronisasi pricelist dari Google Sheet...")
		if err := syncer.Sync(ctx); err != nil {
			logger.Error("Pricelist sync gagal", "err", err)
		}
	}
	run()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			run()
		}
	}
}
