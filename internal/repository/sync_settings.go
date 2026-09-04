package repository

import (
	"context"
	"database/sql"
)

// GetSyncSetting reads a row from sync_settings; returns (nil, nil) when the
// key is not present (mirrors Optional.empty()).
func GetSyncSetting(ctx context.Context, db *sql.DB, key string) (*string, error) {
	var v sql.NullString
	err := db.QueryRowContext(ctx, "SELECT sync_value FROM sync_settings WHERE sync_key = $1", key).Scan(&v)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !v.Valid {
		return nil, nil
	}
	s := v.String
	return &s, nil
}

// UpsertSyncSetting inserts or updates one sync_settings row.
func UpsertSyncSetting(ctx context.Context, db *sql.DB, key, value string) error {
	_, err := db.ExecContext(ctx,
		`INSERT INTO sync_settings (sync_key, sync_value) VALUES ($1, $2)
		 ON CONFLICT (sync_key) DO UPDATE SET sync_value = EXCLUDED.sync_value`,
		key, value)
	return err
}
