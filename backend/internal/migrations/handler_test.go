package migrations

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/access"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/auth"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/database"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/database/databasetest"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/platform"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/testkit"
)

const migrationAPI = "/api/v1/databases/" + testkit.DBSQLite

type migrationHTTPFixture struct {
	service  *Service
	provider *databasetest.Provider
	target   database.TargetHandle
	handler  http.Handler
	sessions map[string]auth.Session
}

func newMigrationHTTP(t *testing.T) migrationHTTPFixture {
	t.Helper()
	h := sqliteTarget(t)
	s, provider := migrationService(t, h)
	if _, err := s.store.DB.Exec(`
	 UPDATE users SET is_platform_admin=true WHERE id='` + testkit.UserAdmin + `';
	 INSERT INTO groups(id,name) VALUES('20000000-0000-4000-8000-000000000001','migration http');
	 INSERT INTO group_memberships(group_id,user_id) VALUES
	 ('20000000-0000-4000-8000-000000000001','` + testkit.UserEdit + `');
	 INSERT INTO group_database_access(group_id,database_id,level) VALUES
	 ('20000000-0000-4000-8000-000000000001','` + testkit.DBSQLite + `','edit'),
	 ('20000000-0000-4000-8000-000000000001','` + testkit.DBPG + `','edit');
	 INSERT INTO groups(id,name) VALUES('20000000-0000-4000-8000-000000000002','migration view');
	 INSERT INTO group_memberships(group_id,user_id) VALUES
	 ('20000000-0000-4000-8000-000000000002','` + testkit.UserView + `');
	 INSERT INTO group_database_access(group_id,database_id,level) VALUES
	 ('20000000-0000-4000-8000-000000000002','` + testkit.DBSQLite + `','view');`); err != nil {
		t.Fatal(err)
	}
	s.access = access.NewPolicy(s.store.DB)
	store, err := auth.NewSessionStore(s.store.DB, platform.SystemClock{}, auth.DefaultSessionOptions())
	if err != nil {
		t.Fatal(err)
	}
	sessions := map[string]auth.Session{}
	for _, user := range []string{testkit.UserEdit, testkit.UserView, testkit.UserAdmin} {
		tx, err := s.store.DB.BeginTx(t.Context(), nil)
		if err != nil {
			t.Fatal(err)
		}
		session, err := store.CreateInTx(t.Context(), tx, user)
		if err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		sessions[user] = session
	}
	authHTTP, err := auth.NewHTTPHandler(store, "http://localhost:8080", false)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	RegisterRoutes(mux, NewHandler(s, auth.PrincipalFromContext))
	return migrationHTTPFixture{s, provider, h, platform.RequestID(authHTTP.RequireSession(authHTTP.RequireCSRF(mux))), sessions}
}

func (f migrationHTTPFixture) request(t *testing.T, user, method, path string, body any, headers http.Header) *httptest.ResponseRecorder {
	t.Helper()
	text, raw := body.(string)
	if !raw {
		value, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		text = string(value)
	}
	r := httptest.NewRequest(method, path, strings.NewReader(text))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Origin", "http://localhost:8080")
	if session, ok := f.sessions[user]; ok {
		r.AddCookie(&http.Cookie{Name: "session", Value: session.Token})
		r.Header.Set("X-CSRF-Token", session.CSRFToken)
	}
	for name, values := range headers {
		r.Header.Del(name)
		for _, value := range values {
			r.Header.Add(name, value)
		}
	}
	w := httptest.NewRecorder()
	f.handler.ServeHTTP(w, r)
	return w
}

