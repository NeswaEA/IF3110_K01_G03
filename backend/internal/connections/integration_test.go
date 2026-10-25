package connections

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/database"
)

// Integration tests need real databases. Locally they skip with a message when
// the variables are unset; with CI set they fail instead of skipping silently.
func requireEnv(t *testing.T, name string) string {
	t.Helper()
	v := os.Getenv(name)
	if v == "" {
		if os.Getenv("CI") != "" {
			t.Fatalf("%s is required in CI", name)
		}
		t.Skipf("set %s to run this integration test", name)
	}
	return v
}

// metaDB creates an isolated schema in the metadata database, applies
// 0002_connections.sql there, and returns a pool bound to that schema.
func metaDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := requireEnv(t, "TEST_METADATA_DATABASE_URL")
	admin, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { admin.Close() })

	var b [6]byte
	_, _ = rand.Read(b[:])
	schema := "t_conn_" + hex.EncodeToString(b[:])
	if _, err := admin.Exec(`CREATE SCHEMA ` + schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = admin.Exec(`DROP SCHEMA ` + schema + ` CASCADE`) })

	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	db, err := sql.Open("pgx", u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	ddl, err := os.ReadFile(filepath.Join("..", "..", "migrations", "0002_connections.sql"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(ddl)); err != nil {
		t.Fatalf("apply 0002: %v", err)
	}
	return db
}

type fakeInit struct {
	mu    sync.Mutex
	err   error
	calls int
}

func (f *fakeInit) EnsureReady(ctx context.Context, h database.TargetHandle) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if h.DB == nil {
		return errors.New("no db in handle")
	}
	return f.err
}

type fakeLifecycle struct {
	err    error
	called []string
}

func (f *fakeLifecycle) Disable(ctx context.Context, tx *sql.Tx, id string) error {
	if tx == nil {
		return errors.New("Disable must run inside the metadata transaction")
	}
	f.called = append(f.called, id)
	return f.err
}

type fakeHistory struct{ has bool }

func (f fakeHistory) HasHistory(context.Context, string) (bool, error) { return f.has, nil }

type env struct {
	meta *sql.DB
	reg  *Registry
	svc  *Service
	init *fakeInit
	life *fakeLifecycle
	dir  string
}

func newEnv(t *testing.T, history HistoryChecker) *env {
	meta := metaDB(t)
	c, err := NewCipher(key32())
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	reg := NewRegistry(meta, c, RegistryOptions{ConnectTimeout: 2 * time.Second, SQLiteDir: dir})
	t.Cleanup(reg.Close)
	in, life := &fakeInit{}, &fakeLifecycle{}
	metaID, err := IdentityFromDSN(os.Getenv("TEST_METADATA_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	svc := NewService(meta, c, reg, in, life, history, ServiceOptions{
		Policy:           Policy{AllowInsecureSSL: true},
		SQLiteDir:        dir,
		MetadataIdentity: metaID,
		Features: Features{
			Runner:  map[database.Driver]bool{database.Postgres: true, database.SQLite: true},
			Diff:    map[database.Driver]bool{database.SQLite: true},
			Explain: map[database.Driver]bool{database.Postgres: true, database.SQLite: true},
		},
	})
	return &env{meta: meta, reg: reg, svc: svc, init: in, life: life, dir: dir}
}

