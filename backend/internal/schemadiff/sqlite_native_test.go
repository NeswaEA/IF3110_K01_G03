package schemadiff

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/contracts"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/database"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/database/databasetest"
	sqliteadapter "github.com/NeswaEA/IF3110_K01_G03/backend/internal/database/sqlite"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/testkit"
)

func nativeDiff(t *testing.T, sourceDDL, desiredDDL string) (DiffResult, database.TargetHandle, database.TargetHandle) {
	t.Helper()
	source := sqliteTarget(t, testkit.DBSQLite, sourceDDL, true)
	desired := sqliteTarget(t, testkit.DBSQLiteDesired, desiredDDL, false)
	before, after := schemaDDL(t, source.DB), schemaDDL(t, desired.DB)
	provider := databasetest.NewProvider(source, desired)
	s := NewService(provider, sqliteadapter.Inspector{}, testkit.NewFixtureAccessChecker(), func(context.Context, contracts.Principal, string) (int64, error) { return 0, nil }, Options{Generators: map[database.Driver]Generator{database.SQLite: SQLiteGenerator{provider}}})
	result, err := s.Diff(t.Context(), principal, source.DatabaseID, DiffInput{desired.DatabaseID, "main"})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, schemaDDL(t, source.DB)) || !reflect.DeepEqual(after, schemaDDL(t, desired.DB)) {
		t.Fatal("generation modified a target")
	}
	if provider.Acquired() != 4 || provider.Released() != 4 {
		t.Fatal("generator leaked target leases", provider.Acquired(), provider.Released())
	}
	for _, target := range []database.TargetHandle{source, desired} {
		var foreignKeys int
		if err := target.DB.QueryRow(`PRAGMA foreign_keys`).Scan(&foreignKeys); err != nil || foreignKeys != 1 || target.DB.Stats().InUse != 0 {
			t.Fatal("connection settings changed or connection leaked", err)
		}
	}
	return result, source, desired
}

func applyNative(t *testing.T, target database.TargetHandle, candidate string) error {
	t.Helper()
	tx, err := target.DB.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(t.Context(), candidate); err != nil {
		return err
	}
	return tx.Commit()
}

func TestSQLiteNativeCreatesTablesConstraintsAndIndexes(t *testing.T) {
	desiredDDL := `CREATE TABLE parent(a INTEGER,b TEXT,PRIMARY KEY(a,b));
CREATE TABLE child(id INTEGER PRIMARY KEY, a INTEGER,b TEXT,label TEXT NOT NULL DEFAULT 'it''s; safe',code BLOB DEFAULT X'01ff',amount DECIMAL(10,2) DEFAULT (1+2),created TEXT DEFAULT CURRENT_TIMESTAMP,
 UNIQUE(a,b),CONSTRAINT positive CHECK (a > 0 AND instr(label, ';') >= 0),
 FOREIGN KEY(a,b) REFERENCES parent(a,b) ON UPDATE CASCADE ON DELETE SET NULL);
CREATE UNIQUE INDEX child_label ON child(label);`
	result, source, desired := nativeDiff(t, "", desiredDDL)
	if !result.Executable || result.CandidateSQL == "" {
		t.Fatal(result)
	}
	if err := applyNative(t, source, result.CandidateSQL); err != nil {
		t.Fatal(err, result.CandidateSQL)
	}
	actual, err := (sqliteadapter.Inspector{}).Inspect(t.Context(), source)
	want, desiredErr := (sqliteadapter.Inspector{}).Inspect(t.Context(), desired)
	if err != nil || desiredErr != nil || actual.Fingerprint != want.Fingerprint {
		t.Fatal("generated tables did not match desired", err, desiredErr)
	}
	if _, err := source.DB.Exec(`INSERT INTO parent VALUES(1,'x'); INSERT INTO child(id,a,b) VALUES(1,1,'x')`); err != nil {
		t.Fatal(err)
	}
	if _, err := source.DB.Exec(`INSERT INTO child(id,a,b,label) VALUES(2,99,'missing','other')`); err == nil {
		t.Fatal("foreign key was lost")
	}
	if _, err := source.DB.Exec(`INSERT INTO child(id,a,b,label) VALUES(3,-1,'x','negative')`); err == nil {
		t.Fatal("CHECK was lost")
	}
}

