//go:build atlas

package schemadiff_test

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/connections"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/contracts"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/database"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/database/postgres"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/migrations"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/platform"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/schemadiff"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/testkit"
	metadata "github.com/NeswaEA/IF3110_K01_G03/backend/migrations"
)

func atlasEnv(t *testing.T, name string) string {
	t.Helper()
	value := os.Getenv(name)
	if value == "" {
		t.Fatalf("%s is required for the separate Atlas suite", name)
	}
	return value
}

func atlasMetadata(t *testing.T) *sql.DB {
	t.Helper()
	dsn := atlasEnv(t, "TEST_METADATA_DATABASE_URL")
	admin, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal("invalid metadata DSN")
	}
	t.Cleanup(func() { admin.Close() })
	schema := "atlas_meta_" + strings.ToLower(rand.Text())
	if _, err := admin.Exec(`CREATE SCHEMA ` + postgres.QuoteIdent(schema)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := admin.Exec(`DROP SCHEMA ` + postgres.QuoteIdent(schema) + ` CASCADE`); err != nil {
			t.Error(err)
		}
	})
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal("invalid metadata URL")
	}
	query := u.Query()
	query.Set("search_path", schema)
	u.RawQuery = query.Encode()
	db, err := sql.Open("pgx", u.String())
	if err != nil {
		t.Fatal("cannot open metadata")
	}
	t.Cleanup(func() { db.Close() })
	if err := platform.MigrateMetadata(t.Context(), db, metadata.Files); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO users(id,username,password_hash) VALUES($1,'editor','$argon2id$test')`, testkit.UserEdit); err != nil {
		t.Fatal(err)
	}
	return db
}

type recordedAtlasConfig struct {
	database.DiffConfigProvider
	path string
}

func (c *recordedAtlasConfig) WithDiffConfig(ctx context.Context, source, desired string, fn func(string) error) error {
	return c.DiffConfigProvider.WithDiffConfig(ctx, source, desired, func(path string) error { c.path = path; return fn(path) })
}

func TestAtlasPostgresCandidateIntegration(t *testing.T) {
	if os.Getenv("ATLAS_OFFLINE_TEST") == "1" {
		for _, address := range []string{"1.1.1.1:443", "8.8.8.8:53"} {
			conn, err := net.DialTimeout("tcp", address, time.Second)
			if err == nil {
				conn.Close()
				t.Fatal("offline test network permits external traffic")
			}
		}
	}
	for _, identical := range []bool{false, true} {
		name := "changes"
		if identical {
			name = "identical"
		}
		t.Run(name, func(t *testing.T) {
			meta := atlasMetadata(t)
			cipher, err := connections.NewCipher(base64.StdEncoding.EncodeToString(make([]byte, 32)))
			if err != nil {
				t.Fatal(err)
			}
			registry := connections.NewRegistry(meta, cipher, connections.RegistryOptions{})
			t.Cleanup(registry.Close)
			u, err := url.Parse(atlasEnv(t, "TEST_TARGET_DATABASE_URL"))
			if err != nil {
				t.Fatal("invalid target URL")
			}
			port, _ := strconv.Atoi(u.Port())
			password, _ := u.User.Password()
			for i, id := range []string{testkit.DBPG, testkit.DBPGDesired} {
				dbName := strings.TrimPrefix(u.Path, "/")
				if i == 1 {
					dbName = "demo_desired"
				}
				plain, _ := json.Marshal(map[string]any{"host": u.Hostname(), "port": port, "database": dbName, "username": u.User.Username(), "password": password, "sslmode": "disable", "editor_role": "platform_editor"})
				sealed, err := cipher.Seal(plain, []byte(id))
				if err != nil {
					t.Fatal(err)
				}
				if _, err := meta.Exec(`INSERT INTO database_connections(id,name,driver,config_encrypted) VALUES($1,$2,'postgres',$3)`, id, dbName, sealed); err != nil {
					t.Fatal(err)
				}
			}
			source, release, err := registry.Acquire(t.Context(), testkit.DBPG)
			if err != nil {
				t.Fatal(err)
			}
			defer release()
			desired, release, err := registry.Acquire(t.Context(), testkit.DBPGDesired)
			if err != nil {
				t.Fatal(err)
			}
			defer release()
			schema := "atlas_diff_" + strings.ToLower(rand.Text())
			qualified := postgres.QuoteIdent(schema)
			for _, target := range []database.TargetHandle{source, desired} {
				if _, err := target.DB.Exec(`SET ROLE platform_editor;CREATE SCHEMA ` + qualified + `;RESET ROLE`); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					if _, err := target.DB.Exec(`SET ROLE platform_editor;DROP SCHEMA ` + qualified + ` CASCADE;RESET ROLE`); err != nil {
						t.Error(err)
					}
				})
			}
			old := `CREATE TABLE ` + qualified + `.parent(id integer PRIMARY KEY,label text);CREATE TABLE ` + qualified + `.obsolete(id integer);`
			next := `CREATE TABLE ` + qualified + `.parent(id bigint PRIMARY KEY,label text NOT NULL UNIQUE,status text DEFAULT 'new');CREATE INDEX parent_status ON ` + qualified + `.parent(status);CREATE TABLE ` + qualified + `.child(id integer PRIMARY KEY,parent_id bigint REFERENCES ` + qualified + `.parent(id),amount integer CHECK(amount>0));`
			if identical {
				next = old
			}
			for _, item := range []struct {
				target database.TargetHandle
				ddl    string
			}{{source, old}, {desired, next}} {
				if _, err := item.target.DB.Exec(`SET ROLE platform_editor;` + item.ddl + `RESET ROLE`); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := source.DB.Exec(`SET ROLE platform_editor;INSERT INTO ` + qualified + `.parent VALUES(1,'one');RESET ROLE`); err != nil {
				t.Fatal(err)
			}
			if err := (migrations.LedgerInitializer{}).EnsureReady(t.Context(), source); err != nil {
				t.Fatal(err)
			}
			if _, err := meta.Exec(`UPDATE database_connections SET migration_ready=true WHERE id=$1`, source.DatabaseID); err != nil {
				t.Fatal(err)
			}
			access := testkit.NewFixtureAccessChecker()
			runner := migrations.NewService(meta, registry, postgres.Inspector{}, access, platform.SystemClock{}, migrations.Options{})
			config := &recordedAtlasConfig{DiffConfigProvider: registry}
			g := schemadiff.AtlasGenerator{Config: config, Binary: atlasEnv(t, "ATLAS_BIN")}
			service := schemadiff.NewService(registry, postgres.Inspector{}, access, func(ctx context.Context, p contracts.Principal, id string) (int64, error) {
				head, err := runner.Head(ctx, p, id)
				return head.Version, err
			}, schemadiff.Options{Generators: map[database.Driver]schemadiff.Generator{database.Postgres: g}})
			beforeSource, err := (postgres.Inspector{}).Inspect(t.Context(), source)
			if err != nil {
				t.Fatal(err)
			}
			beforeDesired, err := (postgres.Inspector{}).Inspect(t.Context(), desired)
			if err != nil {
				t.Fatal(err)
			}
			principal := contracts.Principal{UserID: testkit.UserEdit, Username: "editor"}
			result, err := service.Diff(t.Context(), principal, source.DatabaseID, schemadiff.DiffInput{DesiredDatabaseID: desired.DatabaseID, Schema: schema})
			if err != nil || !result.Executable {
				t.Fatal(result, err)
			}
			if _, err := os.Stat(config.path); !os.IsNotExist(err) {
				t.Fatal("registry config was not cleaned")
			}
			for _, item := range []struct {
				h           database.TargetHandle
				fingerprint string
			}{{source, beforeSource.Fingerprint}, {desired, beforeDesired.Fingerprint}} {
				after, err := (postgres.Inspector{}).Inspect(t.Context(), item.h)
				if err != nil || after.Fingerprint != item.fingerprint {
					t.Fatal("Atlas changed a target", err)
				}
			}
			var rows int
			if err := source.DB.QueryRow(`SELECT count(*) FROM ` + qualified + `.parent`).Scan(&rows); err != nil || rows != 1 {
				t.Fatal("Atlas changed source data", err)
			}
			if identical {
				if len(result.Changes) != 0 || result.CandidateSQL != "" || len(result.Warnings) != 0 {
					t.Fatal(result)
				}
				return
			}
			if len(result.Changes) == 0 || result.CandidateSQL == "" || len(result.Warnings) == 0 {
				t.Fatal("missing changes or destructive warning", result)
			}
			draft, err := runner.CreateDraft(t.Context(), principal, source.DatabaseID, migrations.DraftInput{Title: "Atlas candidate", SQL: result.CandidateSQL, ExpectedVersion: result.ExpectedVersion, SourceFingerprint: &result.SourceFingerprint})
			if err != nil {
				t.Fatal(err)
			}
			run, err := runner.Execute(t.Context(), principal, migrations.ExecuteInput{DatabaseID: source.DatabaseID, DraftID: draft.ID, Revision: draft.Revision, ExpectedVersion: draft.ExpectedVersion, IdempotencyKey: "90000000-0000-4000-8000-000000000001"})
			if err != nil || run.Status != migrations.Succeeded {
				t.Fatal("generated DDL failed through runner", run.Status, err)
			}
			after, err := (postgres.Inspector{}).Inspect(t.Context(), source)
			if err != nil || after.Fingerprint != beforeDesired.Fingerprint {
				t.Fatal("committed candidate did not reach desired schema", err)
			}
		})
	}
}
