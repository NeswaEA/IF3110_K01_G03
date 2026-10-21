package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/database"
)

var ctx = context.Background()

func fixtureDB(t *testing.T) database.TargetHandle {
	t.Helper()
	sqlText, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "tests", "fixtures", "sqlite", "demo.sql"))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "demo.sqlite")
	db, err := sql.Open("sqlite3", "file:"+filepath.ToSlash(path)+"?mode=rwc&_foreign_keys=1&_busy_timeout=5000")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(string(sqlText)); err != nil {
		t.Fatal(err)
	}
	// reserved ledger must stay invisible
	if _, err := db.Exec(`CREATE TABLE _platform_migration_ledger (version INTEGER PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}
	return database.TargetHandle{DatabaseID: "30000000-0000-4000-8000-000000000002", Driver: database.SQLite, DB: db}
}

func table(t *testing.T, s database.SchemaSnapshot, name string) database.Table {
	t.Helper()
	for _, tb := range s.Schemas[0].Tables {
		if tb.Name == name {
			return tb
		}
	}
	t.Fatalf("table %q missing", name)
	return database.Table{}
}

func TestParseChecks(t *testing.T) {
	got := parseChecks(`CREATE TABLE t (
		a INTEGER CHECK (a > 0), -- CHECK (in comment)
		b TEXT DEFAULT 'CHECK (x)' CONSTRAINT "b len" CHECK (length(b) < 10 AND b <> ')'),
		/* CHECK (block comment) */
		CONSTRAINT pos CHECK ((a + 1) > (0)),
		check(b IS NOT NULL)
	)`)
	want := []string{"CHECK (a > 0)", "CHECK (length(b) < 10 AND b <> ')')", "CHECK ((a + 1) > (0))", "CHECK (b IS NOT NULL)"}
	if len(got) != len(want) {
		t.Fatalf("got %d checks: %+v", len(got), got)
	}
	for i, w := range want {
		if got[i].Definition != w || got[i].Kind != database.Check {
			t.Errorf("check %d: %q", i, got[i].Definition)
		}
	}
	if got[0].Name != nil || got[3].Name != nil {
		t.Error("unnamed checks must have nil name")
	}
	if got[1].Name == nil || *got[1].Name != "b len" || got[2].Name == nil || *got[2].Name != "pos" {
		t.Errorf("names: %v %v", got[1].Name, got[2].Name)
	}
}

// Full fixture schema: constraints, composite order, quoted names, hidden reserved objects.
func TestInspectFixture(t *testing.T) {
	h := fixtureDB(t)
	snap, err := Inspector{}.Inspect(ctx, h)
	if err != nil {
		t.Fatal(err)
	}
	if snap.Driver != database.SQLite || len(snap.Schemas) != 1 || snap.Schemas[0].Name != "main" {
		t.Fatalf("schemas %+v", snap.Schemas)
	}
	if len(snap.Schemas[0].Tables) != 7 {
		names := []string{}
		for _, tb := range snap.Schemas[0].Tables {
			names = append(names, tb.Name)
		}
		t.Fatalf("tables %v (ledger must be hidden)", names)
	}
	want, _ := database.Fingerprint(snap)
	if snap.Fingerprint != want {
		t.Fatal("fingerprint not computed with database.Fingerprint")
	}

	items := table(t, snap, "items")
	if c := items.Columns[0]; c.Name != "id" || c.Nullable || c.Ordinal != 1 {
		t.Errorf("id column %+v", c)
	}
	if c := items.Columns[5]; c.Name != "active" || c.Nullable || c.Default == nil || *c.Default != "1" {
		t.Errorf("active column %+v", c)
	}
	if c := items.Columns[3]; c.Name != "note" || !c.Nullable {
		t.Errorf("note column %+v", c)
	}
	kinds := map[database.ConstraintKind]int{}
	for _, c := range items.Constraints {
		kinds[c.Kind]++
	}
	if kinds[database.PrimaryKey] != 1 || kinds[database.Unique] != 1 || kinds[database.Check] != 1 {
		t.Errorf("items constraints %+v", items.Constraints)
	}

	orders := table(t, snap, "orders")
	var fk database.Constraint
	for _, c := range orders.Constraints {
		if c.Kind == database.ForeignKey {
			fk = c
		}
	}
	if fk.References == nil || fk.References.Table != "items" || fk.References.OnDelete != "CASCADE" ||
		!reflect.DeepEqual(fk.References.Columns, []string{"id"}) || fk.Name != nil {
		t.Errorf("orders fk %+v %+v", fk, fk.References)
	}
	if len(orders.Indexes) != 1 || orders.Indexes[0].Name != "orders_item_idx" {
		t.Errorf("orders indexes %+v", orders.Indexes)
	}

	ship := table(t, snap, "shipments")
	if !reflect.DeepEqual(ship.Constraints[0].Columns, []string{"order_id", "line_no"}) ||
		!reflect.DeepEqual(ship.Constraints[0].References.Columns, []string{"order_id", "line_no"}) {
		t.Errorf("composite fk %+v", ship.Constraints[0])
	}
	if !reflect.DeepEqual(ship.Indexes[0].Columns, []string{"carrier", "order_id"}) {
		t.Errorf("composite index %+v", ship.Indexes[0])
	}
	lines := table(t, snap, "order_lines")
	if !reflect.DeepEqual(lines.Constraints[0].Columns, []string{"order_id", "line_no"}) {
		t.Errorf("composite pk %+v", lines.Constraints[0])
	}
	odd := table(t, snap, `Odd "Name"`)
	if odd.Columns[0].Name != "select" || odd.Columns[1].Name != "Mixed Case" {
		t.Errorf("odd identifiers %+v", odd.Columns)
	}

	again, _ := Inspector{}.Inspect(ctx, h)
	if again.Fingerprint != snap.Fingerprint {
		t.Fatal("fingerprint not stable")
	}
}

// Encodings without precision loss, pagination, empty and PK-less tables, quoted names.
func TestReadTable(t *testing.T) {
	h := fixtureDB(t)
	r := Reader{}

	data, more, err := r.ReadTable(ctx, h, "main", "items", 50, 0)
	if err != nil {
		t.Fatal(err)
	}
	if more || !data.OrderStable || len(data.Rows) != 3 {
		t.Fatalf("items: more=%v stable=%v rows=%d", more, data.OrderStable, len(data.Rows))
	}
	if data.Columns[0].Encoding != database.EncDecimalString || data.Columns[4].Encoding != database.EncBase64 {
		t.Fatalf("encodings %+v", data.Columns)
	}
	if data.Rows[1][0] != "9007199254740993" || data.Rows[2][0] != "9223372036854775807" {
		t.Errorf("bigint precision lost: %v %v", data.Rows[1][0], data.Rows[2][0])
	}
	if data.Rows[0][3] != nil || data.Rows[0][4] != "3q2+7w==" {
		t.Errorf("null/binary: %v %v", data.Rows[0][3], data.Rows[0][4])
	}

	page, more, _ := r.ReadTable(ctx, h, "main", "items", 2, 0)
	if !more || len(page.Rows) != 2 {
		t.Errorf("page 1: more=%v rows=%d", more, len(page.Rows))
	}
	page, more, _ = r.ReadTable(ctx, h, "main", "items", 2, 2)
	if more || len(page.Rows) != 1 {
		t.Errorf("page 2: more=%v rows=%d", more, len(page.Rows))
	}

	empty, _, err := r.ReadTable(ctx, h, "main", "empty_table", 50, 0)
	if err != nil || len(empty.Columns) != 2 || empty.Rows == nil || len(empty.Rows) != 0 {
		t.Errorf("empty table %+v %v", empty, err)
	}
	log, _, _ := r.ReadTable(ctx, h, "main", "audit_log", 50, 0)
	if log.OrderStable {
		t.Error("table without PK must report order_stable=false")
	}
	odd, _, err := r.ReadTable(ctx, h, "main", `Odd "Name"`, 50, 0)
	if err != nil || len(odd.Rows) != 1 {
		t.Errorf("odd name: %v", err)
	}

	for _, bad := range []struct{ schema, table string }{
		{"main", "missing"}, {"main", "_platform_migration_ledger"}, {"main", "sqlite_schema"},
		{"other", "items"}, {"main", `items"; DROP TABLE items; --`},
	} {
		if _, _, err := r.ReadTable(ctx, h, bad.schema, bad.table, 50, 0); !errors.Is(err, database.ErrNotFound) {
			t.Errorf("%s.%s: %v", bad.schema, bad.table, err)
		}
	}
	var n int
	if err := h.DB.QueryRow(`SELECT count(*) FROM items`).Scan(&n); err != nil || n != 3 {
		t.Fatal("items table damaged")
	}
}

