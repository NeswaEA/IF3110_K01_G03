package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/database"
)

var ctx = context.Background()

// The target is the Compose demo database loaded from tests/fixtures/postgres/demo.sql,
// reached as platform_runner. These tests only read it.
func target(t *testing.T) database.TargetHandle {
	t.Helper()
	dsn := os.Getenv("TEST_TARGET_DATABASE_URL")
	if dsn == "" {
		if os.Getenv("CI") != "" {
			t.Fatal("TEST_TARGET_DATABASE_URL is required in CI")
		}
		t.Skip("set TEST_TARGET_DATABASE_URL to run PostgreSQL adapter tests")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return database.TargetHandle{DatabaseID: "30000000-0000-4000-8000-000000000001", Driver: database.Postgres, DB: db}
}

func find(t *testing.T, s database.SchemaSnapshot, schema, name string) database.Table {
	t.Helper()
	for _, sc := range s.Schemas {
		if sc.Name != schema {
			continue
		}
		for _, tb := range sc.Tables {
			if tb.Name == name {
				return tb
			}
		}
	}
	t.Fatalf("%s.%s missing", schema, name)
	return database.Table{}
}

func constraint(tb database.Table, name string) *database.Constraint {
	for i := range tb.Constraints {
		if tb.Constraints[i].Name != nil && *tb.Constraints[i].Name == name {
			return &tb.Constraints[i]
		}
	}
	return nil
}

// DB-09, DB-10, DB-15, DB-20.
func TestInspectFixture(t *testing.T) {
	h := target(t)
	// a reserved ledger must stay out of the snapshot and the fingerprint
	if _, err := h.DB.Exec(`CREATE TABLE IF NOT EXISTS _platform.inspector_probe (v int)`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = h.DB.Exec(`DROP TABLE IF EXISTS _platform.inspector_probe`) })

	snap, err := Inspector{}.Inspect(ctx, h)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range snap.Schemas {
		if s.Name == "_platform" {
			t.Fatal("_platform schema exposed")
		}
	}
	if fp, _ := database.Fingerprint(snap); fp != snap.Fingerprint {
		t.Fatal("fingerprint mismatch")
	}

	items := find(t, snap, "public", "items")
	if c := items.Columns[0]; c.Name != "id" || c.DataType != "bigint" || c.Nullable || c.Ordinal != 1 {
		t.Errorf("id %+v", c)
	}
	if c := items.Columns[2]; c.DataType != "numeric(20,4)" || !c.Nullable {
		t.Errorf("price %+v", c)
	}
	if c := items.Columns[5]; c.Default == nil || *c.Default != "true" || c.Nullable {
		t.Errorf("active %+v", c)
	}
	if c := constraint(items, "items_pkey"); c == nil || c.Kind != database.PrimaryKey || c.Definition != "PRIMARY KEY (id)" {
		t.Errorf("pk %+v", c)
	}
	if c := constraint(items, "items_name_key"); c == nil || c.Kind != database.Unique {
		t.Errorf("unique %+v", c)
	}
	if c := constraint(items, "items_price_check"); c == nil || c.Kind != database.Check {
		t.Errorf("check %+v", c)
	}

	orders := find(t, snap, "public", "orders")
	fk := constraint(orders, "orders_item_id_fkey")
	if fk == nil || fk.References == nil || fk.References.Table != "items" || fk.References.OnDelete != "CASCADE" ||
		fk.References.OnUpdate != "NO ACTION" || !reflect.DeepEqual(fk.References.Columns, []string{"id"}) {
		t.Errorf("fk %+v", fk)
	}

	ship := find(t, snap, "public", "shipments")
	if len(ship.Constraints) != 1 || !reflect.DeepEqual(ship.Constraints[0].Columns, []string{"order_id", "line_no"}) ||
		!reflect.DeepEqual(ship.Constraints[0].References.Columns, []string{"order_id", "line_no"}) {
		t.Errorf("composite fk %+v", ship.Constraints)
	}
	if len(ship.Indexes) != 1 || !reflect.DeepEqual(ship.Indexes[0].Columns, []string{"carrier", "order_id"}) {
		t.Errorf("composite index %+v", ship.Indexes)
	}
	pkIdx := find(t, snap, "public", "items").Indexes
	var sawPK bool
	for _, ix := range pkIdx {
		if ix.Name == "items_pkey" && ix.Primary && ix.Unique {
			sawPK = true
		}
	}
	if !sawPK {
		t.Errorf("pk index missing %+v", pkIdx)
	}
	odd := find(t, snap, "public", `Odd "Name"`)
	if odd.Columns[0].Name != "select" || odd.Columns[1].Name != "Mixed Case" {
		t.Errorf("odd %+v", odd.Columns)
	}

	before := snap.Fingerprint
	if _, err := h.DB.Exec(`DROP TABLE _platform.inspector_probe`); err != nil {
		t.Fatal(err)
	}
	after, _ := Inspector{}.Inspect(ctx, h)
	if after.Fingerprint != before {
		t.Fatal("reserved object changed the fingerprint")
	}
}

