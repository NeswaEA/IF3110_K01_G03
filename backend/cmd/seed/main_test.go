package main

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/access"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/auth"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/connections"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/contracts"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/platform"
	metadata "github.com/NeswaEA/IF3110_K01_G03/backend/migrations"
)

func seedDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("TEST_METADATA_DATABASE_URL")
	if dsn == "" {
		if os.Getenv("CI") != "" {
			t.Fatal("TEST_METADATA_DATABASE_URL required in CI")
		}
		t.Skip("TEST_METADATA_DATABASE_URL is not set")
	}
	admin, err := platform.OpenMetadataDB(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { admin.Close() })
	schema := "test_seed_" + strings.ToLower(rand.Text())
	if _, err := admin.Exec(`CREATE SCHEMA "` + schema + `"`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := admin.Exec(`DROP SCHEMA "` + schema + `" CASCADE`); err != nil {
			t.Error("schema cleanup failed")
		}
	})
	u, err := url.Parse(dsn)
	if err != nil || u.Host == "" {
		t.Fatal("test DSN must be a PostgreSQL URL")
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	t.Setenv("METADATA_DATABASE_URL", u.String())
	t.Setenv("APP_ENV", "test")
	t.Setenv("BOOTSTRAP_ADMIN_USERNAME", " Initial.Admin ")
	t.Setenv("BOOTSTRAP_ADMIN_PASSWORD", "initial password 123")
	t.Setenv("FIXTURE_PASSWORD", "fixture password 123")
	t.Setenv("CONNECTION_ENCRYPTION_KEY", base64.StdEncoding.EncodeToString(make([]byte, 32)))
	db, err := platform.OpenMetadataDB(t.Context(), u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := platform.MigrateMetadata(t.Context(), db, metadata.Files); err != nil {
		t.Fatal(err)
	}
	entries := []fixtureConnection{}
	for i, suffix := range []string{"1", "2", "3", "4"} {
		driver := "postgres"
		config := json.RawMessage(`{"host":"localhost","port":5432,"database":"source","username":"runner","password":"target secret","sslmode":"disable","editor_role":"editor"}`)
		if i >= 2 {
			config = json.RawMessage(`{"host":"localhost","port":5432,"database":"desired","username":"runner","password":"target secret","sslmode":"disable","editor_role":"editor"}`)
		}
		if i%2 == 1 {
			driver = "sqlite"
			config = json.RawMessage(`{"file":"source.sqlite"}`)
			if i >= 2 {
				config = json.RawMessage(`{"file":"desired.sqlite"}`)
			}
		}
		var entry fixtureConnection
		entry.ID = databasePrefix + suffix
		entry.Name = "target-" + suffix
		entry.Config = config
		if err := json.Unmarshal([]byte(`"`+driver+`"`), &entry.Driver); err != nil {
			t.Fatal(err)
		}
		entries = append(entries, entry)
	}
	body, err := json.Marshal(entries)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "connections.json")
	if err := os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FIXTURE_CONNECTIONS_FILE", path)
	return db
}

func TestSeedArgumentsAndEnvironment(t *testing.T) {
	for _, args := range [][]string{nil, {"unknown"}, {"admin", "password"}, {"fixtures", "--password-stdin"}, {"admin", "--password-stdin", "extra"}} {
		if run(t.Context(), args, strings.NewReader("")) == nil {
			t.Fatal("invalid arguments accepted")
		}
	}
	for _, env := range []string{"", "production", "typo"} {
		t.Setenv("APP_ENV", env)
		if run(t.Context(), []string{"fixtures"}, nil) == nil {
			t.Fatal("unsafe fixture environment accepted")
		}
	}
}

