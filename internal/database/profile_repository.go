package database

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

// QualityProfile represents a named set of quality-selection rules applied
// when choosing among release candidates for a library item or media
// request (allowed resolutions/sources/codecs/languages, size-per-minute
// bounds, and preference/rejection flags such as PreferProper or RejectCam).
//
// At most one profile is expected to have IsDefault set; GetDefaultQualityProfile
// and ListQualityProfiles rely on is_default ordering to surface it first.
type QualityProfile struct {
	ID                              int64     `json:"id"`
	Name                            string    `json:"name"`
	IsDefault                       bool      `json:"isDefault"`
	Resolutions                     []string  `json:"resolutions"`
	Sources                         []string  `json:"sources"`
	Codecs                          []string  `json:"codecs"`
	Languages                       []string  `json:"languages"`
	AudioFormats                    []string  `json:"audioFormats"`
	HdrFormats                      []string  `json:"hdrFormats"`
	ExcludePatterns                 []string  `json:"excludePatterns"`
	PreferProper                    bool      `json:"preferProper"`
	PreferRepack                    bool      `json:"preferRepack"`
	RejectCam                       bool      `json:"rejectCam"`
	AllowUpgrade                    bool      `json:"allowUpgrade"`
	MinimumUpgradeCustomFormatScore int       `json:"minimumUpgradeCustomFormatScore"`
	CutoffResolution                string    `json:"cutoffResolution"`
	MinimumAgeHours                 int       `json:"minimumAgeHours"`
	MinMBPerMinute                  int       `json:"minMbPerMinute"`
	MaxMBPerMinute                  int       `json:"maxMbPerMinute"`
	CreatedAt                       time.Time `json:"createdAt"`
	UpdatedAt                       time.Time `json:"updatedAt"`
}

// UnmarshalJSON decodes a QualityProfile, translating the legacy
// "minSizeMb"/"maxSizeMb" field names into MinMBPerMinute/MaxMBPerMinute.
//
// It accepts either the current "minMbPerMinute"/"maxMbPerMinute" keys or the
// older "minSizeMb"/"maxSizeMb" keys for backward compatibility with
// previously stored or submitted payloads; when both are present for a given
// bound, the MbPerMinute variant takes precedence.
func (p *QualityProfile) UnmarshalJSON(data []byte) error {
	type Alias QualityProfile
	aux := struct {
		Alias
		MinSizeMb      *int `json:"minSizeMb"`
		MaxSizeMb      *int `json:"maxSizeMb"`
		MinMbPerMinute *int `json:"minMbPerMinute"`
		MaxMbPerMinute *int `json:"maxMbPerMinute"`
	}{}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	*p = QualityProfile(aux.Alias)
	switch {
	case aux.MinMbPerMinute != nil:
		p.MinMBPerMinute = *aux.MinMbPerMinute
	case aux.MinSizeMb != nil:
		p.MinMBPerMinute = *aux.MinSizeMb
	}
	switch {
	case aux.MaxMbPerMinute != nil:
		p.MaxMBPerMinute = *aux.MaxMbPerMinute
	case aux.MaxSizeMb != nil:
		p.MaxMBPerMinute = *aux.MaxSizeMb
	}
	return nil
}

// QualityDefinition represents the configured size-per-minute range for a
// single quality tier (e.g. "1080p WEB-DL") within a media type, used to
// classify a release's bitrate into a named quality bucket.
type QualityDefinition struct {
	ID             int64  `json:"id"`
	MediaType      string `json:"mediaType"`
	QualityKey     string `json:"qualityKey"`
	Title          string `json:"title"`
	MinMBPerMinute int    `json:"minMbPerMinute"`
	MaxMBPerMinute int    `json:"maxMbPerMinute"`
	SortOrder      int    `json:"sortOrder"`
}

// profileSelectCols is the shared column list for quality_profiles queries,
// used by both ListQualityProfiles and single-row lookups so all callers stay
// in sync with scanProfile's column order. Nullable boolean/int columns are
// coalesced to their historical defaults (e.g. prefer_proper/prefer_repack/
// reject_cam default to true) so rows created before those columns existed
// behave the same as if they had been explicitly set.
const profileSelectCols = ` id, name, is_default, resolutions, sources, codecs, languages,
	coalesce(audio_formats,'{}'), coalesce(hdr_formats,'{}'),
	coalesce(exclude_patterns,'{}'),
	coalesce(prefer_proper,true), coalesce(prefer_repack,true), coalesce(reject_cam,true),
	coalesce(allow_upgrade,false),
	coalesce(minimum_upgrade_custom_format_score,0),
	coalesce(cutoff_resolution,''), coalesce(minimum_age_hours,0),
	min_mb_per_minute, max_mb_per_minute, created_at, updated_at `

// scanProfile scans a single quality_profiles row selected with
// profileSelectCols into a QualityProfile. It accepts either *sql.Row or
// *sql.Rows via the minimal Scan interface so callers can share the same
// column list and scan logic across single-row and multi-row queries.
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
		&p.AllowUpgrade, &p.MinimumUpgradeCustomFormatScore,
		&p.CutoffResolution, &p.MinimumAgeHours,
		&p.MinMBPerMinute, &p.MaxMBPerMinute, &p.CreatedAt, &p.UpdatedAt,
	)
	return p, err
}

