package database

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestCreateInitialAdminAllowsExactlyOneConcurrentCaller(t *testing.T) {
	db, sqlDB, ctx := openBlocklistTestDB(t)

	var existing int
	if err := sqlDB.QueryRowContext(ctx, `select count(*) from users`).Scan(&existing); err != nil {
		t.Fatal(err)
	}
	if existing != 0 {
		t.Skipf("first-admin test requires an empty users table; found %d users", existing)
	}

	prefix := fmt.Sprintf("atomic-setup-%d", time.Now().UnixNano())
	t.Cleanup(func() {
		_, _ = sqlDB.ExecContext(ctx, `delete from users where username like $1`, prefix+"%")
	})

	const callers = 16
	start := make(chan struct{})
	type result struct {
		user    User
		created bool
		err     error
	}
	results := make(chan result, callers)
	var ready sync.WaitGroup
	ready.Add(callers)
	for i := 0; i < callers; i++ {
		go func(i int) {
			ready.Done()
			<-start
			user, created, err := db.CreateInitialAdmin(ctx, fmt.Sprintf("%s-%d", prefix, i), "test-hash")
			results <- result{user: user, created: created, err: err}
		}(i)
	}
	ready.Wait()
	close(start)

	created := 0
	var winner User
	for i := 0; i < callers; i++ {
		result := <-results
		if result.err != nil {
			t.Fatalf("CreateInitialAdmin returned error: %v", result.err)
		}
		if result.created {
			created++
			winner = result.user
		}
	}
	if created != 1 {
		t.Fatalf("created callers = %d, want exactly 1", created)
	}
	if winner.ID == 0 || winner.Role != "admin" {
		t.Fatalf("winner = %+v, want persisted admin", winner)
	}

	var persisted int
	if err := sqlDB.QueryRowContext(ctx,
		`select count(*) from users where username like $1 and role = 'admin'`, prefix+"%",
	).Scan(&persisted); err != nil {
		t.Fatal(err)
	}
	if persisted != 1 {
		t.Fatalf("persisted admins = %d, want 1", persisted)
	}
}
