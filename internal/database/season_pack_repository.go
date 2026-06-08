package database

import (
	"context"
	"time"
)

type SeasonPackAttempt struct {
	ID            int64
	TVShowID      int64
	SeasonNumber  int
	LastAttemptAt time.Time
	AttemptCount  int
	LastOutcome   string
}

const (
	SeasonPackOutcomeSearching = "searching"
	SeasonPackOutcomeSelected  = "selected"
	SeasonPackOutcomeFailed    = "failed"
	SeasonPackOutcomeExhausted = "exhausted"
)

// SeasonPackCooldown is the minimum time between pack attempts for the same season.
// After 3 failures the cooldown doubles; after 6 it trebles (capped at 7 days).
func SeasonPackCooldown(attemptCount int) time.Duration {
	switch {
	case attemptCount <= 2:
		return 6 * time.Hour
	case attemptCount <= 5:
		return 24 * time.Hour
	case attemptCount <= 9:
		return 3 * 24 * time.Hour
	default:
		return 7 * 24 * time.Hour
	}
}

// ShouldAttemptSeasonPack returns true if enough time has elapsed since the
// last season pack search attempt for this show/season.
func (db *DB) ShouldAttemptSeasonPack(ctx context.Context, tvShowID int64, season int) (bool, error) {
	var lastAttempt time.Time
	var count int
	err := db.SQL.QueryRowContext(ctx, `
		SELECT last_attempt_at, attempt_count
		FROM season_pack_attempts
		WHERE tv_show_id = $1 AND season_number = $2`,
		tvShowID, season,
	).Scan(&lastAttempt, &count)
	if err != nil {
		// No row → never attempted; go ahead.
		return true, nil
	}
	cooldown := SeasonPackCooldown(count)
	return time.Since(lastAttempt) >= cooldown, nil
}

// RecordSeasonPackAttempt upserts the attempt counter and timestamp.
func (db *DB) RecordSeasonPackAttempt(ctx context.Context, tvShowID int64, season int, outcome string) error {
	_, err := db.SQL.ExecContext(ctx, `
		INSERT INTO season_pack_attempts (tv_show_id, season_number, last_attempt_at, attempt_count, last_outcome)
		VALUES ($1, $2, now(), 1, $3)
		ON CONFLICT (tv_show_id, season_number) DO UPDATE
		    SET last_attempt_at = now(),
		        attempt_count   = season_pack_attempts.attempt_count + 1,
		        last_outcome    = $3`,
		tvShowID, season, outcome)
	return err
}

// ResetSeasonPackAttempts clears the attempt history for a season so it will
// be retried on the next reconciliation pass (e.g. after a manual restore).
func (db *DB) ResetSeasonPackAttempts(ctx context.Context, tvShowID int64, season int) error {
	_, err := db.SQL.ExecContext(ctx, `
		DELETE FROM season_pack_attempts WHERE tv_show_id = $1 AND season_number = $2`,
		tvShowID, season)
	return err
}
