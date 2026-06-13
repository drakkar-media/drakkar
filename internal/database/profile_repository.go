package database

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

type QualityProfile struct {
	ID              int64     `json:"id"`
	Name            string    `json:"name"`
	IsDefault       bool      `json:"isDefault"`
	Resolutions     []string  `json:"resolutions"`
	Sources         []string  `json:"sources"`
	Codecs          []string  `json:"codecs"`
	Languages       []string  `json:"languages"`
	AudioFormats    []string  `json:"audioFormats"`
	HdrFormats      []string  `json:"hdrFormats"`
	ExcludePatterns []string  `json:"excludePatterns"`
	PreferProper    bool      `json:"preferProper"`
	PreferRepack    bool      `json:"preferRepack"`
	RejectCam          bool      `json:"rejectCam"`
	AllowUpgrade       bool      `json:"allowUpgrade"`
	CutoffResolution   string    `json:"cutoffResolution"`
	MinimumAgeHours    int       `json:"minimumAgeHours"`
	MinSizeMB          int       `json:"minSizeMb"`
	MaxSizeMB          int       `json:"maxSizeMb"`
	CreatedAt       time.Time `json:"createdAt"`
	UpdatedAt       time.Time `json:"updatedAt"`
}

type QualityDefinition struct {
	ID         int64  `json:"id"`
	MediaType  string `json:"mediaType"`
	QualityKey string `json:"qualityKey"`
	Title      string `json:"title"`
	MinSizeMB  int    `json:"minSizeMb"`
	MaxSizeMB  int    `json:"maxSizeMb"`
	SortOrder  int    `json:"sortOrder"`
}

const profileSelectCols = ` id, name, is_default, resolutions, sources, codecs, languages,
	coalesce(audio_formats,'{}'), coalesce(hdr_formats,'{}'),
	coalesce(exclude_patterns,'{}'),
	coalesce(prefer_proper,true), coalesce(prefer_repack,true), coalesce(reject_cam,true),
	coalesce(allow_upgrade,false),
	coalesce(cutoff_resolution,''), coalesce(minimum_age_hours,0),
	min_size_mb, max_size_mb, created_at, updated_at `

func scanProfile(row interface {
	Scan(dest ...interface{}) error
}) (QualityProfile, error) {
	var p QualityProfile
	err := row.Scan(
		&p.ID, &p.Name, &p.IsDefault,
		pgTextArrayScan(&p.Resolutions), pgTextArrayScan(&p.Sources),
		pgTextArrayScan(&p.Codecs), pgTextArrayScan(&p.Languages),
		pgTextArrayScan(&p.AudioFormats), pgTextArrayScan(&p.HdrFormats),
		pgTextArrayScan(&p.ExcludePatterns),
		&p.PreferProper, &p.PreferRepack, &p.RejectCam,
		&p.AllowUpgrade,
		&p.CutoffResolution, &p.MinimumAgeHours,
		&p.MinSizeMB, &p.MaxSizeMB, &p.CreatedAt, &p.UpdatedAt,
	)
	return p, err
}