func TestSQLiteNativeOrdersPhasesAndAppendedColumns(t *testing.T) {
	old := `CREATE TABLE items(id INTEGER PRIMARY KEY,label TEXT); INSERT INTO items VALUES(1,'old'); CREATE INDEX old_index ON items(label); CREATE TABLE obsolete(id INTEGER);`
	next := `CREATE TABLE items(id INTEGER PRIMARY KEY,label TEXT,status TEXT NOT NULL DEFAULT 'new',note TEXT,score REAL DEFAULT -1.5e2); CREATE INDEX new_index ON items(status); CREATE TABLE extra(id INTEGER PRIMARY KEY);`
	result, source, desired := nativeDiff(t, old, next)
	if !result.Executable {
		t.Fatal(result)
	}
	previous := -1
	for _, fragment := range []string{`DROP INDEX "old_index"`, `CREATE TABLE "extra"`, `ADD COLUMN "status"`, `ADD COLUMN "note"`, `ADD COLUMN "score"`, `CREATE INDEX new_index`, `DROP TABLE "obsolete"`} {
		index := strings.Index(result.CandidateSQL, fragment)
		if index <= previous {
			t.Fatal("incorrect phase or column order", fragment, result.CandidateSQL)
		}
		previous = index
	}
	if err := applyNative(t, source, result.CandidateSQL); err != nil {
		t.Fatal(err)
	}
	actual, _ := (sqliteadapter.Inspector{}).Inspect(t.Context(), source)
	want, _ := (sqliteadapter.Inspector{}).Inspect(t.Context(), desired)
	if actual.Fingerprint != want.Fingerprint {
		t.Fatal("applied schema differs")
	}
	var label, status string
	var note *string
	if err := source.DB.QueryRow(`SELECT label,status,note FROM items WHERE id=1`).Scan(&label, &status, &note); err != nil || label != "old" || status != "new" || note != nil {
		t.Fatal("existing data was lost", err)
	}
}

func TestSQLiteNativeQuotesNamesAndPreservesCheckLiterals(t *testing.T) {
	result, source, _ := nativeDiff(t, "", `CREATE TABLE "a.b; DROP TABLE secret;"("quote""name" INTEGER PRIMARY KEY,"word" TEXT DEFAULT 'CHECK (;) --' CHECK ("word" != '); DROP TABLE secret; --')); CREATE INDEX "index""name" ON "a.b; DROP TABLE secret;"("word");`)
	if !result.Executable {
		t.Fatal(result)
	}
	if err := applyNative(t, source, result.CandidateSQL); err != nil {
		t.Fatal(err)
	}
}