func assertMigrationHTTPError(t *testing.T, w *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	var body struct {
		Error struct {
			Code      string            `json:"code"`
			Fields    map[string]string `json:"fields"`
			RequestID string            `json:"request_id"`
		} `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || w.Code != status || body.Error.Code != code || body.Error.Fields == nil || body.Error.RequestID == "" || body.Error.RequestID != w.Header().Get("X-Request-ID") {
		t.Fatalf("expected %d %s, got %d %s (%v)", status, code, w.Code, w.Body.String(), err)
	}
}

func migrationHTTPData[T any](t *testing.T, w *httptest.ResponseRecorder, status int) T {
	t.Helper()
	var body struct {
		Data T `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || w.Code != status {
		t.Fatalf("expected status %d, got %d %s (%v)", status, w.Code, w.Body.String(), err)
	}
	return body.Data
}

func assertMigrationCounts(t *testing.T, f migrationHTTPFixture, drafts, runs int) {
	t.Helper()
	var d, r int
	if err := f.service.store.DB.QueryRow(`SELECT (SELECT count(*) FROM migration_drafts),(SELECT count(*) FROM migration_runs)`).Scan(&d, &r); err != nil || d != drafts || r != runs {
		t.Fatalf("metadata counts: drafts=%d runs=%d, want %d/%d (%v)", d, r, drafts, runs, err)
	}
}

func TestMigrationHTTPAuthorizationAndCSRF(t *testing.T) {
	f := newMigrationHTTP(t)
	d := create(t, f.service, f.target, "SELECT 1;")
	run, err := f.service.Execute(t.Context(), editor, input(d))
	if err != nil {
		t.Fatal(err)
	}
	for _, endpoint := range []struct {
		method, path string
		viewAllowed  bool
	}{
		{"GET", "/migration-drafts", false},
		{"POST", "/migration-drafts", false},
		{"GET", "/migration-drafts/" + d.ID, false},
		{"PATCH", "/migration-drafts/" + d.ID, false},
		{"POST", "/migration-drafts/" + d.ID + "/execute", false},
		{"GET", "/migration-runs", true},
		{"GET", "/migration-runs/" + run.ID, true},
		{"POST", "/migration-runs/" + run.ID + "/reconcile", false},
		{"GET", "/migration-head", true},
	} {
		t.Run(endpoint.method+endpoint.path, func(t *testing.T) {
			before := f.provider.Acquired()
			assertMigrationHTTPError(t, f.request(t, "", endpoint.method, migrationAPI+endpoint.path, "bad JSON", nil), 401, "UNAUTHENTICATED")
			assertMigrationHTTPError(t, f.request(t, testkit.UserAdmin, endpoint.method, migrationAPI+endpoint.path, "bad JSON", nil), 404, "NOT_FOUND")
			if !endpoint.viewAllowed {
				assertMigrationHTTPError(t, f.request(t, testkit.UserView, endpoint.method, migrationAPI+endpoint.path, "bad JSON", nil), 403, "FORBIDDEN")
			}
			if endpoint.method != "GET" {
				for _, headers := range []http.Header{
					{"X-CSRF-Token": {}}, {"X-CSRF-Token": {"incorrect"}}, {"Origin": {"https://other.example"}},
				} {
					assertMigrationHTTPError(t, f.request(t, testkit.UserEdit, endpoint.method, migrationAPI+endpoint.path, "bad JSON", headers), 403, "CSRF_REJECTED")
				}
			}
			if f.provider.Acquired() != before {
				t.Fatal("denied request acquired the target")
			}
			assertMigrationCounts(t, f, 1, 1)
			if endpoint.viewAllowed {
				w := f.request(t, testkit.UserView, endpoint.method, migrationAPI+endpoint.path, "", nil)
				if w.Code != 200 {
					t.Fatal(w.Code, w.Body.String())
				}
			}
		})
	}
}

func TestMigrationHTTPRejectsInvalidInputBeforeTarget(t *testing.T) {
	f := newMigrationHTTP(t)
	d := create(t, f.service, f.target, "SELECT 1;")
	key, _ := newID()
	validKey := http.Header{"Idempotency-Key": {key}}
	validDraft := `{"title":"valid","sql":"SELECT 1;","expected_version":0}`
	validPatch := `{"title":"valid","sql":"SELECT 1;","expected_version":0,"revision":1}`
	cases := []struct {
		name, method, path, body string
		headers                  http.Header
		status                   int
		code                     string
	}{
		{"invalid database", "GET", "/api/v1/databases/invalid/migration-head", "", nil, 400, "INVALID_REQUEST"},
		{"array", "POST", migrationAPI + "/migration-drafts", `[]`, nil, 400, "INVALID_REQUEST"},
		{"malformed", "POST", migrationAPI + "/migration-drafts", `{`, nil, 400, "INVALID_REQUEST"},
		{"trailing object", "POST", migrationAPI + "/migration-drafts", validDraft + `{}`, nil, 400, "INVALID_REQUEST"},
		{"missing fields", "POST", migrationAPI + "/migration-drafts", `{}`, nil, 400, "INVALID_REQUEST"},
		{"null SQL", "POST", migrationAPI + "/migration-drafts", `{"title":"x","sql":null,"expected_version":0}`, nil, 400, "INVALID_REQUEST"},
		{"empty SQL", "POST", migrationAPI + "/migration-drafts", `{"title":"x","sql":" \n ","expected_version":0}`, nil, 400, "INVALID_REQUEST"},
		{"NUL SQL", "POST", migrationAPI + "/migration-drafts", `{"title":"x","sql":"SELECT 1;\u0000","expected_version":0}`, nil, 400, "INVALID_REQUEST"},
		{"empty title", "POST", migrationAPI + "/migration-drafts", `{"title":" ","sql":"SELECT 1;","expected_version":0}`, nil, 400, "INVALID_REQUEST"},
		{"server-owned actor", "POST", migrationAPI + "/migration-drafts", strings.TrimSuffix(validDraft, "}") + `,"actor_id":"forged"}`, nil, 400, "INVALID_REQUEST"},
		{"revision on create", "POST", migrationAPI + "/migration-drafts", validPatch, nil, 400, "INVALID_REQUEST"},
		{"revision missing on patch", "PATCH", migrationAPI + "/migration-drafts/" + d.ID, validDraft, nil, 400, "INVALID_REQUEST"},
		{"invalid fingerprint type", "POST", migrationAPI + "/migration-drafts", strings.TrimSuffix(validDraft, "}") + `,"source_fingerprint":42}`, nil, 400, "INVALID_REQUEST"},
		{"invalid fingerprint value", "POST", migrationAPI + "/migration-drafts", strings.TrimSuffix(validDraft, "}") + `,"source_fingerprint":"sha256:bad"}`, nil, 400, "INVALID_REQUEST"},
		{"null execute revision", "POST", migrationAPI + "/migration-drafts/" + d.ID + "/execute", `{"revision":null,"expected_version":0}`, validKey, 400, "INVALID_REQUEST"},
		{"missing execute key", "POST", migrationAPI + "/migration-drafts/" + d.ID + "/execute", `{"revision":1,"expected_version":0}`, nil, 400, "INVALID_REQUEST"},
		{"duplicate execute key", "POST", migrationAPI + "/migration-drafts/" + d.ID + "/execute", `{"revision":1,"expected_version":0}`, http.Header{"Idempotency-Key": {key, key}}, 400, "INVALID_REQUEST"},
		{"invalid execute key", "POST", migrationAPI + "/migration-drafts/" + d.ID + "/execute", `{"revision":1,"expected_version":0}`, http.Header{"Idempotency-Key": {"invalid"}}, 400, "INVALID_REQUEST"},
		{"forged execute status", "POST", migrationAPI + "/migration-drafts/" + d.ID + "/execute", `{"revision":1,"expected_version":0,"status":"succeeded"}`, validKey, 400, "INVALID_REQUEST"},
		{"invalid draft id", "GET", migrationAPI + "/migration-drafts/invalid", "", nil, 400, "INVALID_REQUEST"},
		{"invalid run id", "GET", migrationAPI + "/migration-runs/invalid", "", nil, 400, "INVALID_REQUEST"},
		{"reconcile body not empty", "POST", migrationAPI + "/migration-runs/" + key + "/reconcile", `{"sql":"SELECT 1;"}`, nil, 400, "INVALID_REQUEST"},
		{"wrong content type", "POST", migrationAPI + "/migration-drafts", validDraft, http.Header{"Content-Type": {"text/plain"}}, 400, "INVALID_REQUEST"},
		{"large body", "POST", migrationAPI + "/migration-drafts", `{"sql":"` + strings.Repeat("x", 1<<20) + `"}`, nil, 413, "PAYLOAD_TOO_LARGE"},
	}
	for _, value := range []string{"-1", "9007199254740991", "9007199254740992", "1.5", `"0"`, "null"} {
		cases = append(cases, struct {
			name, method, path, body string
			headers                  http.Header
			status                   int
			code                     string
		}{"expected version " + value, "POST", migrationAPI + "/migration-drafts", `{"title":"x","sql":"SELECT 1;","expected_version":` + value + `}`, nil, 400, "INVALID_REQUEST"})
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := f.provider.Acquired()
			w := f.request(t, testkit.UserEdit, tc.method, tc.path, tc.body, tc.headers)
			assertMigrationHTTPError(t, w, tc.status, tc.code)
			assertMigrationCounts(t, f, 1, 0)
			if f.provider.Acquired() != before {
				t.Fatal("invalid request acquired the target")
			}
		})
	}
	for _, list := range []string{"migration-drafts", "migration-runs"} {
		for _, query := range []string{"limit=0", "limit=201", "limit=", "offset=-1", "offset=100001", "offset=1.5"} {
			t.Run(list+query, func(t *testing.T) {
				assertMigrationHTTPError(t, f.request(t, testkit.UserEdit, "GET", migrationAPI+"/"+list+"?"+query, "", nil), 400, "INVALID_REQUEST")
			})
		}
	}
}

