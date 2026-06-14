package database

import (
	"context"
	"database/sql"
)

func (db *DB) ListBlocklistItems(ctx context.Context) ([]BlocklistItemSummary, error) {
	rows, err := db.SQL.QueryContext(ctx, `
		select id, key, reason, expires_at
		from blocklist_items
		where expires_at is null or expires_at > now()
		order by id desc`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []BlocklistItemSummary
	for rows.Next() {
		var (
			item      BlocklistItemSummary
			expiresAt sql.NullTime
		)
		if err := rows.Scan(&item.ID, &item.Key, &item.Reason, &expiresAt); err != nil {
			return nil, err
		}
		if expiresAt.Valid {
			value := expiresAt.Time
			item.ExpiresAt = &value
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (db *DB) DeleteBlocklistItem(ctx context.Context, id int64) error {
	result, err := db.SQL.ExecContext(ctx, `
		delete from blocklist_items
		where id = $1`, id,
	)
	if err != nil {
		return err
	}
	if rows, err := result.RowsAffected(); err == nil && rows == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (db *DB) DeleteAllBlocklistItems(ctx context.Context) (int, error) {
	result, err := db.SQL.ExecContext(ctx, `
		delete from blocklist_items
		where expires_at is null or expires_at > now()`)
	if err != nil {
		return 0, err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	return int(rows), nil
}

func (db *DB) DeleteBlocklistItemsByReason(ctx context.Context, reason string) (int, error) {
	result, err := db.SQL.ExecContext(ctx, `
		delete from blocklist_items
		where reason = $1 and (expires_at is null or expires_at > now())`, reason,
	)
	if err != nil {
		return 0, err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	return int(rows), nil
}
