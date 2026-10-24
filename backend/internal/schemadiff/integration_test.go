package schemadiff

import (
	"context"
	"database/sql"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/contracts"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/database"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/database/databasetest"
	sqliteadapter "github.com/NeswaEA/IF3110_K01_G03/backend/internal/database/sqlite"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/testkit"
	_ "github.com/mattn/go-sqlite3"
)

func sqliteTarget(t *testing.T, id, ddl string, ready bool) database.TargetHandle {
	t.Helper()
	path := filepath.Join(t.TempDir(), "target.sqlite")
	db, err := sql.Open("sqlite3", "file:"+filepath.ToSlash(path)+"?_foreign_keys=1")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(ddl); err != nil {
		t.Fatal(err)
	}
	return database.TargetHandle{DatabaseID: id, Driver: database.SQLite, DB: db, MigrationReady: ready}
}

func schemaDDL(t *testing.T, db *sql.DB) []string {
	t.Helper()
	rows, err := db.Query(`SELECT COALESCE(sql,'') FROM sqlite_schema ORDER BY type,name`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var ddl string
		if err := rows.Scan(&ddl); err != nil {
			t.Fatal(err)
		}
		out = append(out, ddl)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestDiffReadsRealSQLiteWithoutChangingEitherTarget(t *testing.T) {
	for _, identical := range []bool{false, true} {
		name := "changed"
		if identical {
			name = "identical"
		}
		t.Run(name, func(t *testing.T) {
			ddl := `CREATE TABLE items(id INTEGER PRIMARY KEY, label TEXT); INSERT INTO items VALUES(1,'source'); CREATE TABLE _platform_probe(secret TEXT);`
			source := sqliteTarget(t, testkit.DBSQLite, ddl, true)
			desiredDDL := `CREATE TABLE items(id INTEGER PRIMARY KEY,label TEXT,status TEXT DEFAULT 'new'); CREATE INDEX items_status ON items(status); INSERT INTO items(id,label) VALUES(1,'desired');`
			if identical {
				desiredDDL = `CREATE TABLE items(id INTEGER PRIMARY KEY,label TEXT); INSERT INTO items VALUES(1,'different data');`
			}
			desired := sqliteTarget(t, testkit.DBSQLiteDesired, desiredDDL, false)
			beforeSource, beforeDesired := schemaDDL(t, source.DB), schemaDDL(t, desired.DB)
			provider := databasetest.NewProvider(source, desired)
			s := NewService(provider, sqliteadapter.Inspector{}, testkit.NewFixtureAccessChecker(), func(context.Context, contracts.Principal, string) (int64, error) { return 3, nil }, Options{})
			result, err := s.Diff(t.Context(), principal, source.DatabaseID, DiffInput{desired.DatabaseID, "main"})
			if err != nil || result.ExpectedVersion != 3 {
				t.Fatal(result, err)
			}
			if identical {
				if !result.Executable || len(result.Changes) != 0 || result.SourceFingerprint != result.DesiredFingerprint {
					t.Fatal("data or reserved objects affected the diff", result)
				}
			} else {
				want := []DiffChange{{AddIndex, "main.items.items_status", false}, {AddColumn, "main.items.status", false}}
				if !reflect.DeepEqual(result.Changes, want) || result.Executable || result.CandidateSQL != "" {
					t.Fatal(result)
				}
			}
			if !reflect.DeepEqual(beforeSource, schemaDDL(t, source.DB)) || !reflect.DeepEqual(beforeDesired, schemaDDL(t, desired.DB)) {
				t.Fatal("comparison mutated a target schema or ledger")
			}
			for _, target := range []database.TargetHandle{source, desired} {
				var count, foreignKeys int
				if err := target.DB.QueryRow(`SELECT count(*) FROM items`).Scan(&count); err != nil || count != 1 {
					t.Fatal("data changed", count, err)
				}
				if err := target.DB.QueryRow(`PRAGMA foreign_keys`).Scan(&foreignKeys); err != nil || foreignKeys != 1 || target.DB.Stats().InUse != 0 {
					t.Fatal("comparison leaked a connection or changed foreign_keys", err)
				}
			}
			if provider.Acquired() != 2 || provider.Released() != 2 {
				t.Fatal("target leases leaked")
			}
		})
	}
}