func TestMigrationHTTPDraftBoundariesAndFingerprint(t *testing.T) {
	f := newMigrationHTTP(t)
	snapshot, err := f.service.inspector.Inspect(t.Context(), f.target)
	if err != nil {
		t.Fatal(err)
	}
	sql := "  SELECT 'exact; <script>text</script>';\n-- keep whitespace\n"
	d := migrationHTTPData[MigrationDraft](t, f.request(t, testkit.UserEdit, "POST", migrationAPI+"/migration-drafts", map[string]any{
		"title": "draft", "sql": sql, "expected_version": 0, "source_fingerprint": snapshot.Fingerprint,
	}, nil), 201)
	if d.SQL != sql || d.Revision != 1 || d.CreatedBy != testkit.UserEdit || d.AppliedAt != nil {
		t.Fatalf("incorrect created draft: %+v", d)
	}
	path := migrationAPI + "/migration-drafts/" + d.ID
	patch := map[string]any{"title": "changed", "sql": sql, "revision": d.Revision, "expected_version": 0}
	updated := migrationHTTPData[MigrationDraft](t, f.request(t, testkit.UserEdit, "PATCH", path, patch, nil), 200)
	if updated.Revision != 2 || updated.SourceFingerprint == nil || *updated.SourceFingerprint != snapshot.Fingerprint {
		t.Fatal("omitted fingerprint was not preserved", updated)
	}
	assertMigrationHTTPError(t, f.request(t, testkit.UserEdit, "PATCH", path, patch, nil), 409, "REVISION_CONFLICT")
	patch["revision"], patch["source_fingerprint"] = updated.Revision, nil
	assertMigrationHTTPError(t, f.request(t, testkit.UserEdit, "PATCH", path, patch, nil), 400, "INVALID_REQUEST")
	stored := migrationHTTPData[MigrationDraft](t, f.request(t, testkit.UserEdit, "GET", path, "", nil), 200)
	if stored.Revision != 2 || stored.SQL != sql || stored.SourceFingerprint == nil || *stored.SourceFingerprint != snapshot.Fingerprint {
		t.Fatal("rejected update changed the draft", stored)
	}
	foreignPath := "/api/v1/databases/" + testkit.DBPG + "/migration-drafts/" + d.ID
	assertMigrationHTTPError(t, f.request(t, testkit.UserEdit, "GET", foreignPath, "", nil), 404, "NOT_FOUND")
	for _, tc := range []struct {
		name, sql, title string
		version          int64
		status           int
	}{
		{"exact SQL bytes", strings.Repeat("x", MaxSQLBytes), "x", 0, 201},
		{"too many SQL bytes", strings.Repeat("x", MaxSQLBytes+1), "x", 0, 413},
		{"unicode SQL byte bound", strings.Repeat("é", MaxSQLBytes/2+1), "x", 0, 413},
		{"unicode title limit", "SELECT 1;", strings.Repeat("é", 200), 0, 201},
		{"title exceeds limit", "SELECT 1;", strings.Repeat("é", 201), 0, 400},
		{"largest draft head", "SELECT 1;", "x", maxInteger - 1, 201},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := f.provider.Acquired()
			w := f.request(t, testkit.UserEdit, "POST", migrationAPI+"/migration-drafts", map[string]any{"title": tc.title, "sql": tc.sql, "expected_version": tc.version}, nil)
			if tc.status == 201 {
				got := migrationHTTPData[MigrationDraft](t, w, tc.status)
				if got.SQL != tc.sql || got.ExpectedVersion != tc.version {
					t.Fatal("boundary draft changed input")
				}
			} else {
				code := "INVALID_REQUEST"
				if tc.status == 413 {
					code = "PAYLOAD_TOO_LARGE"
				}
				assertMigrationHTTPError(t, w, tc.status, code)
			}
			if f.provider.Acquired() != before {
				t.Fatal("saving a draft accessed the target")
			}
		})
	}
}