// targetConfig turns TEST_TARGET_DATABASE_URL into a registration config.
func targetConfig(t *testing.T, mut func(map[string]any)) json.RawMessage {
	t.Helper()
	u, err := url.Parse(requireEnv(t, "TEST_TARGET_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	port, _ := strconv.Atoi(u.Port())
	pw, _ := u.User.Password()
	m := map[string]any{"host": u.Hostname(), "port": port, "database": strings.TrimPrefix(u.Path, "/"),
		"username": u.User.Username(), "password": pw, "sslmode": "disable", "editor_role": "platform_editor"}
	if mut != nil {
		mut(m)
	}
	b, _ := json.Marshal(m)
	return b
}

func sqliteFixture(t *testing.T, dir, name string) {
	t.Helper()
	fixture, err := os.ReadFile(filepath.Join("..", "..", "..", "tests", "fixtures", "sqlite", "demo.sql"))
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite3", "file:"+filepath.ToSlash(filepath.Join(dir, name))+"?mode=rwc&_foreign_keys=1")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(string(fixture)); err != nil {
		t.Fatalf("load sqlite fixture: %v", err)
	}
}

func sqliteJSON(file string) json.RawMessage {
	b, _ := json.Marshal(map[string]string{"file": file})
	return b
}

var ctx = context.Background()

// Config is stored encrypted, the DTO is sanitised, and a new registration is not migration_ready.
func TestRegisterStoresEncryptedConfig(t *testing.T) {
	e := newEnv(t, nil)
	cfg := pgJSON(func(m map[string]any) { m["password"] = "very-secret-pw" })
	got, err := e.svc.Register(ctx, RegisterInput{Name: "  PostgreSQL latihan ", Driver: database.Postgres, Config: cfg})
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "PostgreSQL latihan" || got.Revision != 1 || !got.Active || got.MigrationReady {
		t.Fatalf("unexpected DTO %+v", got)
	}
	out, _ := json.Marshal(got)
	if strings.Contains(string(out), "very-secret-pw") || strings.Contains(string(out), "password") {
		t.Fatalf("DTO leaks secret: %s", out)
	}
	var blob []byte
	if err := e.meta.QueryRow(`SELECT config_encrypted FROM database_connections WHERE id = $1`, got.ID).Scan(&blob); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(blob), "very-secret-pw") || strings.Contains(string(blob), "target-postgres") {
		t.Fatal("config stored in plaintext")
	}
}

