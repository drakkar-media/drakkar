package database

import "context"

func (db *DB) ListCustomFormats(ctx context.Context) ([]CustomFormat, error) {
	rows, err := db.SQL.QueryContext(ctx,
		`SELECT id, name, pattern, score, enabled, coalesce(source,'custom') FROM custom_formats ORDER BY name ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CustomFormat
	for rows.Next() {
		var f CustomFormat
		if err := rows.Scan(&f.ID, &f.Name, &f.Pattern, &f.Score, &f.Enabled, &f.Source); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

func (db *DB) UpsertCustomFormat(ctx context.Context, f CustomFormat) (CustomFormat, error) {
	if f.Source == "" {
		f.Source = "custom"
	}
	var out CustomFormat
	err := db.SQL.QueryRowContext(ctx, `
		INSERT INTO custom_formats (name, pattern, score, enabled, source)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (id) DO UPDATE SET
		    name    = excluded.name,
		    pattern = excluded.pattern,
		    score   = excluded.score,
		    enabled = excluded.enabled,
		    source  = excluded.source
		RETURNING id, name, pattern, score, enabled, coalesce(source,'custom')`,
		f.Name, f.Pattern, f.Score, f.Enabled, f.Source,
	).Scan(&out.ID, &out.Name, &out.Pattern, &out.Score, &out.Enabled, &out.Source)
	return out, err
}

func (db *DB) UpdateCustomFormat(ctx context.Context, f CustomFormat) (CustomFormat, error) {
	if f.Source == "" {
		f.Source = "custom"
	}
	var out CustomFormat
	err := db.SQL.QueryRowContext(ctx, `
		UPDATE custom_formats
		SET name=$1, pattern=$2, score=$3, enabled=$4, source=$5
		WHERE id=$6
		RETURNING id, name, pattern, score, enabled, coalesce(source,'custom')`,
		f.Name, f.Pattern, f.Score, f.Enabled, f.Source, f.ID,
	).Scan(&out.ID, &out.Name, &out.Pattern, &out.Score, &out.Enabled, &out.Source)
	return out, err
}

func (db *DB) DeleteCustomFormat(ctx context.Context, id int64) error {
	_, err := db.SQL.ExecContext(ctx, `DELETE FROM custom_formats WHERE id=$1`, id)
	return err
}

// UpsertCustomFormatByName inserts or replaces a custom format matched by name.
// Used by the bulk-import endpoint so re-importing a TRaSH format with the same
// name updates the existing row instead of creating a duplicate.
func (db *DB) UpsertCustomFormatByName(ctx context.Context, f CustomFormat) (CustomFormat, error) {
	if f.Source == "" {
		f.Source = "custom"
	}
	var out CustomFormat
	err := db.SQL.QueryRowContext(ctx, `
		INSERT INTO custom_formats (name, pattern, score, enabled, source)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (name) DO UPDATE SET
		    pattern = excluded.pattern,
		    score   = excluded.score,
		    enabled = excluded.enabled,
		    source  = excluded.source
		RETURNING id, name, pattern, score, enabled, coalesce(source,'custom')`,
		f.Name, f.Pattern, f.Score, f.Enabled, f.Source,
	).Scan(&out.ID, &out.Name, &out.Pattern, &out.Score, &out.Enabled, &out.Source)
	return out, err
}
