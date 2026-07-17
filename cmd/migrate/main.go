// migrate applies the migrations/ directory to the database identified by
// DRAKKAR_TEST_DATABASE_URL (falling back to DATABASE_URL). It exists so CI
// and local development can bring up a fresh Postgres instance to the
// current schema without going through the full drakkar server startup path.
package main

import (
	"context"
	"database/sql"
	"fmt"
	"os"

	"github.com/drakkar-media/drakkar/internal/database"
	_ "github.com/jackc/pgx/v5/stdlib"
)

func main() {
	dsn := os.Getenv("DRAKKAR_TEST_DATABASE_URL")
	if dsn == "" {
		dsn = os.Getenv("DATABASE_URL")
	}
	if dsn == "" {
		fmt.Fprintln(os.Stderr, "migrate: DRAKKAR_TEST_DATABASE_URL or DATABASE_URL must be set")
		os.Exit(1)
	}

	sqlDB, err := sql.Open("pgx", dsn)
	if err != nil {
		fmt.Fprintln(os.Stderr, "migrate: open:", err)
		os.Exit(1)
	}
	defer sqlDB.Close()

	db := &database.DB{SQL: sqlDB}
	if err := db.ApplyMigrations(context.Background(), "migrations"); err != nil {
		fmt.Fprintln(os.Stderr, "migrate: apply:", err)
		os.Exit(1)
	}
	fmt.Println("migrate: schema up to date")
}
