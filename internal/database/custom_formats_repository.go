package database

import "context"

func (db *DB) ListCustomFormats(ctx context.Context) ([]CustomFormat, error) {
	rows, err := db.SQL.QueryContext(ctx,
		`SELECT id, name, pattern, score, enabled FROM custom_formats ORDER BY name ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CustomFormat
	for rows.Next() {
		var f CustomFormat
		if err := rows.Scan(&f.ID, &f.Name, &f.Pattern, &f.Score, &f.Enabled); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

func (db *DB) UpsertCustomFormat(ctx context.Context, f CustomFormat) (CustomFormat, error) {
	var out CustomFormat
	err := db.SQL.QueryRowContext(ctx, `
		INSERT INTO custom_formats (name, pattern, score, enabled)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (id) DO UPDATE SET
		    name    = excluded.name,
		    pattern = excluded.pattern,
		    score   = excluded.score,
		    enabled = excluded.enabled
		RETURNING id, name, pattern, score, enabled`,
		f.Name, f.Pattern, f.Score, f.Enabled,
	).Scan(&out.ID, &out.Name, &out.Pattern, &out.Score, &out.Enabled)
	return out, err
}

func (db *DB) UpdateCustomFormat(ctx context.Context, f CustomFormat) (CustomFormat, error) {
	var out CustomFormat
	err := db.SQL.QueryRowContext(ctx, `
		UPDATE custom_formats
		SET name=$1, pattern=$2, score=$3, enabled=$4
		WHERE id=$5
		RETURNING id, name, pattern, score, enabled`,
		f.Name, f.Pattern, f.Score, f.Enabled, f.ID,
	).Scan(&out.ID, &out.Name, &out.Pattern, &out.Score, &out.Enabled)
	return out, err
}

func (db *DB) DeleteCustomFormat(ctx context.Context, id int64) error {
	_, err := db.SQL.ExecContext(ctx, `DELETE FROM custom_formats WHERE id=$1`, id)
	return err
}
