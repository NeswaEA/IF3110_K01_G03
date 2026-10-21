package connections

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/database"
)

func pgJSON(mut func(m map[string]any)) json.RawMessage {
	m := map[string]any{"host": "target-postgres", "port": 5432, "database": "demo", "username": "platform_runner",
		"password": "s3cr3t-pw", "sslmode": "disable", "editor_role": "platform_editor"}
	if mut != nil {
		mut(m)
	}
	b, _ := json.Marshal(m)
	return b
}

func TestParseConfigPostgres(t *testing.T) {
	local := Policy{AllowInsecureSSL: true}
	if _, err := ParseConfig(database.Postgres, pgJSON(nil), local); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
	cases := map[string]struct {
		mut    func(map[string]any)
		policy Policy
		field  string
	}{
		"port zero":         {func(m map[string]any) { m["port"] = 0 }, local, "config.port"},
		"port too big":      {func(m map[string]any) { m["port"] = 65536 }, local, "config.port"},
		"empty host":        {func(m map[string]any) { m["host"] = "" }, local, "config.host"},
		"host with slash":   {func(m map[string]any) { m["host"] = "a/b" }, local, "config.host"},
		"empty password":    {func(m map[string]any) { m["password"] = "" }, local, "config.password"},
		"bad sslmode":       {func(m map[string]any) { m["sslmode"] = "prefer" }, local, "config.sslmode"},
		"disable in prod":   {nil, Policy{}, "config.sslmode"},
		"superuser role":    {func(m map[string]any) { m["editor_role"] = "postgres" }, local, "config.editor_role"},
		"pg_ role":          {func(m map[string]any) { m["editor_role"] = "pg_write_all_data" }, local, "config.editor_role"},
		"injection in role": {func(m map[string]any) { m["editor_role"] = "x; drop" }, local, "config.editor_role"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := ParseConfig(database.Postgres, pgJSON(c.mut), c.policy)
			var ve *ValidationError
			if !errors.Is(err, ErrInvalid) || !errors.As(err, &ve) {
				t.Fatalf("want validation error, got %v", err)
			}
			if _, ok := ve.Fields[c.field]; !ok {
				t.Fatalf("field %s missing from %v", c.field, ve.Fields)
			}
		})
	}
	t.Run("unknown field", func(t *testing.T) {
		if _, err := ParseConfig(database.Postgres, pgJSON(func(m map[string]any) { m["hots"] = "typo" }), local); !errors.Is(err, ErrInvalid) {
			t.Fatalf("typo field accepted: %v", err)
		}
	})
	t.Run("verify-full allowed without insecure policy", func(t *testing.T) {
		if _, err := ParseConfig(database.Postgres, pgJSON(func(m map[string]any) { m["sslmode"] = "verify-full" }), Policy{}); err != nil {
			t.Fatal(err)
		}
	})
}

func TestParseConfigSQLiteNames(t *testing.T) {
	ok := []string{"demo.sqlite", "demo.db", "a-b_c.1.sqlite"}
	bad := []string{"", "demo", "demo.txt", "../demo.db", "/etc/passwd.db", "sub/demo.db", `..\demo.db`, ".hidden.db", "demo.db/", "a b.db"}
	for _, f := range ok {
		raw, _ := json.Marshal(map[string]string{"file": f})
		if _, err := ParseConfig(database.SQLite, raw, Policy{}); err != nil {
			t.Errorf("%q rejected: %v", f, err)
		}
	}
	for _, f := range bad {
		raw, _ := json.Marshal(map[string]string{"file": f})
		if _, err := ParseConfig(database.SQLite, raw, Policy{}); !errors.Is(err, ErrInvalid) {
			t.Errorf("%q accepted", f)
		}
	}
}

func TestPublicConfigHasNoSecrets(t *testing.T) {
	cfg, _ := ParseConfig(database.Postgres, pgJSON(nil), Policy{AllowInsecureSSL: true})
	b, _ := json.Marshal(cfg.Public())
	s := string(b)
	for _, banned := range []string{"s3cr3t-pw", "password\"", "postgres://", "dsn"} {
		if strings.Contains(s, banned) {
			t.Fatalf("public config leaks %q: %s", banned, s)
		}
	}
	if !strings.Contains(s, `"has_credentials":true`) {
		t.Fatalf("has_credentials missing: %s", s)
	}
}

func TestDSNEscapesSpecialCharacters(t *testing.T) {
	c := PostgresConfig{Host: "h", Port: 5432, Database: "d b", Username: "u@x", Password: `p/ss:w?rd#"\`, SSLMode: "require", EditorRole: "r"}
	dsn := c.DSN(5)
	for _, raw := range []string{`p/ss:w?rd#"\`, "u@x:"} {
		if strings.Contains(dsn, raw) {
			t.Fatalf("unescaped %q in %s", raw, dsn)
		}
	}
	id, err := IdentityFromDSN(dsn)
	if err != nil || id != "postgres:h:5432/d b" {
		t.Fatalf("identity %q err %v", id, err)
	}
}

