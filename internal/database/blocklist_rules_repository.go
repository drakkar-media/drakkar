package database

import "context"

func (db *DB) ListReleaseBlockRules(ctx context.Context) ([]ReleaseBlockRule, error) {
	rows, err := db.SQL.QueryContext(ctx, `
		SELECT id, rule_type, pattern, media_type, action, score_penalty, enabled, source, note, created_at, updated_at
		FROM release_block_rules
		ORDER BY source DESC, rule_type, lower(pattern)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ReleaseBlockRule
	for rows.Next() {
		var r ReleaseBlockRule
		if err := rows.Scan(&r.ID, &r.Type, &r.Pattern, &r.MediaType, &r.Action,
			&r.ScorePenalty, &r.Enabled, &r.Source, &r.Note, &r.CreatedAt, &r.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (db *DB) UpsertReleaseBlockRule(ctx context.Context, r ReleaseBlockRule) (ReleaseBlockRule, error) {
	var out ReleaseBlockRule
	err := db.SQL.QueryRowContext(ctx, `
		INSERT INTO release_block_rules (rule_type, pattern, media_type, action, score_penalty, enabled, source, note)
		VALUES ($1,$2,$3,$4,$5,$6,'custom',$7)
		RETURNING id, rule_type, pattern, media_type, action, score_penalty, enabled, source, note, created_at, updated_at`,
		r.Type, r.Pattern, r.MediaType, r.Action, r.ScorePenalty, r.Enabled, r.Note,
	).Scan(&out.ID, &out.Type, &out.Pattern, &out.MediaType, &out.Action,
		&out.ScorePenalty, &out.Enabled, &out.Source, &out.Note, &out.CreatedAt, &out.UpdatedAt)
	return out, err
}

// UpdateReleaseBlockRule allows toggling enabled/note on any rule, and full edits on 'custom' rules.
func (db *DB) UpdateReleaseBlockRule(ctx context.Context, r ReleaseBlockRule) (ReleaseBlockRule, error) {
	var out ReleaseBlockRule
	err := db.SQL.QueryRowContext(ctx, `
		UPDATE release_block_rules SET
			enabled       = $2,
			note          = $3,
			rule_type     = CASE WHEN source = 'custom' THEN $4 ELSE rule_type END,
			pattern       = CASE WHEN source = 'custom' THEN $5 ELSE pattern END,
			media_type    = CASE WHEN source = 'custom' THEN $6 ELSE media_type END,
			action        = CASE WHEN source = 'custom' THEN $7 ELSE action END,
			score_penalty = CASE WHEN source = 'custom' THEN $8 ELSE score_penalty END,
			updated_at    = now()
		WHERE id = $1
		RETURNING id, rule_type, pattern, media_type, action, score_penalty, enabled, source, note, created_at, updated_at`,
		r.ID, r.Enabled, r.Note, r.Type, r.Pattern, r.MediaType, r.Action, r.ScorePenalty,
	).Scan(&out.ID, &out.Type, &out.Pattern, &out.MediaType, &out.Action,
		&out.ScorePenalty, &out.Enabled, &out.Source, &out.Note, &out.CreatedAt, &out.UpdatedAt)
	return out, err
}

// DeleteReleaseBlockRule removes a rule. Only custom rules can be deleted; default/trash rules are left.
func (db *DB) DeleteReleaseBlockRule(ctx context.Context, id int64) error {
	_, err := db.SQL.ExecContext(ctx, `DELETE FROM release_block_rules WHERE id = $1 AND source = 'custom'`, id)
	return err
}
