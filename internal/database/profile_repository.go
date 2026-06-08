package database

import (
	"context"
	"time"
)

type QualityProfile struct {
	ID           int64     `json:"id"`
	Name         string    `json:"name"`
	IsDefault    bool      `json:"isDefault"`
	Resolutions  []string  `json:"resolutions"`
	Sources      []string  `json:"sources"`
	Codecs       []string  `json:"codecs"`
	Languages    []string  `json:"languages"`
	AudioFormats []string  `json:"audioFormats"`
	HdrFormats   []string  `json:"hdrFormats"`
	PreferProper bool      `json:"preferProper"`
	PreferRepack bool      `json:"preferRepack"`
	RejectCam    bool      `json:"rejectCam"`
	MinSizeMB    int       `json:"minSizeMb"`
	MaxSizeMB    int       `json:"maxSizeMb"`
	CreatedAt    time.Time `json:"createdAt"`
	UpdatedAt    time.Time `json:"updatedAt"`
}

func (db *DB) ListQualityProfiles(ctx context.Context) ([]QualityProfile, error) {
	rows, err := db.SQL.QueryContext(ctx, `
		SELECT id, name, is_default, resolutions, sources, codecs, languages,
		       coalesce(audio_formats,'{}'), coalesce(hdr_formats,'{}'),
		       coalesce(prefer_proper,true), coalesce(prefer_repack,true), coalesce(reject_cam,true),
		       min_size_mb, max_size_mb, created_at, updated_at
		FROM quality_profiles
		ORDER BY is_default DESC, name ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []QualityProfile
	for rows.Next() {
		var p QualityProfile
		if err := rows.Scan(
			&p.ID, &p.Name, &p.IsDefault,
			pgTextArrayScan(&p.Resolutions), pgTextArrayScan(&p.Sources),
			pgTextArrayScan(&p.Codecs), pgTextArrayScan(&p.Languages),
			pgTextArrayScan(&p.AudioFormats), pgTextArrayScan(&p.HdrFormats),
			&p.PreferProper, &p.PreferRepack, &p.RejectCam,
			&p.MinSizeMB, &p.MaxSizeMB, &p.CreatedAt, &p.UpdatedAt,
		); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (db *DB) UpsertQualityProfile(ctx context.Context, p QualityProfile) (QualityProfile, error) {
	var out QualityProfile
	err := db.SQL.QueryRowContext(ctx, `
		INSERT INTO quality_profiles
		    (name, is_default, resolutions, sources, codecs, languages,
		     audio_formats, hdr_formats, prefer_proper, prefer_repack, reject_cam,
		     min_size_mb, max_size_mb, updated_at)
		VALUES ($1,$2,$3::text[],$4::text[],$5::text[],$6::text[],
		        $7::text[],$8::text[],$9,$10,$11,$12,$13,now())
		ON CONFLICT (name) DO UPDATE SET
		    is_default    = excluded.is_default,
		    resolutions   = excluded.resolutions,
		    sources       = excluded.sources,
		    codecs        = excluded.codecs,
		    languages     = excluded.languages,
		    audio_formats = excluded.audio_formats,
		    hdr_formats   = excluded.hdr_formats,
		    prefer_proper = excluded.prefer_proper,
		    prefer_repack = excluded.prefer_repack,
		    reject_cam    = excluded.reject_cam,
		    min_size_mb   = excluded.min_size_mb,
		    max_size_mb   = excluded.max_size_mb,
		    updated_at    = now()
		RETURNING id, name, is_default, resolutions, sources, codecs, languages,
		          audio_formats, hdr_formats, prefer_proper, prefer_repack, reject_cam,
		          min_size_mb, max_size_mb, created_at, updated_at`,
		p.Name, p.IsDefault,
		pgTextArray(p.Resolutions), pgTextArray(p.Sources),
		pgTextArray(p.Codecs), pgTextArray(p.Languages),
		pgTextArray(p.AudioFormats), pgTextArray(p.HdrFormats),
		p.PreferProper, p.PreferRepack, p.RejectCam,
		p.MinSizeMB, p.MaxSizeMB,
	).Scan(
		&out.ID, &out.Name, &out.IsDefault,
		pgTextArrayScan(&out.Resolutions), pgTextArrayScan(&out.Sources),
		pgTextArrayScan(&out.Codecs), pgTextArrayScan(&out.Languages),
		pgTextArrayScan(&out.AudioFormats), pgTextArrayScan(&out.HdrFormats),
		&out.PreferProper, &out.PreferRepack, &out.RejectCam,
		&out.MinSizeMB, &out.MaxSizeMB, &out.CreatedAt, &out.UpdatedAt,
	)
	return out, err
}

func (db *DB) DeleteQualityProfile(ctx context.Context, id int64) error {
	_, err := db.SQL.ExecContext(ctx, `DELETE FROM quality_profiles WHERE id=$1 AND is_default=false`, id)
	return err
}

func (db *DB) GetDefaultQualityProfile(ctx context.Context) (QualityProfile, error) {
	var out QualityProfile
	err := db.SQL.QueryRowContext(ctx, `
		SELECT id, name, is_default, resolutions, sources, codecs, languages,
		       coalesce(audio_formats,'{}'), coalesce(hdr_formats,'{}'),
		       coalesce(prefer_proper,true), coalesce(prefer_repack,true), coalesce(reject_cam,true),
		       min_size_mb, max_size_mb, created_at, updated_at
		FROM quality_profiles
		ORDER BY is_default DESC, name ASC
		LIMIT 1`,
	).Scan(
		&out.ID, &out.Name, &out.IsDefault,
		pgTextArrayScan(&out.Resolutions), pgTextArrayScan(&out.Sources),
		pgTextArrayScan(&out.Codecs), pgTextArrayScan(&out.Languages),
		pgTextArrayScan(&out.AudioFormats), pgTextArrayScan(&out.HdrFormats),
		&out.PreferProper, &out.PreferRepack, &out.RejectCam,
		&out.MinSizeMB, &out.MaxSizeMB, &out.CreatedAt, &out.UpdatedAt,
	)
	return out, err
}

// pgTextArrayScan returns a pointer that can scan a PostgreSQL text[] column.
// We use a custom wrapper because pgx/database-sql needs special handling.
func pgTextArrayScan(dest *[]string) interface{ Scan(interface{}) error } {
	return &textArrayScanner{dest: dest}
}

type textArrayScanner struct{ dest *[]string }

func (s *textArrayScanner) Scan(src interface{}) error {
	if src == nil {
		*s.dest = nil
		return nil
	}
	switch v := src.(type) {
	case string:
		*s.dest = parsePostgresArray(v)
	case []byte:
		*s.dest = parsePostgresArray(string(v))
	}
	return nil
}

// parsePostgresArray parses a PostgreSQL text array literal like {"a","b","c"}.
func parsePostgresArray(s string) []string {
	if s == "{}" || s == "" {
		return nil
	}
	s = s[1 : len(s)-1] // strip { }
	var out []string
	var cur []byte
	inQuote := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '"' {
			inQuote = !inQuote
		} else if c == ',' && !inQuote {
			out = append(out, string(cur))
			cur = cur[:0]
		} else if c == '\\' && i+1 < len(s) {
			i++
			cur = append(cur, s[i])
		} else {
			cur = append(cur, c)
		}
	}
	if len(cur) > 0 || (len(s) > 0 && s[len(s)-1] == ',') {
		out = append(out, string(cur))
	}
	return out
}