func TestReadOnlyAuthorizerIsScopedAndRemoved(t *testing.T) {
	h := fixtureDB(t)
	err := withReadOnlyConn(ctx, h.DB, func(tx *sql.Tx) error {
		if _, err := tx.Exec(`INSERT INTO empty_table VALUES (1, 'x')`); err == nil {
			return errors.New("write allowed under read-only authorizer")
		}
		if _, err := tx.Exec(`PRAGMA foreign_keys = OFF`); err == nil {
			return errors.New("protective PRAGMA change allowed")
		}
		if _, err := tx.Exec(`ATTACH DATABASE ':memory:' AS other`); err == nil {
			return errors.New("ATTACH allowed")
		}
		var n int
		return tx.QueryRow(`SELECT count(*) FROM items`).Scan(&n)
	})
	if err != nil {
		t.Fatal(err)
	}
	// MaxOpenConns=1: the same physical connection must be writable again (runner use)
	if _, err := h.DB.Exec(`INSERT INTO empty_table VALUES (1, 'x')`); err != nil {
		t.Fatalf("authorizer leaked to the pooled connection: %v", err)
	}
	var fk int
	if err := h.DB.QueryRow(`PRAGMA foreign_keys`).Scan(&fk); err != nil || fk != 1 {
		t.Fatalf("foreign_keys changed: %d %v", fk, err)
	}
}

