package connections

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/contracts"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/database"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/testkit"
)

type harness struct {
	*env
	mux  *http.ServeMux
	user *contracts.Principal
}

func newHarness(t *testing.T) *harness {
	h := &harness{env: newEnv(t, nil), mux: http.NewServeMux()}
	RegisterRoutes(h.mux, NewHandler(h.svc, testkit.NewFixtureAccessChecker(), func(context.Context) (contracts.Principal, bool) {
		if h.user == nil {
			return contracts.Principal{}, false
		}
		return *h.user, true
	}))
	return h
}

func (h *harness) do(userID, method, path, body string) (int, map[string]any) {
	h.user = nil
	if userID != "" {
		h.user = &contracts.Principal{UserID: userID, Username: "u", SessionID: "s", IsPlatformAdmin: userID == testkit.UserAdmin}
	}
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	h.mux.ServeHTTP(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func errCode(out map[string]any) string {
	e, _ := out["error"].(map[string]any)
	c, _ := e["code"].(string)
	return c
}

func assertFixture(t *testing.T, out map[string]any, name string) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "tests", "fixtures", "api", name))
	if err != nil {
		t.Fatal(err)
	}
	var want map[string]any
	if err := json.Unmarshal(raw, &want); err != nil {
		t.Fatal(err)
	}
	delete(want["error"].(map[string]any), "request_id")
	got, _ := out["error"].(map[string]any)
	delete(got, "request_id")
	if !reflect.DeepEqual(got, want["error"]) {
		t.Fatalf("got %v, fixture %s has %v", got, name, want["error"])
	}
}

const admin = "/api/v1/admin/connections"

func TestAdminConnectionsRequirePlatformAdmin(t *testing.T) {
	h := newHarness(t)
	if status, out := h.do("", http.MethodGet, admin, ""); status != 401 || errCode(out) != "UNAUTHENTICATED" {
		t.Errorf("no session: %d %v", status, out)
	}
	for _, c := range []struct{ method, path, body string }{
		{http.MethodGet, admin, ""},
		{http.MethodPost, admin, `{"name":"x","driver":"sqlite","config":{"file":"a.sqlite"}}`},
		{http.MethodPatch, admin + "/" + testkit.DBPG, `{"revision":1,"name":"x"}`},
		{http.MethodPost, admin + "/" + testkit.DBPG + "/test", ""},
		{http.MethodDelete, admin + "/" + testkit.DBPG, ""},
	} {
		if status, out := h.do(testkit.UserEdit, c.method, c.path, c.body); status != 403 || errCode(out) != "FORBIDDEN" {
			t.Errorf("%s %s by editor: %d %v", c.method, c.path, status, out)
		}
	}
}

func TestAdminConnectionsLifecycle(t *testing.T) {
	h := newHarness(t)
	sqliteFixture(t, h.dir, "a.sqlite")

	status, out := h.do(testkit.UserAdmin, http.MethodPost, admin, `{"name":"Latihan","driver":"sqlite","config":{"file":"a.sqlite"}}`)
	if status != 201 {
		t.Fatalf("create: %d %v", status, out)
	}
	created := out["data"].(map[string]any)
	id := created["id"].(string)
	if created["revision"] != 1.0 || created["migration_ready"] != false || created["active"] != true {
		t.Fatalf("created %v", created)
	}

	if status, out := h.do(testkit.UserAdmin, http.MethodPost, admin, `{"name":"x","driver":"sqlite","config":{"file":"b.sqlite"},"revision":3}`); status != 400 {
		t.Errorf("server-owned field accepted: %d %v", status, out)
	}
	pg, _ := json.Marshal(map[string]any{"name": "PG", "driver": "postgres", "config": map[string]any{"host": "db.example", "port": 70000,
		"database": "demo", "username": "u", "password": "p", "sslmode": "require", "editor_role": "platform_editor"}})
	_, out = h.do(testkit.UserAdmin, http.MethodPost, admin, string(pg))
	assertFixture(t, out, "connections_error_validation.json")
	if strings.Contains(toJSON(out), `"p"`) {
		t.Fatal("password reflected in validation error")
	}

	status, out = h.do(testkit.UserAdmin, http.MethodGet, admin+"?limit=1", "")
	page, _ := out["page"].(map[string]any)
	if status != 200 || len(out["data"].([]any)) != 1 || page["limit"] != 1.0 {
		t.Fatalf("list: %d %v", status, out)
	}
	if status, out := h.do(testkit.UserAdmin, http.MethodGet, admin+"?limit=500", ""); status != 400 {
		t.Errorf("limit bound: %d %v", status, out)
	}
	if status, out := h.do(testkit.UserAdmin, http.MethodGet, admin+"/"+id, ""); status != 200 || out["data"].(map[string]any)["id"] != id {
		t.Fatalf("get: %d %v", status, out)
	}
	if status, out := h.do(testkit.UserAdmin, http.MethodGet, admin+"/not-a-uuid", ""); status != 400 || errCode(out) != "INVALID_REQUEST" {
		t.Errorf("bad id: %d %v", status, out)
	}
	if status, out := h.do(testkit.UserAdmin, http.MethodGet, admin+"/"+testkit.DBPG, ""); status != 404 || errCode(out) != "NOT_FOUND" {
		t.Errorf("unknown id: %d %v", status, out)
	}

	if status, out := h.do(testkit.UserAdmin, http.MethodPatch, admin+"/"+id, `{"name":"Baru"}`); status != 400 ||
		out["error"].(map[string]any)["fields"].(map[string]any)["revision"] == nil {
		t.Errorf("missing revision: %d %v", status, out)
	}
	if status, out := h.do(testkit.UserAdmin, http.MethodPatch, admin+"/"+id, `{"revision":1}`); status != 400 {
		t.Errorf("revision alone is not a change: %d %v", status, out)
	}
	_, out = h.do(testkit.UserAdmin, http.MethodPatch, admin+"/"+id, `{"revision":9,"name":"Baru"}`)
	assertFixture(t, out, "connections_error_revision_conflict.json")
	if status, out := h.do(testkit.UserAdmin, http.MethodPatch, admin+"/"+id, `{"revision":1,"name":"Baru"}`); status != 200 ||
		out["data"].(map[string]any)["revision"] != 2.0 {
		t.Fatalf("patch: %d %v", status, out)
	}

	status, out = h.do(testkit.UserAdmin, http.MethodPost, admin+"/"+id+"/test", "")
	if status != 200 || out["data"].(map[string]any)["reachable"] != true || out["data"].(map[string]any)["capabilities"] == nil {
		t.Fatalf("test: %d %v", status, out)
	}

	rec := httptest.NewRecorder()
	h.user = &contracts.Principal{UserID: testkit.UserAdmin, IsPlatformAdmin: true}
	h.mux.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, admin+"/"+id, nil))
	if rec.Code != 204 || rec.Body.Len() != 0 {
		t.Fatalf("delete: %d %q", rec.Code, rec.Body.String())
	}
	if len(h.life.called) != 1 || h.life.called[0] != id {
		t.Fatalf("access lifecycle not told: %v", h.life.called)
	}
	if status, out := h.do(testkit.UserAdmin, http.MethodPost, admin+"/"+id+"/test", ""); status != 404 {
		t.Errorf("test after deactivate: %d %v", status, out)
	}
}

func toJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// register stores an SQLite registration under a fixture id, sealed for that
// id, so the fixture access checker grants it.
func (h *harness) register(t *testing.T, name, file, id string) {
	t.Helper()
	cfg, err := ParseConfig(database.SQLite, sqliteJSON(file), Policy{})
	if err != nil {
		t.Fatal(err)
	}
	plain, err := encodeConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	enc, err := h.svc.cipher.Seal(plain, []byte(id))
	if err != nil {
		t.Fatal(err)
	}
	if err := h.svc.store.Insert(ctx, h.meta, Row{ID: id, Name: name, Driver: database.SQLite, ConfigEncrypted: enc}); err != nil {
		t.Fatal(err)
	}
}

func TestDatabasesListOnlyAccessibleTargets(t *testing.T) {
	h := newHarness(t)
	h.register(t, "Zeta", "z.sqlite", testkit.DBSQLite)
	h.register(t, "Alpha", "a.sqlite", testkit.DBPG)
	h.register(t, "Hidden", "h.sqlite", "30000000-0000-4000-8000-0000000000ff")

	names := func(out map[string]any) (got []string) {
		for _, d := range out["data"].([]any) {
			m := d.(map[string]any)
			got = append(got, m["name"].(string)+":"+m["effective_access"].(string))
		}
		return got
	}
	if status, out := h.do("", http.MethodGet, "/api/v1/databases", ""); status != 401 {
		t.Errorf("no session: %d %v", status, out)
	}
	_, out := h.do(testkit.UserView, http.MethodGet, "/api/v1/databases", "")
	if got := names(out); !reflect.DeepEqual(got, []string{"Alpha:view", "Zeta:view"}) {
		t.Fatalf("view user sees %v", got)
	}
	_, out = h.do(testkit.UserEdit, http.MethodGet, "/api/v1/databases?limit=1", "")
	if got := names(out); !reflect.DeepEqual(got, []string{"Alpha:edit"}) || out["page"].(map[string]any)["has_more"] != true {
		t.Fatalf("edit user page 1: %v %v", got, out["page"])
	}
	_, out = h.do(testkit.UserEdit, http.MethodGet, "/api/v1/databases?limit=1&offset=1", "")
	if got := names(out); !reflect.DeepEqual(got, []string{"Zeta:edit"}) || out["page"].(map[string]any)["has_more"] != false {
		t.Fatalf("edit user page 2: %v %v", got, out["page"])
	}
	for _, u := range []string{testkit.UserNone, testkit.UserAdmin} {
		if _, out := h.do(u, http.MethodGet, "/api/v1/databases", ""); len(out["data"].([]any)) != 0 {
			t.Errorf("%s sees %v", u, out["data"])
		}
	}

	status, out := h.do(testkit.UserView, http.MethodGet, "/api/v1/databases/"+testkit.DBSQLite, "")
	d, _ := out["data"].(map[string]any)
	if status != 200 || d["effective_access"] != "view" || d["driver"] != "sqlite" || d["capabilities"].(map[string]any)["explain_analyze"] != false {
		t.Fatalf("detail: %d %v", status, out)
	}
	if _, has := d["config"]; has {
		t.Fatal("public database exposes config")
	}
	for _, c := range []struct {
		user, id string
		status   int
	}{
		{testkit.UserNone, testkit.DBSQLite, 404},
		{testkit.UserView, "30000000-0000-4000-8000-0000000000ff", 404},
		{testkit.UserView, "NOT-A-UUID", 400},
	} {
		if status, out := h.do(c.user, http.MethodGet, "/api/v1/databases/"+c.id, ""); status != c.status {
			t.Errorf("%s %s: %d %v", c.user, c.id, status, out)
		}
	}
}