// ListQualityProfiles returns all quality profiles, with the default profile
// (if any) first and the remainder ordered alphabetically by name.
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

// GetQualityProfileByName looks up a quality profile by its exact name.
//
// Errors:
//   - sql.ErrNoRows: no profile with the given name exists.
func (db *DB) GetQualityProfileByName(ctx context.Context, name string) (QualityProfile, error) {
	row := db.SQL.QueryRowContext(ctx,
		`SELECT`+profileSelectCols+`FROM quality_profiles WHERE name=$1`, name)
	return scanProfile(row)
}

// UpsertQualityProfile creates a new profile (p.ID == 0) or updates an
// existing one by id (p.ID > 0).
//
// This used to always INSERT ... ON CONFLICT (name) DO UPDATE regardless of
// p.ID, which was never referenced at all. Renaming an existing profile (same
// id, new name) therefore never matched the conflict target and silently
// INSERTed a brand-new row, leaving the original untouched under its old
// name -- and saving a *new* profile whose name happened to collide with an
// existing one (e.g. two unrenamed "New Profile" drafts) silently overwrote
// that unrelated profile's entire configuration. Updating by id when one is
// present fixes both: renames update the same row, and a name collision on
// update or create now surfaces as a real unique-constraint error instead of
// silently merging into the wrong row.
func (db *DB) UpsertQualityProfile(ctx context.Context, p QualityProfile) (QualityProfile, error) {
	if p.ID > 0 {
		row := db.SQL.QueryRowContext(ctx, `
			UPDATE quality_profiles SET
			    name               = $2,
			    is_default         = $3,
			    resolutions        = $4::text[],
			    sources            = $5::text[],
			    codecs             = $6::text[],
			    languages          = $7::text[],
			    audio_formats      = $8::text[],
			    hdr_formats        = $9::text[],
			    exclude_patterns   = $10::text[],
			    prefer_proper      = $11,
			    prefer_repack      = $12,
			    reject_cam         = $13,
			    allow_upgrade      = $14,
			    minimum_upgrade_custom_format_score = $15,
			    cutoff_resolution  = $16,
			    minimum_age_hours  = $17,
			    min_mb_per_minute  = $18,
			    max_mb_per_minute  = $19,
			    updated_at         = now()
			WHERE id = $1
			RETURNING`+profileSelectCols,
			p.ID, p.Name, p.IsDefault,
			pgTextArray(p.Resolutions), pgTextArray(p.Sources),
			pgTextArray(p.Codecs), pgTextArray(p.Languages),
			pgTextArray(p.AudioFormats), pgTextArray(p.HdrFormats),
			pgTextArray(p.ExcludePatterns),
			p.PreferProper, p.PreferRepack, p.RejectCam, p.AllowUpgrade, p.MinimumUpgradeCustomFormatScore,
			p.CutoffResolution, p.MinimumAgeHours,
			p.MinMBPerMinute, p.MaxMBPerMinute,
		)
		return scanProfile(row)
	}
	row := db.SQL.QueryRowContext(ctx, `
		INSERT INTO quality_profiles
		    (name, is_default, resolutions, sources, codecs, languages,
		     audio_formats, hdr_formats, exclude_patterns,
		     prefer_proper, prefer_repack, reject_cam, allow_upgrade, minimum_upgrade_custom_format_score,
		     cutoff_resolution, minimum_age_hours,
		     min_mb_per_minute, max_mb_per_minute, updated_at)
		VALUES ($1,$2,$3::text[],$4::text[],$5::text[],$6::text[],
		        $7::text[],$8::text[],$9::text[],$10,$11,$12,$13,$14,$15,$16,$17,$18,now())
		RETURNING`+profileSelectCols,
		p.Name, p.IsDefault,
		pgTextArray(p.Resolutions), pgTextArray(p.Sources),
		pgTextArray(p.Codecs), pgTextArray(p.Languages),
		pgTextArray(p.AudioFormats), pgTextArray(p.HdrFormats),
		pgTextArray(p.ExcludePatterns),
		p.PreferProper, p.PreferRepack, p.RejectCam, p.AllowUpgrade, p.MinimumUpgradeCustomFormatScore,
		p.CutoffResolution, p.MinimumAgeHours,
		p.MinMBPerMinute, p.MaxMBPerMinute,
	)
	return scanProfile(row)
}

// DeleteQualityProfile deletes the quality profile with the given id.
//
// The default profile is protected: the delete is scoped to is_default=false,
// so attempting to delete the current default silently affects zero rows
// instead of returning an error. Callers must not rely on the returned error
// to detect a missing or protected id.
func (db *DB) DeleteQualityProfile(ctx context.Context, id int64) error {
	_, err := db.SQL.ExecContext(ctx, `DELETE FROM quality_profiles WHERE id=$1 AND is_default=false`, id)
	return err
}