func TestSQLiteNativeRejectsUnsupportedChangesWithoutPartialCandidates(t *testing.T) {
	for _, tc := range []struct{ name, old, next string }{
		{"drop column", `CREATE TABLE items(id INTEGER,label TEXT)`, `CREATE TABLE items(id INTEGER)`},
		{"type", `CREATE TABLE items(id INTEGER)`, `CREATE TABLE items(id TEXT)`},
		{"default", `CREATE TABLE items(id INTEGER DEFAULT 1)`, `CREATE TABLE items(id INTEGER DEFAULT 2)`},
		{"nullable", `CREATE TABLE items(id INTEGER)`, `CREATE TABLE items(id INTEGER NOT NULL)`},
		{"rename", `CREATE TABLE items(id INTEGER,label TEXT)`, `CREATE TABLE items(id INTEGER,title TEXT)`},
		{"not null no default", `CREATE TABLE items(id INTEGER)`, `CREATE TABLE items(id INTEGER,label TEXT NOT NULL)`},
		{"timestamp default", `CREATE TABLE items(id INTEGER)`, `CREATE TABLE items(id INTEGER,label TEXT DEFAULT CURRENT_TIMESTAMP)`},
		{"expression default", `CREATE TABLE items(id INTEGER)`, `CREATE TABLE items(id INTEGER,label INTEGER DEFAULT (1+2))`},
		{"new unique column", `CREATE TABLE items(id INTEGER)`, `CREATE TABLE items(id INTEGER,label TEXT UNIQUE)`},
		{"new references column", `CREATE TABLE parent(id INTEGER PRIMARY KEY); CREATE TABLE items(id INTEGER)`, `CREATE TABLE parent(id INTEGER PRIMARY KEY); CREATE TABLE items(id INTEGER,parent_id INTEGER REFERENCES parent(id))`},
		{"change check", `CREATE TABLE items(id INTEGER CHECK(id>0))`, `CREATE TABLE items(id INTEGER CHECK(id>1))`},
		{"incoming fk", `CREATE TABLE parent(id INTEGER PRIMARY KEY);CREATE TABLE items(id INTEGER REFERENCES parent(id))`, `CREATE TABLE items(id INTEGER)`},
		{"partial index", `CREATE TABLE items(id INTEGER)`, `CREATE TABLE items(id INTEGER);CREATE INDEX idx ON items(id) WHERE id>0`},
		{"expression index", `CREATE TABLE items(id INTEGER)`, `CREATE TABLE items(id INTEGER);CREATE INDEX idx ON items(abs(id))`},
		{"alter index", `CREATE TABLE items(a INTEGER,b INTEGER);CREATE INDEX idx ON items(a)`, `CREATE TABLE items(a INTEGER,b INTEGER);CREATE INDEX idx ON items(b)`},
		{"generated", `CREATE TABLE items(id INTEGER)`, `CREATE TABLE items(id INTEGER,label INTEGER AS (id+1))`},
		{"virtual table", `CREATE TABLE items(id INTEGER)`, `CREATE TABLE items(id INTEGER); CREATE VIRTUAL TABLE positions USING rtree(id,min_x,max_x,min_y,max_y)`},
		{"generated expression invisible to snapshot", `CREATE TABLE items(id INTEGER,label INTEGER AS (id+1))`, `CREATE TABLE items(id INTEGER,label INTEGER AS (id+2))`},
		{"view invisible to snapshot", `CREATE TABLE items(id INTEGER)`, `CREATE TABLE items(id INTEGER); CREATE VIEW report AS SELECT id FROM items`},
		{"trigger invisible to snapshot", `CREATE TABLE items(id INTEGER)`, `CREATE TABLE items(id INTEGER); CREATE TRIGGER prevent BEFORE INSERT ON items BEGIN SELECT RAISE(ABORT,'no'); END`},
		{"without rowid", `CREATE TABLE items(id INTEGER PRIMARY KEY)`, `CREATE TABLE items(id INTEGER PRIMARY KEY) WITHOUT ROWID`},
		{"strict", `CREATE TABLE items(id INTEGER)`, `CREATE TABLE items(id INTEGER) STRICT`},
		{"collation", `CREATE TABLE items(id INTEGER,label TEXT)`, `CREATE TABLE items(id INTEGER,label TEXT COLLATE NOCASE)`},
		{"descending primary key", `CREATE TABLE items(id INTEGER PRIMARY KEY)`, `CREATE TABLE items(id INTEGER PRIMARY KEY DESC)`},
		{"autoincrement", `CREATE TABLE items(id INTEGER PRIMARY KEY)`, `CREATE TABLE items(id INTEGER PRIMARY KEY AUTOINCREMENT)`},
		{"unchanged view dependency", `CREATE TABLE items(id INTEGER); CREATE VIEW report AS SELECT id FROM items`, `CREATE TABLE items(id INTEGER,label TEXT); CREATE VIEW report AS SELECT id FROM items`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result, _, _ := nativeDiff(t, tc.old, tc.next+`;CREATE TABLE supported_extra(id INTEGER);`)
			if result.Executable || result.CandidateSQL != "" || len(result.Warnings) == 0 || len(result.Changes) == 0 {
				t.Fatal("unsupported change exposed partial SQL", result)
			}
		})
	}
}

func TestSQLiteNativeFindsChangesOutsideTheSharedSnapshot(t *testing.T) {
	for _, tc := range []struct{ old, next string }{
		{`CREATE TABLE items(id INTEGER,label INTEGER AS (id+1))`, `CREATE TABLE items(id INTEGER,label INTEGER AS (id+2))`},
		{`CREATE TABLE items(id INTEGER)`, `CREATE TABLE items(id INTEGER); CREATE VIEW report AS SELECT id FROM items`},
		{`CREATE TABLE items(id INTEGER)`, `CREATE TABLE items(id INTEGER) STRICT`},
	} {
		result, _, _ := nativeDiff(t, tc.old, tc.next)
		if result.Executable || result.CandidateSQL != "" || len(result.Changes) != 1 || result.Changes[0].Kind != Other {
			t.Fatal("invisible difference was reported as identical", result)
		}
	}
}

