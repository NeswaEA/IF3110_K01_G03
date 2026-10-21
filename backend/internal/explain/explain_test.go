package explain

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	_ "github.com/mattn/go-sqlite3"

	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/database"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/database/databasetest"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/database/postgres"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/database/sqlite"
)

var ctx = context.Background()

func TestClassifySQLite(t *testing.T) {
	ok := map[string]string{
		"SELECT 1":                             "SELECT 1",
		"  select ';' as x ;  ":                "select ';' as x",
		"-- note; here\nSELECT 2":              "-- note; here\nSELECT 2",
		"/* a; b */ DELETE FROM t WHERE 1":     "/* a; b */ DELETE FROM t WHERE 1",
		`SELECT "a;b" FROM [x;y]; -- tail`:     `SELECT "a;b" FROM [x;y]`,
		"WITH c AS (SELECT 1) SELECT * FROM c": "WITH c AS (SELECT 1) SELECT * FROM c",
	}
	for in, want := range ok {
		got, err := classifySQLite(in)
		if err != nil || got != want {
			t.Errorf("%q: got %q %v", in, got, err)
		}
	}
	bad := map[string]error{
		"":                            ErrUnsupportedSQL,
		"  -- only a comment":         ErrUnsupportedSQL,
		"SELECT 1; SELECT 2":          ErrMultipleStatements,
		"SELECT 1; DELETE FROM items": ErrMultipleStatements,
		"BEGIN":                       ErrUnsupportedSQL,
		"COMMIT":                      ErrUnsupportedSQL,
		"EXPLAIN SELECT 1":            ErrUnsupportedSQL,
		"PRAGMA foreign_keys = OFF":   ErrUnsupportedSQL,
		"ATTACH 'x.db' AS x":          ErrUnsupportedSQL,
		"CREATE TABLE t (a int)":      ErrPlanUnsupported,
		"DROP TABLE items":            ErrPlanUnsupported,
	}
	for in, want := range bad {
		if _, err := classifySQLite(in); !errors.Is(err, want) {
			t.Errorf("%q: %v, want %v", in, err, want)
		}
	}
}

func TestClassifyPostgres(t *testing.T) {
	if got, err := classifyPostgres("SELECT ';' AS semi -- c;\n;"); err != nil || got != "SELECT ';' AS semi -- c;" {
		t.Errorf("literal/comment semicolons: %q %v", got, err)
	}
	for _, in := range []string{"SELECT 1", "INSERT INTO t VALUES (1)", "UPDATE t SET a = 1", "DELETE FROM t",
		"WITH x AS (SELECT 1) SELECT * FROM x"} {
		if _, err := classifyPostgres(in); err != nil {
			t.Errorf("%q: %v", in, err)
		}
	}
	cases := map[string]error{
		"SELECT 1; SELECT 2":             ErrMultipleStatements,
		"BEGIN":                          ErrUnsupportedSQL,
		"COMMIT":                         ErrUnsupportedSQL,
		"EXPLAIN ANALYZE SELECT 1":       ErrUnsupportedSQL,
		"SET ROLE postgres":              ErrUnsupportedSQL,
		"COPY t TO PROGRAM 'id'":         ErrUnsupportedSQL,
		"DO $$ BEGIN END $$":             ErrUnsupportedSQL,
		"CREATE TABLE t (a int)":         ErrPlanUnsupported,
		"ALTER TABLE t ADD COLUMN b int": ErrPlanUnsupported,
		"GRANT SELECT ON t TO PUBLIC":    ErrPlanUnsupported,
	}
	for in, want := range cases {
		if _, err := classifyPostgres(in); !errors.Is(err, want) {
			t.Errorf("%q: %v, want %v", in, err, want)
		}
	}
	var ee *database.EngineError
	if _, err := classifyPostgres("SELEC 1"); !errors.As(err, &ee) {
		t.Errorf("syntax error must be an engine error: %v", err)
	}
}

func TestErrorCodes(t *testing.T) {
	if ErrorCode(ErrMultipleStatements) != "UNSUPPORTED_SQL" || ErrorCode(ErrPlanUnsupported) != "PLAN_UNSUPPORTED" ||
		ErrorCode(&database.EngineError{Message: "x"}) != "SQL_EXECUTION_FAILED" || ErrorCode(errors.New("x")) != "" {
		t.Fatal("error code mapping")
	}
}

func sqliteTarget(t *testing.T) database.TargetHandle {
	t.Helper()
	fixture, err := os.ReadFile(filepath.Join("..", "..", "..", "tests", "fixtures", "sqlite", "demo.sql"))
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite3", "file:"+filepath.ToSlash(filepath.Join(t.TempDir(), "d.sqlite"))+"?mode=rwc&_foreign_keys=1")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(string(fixture)); err != nil {
		t.Fatal(err)
	}
	return database.TargetHandle{DatabaseID: "lite", Driver: database.SQLite, DB: db}
}