func key32() string {
	return base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef"))
}

func TestCipher(t *testing.T) {
	c, err := NewCipher(key32())
	if err != nil {
		t.Fatal(err)
	}
	blob, _ := c.Seal([]byte("secret-config"), []byte("row-1"))
	if strings.Contains(string(blob), "secret-config") {
		t.Fatal("plaintext visible in blob")
	}
	if got, err := c.Open(blob, []byte("row-1")); err != nil || string(got) != "secret-config" {
		t.Fatalf("round trip: %q %v", got, err)
	}
	if _, err := c.Open(blob, []byte("row-2")); err == nil {
		t.Fatal("blob moved to another row must not open")
	}
	blob[len(blob)-1] ^= 1
	if _, err := c.Open(blob, []byte("row-1")); err == nil {
		t.Fatal("tampered blob must not open")
	}
	other, _ := c.Seal([]byte("secret-config"), []byte("row-1"))
	again, _ := c.Seal([]byte("secret-config"), []byte("row-1"))
	if string(other) == string(again) {
		t.Fatal("nonce reuse")
	}
	for _, bad := range []string{"", "not base64!", base64.StdEncoding.EncodeToString([]byte("short"))} {
		if _, err := NewCipher(bad); err == nil {
			t.Fatalf("key %q accepted", bad)
		}
	}
}

func TestResolveSQLitePath(t *testing.T) {
	dir := t.TempDir()
	outside := filepath.Join(t.TempDir(), "secret.db")
	if err := os.WriteFile(outside, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "real.db"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	if p, err := ResolveSQLitePath(dir, "real.db"); err != nil || filepath.Base(p) != "real.db" {
		t.Fatalf("existing file: %q %v", p, err)
	}
	if _, err := ResolveSQLitePath(dir, "missing.db"); err != nil {
		t.Fatalf("missing file must be allowed here: %v", err)
	}
	for _, f := range []string{"../real.db", "/etc/x.db", "a/b.db"} {
		if _, err := ResolveSQLitePath(dir, f); !errors.Is(err, ErrInvalid) {
			t.Errorf("%q not rejected", f)
		}
	}
	if _, err := ResolveSQLitePath("relative/dir", "real.db"); !errors.Is(err, ErrInvalid) {
		t.Error("relative directory accepted")
	}

	if err := os.Symlink(outside, filepath.Join(dir, "link.db")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := ResolveSQLitePath(dir, "link.db"); !errors.Is(err, ErrInvalid) {
		t.Error("symlink to outside file accepted")
	}
	if err := os.Symlink(filepath.Join(t.TempDir(), "nowhere"), filepath.Join(dir, "dangling.db")); err == nil {
		if _, err := ResolveSQLitePath(dir, "dangling.db"); !errors.Is(err, ErrInvalid) {
			t.Error("dangling symlink accepted")
		}
	}
	if err := os.Symlink(filepath.Join(dir, "real.db"), filepath.Join(dir, "alias.db")); err == nil {
		if p, err := ResolveSQLitePath(dir, "alias.db"); err != nil || filepath.Base(p) != "real.db" {
			t.Errorf("symlink inside dir should resolve to the real file: %q %v", p, err)
		}
	}
}

func TestRedactErr(t *testing.T) {
	cause := errors.New("exec failed: postgres://u:hunter2@h/db")
	got := redactErr(cause, "hunter2")
	if strings.Contains(got.Error(), "hunter2") {
		t.Fatalf("secret leaked: %v", got)
	}
	if !errors.Is(got, cause) {
		t.Fatal("redaction must keep the error chain")
	}
	if redactErr(nil, "x") != nil {
		t.Fatal("nil must stay nil")
	}
}

func TestErrorCodes(t *testing.T) {
	cases := map[error]string{
		database.ErrNotFound: "NOT_FOUND", ErrInvalid: "INVALID_REQUEST", ErrRevisionConflict: "REVISION_CONFLICT",
		ErrDuplicateTarget: "DUPLICATE_TARGET", database.ErrTargetTimeout: "TARGET_TIMEOUT",
		database.ErrTargetUnavailable: "TARGET_UNAVAILABLE", database.ErrLedgerIncompatible: "LEDGER_INCOMPATIBLE",
	}
	for err, want := range cases {
		if got := ErrorCode(err); got != want {
			t.Errorf("%v: %s != %s", err, got, want)
		}
	}
	if ErrorCode(errors.New("other")) != "" {
		t.Error("unknown error must map to empty code")
	}
}
