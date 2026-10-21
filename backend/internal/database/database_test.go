package database_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/database"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/database/databasetest"
)

func sp(s string) *string { return &s }

func sample() database.SchemaSnapshot {
	return database.SchemaSnapshot{
		DatabaseID: "30000000-0000-4000-8000-000000000001",
		Driver:     database.Postgres,
		Schemas: []database.Schema{{
			Name: "public",
			Tables: []database.Table{
				{
					Name: "orders",
					Columns: []database.Column{
						{Name: "id", DataType: "bigint", Ordinal: 1},
						{Name: "item_id", DataType: "bigint", Ordinal: 2, Nullable: true},
					},
					Constraints: []database.Constraint{
						{Name: sp("orders_pkey"), Kind: database.PrimaryKey, Columns: []string{"id"}, Definition: "PRIMARY KEY (id)"},
						{Name: sp("orders_item_fk"), Kind: database.ForeignKey, Columns: []string{"item_id"},
							Definition: "FOREIGN KEY (item_id) REFERENCES items(id)",
							References: &database.ForeignReference{Schema: "public", Table: "items",
								Columns: []string{"id"}, OnUpdate: "NO ACTION", OnDelete: "CASCADE"}},
					},
					Indexes: []database.Index{
						{Name: "orders_pkey", Unique: true, Primary: true, Columns: []string{"id"}, Definition: "CREATE UNIQUE INDEX orders_pkey ON public.orders (id)"},
						{Name: "orders_item_idx", Columns: []string{"item_id"}, Definition: "CREATE INDEX orders_item_idx ON public.orders (item_id)"},
					},
				},
				{Name: "items", Columns: []database.Column{{Name: "id", DataType: "bigint", Ordinal: 1}}},
			},
		}},
	}
}

func mustFP(t *testing.T, s database.SchemaSnapshot) string {
	t.Helper()
	fp, err := database.Fingerprint(s)
	if err != nil {
		t.Fatal(err)
	}
	return fp
}

func TestFingerprintFormatAndStability(t *testing.T) {
	base := mustFP(t, sample())
	if !strings.HasPrefix(base, "sha256:") || len(base) != len("sha256:")+64 {
		t.Fatalf("bad format %q", base)
	}

	shuffled := sample()
	tb := shuffled.Schemas[0].Tables
	tb[0], tb[1] = tb[1], tb[0]
	o := &tb[1] // orders after swap
	o.Columns[0], o.Columns[1] = o.Columns[1], o.Columns[0]
	o.Constraints[0], o.Constraints[1] = o.Constraints[1], o.Constraints[0]
	o.Indexes[0], o.Indexes[1] = o.Indexes[1], o.Indexes[0]
	shuffled.DatabaseID = "30000000-0000-4000-8000-000000000003" // other target, same schema
	shuffled.Fingerprint = "sha256:ignored"
	if got := mustFP(t, shuffled); got != base {
		t.Fatalf("order/identity changed fingerprint: %s != %s", got, base)
	}
}

func TestFingerprintDetectsChanges(t *testing.T) {
	base := mustFP(t, sample())
	cases := map[string]func(*database.SchemaSnapshot){
		"column type": func(s *database.SchemaSnapshot) { s.Schemas[0].Tables[0].Columns[1].DataType = "integer" },
		"nullable":    func(s *database.SchemaSnapshot) { s.Schemas[0].Tables[0].Columns[1].Nullable = false },
		"constraint action": func(s *database.SchemaSnapshot) {
			s.Schemas[0].Tables[0].Constraints[1].References.OnDelete = "RESTRICT"
		},
		"constraint cols": func(s *database.SchemaSnapshot) { s.Schemas[0].Tables[0].Constraints[0].Columns = []string{"item_id"} },
		"index unique":    func(s *database.SchemaSnapshot) { s.Schemas[0].Tables[0].Indexes[1].Unique = true },
		"drop table":      func(s *database.SchemaSnapshot) { s.Schemas[0].Tables = s.Schemas[0].Tables[:1] },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			s := sample()
			mutate(&s)
			if mustFP(t, s) == base {
				t.Fatal("fingerprint did not change")
			}
		})
	}
}

