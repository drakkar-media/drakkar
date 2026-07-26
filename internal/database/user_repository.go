package database

import (
	"context"
	"time"
)

// User represents an authenticated account holder.
type User struct {
	ID        int64     `json:"id"`
	Username  string    `json:"username"`
	Role      string    `json:"role"`
	CreatedAt time.Time `json:"createdAt"`
}

// APIToken represents a long-lived credential issued to a user for
// programmatic (non-session) API access.
type APIToken struct {
	ID         int64      `json:"id"`
	UserID     int64      `json:"userId"`
	Name       string     `json:"name"`
	CreatedAt  time.Time  `json:"createdAt"`
	LastUsedAt *time.Time `json:"lastUsedAt,omitempty"`
	ExpiresAt  *time.Time `json:"expiresAt,omitempty"`
}

// CountUsers returns the total number of registered user accounts.
func (db *DB) CountUsers(ctx context.Context) (int, error) {
	var n int
	err := db.SQL.QueryRowContext(ctx, `SELECT count(*) FROM users`).Scan(&n)
	return n, err
}

// ListUsers returns all user accounts ordered by ID.
func (db *DB) ListUsers(ctx context.Context) ([]User, error) {
	rows, err := db.SQL.QueryContext(ctx, `SELECT id, username, role, created_at FROM users ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []User
	for rows.Next() {
		var u User
		if err := rows.Scan(&u.ID, &u.Username, &u.Role, &u.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// CreateUser inserts a new user account.
//
// passwordHash must already be a hashed credential (e.g. bcrypt output); this
// method performs no hashing itself.
func (db *DB) CreateUser(ctx context.Context, username, passwordHash, role string) (User, error) {
	var u User
	err := db.SQL.QueryRowContext(ctx,
		`INSERT INTO users (username, password_hash, role) VALUES ($1, $2, $3)
		 RETURNING id, username, role, created_at`,
		username, passwordHash, role,
	).Scan(&u.ID, &u.Username, &u.Role, &u.CreatedAt)
	return u, err
}

// GetUserByUsername looks up a user's credentials by username for login
// verification. The caller is responsible for comparing passwordHash against
// the supplied password.
func (db *DB) GetUserByUsername(ctx context.Context, username string) (id int64, passwordHash, role string, err error) {
	err = db.SQL.QueryRowContext(ctx,
		`SELECT id, password_hash, role FROM users WHERE username = $1`, username,
	).Scan(&id, &passwordHash, &role)
	return
}

// UpdateUserPassword replaces the stored password hash for a user.
func (db *DB) UpdateUserPassword(ctx context.Context, userID int64, passwordHash string) error {
	_, err := db.SQL.ExecContext(ctx,
		`UPDATE users SET password_hash = $1 WHERE id = $2`, passwordHash, userID)
	return err
}

// DeleteUser removes a user account by ID.
func (db *DB) DeleteUser(ctx context.Context, id int64) error {
	_, err := db.SQL.ExecContext(ctx, `DELETE FROM users WHERE id = $1`, id)
	return err
}

// CreateSession persists a login session identified by a pre-hashed session
// token. Only the hash is stored; the raw token is never written to the
// database.
func (db *DB) CreateSession(ctx context.Context, userID int64, tokenHash string, expiresAt time.Time) error {
	_, err := db.SQL.ExecContext(ctx,
		`INSERT INTO sessions (user_id, token_hash, expires_at) VALUES ($1, $2, $3)`,
		userID, tokenHash, expiresAt)
	return err
}

// GetSessionByTokenHash resolves an active session's owning user by session
// token hash. Callers must separately check expiresAt against the current
// time; expired sessions are not filtered out here so they can still be
// distinguished from a nonexistent session for pruning/diagnostics.
func (db *DB) GetSessionByTokenHash(ctx context.Context, tokenHash string) (userID int64, username, role string, expiresAt time.Time, err error) {
	err = db.SQL.QueryRowContext(ctx, `
		SELECT s.user_id, u.username, u.role, s.expires_at
		FROM sessions s
		JOIN users u ON u.id = s.user_id
		WHERE s.token_hash = $1`, tokenHash,
	).Scan(&userID, &username, &role, &expiresAt)
	return
}

// DeleteSession removes a single session by token hash (used on logout).
func (db *DB) DeleteSession(ctx context.Context, tokenHash string) error {
	_, err := db.SQL.ExecContext(ctx, `DELETE FROM sessions WHERE token_hash = $1`, tokenHash)
	return err
}

// ListAPITokens returns all API tokens belonging to a user, most recently
// created first.
func (db *DB) ListAPITokens(ctx context.Context, userID int64) ([]APIToken, error) {
	rows, err := db.SQL.QueryContext(ctx, `
		SELECT id, user_id, name, created_at, last_used_at, expires_at
		FROM api_tokens
		WHERE user_id = $1
		ORDER BY created_at DESC, id DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []APIToken
	for rows.Next() {
		var tok APIToken
		if err := rows.Scan(&tok.ID, &tok.UserID, &tok.Name, &tok.CreatedAt, &tok.LastUsedAt, &tok.ExpiresAt); err != nil {
			return nil, err
		}
		out = append(out, tok)
	}
	return out, rows.Err()
}

// CreateAPIToken issues a new API token for a user. As with sessions, only
// tokenHash is persisted; expiresAt may be nil for a non-expiring token.
func (db *DB) CreateAPIToken(ctx context.Context, userID int64, name, tokenHash string, expiresAt *time.Time) (APIToken, error) {
	var tok APIToken
	err := db.SQL.QueryRowContext(ctx, `
		INSERT INTO api_tokens (user_id, name, token_hash, expires_at)
		VALUES ($1, $2, $3, $4)
		RETURNING id, user_id, name, created_at, last_used_at, expires_at`,
		userID, name, tokenHash, expiresAt,
	).Scan(&tok.ID, &tok.UserID, &tok.Name, &tok.CreatedAt, &tok.LastUsedAt, &tok.ExpiresAt)
	return tok, err
}

// GetAPITokenByHash resolves an API token's owning user by token hash for
// request authentication. Callers must separately reject tokens whose
// expiresAt has passed.
func (db *DB) GetAPITokenByHash(ctx context.Context, tokenHash string) (userID int64, username, role string, expiresAt *time.Time, err error) {
	err = db.SQL.QueryRowContext(ctx, `
		SELECT t.user_id, u.username, u.role, t.expires_at
		FROM api_tokens t
		JOIN users u ON u.id = t.user_id
		WHERE t.token_hash = $1`, tokenHash,
	).Scan(&userID, &username, &role, &expiresAt)
	return
}

// TouchAPITokenUsed updates last_used_at for an API token to the current
// time, typically called on each authenticated request for auditing.
func (db *DB) TouchAPITokenUsed(ctx context.Context, tokenHash string) error {
	_, err := db.SQL.ExecContext(ctx, `UPDATE api_tokens SET last_used_at = now() WHERE token_hash = $1`, tokenHash)
	return err
}

// DeleteAPIToken removes an API token, scoped to userID so a user cannot
// delete another user's token by guessing its ID.
func (db *DB) DeleteAPIToken(ctx context.Context, userID, tokenID int64) error {
	_, err := db.SQL.ExecContext(ctx, `DELETE FROM api_tokens WHERE id = $1 AND user_id = $2`, tokenID, userID)
	return err
}

// PruneExpiredSessions removes sessions that have passed their expiry time.
func (db *DB) PruneExpiredSessions(ctx context.Context) error {
	_, err := db.SQL.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at < now()`)
	return err
}
