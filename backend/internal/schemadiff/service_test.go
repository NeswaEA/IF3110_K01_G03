package schemadiff

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/contracts"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/database"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/database/databasetest"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/testkit"
)

var principal = contracts.Principal{UserID: testkit.UserEdit, Username: "editor"}

func ptr(value string) *string { return &value }

func clone[T any](value T) T {
	encoded, _ := json.Marshal(value)
	var copied T
	_ = json.Unmarshal(encoded, &copied)
	return copied
}

type snapshotInspector struct {
	snapshots map[string]database.SchemaSnapshot
	errors    map[string]error
	calls     int
}

func (i *snapshotInspector) Inspect(ctx context.Context, h database.TargetHandle) (database.SchemaSnapshot, error) {
	i.calls++
	if err := ctx.Err(); err != nil {
		return database.SchemaSnapshot{}, err
	}
	return clone(i.snapshots[h.DatabaseID]), i.errors[h.DatabaseID]
}

func (i *snapshotInspector) InspectWith(context.Context, database.TargetHandle, database.SQLExecutor) (database.SchemaSnapshot, error) {
	panic("comparison must use the shared inspector")
}

type generatorFunc func(context.Context, Comparison) (Candidate, error)

func (f generatorFunc) Generate(ctx context.Context, c Comparison) (Candidate, error) {
	return f(ctx, c)
}

type accessFunc struct {
	contracts.AccessChecker
	require func(context.Context, contracts.Principal, contracts.Action, string) error
}

func (a accessFunc) Require(ctx context.Context, p contracts.Principal, action contracts.Action, id string) error {
	return a.require(ctx, p, action, id)
}

type serviceFixture struct {
	service   *Service
	provider  *databasetest.Provider
	inspector *snapshotInspector
	in        DiffInput
}

func fixture() serviceFixture {
	source := database.SchemaSnapshot{DatabaseID: testkit.DBSQLite, Driver: database.SQLite,
		Schemas: []database.Schema{{Name: "main", Tables: []database.Table{{Name: "items", Columns: []database.Column{{Name: "id", DataType: "INTEGER", Ordinal: 1}}}}}}}
	desired := clone(source)
	desired.DatabaseID = testkit.DBSQLiteDesired
	desired.Schemas[0].Tables[0].Columns = append(desired.Schemas[0].Tables[0].Columns, database.Column{Name: "status", DataType: "TEXT", Nullable: true, Ordinal: 2})
	provider := databasetest.NewProvider(
		database.TargetHandle{DatabaseID: source.DatabaseID, Driver: source.Driver, MigrationReady: true},
		database.TargetHandle{DatabaseID: desired.DatabaseID, Driver: desired.Driver},
	)
	inspector := &snapshotInspector{snapshots: map[string]database.SchemaSnapshot{source.DatabaseID: source, desired.DatabaseID: desired}, errors: map[string]error{}}
	s := NewService(provider, inspector, testkit.NewFixtureAccessChecker(), func(context.Context, contracts.Principal, string) (int64, error) { return 7, nil }, Options{})
	return serviceFixture{s, provider, inspector, DiffInput{desired.DatabaseID, "main"}}
}

func assertCode(t *testing.T, err error, code string) {
	t.Helper()
	var domain *contracts.DomainError
	if !errors.As(err, &domain) || domain.Code != code {
		t.Fatalf("want %s, got %v", code, err)
	}
}

func assertReleased(t *testing.T, f serviceFixture, acquired int64) {
	t.Helper()
	if f.provider.Acquired() != acquired || f.provider.Released() != acquired {
		t.Fatal("unexpected lease counts", f.provider.Acquired(), f.provider.Released())
	}
}

func TestDiffRequiresBothPermissionsBeforeOpeningTargets(t *testing.T) {
	for _, tc := range []struct {
		name string
		user contracts.Principal
		deny string
		code string
	}{
		{"anonymous", contracts.Principal{}, "", "UNAUTHENTICATED"},
		{"view on source", contracts.Principal{UserID: testkit.UserView}, "", "FORBIDDEN"},
		{"admin without grants", contracts.Principal{UserID: testkit.UserAdmin, IsPlatformAdmin: true}, "", "NOT_FOUND"},
		{"no view on desired", principal, testkit.DBSQLiteDesired, "NOT_FOUND"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := fixture()
			if tc.deny != "" {
				original := f.service.access
				f.service.access = accessFunc{AccessChecker: original, require: func(ctx context.Context, p contracts.Principal, action contracts.Action, id string) error {
					if id == tc.deny {
						return problem("NOT_FOUND", "Database tidak ditemukan.")
					}
					return original.Require(ctx, p, action, id)
				}}
			}
			f.service.head = func(context.Context, contracts.Principal, string) (int64, error) {
				t.Fatal("denied request read head")
				return 0, nil
			}
			f.service.generators[database.SQLite] = generatorFunc(func(context.Context, Comparison) (Candidate, error) {
				t.Fatal("denied request reached generator")
				return Candidate{}, nil
			})
			_, err := f.service.Diff(t.Context(), tc.user, testkit.DBSQLite, f.in)
			assertCode(t, err, tc.code)
			assertReleased(t, f, 0)
			if f.inspector.calls != 0 {
				t.Fatal("denied request read schema")
			}
		})
	}
}

