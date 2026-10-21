package viewer

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/contracts"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/database"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/database/databasetest"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/testkit"
)

type harness struct {
	mux  *http.ServeMux
	p    *databasetest.Provider
	r    *fakeReader
	user *contracts.Principal
}

func newHarness() *harness {
	p := databasetest.NewProvider(database.TargetHandle{DatabaseID: testkit.DBPG, Driver: database.Postgres})
	h := &harness{mux: http.NewServeMux(), p: p, r: &fakeReader{}}
	svc := NewService(p, fakeInspector{}, map[database.Driver]TableReader{database.Postgres: h.r}, time.Second)
	RegisterRoutes(h.mux, NewHandler(svc, testkit.NewFixtureAccessChecker(), func(context.Context) (contracts.Principal, bool) {
		if h.user == nil {
			return contracts.Principal{}, false
		}
		return *h.user, true
	}))
	return h
}

func (h *harness) get(userID, path string) (int, map[string]any) {
	h.user = nil
	if userID != "" {
		h.user = &contracts.Principal{UserID: userID, Username: "u", SessionID: "s"}
	}
	rec := httptest.NewRecorder()
	h.mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func errCode(out map[string]any) string {
	e, _ := out["error"].(map[string]any)
	c, _ := e["code"].(string)
	return c
}

func fixtureError(t *testing.T, name string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "tests", "fixtures", "api", name))
	if err != nil {
		t.Fatal(err)
	}
	var f map[string]any
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	e := f["error"].(map[string]any)
	delete(e, "request_id")
	return e
}

func TestViewerHandlerAccess(t *testing.T) {
	h := newHarness()
	data := "/api/v1/databases/" + testkit.DBPG + "/data?schema=public&table=items"
	cases := []struct {
		user, path string
		status     int
		code       string
	}{
		{"", data, 401, "UNAUTHENTICATED"},
		{testkit.UserNone, data, 404, "NOT_FOUND"},
		{testkit.UserAdmin, data, 404, "NOT_FOUND"}, // admin is not implicitly a viewer
		{testkit.UserView, "/api/v1/databases/NOT-A-UUID/schema", 400, "INVALID_REQUEST"},
		{testkit.UserView, "/api/v1/databases/" + testkit.DBPG + "/data?table=items", 400, "INVALID_REQUEST"},
		{testkit.UserView, data + "&limit=201", 400, "INVALID_REQUEST"},
		{testkit.UserView, data + "&offset=-1", 400, "INVALID_REQUEST"},
		{testkit.UserView, "/api/v1/databases/" + testkit.DBPG + "/data?schema=_platform&table=migration_ledger", 404, "NOT_FOUND"},
	}
	for _, c := range cases {
		if status, out := h.get(c.user, c.path); status != c.status || errCode(out) != c.code {
			t.Errorf("%s %s: %d %v", c.user, c.path, status, out)
		}
	}
	if h.p.Acquired() != 0 || h.r.calls != 0 {
		t.Fatalf("target touched by rejected requests: acquired=%d reads=%d", h.p.Acquired(), h.r.calls)
	}
}

func TestViewerHandlerResponses(t *testing.T) {
	h := newHarness()
	status, out := h.get(testkit.UserView, "/api/v1/databases/"+testkit.DBPG+"/data?schema=public&table=items&limit=10&offset=20")
	page, _ := out["page"].(map[string]any)
	if status != 200 || out["data"] == nil || page["limit"] != 10.0 || page["offset"] != 20.0 || page["has_more"] != true {
		t.Fatalf("data: %d %v", status, out)
	}
	if status, out := h.get(testkit.UserView, "/api/v1/databases/"+testkit.DBPG+"/schema"); status != 200 || out["page"] != nil {
		t.Fatalf("schema is not paginated: %d %v", status, out)
	}

	h.r.err = database.ErrTargetPermission
	status, out = h.get(testkit.UserEdit, "/api/v1/databases/"+testkit.DBPG+"/data?schema=public&table=items")
	if status != 403 || errCode(out) != "FORBIDDEN" {
		t.Fatalf("permission: %d %v", status, out)
	}
	h.r.err = database.ErrTargetUnavailable
	_, out = h.get(testkit.UserView, "/api/v1/databases/"+testkit.DBPG+"/data?schema=public&table=items")
	got := out["error"].(map[string]any)
	delete(got, "request_id")
	if want := fixtureError(t, "viewer_error_target_unavailable.json"); !reflect.DeepEqual(got, want) {
		t.Fatalf("unavailable %v, fixture %v", got, want)
	}
	h.r.err = database.ErrTargetTimeout
	if status, out := h.get(testkit.UserView, "/api/v1/databases/"+testkit.DBPG+"/data?schema=public&table=items"); status != 504 || errCode(out) != "TARGET_TIMEOUT" {
		t.Fatalf("timeout: %d %v", status, out)
	}
	if h.p.Acquired() != h.p.Released() {
		t.Fatalf("lease leak %d/%d", h.p.Acquired(), h.p.Released())
	}
}
