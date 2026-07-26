package database

import (
	"context"
	"database/sql"
	"encoding/json"
)

// GetAppSetting looks up a JSON-encoded application setting by key and
// unmarshals it into dst.
//
// Parameters:
//   - key: unique app_settings row key.
//   - dst: pointer to unmarshal the stored JSON value into; left untouched
//     when the key does not exist.
//
// Returns:
//   - bool: whether a setting for key was found. False (with a nil error)
//     when there is no matching row, so callers can distinguish "not set"
//     from a real error.
func (db *DB) GetAppSetting(ctx context.Context, key string, dst any) (bool, error) {
	var raw []byte
	err := db.SQL.QueryRowContext(ctx, `
		select value
		from app_settings
		where key = $1`, key,
	).Scan(&raw)
	if err != nil {
		if err == sql.ErrNoRows {
			return false, nil
		}
		return false, err
	}
	if err := json.Unmarshal(raw, dst); err != nil {
		return false, err
	}
	return true, nil
}

// PutAppSetting JSON-encodes value and upserts it under key, creating the row
// if it doesn't exist and refreshing updated_at either way.
func (db *DB) PutAppSetting(ctx context.Context, key string, value any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	_, err = db.SQL.ExecContext(ctx, `
		insert into app_settings (key, value, updated_at)
		values ($1, $2::jsonb, now())
		on conflict (key)
		do update set value = excluded.value, updated_at = now()`, key, string(raw),
	)
	return err
}
