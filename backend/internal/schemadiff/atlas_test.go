package schemadiff

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/database"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/testkit"
)

type diffConfigFunc func(context.Context, string, string, func(string) error) error

func (f diffConfigFunc) WithDiffConfig(ctx context.Context, source, desired string, fn func(string) error) error {
	return f(ctx, source, desired, fn)
}

const atlasTestSQL = "ALTER TABLE \"public\".\"items\" ADD COLUMN \"status\" text;\n"

func atlasComparison() Comparison {
	return Comparison{Source: database.SchemaSnapshot{DatabaseID: testkit.DBPG, Driver: database.Postgres}, Desired: database.SchemaSnapshot{DatabaseID: testkit.DBPGDesired, Driver: database.Postgres}, Schema: "public", Changes: []DiffChange{{AddColumn, "public.items.status", false}}}
}

func atlasTestBinary(t *testing.T, mode string) string {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "atlas")
	quoted := "'" + strings.ReplaceAll(executable, "'", "'\"'\"'") + "'"
	script := "#!/bin/sh\nexport GORACE=atexit_sleep_ms=0\nexec " + quoted + " -test.run='^TestAtlasChild$' -- --atlas-helper=" + mode + " \"$@\"\n"
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestAtlasChild(t *testing.T) {
	mode := ""
	var args []string
	for i, arg := range os.Args {
		if strings.HasPrefix(arg, "--atlas-helper=") {
			mode = strings.TrimPrefix(arg, "--atlas-helper=")
			args = os.Args[i+1:]
			break
		}
	}
	if mode == "" {
		return
	}
	if len(args) == 1 && args[0] == "version" {
		if mode == "wrong-version" {
			fmt.Print("atlas community community version v1.4.0\n")
		} else if mode == "standard" {
			fmt.Print("atlas version v1.3.0\n")
		} else {
			fmt.Print("atlas community community version v1.3.0\n")
		}
		os.Exit(0)
	}
	if len(args) != 14 || args[0] != "schema" || args[1] != "diff" || args[2] != "--config" || !strings.HasPrefix(args[3], "file:///") || !reflect.DeepEqual(args[4:], []string{"--env", "source", "--from", "env://url", "--to", "env://schema.src", "--schema", "public", "--format", "{{ sql . }}"}) {
		fmt.Fprint(os.Stderr, "wrong arguments")
		os.Exit(2)
	}
	if os.Getenv("ATLAS_TOKEN") != "" || os.Getenv("DATABASE_URL") != "" || os.Getenv("ATLAS_NO_UPDATE_NOTIFIER") != "1" || os.Getenv("ATLAS_NO_UPGRADE_SUGGESTIONS") != "1" {
		os.Exit(3)
	}
	if mode == "environment" {
		path := strings.TrimPrefix(args[3], "file://")
		if err := os.WriteFile(filepath.Join(filepath.Dir(path), "child-home"), []byte(os.Getenv("HOME")), 0o600); err != nil {
			os.Exit(4)
		}
	}
	switch mode {
	case "fail":
		fmt.Fprint(os.Stderr, "postgres://user:private-secret@private-host/db "+args[3])
		os.Exit(2)
	case "stdout-limit":
		fmt.Print(strings.Repeat("x", (1<<20)+1))
		os.Exit(0)
	case "stderr-limit":
		fmt.Fprint(os.Stderr, strings.Repeat("x", (1<<20)+1))
		os.Exit(0)
	case "sql-limit":
		fmt.Print(strings.Repeat("x", (256<<10)+1))
		os.Exit(0)
	case "timeout":
		for {
			time.Sleep(time.Minute)
		}
	case "unsupported":
		fmt.Print(atlasTestSQL + "VACUUM;")
		os.Exit(0)
	case "empty":
		os.Exit(0)
	case "invalid-utf8":
		os.Stdout.Write([]byte{255})
		os.Exit(0)
	case "nul":
		fmt.Print(atlasTestSQL + "\x00")
		os.Exit(0)
	}
	fmt.Print(atlasTestSQL)
	os.Exit(0)
}

