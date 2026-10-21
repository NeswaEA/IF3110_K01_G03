package explain

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
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
	db := sqliteTarget(t).DB
	if _, err := db.Exec(`CREATE TABLE _platform_ledger (v int)`); err != nil {
		t.Fatal(err)
	}
	ok := map[string]string{
		"SELECT 1":                                 "SELECT 1",
		"  select ';' as x ;  ":                    "  select ';' as x ;",
		"-- note; here\nSELECT 2":                  "-- note; here\nSELECT 2",
		"/* a; b */ DELETE FROM items":             "/* a; b */ DELETE FROM items",
		`SELECT "a;b" FROM items; -- tail`:         `SELECT "a;b" FROM items; `,
		"SELECT 1 /* x */ ; /* y */":               "SELECT 1 /* x */ ; ",
		"WITH c AS (SELECT 1) SELECT * FROM c":     "WITH c AS (SELECT 1) SELECT * FROM c",
		"VALUES (1)":                               "VALUES (1)",
		"REPLACE INTO empty_table VALUES (1, 'a')": "REPLACE INTO empty_table VALUES (1, 'a')",
		"UPDATE items SET note = 'x' WHERE id = 2": "UPDATE items SET note = 'x' WHERE id = 2",
		"WITH RECURSIVE n(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM n WHERE x<3) SELECT x FROM n": "WITH RECURSIVE n(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM n WHERE x<3) SELECT x FROM n",
	}
	for in, want := range ok {
		got, err := classifySQLite(ctx, db, in)
		if err != nil || got != want {
			t.Errorf("%q: got %q %v", in, got, err)
		}
	}
	bad := map[string]error{
		"SELECT 1; SELECT 2":                 ErrMultipleStatements,
		"SELECT 1; DELETE FROM items":        ErrMultipleStatements,
		"SELECT 1;;":                         ErrMultipleStatements,
		"BEGIN":                              ErrUnsupportedSQL,
		"COMMIT":                             ErrUnsupportedSQL,
		"SAVEPOINT a":                        ErrUnsupportedSQL,
		"EXPLAIN SELECT 1":                   ErrUnsupportedSQL,
		"explain query plan SELECT 1":        ErrUnsupportedSQL,
		"PRAGMA foreign_keys = OFF":          ErrUnsupportedSQL,
		"PRAGMA table_info(items)":           ErrUnsupportedSQL,
		"ATTACH 'x.db' AS x":                 ErrUnsupportedSQL,
		"VACUUM":                             ErrUnsupportedSQL,
		"ANALYZE":                            ErrUnsupportedSQL,
		"SELECT * FROM _platform_ledger":     ErrUnsupportedSQL,
		"DELETE FROM _platform_ledger":       ErrUnsupportedSQL,
		"CREATE TABLE t (a int)":             ErrPlanUnsupported,
		"DROP TABLE items":                   ErrPlanUnsupported,
		"ALTER TABLE items ADD COLUMN z int": ErrPlanUnsupported,
		"CREATE INDEX ix ON items (note)":    ErrPlanUnsupported,
		"REINDEX":                            ErrPlanUnsupported,
		"SELECT * FROM items WHERE id IN (SELECT v FROM _platform_ledger)": ErrUnsupportedSQL,
		"WITH l AS (SELECT * FROM _platform_ledger) SELECT * FROM l":       ErrUnsupportedSQL,
		// one statement for the engine even though its body holds semicolons
		"CREATE TRIGGER tr AFTER INSERT ON orders BEGIN DELETE FROM audit_log; DELETE FROM empty_table; END": ErrPlanUnsupported,
	}
	for in, want := range bad {
		if _, err := classifySQLite(ctx, db, in); !errors.Is(err, want) {
			t.Errorf("%q: %v, want %v", in, err, want)
		}
	}
	var ee *database.EngineError
	for _, in := range []string{"SELEC 1", "SELECT * FROM not_created_yet"} {
		if _, err := classifySQLite(ctx, db, in); !errors.As(err, &ee) {
			t.Errorf("%q: want engine error, got %v", in, err)
		}
	}
	if _, err := db.Exec(`INSERT INTO empty_table VALUES (5, 'authorizer cleared')`); err != nil {
		t.Fatalf("classification left an authorizer on the pooled connection: %v", err)
	}
}

