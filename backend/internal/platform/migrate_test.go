package platform_test

import (
	"context"
	"crypto/rand"
	"database/sql"
	"io/fs"
	"os"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/platform"
	metadata "github.com/NeswaEA/IF3110_K01_G03/backend/migrations"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

func migrationDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("TEST_METADATA_DATABASE_URL")
	if dsn == "" {
		if os.Getenv("CI") != "" {
			t.Fatal("TEST_METADATA_DATABASE_URL is required in CI")
		}
		t.Skip("TEST_METADATA_DATABASE_URL is not set")
	}
	admin, err := platform.OpenMetadataDB(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { admin.Close() })
	schema := "test_migrate_" + strings.ToLower(rand.Text())
	if _, err := admin.Exec(`CREATE SCHEMA "` + schema + `"`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := admin.Exec(`DROP SCHEMA "` + schema + `" CASCADE`); err != nil {
			t.Error("test schema cleanup failed")
		}
	})
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal("invalid test DSN")
	}
	cfg.RuntimeParams["search_path"] = schema
	db := stdlib.OpenDB(*cfg)
	t.Cleanup(func() { db.Close() })
	return db
}

func sqlFiles(entries map[string]string) fstest.MapFS {
	files := fstest.MapFS{}
	for name, body := range entries {
		files[name] = &fstest.MapFile{Data: []byte(body)}
	}
	return files
}

func TestMigrationFilesRejectInvalidInput(t *testing.T) {
	for name, files := range map[string]fs.FS{
		"empty directory":   fstest.MapFS{},
		"invalid name":      sqlFiles(map[string]string{"identity.sql": "SELECT 1"}),
		"duplicate version": sqlFiles(map[string]string{"0001_one.sql": "SELECT 1", "0001_two.sql": "SELECT 2"}),
		"empty migration":   sqlFiles(map[string]string{"0001_empty.sql": " \n"}),
	} {
		t.Run(name, func(t *testing.T) {
			if platform.MigrateMetadata(context.Background(), nil, files) == nil {
				t.Fatal("invalid migration files accepted")
			}
		})
	}
}

func TestMetadataMigrationsReplayAndRerun(t *testing.T) {
	db := migrationDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for i := 0; i < 2; i++ {
		if err := platform.MigrateMetadata(ctx, db, metadata.Files); err != nil {
			t.Fatal(err)
		}
	}
	var count int
	if err := db.QueryRow("SELECT count(*) FROM app_schema_migrations WHERE length(checksum)=64 AND applied_at IS NOT NULL").Scan(&count); err != nil || count != 4 {
		t.Fatal("migration ledger is incomplete")
	}
	var tables int
	if err := db.QueryRow("SELECT count(*) FROM information_schema.tables WHERE table_schema=current_schema() AND table_name IN ('users','groups','database_connections','sessions','session_tokens','group_memberships','group_database_access','migration_drafts','migration_runs')").Scan(&tables); err != nil || tables != 9 {
		t.Fatal("metadata tables are incomplete")
	}
}

func TestMetadataMigrationsRejectHistoryChanges(t *testing.T) {
	db := migrationDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	initial := sqlFiles(map[string]string{"0001_initial.sql": "CREATE TABLE original (id integer);"})
	if err := platform.MigrateMetadata(ctx, db, initial); err != nil {
		t.Fatal(err)
	}
	for name, files := range map[string]fs.FS{
		"checksum": sqlFiles(map[string]string{"0001_initial.sql": "CREATE TABLE original (id text);", "0002_new.sql": "CREATE TABLE pending (id integer);"}),
		"removed":  sqlFiles(map[string]string{"0002_new.sql": "CREATE TABLE pending (id integer);"}),
		"backfill": sqlFiles(map[string]string{"0000_old.sql": "CREATE TABLE pending (id integer);", "0001_initial.sql": "CREATE TABLE original (id integer);"}),
	} {
		t.Run(name, func(t *testing.T) {
			if platform.MigrateMetadata(ctx, db, files) == nil {
				t.Fatal("changed migration history accepted")
			}
			var exists bool
			if err := db.QueryRow("SELECT to_regclass('pending') IS NOT NULL").Scan(&exists); err != nil || exists {
				t.Fatal("new migration ran before history validation")
			}
		})
	}
	if err := platform.MigrateMetadata(ctx, db, initial); err != nil {
		t.Fatal("history validation left the lock held")
	}
}

func TestMetadataMigrationFailureRollsBackBatch(t *testing.T) {
	db := migrationDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	files := sqlFiles(map[string]string{
		"0001_first.sql":  "CREATE TABLE first_table (id integer);",
		"0002_broken.sql": "CREATE TABLE second_table (id integer); SELECT secret_value FROM missing_table;",
	})
	err := platform.MigrateMetadata(ctx, db, files)
	if err == nil || strings.Contains(err.Error(), "secret_value") || strings.Contains(err.Error(), "missing_table") {
		t.Fatal("expected sanitized migration failure")
	}
	var exists bool
	if err := db.QueryRow("SELECT to_regclass('first_table') IS NOT NULL OR to_regclass('second_table') IS NOT NULL OR to_regclass('app_schema_migrations') IS NOT NULL").Scan(&exists); err != nil || exists {
		t.Fatal("failed batch left partial schema or ledger")
	}
	files["0002_broken.sql"].Data = []byte("CREATE TABLE second_table (id integer);")
	if err := platform.MigrateMetadata(ctx, db, files); err != nil {
		t.Fatal(err)
	}
}

func TestConcurrentMetadataMigrations(t *testing.T) {
	db := migrationDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	files := sqlFiles(map[string]string{"0001_initial.sql": "SELECT pg_sleep(0.1); CREATE TABLE once_only (id integer);"})
	start := make(chan struct{})
	results := make(chan error, 2)
	var workers sync.WaitGroup
	for i := 0; i < 2; i++ {
		workers.Add(1)
		go func() { defer workers.Done(); <-start; results <- platform.MigrateMetadata(ctx, db, files) }()
	}
	close(start)
	workers.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatal(err)
		}
	}
	var count int
	if err := db.QueryRow("SELECT count(*) FROM app_schema_migrations").Scan(&count); err != nil || count != 1 {
		t.Fatal("concurrent replay duplicated ledger entries")
	}
}

func TestMetadataMigrationLockCancellation(t *testing.T) {
	db := migrationDB(t)
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec("SELECT pg_advisory_xact_lock($1)", int64(0x4d455441534348)); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	files := sqlFiles(map[string]string{"0001_initial.sql": "CREATE TABLE after_lock (id integer);"})
	if platform.MigrateMetadata(ctx, db, files) == nil {
		t.Fatal("migration bypassed an active replay lock")
	}
	if ctx.Err() == nil {
		t.Fatal("migration failed before lock deadline")
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	retryCtx, retryCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer retryCancel()
	if err := platform.MigrateMetadata(retryCtx, db, files); err != nil {
		t.Fatal(err)
	}
}
