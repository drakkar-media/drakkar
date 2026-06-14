package database

import (
	"context"
	"time"
)

type User struct {
	ID           int64     `json:"id"`
	Username     string    `json:"username"`
	Role         string    `json:"role"`
	CreatedAt    time.Time `json:"createdAt"`
}

func (db *DB) CountUsers(ctx context.Context) (int, error) {
	var n int
	err := db.SQL.QueryRowContext(ctx, `SELECT count(*) FROM users`).Scan(&n)
	return n, err
}

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

func (db *DB) CreateUser(ctx context.Context, username, passwordHash, role string) (User, error) {
	var u User
	err := db.SQL.QueryRowContext(ctx,
		`INSERT INTO users (username, password_hash, role) VALUES ($1, $2, $3)
		 RETURNING id, username, role, created_at`,
		username, passwordHash, role,
	).Scan(&u.ID, &u.Username, &u.Role, &u.CreatedAt)
	return u, err
}

func (db *DB) GetUserByUsername(ctx context.Context, username string) (id int64, passwordHash, role string, err error) {
	err = db.SQL.QueryRowContext(ctx,
		`SELECT id, password_hash, role FROM users WHERE username = $1`, username,
	).Scan(&id, &passwordHash, &role)
	return
}

func (db *DB) UpdateUserPassword(ctx context.Context, userID int64, passwordHash string) error {
	_, err := db.SQL.ExecContext(ctx,
		`UPDATE users SET password_hash = $1 WHERE id = $2`, passwordHash, userID)
	return err
}

func (db *DB) DeleteUser(ctx context.Context, id int64) error {
	_, err := db.SQL.ExecContext(ctx, `DELETE FROM users WHERE id = $1`, id)
	return err
}

func (db *DB) CreateSession(ctx context.Context, userID int64, tokenHash string, expiresAt time.Time) error {
	_, err := db.SQL.ExecContext(ctx,
		`INSERT INTO sessions (user_id, token_hash, expires_at) VALUES ($1, $2, $3)`,
		userID, tokenHash, expiresAt)
	return err
}

func (db *DB) GetSessionByTokenHash(ctx context.Context, tokenHash string) (userID int64, username, role string, expiresAt time.Time, err error) {
	err = db.SQL.QueryRowContext(ctx, `
		SELECT s.user_id, u.username, u.role, s.expires_at
		FROM sessions s
		JOIN users u ON u.id = s.user_id
		WHERE s.token_hash = $1`, tokenHash,
	).Scan(&userID, &username, &role, &expiresAt)
	return
}

func (db *DB) DeleteSession(ctx context.Context, tokenHash string) error {
	_, err := db.SQL.ExecContext(ctx, `DELETE FROM sessions WHERE token_hash = $1`, tokenHash)
	return err
}

// PruneExpiredSessions removes sessions that have passed their expiry time.
func (db *DB) PruneExpiredSessions(ctx context.Context) error {
	_, err := db.SQL.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at < now()`)
	return err
}