func TestAtlasProcessBoundaryAndCleanup(t *testing.T) {
	t.Setenv("ATLAS_TOKEN", "must-not-reach-child")
	t.Setenv("DATABASE_URL", "postgres://private-secret")
	for _, tc := range []struct {
		mode, code string
		executable bool
	}{
		{"success", "", true}, {"environment", "", true}, {"wrong-version", "DIFF_UNAVAILABLE", false}, {"standard", "DIFF_UNAVAILABLE", false},
		{"missing", "DIFF_UNAVAILABLE", false}, {"fail", "DIFF_UNAVAILABLE", false}, {"stdout-limit", "DIFF_UNAVAILABLE", false}, {"stderr-limit", "DIFF_UNAVAILABLE", false},
		{"sql-limit", "DIFF_UNAVAILABLE", false}, {"timeout", "DIFF_UNAVAILABLE", false}, {"invalid-utf8", "DIFF_UNAVAILABLE", false}, {"nul", "DIFF_UNAVAILABLE", false},
		{"empty", "DIFF_UNAVAILABLE", false}, {"unsupported", "", false},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			var seen, home string
			provider := diffConfigFunc(func(_ context.Context, source, desired string, fn func(string) error) error {
				if source != testkit.DBPG || desired != testkit.DBPGDesired {
					t.Fatal("wrong registration IDs")
				}
				dir, err := os.MkdirTemp(t.TempDir(), "config-")
				if err != nil {
					t.Fatal(err)
				}
				defer os.RemoveAll(dir)
				seen = filepath.Join(dir, "atlas.hcl")
				if err := os.WriteFile(seen, []byte("private-secret"), 0o600); err != nil {
					t.Fatal(err)
				}
				err = fn(seen)
				if data, readErr := os.ReadFile(filepath.Join(dir, "child-home")); readErr == nil {
					home = string(data)
				}
				return err
			})
			bin := atlasTestBinary(t, tc.mode)
			if tc.mode == "missing" {
				bin = filepath.Join(t.TempDir(), "absent")
			}
			g := AtlasGenerator{Config: provider, Binary: bin, Timeout: 2 * time.Second}
			if tc.mode == "timeout" {
				g.Timeout = 200 * time.Millisecond
			}
			candidate, err := g.Generate(t.Context(), atlasComparison())
			if tc.code != "" {
				assertCode(t, err, tc.code)
				if strings.Contains(err.Error(), "private-secret") || seen != "" && strings.Contains(err.Error(), seen) {
					t.Fatal("private process output leaked", err)
				}
			} else if err != nil || candidate.Executable != tc.executable {
				t.Fatal(candidate, err)
			}
			if candidate.Executable && candidate.SQL != atlasTestSQL {
				t.Fatal("candidate was rewritten", candidate)
			}
			if tc.mode == "unsupported" && (candidate.SQL != "" || len(candidate.Warnings) == 0) {
				t.Fatal("unsupported output exposed a partial candidate")
			}
			if seen != "" {
				if _, err := os.Stat(seen); !os.IsNotExist(err) {
					t.Fatal("config was not cleaned")
				}
			}
			if home != "" {
				if _, err := os.Stat(home); !os.IsNotExist(err) {
					t.Fatal("private HOME was not cleaned")
				}
			}
		})
	}
}

