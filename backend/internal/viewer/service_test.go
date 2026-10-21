package viewer

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/database"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/database/databasetest"
)

type fakeReader struct {
	calls         int
	limit, offset int
	err           error
	deadline      bool
}

func (f *fakeReader) ReadTable(ctx context.Context, _ database.TargetHandle, _, _ string, limit, offset int) (database.TableData, bool, error) {
	f.calls++
	f.limit, f.offset = limit, offset
	_, f.deadline = ctx.Deadline()
	return database.TableData{Columns: []database.DataColumn{}, Rows: [][]any{}}, true, f.err
}

type fakeInspector struct{}

func (fakeInspector) Inspect(context.Context, database.TargetHandle) (database.SchemaSnapshot, error) {
	return database.SchemaSnapshot{Driver: database.Postgres}, nil
}
func (fakeInspector) InspectWith(context.Context, database.TargetHandle, database.SQLExecutor) (database.SchemaSnapshot, error) {
	return database.SchemaSnapshot{}, nil
}

func newSvc() (*Service, *databasetest.Provider, *fakeReader) {
	p := databasetest.NewProvider(database.TargetHandle{DatabaseID: "db", Driver: database.Postgres})
	r := &fakeReader{}
	return NewService(p, fakeInspector{}, map[database.Driver]TableReader{database.Postgres: r}, time.Second), p, r
}

func TestDataValidatesBeforeTouchingTarget(t *testing.T) {
	s, p, r := newSvc()
	cases := []struct {
		schema, table string
		limit, offset int
		want          error
	}{
		{"", "items", 0, 0, ErrInvalid},
		{"public", "", 0, 0, ErrInvalid},
		{"public", "items", 201, 0, ErrInvalid},
		{"public", "items", -1, 0, ErrInvalid},
		{"public", "items", 10, -1, ErrInvalid},
		{"public", "items", 10, 100001, ErrInvalid},
		{"_platform", "migration_ledger", 10, 0, database.ErrNotFound},
		{"main", "_platform_migration_ledger", 10, 0, database.ErrNotFound},
	}
	for _, c := range cases {
		if _, _, err := s.Data(context.Background(), "db", c.schema, c.table, c.limit, c.offset); !errors.Is(err, c.want) {
			t.Errorf("%+v: %v", c, err)
		}
	}
	if p.Acquired() != 0 || r.calls != 0 {
		t.Fatal("invalid requests must not reach the target")
	}
}

func TestDataDefaultsReleasesAndBoundsTime(t *testing.T) {
	s, p, r := newSvc()
	_, page, err := s.Data(context.Background(), "db", "public", "items", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if r.limit != DefaultLimit || page.Limit != DefaultLimit || !page.HasMore || !r.deadline {
		t.Fatalf("limit=%d page=%+v deadline=%v", r.limit, page, r.deadline)
	}
	r.err = database.ErrTargetTimeout
	if _, _, err := s.Data(context.Background(), "db", "public", "items", 200, 100000); !errors.Is(err, database.ErrTargetTimeout) {
		t.Fatal(err)
	}
	if p.Acquired() != 2 || p.Released() != 2 {
		t.Fatalf("lease leak: acquired=%d released=%d", p.Acquired(), p.Released())
	}
	if _, _, err := s.Data(context.Background(), "missing", "public", "items", 0, 0); !errors.Is(err, database.ErrNotFound) {
		t.Fatal(err)
	}
}

func TestSchemaReleasesLease(t *testing.T) {
	s, p, _ := newSvc()
	if _, err := s.Schema(context.Background(), "db"); err != nil {
		t.Fatal(err)
	}
	if p.Released() != 1 {
		t.Fatal("lease not released")
	}
}