func TestMigrationHTTPExecuteIdempotencyAndRecovery(t *testing.T) {
	f := newMigrationHTTP(t)
	d := create(t, f.service, f.target, `CREATE TABLE http_effect(id INTEGER PRIMARY KEY); INSERT INTO http_effect VALUES(1);`)
	key, _ := newID()
	headers := http.Header{"Idempotency-Key": {key}}
	path := migrationAPI + "/migration-drafts/" + d.ID + "/execute"
	body := map[string]any{"revision": d.Revision, "expected_version": d.ExpectedVersion}
	f.service.finalize = func(context.Context, MigrationRun) error { return errors.New("metadata finalization unavailable") }
	run := migrationHTTPData[MigrationRun](t, f.request(t, testkit.UserEdit, "POST", path, body, headers), 202)
	if run.Status != OutcomeUnknown || run.Version != nil || run.FinishedAt != nil || run.Error != nil {
		t.Fatal("uncertain result claimed a terminal outcome", run)
	}
	retry := migrationHTTPData[MigrationRun](t, f.request(t, testkit.UserEdit, "POST", path, body, headers), 202)
	if retry.ID != run.ID {
		t.Fatal("idempotency retry created another run")
	}
	assertMigrationHTTPError(t, f.request(t, testkit.UserEdit, "POST", path, map[string]any{"revision": d.Revision + 1, "expected_version": 0}, headers), 409, "IDEMPOTENCY_CONFLICT")
	newKey, _ := newID()
	assertMigrationHTTPError(t, f.request(t, testkit.UserEdit, "POST", path, body, http.Header{"Idempotency-Key": {newKey}}), 409, "TARGET_OUTCOME_UNKNOWN")
	f.service.finalize = f.service.store.finalize
	terminal := migrationHTTPData[MigrationRun](t, f.request(t, testkit.UserEdit, "POST", migrationAPI+"/migration-runs/"+run.ID+"/reconcile", `{}`, nil), 200)
	if terminal.Status != Succeeded || terminal.Version == nil || *terminal.Version != 1 || terminal.SQL != d.SQL || terminal.Actor.ID != testkit.UserEdit {
		t.Fatal("reconcile changed committed input", terminal)
	}
	for _, route := range []string{path, "/api/v1/databases/" + testkit.DBPG + "/migration-drafts/" + d.ID + "/execute"} {
		code, status := "DRAFT_APPLIED", 409
		if route != path {
			code, status = "NOT_FOUND", 404
		}
		assertMigrationHTTPError(t, f.request(t, testkit.UserEdit, "POST", route, body, http.Header{"Idempotency-Key": {newKey}}), status, code)
	}
	assertMigrationHTTPError(t, f.request(t, testkit.UserEdit, "PATCH", migrationAPI+"/migration-drafts/"+d.ID, map[string]any{"title": d.Title, "sql": "SELECT 2;", "revision": d.Revision, "expected_version": 1}, nil), 409, "DRAFT_APPLIED")
	foreignRun := "/api/v1/databases/" + testkit.DBPG + "/migration-runs/" + run.ID
	assertMigrationHTTPError(t, f.request(t, testkit.UserEdit, "GET", foreignRun, "", nil), 404, "NOT_FOUND")
	assertMigrationHTTPError(t, f.request(t, testkit.UserEdit, "POST", foreignRun+"/reconcile", `{}`, nil), 404, "NOT_FOUND")
	var effects, ledger int
	if err := f.target.DB.QueryRow(`SELECT (SELECT count(*) FROM http_effect),(SELECT count(*) FROM _platform_migration_ledger)`).Scan(&effects, &ledger); err != nil || effects != 1 || ledger != 1 {
		t.Fatalf("recovery replayed SQL: effects=%d ledger=%d (%v)", effects, ledger, err)
	}
	assertMigrationCounts(t, f, 1, 1)
}