func pgTarget(t *testing.T) database.TargetHandle {
	t.Helper()
	dsn := os.Getenv("TEST_TARGET_DATABASE_URL")
	if dsn == "" {
		if os.Getenv("CI") != "" {
			t.Fatal("TEST_TARGET_DATABASE_URL is required in CI")
		}
		t.Skip("set TEST_TARGET_DATABASE_URL to run PostgreSQL explain tests")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return database.TargetHandle{DatabaseID: "pg", Driver: database.Postgres, DB: db}
}

func count(t *testing.T, db *sql.DB, q string) int {
	t.Helper()
	var n int
	if err := db.QueryRow(q).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// BONUS-12..BONUS-16, BONUS-18 on real engines.
func TestExplainEngines(t *testing.T) {
	lite := sqliteTarget(t)
	handles := []database.TargetHandle{lite}
	planners := map[database.Driver]Planner{database.SQLite: sqlite.Planner{}, database.Postgres: postgres.Planner{}}
	var pg *database.TargetHandle
	if os.Getenv("TEST_TARGET_DATABASE_URL") != "" || os.Getenv("CI") != "" {
		h := pgTarget(t)
		pg = &h
		handles = append(handles, h)
	}
	p := databasetest.NewProvider(handles...)
	s := NewService(p, planners, 5*time.Second)

	t.Run("sqlite plan, metrics null", func(t *testing.T) {
		res, err := s.Explain(ctx, "lite", "SELECT * FROM items WHERE id = 2", "estimate")
		if err != nil {
			t.Fatal(err)
		}
		if res.Driver != database.SQLite || res.Mode != "estimate" || len(res.Nodes) == 0 || len(res.Warnings) == 0 {
			t.Fatalf("%+v", res)
		}
		n := res.Nodes[0]
		if n.ParentID != nil || n.Relation == nil || *n.Relation != "items" || n.EstimatedCost != nil || n.EstimatedRows != nil || n.ActualTimeMS != nil {
			t.Fatalf("node %+v", n)
		}
		if b, err := json.Marshal(res); err != nil || !json.Valid(b) {
			t.Fatal("result not JSON")
		}
	})

	t.Run("sqlite explain of DML changes nothing", func(t *testing.T) {
		before := count(t, lite.DB, `SELECT count(*) FROM items`)
		if _, err := s.Explain(ctx, "lite", "DELETE FROM items", "estimate"); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Explain(ctx, "lite", "SELECT 1; DELETE FROM items", "estimate"); !errors.Is(err, ErrMultipleStatements) {
			t.Fatal(err)
		}
		if count(t, lite.DB, `SELECT count(*) FROM items`) != before {
			t.Fatal("explain modified data")
		}
		if _, err := lite.DB.Exec(`INSERT INTO empty_table VALUES (7, 'after explain')`); err != nil {
			t.Fatalf("authorizer leaked to pool: %v", err)
		}
	})

	t.Run("sqlite rejections", func(t *testing.T) {
		if _, err := s.Explain(ctx, "lite", "CREATE TABLE z (a int)", "estimate"); !errors.Is(err, ErrPlanUnsupported) {
			t.Error(err)
		}
		if _, err := s.Explain(ctx, "lite", "SELECT 1", "analyze"); !errors.Is(err, ErrUnsupportedSQL) {
			t.Error(err)
		}
		var ee *database.EngineError
		if _, err := s.Explain(ctx, "lite", "SELECT * FROM not_created_yet", "estimate"); !errors.As(err, &ee) {
			t.Errorf("missing table: %v", err)
		}
	})

	if pg == nil {
		t.Skip("PostgreSQL part needs TEST_TARGET_DATABASE_URL")
	}
	t.Run("postgres plan tree", func(t *testing.T) {
		res, err := s.Explain(ctx, "pg", "SELECT o.id FROM orders o JOIN items i ON i.id = o.item_id WHERE i.id = 2", "estimate")
		if err != nil {
			t.Fatal(err)
		}
		if res.Driver != database.Postgres || len(res.Nodes) < 2 || res.Nodes[0].ParentID != nil ||
			res.Nodes[1].ParentID == nil || *res.Nodes[1].ParentID != "0" {
			t.Fatalf("tree %+v", res.Nodes)
		}
		for _, n := range res.Nodes {
			if n.Operation == "" || n.EstimatedCost == nil || n.EstimatedRows == nil || n.ActualTimeMS != nil || n.ActualRows != nil {
				t.Fatalf("node %+v", n)
			}
		}
		if _, ok := res.Raw.([]any); !ok {
			t.Fatalf("raw must be engine JSON array, got %T", res.Raw)
		}
	})
	t.Run("postgres explain of DML changes nothing", func(t *testing.T) {
		before := count(t, pg.DB, `SELECT count(*) FROM public.items`)
		if _, err := s.Explain(ctx, "pg", "DELETE FROM public.items", "estimate"); err != nil {
			t.Fatal(err)
		}
		if count(t, pg.DB, `SELECT count(*) FROM public.items`) != before {
			t.Fatal("explain modified data")
		}
		if count(t, pg.DB, `SELECT count(*) FROM pg_tables WHERE schemaname = '_platform'`) != 0 {
			t.Fatal("explain touched the ledger namespace")
		}
	})
	t.Run("postgres rejections", func(t *testing.T) {
		for in, want := range map[string]error{
			"ALTER TABLE items ADD COLUMN x int": ErrPlanUnsupported,
			"EXPLAIN ANALYZE SELECT 1":           ErrUnsupportedSQL,
			"SELECT 1; SELECT 2":                 ErrMultipleStatements,
		} {
			if _, err := s.Explain(ctx, "pg", in, "estimate"); !errors.Is(err, want) {
				t.Errorf("%q: %v", in, err)
			}
		}
		var ee *database.EngineError
		if _, err := s.Explain(ctx, "pg", "SELECT * FROM not_created_yet", "estimate"); !errors.As(err, &ee) || ee.Message == "" {
			t.Errorf("missing table: %v", err)
		}
	})
	if p.Acquired() != p.Released() {
		t.Fatalf("lease leak %d/%d", p.Acquired(), p.Released())
	}
}