// GetDefaultQualityProfile returns the profile flagged as default. If no
// profile has IsDefault set, it falls back to the alphabetically first
// profile so callers always get a usable profile when at least one exists.
//
// Errors:
//   - sql.ErrNoRows: no quality profiles exist.
func (db *DB) GetDefaultQualityProfile(ctx context.Context) (QualityProfile, error) {
	row := db.SQL.QueryRowContext(ctx,
		`SELECT`+profileSelectCols+`FROM quality_profiles ORDER BY is_default DESC, name ASC LIMIT 1`)
	return scanProfile(row)
}

// GetLibraryItemQualityProfile returns the quality profile assigned to a
// library item.
//
// Returns:
//   - *QualityProfile: nil, with a nil error, when the item has no
//     quality_profile_id assigned or references a profile that no longer
//     exists; callers should treat this as "use the default profile" rather
//     than an error condition.
func (db *DB) GetLibraryItemQualityProfile(ctx context.Context, libraryItemID int64) (*QualityProfile, error) {
	row := db.SQL.QueryRowContext(ctx,
		`SELECT`+profileSelectCols+`FROM quality_profiles
		 WHERE id = (SELECT quality_profile_id FROM library_items WHERE id = $1)`,
		libraryItemID)
	p, err := scanProfile(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &p, nil
}

// SetLibraryItemQualityProfile assigns or clears (profileID == nil) the
// quality profile used for a library item's release selection.
func (db *DB) SetLibraryItemQualityProfile(ctx context.Context, libraryItemID int64, profileID *int64) error {
	_, err := db.SQL.ExecContext(ctx,
		`UPDATE library_items SET quality_profile_id=$1 WHERE id=$2`, profileID, libraryItemID)
	return err
}

// SetMediaRequestQualityProfile assigns a quality profile to the library
// item associated with a media request, resolving that library item through
// the request's most recently created matching queue entry (matched by the
// "seerr-movie-"/"seerr-tv-" idempotency key derived from the request's
// external id).
//
// Parameters:
//   - profileID: the profile to assign, or nil to clear the assignment.
//
// Returns:
//   - int64: the id of the library item that was updated.
//
// Errors:
//   - sql.ErrNoRows: the request does not resolve to any library item (no
//     matching queue entry, or the queue entry has no linked library item).
func (db *DB) SetMediaRequestQualityProfile(ctx context.Context, requestID int64, profileID *int64) (int64, error) {
	var libraryItemID int64
	err := db.SQL.QueryRowContext(ctx, `
		with target as (
			select li.id as library_item_id
			from media_requests mr
			left join queue_items q on q.id = (
				select q2.id from queue_items q2
				where q2.idempotency_key in ('seerr-movie-' || coalesce(mr.external_id, ''), 'seerr-tv-' || coalesce(mr.external_id, ''))
				order by q2.id desc
				limit 1
			)
			left join library_items li on li.id = q.library_item_id
			where mr.id = $2
			limit 1
		)
		update library_items li
		set quality_profile_id = $1
		from target
		where li.id = target.library_item_id
		returning li.id
	`, profileID, requestID).Scan(&libraryItemID)
	if err != nil {
		return 0, err
	}
	return libraryItemID, nil
}

// ListQualityDefinitions returns all quality definitions across all media
// types, ordered by media type and then by their configured sort order.
func (db *DB) ListQualityDefinitions(ctx context.Context) ([]QualityDefinition, error) {
	rows, err := db.SQL.QueryContext(ctx,
		`SELECT id, media_type, quality_key, title, min_mb_per_minute, max_mb_per_minute, sort_order
		 FROM quality_definitions ORDER BY media_type, sort_order`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []QualityDefinition
	for rows.Next() {
		var d QualityDefinition
		if err := rows.Scan(&d.ID, &d.MediaType, &d.QualityKey, &d.Title, &d.MinMBPerMinute, &d.MaxMBPerMinute, &d.SortOrder); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// UpdateQualityDefinition updates the min/max MB-per-minute bounds of an
// existing quality definition, identified by d.ID. Other fields on d
// (MediaType, QualityKey, Title, SortOrder) are ignored for the update and
// are instead populated from the database in the returned value.
//
// Errors:
//   - sql.ErrNoRows: no quality definition with the given id exists.
func (db *DB) UpdateQualityDefinition(ctx context.Context, d QualityDefinition) (QualityDefinition, error) {
	var out QualityDefinition
	err := db.SQL.QueryRowContext(ctx,
		`UPDATE quality_definitions SET min_mb_per_minute=$1, max_mb_per_minute=$2
		 WHERE id=$3
		 RETURNING id, media_type, quality_key, title, min_mb_per_minute, max_mb_per_minute, sort_order`,
		d.MinMBPerMinute, d.MaxMBPerMinute, d.ID,
	).Scan(&out.ID, &out.MediaType, &out.QualityKey, &out.Title, &out.MinMBPerMinute, &out.MaxMBPerMinute, &out.SortOrder)
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