func TestDiffValidatesInputEngineAndReadiness(t *testing.T) {
	for _, tc := range []struct {
		name     string
		change   func(*serviceFixture)
		code     string
		acquired int64
	}{
		{"invalid desired id", func(f *serviceFixture) { f.in.DesiredDatabaseID = "invalid" }, "INVALID_REQUEST", 0},
		{"empty schema", func(f *serviceFixture) { f.in.Schema = " " }, "INVALID_REQUEST", 0},
		{"NUL schema", func(f *serviceFixture) { f.in.Schema = "main\x00" }, "INVALID_REQUEST", 0},
		{"invalid UTF-8", func(f *serviceFixture) { f.in.Schema = string([]byte{0xff}) }, "INVALID_REQUEST", 0},
		{"reserved schema", func(f *serviceFixture) { f.in.Schema = "_platform" }, "NOT_FOUND", 0},
		{"SQLite schema", func(f *serviceFixture) { f.in.Schema = "other" }, "DIFF_UNSUPPORTED", 0},
		{"mixed engine", func(f *serviceFixture) {
			f.provider = databasetest.NewProvider(database.TargetHandle{DatabaseID: testkit.DBSQLite, Driver: database.SQLite, MigrationReady: true}, database.TargetHandle{DatabaseID: testkit.DBSQLiteDesired, Driver: database.Postgres})
			f.service.provider = f.provider
		}, "DIFF_UNSUPPORTED", 0},
		{"unready source", func(f *serviceFixture) {
			f.provider = databasetest.NewProvider(database.TargetHandle{DatabaseID: testkit.DBSQLite, Driver: database.SQLite}, database.TargetHandle{DatabaseID: testkit.DBSQLiteDesired, Driver: database.SQLite})
			f.service.provider = f.provider
		}, "TARGET_NOT_READY", 1},
		{"missing source schema", func(f *serviceFixture) {
			s := f.inspector.snapshots[testkit.DBSQLite]
			s.Schemas = nil
			f.inspector.snapshots[testkit.DBSQLite] = s
		}, "NOT_FOUND", 2},
		{"missing desired schema", func(f *serviceFixture) {
			s := f.inspector.snapshots[testkit.DBSQLiteDesired]
			s.Schemas = nil
			f.inspector.snapshots[testkit.DBSQLiteDesired] = s
		}, "NOT_FOUND", 2},
		{"wrong snapshot driver", func(f *serviceFixture) {
			s := f.inspector.snapshots[testkit.DBSQLite]
			s.Driver = database.Postgres
			f.inspector.snapshots[testkit.DBSQLite] = s
		}, "INVALID_REQUEST", 2},
		{"wrong snapshot id", func(f *serviceFixture) {
			s := f.inspector.snapshots[testkit.DBSQLite]
			s.DatabaseID = testkit.DBPG
			f.inspector.snapshots[testkit.DBSQLite] = s
		}, "INVALID_REQUEST", 2},
		{"missing head adapter", func(f *serviceFixture) { f.service.head = nil }, "DIFF_UNAVAILABLE", 2},
		{"unsafe head", func(f *serviceFixture) {
			f.service.head = func(context.Context, contracts.Principal, string) (int64, error) { return maxVersion + 1, nil }
		}, "INVALID_REQUEST", 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := fixture()
			tc.change(&f)
			_, err := f.service.Diff(t.Context(), principal, testkit.DBSQLite, f.in)
			assertCode(t, err, tc.code)
			assertReleased(t, f, tc.acquired)
		})
	}
	f := fixture()
	_, err := f.service.Diff(t.Context(), principal, "invalid", f.in)
	assertCode(t, err, "INVALID_REQUEST")
	assertReleased(t, f, 0)
}

