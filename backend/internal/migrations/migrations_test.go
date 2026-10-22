package migrations

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/connections"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/contracts"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/database"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/database/databasetest"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/database/postgres"
	sqliteadapter "github.com/NeswaEA/IF3110_K01_G03/backend/internal/database/sqlite"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/platform"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/testkit"
	metadata "github.com/NeswaEA/IF3110_K01_G03/backend/migrations"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	sqlite3 "github.com/mattn/go-sqlite3"
)

var editor = contracts.Principal{UserID: testkit.UserEdit, Username: "editor"}
var viewerPrincipal = contracts.Principal{UserID: testkit.UserView, Username: "viewer"}

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

func metadataDB(t *testing.T) *sql.DB {
	t.Helper()
	cfg, err := pgx.ParseConfig(requireEnv(t, "TEST_METADATA_DATABASE_URL"))
	if err != nil {
		t.Fatal("invalid metadata DSN")
	}
	admin := stdlib.OpenDB(*cfg)
	t.Cleanup(func() { admin.Close() })
	schema := "test_migration_" + strings.ToLower(rand.Text())
	if _, err := admin.Exec(`CREATE SCHEMA "` + schema + `"`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, err := admin.Exec(`DROP SCHEMA "` + schema + `" CASCADE`)
		if err != nil {
			t.Error(err)
		}
	})
	cfg.RuntimeParams["search_path"] = schema
	cfg.RuntimeParams["application_name"] = schema
	db := stdlib.OpenDB(*cfg)
	db.SetMaxOpenConns(5)
	t.Cleanup(func() { db.Close() })
	if err := platform.MigrateMetadata(t.Context(), db, metadata.Files); err != nil {
		t.Fatal(err)
	}
	for _, user := range []struct{ id, name string }{{testkit.UserEdit, "editor"}, {testkit.UserView, "viewer"}, {testkit.UserAdmin, "admin"}} {
		if _, err := db.Exec(`INSERT INTO users(id,username,password_hash) VALUES($1,$2,'$argon2id$test')`, user.id, user.name); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{testkit.DBPG, testkit.DBSQLite, testkit.DBPGDesired, testkit.DBSQLiteDesired} {
		if _, err := db.Exec(`INSERT INTO database_connections(id,name,driver,config_encrypted,migration_ready) VALUES($1,'test','sqlite',$2,true)`, id, []byte{1}); err != nil {
			t.Fatal(err)
		}
	}
	return db
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

func migrationService(t *testing.T, h database.TargetHandle) (*Service, *databasetest.Provider) {
	t.Helper()
	if err := (LedgerInitializer{}).EnsureReady(t.Context(), h); err != nil {
		t.Fatal(err)
	}
	p := databasetest.NewProvider(h)
	inspector := database.DispatchInspector{ByDriver: map[database.Driver]database.Inspector{database.Postgres: postgres.Inspector{}, database.SQLite: sqliteadapter.Inspector{}}}
	s := NewService(metadataDB(t), p, inspector, testkit.NewFixtureAccessChecker(), platform.SystemClock{}, Options{Timeout: 3 * time.Second})
	return s, p
}

func create(t *testing.T, s *Service, h database.TargetHandle, query string) MigrationDraft {
	t.Helper()
	head, err := s.Head(t.Context(), editor, h.DatabaseID)
	if err != nil {
		t.Fatal(err)
	}
	d, err := s.CreateDraft(t.Context(), editor, h.DatabaseID, DraftInput{Title: "test migration", SQL: query, ExpectedVersion: head.Version})
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func input(d MigrationDraft) ExecuteInput {
	id, _ := newID()
	return ExecuteInput{DatabaseID: d.DatabaseID, DraftID: d.ID, Revision: d.Revision, ExpectedVersion: d.ExpectedVersion, IdempotencyKey: id}
}

func assertCode(t *testing.T, err error, code string) {
	t.Helper()
	if !isCode(err, code) {
		t.Fatalf("want %s, got %v", code, err)
	}
}

func uniqueTable(t *testing.T, h database.TargetHandle) string {
	t.Helper()
	name := "migration_" + strings.ToLower(rand.Text())
	t.Cleanup(func() { _, _ = h.DB.Exec(`DROP TABLE IF EXISTS "` + name + `" CASCADE`) })
	if h.Driver == database.SQLite {
		return `"` + name + `"`
	}
	return `public."` + name + `"`
}

func TestLedgerInitializer(t *testing.T) {
	for _, driver := range []database.Driver{database.SQLite, database.Postgres} {
		t.Run(string(driver), func(t *testing.T) {
			h := sqliteTarget(t)
			if driver == database.Postgres {
				h = pgTarget(t)
			}
			for i := 0; i < 2; i++ {
				if err := (LedgerInitializer{}).EnsureReady(t.Context(), h); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := readHead(t.Context(), h.DB, h.Driver); err != nil {
				t.Fatal(err)
			}
			if h.Driver == database.Postgres {
				tx, err := beginTarget(t.Context(), h)
				if err != nil {
					t.Fatal(err)
				}
				defer tx.close()
				if err := tx.asEditor(t.Context(), h.EditorRole); err != nil {
					t.Fatal(err)
				}
				if _, err := tx.ExecContext(t.Context(), `SELECT * FROM _platform.migration_ledger`); err == nil {
					t.Fatal("editor can access ledger")
				}
			}
		})
	}
	for _, ddl := range []string{
		`CREATE TABLE _platform_migration_ledger(version INTEGER PRIMARY KEY)`,
		`CREATE TABLE _platform_migration_ledger(version INTEGER PRIMARY KEY,run_id TEXT NOT NULL,sql_hash TEXT NOT NULL,sql_snapshot TEXT NOT NULL,actor_id TEXT NOT NULL,actor_name_snapshot TEXT NOT NULL,applied_at TEXT NOT NULL)`,
	} {
		h := sqliteTarget(t)
		if _, err := h.DB.Exec(ddl); err != nil {
			t.Fatal(err)
		}
		if err := (LedgerInitializer{}).EnsureReady(t.Context(), h); !errors.Is(err, database.ErrLedgerIncompatible) {
			t.Fatalf("incompatible ledger accepted: %v", err)
		}
	}
}

func TestDraftRevisionAndAccess(t *testing.T) {
	h := sqliteTarget(t)
	s, p := migrationService(t, h)
	d := create(t, s, h, "CREATE TABLE test(id INTEGER);")
	u := UpdateDraftInput{DraftInput: DraftInput{Title: "edited", SQL: "CREATE TABLE changed(id INTEGER);", ExpectedVersion: 0}, Revision: 1}
	updated, err := s.UpdateDraft(t.Context(), editor, h.DatabaseID, d.ID, u)
	if err != nil || updated.Revision != 2 || updated.SQL != u.SQL {
		t.Fatalf("update: %+v %v", updated, err)
	}
	_, err = s.UpdateDraft(t.Context(), editor, h.DatabaseID, d.ID, u)
	assertCode(t, err, "REVISION_CONFLICT")
	_, err = s.GetDraft(t.Context(), editor, testkit.DBPG, d.ID)
	assertCode(t, err, "NOT_FOUND")
	before := p.Acquired()
	_, err = s.CreateDraft(t.Context(), viewerPrincipal, h.DatabaseID, DraftInput{})
	assertCode(t, err, "FORBIDDEN")
	_, err = s.Execute(t.Context(), viewerPrincipal, input(d))
	assertCode(t, err, "FORBIDDEN")
	_, err = s.GetDraft(t.Context(), viewerPrincipal, h.DatabaseID, d.ID)
	assertCode(t, err, "FORBIDDEN")
	if p.Acquired() != before {
		t.Fatal("denied request acquired target")
	}
	if has, err := s.store.HasHistory(t.Context(), h.DatabaseID); err != nil || has {
		t.Fatal("draft counts as history")
	}
}

func TestMigrationTransactions(t *testing.T) {
	for _, driver := range []database.Driver{database.SQLite, database.Postgres} {
		t.Run(string(driver), func(t *testing.T) {
			h := sqliteTarget(t)
			if driver == database.Postgres {
				h = pgTarget(t)
			}
			s, p := migrationService(t, h)
			table := uniqueTable(t, h)
			d := create(t, s, h, `CREATE TABLE `+table+`(id INTEGER PRIMARY KEY,value TEXT); INSERT INTO `+table+` VALUES(1,'a;b'); -- ; in a comment`)
			in := input(d)
			r, err := s.Execute(t.Context(), editor, in)
			if err != nil || r.Status != Succeeded || r.Version == nil || *r.Version != d.ExpectedVersion+1 || r.SQL != d.SQL {
				t.Fatalf("execute: %+v %v", r, err)
			}
			again, err := s.Execute(t.Context(), editor, in)
			if err != nil || again.ID != r.ID {
				t.Fatalf("idempotency: %+v %v", again, err)
			}
			in.Revision++
			_, err = s.Execute(t.Context(), editor, in)
			assertCode(t, err, "IDEMPOTENCY_CONFLICT")
			_, err = s.Execute(t.Context(), editor, input(d))
			assertCode(t, err, "DRAFT_APPLIED")
			var value string
			if err := h.DB.QueryRow(`SELECT value FROM ` + table + ` WHERE id=1`).Scan(&value); err != nil || value != "a;b" {
				t.Fatal(value, err)
			}
			var ledgerSQL string
			if err := h.DB.QueryRow(`SELECT sql_snapshot FROM `+ledgerTable(h.Driver)+` WHERE run_id=$1`, r.ID).Scan(&ledgerSQL); err != nil || ledgerSQL != d.SQL {
				t.Fatal("snapshot differs", err)
			}
			bad := create(t, s, h, `INSERT INTO `+table+` VALUES(2,'rollback'); INSERT INTO missing_migration_table VALUES(1);`)
			failed, err := s.Execute(t.Context(), editor, input(bad))
			assertCode(t, err, "SQL_EXECUTION_FAILED")
			if failed.Status != Failed || failed.Version != nil || failed.Error == nil {
				t.Fatal("incorrect failed run")
			}
			var count int
			if err := h.DB.QueryRow(`SELECT count(*) FROM ` + table).Scan(&count); err != nil || count != 1 {
				t.Fatal("partial changes remained", err)
			}
			head, err := s.Head(t.Context(), editor, h.DatabaseID)
			if err != nil || head.Version != *r.Version {
				t.Fatal("failure advanced head")
			}
			if has, err := s.store.HasHistory(t.Context(), h.DatabaseID); err != nil || !has {
				t.Fatal("run missing from history")
			}
			history, _, err := s.ListRuns(t.Context(), viewerPrincipal, h.DatabaseID, 50, 0)
			if err != nil || len(history) != 2 || history[0].Actor.ID != editor.UserID {
				t.Fatal("history", err)
			}
			if p.Acquired() != p.Released() {
				t.Fatal("lease leak")
			}
			_, err = s.GetRun(t.Context(), editor, testkit.DBPGDesired, r.ID)
			assertCode(t, err, "NOT_FOUND")
		})
	}
}

func TestSQLGuardRollsBackAndRestoresConnection(t *testing.T) {
	for _, driver := range []database.Driver{database.SQLite, database.Postgres} {
		t.Run(string(driver), func(t *testing.T) {
			h := sqliteTarget(t)
			if driver == database.Postgres {
				h = pgTarget(t)
			}
			s, _ := migrationService(t, h)
			table := uniqueTable(t, h)
			first := create(t, s, h, `CREATE TABLE `+table+`(id INTEGER PRIMARY KEY);`)
			if _, err := s.Execute(t.Context(), editor, input(first)); err != nil {
				t.Fatal(err)
			}
			queries := []string{`COMMIT;`, `ROLLBACK;`, `SAVEPOINT attack;`}
			if driver == database.Postgres {
				queries = append(queries, `SET ROLE platform_runner;`, `CREATE INDEX CONCURRENTLY forbidden_idx ON `+table+`(id);`, `DELETE FROM _platform.migration_ledger;`, `SELECT set_config('role','platform_runner',false);`, `COPY `+table+` TO PROGRAM 'echo secret';`)
			} else {
				queries = append(queries, `PRAGMA foreign_keys=OFF;`, `ATTACH DATABASE ':memory:' AS other;`, `DELETE FROM _platform_migration_ledger;`, `ALTER TABLE _platform_migration_ledger RENAME TO stolen;`)
				queries = append(queries, `ALTER TABLE `+table+` RENAME TO _platform_hidden;`, `ALTER TABLE `+table+` RENAME TO '_platform_hidden';`)
			}
			for _, query := range queries {
				d := create(t, s, h, `INSERT INTO `+table+` VALUES(100); `+query)
				_, err := s.Execute(t.Context(), editor, input(d))
				assertCode(t, err, "UNSUPPORTED_SQL")
				var count int
				if err := h.DB.QueryRow(`SELECT count(*) FROM ` + table).Scan(&count); err != nil || count != 0 {
					t.Fatal("guard left changes", err)
				}
			}
			good := create(t, s, h, `ALTER TABLE `+table+` ADD COLUMN name TEXT; INSERT INTO `+table+`(id,name) VALUES(1,'after guard');`)
			if _, err := s.Execute(t.Context(), editor, input(good)); err != nil {
				t.Fatal("connection not reusable", err)
			}
		})
	}
}

func TestCommittedTargetSurvivesMetadataFailure(t *testing.T) {
	for _, driver := range []database.Driver{database.SQLite, database.Postgres} {
		t.Run(string(driver), func(t *testing.T) {
			h := sqliteTarget(t)
			if driver == database.Postgres {
				h = pgTarget(t)
			}
			s, _ := migrationService(t, h)
			table := uniqueTable(t, h)
			d := create(t, s, h, `CREATE TABLE `+table+`(id INTEGER); INSERT INTO `+table+` VALUES(1);`)
			original := s.finalize
			s.finalize = func(context.Context, MigrationRun) error { return errors.New("metadata offline") }
			in := input(d)
			r, err := s.Execute(t.Context(), editor, in)
			if err != nil || r.Status != OutcomeUnknown {
				t.Fatalf("uncertain commit: %+v %v", r, err)
			}
			_, err = s.Execute(t.Context(), editor, input(d))
			assertCode(t, err, "TARGET_OUTCOME_UNKNOWN")
			retry, err := s.Execute(t.Context(), editor, in)
			if err != nil || retry.ID != r.ID {
				t.Fatal("retry changed run")
			}
			s.finalize = original
			recovered, err := s.Reconcile(t.Context(), editor, h.DatabaseID, r.ID)
			if err != nil || recovered.Status != Succeeded {
				t.Fatalf("reconcile: %+v %v", recovered, err)
			}
			if _, err := s.Reconcile(t.Context(), editor, h.DatabaseID, r.ID); err != nil {
				t.Fatal("terminal reconcile", err)
			}
			var count int
			if err := h.DB.QueryRow(`SELECT count(*) FROM ` + table).Scan(&count); err != nil || count != 1 {
				t.Fatal("recovery replayed SQL")
			}
			applied, err := s.GetDraft(t.Context(), editor, h.DatabaseID, d.ID)
			if err != nil || applied.AppliedAt == nil {
				t.Fatal("draft not finalized")
			}
		})
	}
}

func TestStaleHeadAndFingerprint(t *testing.T) {
	h := sqliteTarget(t)
	s, _ := migrationService(t, h)
	d1 := create(t, s, h, "CREATE TABLE first(id INTEGER);")
	d2 := create(t, s, h, "CREATE TABLE second(id INTEGER);")
	if _, err := s.Execute(t.Context(), editor, input(d1)); err != nil {
		t.Fatal(err)
	}
	_, err := s.Execute(t.Context(), editor, input(d2))
	assertCode(t, err, "VERSION_CONFLICT")
	d := create(t, s, h, "CREATE TABLE third(id INTEGER);")
	fp := "sha256:" + strings.Repeat("0", 64)
	_, err = s.UpdateDraft(t.Context(), editor, h.DatabaseID, d.ID, UpdateDraftInput{DraftInput: DraftInput{Title: d.Title, SQL: d.SQL, ExpectedVersion: d.ExpectedVersion, SourceFingerprint: &fp}, Revision: d.Revision, FingerprintProvided: true})
	if err != nil {
		t.Fatal(err)
	}
	d.Revision++
	_, err = s.Execute(t.Context(), editor, input(d))
	assertCode(t, err, "SCHEMA_CONFLICT")
}

func TestUnresolvedRecoveryAndActiveLease(t *testing.T) {
	h := sqliteTarget(t)
	s, p := migrationService(t, h)
	d := create(t, s, h, "CREATE TABLE recovery_only(id INTEGER);")
	in := input(d)
	r, _, _, err := s.createRun(t.Context(), editor, in, inputHash(in))
	if err != nil {
		t.Fatal(err)
	}
	if has, err := s.store.HasHistory(t.Context(), h.DatabaseID); err != nil || !has {
		t.Fatal("running run missing from history")
	}
	release, ok := s.lease(h.DatabaseID)
	if !ok {
		t.Fatal("lease")
	}
	_, err = s.Reconcile(t.Context(), editor, h.DatabaseID, r.ID)
	assertCode(t, err, "RUN_IN_PROGRESS")
	release()
	p.Fail(h.DatabaseID, database.ErrTargetUnavailable)
	unknown, err := s.Reconcile(t.Context(), editor, h.DatabaseID, r.ID)
	if err != nil || unknown.Status != OutcomeUnknown {
		t.Fatal("unavailable target incorrectly resolved")
	}
	if has, err := s.store.HasHistory(t.Context(), h.DatabaseID); err != nil || !has {
		t.Fatal("unknown run missing from history")
	}
	p.Fail(h.DatabaseID, nil)
	if err := s.Recover(t.Context()); err != nil {
		t.Fatal(err)
	}
	r, err = s.GetRun(t.Context(), editor, h.DatabaseID, r.ID)
	if err != nil || r.Status != Failed || r.Error.Code != "EXECUTION_INTERRUPTED" {
		t.Fatalf("startup recovery: %+v %v", r, err)
	}
}

func TestPostgresDeferredConstraintAndTimeout(t *testing.T) {
	h := pgTarget(t)
	s, _ := migrationService(t, h)
	table := uniqueTable(t, h)
	d := create(t, s, h, `CREATE TABLE `+table+`(id INTEGER PRIMARY KEY,parent INTEGER REFERENCES `+table+`(id) DEFERRABLE INITIALLY DEFERRED); INSERT INTO `+table+` VALUES(1,2);`)
	r, err := s.Execute(t.Context(), editor, input(d))
	assertCode(t, err, "SQL_EXECUTION_FAILED")
	if r.Status != Failed {
		t.Fatal("commit constraint claimed success")
	}
	s.timeout = 100 * time.Millisecond
	d = create(t, s, h, "SELECT pg_sleep(5);")
	r, err = s.Execute(t.Context(), editor, input(d))
	if err == nil && r.Status == OutcomeUnknown {
		r, err = s.Reconcile(t.Context(), editor, h.DatabaseID, r.ID)
		if err != nil {
			t.Fatal(err)
		}
	} else {
		assertCode(t, err, "TARGET_TIMEOUT")
	}
	if r.Status != Failed {
		t.Fatal("query timeout unresolved despite rollback")
	}
	s.timeout = 3 * time.Second
	d = create(t, s, h, `CREATE TABLE `+table+`(id INTEGER);`)
	if _, err := s.Execute(t.Context(), editor, input(d)); err != nil {
		t.Fatal("timeout left connection unusable", err)
	}
}

func TestSQLiteLockTimeout(t *testing.T) {
	h := sqliteTarget(t)
	s, _ := migrationService(t, h)
	d := create(t, s, h, "CREATE TABLE after_timeout(id INTEGER);")
	var path string
	if err := h.DB.QueryRow(`SELECT file FROM pragma_database_list WHERE name='main'`).Scan(&path); err != nil {
		t.Fatal(err)
	}
	other, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	conn, err := other.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(t.Context(), "BEGIN IMMEDIATE"); err != nil {
		t.Fatal(err)
	}
	s.timeout = 100 * time.Millisecond
	start := time.Now()
	_, err = s.Execute(t.Context(), editor, input(d))
	assertCode(t, err, "TARGET_TIMEOUT")
	if time.Since(start) > 2*time.Second {
		t.Fatal("lock ignored deadline")
	}
	if _, err := conn.ExecContext(t.Context(), "ROLLBACK"); err != nil {
		t.Fatal(err)
	}
	s.timeout = 3 * time.Second
	if _, err := s.Execute(t.Context(), editor, input(d)); err != nil {
		t.Fatal("busy connection not reusable", err)
	}
}

func TestConcurrentExecuteAndReconcile(t *testing.T) {
	h := sqliteTarget(t)
	s, _ := migrationService(t, h)
	d := create(t, s, h, "CREATE TABLE concurrent_effect(id INTEGER);")
	stale := create(t, s, h, "CREATE TABLE stale_effect(id INTEGER);")
	entered, release := make(chan struct{}), make(chan struct{})
	original := s.finalize
	s.finalize = func(ctx context.Context, r MigrationRun) error { close(entered); <-release; return original(ctx, r) }
	type outcome struct {
		run MigrationRun
		err error
	}
	result := make(chan outcome, 1)
	in := input(d)
	go func() { r, err := s.Execute(context.Background(), editor, in); result <- outcome{r, err} }()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("execute did not reach finalization")
	}
	existing, err := s.Execute(t.Context(), editor, in)
	if err != nil || existing.Status != Running {
		close(release)
		t.Fatal("duplicate did not return stored run", err)
	}
	_, err = s.Execute(t.Context(), editor, input(stale))
	assertCode(t, err, "RUN_IN_PROGRESS")
	_, err = s.Reconcile(t.Context(), editor, h.DatabaseID, existing.ID)
	assertCode(t, err, "RUN_IN_PROGRESS")
	close(release)
	finished := <-result
	if finished.err != nil || finished.run.Status != Succeeded {
		t.Fatal(finished.err)
	}
	s.finalize = original
	_, err = s.Execute(t.Context(), editor, input(stale))
	assertCode(t, err, "VERSION_CONFLICT")
}

func TestUncertainSQLiteCommitNeverReplays(t *testing.T) {
	h := sqliteTarget(t)
	s, _ := migrationService(t, h)
	d := create(t, s, h, "CREATE TABLE interrupted_commit(id INTEGER);")
	setHook := func(hook func() int) {
		conn, err := h.DB.Conn(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		if err := conn.Raw(func(c any) error { c.(*sqlite3.SQLiteConn).RegisterCommitHook(hook); return nil }); err != nil {
			t.Fatal(err)
		}
	}
	setHook(func() int { return 1 })
	defer setHook(nil)
	in := input(d)
	r, err := s.Execute(t.Context(), editor, in)
	if err != nil || r.Status != OutcomeUnknown {
		t.Fatal("unconfirmed commit incorrectly terminal", r.Status, err)
	}
	setHook(nil)
	r, err = s.Reconcile(t.Context(), editor, h.DatabaseID, r.ID)
	if err != nil || r.Status != Failed {
		t.Fatal("missing ledger not recovered", err)
	}
	retry, err := s.Execute(t.Context(), editor, in)
	if err != nil || retry.ID != r.ID || retry.Status != Failed {
		t.Fatal("old key executed again", err)
	}
	var count int
	if err := h.DB.QueryRow(`SELECT count(*) FROM sqlite_schema WHERE name='interrupted_commit'`).Scan(&count); err != nil || count != 0 {
		t.Fatal("recovery created table")
	}
}

func registryFixture(t *testing.T, history connections.HistoryChecker) (*Service, *connections.Service, database.TargetHandle) {
	t.Helper()
	h := sqliteTarget(t)
	var path string
	if err := h.DB.QueryRow(`SELECT file FROM pragma_database_list WHERE name='main'`).Scan(&path); err != nil {
		t.Fatal(err)
	}
	db := metadataDB(t)
	if _, err := db.Exec(`UPDATE database_connections SET active=false WHERE id<>$1`, h.DatabaseID); err != nil {
		t.Fatal(err)
	}
	cipher, err := connections.NewCipher("AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=")
	if err != nil {
		t.Fatal(err)
	}
	config, err := cipher.Seal([]byte(`{"file":"target.sqlite"}`), []byte(h.DatabaseID))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE database_connections SET config_encrypted=$2,migration_ready=false WHERE id=$1`, h.DatabaseID, config); err != nil {
		t.Fatal(err)
	}
	reg := connections.NewRegistry(db, cipher, connections.RegistryOptions{SQLiteDir: filepath.Dir(path)})
	t.Cleanup(reg.Close)
	store := NewStore(db)
	if history == nil {
		history = store
	}
	connectionsService := connections.NewService(db, cipher, reg, LedgerInitializer{}, nil, history, connections.ServiceOptions{SQLiteDir: filepath.Dir(path), Features: connections.Features{Runner: map[database.Driver]bool{database.SQLite: true}}})
	checked, err := connectionsService.Test(t.Context(), h.DatabaseID)
	if err != nil || !checked.Capabilities.TransactionalMigrations {
		t.Fatal("real readiness failed", err)
	}
	s := NewService(db, reg, sqliteadapter.Inspector{}, testkit.NewFixtureAccessChecker(), platform.SystemClock{}, Options{})
	return s, connectionsService, h
}

func TestRegistryUsesRealInitializerAndHistory(t *testing.T) {
	s, connectionsService, h := registryFixture(t, nil)
	d := create(t, s, h, "CREATE TABLE registry_effect(id INTEGER);")
	if _, err := s.Execute(t.Context(), editor, input(d)); err != nil {
		t.Fatal("real provider execute", err)
	}
	if has, err := s.store.HasHistory(t.Context(), h.DatabaseID); err != nil || !has {
		t.Fatal("real history missing")
	}
	_, err := connectionsService.Update(t.Context(), h.DatabaseID, connections.UpdateInput{Revision: 1, Config: []byte(`{"file":"other.sqlite"}`)})
	if !errors.Is(err, connections.ErrIdentityLocked) {
		t.Fatal("real history allowed retarget", err)
	}
	name := "renamed"
	if _, err := connectionsService.Update(t.Context(), h.DatabaseID, connections.UpdateInput{Revision: 1, Name: &name}); err != nil {
		t.Fatal("name update blocked", err)
	}
}

type blockedHistory struct {
	store   *Store
	entered chan struct{}
	release chan struct{}
}

func (h *blockedHistory) HasHistory(ctx context.Context, id string) (bool, error) {
	has, err := h.store.HasHistory(ctx, id)
	close(h.entered)
	select {
	case <-h.release:
		return has, err
	case <-ctx.Done():
		return false, ctx.Err()
	}
}

func TestRetargetSerializesWithFirstRun(t *testing.T) {
	checker := &blockedHistory{entered: make(chan struct{}), release: make(chan struct{})}
	s, connectionService, h := registryFixture(t, checker)
	checker.store = s.store
	defer func() {
		select {
		case <-checker.release:
		default:
			close(checker.release)
		}
	}()
	var path string
	if err := h.DB.QueryRow(`SELECT file FROM pragma_database_list WHERE name='main'`).Scan(&path); err != nil {
		t.Fatal(err)
	}
	other, err := sql.Open("sqlite3", filepath.Join(filepath.Dir(path), "other.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	if err := other.Ping(); err != nil {
		t.Fatal(err)
	}
	other.Close()
	d := create(t, s, h, "CREATE TABLE must_not_reach_old_target(id INTEGER);")
	updated := make(chan error, 1)
	go func() {
		_, err := connectionService.Update(t.Context(), h.DatabaseID, connections.UpdateInput{Revision: 1, Config: []byte(`{"file":"other.sqlite"}`)})
		updated <- err
	}()
	select {
	case <-checker.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("retarget did not reach history check")
	}
	executed := make(chan error, 1)
	go func() { _, err := s.Execute(t.Context(), editor, input(d)); executed <- err }()
	deadline := time.After(3 * time.Second)
	for {
		var waiting bool
		err := s.store.DB.QueryRow(`SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE application_name=current_setting('application_name') AND wait_event_type='Lock')`).Scan(&waiting)
		if err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		select {
		case <-deadline:
			t.Fatal("execute did not wait for retarget transaction")
		case <-time.After(time.Millisecond):
		}
	}
	close(checker.release)
	if err := <-updated; err != nil {
		t.Fatal(err)
	}
	assertCode(t, <-executed, "TARGET_NOT_READY")
	var count int
	if err := h.DB.QueryRow(`SELECT count(*) FROM sqlite_schema WHERE name='must_not_reach_old_target'`).Scan(&count); err != nil || count != 0 {
		t.Fatal("retarget raced execution on old target")
	}
}

func TestNativeSchemaOperations(t *testing.T) {
	for _, engine := range []database.Driver{database.SQLite, database.Postgres} {
		t.Run(string(engine), func(t *testing.T) {
			h := sqliteTarget(t)
			if engine == database.Postgres {
				h = pgTarget(t)
			}
			s, _ := migrationService(t, h)
			table := uniqueTable(t, h)
			index := "migration_index_" + strings.ToLower(rand.Text())
			d := create(t, s, h, `CREATE TABLE `+table+`(id INTEGER PRIMARY KEY,label TEXT);
			 ALTER TABLE `+table+` ADD COLUMN status TEXT;
			 CREATE INDEX "`+index+`" ON `+table+`(status);
			 INSERT INTO `+table+` VALUES(1,'_platform_literal','ready');
			 UPDATE `+table+` SET label='changed' WHERE id=1;
			 ALTER TABLE `+table+` RENAME COLUMN label TO name;
			 DROP INDEX "`+index+`";
			 ALTER TABLE `+table+` DROP COLUMN status;
			 INSERT INTO `+table+` VALUES(2,'removed'); DELETE FROM `+table+` WHERE id=2;`)
			if _, err := s.Execute(t.Context(), editor, input(d)); err != nil {
				t.Fatal("native DDL/DML rejected", err)
			}
			var name string
			if err := h.DB.QueryRow(`SELECT name FROM ` + table + ` WHERE id=1`).Scan(&name); err != nil || name != "changed" {
				t.Fatal(name, err)
			}
			empty := create(t, s, h, "-- no statement;\n/* comment */")
			_, err := s.Execute(t.Context(), editor, input(empty))
			assertCode(t, err, "INVALID_REQUEST")
		})
	}
}
