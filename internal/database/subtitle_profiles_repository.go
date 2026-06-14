package database

import "context"

func (db *DB) ListSubtitleProfiles(ctx context.Context) ([]SubtitleProfile, error) {
	rows, err := db.SQL.QueryContext(ctx,
		`SELECT id, name, languages, prefer_hearing_impaired, require_exact_language, is_default, created_at, updated_at
		 FROM subtitle_profiles ORDER BY name ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SubtitleProfile
	for rows.Next() {
		var p SubtitleProfile
		if err := rows.Scan(&p.ID, &p.Name, pgTextArrayScan(&p.Languages), &p.PreferHearingImpaired, &p.RequireExactLanguage, &p.IsDefault, &p.CreatedAt, &p.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (db *DB) CreateSubtitleProfile(ctx context.Context, p SubtitleProfile) (SubtitleProfile, error) {
	if p.Languages == nil {
		p.Languages = []string{}
	}
	if p.IsDefault {
		if _, err := db.SQL.ExecContext(ctx, `UPDATE subtitle_profiles SET is_default=false WHERE is_default=true`); err != nil {
			return SubtitleProfile{}, err
		}
	}
	var out SubtitleProfile
	err := db.SQL.QueryRowContext(ctx, `
		INSERT INTO subtitle_profiles (name, languages, prefer_hearing_impaired, require_exact_language, is_default)
		VALUES ($1, $2::text[], $3, $4, $5)
		RETURNING id, name, languages, prefer_hearing_impaired, require_exact_language, is_default, created_at, updated_at`,
		p.Name, pgTextArray(p.Languages), p.PreferHearingImpaired, p.RequireExactLanguage, p.IsDefault,
	).Scan(&out.ID, &out.Name, pgTextArrayScan(&out.Languages), &out.PreferHearingImpaired, &out.RequireExactLanguage, &out.IsDefault, &out.CreatedAt, &out.UpdatedAt)
	return out, err
}

func (db *DB) UpdateSubtitleProfile(ctx context.Context, p SubtitleProfile) (SubtitleProfile, error) {
	if p.Languages == nil {
		p.Languages = []string{}
	}
	if p.IsDefault {
		if _, err := db.SQL.ExecContext(ctx, `UPDATE subtitle_profiles SET is_default=false WHERE is_default=true AND id<>$1`, p.ID); err != nil {
			return SubtitleProfile{}, err
		}
	}
	var out SubtitleProfile
	err := db.SQL.QueryRowContext(ctx, `
		UPDATE subtitle_profiles
		SET name=$1, languages=$2::text[], prefer_hearing_impaired=$3, require_exact_language=$4, is_default=$5, updated_at=now()
		WHERE id=$6
		RETURNING id, name, languages, prefer_hearing_impaired, require_exact_language, is_default, created_at, updated_at`,
		p.Name, pgTextArray(p.Languages), p.PreferHearingImpaired, p.RequireExactLanguage, p.IsDefault, p.ID,
	).Scan(&out.ID, &out.Name, pgTextArrayScan(&out.Languages), &out.PreferHearingImpaired, &out.RequireExactLanguage, &out.IsDefault, &out.CreatedAt, &out.UpdatedAt)
	return out, err
}

func (db *DB) DeleteSubtitleProfile(ctx context.Context, id int64) error {
	_, err := db.SQL.ExecContext(ctx, `DELETE FROM subtitle_profiles WHERE id=$1`, id)
	return err
}