func TestSQLiteNativeIdenticalIncludingUnsupportedObjects(t *testing.T) {
	for _, ddl := range []string{"", `CREATE TABLE items(id INTEGER)`, `CREATE TABLE items(id INTEGER,label INTEGER AS (id+1)); CREATE VIEW report AS SELECT id FROM items;`} {
		result, _, _ := nativeDiff(t, ddl, ddl)
		if !result.Executable || result.CandidateSQL != "" || len(result.Warnings) != 0 || len(result.Changes) != 0 {
			t.Fatal(result)
		}
	}
}

func TestSQLiteNativeUniqueFailureRollsBackEarlierDDL(t *testing.T) {
	result, source, _ := nativeDiff(t, `CREATE TABLE items(id INTEGER); INSERT INTO items VALUES(1),(1)`, `CREATE TABLE items(id INTEGER,label TEXT);CREATE UNIQUE INDEX idx ON items(id)`)
	if !result.Executable {
		t.Fatal(result)
	}
	before := schemaDDL(t, source.DB)
	if err := applyNative(t, source, result.CandidateSQL); err == nil {
		t.Fatal("duplicate data accepted unique index")
	}
	if !reflect.DeepEqual(before, schemaDDL(t, source.DB)) {
		t.Fatal("failure did not roll back ADD COLUMN")
	}
	var count int
	if err := source.DB.QueryRow(`SELECT count(*) FROM items`).Scan(&count); err != nil || count != 2 {
		t.Fatal("rollback lost data", err)
	}
}

func TestSQLiteNativeDetectsStaleSnapshotsAndReleasesLeases(t *testing.T) {
	h := sqliteTarget(t, testkit.DBSQLite, `CREATE TABLE items(id INTEGER)`, true)
	snap, err := (sqliteadapter.Inspector{}).Inspect(t.Context(), h)
	if err != nil {
		t.Fatal(err)
	}
	provider := databasetest.NewProvider(h)
	g := SQLiteGenerator{provider}
	if _, err := h.DB.Exec(`ALTER TABLE items ADD COLUMN changed TEXT`); err != nil {
		t.Fatal(err)
	}
	_, err = g.Generate(t.Context(), Comparison{Source: snap, Desired: snap, Schema: "main"})
	assertCode(t, err, "SCHEMA_CONFLICT")
	if provider.Acquired() != provider.Released() || h.DB.Stats().InUse != 0 {
		t.Fatal("stale snapshot leaked resources")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = g.Generate(ctx, Comparison{Source: snap, Desired: snap, Schema: "main"})
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestSQLiteNativeDemoFixtures(t *testing.T) {
	dir := filepath.Join("..", "..", "..", "tests", "fixtures", "sqlite")
	base, err := os.ReadFile(filepath.Join(dir, "demo.sql"))
	if err != nil {
		t.Fatal(err)
	}
	changes, err := os.ReadFile(filepath.Join(dir, "desired.sql"))
	if err != nil {
		t.Fatal(err)
	}
	result, source, desired := nativeDiff(t, string(base), string(base)+"\n"+string(changes))
	if !result.Executable || result.CandidateSQL == "" {
		t.Fatal("demo diff unavailable", result)
	}
	if err := applyNative(t, source, result.CandidateSQL); err != nil {
		t.Fatal(err)
	}
	actual, err := (sqliteadapter.Inspector{}).Inspect(t.Context(), source)
	if err != nil {
		t.Fatal(err)
	}
	want, err := (sqliteadapter.Inspector{}).Inspect(t.Context(), desired)
	if err != nil {
		t.Fatal(err)
	}
	if actual.Fingerprint != want.Fingerprint {
		t.Fatal("demo candidate did not reach desired")
	}
	var count int
	if err := source.DB.QueryRow(`SELECT count(*) FROM items`).Scan(&count); err != nil || count != 3 {
		t.Fatal("demo candidate lost data", err)
	}
}
