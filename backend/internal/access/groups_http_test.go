package access_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/access"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/platform"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/testkit"
)

const groupsPath = "/api/v1/admin/groups"

func TestGroupHTTPAuthorizationAndCSRF(t *testing.T) {
	f := newUserHTTPFixture(t)
	for _, route := range []struct{ method, path, body string }{
		{"GET", groupsPath, ""},
		{"POST", groupsPath, `{"name":"blocked"}`},
		{"PATCH", groupsPath + "/" + viewGroup, `{"name":"blocked"}`},
		{"DELETE", groupsPath + "/" + viewGroup, ""},
		{"GET", groupsPath + "/" + viewGroup + "/members", ""},
		{"PUT", groupsPath + "/" + viewGroup + "/members/" + testkit.UserNone, `{}`},
		{"DELETE", groupsPath + "/" + viewGroup + "/members/" + testkit.UserView, ""},
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
	var groups, members int
	if err := f.db.QueryRow(`SELECT (SELECT count(*) FROM groups), (SELECT count(*) FROM group_memberships)`).Scan(&groups, &members); err != nil || groups != 2 || members != 3 {
		t.Fatal("denied requests changed groups or memberships", err)
	}
}

func TestGroupHTTPCRUDAndMembers(t *testing.T) {
	f := newUserHTTPFixture(t)
	w := f.request("GET", groupsPath, "", &f.admin, false)
	var list struct {
		Data []access.Group `json:"data"`
		Page platform.Page  `json:"page"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil || w.Code != 200 || len(list.Data) != 2 || list.Page.Limit != 50 || list.Page.Offset != 0 || list.Page.HasMore {
		t.Fatal("incorrect default group list", err, w.Body.String())
	}
	w = f.request("POST", groupsPath, `{"name":" Api group "}`, &f.admin, true)
	var result struct {
		Data access.Group `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil || w.Code != 201 || result.Data.Name != "Api group" {
		t.Fatal("group creation failed", err, w.Body.String())
	}
	var raw struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &raw); err != nil || len(raw.Data) != 4 || w.Header().Get("Cache-Control") != "no-store" ||
		!strings.HasSuffix(raw.Data["created_at"].(string), "Z") || !strings.HasSuffix(raw.Data["updated_at"].(string), "Z") {
		t.Fatal("incorrect group DTO", err)
	}
	assertUserHTTPError(t, f.request("POST", groupsPath, `{"name":"Api group"}`, &f.admin, true), 409, "ALREADY_EXISTS")
	w = f.request("GET", groupsPath+"?limit=1&offset=0", "", &f.admin, false)
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil || w.Code != 200 || len(list.Data) != 1 || list.Data[0].ID != result.Data.ID || !list.Page.HasMore {
		t.Fatal("incorrect paginated group list", err)
	}
	w = f.request("GET", groupsPath+"?offset=100000", "", &f.admin, false)
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil || list.Data == nil || len(list.Data) != 0 || list.Page.HasMore {
		t.Fatal("empty group list should use an empty array", err)
	}
	base := groupsPath + "/" + result.Data.ID
	f.clock.Advance(time.Minute)
	w = f.request("PATCH", base, `{"name":"Renamed group"}`, &f.admin, true)
	var renamed struct {
		Data access.Group `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &renamed); err != nil || w.Code != 200 || renamed.Data.ID != result.Data.ID || renamed.Data.Name != "Renamed group" || !renamed.Data.CreatedAt.Equal(result.Data.CreatedAt) {
		t.Fatal("group rename failed", err)
	}
	var members struct {
		Data []access.User `json:"data"`
		Page platform.Page `json:"page"`
	}
	w = f.request("GET", base+"/members", "", &f.admin, false)
	if err := json.Unmarshal(w.Body.Bytes(), &members); err != nil || w.Code != 200 || members.Data == nil || len(members.Data) != 0 || members.Page.Limit != 50 {
		t.Fatal("empty member list should use an empty array", err)
	}
	outsider := createSession(t, f.db, f.sessions, testkit.UserNone)
	other := createSession(t, f.db, f.sessions, testkit.UserEdit)
	for _, id := range []string{testkit.UserView, testkit.UserNone} {
		w = f.request("PUT", base+"/members/"+id, `{}`, &f.admin, true)
		if w.Code != 204 || w.Body.Len() != 0 || w.Header().Get("Content-Type") != "" {
			t.Fatal("member addition must return an empty 204", w.Code)
		}
	}
	rotated, err := f.sessions.Resolve(t.Context(), outsider.Token)
	if err != nil || rotated.Token == "" {
		t.Fatal("member addition did not force rotation", err)
	}
	if w := f.request("PUT", base+"/members/"+testkit.UserNone, `{}`, &f.admin, true); w.Code != 204 {
		t.Fatal("duplicate member addition failed", w.Code)
	}
	if session, err := f.sessions.Resolve(t.Context(), rotated.Token); err != nil || session.Token != "" {
		t.Fatal("duplicate member addition changed the session", err)
	}
	w = f.request("GET", base+"/members?limit=1&offset=0", "", &f.admin, false)
	if err := json.Unmarshal(w.Body.Bytes(), &members); err != nil || w.Code != 200 || len(members.Data) != 1 || members.Data[0].ID != testkit.UserView || !members.Page.HasMore {
		t.Fatal("incorrect first member page", err, w.Body.String())
	}
	w = f.request("GET", base+"/members?limit=1&offset=1", "", &f.admin, false)
	if err := json.Unmarshal(w.Body.Bytes(), &members); err != nil || w.Code != 200 || len(members.Data) != 1 || members.Data[0].ID != testkit.UserNone || members.Page.HasMore {
		t.Fatal("incorrect final member page", err)
	}
	for _, secret := range []string{"password_hash", "$argon2id$", "csrf_secret", outsider.Token, outsider.ID} {
		if strings.Contains(w.Body.String(), secret) {
			t.Fatalf("member DTO exposed %q", secret)
		}
	}
	w = f.request("DELETE", base+"/members/"+testkit.UserNone, "", &f.admin, true)
	if w.Code != 204 || w.Body.Len() != 0 {
		t.Fatal("member removal must return an empty 204", w.Code)
	}
	_, err = f.sessions.Resolve(t.Context(), rotated.Token)
	requireCode(t, err, "UNAUTHENTICATED")
	if w := f.request("DELETE", base+"/members/"+testkit.UserNone, "", &f.admin, true); w.Code != 204 {
		t.Fatal("repeated member removal failed", w.Code)
	}
	w = f.request("DELETE", base, "", &f.admin, true)
	if w.Code != 204 || w.Body.Len() != 0 {
		t.Fatal("group deletion must return an empty 204", w.Code)
	}
	assertUserHTTPError(t, f.request("GET", "/api/v1/auth/me", "", &f.viewer, false), 401, "UNAUTHENTICATED")
	if _, err := f.sessions.Resolve(t.Context(), other.Token); err != nil {
		t.Fatal("group deletion revoked a non-member", err)
	}
	assertUserHTTPError(t, f.request("GET", base+"/members", "", &f.admin, false), 404, "NOT_FOUND")
	assertUserHTTPError(t, f.request("DELETE", base, "", &f.admin, true), 404, "NOT_FOUND")
}

func TestGroupHTTPValidation(t *testing.T) {
	f := newUserHTTPFixture(t)
	base := groupsPath + "/" + viewGroup
	for _, tc := range []struct {
		method, path, body string
		status             int
		code               string
	}{
		{"GET", groupsPath + "?limit=0", "", 400, "INVALID_REQUEST"},
		{"GET", base + "/members?limit=201", "", 400, "INVALID_REQUEST"},
		{"GET", base + "/members?offset=-1", "", 400, "INVALID_REQUEST"},
		{"GET", base + "/members?offset=100001", "", 400, "INVALID_REQUEST"},
		{"GET", base + "/members?limit=1.5", "", 400, "INVALID_REQUEST"},
		{"POST", groupsPath, `{}`, 400, "INVALID_REQUEST"},
		{"POST", groupsPath, `{"name":null}`, 400, "INVALID_REQUEST"},
		{"POST", groupsPath, `{"name":"bad\u0000name"}`, 400, "INVALID_REQUEST"},
		{"POST", groupsPath, `{"name":"` + strings.Repeat("x", 101) + `"}`, 400, "INVALID_REQUEST"},
		{"POST", groupsPath, `{"name":"group","active":true}`, 400, "INVALID_REQUEST"},
		{"POST", groupsPath, `{"name":"` + strings.Repeat("x", 1<<20) + `"}`, 413, "PAYLOAD_TOO_LARGE"},
		{"PATCH", base, `{"name":true}`, 400, "INVALID_REQUEST"},
		{"PATCH", base, `{"name":"writers"}`, 409, "ALREADY_EXISTS"},
		{"PATCH", groupsPath + "/bad-id", `{"name":"valid"}`, 400, "INVALID_REQUEST"},
		{"PATCH", groupsPath + "/" + missingUser, `{"name":"valid"}`, 404, "NOT_FOUND"},
		{"DELETE", groupsPath + "/bad-id", "", 400, "INVALID_REQUEST"},
		{"DELETE", groupsPath + "/" + missingUser, "", 404, "NOT_FOUND"},
		{"GET", groupsPath + "/bad-id/members", "", 400, "INVALID_REQUEST"},
		{"GET", groupsPath + "/" + missingUser + "/members", "", 404, "NOT_FOUND"},
		{"PUT", base + "/members/" + testkit.UserNone, ``, 400, "INVALID_REQUEST"},
		{"PUT", base + "/members/" + testkit.UserNone, `{"active":true}`, 400, "INVALID_REQUEST"},
		{"PUT", base + "/members/" + testkit.UserNone, `{} {}`, 400, "INVALID_REQUEST"},
		{"PUT", base + "/members/bad-id", `{}`, 400, "INVALID_REQUEST"},
		{"PUT", base + "/members/" + missingUser, `{}`, 404, "NOT_FOUND"},
		{"PUT", groupsPath + "/" + missingUser + "/members/" + testkit.UserNone, `{}`, 404, "NOT_FOUND"},
		{"DELETE", base + "/members/bad-id", "", 400, "INVALID_REQUEST"},
		{"DELETE", base + "/members/" + missingUser, "", 404, "NOT_FOUND"},
	} {
		assertUserHTTPError(t, f.request(tc.method, tc.path, tc.body, &f.admin, true), tc.status, tc.code)
	}
}

func TestGroupHTTPDeletionRevokesAdminMember(t *testing.T) {
	f := newUserHTTPFixture(t)
	if err := groupService(f.userFixture).AddMember(t.Context(), viewGroup, testkit.UserAdmin); err != nil {
		t.Fatal(err)
	}
	current := createSession(t, f.db, f.sessions, testkit.UserAdmin)
	w := f.request("DELETE", groupsPath+"/"+viewGroup, "", &current, true)
	if w.Code != 204 {
		t.Fatal("admin could not delete their own group", w.Code)
	}
	assertUserHTTPError(t, f.request("GET", groupsPath, "", &current, false), 401, "UNAUTHENTICATED")
	current = f.login(t, "admin", userPassword)
	if w := f.request("GET", groupsPath, "", &current, false); w.Code != 200 {
		t.Fatal("group deletion removed platform admin privileges", w.Code)
	}
}
