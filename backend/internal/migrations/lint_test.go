package migrations

import (
	"crypto/sha256"
	"slices"
	"testing"

	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/database"
)

func TestMigrationLint(t *testing.T) {
	type expectedFinding struct {
		rule, severity, category string
		index                    int
	}
	tests := []struct {
		name, query string
		driver      database.Driver
		want        []expectedFinding
	}{
		{"postgres parse", "SELECT FROM ;", database.Postgres, []expectedFinding{{"parse_error", "error", "safety", 0}}},
		{"postgres safety", "SELECT $1; SET ROLE platform_runner; SELECT * FROM _platform.migration_history; SELECT set_config('role','x',true)",
			database.Postgres, []expectedFinding{{"unbound_parameter", "error", "safety", 1},
				{"unsupported_statement", "error", "safety", 2}, {"unsupported_statement", "error", "safety", 3},
				{"unsupported_statement", "error", "safety", 4}}},
		{"postgres warnings", "DELETE FROM items; ALTER TABLE items ALTER COLUMN price TYPE bigint; " +
			"ALTER TABLE items ADD COLUMN code text NOT NULL; ALTER TABLE items RENAME COLUMN price TO cost; " +
			"DROP TABLE items; CREATE INDEX items_code ON items(code); ALTER TABLE items ADD CHECK (code IS NOT NULL) NOT VALID",
			database.Postgres, []expectedFinding{{"unbounded_dml", "warning", "data_loss", 1},
				{"breaking_type_change", "warning", "compatibility", 2}, {"exclusive_lock", "warning", "locking", 2},
				{"not_null_without_default", "warning", "compatibility", 3}, {"breaking_rename", "warning", "compatibility", 4},
				{"destructive_drop", "warning", "data_loss", 5}, {"index_blocks_writes", "info", "locking", 6}}},
		{"sqlite syntax and guard", "SELECT FROM; ATTACH ':memory:' AS another; SELECT * FROM [_platform_migration_history]; SELECT 1",
			database.SQLite, []expectedFinding{{"parse_error", "error", "safety", 1}, {"unsupported_statement", "error", "safety", 2},
				{"unsupported_statement", "error", "safety", 3}}},
		{"sqlite quote and schema-dependent DML", "UPDATE items SET value='a;?'; -- comment ; :param\n" +
			"SELECT ?1; SELECT item$state FROM items WHERE id=1",
			database.SQLite, []expectedFinding{{"unbounded_dml", "warning", "data_loss", 1}, {"unbound_parameter", "error", "safety", 2}}},
		{"sqlite warnings", "ALTER TABLE items RENAME COLUMN a TO b; CREATE INDEX idx ON items(b); " +
			"DROP TABLE items; ALTER TABLE items ADD COLUMN a text NOT NULL; DROP INDEX items_idx; REINDEX items_idx",
			database.SQLite, []expectedFinding{{"breaking_rename", "warning", "compatibility", 1}, {"index_blocks_writes", "info", "locking", 2},
				{"destructive_drop", "warning", "data_loss", 3}, {"not_null_without_default", "warning", "compatibility", 4}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			findings, err := lintRevision(t.Context(), tc.driver, revisionContent{UpSQL: tc.query, Mode: "normal"})
			if err != nil {
				t.Fatal(err)
			}
			got := make([]expectedFinding, 0, len(findings))
			for _, finding := range findings {
				if finding.Direction != "up" {
					t.Fatalf("unexpected direction %q", finding.Direction)
				}
				index := 0
				if finding.StatementIndex != nil {
					index = *finding.StatementIndex
				}
				got = append(got, expectedFinding{finding.Rule, finding.Severity, finding.Category, index})
			}
			if !slices.Equal(got, tc.want) {
				t.Fatalf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestBatchTemplateLintAndCanonicalHash(t *testing.T) {
	batch := &BatchConfig{Schema: "public", Table: "items", KeyColumn: "id", BatchSize: 10, PauseMS: 25}
	const valid = "UPDATE public.items SET price=price*2 WHERE id IN (SELECT id FROM public.items WHERE id>$1 AND id<=$2 ORDER BY id LIMIT $3) RETURNING id"
	for _, tc := range []struct {
		name, query string
		wantError   bool
	}{
		{"valid", valid, false},
		{"missing parameter", "DELETE FROM public.items WHERE id>$1 AND id<=$2 RETURNING id", true},
		{"wrong key", "DELETE FROM public.items WHERE id IN (SELECT id FROM public.items WHERE id>$1 AND id<=$2 LIMIT $3) RETURNING price", true},
		{"values insert", "INSERT INTO public.items(id,price) VALUES ($1 + $2,$3) RETURNING id", true},
		{"other target", "DELETE FROM public.other WHERE id IN (SELECT id FROM public.items WHERE id>$1 AND id<=$2 LIMIT $3) RETURNING id", true},
		{"extra statement", valid + "; SELECT 1", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			findings, err := lintRevision(t.Context(), database.Postgres, revisionContent{
				UpSQL: valid, DownSQL: &tc.query, Mode: "batch", Batch: batch,
			})
			if err != nil {
				t.Fatal(err)
			}
			if tc.wantError != slices.ContainsFunc(findings, func(f LintFinding) bool {
				return f.Direction == "down" && f.Rule == "batch_template" && f.Severity == "error"
			}) {
				t.Fatalf("unexpected batch findings: %+v", findings)
			}
		})
	}
	content := revisionContent{UpSQL: "SELECT 1 < 2", IrreversibleReason: new("contoh"), Mode: "batch", Batch: batch}
	got, err := content.hash()
	want := sha256.Sum256([]byte(`["SELECT 1 < 2",null,"contoh","batch",{"batch_size":10,"key_column":"id","pause_ms":25,"schema":"public","table":"items"}]`))
	if err != nil || got != want {
		t.Fatalf("canonical hash: got %x, want %x (%v)", got, want, err)
	}
}