// The metadata database and a duplicate target identity are rejected.
func TestRegisterRejectsMetadataAndDuplicates(t *testing.T) {
	e := newEnv(t, nil)
	mu, _ := url.Parse(os.Getenv("TEST_METADATA_DATABASE_URL"))
	mport, _ := strconv.Atoi(mu.Port())
	metaCfg := pgJSON(func(m map[string]any) {
		m["host"], m["port"], m["database"] = mu.Hostname(), mport, strings.TrimPrefix(mu.Path, "/")
	})
	if _, err := e.svc.Register(ctx, RegisterInput{Name: "meta", Driver: database.Postgres, Config: metaCfg}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("metadata as target: %v", err)
	}

	if _, err := e.svc.Register(ctx, RegisterInput{Name: "a", Driver: database.Postgres, Config: pgJSON(nil)}); err != nil {
		t.Fatal(err)
	}
	dup := pgJSON(func(m map[string]any) { m["host"], m["username"] = "TARGET-POSTGRES", "other_user" })
	if _, err := e.svc.Register(ctx, RegisterInput{Name: "b", Driver: database.Postgres, Config: dup}); !errors.Is(err, ErrDuplicateTarget) {
		t.Fatalf("duplicate pg: %v", err)
	}

	if _, err := e.svc.Register(ctx, RegisterInput{Name: "s1", Driver: database.SQLite, Config: sqliteJSON("demo.sqlite")}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Register(ctx, RegisterInput{Name: "s2", Driver: database.SQLite, Config: sqliteJSON("demo.sqlite")}); !errors.Is(err, ErrDuplicateTarget) {
		t.Fatalf("duplicate sqlite: %v", err)
	}
	if _, err := e.svc.Register(ctx, RegisterInput{Name: "", Driver: database.SQLite, Config: sqliteJSON("x.db")}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("empty name: %v", err)
	}
}

// A host-name alias (IP, other DNS name) gets the metadata database past the
// identity check; opening its pool still refuses it.
func TestAcquireRejectsMetadataUnderAlias(t *testing.T) {
	e := newEnv(t, nil)
	e.svc.opts.MetadataIdentity = "" // stands in for an alias the name check cannot see
	mu, _ := url.Parse(os.Getenv("TEST_METADATA_DATABASE_URL"))
	mport, _ := strconv.Atoi(mu.Port())
	pw, _ := mu.User.Password()
	cfg := pgJSON(func(m map[string]any) {
		m["host"], m["port"], m["database"] = mu.Hostname(), mport, strings.TrimPrefix(mu.Path, "/")
		m["username"], m["password"], m["editor_role"] = mu.User.Username(), pw, mu.User.Username()
	})
	got, err := e.svc.Register(ctx, RegisterInput{Name: "meta alias", Driver: database.Postgres, Config: cfg})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.reg.Acquire(ctx, got.ID); !errors.Is(err, database.ErrTargetUnavailable) {
		t.Fatalf("metadata under alias: %v", err)
	}
}

func TestListPaginatesAndIncludesInactive(t *testing.T) {
	e := newEnv(t, nil)
	for _, n := range []string{"c", "a", "b"} {
		if _, err := e.svc.Register(ctx, RegisterInput{Name: n, Driver: database.SQLite, Config: sqliteJSON(n + ".db")}); err != nil {
			t.Fatal(err)
		}
	}
	page, more, err := e.svc.List(ctx, 2, 0)
	if err != nil || !more || len(page) != 2 || page[0].Name != "a" || page[1].Name != "b" {
		t.Fatalf("page1 %+v more=%v err=%v", page, more, err)
	}
	if err := e.svc.Deactivate(ctx, page[0].ID); err != nil {
		t.Fatal(err)
	}
	page, more, _ = e.svc.List(ctx, 2, 2)
	if more || len(page) != 1 || page[0].Name != "c" {
		t.Fatalf("page2 %+v", page)
	}
	all, _, _ := e.svc.List(ctx, 10, 0)
	if all[0].Active {
		t.Fatal("inactive row must stay listed with active=false")
	}
}

// Stale revisions conflict; PATCH needs a change, keeps an omitted password, rejects an empty one.
func TestUpdateRevisionAndPasswordRules(t *testing.T) {
	e := newEnv(t, nil)
	c, _ := e.svc.Register(ctx, RegisterInput{Name: "pg", Driver: database.Postgres, Config: pgJSON(nil)})

	if _, err := e.svc.Update(ctx, c.ID, UpdateInput{Revision: 1}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("revision-only patch: %v", err)
	}
	name := "renamed"
	u, err := e.svc.Update(ctx, c.ID, UpdateInput{Revision: 1, Name: &name, Config: json.RawMessage(`{"port":5433}`)})
	if err != nil || u.Revision != 2 || u.Name != "renamed" {
		t.Fatalf("update: %+v %v", u, err)
	}
	stale := "stale"
	if _, err := e.svc.Update(ctx, c.ID, UpdateInput{Revision: 1, Name: &stale}); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("stale revision: %v", err)
	}
	if got, _ := e.svc.Get(ctx, c.ID); got.Name != "renamed" {
		t.Fatal("stale update overwrote data")
	}

	// password omitted keeps the old one; empty password is rejected
	row, _ := e.svc.store.Get(ctx, e.meta, c.ID)
	cfg, _ := e.reg.config(row)
	if cfg.Postgres.Password != "s3cr3t-pw" || cfg.Postgres.Port != 5433 {
		t.Fatalf("merged config wrong: %+v", cfg.Postgres)
	}
	if _, err := e.svc.Update(ctx, c.ID, UpdateInput{Revision: 2, Config: json.RawMessage(`{"password":""}`)}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("empty password: %v", err)
	}
	if _, err := e.svc.Update(ctx, c.ID, UpdateInput{Revision: 2, Config: json.RawMessage(`{"driver":"sqlite"}`)}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("driver change via config: %v", err)
	}
}

func TestConcurrentUpdatesOneWins(t *testing.T) {
	e := newEnv(t, nil)
	c, _ := e.svc.Register(ctx, RegisterInput{Name: "pg", Driver: database.Postgres, Config: pgJSON(nil)})
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			n := "n" + strconv.Itoa(i)
			_, errs[i] = e.svc.Update(ctx, c.ID, UpdateInput{Revision: 1, Name: &n})
		}(i)
	}
	wg.Wait()
	ok, conflict := 0, 0
	for _, err := range errs {
		switch {
		case err == nil:
			ok++
		case errors.Is(err, ErrRevisionConflict):
			conflict++
		default:
			t.Fatal(err)
		}
	}
	if ok != 1 || conflict != 1 {
		t.Fatalf("ok=%d conflict=%d", ok, conflict)
	}
}

