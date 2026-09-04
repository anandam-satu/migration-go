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