func TestClassifyPostgres(t *testing.T) {
	if got, err := classifyPostgres("SELECT ';' AS semi -- c;\n;"); err != nil || got != "SELECT ';' AS semi -- c;" {
		t.Errorf("literal/comment semicolons: %q %v", got, err)
	}
	for _, in := range []string{"SELECT 1", "INSERT INTO t VALUES (1)", "UPDATE t SET a = 1", "DELETE FROM t",
		"WITH x AS (SELECT 1) SELECT * FROM x", "SELECT 1 AS _platform", "SELECT _platform FROM t", "SELECT t._platform FROM t"} {
		if _, err := classifyPostgres(in); err != nil {
			t.Errorf("%q: %v", in, err)
		}
	}
	cases := map[string]error{
		"":                                 ErrMultipleStatements,
		"-- only a comment":                ErrMultipleStatements,
		"SELECT 1; SELECT 2":               ErrMultipleStatements,
		"BEGIN":                            ErrUnsupportedSQL,
		"COMMIT":                           ErrUnsupportedSQL,
		"SAVEPOINT a":                      ErrUnsupportedSQL,
		"EXPLAIN ANALYZE SELECT 1":         ErrUnsupportedSQL,
		"SET ROLE postgres":                ErrUnsupportedSQL,
		"SET search_path = _platform":      ErrUnsupportedSQL,
		"RESET ALL":                        ErrUnsupportedSQL,
		"GRANT SELECT ON t TO PUBLIC":      ErrUnsupportedSQL,
		"REVOKE SELECT ON t FROM PUBLIC":   ErrUnsupportedSQL,
		"CREATE ROLE x":                    ErrUnsupportedSQL,
		"COPY t TO PROGRAM 'id'":           ErrUnsupportedSQL,
		"DO $$ BEGIN END $$":               ErrUnsupportedSQL,
		"CALL p()":                         ErrUnsupportedSQL,
		"VACUUM":                           ErrUnsupportedSQL,
		"ANALYZE t":                        ErrUnsupportedSQL,
		"LISTEN x":                         ErrUnsupportedSQL,
		"SELECT * FROM _platform.ledger":   ErrUnsupportedSQL,
		`SELECT * FROM "_platform".ledger`: ErrUnsupportedSQL,
		"SELECT (SELECT count(*) FROM _platform.ledger)":                ErrUnsupportedSQL,
		"WITH l AS (SELECT * FROM _platform.ledger) SELECT * FROM l":    ErrUnsupportedSQL,
		"SELECT * FROM t WHERE id IN (SELECT id FROM _PLATFORM.ledger)": ErrUnsupportedSQL,
		"INSERT INTO _platform.ledger VALUES (1)":                       ErrUnsupportedSQL,
		"SELECT _platform.fn()":                                         ErrUnsupportedSQL,
		"SELECT _platform.ledger.id FROM t":                             ErrUnsupportedSQL,
		"SELECT 1::_platform.ty":                                        ErrUnsupportedSQL,
		"CREATE TABLE t (a int)":                                        ErrPlanUnsupported,
		"ALTER TABLE t ADD COLUMN b int":                                ErrPlanUnsupported,
		"DROP TABLE t":                                                  ErrPlanUnsupported,
		"TRUNCATE t":                                                    ErrPlanUnsupported,
		"COMMENT ON TABLE t IS 'x'":                                     ErrPlanUnsupported,
		"REINDEX TABLE t":                                               ErrPlanUnsupported,
		"CREATE INDEX ON t (a)":                                         ErrPlanUnsupported,
		"SELECT 1 INTO t2":                                              ErrPlanUnsupported,
		"MERGE INTO t USING s ON t.id = s.id WHEN MATCHED THEN DELETE":  ErrPlanUnsupported,
	}
	for in, want := range cases {
		if _, err := classifyPostgres(in); !errors.Is(err, want) {
			t.Errorf("%q: %v, want %v", in, err, want)
		}
	}
	var ee *database.EngineError
	for _, in := range []string{"SELEC 1", "SELECT * FROM t WHERE id = $1", "SELECT (SELECT $2)"} {
		if _, err := classifyPostgres(in); !errors.As(err, &ee) {
			t.Errorf("%q must be an engine error: %v", in, err)
		}
	}
}

func TestErrorCodes(t *testing.T) {
	for err, want := range map[error]string{
		ErrInvalidRequest: "INVALID_REQUEST", ErrPayloadTooLarge: "PAYLOAD_TOO_LARGE",
		ErrMultipleStatements: "UNSUPPORTED_SQL", ErrUnsupportedSQL: "UNSUPPORTED_SQL", ErrPlanUnsupported: "PLAN_UNSUPPORTED",
		&database.EngineError{Message: "x"}: "SQL_EXECUTION_FAILED", errors.New("x"): "",
	} {
		if got := ErrorCode(err); got != want {
			t.Errorf("%v: %q, want %q", err, got, want)
		}
	}
}

