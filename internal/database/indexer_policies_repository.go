package database

import "context"

// ListIndexerPolicies returns all indexer policies ordered alphabetically by
// indexer name, including disabled ones.
//
// Errors:
//   - error: any failure executing or scanning the underlying query.
func (db *DB) ListIndexerPolicies(ctx context.Context) ([]IndexerPolicy, error) {
	rows, err := db.SQL.QueryContext(ctx,
		`SELECT id, indexer_name, score_modifier, enabled, note, created_at, updated_at
		 FROM indexer_policies ORDER BY indexer_name ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []IndexerPolicy
	for rows.Next() {
		var p IndexerPolicy
		if err := rows.Scan(&p.ID, &p.IndexerName, &p.ScoreModifier, &p.Enabled, &p.Note, &p.CreatedAt, &p.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// UpsertIndexerPolicy inserts a new policy or updates the existing one for
// p.IndexerName, which is unique.
//
// Returns:
//   - IndexerPolicy: the row as stored, with CreatedAt/UpdatedAt populated by
//     the database.
func (db *DB) UpsertIndexerPolicy(ctx context.Context, p IndexerPolicy) (IndexerPolicy, error) {
	var out IndexerPolicy
	err := db.SQL.QueryRowContext(ctx, `
		INSERT INTO indexer_policies (indexer_name, score_modifier, enabled, note)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (indexer_name) DO UPDATE SET
		    score_modifier = excluded.score_modifier,
		    enabled        = excluded.enabled,
		    note           = excluded.note,
		    updated_at     = now()
		RETURNING id, indexer_name, score_modifier, enabled, note, created_at, updated_at`,
		p.IndexerName, p.ScoreModifier, p.Enabled, p.Note,
	).Scan(&out.ID, &out.IndexerName, &out.ScoreModifier, &out.Enabled, &out.Note, &out.CreatedAt, &out.UpdatedAt)
	return out, err
}

// UpdateIndexerPolicy overwrites the policy identified by p.ID with the
// provided field values and refreshes UpdatedAt.
//
// Errors:
//   - sql.ErrNoRows: no indexer policy exists with the given ID.
func (db *DB) UpdateIndexerPolicy(ctx context.Context, p IndexerPolicy) (IndexerPolicy, error) {
	var out IndexerPolicy
	err := db.SQL.QueryRowContext(ctx, `
		UPDATE indexer_policies
		SET score_modifier=$1, enabled=$2, note=$3, updated_at=now()
		WHERE id=$4
		RETURNING id, indexer_name, score_modifier, enabled, note, created_at, updated_at`,
		p.ScoreModifier, p.Enabled, p.Note, p.ID,
	).Scan(&out.ID, &out.IndexerName, &out.ScoreModifier, &out.Enabled, &out.Note, &out.CreatedAt, &out.UpdatedAt)
	return out, err
}

// DeleteIndexerPolicy removes the indexer policy identified by id.
//
// It is a no-op if no row matches id.
func (db *DB) DeleteIndexerPolicy(ctx context.Context, id int64) error {
	_, err := db.SQL.ExecContext(ctx, `DELETE FROM indexer_policies WHERE id=$1`, id)
	return err
}

// LoadIndexerPolicyMap returns a map of indexerName → scoreModifier for all enabled policies.
// Used by the workflow service to apply per-indexer score adjustments during ranking.
func (db *DB) LoadIndexerPolicyMap(ctx context.Context) (map[string]int, error) {
	rows, err := db.SQL.QueryContext(ctx,
		`SELECT indexer_name, score_modifier FROM indexer_policies WHERE enabled=true`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]int)
	for rows.Next() {
		var name string
		var mod int
		if err := rows.Scan(&name, &mod); err != nil {
			return nil, err
		}
		out[name] = mod
	}
	return out, rows.Err()
}
