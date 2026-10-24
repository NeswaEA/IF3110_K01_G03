package migrations

import (
	"context"
	"testing"

	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/contracts"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/database"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/database/databasetest"
	sqliteadapter "github.com/NeswaEA/IF3110_K01_G03/backend/internal/database/sqlite"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/schemadiff"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/testkit"
)

func TestSQLiteDiffCandidateThroughMigrationRunner(t *testing.T) {
	for _, tc := range []struct {
		name, source, desired, status string
		version                       int64
	}{
		{"commit", `CREATE TABLE parent(id INTEGER PRIMARY KEY); INSERT INTO parent VALUES(1);`, `CREATE TABLE parent(id INTEGER PRIMARY KEY,label TEXT DEFAULT 'new'); CREATE TABLE child(id INTEGER PRIMARY KEY,parent_id INTEGER REFERENCES parent(id) ON DELETE CASCADE,value INTEGER CHECK(value>0)); CREATE INDEX child_parent ON child(parent_id);`, Succeeded, 1},
		{"rollback", `CREATE TABLE items(id INTEGER); INSERT INTO items VALUES(1),(1);`, `CREATE TABLE items(id INTEGER,label TEXT); CREATE UNIQUE INDEX unique_items ON items(id);`, Failed, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source, desired := sqliteTarget(t), sqliteTarget(t)
			desired.DatabaseID = testkit.DBSQLiteDesired
			if _, err := source.DB.Exec(tc.source); err != nil {
				t.Fatal(err)
			}
			if _, err := desired.DB.Exec(tc.desired); err != nil {
				t.Fatal(err)
			}
			runner, _ := migrationService(t, source)
			provider := databasetest.NewProvider(source, desired)
			diff := schemadiff.NewService(provider, sqliteadapter.Inspector{}, testkit.NewFixtureAccessChecker(), func(ctx context.Context, p contracts.Principal, id string) (int64, error) {
				head, err := runner.Head(ctx, p, id)
				return head.Version, err
			}, schemadiff.Options{Generators: map[database.Driver]schemadiff.Generator{database.SQLite: schemadiff.SQLiteGenerator{Provider: provider}}})
			result, err := diff.Diff(t.Context(), editor, source.DatabaseID, schemadiff.DiffInput{DesiredDatabaseID: desired.DatabaseID, Schema: "main"})
			if err != nil || !result.Executable || result.CandidateSQL == "" {
				t.Fatal("candidate unavailable", result, err)
			}
			draft, err := runner.CreateDraft(t.Context(), editor, source.DatabaseID, DraftInput{Title: "generated candidate", SQL: result.CandidateSQL, ExpectedVersion: result.ExpectedVersion, SourceFingerprint: &result.SourceFingerprint})
			if err != nil {
				t.Fatal(err)
			}
			run, err := runner.Execute(t.Context(), editor, input(draft))
			if tc.status == Succeeded && err != nil {
				t.Fatal(err)
			}
			if tc.status == Failed {
				assertCode(t, err, "SQL_EXECUTION_FAILED")
			}
			if run.Status != tc.status || run.SQL != result.CandidateSQL {
				t.Fatal("unexpected run", run.Status, err)
			}
			head, err := runner.Head(t.Context(), editor, source.DatabaseID)
			if err != nil || head.Version != tc.version {
				t.Fatal("wrong committed head", head, err)
			}
			actual, err := (sqliteadapter.Inspector{}).Inspect(t.Context(), source)
			if err != nil {
				t.Fatal(err)
			}
			want := result.DesiredFingerprint
			if tc.status == Failed {
				want = result.SourceFingerprint
			}
			if actual.Fingerprint != want {
				t.Fatal("runner did not commit or roll back the entire candidate")
			}
			var ledger int64
			if err := source.DB.QueryRow(`SELECT count(*) FROM _platform_migration_ledger`).Scan(&ledger); err != nil || ledger != tc.version {
				t.Fatal("wrong ledger version", ledger, err)
			}
		})
	}
}