func TestBootstrapConcurrentAndNoop(t *testing.T) {
	db := seedDB(t)
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Go(func() {
			if err := run(t.Context(), []string{"admin"}, nil); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	var count int
	var hash string
	if err := db.QueryRow(`SELECT count(*) FROM users`).Scan(&count); err != nil || count != 1 {
		t.Fatal("concurrent bootstrap created multiple users")
	}
	if err := db.QueryRow(`SELECT password_hash FROM users WHERE username='initial.admin' AND is_platform_admin AND active`).Scan(&hash); err != nil {
		t.Fatal(err)
	}
	match, err := (auth.PasswordHasher{}).Verify(t.Context(), "initial password 123", hash)
	if err != nil || !match {
		t.Fatal("bootstrap password was not hashed correctly")
	}
	t.Setenv("BOOTSTRAP_ADMIN_USERNAME", "")
	t.Setenv("BOOTSTRAP_ADMIN_PASSWORD", "")
	if err := run(t.Context(), []string{"admin", "--password-stdin"}, nil); err != nil {
		t.Fatal("existing admin should not require credentials", err)
	}
	var unchanged string
	if err := db.QueryRow(`SELECT password_hash FROM users`).Scan(&unchanged); err != nil || unchanged != hash {
		t.Fatal("rerun changed password")
	}
}

func TestBootstrapStdinAndCollision(t *testing.T) {
	db := seedDB(t)
	password := "  spaced password 123  "
	if err := run(t.Context(), []string{"admin", "--password-stdin"}, strings.NewReader(password+"\r\n")); err != nil {
		t.Fatal(err)
	}
	var hash string
	if err := db.QueryRow(`SELECT password_hash FROM users`).Scan(&hash); err != nil {
		t.Fatal(err)
	}
	match, err := (auth.PasswordHasher{}).Verify(t.Context(), password, hash)
	if err != nil || !match {
		t.Fatal("stdin altered password spaces")
	}
	if _, err := db.Exec(`UPDATE users SET is_platform_admin=false`); err != nil {
		t.Fatal(err)
	}
	if err := run(t.Context(), []string{"admin"}, nil); err == nil || strings.Contains(err.Error(), "initial password") {
		t.Fatal("username collision was not rejected safely")
	}
	var admin bool
	if err := db.QueryRow(`SELECT is_platform_admin FROM users`).Scan(&admin); err != nil || admin {
		t.Fatal("existing user was promoted")
	}
}

func TestFixturesAccessEncryptionAndReseed(t *testing.T) {
	db := seedDB(t)
	for i := 0; i < 2; i++ {
		if err := run(t.Context(), []string{"fixtures"}, nil); err != nil {
			t.Fatal(err)
		}
		policy := access.NewPolicy(db)
		for userIndex, wants := range [][]contracts.AccessLevel{
			{contracts.AccessNone, contracts.AccessNone, contracts.AccessNone, contracts.AccessNone},
			{contracts.AccessView, contracts.AccessView, contracts.AccessView, contracts.AccessView},
			{contracts.AccessEdit, contracts.AccessEdit, contracts.AccessView, contracts.AccessView},
			{contracts.AccessNone, contracts.AccessNone, contracts.AccessNone, contracts.AccessNone},
		} {
			for databaseIndex, want := range wants {
				got, err := policy.Effective(t.Context(), userPrefix+string(rune('1'+userIndex)), databasePrefix+string(rune('1'+databaseIndex)))
				if err != nil || got != want {
					t.Fatal("incorrect fixture access", got, err)
				}
			}
		}
	}
	var users, groups, grants int
	if err := db.QueryRow(`SELECT (SELECT count(*) FROM users),(SELECT count(*) FROM groups),(SELECT count(*) FROM group_database_access)`).Scan(&users, &groups, &grants); err != nil || users != 4 || groups != 2 || grants != 8 {
		t.Fatal("fixture counts changed on rerun")
	}
	var encrypted []byte
	var ready bool
	if err := db.QueryRow(`SELECT config_encrypted,migration_ready FROM database_connections WHERE id=$1`, databasePrefix+"1").Scan(&encrypted, &ready); err != nil || ready || strings.Contains(string(encrypted), "target secret") {
		t.Fatal("incorrect connection seed")
	}
	cipher, err := connections.NewCipher(os.Getenv("CONNECTION_ENCRYPTION_KEY"))
	if err != nil {
		t.Fatal(err)
	}
	plain, err := cipher.Open(encrypted, []byte(databasePrefix+"1"))
	if err != nil || !strings.Contains(string(plain), "target secret") {
		t.Fatal("connection encryption cannot be decoded")
	}
	store, err := auth.NewSessionStore(db, platform.SystemClock{}, auth.DefaultSessionOptions())
	if err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	session, err := store.CreateInTx(t.Context(), tx, userPrefix+"3")
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := run(t.Context(), []string{"fixtures"}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Resolve(t.Context(), session.Token); err == nil {
		t.Fatal("reseed left old session active")
	}
}

func TestFixturesRollbackAndTargetProtection(t *testing.T) {
	db := seedDB(t)
	if _, err := db.Exec(`INSERT INTO users(id,username,password_hash) VALUES(gen_random_uuid(),'viewer','$argon2id$test')`); err != nil {
		t.Fatal(err)
	}
	if err := run(t.Context(), []string{"fixtures"}, nil); err == nil {
		t.Fatal("fixture collision accepted")
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM database_connections`).Scan(&count); err != nil || count != 0 {
		t.Fatal("failed seed left partial connections")
	}
	if _, err := db.Exec(`DELETE FROM users`); err != nil {
		t.Fatal(err)
	}
	if err := run(t.Context(), []string{"fixtures"}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE database_connections SET name='changed' WHERE id=$1`, databasePrefix+"1"); err != nil {
		t.Fatal(err)
	}
	if err := run(t.Context(), []string{"fixtures"}, nil); err == nil {
		t.Fatal("existing connection silently replaced")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := run(ctx, []string{"fixtures"}, nil); err == nil {
		t.Fatal("canceled seed succeeded")
	}
}