func TestIdentityLockedAfterHistory(t *testing.T) {
	locked := newEnv(t, fakeHistory{has: true})
	c, _ := locked.svc.Register(ctx, RegisterInput{Name: "pg", Driver: database.Postgres, Config: pgJSON(nil)})
	if _, err := locked.svc.Update(ctx, c.ID, UpdateInput{Revision: 1, Config: json.RawMessage(`{"database":"other"}`)}); !errors.Is(err, ErrIdentityLocked) {
		t.Fatalf("retarget with history: %v", err)
	}
	if _, err := locked.svc.Update(ctx, c.ID, UpdateInput{Revision: 1, Config: json.RawMessage(`{"password":"rotated-pw"}`)}); err != nil {
		t.Fatalf("password rotation must stay allowed: %v", err)
	}

	free := newEnv(t, fakeHistory{has: false})
	c2, _ := free.svc.Register(ctx, RegisterInput{Name: "pg", Driver: database.Postgres, Config: pgJSON(nil)})
	_, _ = free.meta.Exec(`UPDATE database_connections SET migration_ready = true WHERE id = $1`, c2.ID)
	u, err := free.svc.Update(ctx, c2.ID, UpdateInput{Revision: 1, Config: json.RawMessage(`{"database":"other"}`)})
	if err != nil || u.MigrationReady {
		t.Fatalf("retarget without history must reset readiness: %+v %v", u, err)
	}
}

// Deactivation is atomic with access revocation, keeps the row, and blocks leases.
func TestDeactivate(t *testing.T) {
	e := newEnv(t, nil)
	sqliteFixture(t, e.dir, "demo.sqlite")
	c, _ := e.svc.Register(ctx, RegisterInput{Name: "s", Driver: database.SQLite, Config: sqliteJSON("demo.sqlite")})

	e.life.err = errors.New("revocation failed")
	if err := e.svc.Deactivate(ctx, c.ID); err == nil {
		t.Fatal("lifecycle error must abort deactivation")
	}
	if got, _ := e.svc.Get(ctx, c.ID); !got.Active {
		t.Fatal("deactivation not rolled back")
	}

	e.life.err = nil
	if err := e.svc.Deactivate(ctx, c.ID); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.Deactivate(ctx, c.ID); err != nil {
		t.Fatalf("second deactivate must be idempotent: %v", err)
	}
	if len(e.life.called) < 1 || e.life.called[len(e.life.called)-1] != c.ID {
		t.Fatalf("lifecycle not called: %v", e.life.called)
	}
	got, err := e.svc.Get(ctx, c.ID)
	if err != nil || got.Active {
		t.Fatalf("row must remain, inactive: %+v %v", got, err)
	}
	if _, rel, err := e.reg.Acquire(ctx, c.ID); !errors.Is(err, database.ErrNotFound) || rel != nil {
		t.Fatalf("inactive acquire: %v", err)
	}
	if err := e.svc.Deactivate(ctx, "00000000-0000-4000-8000-000000000000"); !errors.Is(err, database.ErrNotFound) {
		t.Fatalf("unknown id: %v", err)
	}
}

// The outcome of a connection test is recorded; failures keep the row.
func TestConnectionTestRecordsReadiness(t *testing.T) {
	e := newEnv(t, nil)
	sqliteFixture(t, e.dir, "demo.sqlite")
	c, _ := e.svc.Register(ctx, RegisterInput{Name: "s", Driver: database.SQLite, Config: sqliteJSON("demo.sqlite")})

	e.init.err = database.ErrLedgerIncompatible
	if _, err := e.svc.Test(ctx, c.ID); !errors.Is(err, database.ErrLedgerIncompatible) {
		t.Fatalf("want ledger error, got %v", err)
	}
	got, _ := e.svc.Get(ctx, c.ID)
	if got.MigrationReady || got.LastErrorCode == nil || *got.LastErrorCode != "LEDGER_INCOMPATIBLE" || got.LastCheckedAt == nil {
		t.Fatalf("failure not recorded: %+v", got)
	}

	e.init.err = nil
	res, err := e.svc.Test(ctx, c.ID)
	if err != nil || !res.Reachable || !res.Capabilities.TransactionalMigrations || res.Capabilities.ExplainAnalyze {
		t.Fatalf("test result %+v %v", res, err)
	}
	got, _ = e.svc.Get(ctx, c.ID)
	if !got.MigrationReady || got.LastErrorCode != nil || got.Revision != 1 {
		t.Fatalf("success not recorded or revision bumped: %+v", got)
	}
}