func TestFingerprintCompositeColumnOrderMatters(t *testing.T) {
	a, b := sample(), sample()
	a.Schemas[0].Tables[0].Indexes[1].Columns = []string{"id", "item_id"}
	b.Schemas[0].Tables[0].Indexes[1].Columns = []string{"item_id", "id"}
	if mustFP(t, a) == mustFP(t, b) {
		t.Fatal("composite index column order must change fingerprint")
	}
}

func TestFingerprintIgnoresReservedObjects(t *testing.T) {
	base := mustFP(t, sample())

	pg := sample()
	pg.Schemas = append(pg.Schemas, database.Schema{Name: "_platform",
		Tables: []database.Table{{Name: "migration_ledger", Columns: []database.Column{{Name: "version", DataType: "bigint", Ordinal: 1}}}}})
	if mustFP(t, pg) != base {
		t.Fatal("_platform schema must be excluded")
	}

	lite := sample()
	lite.Schemas[0].Tables = append(lite.Schemas[0].Tables,
		database.Table{Name: "_platform_migration_ledger", Columns: []database.Column{{Name: "version", DataType: "INTEGER", Ordinal: 1}}})
	if mustFP(t, lite) != base {
		t.Fatal("_platform_ tables must be excluded")
	}
}

func TestFingerprintRejectsMalformed(t *testing.T) {
	cases := map[string]func(*database.SchemaSnapshot){
		"no driver":        func(s *database.SchemaSnapshot) { s.Driver = "" },
		"unknown driver":   func(s *database.SchemaSnapshot) { s.Driver = "mysql" },
		"empty schema":     func(s *database.SchemaSnapshot) { s.Schemas[0].Name = "" },
		"duplicate table":  func(s *database.SchemaSnapshot) { s.Schemas[0].Tables[1].Name = "orders" },
		"bad ordinal":      func(s *database.SchemaSnapshot) { s.Schemas[0].Tables[0].Columns[0].Ordinal = 0 },
		"duplicate column": func(s *database.SchemaSnapshot) { s.Schemas[0].Tables[0].Columns[1].Name = "id" },
		"bad kind":         func(s *database.SchemaSnapshot) { s.Schemas[0].Tables[0].Constraints[0].Kind = "exclusion" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			s := sample()
			mutate(&s)
			if _, err := database.Fingerprint(s); !errors.Is(err, database.ErrInvalidSnapshot) {
				t.Fatalf("want ErrInvalidSnapshot, got %v", err)
			}
		})
	}
}

func TestNormalizeSnapshotNeverNullCollections(t *testing.T) {
	s := database.NormalizeSnapshot(database.SchemaSnapshot{Driver: database.SQLite,
		Schemas: []database.Schema{{Name: "main", Tables: []database.Table{{Name: "t",
			Constraints: []database.Constraint{{Kind: database.ForeignKey,
				References: &database.ForeignReference{}}}}}}}})
	tb := s.Schemas[0].Tables[0]
	if tb.Columns == nil || tb.Indexes == nil || tb.Constraints[0].Columns == nil || tb.Constraints[0].References.Columns == nil {
		t.Fatal("nil collection left behind")
	}
	if database.NormalizeSnapshot(database.SchemaSnapshot{}).Schemas == nil {
		t.Fatal("nil schemas")
	}
}