func TestAtlasEmptyAndAdditionalSchemaChanges(t *testing.T) {
	for _, mode := range []string{"empty", "success"} {
		provider := diffConfigFunc(func(_ context.Context, _, _ string, fn func(string) error) error {
			path := filepath.Join(t.TempDir(), "atlas.hcl")
			if err := os.WriteFile(path, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			return fn(path)
		})
		c := atlasComparison()
		c.Changes = nil
		candidate, err := (AtlasGenerator{Config: provider, Binary: atlasTestBinary(t, mode)}).Generate(t.Context(), c)
		if err != nil || !candidate.Executable {
			t.Fatal(candidate, err)
		}
		if mode == "empty" && (candidate.SQL != "" || len(candidate.Changes) != 0) {
			t.Fatal(candidate)
		}
		if mode == "success" && (len(candidate.Changes) != 1 || candidate.Changes[0].Kind != Other || len(candidate.Warnings) == 0) {
			t.Fatal("extra schema change was hidden", candidate)
		}
	}
}

func TestAtlasRejectsNonPrivateConfig(t *testing.T) {
	provider := diffConfigFunc(func(_ context.Context, _, _ string, fn func(string) error) error {
		path := filepath.Join(t.TempDir(), "atlas.hcl")
		if err := os.WriteFile(path, nil, 0o644); err != nil {
			t.Fatal(err)
		}
		return fn(path)
	})
	_, err := (AtlasGenerator{Config: provider, Binary: atlasTestBinary(t, "success")}).Generate(t.Context(), atlasComparison())
	assertCode(t, err, "DIFF_UNAVAILABLE")
}

func TestAtlasTransactionalDDL(t *testing.T) {
	for _, sql := range []string{
		`CREATE TABLE public.items(id bigint PRIMARY KEY,label text UNIQUE,parent bigint REFERENCES public.parents(id),CHECK(id>0));`,
		`ALTER TABLE public.items ADD COLUMN status text DEFAULT 'new'; ALTER TABLE public.items ALTER COLUMN status SET NOT NULL;`,
		`ALTER TABLE public.items ALTER COLUMN id TYPE numeric; ALTER TABLE public.items DROP COLUMN label;`,
		`ALTER TABLE public.items ADD CONSTRAINT positive CHECK(id>0); ALTER TABLE public.items DROP CONSTRAINT positive;`,
		`CREATE UNIQUE INDEX items_idx ON public.items(id); DROP INDEX public.items_idx; DROP TABLE public.items;`,
		`CREATE TABLE public.items(id serial PRIMARY KEY,created timestamptz DEFAULT now());`,
		`ALTER TABLE public.items RENAME COLUMN id TO other;`,
	} {
		if !atlasTransactionalDDL(sql, "public") {
			t.Fatal("transactional DDL rejected", sql)
		}
	}
	for _, sql := range []string{
		`BEGIN;CREATE TABLE items(id int);COMMIT;`, `SELECT 1;`, `INSERT INTO items VALUES(1);`, `VACUUM;`, `CREATE DATABASE other;`,
		`SET ROLE editor;`, `ALTER TABLE public.items OWNER TO other;`, `CREATE INDEX CONCURRENTLY idx ON public.items(id);`, `DROP INDEX CONCURRENTLY public.idx;`,
		`CREATE TABLE _platform.secrets(id int);`, `DROP TABLE _platform.migration_ledger;`, `CREATE TABLE public._platform_reserved(id int);`,
		`CREATE TABLE other.items(id int);`, `CREATE TABLE public.items(id int DEFAULT set_config('role','admin',false));`,
		`CREATE TABLE public.items(id int DEFAULT pg_catalog.pg_read_file('/secret')::int);`, `CREATE TEMP TABLE public.items(id int);`,
		`CREATE TABLE public.items(id int) PARTITION BY RANGE(id);`, `CREATE TABLE public.items() INHERITS(public.parent);`,
		`CREATE EXTENSION hstore;`, `CREATE VIEW public.items AS SELECT 1;`, `ALTER TABLE public.items SET SCHEMA other;`, `DROP SCHEMA public;`,
		`CREATE TABLE public.items(id int DEFAULT $1);`, `not SQL`,
	} {
		if atlasTransactionalDDL(sql, "public") {
			t.Fatal("unsupported SQL accepted", sql)
		}
	}
}