// Request checks run in order and never reach the target; PostgreSQL input is
// classified before any connection is opened.
func TestExplainRejectsBeforeTarget(t *testing.T) {
	p := databasetest.NewProvider(database.TargetHandle{DatabaseID: "pg", Driver: database.Postgres},
		database.TargetHandle{DatabaseID: "lite", Driver: database.SQLite})
	planners := map[database.Driver]Planner{database.Postgres: postgres.Planner{}, database.SQLite: sqlite.Planner{}}
	s := NewService(p, p, planners, time.Second)
	cases := []struct {
		id, sql, mode string
		want          error
	}{
		{"pg", strings.Repeat("x", MaxSQLBytes+1), "analyze", ErrPayloadTooLarge},
		{"pg", " \n\t", "analyze", ErrInvalidRequest},
		{"pg", "SELECT 1\x00; DROP TABLE t", "estimate", ErrInvalidRequest},
		{"pg", "SELECT 1", "analyze", ErrUnsupportedSQL},
		{"missing", "SELECT 1", "estimate", database.ErrNotFound},
		{"pg", "SELECT 1; SELECT 2", "estimate", ErrMultipleStatements},
		{"pg", "SELECT * FROM _platform.ledger", "estimate", ErrUnsupportedSQL},
		{"pg", "BEGIN", "estimate", ErrUnsupportedSQL},
		{"pg", "CREATE TABLE z (a int)", "estimate", ErrPlanUnsupported},
	}
	for _, c := range cases {
		if _, err := s.Explain(ctx, c.id, c.sql, c.mode); !errors.Is(err, c.want) {
			t.Errorf("%s %.30q %s: %v, want %v", c.id, c.sql, c.mode, err, c.want)
		}
	}
	if p.Acquired() != 0 {
		t.Fatalf("target opened %d times for rejected input", p.Acquired())
	}
	if _, err := NewService(p, p, map[database.Driver]Planner{}, time.Second).Explain(ctx, "lite", "SELECT 1", "estimate"); !errors.Is(err, ErrPlanUnsupported) {
		t.Fatalf("driver without planner: %v", err)
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
	return database.TargetHandle{DatabaseID: "pg", Driver: database.Postgres, DB: db, EditorRole: "platform_editor"}
}

func count(t *testing.T, db *sql.DB, q string) int {
	t.Helper()
	var n int
	if err := db.QueryRow(q).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// checksum reads a one-value digest of a table so a change to any row is visible.
func checksum(t *testing.T, db *sql.DB, q string) string {
	t.Helper()
	var s sql.NullString
	if err := db.QueryRow(q).Scan(&s); err != nil {
		t.Fatal(err)
	}
	return s.String
}

// Plans, rejections and no side effects, on real engines.
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
	s := NewService(p, p, planners, 5*time.Second)

	t.Run("sqlite plan normalised", func(t *testing.T) {
		res, err := s.Explain(ctx, "lite", "SELECT * FROM items WHERE id = 2", "estimate")
		if err != nil {
			t.Fatal(err)
		}
		if res.Driver != database.SQLite || res.Mode != "estimate" || len(res.Nodes) != 1 ||
			len(res.Warnings) != 1 || res.Warnings[0] != sqlite.PlanWarning {
			t.Fatalf("%+v", res)
		}
		n := res.Nodes[0]
		if n.ParentID != nil || n.Operation != "SEARCH" || n.Relation == nil || *n.Relation != "items" || n.Index != nil ||
			n.EstimatedCost != nil || n.EstimatedRows != nil || n.ActualTimeMS != nil || n.ActualRows != nil {
			t.Fatalf("node %+v", n)
		}
		raw, ok := res.Raw.([]any)
		if !ok || len(raw) != 1 || !strings.HasPrefix(raw[0].(map[string]any)["detail"].(string), "SEARCH items USING INTEGER PRIMARY KEY") {
			t.Fatalf("raw %#v", res.Raw)
		}
		if b, err := json.Marshal(res); err != nil || !json.Valid(b) {
			t.Fatal("result not JSON")
		}

		res, err = s.Explain(ctx, "lite", "SELECT id FROM orders WHERE item_id = 2", "estimate")
		if err != nil || len(res.Nodes) != 1 || res.Nodes[0].Index == nil || *res.Nodes[0].Index != "orders_item_idx" {
			t.Fatalf("named index: %+v %v", res.Nodes, err)
		}
	})

	t.Run("sqlite explain of DML changes nothing", func(t *testing.T) {
		const sum = `SELECT group_concat(id || ':' || coalesce(note, '-'), ',') FROM items`
		before := checksum(t, lite.DB, sum)
		for _, q := range []string{"DELETE FROM items", "UPDATE items SET note = 'changed'",
			"INSERT INTO items (id, name) SELECT id + 100, name || 'x' FROM items"} {
			if _, err := s.Explain(ctx, "lite", q, "estimate"); err != nil {
				t.Fatalf("%s: %v", q, err)
			}
		}
		if _, err := s.Explain(ctx, "lite", "SELECT 1; DELETE FROM items", "estimate"); !errors.Is(err, ErrMultipleStatements) {
			t.Fatal(err)
		}
		if checksum(t, lite.DB, sum) != before {
			t.Fatal("explain modified data")
		}
		// INSERT ... VALUES has no plan rows on SQLite
		if _, err := s.Explain(ctx, "lite", "INSERT INTO empty_table VALUES (8, 'x')", "estimate"); !errors.Is(err, ErrPlanUnsupported) {
			t.Fatalf("empty plan: %v", err)
		}
		if _, err := lite.DB.Exec(`INSERT INTO empty_table VALUES (7, 'after explain')`); err != nil {
			t.Fatalf("authorizer leaked to pool: %v", err)
		}
	})

	t.Run("sqlite rejections", func(t *testing.T) {
		if _, err := s.Explain(ctx, "lite", "CREATE TABLE z (a int)", "estimate"); !errors.Is(err, ErrPlanUnsupported) {
			t.Error(err)
		}
		var ee *database.EngineError
		if _, err := s.Explain(ctx, "lite", "SELECT * FROM not_created_yet", "estimate"); !errors.As(err, &ee) {
			t.Errorf("missing table: %v", err)
		}
		if _, err := s.Explain(ctx, "lite", "SELECT ?", "estimate"); !errors.As(err, &ee) {
			t.Errorf("placeholder: %v", err)
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
		if res.Driver != database.Postgres || len(res.Nodes) < 2 || res.Nodes[0].ID != "0" || res.Nodes[0].ParentID != nil ||
			res.Nodes[1].ID != "1" || res.Nodes[1].ParentID == nil || *res.Nodes[1].ParentID != "0" || len(res.Warnings) != 0 {
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
		const sum = `SELECT md5(string_agg(id::text || ':' || coalesce(note, '-'), ',' ORDER BY id)) FROM public.items`
		before := checksum(t, pg.DB, sum)
		ledger := count(t, pg.DB, `SELECT count(*) FROM pg_tables WHERE schemaname = '_platform'`)
		for _, q := range []string{"DELETE FROM public.items", "UPDATE public.items SET note = 'changed'",
			"INSERT INTO public.items (id, name) VALUES (424242, 'explain only')"} {
			if _, err := s.Explain(ctx, "pg", q, "estimate"); err != nil {
				t.Fatalf("%s: %v", q, err)
			}
		}
		if checksum(t, pg.DB, sum) != before {
			t.Fatal("explain modified data")
		}
		if count(t, pg.DB, `SELECT count(*) FROM pg_tables WHERE schemaname = '_platform'`) != ledger {
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
		for _, in := range []string{"SELECT * FROM not_created_yet", "SELECT $1"} {
			if _, err := s.Explain(ctx, "pg", in, "estimate"); !errors.As(err, &ee) || ee.Message == "" {
				t.Errorf("%q: %v", in, err)
			}
		}
	})
	t.Run("postgres plans as the editor role", func(t *testing.T) {
		// created by the login role, so platform_editor has no privilege on it
		if _, err := pg.DB.Exec(`CREATE TABLE public.runner_only_plan (id int)`); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _, _ = pg.DB.Exec(`DROP TABLE IF EXISTS public.runner_only_plan`) })
		var ee *database.EngineError
		_, err := s.Explain(ctx, "pg", "SELECT * FROM public.runner_only_plan", "estimate")
		if !errors.As(err, &ee) || !strings.Contains(ee.Message, "permission denied") {
			t.Fatalf("want engine permission error, got %v", err)
		}
	})
	if p.Acquired() != p.Released() {
		t.Fatalf("lease leak %d/%d", p.Acquired(), p.Released())
	}
}