func TestProviderLeaseDoesNotCloseSharedPool(t *testing.T) {
	h := database.TargetHandle{DatabaseID: "db1", Driver: database.Postgres, Revision: 1, MigrationReady: true}
	p := databasetest.NewProvider(h)
	var tp database.TargetProvider = p

	handle, release, err := tp.Acquire(context.Background(), "db1")
	if err != nil || release == nil {
		t.Fatalf("acquire: %v release=%v", err, release != nil)
	}
	if handle.Revision != 1 {
		t.Fatal("revision lost")
	}
	release()
	release() // double release must not double count
	if p.Acquired() != 1 || p.Released() != 1 {
		t.Fatalf("acquired=%d released=%d", p.Acquired(), p.Released())
	}

	p.SetRevision("db1", 2)
	h2, rel2, _ := tp.Acquire(context.Background(), "db1")
	defer rel2()
	if h2.Revision != 2 {
		t.Fatal("new lease must carry new revision")
	}
}

func TestProviderErrorsReturnNilRelease(t *testing.T) {
	p := databasetest.NewProvider(database.TargetHandle{DatabaseID: "db1"})

	if _, rel, err := p.Acquire(context.Background(), "missing"); !errors.Is(err, database.ErrNotFound) || rel != nil {
		t.Fatalf("missing: err=%v rel=%v", err, rel != nil)
	}
	p.Fail("db1", database.ErrTargetUnavailable)
	if _, rel, err := p.Acquire(context.Background(), "db1"); !errors.Is(err, database.ErrTargetUnavailable) || rel != nil {
		t.Fatalf("fail: err=%v rel=%v", err, rel != nil)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	p.Fail("db1", nil)
	if _, rel, err := p.Acquire(ctx, "db1"); err == nil || rel != nil {
		t.Fatal("cancelled ctx must fail without release")
	}
	if p.Acquired() != 0 {
		t.Fatal("failed acquires must not count")
	}
}

type stubInspector struct{ called bool }

func (s *stubInspector) Inspect(context.Context, database.TargetHandle) (database.SchemaSnapshot, error) {
	s.called = true
	return database.SchemaSnapshot{}, nil
}
func (s *stubInspector) InspectWith(context.Context, database.TargetHandle, database.SQLExecutor) (database.SchemaSnapshot, error) {
	s.called = true
	return database.SchemaSnapshot{}, nil
}

func TestDispatchInspectorRoutesByDriver(t *testing.T) {
	pg, lite := &stubInspector{}, &stubInspector{}
	d := database.DispatchInspector{ByDriver: map[database.Driver]database.Inspector{database.Postgres: pg, database.SQLite: lite}}
	var _ database.Inspector = d

	if _, err := d.Inspect(context.Background(), database.TargetHandle{Driver: database.SQLite}); err != nil {
		t.Fatal(err)
	}
	if pg.called || !lite.called {
		t.Fatal("wrong inspector used")
	}
	if _, err := d.Inspect(context.Background(), database.TargetHandle{Driver: "mysql"}); err == nil {
		t.Fatal("unknown driver must error")
	}
}

type stateErr string

func (e stateErr) Error() string    { return "engine said: host=secret " + string(e) }
func (e stateErr) SQLState() string { return string(e) }

func TestClassifyTargetError(t *testing.T) {
	ctx := context.Background()
	cases := map[error]error{
		stateErr("42501"):                         database.ErrTargetPermission,
		fmt.Errorf("wrap: %w", stateErr("42501")): database.ErrTargetPermission,
		stateErr("57014"):                         database.ErrTargetTimeout,
		stateErr("08006"):                         database.ErrTargetUnavailable,
		errors.New("dial tcp: host=secret"):       database.ErrTargetUnavailable,
		context.DeadlineExceeded:                  database.ErrTargetTimeout,
		database.ErrNotFound:                      database.ErrNotFound,
	}
	for in, want := range cases {
		if got := database.ClassifyTargetError(ctx, in); got != want {
			t.Errorf("%v: got %v, want %v", in, got, want)
		}
	}
	if got := database.ClassifyTargetError(ctx, context.Canceled); !errors.Is(got, context.Canceled) {
		t.Errorf("cancel: %v", got)
	}
}