func (db *DB) ListQualityProfiles(ctx context.Context) ([]QualityProfile, error) {
	rows, err := db.SQL.QueryContext(ctx,
		`SELECT`+profileSelectCols+`FROM quality_profiles ORDER BY is_default DESC, name ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []QualityProfile
	for rows.Next() {
		p, err := scanProfile(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (db *DB) GetQualityProfileByName(ctx context.Context, name string) (QualityProfile, error) {
	row := db.SQL.QueryRowContext(ctx,
		`SELECT`+profileSelectCols+`FROM quality_profiles WHERE name=$1`, name)
	return scanProfile(row)
}

func (db *DB) UpsertQualityProfile(ctx context.Context, p QualityProfile) (QualityProfile, error) {
	row := db.SQL.QueryRowContext(ctx, `
		INSERT INTO quality_profiles
		    (name, is_default, resolutions, sources, codecs, languages,
		     audio_formats, hdr_formats, exclude_patterns,
		     prefer_proper, prefer_repack, reject_cam, allow_upgrade,
		     cutoff_resolution, minimum_age_hours,
		     min_size_mb, max_size_mb, updated_at)
		VALUES ($1,$2,$3::text[],$4::text[],$5::text[],$6::text[],
		        $7::text[],$8::text[],$9::text[],$10,$11,$12,$13,$14,$15,$16,$17,now())
		ON CONFLICT (name) DO UPDATE SET
		    is_default         = excluded.is_default,
		    resolutions        = excluded.resolutions,
		    sources            = excluded.sources,
		    codecs             = excluded.codecs,
		    languages          = excluded.languages,
		    audio_formats      = excluded.audio_formats,
		    hdr_formats        = excluded.hdr_formats,
		    exclude_patterns   = excluded.exclude_patterns,
		    prefer_proper      = excluded.prefer_proper,
		    prefer_repack      = excluded.prefer_repack,
		    reject_cam         = excluded.reject_cam,
		    allow_upgrade      = excluded.allow_upgrade,
		    cutoff_resolution  = excluded.cutoff_resolution,
		    minimum_age_hours  = excluded.minimum_age_hours,
		    min_size_mb        = excluded.min_size_mb,
		    max_size_mb        = excluded.max_size_mb,
		    updated_at         = now()
		RETURNING`+profileSelectCols,
		p.Name, p.IsDefault,
		pgTextArray(p.Resolutions), pgTextArray(p.Sources),
		pgTextArray(p.Codecs), pgTextArray(p.Languages),
		pgTextArray(p.AudioFormats), pgTextArray(p.HdrFormats),
		pgTextArray(p.ExcludePatterns),
		p.PreferProper, p.PreferRepack, p.RejectCam, p.AllowUpgrade,
		p.CutoffResolution, p.MinimumAgeHours,
		p.MinSizeMB, p.MaxSizeMB,
	)
	return scanProfile(row)
}

func (db *DB) DeleteQualityProfile(ctx context.Context, id int64) error {
	_, err := db.SQL.ExecContext(ctx, `DELETE FROM quality_profiles WHERE id=$1 AND is_default=false`, id)
	return err
}

func (db *DB) GetDefaultQualityProfile(ctx context.Context) (QualityProfile, error) {
	row := db.SQL.QueryRowContext(ctx,
		`SELECT`+profileSelectCols+`FROM quality_profiles ORDER BY is_default DESC, name ASC LIMIT 1`)
	return scanProfile(row)
}

func (db *DB) GetLibraryItemQualityProfile(ctx context.Context, libraryItemID int64) (*QualityProfile, error) {
	row := db.SQL.QueryRowContext(ctx,
		`SELECT`+profileSelectCols+`FROM quality_profiles qp
		 JOIN library_items li ON li.quality_profile_id = qp.id
		 WHERE li.id = $1`, libraryItemID)
	p, err := scanProfile(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &p, nil
}

func (db *DB) SetLibraryItemQualityProfile(ctx context.Context, libraryItemID int64, profileID *int64) error {
	_, err := db.SQL.ExecContext(ctx,
		`UPDATE library_items SET quality_profile_id=$1 WHERE id=$2`, profileID, libraryItemID)
	return err
}

func (db *DB) ListQualityDefinitions(ctx context.Context) ([]QualityDefinition, error) {
	rows, err := db.SQL.QueryContext(ctx,
		`SELECT id, media_type, quality_key, title, min_size_mb, max_size_mb, sort_order
		 FROM quality_definitions ORDER BY media_type, sort_order`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []QualityDefinition
	for rows.Next() {
		var d QualityDefinition
		if err := rows.Scan(&d.ID, &d.MediaType, &d.QualityKey, &d.Title, &d.MinSizeMB, &d.MaxSizeMB, &d.SortOrder); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func (db *DB) UpdateQualityDefinition(ctx context.Context, d QualityDefinition) (QualityDefinition, error) {
	var out QualityDefinition
	err := db.SQL.QueryRowContext(ctx,
		`UPDATE quality_definitions SET min_size_mb=$1, max_size_mb=$2
		 WHERE id=$3
		 RETURNING id, media_type, quality_key, title, min_size_mb, max_size_mb, sort_order`,
		d.MinSizeMB, d.MaxSizeMB, d.ID,
	).Scan(&out.ID, &out.MediaType, &out.QualityKey, &out.Title, &out.MinSizeMB, &out.MaxSizeMB, &out.SortOrder)
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