func TestDiffUsesFullFingerprintsAndTheInjectedHead(t *testing.T) {
	f := fixture()
	for _, id := range []string{testkit.DBSQLite, testkit.DBSQLiteDesired} {
		snap := f.inspector.snapshots[id]
		snap.Schemas[0].Name = "public"
		snap.Schemas = append(snap.Schemas, database.Schema{Name: "audit", Tables: []database.Table{{Name: "events", Columns: []database.Column{{Name: "id", DataType: "bigint", Ordinal: 1}}}}})
		snap.Driver = database.Postgres
		snap.Fingerprint = "spoofed fingerprint"
		f.inspector.snapshots[id] = snap
	}
	f.provider = databasetest.NewProvider(database.TargetHandle{DatabaseID: testkit.DBSQLite, Driver: database.Postgres, MigrationReady: true}, database.TargetHandle{DatabaseID: testkit.DBSQLiteDesired, Driver: database.Postgres})
	f.service.provider = f.provider
	f.in.Schema = "public"
	calls := 0
	f.service.head = func(_ context.Context, p contracts.Principal, id string) (int64, error) {
		calls++
		if p != principal || id != testkit.DBSQLite {
			t.Fatal("head reader did not receive principal and source")
		}
		return 7, nil
	}
	result, err := f.service.Diff(t.Context(), principal, testkit.DBSQLite, f.in)
	if err != nil || result.ExpectedVersion != 7 || result.Driver != database.Postgres || calls != 2 {
		t.Fatal("diff metadata", result, err)
	}
	full, err := database.Fingerprint(f.inspector.snapshots[testkit.DBSQLite])
	if err != nil || result.SourceFingerprint != full || result.DesiredFingerprint == result.SourceFingerprint {
		t.Fatal("diff did not use shared full-snapshot fingerprints", err)
	}
	selected := f.inspector.snapshots[testkit.DBSQLite]
	selected.Schemas = selected.Schemas[:1]
	partial, _ := database.Fingerprint(selected)
	if result.SourceFingerprint == partial {
		t.Fatal("fingerprint lost schemas outside the selected schema")
	}
	if !reflect.DeepEqual(result.Changes, []DiffChange{{AddColumn, "public.items.status", false}}) || result.Executable || result.CandidateSQL != "" || len(result.Warnings) == 0 {
		t.Fatal("unavailable generator was advertised as executable", result)
	}
	assertReleased(t, f, 2)
}

func TestIdenticalAndSelfDiffHaveAnEmptyExecutableResult(t *testing.T) {
	for _, self := range []bool{false, true} {
		f := fixture()
		snap := clone(f.inspector.snapshots[testkit.DBSQLite])
		snap.DatabaseID = testkit.DBSQLiteDesired
		f.inspector.snapshots[snap.DatabaseID] = snap
		if self {
			f.in.DesiredDatabaseID = testkit.DBSQLite
		}
		f.service.generators[database.SQLite] = generatorFunc(func(context.Context, Comparison) (Candidate, error) {
			t.Fatal("empty diff called generator")
			return Candidate{}, nil
		})
		result, err := f.service.Diff(t.Context(), principal, testkit.DBSQLite, f.in)
		if err != nil || !result.Executable || result.CandidateSQL != "" || len(result.Changes) != 0 || len(result.Warnings) != 0 || result.SourceFingerprint != result.DesiredFingerprint {
			t.Fatal("identical diff", result, err)
		}
		encoded, _ := json.Marshal(result)
		if !strings.Contains(string(encoded), `"changes":[]`) || !strings.Contains(string(encoded), `"warnings":[]`) {
			t.Fatal("empty collections are not arrays", string(encoded))
		}
		if self {
			assertReleased(t, f, 1)
		} else {
			assertReleased(t, f, 2)
		}
	}
}

