package migrations

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/database"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/testkit"
	_ "github.com/jackc/pgx/v5/stdlib"
	_ "github.com/mattn/go-sqlite3"
)

func requireEnv(t *testing.T, key string) string {
	t.Helper()
	if value := os.Getenv(key); value != "" {
		return value
	}
	if os.Getenv("CI") != "" {
		t.Fatalf("%s is required", key)
	}
	t.Skip(key + " is not set")
	return ""
}

func sqliteTarget(t *testing.T) database.TargetHandle {
	t.Helper()
	path := filepath.Join(t.TempDir(), "target.sqlite")
	db, err := sql.Open("sqlite3", "file:"+filepath.ToSlash(path)+"?_foreign_keys=1&_busy_timeout=5000")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	t.Cleanup(func() { db.Close() })
	return database.TargetHandle{DatabaseID: testkit.DBSQLite, Driver: database.SQLite, DB: db, MigrationReady: true}
}

func pgTarget(t *testing.T) database.TargetHandle {
	t.Helper()
	db, err := sql.Open("pgx", requireEnv(t, "TEST_TARGET_DATABASE_URL"))
	if err != nil {
		t.Fatal("invalid target DSN")
	}
	db.SetMaxOpenConns(5)
	t.Cleanup(func() { db.Close() })
	return database.TargetHandle{DatabaseID: testkit.DBPG, Driver: database.Postgres, DB: db, MigrationReady: true, EditorRole: "platform_editor"}
}

func TestHistoryInitializerIsIdempotentAndHidden(t *testing.T) {
	for _, driver := range []database.Driver{database.SQLite, database.Postgres} {
		t.Run(string(driver), func(t *testing.T) {
			h := sqliteTarget(t)
			if driver == database.Postgres {
				h = pgTarget(t)
			}
			for i := 0; i < 2; i++ {
				if err := (HistoryInitializer{}).EnsureReady(t.Context(), h); err != nil {
					t.Fatal(err)
				}
			}
			history, checkpoints := "_platform_migration_history", ""
			if driver == database.Postgres {
				history, checkpoints = "_platform.migration_history", "_platform.migration_checkpoints"
			}
			var n int
			if err := h.DB.QueryRow("SELECT count(*) FROM " + history).Scan(&n); err != nil {
				t.Fatal(err)
			}
			if driver != database.Postgres {
				return
			}
			if err := h.DB.QueryRow("SELECT count(*) FROM " + checkpoints).Scan(&n); err != nil {
				t.Fatal(err)
			}
			tx, err := beginTarget(t.Context(), h)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.close()
			if err := tx.asEditor(t.Context(), h.EditorRole); err != nil {
				t.Fatal(err)
			}
			if _, err := tx.ExecContext(t.Context(), "SELECT 1 FROM "+history); err == nil {
				t.Fatal("editor can read the history")
			}
		})
	}
}

func TestHistoryInitializerRejectsIncompatibleSQLiteTables(t *testing.T) {
	for name, ddl := range map[string]string{
		"earlier ledger":     `CREATE TABLE _platform_migration_ledger(version INTEGER PRIMARY KEY)`,
		"missing columns":    `CREATE TABLE _platform_migration_history(seq INTEGER PRIMARY KEY, execution_id TEXT NOT NULL UNIQUE)`,
		"execution not uniq": `CREATE TABLE _platform_migration_history(seq INTEGER PRIMARY KEY, execution_id TEXT NOT NULL, migration_id TEXT NOT NULL, migration_revision INTEGER NOT NULL, action TEXT NOT NULL, version INTEGER NOT NULL, head_after INTEGER NOT NULL, reverts_execution_id TEXT, sql_executed TEXT NOT NULL, sql_hash TEXT NOT NULL, mode TEXT NOT NULL, batches INTEGER NOT NULL, rows INTEGER NOT NULL, actor_id TEXT NOT NULL, actor_name TEXT NOT NULL, committed_at TEXT NOT NULL)`,
		"nullable column":    `CREATE TABLE _platform_migration_history(seq INTEGER PRIMARY KEY, execution_id TEXT NOT NULL UNIQUE, migration_id TEXT, migration_revision INTEGER NOT NULL, action TEXT NOT NULL, version INTEGER NOT NULL, head_after INTEGER NOT NULL, reverts_execution_id TEXT, sql_executed TEXT NOT NULL, sql_hash TEXT NOT NULL, mode TEXT NOT NULL, batches INTEGER NOT NULL, rows INTEGER NOT NULL, actor_id TEXT NOT NULL, actor_name TEXT NOT NULL, committed_at TEXT NOT NULL)`,
	} {
		t.Run(name, func(t *testing.T) {
			h := sqliteTarget(t)
			if _, err := h.DB.Exec(ddl); err != nil {
				t.Fatal(err)
			}
			if err := (HistoryInitializer{}).EnsureReady(t.Context(), h); !errors.Is(err, database.ErrLedgerIncompatible) {
				t.Fatalf("incompatible history accepted: %v", err)
			}
		})
	}
}
