package main

import (
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/auth"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/connections"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/migrations"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/platform"
	metadata "github.com/NeswaEA/IF3110_K01_G03/backend/migrations"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

func appDB(t *testing.T) (*sql.DB, string) {
	t.Helper()
	dsn := os.Getenv("TEST_METADATA_DATABASE_URL")
	if dsn == "" {
		if os.Getenv("CI") != "" {
			t.Fatal("TEST_METADATA_DATABASE_URL is required in CI")
		}
		t.Skip("TEST_METADATA_DATABASE_URL is not set")
	}
	admin, err := platform.OpenMetadataDB(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { admin.Close() })
	schema := "test_app_" + strings.ToLower(rand.Text())
	if _, err := admin.Exec(`CREATE SCHEMA "` + schema + `"`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := admin.Exec(`DROP SCHEMA "` + schema + `" CASCADE`); err != nil {
			t.Error("schema cleanup failed")
		}
	})
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal("invalid test DSN")
	}
	cfg.RuntimeParams["search_path"] = schema
	db := stdlib.OpenDB(*cfg)
	t.Cleanup(func() { db.Close() })
	if err := platform.MigrateMetadata(t.Context(), db, metadata.Files); err != nil {
		t.Fatal(err)
	}
	return db, dsn
}

func TestAppAuthenticationViewerExplainAndDisable(t *testing.T) {
	db, dsn := appDB(t)
	dir := t.TempDir()
	target, err := sql.Open("sqlite3", filepath.Join(dir, "demo.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	if _, err := target.Exec(`CREATE TABLE items(id INTEGER PRIMARY KEY, name TEXT); INSERT INTO items VALUES(1,'sample')`); err != nil {
		t.Fatal(err)
	}
	key := base64.StdEncoding.EncodeToString(make([]byte, 32))
	cipher, err := connections.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	const databaseID = "30000000-0000-4000-8000-000000000001"
	encrypted, err := cipher.Seal([]byte(`{"file":"demo.sqlite"}`), []byte(databaseID))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO database_connections(id,name,driver,config_encrypted) VALUES($1,'demo','sqlite',$2)`, databaseID, encrypted); err != nil {
		t.Fatal(err)
	}
	hash, err := (auth.PasswordHasher{}).Hash(t.Context(), "example password 123")
	if err != nil {
		t.Fatal(err)
	}
	for i, name := range []string{"admin", "viewer", "editor"} {
		id := "10000000-0000-4000-8000-00000000000" + string(rune('1'+i))
		if _, err := db.Exec(`INSERT INTO users(id,username,password_hash,is_platform_admin) VALUES($1,$2,$3,$4)`, id, name, hash, i == 0); err != nil {
			t.Fatal(err)
		}
		if i > 0 {
			group := "20000000-0000-4000-8000-00000000000" + string(rune('1'+i))
			if _, err := db.Exec(`INSERT INTO groups(id,name) VALUES($1,$2)`, group, name); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`INSERT INTO group_memberships(group_id,user_id) VALUES($1,$2)`, group, id); err != nil {
				t.Fatal(err)
			}
			level := "view"
			if i == 2 {
				level = "edit"
			}
			if _, err := db.Exec(`INSERT INTO group_database_access(group_id,database_id,level) VALUES($1,$2,$3)`, group, databaseID, level); err != nil {
				t.Fatal(err)
			}
		}
	}
	cfg := platform.Config{AppEnv: "test", AppOrigin: "http://localhost:8080", MetadataDatabaseURL: dsn, SessionIdleTimeout: 30 * time.Minute, SessionAbsoluteTimeout: 8 * time.Hour, SessionRotationInterval: 15 * time.Minute, SessionGracePeriod: 30 * time.Second}
	h, close, err := newApp(t.Context(), db, cfg, runtimeConfig{EncryptionKey: key, SQLiteDir: dir, WebDir: testWeb(t), ConnectTimeout: time.Second, QueryTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer close()
	h = platform.RequestID(h)
	request := func(method, path, body string, cookie *http.Cookie, csrf string, extra ...map[string]string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Origin", cfg.AppOrigin)
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Accept", "text/html")
		if cookie != nil {
			r.AddCookie(cookie)
		}
		if csrf != "" {
			r.Header.Set("X-CSRF-Token", csrf)
		}
		for _, headers := range extra {
			for name, value := range headers {
				r.Header.Set(name, value)
			}
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	type loginState struct {
		cookie *http.Cookie
		csrf   string
	}
	login := func(name string) loginState {
		w := request("GET", "/api/v1/auth/csrf", "", nil, "")
		var payload struct {
			Data struct {
				Token string `json:"csrf_token"`
			} `json:"data"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil || w.Code != 200 {
			t.Fatal("prelogin failed")
		}
		w = request("POST", "/api/v1/auth/login", `{"username":"`+name+`","password":"example password 123"}`, w.Result().Cookies()[0], payload.Data.Token)
		if w.Code != 200 {
			t.Fatal("login failed", w.Code)
		}
		cookie := w.Result().Cookies()[0]
		w = request("GET", "/api/v1/auth/csrf", "", cookie, "")
		if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil || w.Code != 200 {
			t.Fatal("authenticated CSRF failed")
		}
		return loginState{cookie, payload.Data.Token}
	}
	if w := request("GET", "/api/v1/databases", "", nil, ""); w.Code != 401 {
		t.Fatal("unprotected API")
	}
	admin := login("admin")
	view := login("viewer")
	edit := login("editor")
	base := "/api/v1/databases/" + databaseID
	for _, tc := range []struct {
		method, path, body string
		who                loginState
		status             int
		contains           string
	}{
		{"GET", "/healthz", "", loginState{}, 200, "ok"},
		{"GET", "/readyz", "", loginState{}, 200, "ready"},
		{"GET", "/api/v1/databases", "", view, 200, `"effective_access":"view"`},
		{"GET", base + "/schema", "", view, 200, "items"},
		{"GET", base + "/data?schema=main&table=items", "", view, 200, "sample"},
		{"GET", base + "/schema", "", admin, 404, "NOT_FOUND"},
		{"POST", base + "/explain", `{"sql":"SELECT * FROM items","mode":"estimate"}`, view, 403, "FORBIDDEN"},
		{"POST", base + "/explain", `{"sql":"SELECT * FROM items","mode":"estimate"}`, edit, 200, "nodes"},
		{"POST", base + "/explain", `{}`, loginState{edit.cookie, ""}, 403, "CSRF_REJECTED"},
		{"GET", "/api/v1/admin/connections", "", view, 403, "FORBIDDEN"},
		{"GET", "/api/v1/admin/connections", "", admin, 200, "demo"},
		{"POST", "/api/v1/admin/connections/" + databaseID + "/test", `{}`, admin, 200, `"reachable":true`},
		{"GET", "/api/v1/missing", "", edit, 404, "NOT_FOUND"},
		{"GET", base + "/migration-runs", "", edit, 200, `"data":[]`},
		{"GET", "/databases", "", loginState{}, 302, ""},
		{"GET", "/", "", view, 302, ""},
		{"GET", "/databases", "", view, 200, `data-widget="database-list"`},
		{"GET", "/databases/" + databaseID, "", view, 200, `data-database-id="` + databaseID + `"`},
		{"GET", "/databases/" + databaseID, "", admin, 404, "Halaman tidak ditemukan"},
		{"GET", "/databases/" + databaseID + "/migrations", "", view, 200, `data-widget="migrations"`},
		{"GET", "/admin/connections", "", view, 403, "Akses ditolak"},
		{"GET", "/admin/connections", "", admin, 200, `data-widget="connections"`},
		{"GET", "/unknown", "", loginState{}, 404, "Halaman tidak ditemukan"},
		{"GET", "/assets/missing.js", "", view, 404, "Halaman tidak ditemukan"},
	} {
		w := request(tc.method, tc.path, tc.body, tc.who.cookie, tc.who.csrf)
		if w.Code != tc.status || !strings.Contains(w.Body.String(), tc.contains) {
			t.Fatalf("%s %s: expected %d %s, got %d %s", tc.method, tc.path, tc.status, tc.contains, w.Code, w.Body.String())
		}
	}
	if w := request("GET", "/databases/"+databaseID+"?table=items", "", nil, ""); w.Header().Get("Location") != "/login?next=%2Fdatabases%2F"+databaseID+"%3Ftable%3Ditems" {
		t.Fatal("anonymous page was not sent to login", w.Header().Get("Location"))
	}
	w := request("GET", "/api/v1/databases", "", edit.cookie, "")
	if !strings.Contains(w.Body.String(), `"transactional_migrations":true`) || !strings.Contains(w.Body.String(), `"schema_diff":false`) {
		t.Fatal("unavailable capabilities advertised")
	}
	draftBody := `{"title":"HTTP migration","sql":"CREATE TABLE migrated(id INTEGER PRIMARY KEY); INSERT INTO migrated VALUES(1);","expected_version":0}`
	for _, denied := range []struct {
		who    loginState
		status int
	}{{view, 403}, {loginState{}, 401}, {loginState{edit.cookie, ""}, 403}} {
		if w := request("POST", base+"/migration-drafts", draftBody, denied.who.cookie, denied.who.csrf); w.Code != denied.status {
			t.Fatalf("migration access: %d %s", w.Code, w.Body.String())
		}
	}
	w = request("POST", base+"/migration-drafts", draftBody, edit.cookie, edit.csrf)
	var draft struct {
		Data migrations.MigrationDraft `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &draft); err != nil || w.Code != 201 {
		t.Fatal("create draft", w.Code, w.Body.String())
	}
	executePath := base + "/migration-drafts/" + draft.Data.ID + "/execute"
	idempotencyHeaders := map[string]string{"Idempotency-Key": "70000000-0000-4000-8000-000000000001"}
	for _, denied := range []struct {
		who    loginState
		status int
	}{{view, 403}, {loginState{}, 401}, {loginState{edit.cookie, ""}, 403}} {
		w := request("POST", executePath, `{"revision":1,"expected_version":0}`, denied.who.cookie, denied.who.csrf, idempotencyHeaders)
		if w.Code != denied.status {
			t.Fatalf("execute access: %d %s", w.Code, w.Body.String())
		}
	}
	w = request("POST", executePath, `{"revision":1,"expected_version":0}`, edit.cookie, edit.csrf, idempotencyHeaders)
	var run struct {
		Data migrations.MigrationRun `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &run); err != nil || w.Code != 200 || run.Data.Status != migrations.Succeeded {
		t.Fatal("execute", w.Code, w.Body.String())
	}
	firstRun := run.Data.ID
	if w := request("POST", base+"/migration-runs/"+firstRun+"/reconcile", `{}`, view.cookie, view.csrf); w.Code != 403 {
		t.Fatal("view reconciled run", w.Code)
	}
	w = request("POST", executePath, `{"revision":1,"expected_version":0}`, edit.cookie, edit.csrf, idempotencyHeaders)
	if err := json.Unmarshal(w.Body.Bytes(), &run); err != nil || w.Code != 200 || run.Data.ID != firstRun {
		t.Fatal("duplicate execute", w.Code, w.Body.String())
	}
	if w := request("GET", base+"/migration-runs/"+firstRun, "", view.cookie, ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"username":"editor"`) {
		t.Fatal("view history", w.Code, w.Body.String())
	}
	if w := request("GET", base+"/migration-drafts/"+draft.Data.ID, "", view.cookie, ""); w.Code != 403 {
		t.Fatal("view read draft", w.Code)
	}
	w = request("PATCH", "/api/v1/admin/connections/"+databaseID, `{"revision":1,"config":{"file":"different.sqlite"}}`, admin.cookie, admin.csrf)
	if w.Code != 409 || !strings.Contains(w.Body.String(), "REVISION_CONFLICT") {
		t.Fatal("history did not lock target", w.Code, w.Body.String())
	}
	w = request("POST", base+"/migration-drafts", `{"title":"HTTP rollback","sql":"INSERT INTO migrated VALUES(2); INSERT INTO missing_table VALUES(3);","expected_version":1}`, edit.cookie, edit.csrf)
	if err := json.Unmarshal(w.Body.Bytes(), &draft); err != nil || w.Code != 201 {
		t.Fatal("create rollback draft", w.Code, w.Body.String())
	}
	idempotencyHeaders["Idempotency-Key"] = "70000000-0000-4000-8000-000000000002"
	w = request("POST", base+"/migration-drafts/"+draft.Data.ID+"/execute", `{"revision":1,"expected_version":1}`, edit.cookie, edit.csrf, idempotencyHeaders)
	if w.Code != 422 || !strings.Contains(w.Body.String(), `"run_id"`) {
		t.Fatal("failed execution", w.Code, w.Body.String())
	}
	var count int
	if err := target.QueryRow("SELECT count(*) FROM migrated").Scan(&count); err != nil || count != 1 {
		t.Fatal("HTTP execution did not roll back")
	}
	w = request("GET", base+"/migration-head", "", view.cookie, "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"version":1`) {
		t.Fatal("failed run advanced head", w.Code, w.Body.String())
	}
	if w := request("DELETE", "/api/v1/admin/connections/"+databaseID, "", admin.cookie, admin.csrf); w.Code != 204 {
		t.Fatal("disable failed", w.Code)
	}
	for _, who := range []loginState{view, edit} {
		if w := request("GET", "/api/v1/auth/me", "", who.cookie, ""); w.Code != 401 {
			t.Fatal("disable failed to revoke sessions")
		}
	}
	if w := request("GET", "/api/v1/auth/me", "", admin.cookie, ""); w.Code != 200 {
		t.Fatal("unrelated admin revoked")
	}
}
