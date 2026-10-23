package access_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/access"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/contracts"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/platform"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/testkit"
)

func TestGrantHTTPAuthorizationAndCSRF(t *testing.T) {
	f := newUserHTTPFixture(t)
	base := groupsPath + "/" + viewGroup + "/grants"
	for _, route := range []struct{ method, path, body string }{
		{"GET", base, ""},
		{"PUT", base + "/" + testkit.DBPG, `{"level":"edit"}`},
		{"DELETE", base + "/" + testkit.DBPG, ""},
	} {
		assertUserHTTPError(t, f.request(route.method, route.path, route.body, nil, false), 401, "UNAUTHENTICATED")
		assertUserHTTPError(t, f.request(route.method, route.path, route.body, &f.viewer, true), 403, "FORBIDDEN")
		if route.method != "GET" {
			assertUserHTTPError(t, f.request(route.method, route.path, route.body, &f.admin, false), 403, "CSRF_REJECTED")
			assertUserHTTPError(t, f.request(route.method, route.path, route.body, &f.admin, true,
				map[string]string{"Origin": "https://other.example"}), 403, "CSRF_REJECTED")
			assertUserHTTPError(t, f.request(route.method, route.path, route.body, &f.admin, true,
				map[string]string{"X-CSRF-Token": "invalid"}), 403, "CSRF_REJECTED")
		}
	}
	grants, _, err := groupService(f.userFixture).Grants(t.Context(), adminPrincipal, viewGroup, 50, 0)
	if err != nil || len(grants) != 1 || grants[0].Level != contracts.AccessView {
		t.Fatal("denied requests changed grant records", grants, err)
	}
}

