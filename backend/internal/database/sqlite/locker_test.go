package sqlite

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/database"
)

func metadataDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("TEST_METADATA_DATABASE_URL")
	if dsn == "" {
		if os.Getenv("CI") != "" {
			t.Fatal("TEST_METADATA_DATABASE_URL is required in CI")
		}
		t.Skip("set TEST_METADATA_DATABASE_URL to run the SQLite locker tests")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func emptyTarget(t *testing.T, id string) database.TargetHandle {
	t.Helper()
	path := filepath.Join(t.TempDir(), "t.sqlite")
	db, err := sql.Open("sqlite3", "file:"+filepath.ToSlash(path)+"?mode=rwc")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	return database.TargetHandle{DatabaseID: id, Driver: database.SQLite, DB: db}
}

func TestLockerSerialisesOneFileAndAllowsOthers(t *testing.T) {
	locker := Locker{Metadata: metadataDB(t)}
	a := emptyTarget(t, "30000000-0000-4000-8000-0000000000a1")
	b := emptyTarget(t, "30000000-0000-4000-8000-0000000000b2")

	first, err := locker.Acquire(ctx, a)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := locker.Acquire(ctx, a); !errors.Is(err, database.ErrLockBusy) {
		t.Fatalf("second acquire on the same file: %v, want ErrLockBusy", err)
	}
	other, err := locker.Acquire(ctx, b)
	if err != nil {
		t.Fatalf("another SQLite target must run in parallel: %v", err)
	}
	other.Release()

	// The pinned connection runs the migration transaction.
	if _, err := first.Conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		t.Fatal(err)
	}
	if _, err := first.Conn.ExecContext(ctx, "CREATE TABLE x (id integer)"); err != nil {
		t.Fatal(err)
	}
	if _, err := first.Conn.ExecContext(ctx, "COMMIT"); err != nil {
		t.Fatal(err)
	}
	first.Release()
	first.Release()

	again, err := locker.Acquire(ctx, a)
	if err != nil {
		t.Fatalf("acquire after release: %v", err)
	}
	again.Release()
}