func TestInspectWithUsesCallerTransaction(t *testing.T) {
	h := target(t)
	tx, err := h.DB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`SET LOCAL ROLE platform_editor; CREATE TABLE public.tx_only (id int PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}
	snap, err := Inspector{}.InspectWith(ctx, h, tx)
	if err != nil {
		t.Fatal(err)
	}
	find(t, snap, "public", "tx_only") // visible only inside the caller's transaction
	outside, _ := Inspector{}.Inspect(ctx, h)
	if outside.Fingerprint == snap.Fingerprint {
		t.Fatal("uncommitted table leaked or fingerprint ignores tables")
	}
}

// DB-11..DB-14, DB-10.
func TestReadTable(t *testing.T) {
	h := target(t)
	r := Reader{}
	data, more, err := r.ReadTable(ctx, h, "public", "items", 50, 0)
	if err != nil {
		t.Fatal(err)
	}
	if more || !data.OrderStable || len(data.Rows) != 3 {
		t.Fatalf("more=%v stable=%v rows=%d", more, data.OrderStable, len(data.Rows))
	}
	enc := map[string]database.Encoding{}
	for _, c := range data.Columns {
		enc[c.Name] = c.Encoding
	}
	wantEnc := map[string]database.Encoding{"id": database.EncDecimalString, "name": database.EncText, "price": database.EncDecimalString,
		"note": database.EncText, "payload": database.EncBase64, "active": database.EncBoolean, "created_at": database.EncTimestamp}
	if !reflect.DeepEqual(enc, wantEnc) {
		t.Errorf("encodings %v", enc)
	}
	r0, r1 := data.Rows[0], data.Rows[1]
	if r1[0] != "9007199254740993" || r1[2] != "12345678901234.1234" {
		t.Errorf("precision: %v %v", r1[0], r1[2])
	}
	if r0[3] != nil || r0[4] != "3q2+7w==" || r0[5] != true || r0[6] != "2026-10-20T08:00:00.000000Z" {
		t.Errorf("row0 %v", r0)
	}
	if b, _ := json.Marshal(data.Rows); !json.Valid(b) {
		t.Fatal("rows not JSON-encodable")
	}

	if p, more, _ := r.ReadTable(ctx, h, "public", "items", 2, 0); !more || len(p.Rows) != 2 {
		t.Errorf("page1 more=%v rows=%d", more, len(p.Rows))
	}
	if p, more, _ := r.ReadTable(ctx, h, "public", "items", 2, 2); more || len(p.Rows) != 1 {
		t.Errorf("page2 more=%v rows=%d", more, len(p.Rows))
	}
	if e, _, err := r.ReadTable(ctx, h, "public", "empty_table", 50, 0); err != nil || len(e.Columns) != 2 || e.Rows == nil || len(e.Rows) != 0 {
		t.Errorf("empty %+v %v", e, err)
	}
	if l, _, _ := r.ReadTable(ctx, h, "public", "audit_log", 50, 0); l.OrderStable || len(l.Rows) != 2 {
		t.Errorf("audit_log %+v", l)
	}
	if o, _, err := r.ReadTable(ctx, h, "public", `Odd "Name"`, 50, 0); err != nil || len(o.Rows) != 1 || o.Rows[0][0] != json.Number("1") {
		t.Errorf("odd %+v %v", o, err)
	}
	for _, bad := range [][2]string{{"public", "missing"}, {"_platform", "migration_ledger"},
		{"public", `items"; DROP TABLE public.items; --`}, {"pg_catalog", "pg_class"}} {
		if _, _, err := r.ReadTable(ctx, h, bad[0], bad[1], 50, 0); !errors.Is(err, database.ErrNotFound) {
			t.Errorf("%v: %v", bad, err)
		}
	}
}
