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
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/schemadiff"
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
	const desiredID = "30000000-0000-4000-8000-000000000003"
	encrypted, err := cipher.Seal([]byte(`{"file":"demo.sqlite"}`), []byte(databaseID))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO database_connections(id,name,driver,config_encrypted) VALUES($1,'demo','sqlite',$2)`, databaseID, encrypted); err != nil {
		t.Fatal(err)
	}
	desired, err := sql.Open("sqlite3", filepath.Join(dir, "desired.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer desired.Close()
	if _, err := desired.Exec(`CREATE TABLE items(id INTEGER PRIMARY KEY,name TEXT,status TEXT NOT NULL DEFAULT 'active')`); err != nil {
		t.Fatal(err)
	}
	encrypted, err = cipher.Seal([]byte(`{"file":"desired.sqlite"}`), []byte(desiredID))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO database_connections(id,name,driver,config_encrypted) VALUES($1,'desired','sqlite',$2)`, desiredID, encrypted); err != nil {
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
			if _, err := db.Exec(`INSERT INTO group_database_access(group_id,database_id,level) VALUES($1,$2,'view')`, group, desiredID); err != nil {
				t.Fatal(err)
			}
		}
	}
	cfg := platform.Config{AppEnv: "test", AppOrigin: "http://localhost:8080", MetadataDatabaseURL: dsn, SessionIdleTimeout: 30 * time.Minute, SessionAbsoluteTimeout: 8 * time.Hour, SessionRotationInterval: 15 * time.Minute, SessionGracePeriod: 30 * time.Second}
	h, close, err := newApp(t.Context(), db, cfg, runtimeConfig{EncryptionKey: key, SQLiteDir: dir, WebDir: testWeb(t), AtlasBin: filepath.Join(dir, "absent-atlas"), ConnectTimeout: time.Second, QueryTimeout: time.Second})
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
	if w := request("GET", "/login?next=%2Fdatabases", "", nil, ""); w.Code != 200 ||
		!strings.Contains(w.Body.String(), `data-next="/databases"`) || len(w.Result().Cookies()) != 0 ||
		w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("public login page unavailable or created a session", w.Code)
	}
	var anonymousSessions int
	if err := db.QueryRow("SELECT count(*) FROM sessions").Scan(&anonymousSessions); err != nil || anonymousSessions != 0 {
		t.Fatal("page load created an anonymous session", err)
	}
	if w := request("POST", "/api/v1/auth/login", `{"username":"editor","password":"example password 123"}`, nil, ""); w.Code != 403 {
		t.Fatal("login accepted a request without CSRF", w.Code)
	}
	admin := login("admin")
	view := login("viewer")
	edit := login("editor")
	base := "/api/v1/databases/" + databaseID
	diffBody := `{"desired_database_id":"` + desiredID + `","schema":"main"}`
	for _, tc := range []struct {
		who    loginState
		status int
		code   string
	}{
		{loginState{}, 401, "UNAUTHENTICATED"}, {loginState{edit.cookie, ""}, 403, "CSRF_REJECTED"}, {view, 403, "FORBIDDEN"}, {admin, 404, "NOT_FOUND"}, {edit, 503, "TARGET_NOT_READY"},
	} {
		w := request("POST", base+"/schema-diff", diffBody, tc.who.cookie, tc.who.csrf)
		if w.Code != tc.status || !strings.Contains(w.Body.String(), tc.code) {
			t.Fatal("schema diff middleware/readiness", w.Code, w.Body.String())
		}
	}
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
		{"GET", "/api/v1/admin/users", "", loginState{}, 401, "UNAUTHENTICATED"},
		{"GET", "/api/v1/admin/users", "", view, 403, "FORBIDDEN"},
		{"GET", "/api/v1/admin/users", "", admin, 200, `"username":"admin"`},
		{"POST", "/api/v1/admin/users", `{"username":"api-user","password":"example password 123"}`, admin, 201, `"username":"api-user"`},
		{"POST", "/api/v1/admin/users", `{}`, loginState{admin.cookie, ""}, 403, "CSRF_REJECTED"},
		{"PATCH", "/api/v1/admin/users/10000000-0000-4000-8000-000000000099", `{"active":false}`, admin, 404, "NOT_FOUND"},
		{"PUT", "/api/v1/admin/users/10000000-0000-4000-8000-000000000099/password", `{"password":"example password 123"}`, admin, 404, "NOT_FOUND"},
		{"GET", "/api/v1/admin/groups", "", loginState{}, 401, "UNAUTHENTICATED"},
		{"GET", "/api/v1/admin/groups", "", view, 403, "FORBIDDEN"},
		{"GET", "/api/v1/admin/groups", "", admin, 200, `"name":"viewer"`},
		{"POST", "/api/v1/admin/groups", `{"name":"api-group"}`, admin, 201, `"name":"api-group"`},
		{"POST", "/api/v1/admin/connections/" + databaseID + "/test", `{}`, admin, 200, `"reachable":true`},
		{"GET", "/api/v1/missing", "", edit, 404, "NOT_FOUND"},
		{"GET", base + "/migration-runs", "", edit, 200, `"data":[]`},
		{"GET", "/databases", "", loginState{}, 302, ""},
		{"GET", "/", "", view, 302, ""},
		{"GET", "/databases", "", view, 200, `<a href="/databases/` + databaseID + `">demo</a> (sqlite, akses lihat)`},
		{"GET", "/databases/" + databaseID + "?schema=main&table=items", "", view, 200, "<td>sample</td>"},
		{"GET", "/databases/" + databaseID + "?schema=main&table=missing", "", view, 404, "Halaman tidak ditemukan"},
		{"GET", "/databases/" + databaseID, "", admin, 404, "Halaman tidak ditemukan"},
		{"GET", "/databases/" + databaseID + "/migrations", "", view, 200, `data-widget="migrations"`},
		{"GET", "/admin/connections", "", view, 403, "Akses ditolak"},
		{"GET", "/admin/connections", "", admin, 200, `data-widget="connections"`},
		{"GET", "/admin", "", loginState{}, 302, ""},
		{"GET", "/admin", "", view, 403, "Akses ditolak"},
		{"GET", "/admin", "", admin, 302, ""},
		{"GET", "/admin/users", "", loginState{}, 302, ""},
		{"GET", "/admin/users", "", view, 403, "Akses ditolak"},
		{"GET", "/admin/users", "", admin, 200, `<th scope="row">api-user</th>`},
		{"GET", "/admin/users?user=10000000-0000-4000-8000-000000000001", "", admin, 200, `data-operation="update"`},
		{"GET", "/admin/users?offset=bad", "", admin, 400, "Permintaan tidak valid"},
		{"GET", "/admin/users?user=10000000-0000-4000-8000-000000000099", "", admin, 404, "Halaman tidak ditemukan"},
		{"GET", "/admin/groups", "", loginState{}, 302, ""},
		{"GET", "/admin/groups", "", view, 403, "Akses ditolak"},
		{"GET", "/admin/groups", "", admin, 200, `<th scope="row">api-group</th>`},
		{"GET", "/admin/groups?group=20000000-0000-4000-8000-000000000002", "", admin, 200, `data-operation="delete"`},
		{"GET", "/admin/groups?offset=bad", "", admin, 400, "Permintaan tidak valid"},
		{"GET", "/admin/groups?group=20000000-0000-4000-8000-000000000099", "", admin, 404, "Halaman tidak ditemukan"},
		{"GET", "/admin/groups/20000000-0000-4000-8000-000000000002/members", "", loginState{}, 302, ""},
		{"GET", "/admin/groups/20000000-0000-4000-8000-000000000002/members", "", view, 403, "Akses ditolak"},
		{"GET", "/admin/groups/20000000-0000-4000-8000-000000000002/members", "", admin, 200, `<th scope="row">viewer</th>`},
		{"GET", "/admin/groups/20000000-0000-4000-8000-000000000002/members?member=10000000-0000-4000-8000-000000000002", "", admin, 200, `data-operation="remove"`},
		{"GET", "/admin/groups/20000000-0000-4000-8000-000000000002/members?users_offset=bad", "", admin, 400, "Permintaan tidak valid"},
		{"GET", "/admin/groups/20000000-0000-4000-8000-000000000099/members", "", admin, 404, "Halaman tidak ditemukan"},
		{"GET", "/admin/groups/20000000-0000-4000-8000-000000000002/grants", "", loginState{}, 302, ""},
		{"GET", "/admin/groups/20000000-0000-4000-8000-000000000002/grants", "", view, 403, "Akses ditolak"},
		{"GET", "/admin/groups/20000000-0000-4000-8000-000000000002/grants", "", admin, 200, `<caption>Grant pada halaman ini</caption>`},
		{"GET", "/admin/groups/20000000-0000-4000-8000-000000000002/grants?database=" + databaseID, "", admin, 200, `data-operation="update"`},
		{"GET", "/admin/groups/20000000-0000-4000-8000-000000000002/grants?databases_offset=bad", "", admin, 400, "Permintaan tidak valid"},
		{"GET", "/admin/groups/20000000-0000-4000-8000-000000000099/grants", "", admin, 404, "Halaman tidak ditemukan"},
		{"GET", "/unknown", "", loginState{}, 404, "Halaman tidak ditemukan"},
		{"GET", "/assets/missing.js", "", view, 404, "Halaman tidak ditemukan"},
	} {
		w := request(tc.method, tc.path, tc.body, tc.who.cookie, tc.who.csrf)
		if w.Code != tc.status || !strings.Contains(w.Body.String(), tc.contains) {
			t.Fatalf("%s %s: expected %d %s, got %d %s", tc.method, tc.path, tc.status, tc.contains, w.Code, w.Body.String())
		}
	}
	var groupID, memberID string
	if err := db.QueryRow(`SELECT id FROM groups WHERE name='api-group'`).Scan(&groupID); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT id FROM users WHERE username='api-user'`).Scan(&memberID); err != nil {
		t.Fatal(err)
	}
	groupPath := "/api/v1/admin/groups/" + groupID
	for _, tc := range []struct {
		method, path, body string
		status             int
		contains           string
	}{
		{"PATCH", groupPath, `{"name":"renamed group"}`, 200, `"name":"renamed group"`},
		{"GET", groupPath + "/members", "", 200, `"data":[]`},
		{"PUT", groupPath + "/members/" + memberID, `{}`, 204, ""},
		{"GET", groupPath + "/members", "", 200, `"username":"api-user"`},
		{"GET", groupPath + "/grants", "", 200, `"data":[]`},
		{"PUT", groupPath + "/grants/" + databaseID, `{"level":"view"}`, 200, `"level":"view"`},
		{"GET", groupPath + "/grants", "", 200, `"database_id":"` + databaseID + `"`},
		{"PUT", groupPath + "/grants/" + databaseID, `{"level":"edit"}`, 200, `"level":"edit"`},
		{"DELETE", groupPath + "/grants/" + databaseID, "", 204, ""},
		{"GET", groupPath + "/grants", "", 200, `"data":[]`},
		{"DELETE", groupPath + "/members/" + memberID, "", 204, ""},
		{"DELETE", groupPath, "", 204, ""},
	} {
		w := request(tc.method, tc.path, tc.body, admin.cookie, admin.csrf)
		if w.Code != tc.status || !strings.Contains(w.Body.String(), tc.contains) {
			t.Fatalf("%s %s: expected %d, got %d %s", tc.method, tc.path, tc.status, w.Code, w.Body.String())
		}
	}
	if w := request("GET", "/databases/"+databaseID+"?table=items", "", nil, ""); w.Header().Get("Location") != "/login?next=%2Fdatabases%2F"+databaseID+"%3Ftable%3Ditems" {
		t.Fatal("anonymous page was not sent to login", w.Header().Get("Location"))
	}
	w := request("GET", "/api/v1/databases", "", edit.cookie, "")
	if !strings.Contains(w.Body.String(), `"transactional_migrations":true`) || !strings.Contains(w.Body.String(), `"schema_diff":true`) {
		t.Fatal("unavailable capabilities advertised")
	}
	w = request("POST", base+"/schema-diff", diffBody, edit.cookie, edit.csrf)
	var diff struct {
		Data schemadiff.DiffResult `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &diff); err != nil || w.Code != 200 || !diff.Data.Executable || diff.Data.CandidateSQL == "" || diff.Data.ExpectedVersion != 0 {
		t.Fatal("schema diff", w.Code, w.Body.String(), err)
	}
	var unexpected int
	if err := target.QueryRow(`SELECT count(*) FROM pragma_table_info('items') WHERE name='status'`).Scan(&unexpected); err != nil || unexpected != 0 {
		t.Fatal("schema diff applied candidate", err)
	}
	if err := db.QueryRow(`SELECT (SELECT count(*) FROM migration_runs)+(SELECT count(*) FROM migration_drafts)`).Scan(&unexpected); err != nil || unexpected != 0 {
		t.Fatal("schema diff created run or draft", err)
	}
	w = request("POST", base+"/schema-diff", `{"desired_database_id":"90000000-0000-4000-8000-000000000099","schema":"main"}`, edit.cookie, edit.csrf)
	if w.Code != 404 {
		t.Fatal("unauthorized desired database", w.Code, w.Body.String())
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
	pagePath := "/databases/" + databaseID + "/migrations"
	for _, tc := range []struct {
		who      loginState
		query    string
		status   int
		contains string
	}{
		{edit, "?draft=" + draft.Data.ID + "&run=" + firstRun, 200, "CREATE TABLE migrated"},
		{view, "?run=" + firstRun, 200, "Status: Berhasil"},
		{view, "?draft=" + draft.Data.ID, 403, "Akses ditolak"},
		{edit, "?run=90000000-0000-4000-8000-000000000099", 404, "Halaman tidak ditemukan"},
		{edit, "?history_offset=-1", 400, "Permintaan tidak valid"},
	} {
		w := request("GET", pagePath+tc.query, "", tc.who.cookie, "")
		if w.Code != tc.status || !strings.Contains(w.Body.String(), tc.contains) {
			t.Fatal("server-rendered migration page", w.Code, w.Body.String())
		}
	}
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
	var runsBefore, runsAfter int
	if err := db.QueryRow(`SELECT count(*) FROM migration_runs WHERE database_id=$1`, databaseID).Scan(&runsBefore); err != nil || runsBefore < 2 {
		t.Fatal("runs before disable", runsBefore, err)
	}
	if w := request("DELETE", "/api/v1/admin/connections/"+databaseID, "", admin.cookie, admin.csrf); w.Code != 204 {
		t.Fatal("disable failed", w.Code)
	}
	if err := db.QueryRow(`SELECT count(*) FROM migration_runs WHERE database_id=$1`, databaseID).Scan(&runsAfter); err != nil || runsAfter != runsBefore {
		t.Fatal("disable lost migration history", runsBefore, runsAfter, err)
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
