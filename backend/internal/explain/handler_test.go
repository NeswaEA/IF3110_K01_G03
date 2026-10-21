package explain

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
	"time"

	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/contracts"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/database"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/database/databasetest"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/database/postgres"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/database/sqlite"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/testkit"
)

type harness struct {
	mux  *http.ServeMux
	p    *databasetest.Provider
	user *contracts.Principal
}

func newHarness(t *testing.T) *harness {
	lite := sqliteTarget(t)
	lite.DatabaseID = testkit.DBSQLite
	p := databasetest.NewProvider(lite, database.TargetHandle{DatabaseID: testkit.DBPG, Driver: database.Postgres})
	h := &harness{mux: http.NewServeMux(), p: p}
	svc := NewService(p, p, map[database.Driver]Planner{database.Postgres: postgres.Planner{}, database.SQLite: sqlite.Planner{}}, 5*time.Second)
	RegisterRoutes(h.mux, NewHandler(svc, testkit.NewFixtureAccessChecker(), func(context.Context) (contracts.Principal, bool) {
		if h.user == nil {
			return contracts.Principal{}, false
		}
		return *h.user, true
	}))
	return h
}

func (h *harness) as(userID string) *harness {
	h.user = &contracts.Principal{UserID: userID, Username: "u", SessionID: "s", IsPlatformAdmin: userID == testkit.UserAdmin}
	return h
}

func (h *harness) post(id, body string) (int, map[string]any) {
	req := httptest.NewRequest(http.MethodPost, "/api/v1/databases/"+id+"/explain", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.mux.ServeHTTP(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func code(out map[string]any) string {
	e, _ := out["error"].(map[string]any)
	c, _ := e["code"].(string)
	return c
}

// matchesFixture compares an error body with a shared API fixture, ignoring request_id.
func matchesFixture(t *testing.T, out map[string]any, name string) {
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
	got := out["error"].(map[string]any)
	delete(got, "request_id")
	if !reflect.DeepEqual(got, want["error"]) {
		t.Fatalf("response %v does not match fixture %s %v", got, name, want["error"])
	}
}

// Session, then edit access, then body: earlier failures hide later ones and
// nothing reaches the target.
func TestExplainHandlerValidationOrder(t *testing.T) {
	h := newHarness(t)
	big := `{"sql":"SELECT '` + strings.Repeat("x", MaxSQLBytes) + `'","mode":"estimate"}`
	cases := []struct {
		name, user, id, body string
		status               int
		code                 string
	}{
		{"no session", "", testkit.DBSQLite, `not json`, 401, "UNAUTHENTICATED"},
		{"view only", testkit.UserView, testkit.DBSQLite, `not json`, 403, "FORBIDDEN"},
		{"no grant", testkit.UserNone, testkit.DBSQLite, `not json`, 404, "NOT_FOUND"},
		{"admin without grant", testkit.UserAdmin, testkit.DBSQLite, `{"sql":"SELECT 1","mode":"estimate"}`, 404, "NOT_FOUND"},
		{"malformed id", testkit.UserEdit, "not-a-uuid", `{"sql":"SELECT 1","mode":"estimate"}`, 400, "INVALID_REQUEST"},
		{"broken json", testkit.UserEdit, testkit.DBSQLite, `{"sql":`, 400, "INVALID_REQUEST"},
		{"unknown field", testkit.UserEdit, testkit.DBSQLite, `{"sql":"SELECT 1","mode":"estimate","actor_id":"x"}`, 400, "INVALID_REQUEST"},
		{"missing mode", testkit.UserEdit, testkit.DBSQLite, `{"sql":"SELECT 1"}`, 400, "INVALID_REQUEST"},
		{"blank sql", testkit.UserEdit, testkit.DBSQLite, `{"sql":"  ","mode":"estimate"}`, 400, "INVALID_REQUEST"},
		{"sql too large", testkit.UserEdit, testkit.DBSQLite, big, 413, "PAYLOAD_TOO_LARGE"},
		{"analyze", testkit.UserEdit, testkit.DBSQLite, `{"sql":"SELECT 1","mode":"analyze"}`, 422, "UNSUPPORTED_SQL"},
		{"postgres reserved", testkit.UserEdit, testkit.DBPG, `{"sql":"SELECT * FROM _platform.ledger","mode":"estimate"}`, 422, "UNSUPPORTED_SQL"},
		{"postgres ddl", testkit.UserEdit, testkit.DBPG, `{"sql":"DROP TABLE items","mode":"estimate"}`, 422, "PLAN_UNSUPPORTED"},
	}
	for _, c := range cases {
		if c.user == "" {
			h.user = nil
		} else {
			h.as(c.user)
		}
		status, out := h.post(c.id, c.body)
		if status != c.status || code(out) != c.code {
			t.Errorf("%s: %d %s, want %d %s", c.name, status, code(out), c.status, c.code)
		}
	}
	if h.p.Acquired() != 0 {
		t.Fatalf("target opened %d times for rejected requests", h.p.Acquired())
	}

	h.as(testkit.UserEdit)
	if status, out := h.post(testkit.DBSQLite, `{"sql":"SELECT 1","mode":"analyze"}`); status != 422 ||
		out["error"].(map[string]any)["fields"].(map[string]any)["mode"] == nil {
		t.Errorf("mode error must name the field: %v", out)
	}
}

func TestExplainHandlerResponses(t *testing.T) {
	h := newHarness(t).as(testkit.UserEdit)

	status, out := h.post(testkit.DBSQLite, `{"sql":"SELECT * FROM items WHERE id = 2","mode":"estimate"}`)
	data, _ := out["data"].(map[string]any)
	if status != 200 || data["driver"] != "sqlite" || data["mode"] != "estimate" || len(data["nodes"].([]any)) != 1 {
		t.Fatalf("plan: %d %v", status, out)
	}
	node := data["nodes"].([]any)[0].(map[string]any)
	if node["operation"] != "SEARCH" || node["estimated_cost"] != nil || node["actual_rows"] != nil {
		t.Fatalf("node %v", node)
	}

	_, out = h.post(testkit.DBSQLite, `{"sql":"SELECT 1; SELECT 2","mode":"estimate"}`)
	matchesFixture(t, out, "explain_error_multiple_statements.json")
	_, out = h.post(testkit.DBSQLite, `{"sql":"CREATE TABLE z (a int)","mode":"estimate"}`)
	matchesFixture(t, out, "explain_error_unsupported.json")

	status, out = h.post(testkit.DBSQLite, `{"sql":"SELECT * FROM not_created_yet","mode":"estimate"}`)
	msg, _ := out["error"].(map[string]any)["message"].(string)
	if status != 422 || code(out) != "SQL_EXECUTION_FAILED" || !strings.Contains(msg, "not_created_yet") {
		t.Fatalf("engine error: %d %v", status, out)
	}

	h.p.Fail(testkit.DBPG, database.ErrTargetUnavailable)
	if status, out := h.post(testkit.DBPG, `{"sql":"SELECT 1","mode":"estimate"}`); status != 502 || code(out) != "TARGET_UNAVAILABLE" {
		t.Errorf("unavailable: %d %v", status, out)
	}
	h.p.Fail(testkit.DBPG, database.ErrTargetTimeout)
	if status, out := h.post(testkit.DBPG, `{"sql":"SELECT 1","mode":"estimate"}`); status != 504 || code(out) != "TARGET_TIMEOUT" {
		t.Errorf("timeout: %d %v", status, out)
	}
	if h.p.Acquired() != h.p.Released() {
		t.Fatalf("lease leak %d/%d", h.p.Acquired(), h.p.Released())
	}
}