func TestDescribePlanDetail(t *testing.T) {
	str := func(p *string) string {
		if p == nil {
			return "<nil>"
		}
		return *p
	}
	for detail, want := range map[string][3]string{
		"SCAN items": {"SCAN", "items", "<nil>"},
		"SEARCH items USING INTEGER PRIMARY KEY (rowid=?)":               {"SEARCH", "items", "<nil>"},
		"SEARCH orders USING INDEX orders_item_idx (item_id=?)":          {"SEARCH", "orders", "orders_item_idx"},
		"SEARCH orders USING COVERING INDEX orders_item_idx (item_id=?)": {"SEARCH", "orders", "orders_item_idx"},
		"SEARCH o USING AUTOMATIC COVERING INDEX (item_id=?)":            {"SEARCH", "o", "<nil>"},
		"SEARCH o USING AUTOMATIC PARTIAL COVERING INDEX (item_id=?)":    {"SEARCH", "o", "<nil>"},
		"USE TEMP B-TREE FOR ORDER BY":                                   {"USE TEMP B-TREE FOR ORDER BY", "<nil>", "<nil>"},
		"CORRELATED SCALAR SUBQUERY 1":                                   {"CORRELATED SCALAR SUBQUERY 1", "<nil>", "<nil>"},
	} {
		op, rel, ix := describe(detail)
		if got := [3]string{op, str(rel), str(ix)}; got != want {
			t.Errorf("%q: %v, want %v", detail, got, want)
		}
	}
}

// Statement boundaries come from the engine, so the driver's tail must stay readable.
func TestClassifyReadsEngineTail(t *testing.T) {
	h := fixtureDB(t)
	cases := map[string]StatementKind{
		"SELECT 1":                    Plannable,
		"SELECT 1; -- done":           Plannable,
		"SELECT 1; SELECT 2":          Multiple,
		"SELECT ';' ; SELECT 2":       Multiple,
		"CREATE TABLE z (a int)":      NoPlan,
		"BEGIN":                       Unsupported,
		"SELECT * FROM _platform_x_y": Unsupported,
	}
	if _, err := h.DB.Exec(`CREATE TABLE _platform_x_y (v int)`); err != nil {
		t.Fatal(err)
	}
	for in, want := range cases {
		if _, kind, err := Classify(ctx, h.DB, in); err != nil || kind != want {
			t.Errorf("%q: kind %d err %v, want %d", in, kind, err, want)
		}
	}
	var ee *database.EngineError
	if _, _, err := Classify(ctx, h.DB, "SELECT ?"); !errors.As(err, &ee) {
		t.Errorf("placeholder: %v", err)
	}
}
