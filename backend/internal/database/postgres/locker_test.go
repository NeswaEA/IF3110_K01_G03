package postgres

import (
	"database/sql"
	"errors"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/database"
)

func TestLockerIsExclusivePerDatabase(t *testing.T) {
	h := target(t)
	locker := Locker{}
	first, err := locker.Acquire(ctx, h)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := locker.Acquire(ctx, h); !errors.Is(err, database.ErrLockBusy) {
		t.Fatalf("second acquire on the same database: %v, want ErrLockBusy", err)
	}

	// Another database on the same server has its own lock.
	other := otherDatabase(t)
	second, err := locker.Acquire(ctx, other)
	if err != nil {
		t.Fatalf("lock on another database: %v", err)
	}
	second.Release()

	// The lock survives idle time on its connection (the gap between batches).
	if _, err := first.Conn.ExecContext(ctx, "SELECT pg_sleep(0.2)"); err != nil {
		t.Fatal(err)
	}
	if _, err := locker.Acquire(ctx, h); !errors.Is(err, database.ErrLockBusy) {
		t.Fatalf("lock between statements: %v, want ErrLockBusy", err)
	}
	first.Release()
	first.Release() // idempotent

	again, err := locker.Acquire(ctx, h)
	if err != nil {
		t.Fatalf("acquire after release: %v", err)
	}
	again.Release()
}

func TestLockerReleaseEndsSessionInAbortedTransaction(t *testing.T) {
	h := target(t)
	lock, err := Locker{}.Acquire(ctx, h)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lock.Conn.ExecContext(ctx, "BEGIN"); err != nil {
		t.Fatal(err)
	}
	_, _ = lock.Conn.ExecContext(ctx, "SELECT 1/0") // aborts the transaction
	lock.Release()

	again, err := Locker{}.Acquire(ctx, h)
	if err != nil {
		t.Fatalf("acquire after releasing an aborted session: %v", err)
	}
	again.Release()
}

func TestLockerLostWhenBackendTerminated(t *testing.T) {
	h := target(t)
	lock, err := Locker{PingInterval: 100 * time.Millisecond}.Acquire(ctx, h)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Release()
	var pid int
	if err := lock.Conn.QueryRowContext(ctx, "SELECT pg_backend_pid()").Scan(&pid); err != nil {
		t.Fatal(err)
	}
	if _, err := lock.Conn.ExecContext(ctx, "BEGIN"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.DB.ExecContext(ctx, "SELECT pg_terminate_backend($1)", pid); err != nil {
		t.Fatal(err)
	}
	select {
	case <-lock.Lost:
	case <-time.After(5 * time.Second):
		t.Fatal("Lost not closed after the lock backend was terminated")
	}
	if _, err := lock.Conn.ExecContext(ctx, "COMMIT"); err == nil {
		t.Fatal("COMMIT succeeded on a terminated lock connection")
	}
	// The server dropped the lock with the session.
	next, err := Locker{}.Acquire(ctx, h)
	if err != nil {
		t.Fatalf("acquire after the holder was terminated: %v", err)
	}
	next.Release()
}

func TestLockerKeepsLockWhileStatementRuns(t *testing.T) {
	h := target(t)
	lock, err := Locker{PingInterval: 50 * time.Millisecond}.Acquire(ctx, h)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Release()
	rows, err := lock.Conn.QueryContext(ctx, "SELECT g, pg_sleep(0.05) FROM generate_series(1, 6) g")
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() { // the ping must not interleave with the open result set
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	rows.Close()
	select {
	case <-lock.Lost:
		t.Fatal("Lost closed while the connection was healthy")
	default:
	}
}

// otherDatabase opens the same server's "postgres" database, which every role
// may connect to by default.
func otherDatabase(t *testing.T) database.TargetHandle {
	t.Helper()
	u, err := url.Parse(os.Getenv("TEST_TARGET_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/postgres"
	db, err := sql.Open("pgx", u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return database.TargetHandle{DatabaseID: "30000000-0000-4000-8000-000000000099", Driver: database.Postgres, DB: db}
}