// Path checks hold at the registry level, and mode=rw never creates a missing file.
func TestSQLiteAcquireBoundaries(t *testing.T) {
	e := newEnv(t, nil)
	c, _ := e.svc.Register(ctx, RegisterInput{Name: "s", Driver: database.SQLite, Config: sqliteJSON("missing.sqlite")})
	if _, _, err := e.reg.Acquire(ctx, c.ID); !errors.Is(err, database.ErrTargetUnavailable) {
		t.Fatalf("missing file: %v", err)
	}
	if _, err := os.Stat(filepath.Join(e.dir, "missing.sqlite")); !os.IsNotExist(err) {
		t.Fatal("registry created the missing file")
	}

	outside := filepath.Join(t.TempDir(), "outside.sqlite")
	sqliteFixture(t, filepath.Dir(outside), "outside.sqlite")
	if err := os.Symlink(outside, filepath.Join(e.dir, "link.sqlite")); err == nil {
		if _, err := e.svc.Register(ctx, RegisterInput{Name: "l", Driver: database.SQLite, Config: sqliteJSON("link.sqlite")}); !errors.Is(err, ErrInvalid) {
			t.Fatalf("symlink outside registered: %v", err)
		}
	}
}

// Foreign keys are enforced on every pooled SQLite connection.
func TestSQLiteForeignKeysOnEveryConnection(t *testing.T) {
	e := newEnv(t, nil)
	sqliteFixture(t, e.dir, "demo.sqlite")
	c, _ := e.svc.Register(ctx, RegisterInput{Name: "s", Driver: database.SQLite, Config: sqliteJSON("demo.sqlite")})
	h, release, err := e.reg.Acquire(ctx, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	h.DB.SetMaxIdleConns(0) // force a brand-new connection each time
	for i := 0; i < 3; i++ {
		var fk int
		if err := h.DB.QueryRow(`PRAGMA foreign_keys`).Scan(&fk); err != nil || fk != 1 {
			t.Fatalf("connection %d: foreign_keys=%d err=%v", i, fk, err)
		}
	}
	if _, err := h.DB.Exec(`INSERT INTO orders (id, item_id) VALUES (99, 123456)`); err == nil {
		t.Fatal("FK violation accepted")
	}
}

// A writer holding the SQLite file makes reads fail within the busy timeout instead of hanging.
func TestSQLiteBusyWriterDoesNotHang(t *testing.T) {
	e := newEnv(t, nil)
	sqliteFixture(t, e.dir, "demo.sqlite")
	c, _ := e.svc.Register(ctx, RegisterInput{Name: "s", Driver: database.SQLite, Config: sqliteJSON("demo.sqlite")})
	h, release, err := e.reg.Acquire(ctx, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	other, err := sql.Open("sqlite3", "file:"+filepath.ToSlash(filepath.Join(e.dir, "demo.sqlite"))+"?mode=rw")
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	writer, err := other.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	if _, err := writer.ExecContext(ctx, `BEGIN EXCLUSIVE`); err != nil {
		t.Fatal(err)
	}
	defer writer.ExecContext(ctx, `ROLLBACK`)

	start := time.Now()
	var n int
	err = h.DB.QueryRowContext(ctx, `SELECT count(*) FROM items`).Scan(&n)
	if err == nil {
		t.Fatal("read succeeded while another connection held an exclusive lock")
	}
	if got := database.ClassifyTargetError(ctx, err); !errors.Is(got, database.ErrTargetUnavailable) && !errors.Is(got, database.ErrTargetTimeout) {
		t.Fatalf("busy error classified as %v", got)
	}
	if elapsed := time.Since(start); elapsed > 7*time.Second {
		t.Fatalf("busy read took %s", elapsed)
	}
}

// PostgreSQL target tests (TEST_TARGET_DATABASE_URL).

// A config update gives new leases a new pool; the old pool closes after its last lease.
func TestPoolInvalidationOnRevision(t *testing.T) {
	e := newEnv(t, nil)
	c, err := e.svc.Register(ctx, RegisterInput{Name: "pg", Driver: database.Postgres, Config: targetConfig(t, nil)})
	if err != nil {
		t.Fatal(err)
	}
	h1, rel1, err := e.reg.Acquire(ctx, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if h1.EditorRole != "platform_editor" {
		t.Fatalf("editor role not carried on the lease: %q", h1.EditorRole)
	}
	if d, err := e.reg.Driver(ctx, c.ID); err != nil || d != database.Postgres {
		t.Fatalf("driver lookup: %q %v", d, err)
	}
	name := "renamed"
	if _, err := e.svc.Update(ctx, c.ID, UpdateInput{Revision: 1, Name: &name}); err != nil {
		t.Fatal(err)
	}
	h2, rel2, err := e.reg.Acquire(ctx, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer rel2()
	if h2.Revision != 2 || h2.DB == h1.DB {
		t.Fatalf("new revision must get a new pool: rev=%d same=%v", h2.Revision, h2.DB == h1.DB)
	}
	if err := h1.DB.PingContext(ctx); err != nil {
		t.Fatalf("old pool closed while still leased: %v", err)
	}
	rel1()
	rel1() // second call is a no-op
	deadline := time.Now().Add(2 * time.Second)
	for h1.DB.PingContext(ctx) == nil {
		if time.Now().After(deadline) {
			t.Fatal("old pool not closed after last release")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err := h2.DB.PingContext(ctx); err != nil {
		t.Fatalf("current pool affected: %v", err)
	}
}

// Wrong credentials give a controlled error without DSN or password.
func TestWrongCredentialsAreSanitised(t *testing.T) {
	e := newEnv(t, nil)
	c, _ := e.svc.Register(ctx, RegisterInput{Name: "pg", Driver: database.Postgres,
		Config: targetConfig(t, func(m map[string]any) { m["password"] = "definitely-wrong-pw" })})
	_, _, err := e.reg.Acquire(ctx, c.ID)
	if !errors.Is(err, database.ErrTargetUnavailable) {
		t.Fatalf("want unavailable, got %v", err)
	}
	if strings.Contains(err.Error(), "definitely-wrong-pw") || strings.Contains(err.Error(), "postgres://") {
		t.Fatalf("error leaks secret: %v", err)
	}
	_, err = e.svc.Test(ctx, c.ID)
	got, _ := e.svc.Get(ctx, c.ID)
	if err == nil || got.LastErrorCode == nil || *got.LastErrorCode != "TARGET_UNAVAILABLE" {
		t.Fatalf("test failure not recorded: %v %+v", err, got)
	}
}

// An unreachable target fails within the connect deadline.
func TestUnreachableTargetHonoursDeadline(t *testing.T) {
	e := newEnv(t, nil)
	e.reg.opts.ConnectTimeout = 700 * time.Millisecond
	c, _ := e.svc.Register(ctx, RegisterInput{Name: "pg", Driver: database.Postgres,
		Config: targetConfig(t, func(m map[string]any) { m["host"], m["port"] = "192.0.2.1", 5432 })}) // TEST-NET-1, never routed
	start := time.Now()
	_, _, err := e.reg.Acquire(ctx, c.ID)
	if !errors.Is(err, database.ErrTargetTimeout) && !errors.Is(err, database.ErrTargetUnavailable) {
		t.Fatalf("unexpected error %v", err)
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Fatalf("acquire hung for %v", d)
	}
}

func TestConnectionTestChecksEditorRole(t *testing.T) {
	e := newEnv(t, nil)
	good, _ := e.svc.Register(ctx, RegisterInput{Name: "pg", Driver: database.Postgres, Config: targetConfig(t, nil)})
	if res, err := e.svc.Test(ctx, good.ID); err != nil || !res.Reachable {
		t.Fatalf("real target: %+v %v", res, err)
	}
	if e.init.calls != 1 {
		t.Fatalf("initializer calls %d", e.init.calls)
	}

	e2 := newEnv(t, nil)
	bad, _ := e2.svc.Register(ctx, RegisterInput{Name: "pg", Driver: database.Postgres,
		Config: targetConfig(t, func(m map[string]any) { m["editor_role"] = "no_such_role" })})
	if _, err := e2.svc.Test(ctx, bad.ID); !errors.Is(err, ErrInvalid) {
		t.Fatalf("missing editor role: %v", err)
	}
}

func TestWithDiffConfig(t *testing.T) {
	e := newEnv(t, nil)
	src, _ := e.svc.Register(ctx, RegisterInput{Name: "src", Driver: database.Postgres, Config: targetConfig(t, nil)})
	dst, _ := e.svc.Register(ctx, RegisterInput{Name: "dst", Driver: database.Postgres,
		Config: targetConfig(t, func(m map[string]any) { m["database"] = "desired" })})

	var seen string
	err := e.reg.WithDiffConfig(ctx, src.ID, dst.ID, func(path string) error {
		seen = path
		if !filepath.IsAbs(path) {
			t.Errorf("path not absolute: %s", path)
		}
		st, err := os.Stat(path)
		if err != nil {
			return err
		}
		if runtime.GOOS != "windows" && st.Mode().Perm() != 0o600 {
			t.Errorf("mode %v", st.Mode().Perm())
		}
		b, _ := os.ReadFile(path)
		if !strings.Contains(string(b), `env "source"`) || !strings.Contains(string(b), `env "desired"`) {
			t.Errorf("config content: %s", b)
		}
		pw, _ := url.Parse(os.Getenv("TEST_TARGET_DATABASE_URL"))
		p, _ := pw.User.Password()
		return errors.New("atlas failed for " + p + " using " + path)
	})
	if err == nil {
		t.Fatal("callback error must propagate")
	}
	pw, _ := url.Parse(os.Getenv("TEST_TARGET_DATABASE_URL"))
	if p, _ := pw.User.Password(); strings.Contains(err.Error(), p) || strings.Contains(err.Error(), seen) {
		t.Fatalf("callback error not redacted: %v", err)
	}
	if _, err := os.Stat(seen); !os.IsNotExist(err) {
		t.Fatal("temporary config not removed")
	}

	sqliteFixture(t, e.dir, "a.sqlite")
	lite, _ := e.svc.Register(ctx, RegisterInput{Name: "lite", Driver: database.SQLite, Config: sqliteJSON("a.sqlite")})
	if err := e.reg.WithDiffConfig(ctx, src.ID, lite.ID, func(string) error { return nil }); !errors.Is(err, database.ErrDiffUnsupported) {
		t.Fatalf("cross-engine: %v", err)
	}
	if err := e.reg.WithDiffConfig(ctx, lite.ID, lite.ID, func(string) error { return nil }); !errors.Is(err, database.ErrDiffUnsupported) {
		t.Fatalf("sqlite: %v", err)
	}
}

type initializerFunc func(context.Context, database.TargetHandle) error

func (f initializerFunc) EnsureReady(ctx context.Context, h database.TargetHandle) error {
	return f(ctx, h)
}

func TestConnectionReadinessDoesNotCrossRevisions(t *testing.T) {
	e := newEnv(t, nil)
	sqliteFixture(t, e.dir, "first.sqlite")
	connection, err := e.svc.Register(ctx, RegisterInput{Name: "revision probe", Driver: database.SQLite, Config: sqliteJSON("first.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	e.svc.init = initializerFunc(func(context.Context, database.TargetHandle) error { close(entered); <-release; return nil })
	finished := make(chan error, 1)
	go func() { _, err := e.svc.Test(ctx, connection.ID); finished <- err }()
	<-entered
	_, updateErr := e.svc.Update(ctx, connection.ID, UpdateInput{Revision: 1, Config: sqliteJSON("second.sqlite")})
	close(release)
	if updateErr != nil {
		t.Fatal(updateErr)
	}
	if err := <-finished; !errors.Is(err, ErrRevisionConflict) {
		t.Fatal("stale probe marked new revision ready", err)
	}
	current, err := e.svc.Get(ctx, connection.ID)
	if err != nil || current.MigrationReady {
		t.Fatal("new target falsely ready")
	}
}