func TestMigrationHTTPErrorRedactionAndFailedRun(t *testing.T) {
	f := newMigrationHTTP(t)
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	const secret = "private_migration_marker"
	for _, tc := range []struct {
		name    string
		failure error
		status  int
		code    string
	}{
		{"engine rejection", nil, 422, "SQL_EXECUTION_FAILED"},
		{"target unavailable", fmt.Errorf("postgres://user:password@host/%s: %w", secret, database.ErrTargetUnavailable), 502, "TARGET_UNAVAILABLE"},
		{"target timeout", fmt.Errorf("%s: %w", secret, database.ErrTargetTimeout), 504, "TARGET_TIMEOUT"},
		{"internal failure", errors.New(secret), 500, "INTERNAL_ERROR"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			logs.Reset()
			d := create(t, f.service, f.target, `INSERT INTO `+secret+` VALUES(1);`)
			if tc.failure != nil {
				f.provider.Fail(f.target.DatabaseID, tc.failure)
				t.Cleanup(func() { f.provider.Fail(f.target.DatabaseID, nil) })
			}
			key, _ := newID()
			w := f.request(t, testkit.UserEdit, "POST", migrationAPI+"/migration-drafts/"+d.ID+"/execute", map[string]any{"revision": d.Revision, "expected_version": d.ExpectedVersion}, http.Header{"Idempotency-Key": {key}})
			assertMigrationHTTPError(t, w, tc.status, tc.code)
			var body struct {
				Error struct {
					RunID string `json:"run_id"`
				} `json:"error"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || body.Error.RunID == "" {
				t.Fatal("error lost its run id", err)
			}
			if strings.Contains(w.Body.String(), secret) || strings.Contains(logs.String(), secret) || strings.Contains(logs.String(), "password") {
				t.Fatal("SQL or target credentials leaked through an error or log")
			}
			stored := migrationHTTPData[MigrationRun](t, f.request(t, testkit.UserView, "GET", migrationAPI+"/migration-runs/"+body.Error.RunID, "", nil), 200)
			if stored.Status != Failed || stored.Version != nil || stored.Error == nil || stored.Error.Code != tc.code || stored.SQL != d.SQL || strings.Contains(stored.Error.Message, secret) {
				t.Fatal("failed history lost its status or protected SQL snapshot", stored)
			}
		})
	}
}
