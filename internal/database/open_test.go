package database

import (
	"context"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/drakkar-media/drakkar/internal/config"
)

// TestOpenConnectsWithKeepaliveDialer guards the fix for a real production
// incident (2026-07-21): a connection whose TCP peer vanished mid-query, with
// no keepalive configured, blocked its read() forever instead of erroring --
// the pool never got the connection back, and all 8 download workers
// eventually piled up waiting for one of the 25 slots to free, wedging the
// whole download pipeline for 30+ minutes with no logged error. Open() now
// installs a DialFunc with a short KeepAliveConfig so a dead peer is detected
// in ~30s instead of the OS default (Linux: 2h+). This test doesn't simulate
// a severed connection (not practical against a real Postgres in CI), but
// confirms the custom DialFunc doesn't break normal connectivity -- Open,
// Ping, and Close must all still succeed.
func TestOpenConnectsWithKeepaliveDialer(t *testing.T) {
	dsn := os.Getenv("DRAKKAR_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("DRAKKAR_TEST_DATABASE_URL not set")
	}
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil {
		t.Fatal(err)
	}
	password, _ := u.User.Password()
	cfg := config.DatabaseConfig{
		Host:     u.Hostname(),
		Port:     port,
		Name:     strings.TrimPrefix(u.Path, "/"),
		Username: u.User.Username(),
		Password: password,
	}
	db, err := Open(cfg)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()
	if err := db.Ping(context.Background()); err != nil {
		t.Fatalf("Ping: %v", err)
	}
}

// TestOpenSetsStatementAndLockTimeouts guards a real production incident
// (2026-07-22, a recurrence of the 2026-07-21 pool-wedge incident despite the
// keepalive fix): dozens of connections piled up in (*sql.DB).conn over the
// course of the process's uptime, roughly one every 30 minutes, alongside a
// Redis pool failure at the same moment -- some stall (a lock wait behind an
// earlier stuck holder, or a connection wedged in a way idle-only keepalive
// probing can't catch) kept consuming connections faster than they were
// returned. statement_timeout/lock_timeout guarantee any single query or
// FOR UPDATE wait is forcibly cancelled server-side well before it could
// exhaust the pool, regardless of what caused it to stall.
func TestOpenSetsStatementAndLockTimeouts(t *testing.T) {
	dsn := os.Getenv("DRAKKAR_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("DRAKKAR_TEST_DATABASE_URL not set")
	}
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil {
		t.Fatal(err)
	}
	password, _ := u.User.Password()
	cfg := config.DatabaseConfig{
		Host:     u.Hostname(),
		Port:     port,
		Name:     strings.TrimPrefix(u.Path, "/"),
		Username: u.User.Username(),
		Password: password,
	}
	db, err := Open(cfg)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	var statementTimeout, lockTimeout string
	if err := db.SQL.QueryRowContext(context.Background(), `show statement_timeout`).Scan(&statementTimeout); err != nil {
		t.Fatal(err)
	}
	if err := db.SQL.QueryRowContext(context.Background(), `show lock_timeout`).Scan(&lockTimeout); err != nil {
		t.Fatal(err)
	}
	if statementTimeout != "1min" {
		t.Errorf("expected statement_timeout to be set to 60s (\"1min\"), got %q", statementTimeout)
	}
	if lockTimeout != "20s" {
		t.Errorf("expected lock_timeout to be set to 20s, got %q", lockTimeout)
	}
}
