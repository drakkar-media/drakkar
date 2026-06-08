package database

import (
	"context"
	"database/sql"
	"encoding/json"
)

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