func TestDiffGeneratorBoundaryAndHeadConflict(t *testing.T) {
	for _, tc := range []struct {
		name      string
		candidate Candidate
		headMoves bool
		code      string
	}{
		{"editable SQL", Candidate{SQL: "ALTER TABLE items ADD COLUMN status TEXT;\n", Executable: true}, false, ""},
		{"discard partial SQL", Candidate{SQL: "ALTER TABLE items ADD COLUMN partial TEXT;", Warnings: []string{"Rebuild tidak didukung.", "Rebuild tidak didukung."}}, false, ""},
		{"discard invalid partial SQL", Candidate{SQL: "\x00" + strings.Repeat("x", 256<<10), Warnings: []string{"Perubahan tidak didukung."}}, false, ""},
		{"missing unsupported reason", Candidate{}, false, ""},
		{"no SQL for changes", Candidate{Executable: true}, false, "DIFF_UNAVAILABLE"},
		{"NUL candidate", Candidate{SQL: "SELECT 1;\x00", Executable: true}, false, "DIFF_UNAVAILABLE"},
		{"oversized candidate", Candidate{SQL: strings.Repeat("x", (256<<10)+1), Executable: true}, false, "DIFF_UNAVAILABLE"},
		{"head changed", Candidate{SQL: "ALTER TABLE items ADD COLUMN status TEXT;", Executable: true}, true, "VERSION_CONFLICT"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := fixture()
			f.service.generators[database.SQLite] = generatorFunc(func(_ context.Context, c Comparison) (Candidate, error) {
				if c.Source.DatabaseID != testkit.DBSQLite || c.Desired.DatabaseID != f.in.DesiredDatabaseID || c.Schema != "main" || len(c.Changes) != 1 {
					t.Fatal("generator received the wrong comparison")
				}
				c.Changes[0].Object = "must not change result"
				if tc.headMoves {
					f.service.head = func(context.Context, contracts.Principal, string) (int64, error) { return 8, nil }
				}
				return tc.candidate, nil
			})
			result, err := f.service.Diff(t.Context(), principal, testkit.DBSQLite, f.in)
			if tc.code != "" {
				assertCode(t, err, tc.code)
			} else {
				if err != nil || result.Executable != tc.candidate.Executable || result.Changes[0].Object != "main.items.status" {
					t.Fatal(result, err)
				}
				if tc.candidate.Executable && result.CandidateSQL != tc.candidate.SQL {
					t.Fatal("SQL was rewritten")
				}
				if !tc.candidate.Executable && (result.CandidateSQL != "" || len(result.Warnings) == 0) {
					t.Fatal("partial candidate was exposed")
				}
			}
			assertReleased(t, f, 2)
		})
	}
}

func TestDiffPreservesGeneratorDomainErrors(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		code string
	}{
		{"not found", database.ErrNotFound, "NOT_FOUND"},
		{"timeout", database.ErrTargetTimeout, "TARGET_TIMEOUT"},
		{"permission", database.ErrTargetPermission, "FORBIDDEN"},
		{"unavailable", database.ErrTargetUnavailable, "TARGET_UNAVAILABLE"},
		{"unsupported", database.ErrDiffUnsupported, "DIFF_UNSUPPORTED"},
		{"domain", problem("DIFF_UNAVAILABLE", "Atlas belum tersedia."), "DIFF_UNAVAILABLE"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := fixture()
			f.service.generators[database.SQLite] = generatorFunc(func(context.Context, Comparison) (Candidate, error) { return Candidate{}, tc.err })
			_, err := f.service.Diff(t.Context(), principal, testkit.DBSQLite, f.in)
			assertCode(t, err, tc.code)
			assertReleased(t, f, 2)
		})
	}
}

func TestDiffFailuresAreSanitizedAndReleaseLeases(t *testing.T) {
	secret := "postgres://user:private-password@secret-host/db"
	for _, stage := range []string{"acquire source", "acquire desired", "inspect source", "inspect desired", "head", "generator"} {
		t.Run(stage, func(t *testing.T) {
			f := fixture()
			failure := errors.New(secret)
			code, acquired := "INTERNAL_ERROR", int64(2)
			switch stage {
			case "acquire source":
				f.provider.Fail(testkit.DBSQLite, failure)
				acquired = 0
			case "acquire desired":
				f.provider.Fail(testkit.DBSQLiteDesired, failure)
				acquired = 1
			case "inspect source":
				f.inspector.errors[testkit.DBSQLite] = failure
			case "inspect desired":
				f.inspector.errors[testkit.DBSQLiteDesired] = failure
			case "head":
				f.service.head = func(context.Context, contracts.Principal, string) (int64, error) { return 0, failure }
			case "generator":
				f.service.generators[database.SQLite] = generatorFunc(func(context.Context, Comparison) (Candidate, error) { return Candidate{}, failure })
				code = "DIFF_UNAVAILABLE"
			}
			_, err := f.service.Diff(t.Context(), principal, testkit.DBSQLite, f.in)
			assertCode(t, err, code)
			if strings.Contains(err.Error(), secret) {
				t.Fatal("private driver error reached the caller")
			}
			assertReleased(t, f, acquired)
		})
	}
}

func TestDiffDeadlineAndCancellation(t *testing.T) {
	f := fixture()
	f.service.timeout = 10 * time.Millisecond
	f.service.generators[database.SQLite] = generatorFunc(func(ctx context.Context, _ Comparison) (Candidate, error) {
		<-ctx.Done()
		return Candidate{}, ctx.Err()
	})
	_, err := f.service.Diff(t.Context(), principal, testkit.DBSQLite, f.in)
	assertCode(t, err, "TARGET_TIMEOUT")
	assertReleased(t, f, 2)
	f = fixture()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = f.service.Diff(ctx, principal, testkit.DBSQLite, f.in)
	assertCode(t, err, "TARGET_UNAVAILABLE")
	assertReleased(t, f, 0)
}
