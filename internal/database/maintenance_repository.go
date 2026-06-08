package database

import (
	"context"
	"database/sql"
	"errors"
)

func (db *DB) ListSymlinkPublicationRecords(ctx context.Context) ([]SymlinkPublicationRecord, error) {
	rows, err := db.SQL.QueryContext(ctx, `
		select id, library_path, target_path
		from symlink_publications
		order by id asc`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []SymlinkPublicationRecord
	for rows.Next() {
		var item SymlinkPublicationRecord
		if err := rows.Scan(&item.ID, &item.LibraryPath, &item.TargetPath); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (db *DB) DeleteSymlinkPublication(ctx context.Context, publicationID int64) error {
	_, err := db.SQL.ExecContext(ctx, `delete from symlink_publications where id = $1`, publicationID)
	return err
}

func (db *DB) TouchMaintenanceCursor(ctx context.Context, taskName string, cursor string) error {
	_, err := db.SQL.ExecContext(ctx, `
		insert into maintenance_cursors (task_name, cursor)
		values ($1, $2)
		on conflict (task_name)
		do update set cursor = excluded.cursor, updated_at = now()`, taskName, cursor,
	)
	return err
}

func (db *DB) GetMaintenanceCursor(ctx context.Context, taskName string) (string, error) {
	var cursor string
	err := db.SQL.QueryRowContext(ctx, `
		select cursor
		from maintenance_cursors
		where task_name = $1`, taskName,
	).Scan(&cursor)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return cursor, nil
}

func (db *DB) ListMaintenanceCursors(ctx context.Context) ([]MaintenanceCursorEntry, error) {
	rows, err := db.SQL.QueryContext(ctx, `
		select task_name, cursor, updated_at
		from maintenance_cursors
		order by task_name asc`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MaintenanceCursorEntry
	for rows.Next() {
		var item MaintenanceCursorEntry
		if err := rows.Scan(&item.TaskName, &item.Cursor, &item.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}