func TestGrantHTTPLifecycleAndDTO(t *testing.T) {
	f := newUserHTTPFixture(t)
	base := groupsPath + "/" + viewGroup + "/grants"
	w := f.request("GET", base, "", &f.admin, false)
	var list struct {
		Data []access.Grant `json:"data"`
		Page platform.Page  `json:"page"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil || w.Code != 200 || len(list.Data) != 1 || list.Data[0].DatabaseID != testkit.DBPG ||
		list.Page.Limit != 50 || list.Page.Offset != 0 || list.Page.HasMore {
		t.Fatal("incorrect default grant list", err, w.Body.String())
	}
	policy := access.NewPolicy(f.db)
	requireCode(t, policy.Require(t.Context(), f.admin.Principal, contracts.ViewDatabase, testkit.DBSQLite), "NOT_FOUND")
	path := base + "/" + testkit.DBSQLite
	w = f.request("PUT", path, `{"level":"edit"}`, &f.admin, true)
	var result struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil || w.Code != 200 || len(result.Data) != 2 || result.Data["database_id"] != testkit.DBSQLite ||
		result.Data["level"] != "edit" || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("incorrect grant DTO", err, w.Code, w.Body.String())
	}
	requireCode(t, policy.Require(t.Context(), f.admin.Principal, contracts.ViewDatabase, testkit.DBSQLite), "NOT_FOUND")
	rotated, err := f.sessions.Resolve(t.Context(), f.viewer.Token)
	if err != nil || rotated.Token == "" {
		t.Fatal("grant did not rotate the member's session", err)
	}
	f.viewer.Token = rotated.Token
	f.clock.Advance(time.Minute)
	if w := f.request("PUT", path, `{"level":"edit"}`, &f.admin, true); w.Code != 200 {
		t.Fatal("duplicate grant failed", w.Code)
	}
	if resolved, err := f.sessions.Resolve(t.Context(), f.viewer.Token); err != nil || resolved.Token != "" {
		t.Fatal("duplicate grant changed the session", err)
	}
	for _, page := range []struct {
		query, database string
		more            bool
	}{
		{"?limit=1&offset=0", testkit.DBPG, true},
		{"?limit=1&offset=1", testkit.DBSQLite, false},
	} {
		w = f.request("GET", base+page.query, "", &f.admin, false)
		if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil || w.Code != 200 || len(list.Data) != 1 || list.Data[0].DatabaseID != page.database || list.Page.HasMore != page.more {
			t.Fatal("incorrect grant pagination", err, w.Body.String())
		}
	}
	w = f.request("GET", base+"?offset=100000", "", &f.admin, false)
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil || w.Code != 200 || list.Data == nil || len(list.Data) != 0 || list.Page.HasMore {
		t.Fatal("empty grant page must use an empty array", err)
	}
	w = f.request("PUT", path, `{"level":"view"}`, &f.admin, true)
	if w.Code != 200 {
		t.Fatal("grant downgrade failed", w.Code, w.Body.String())
	}
	assertUserHTTPError(t, f.request("GET", "/api/v1/auth/me", "", &f.viewer, false), 401, "UNAUTHENTICATED")
	f.viewer = f.login(t, "viewer", userPassword)
	level, err := policy.Effective(t.Context(), testkit.UserView, testkit.DBSQLite)
	if err != nil || level != contracts.AccessView {
		t.Fatal("downgrade did not change effective access", err)
	}
	w = f.request("DELETE", path, "", &f.admin, true)
	if w.Code != 204 || w.Body.Len() != 0 || w.Header().Get("Content-Type") != "" {
		t.Fatal("grant removal must return an empty 204", w.Code)
	}
	assertUserHTTPError(t, f.request("GET", "/api/v1/auth/me", "", &f.viewer, false), 401, "UNAUTHENTICATED")
	f.viewer = f.login(t, "viewer", userPassword)
	if w := f.request("DELETE", path, "", &f.admin, true); w.Code != 204 {
		t.Fatal("repeated grant removal failed", w.Code)
	}
	if w := f.request("GET", "/api/v1/auth/me", "", &f.viewer, false); w.Code != 200 {
		t.Fatal("repeated removal revoked a new session", w.Code)
	}
	level, err = policy.Effective(t.Context(), testkit.UserView, testkit.DBPG)
	if err != nil || level != contracts.AccessView {
		t.Fatal("removal changed another database's grant", err)
	}
	empty, err := groupService(f.userFixture).Create(t.Context(), adminPrincipal, "no grants")
	if err != nil {
		t.Fatal(err)
	}
	w = f.request("GET", groupsPath+"/"+empty.ID+"/grants", "", &f.admin, false)
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil || w.Code != 200 || list.Data == nil || len(list.Data) != 0 {
		t.Fatal("group without grants should have an empty list", err)
	}
}

func TestGrantHTTPValidation(t *testing.T) {
	f := newUserHTTPFixture(t)
	base := groupsPath + "/" + viewGroup + "/grants"
	path := base + "/" + testkit.DBPG
	for _, tc := range []struct {
		method, path, body string
		status             int
		code               string
	}{
		{"GET", base + "?limit=0", "", 400, "INVALID_REQUEST"},
		{"GET", base + "?limit=201", "", 400, "INVALID_REQUEST"},
		{"GET", base + "?offset=-1", "", 400, "INVALID_REQUEST"},
		{"GET", base + "?offset=100001", "", 400, "INVALID_REQUEST"},
		{"GET", base + "?limit=1.5", "", 400, "INVALID_REQUEST"},
		{"GET", groupsPath + "/bad-id/grants", "", 400, "INVALID_REQUEST"},
		{"GET", groupsPath + "/" + missingUser + "/grants", "", 404, "NOT_FOUND"},
		{"PUT", path, `{}`, 400, "INVALID_REQUEST"},
		{"PUT", path, `{"level":null}`, 400, "INVALID_REQUEST"},
		{"PUT", path, `{"level":1}`, 400, "INVALID_REQUEST"},
		{"PUT", path, `{"level":"none"}`, 400, "INVALID_REQUEST"},
		{"PUT", path, `{"level":"VIEW"}`, 400, "INVALID_REQUEST"},
		{"PUT", path, `{"level":"view","user_id":"` + testkit.UserNone + `"}`, 400, "INVALID_REQUEST"},
		{"PUT", path, `{"level":"view"} {}`, 400, "INVALID_REQUEST"},
		{"PUT", path, `{"level":"` + strings.Repeat("x", 1<<20) + `"}`, 413, "PAYLOAD_TOO_LARGE"},
		{"PUT", base + "/bad-id", `{"level":"view"}`, 400, "INVALID_REQUEST"},
		{"PUT", base + "/" + missingUser, `{"level":"view"}`, 404, "NOT_FOUND"},
		{"PUT", groupsPath + "/" + missingUser + "/grants/" + testkit.DBPG, `{"level":"view"}`, 404, "NOT_FOUND"},
		{"DELETE", base + "/bad-id", "", 400, "INVALID_REQUEST"},
		{"DELETE", base + "/" + missingUser, "", 404, "NOT_FOUND"},
		{"DELETE", groupsPath + "/" + missingUser + "/grants/" + testkit.DBPG, "", 404, "NOT_FOUND"},
	} {
		assertUserHTTPError(t, f.request(tc.method, tc.path, tc.body, &f.admin, true), tc.status, tc.code)
	}
	assertUserHTTPError(t, f.request("PUT", path, `{"level":"view"}`, &f.admin, true, map[string]string{"Content-Type": "text/plain"}), 400, "INVALID_REQUEST")
}
