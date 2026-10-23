package access_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/access"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/auth"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/platform"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/testkit"
)

const usersPath = "/api/v1/admin/users"
const usersOrigin = "http://localhost:8080"

type userHTTPFixture struct {
	userFixture
	handler http.Handler
	admin   auth.Session
	viewer  auth.Session
}

func newUserHTTPFixture(t *testing.T) userHTTPFixture {
	t.Helper()
	f := newUserFixture(t)
	hash, err := (auth.PasswordHasher{}).Hash(t.Context(), userPassword)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec(`UPDATE users SET password_hash=$1`, hash); err != nil {
		t.Fatal(err)
	}
	authHandler, err := auth.NewHTTPHandler(f.sessions, usersOrigin, false)
	if err != nil {
		t.Fatal(err)
	}
	protected := http.NewServeMux()
	access.RegisterUserRoutes(protected, access.NewUserHandler(f.users, auth.PrincipalFromContext))
	access.RegisterGroupRoutes(protected, access.NewGroupHandler(groupService(f), auth.PrincipalFromContext))
	mux := http.NewServeMux()
	authHandler.RegisterRoutes(mux)
	mux.Handle("/api/", authHandler.RequireSession(authHandler.RequireCSRF(protected)))
	return userHTTPFixture{
		userFixture: f, handler: platform.RequestID(mux),
		admin:  createSession(t, f.db, f.sessions, testkit.UserAdmin),
		viewer: createSession(t, f.db, f.sessions, testkit.UserView),
	}
}

func (f userHTTPFixture) request(method, path, body string, session *auth.Session, csrf bool, extra ...map[string]string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Origin", usersOrigin)
	if session != nil {
		r.AddCookie(&http.Cookie{Name: "session", Value: session.Token})
		if csrf {
			r.Header.Set("X-CSRF-Token", session.CSRFToken)
		}
	}
	for _, headers := range extra {
		for name, value := range headers {
			r.Header.Set(name, value)
		}
	}
	w := httptest.NewRecorder()
	f.handler.ServeHTTP(w, r)
	return w
}

func assertUserHTTPError(t *testing.T, w *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	var payload struct {
		Error struct {
			Code      string            `json:"code"`
			Fields    map[string]string `json:"fields"`
			RequestID string            `json:"request_id"`
		} `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil || w.Code != status || payload.Error.Code != code ||
		payload.Error.Fields == nil || payload.Error.RequestID == "" || payload.Error.RequestID != w.Header().Get("X-Request-ID") {
		t.Fatalf("expected %d %s, got %d %s", status, code, w.Code, w.Body.String())
	}
	for _, secret := range []string{userPassword, "$argon2id$", "csrf_secret", "SessionID", "database is closed"} {
		if strings.Contains(w.Body.String(), secret) {
			t.Fatalf("error response exposed %q", secret)
		}
	}
}

func (f userHTTPFixture) loginRequest(t *testing.T, username, password string) *httptest.ResponseRecorder {
	t.Helper()
	w := f.request("GET", "/api/v1/auth/csrf", "", nil, false)
	var payload struct {
		Data struct {
			Token string `json:"csrf_token"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil || w.Code != 200 || len(w.Result().Cookies()) != 1 {
		t.Fatal("could not obtain pre-login CSRF", err)
	}
	prelogin := auth.Session{Token: w.Result().Cookies()[0].Value, CSRFToken: payload.Data.Token}
	body, err := json.Marshal(map[string]string{"username": username, "password": password})
	if err != nil {
		t.Fatal(err)
	}
	return f.request("POST", "/api/v1/auth/login", string(body), &prelogin, true)
}

func (f userHTTPFixture) login(t *testing.T, username, password string) auth.Session {
	t.Helper()
	w := f.loginRequest(t, username, password)
	if w.Code != 200 || len(w.Result().Cookies()) != 1 {
		t.Fatal("login failed", w.Code, w.Body.String())
	}
	token := w.Result().Cookies()[0].Value
	session, err := f.sessions.Resolve(t.Context(), token)
	if err != nil {
		t.Fatal(err)
	}
	session.Token = token
	return session
}

func TestUserHTTPAuthorizationAndCSRF(t *testing.T) {
	f := newUserHTTPFixture(t)
	for _, route := range []struct{ method, path, body string }{
		{"GET", usersPath, ""},
		{"POST", usersPath, `{"username":"blocked","password":"` + userPassword + `"}`},
		{"PATCH", usersPath + "/" + testkit.UserEdit, `{"active":false}`},
		{"PUT", usersPath + "/" + testkit.UserEdit + "/password", `{"password":"` + userPassword + `"}`},
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
	var count int
	if err := f.db.QueryRow(`SELECT count(*) FROM users`).Scan(&count); err != nil || count != 4 {
		t.Fatal("denied requests changed user records", err)
	}
	if _, err := f.db.Exec(`UPDATE users SET is_platform_admin=false WHERE id=$1`, testkit.UserAdmin); err != nil {
		t.Fatal(err)
	}
	assertUserHTTPError(t, f.request("GET", usersPath, "", &f.admin, false), 403, "FORBIDDEN")
	if _, err := f.db.Exec(`UPDATE users SET active=false WHERE id=$1`, testkit.UserAdmin); err != nil {
		t.Fatal(err)
	}
	assertUserHTTPError(t, f.request("GET", usersPath, "", &f.admin, false), 401, "UNAUTHENTICATED")
}

func TestUserHTTPListAndCreate(t *testing.T) {
	f := newUserHTTPFixture(t)
	w := f.request("GET", usersPath, "", &f.admin, false)
	var list struct {
		Data []access.User `json:"data"`
		Page platform.Page `json:"page"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil || w.Code != 200 || len(list.Data) != 4 ||
		list.Page.Limit != 50 || list.Page.Offset != 0 || list.Page.HasMore || list.Data[0].Username != "admin" {
		t.Fatal("incorrect default user list", err, w.Body.String())
	}
	w = f.request("POST", usersPath, `{"username":" Api.User ","password":"`+userPassword+`"}`, &f.admin, true)
	var result struct {
		Data access.User `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil || w.Code != 201 || result.Data.Username != "api.user" || !result.Data.Active || result.Data.IsPlatformAdmin {
		t.Fatal("incorrect created user", err, w.Body.String())
	}
	var raw struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &raw); err != nil || len(raw.Data) != 6 || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("incorrect user DTO", err)
	}
	for _, name := range []string{"id", "username", "active", "is_platform_admin", "created_at", "updated_at"} {
		if _, ok := raw.Data[name]; !ok {
			t.Fatalf("missing user field %s", name)
		}
	}
	if !strings.HasSuffix(raw.Data["created_at"].(string), "Z") || !strings.HasSuffix(raw.Data["updated_at"].(string), "Z") {
		t.Fatal("user timestamps must be UTC")
	}
	assertUserHTTPError(t, f.request("POST", usersPath, `{"username":"API.USER","password":"`+userPassword+`"}`, &f.admin, true), 409, "ALREADY_EXISTS")
	w = f.request("GET", usersPath+"?limit=2&offset=1", "", &f.admin, false)
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil || w.Code != 200 || len(list.Data) != 2 || list.Page.Limit != 2 || list.Page.Offset != 1 || !list.Page.HasMore || list.Data[0].Username != "api.user" {
		t.Fatal("incorrect paginated user list", err)
	}
	w = f.request("GET", usersPath+"?offset=100000", "", &f.admin, false)
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil || w.Code != 200 || list.Data == nil || len(list.Data) != 0 || list.Page.HasMore {
		t.Fatal("incorrect empty page", err)
	}
	f.login(t, result.Data.Username, userPassword)
}

func TestUserHTTPValidation(t *testing.T) {
	f := newUserHTTPFixture(t)
	for _, tc := range []struct {
		method, path, body string
		status             int
		code               string
	}{
		{"GET", usersPath + "?limit=0", "", 400, "INVALID_REQUEST"},
		{"GET", usersPath + "?limit=201", "", 400, "INVALID_REQUEST"},
		{"GET", usersPath + "?offset=-1", "", 400, "INVALID_REQUEST"},
		{"GET", usersPath + "?offset=100001", "", 400, "INVALID_REQUEST"},
		{"GET", usersPath + "?limit=1.5", "", 400, "INVALID_REQUEST"},
		{"GET", usersPath + "?offset=999999999999999999999", "", 400, "INVALID_REQUEST"},
		{"POST", usersPath, `{"username":"ab","password":"` + userPassword + `"}`, 400, "INVALID_REQUEST"},
		{"POST", usersPath, `{"username":"valid","password":"short"}`, 400, "INVALID_REQUEST"},
		{"POST", usersPath, `{"username":"valid","password":"` + userPassword + `","active":false}`, 400, "INVALID_REQUEST"},
		{"POST", usersPath, `{"username":"valid","password":"` + userPassword + `","is_platform_admin":null}`, 400, "INVALID_REQUEST"},
		{"POST", usersPath, `{"username":"valid","password":"` + userPassword + `","is_platform_admin":"true"}`, 400, "INVALID_REQUEST"},
		{"POST", usersPath, `{"username":"valid","password":"` + strings.Repeat("x", 1<<20) + `"}`, 413, "PAYLOAD_TOO_LARGE"},
		{"POST", usersPath, `{} {}`, 400, "INVALID_REQUEST"},
		{"PATCH", usersPath + "/" + testkit.UserEdit, `{}`, 400, "INVALID_REQUEST"},
		{"PATCH", usersPath + "/" + testkit.UserEdit, `{"active":null}`, 400, "INVALID_REQUEST"},
		{"PATCH", usersPath + "/" + testkit.UserEdit, `{"active":true,"is_platform_admin":null}`, 400, "INVALID_REQUEST"},
		{"PATCH", usersPath + "/" + testkit.UserEdit, `{"active":"false"}`, 400, "INVALID_REQUEST"},
		{"PATCH", usersPath + "/" + testkit.UserEdit, `{"username":"renamed"}`, 400, "INVALID_REQUEST"},
		{"PATCH", usersPath + "/not-a-uuid", `{"active":false}`, 400, "INVALID_REQUEST"},
		{"PATCH", usersPath + "/" + missingUser, `{"active":false}`, 404, "NOT_FOUND"},
		{"PUT", usersPath + "/not-a-uuid/password", `{"password":"` + userPassword + `"}`, 400, "INVALID_REQUEST"},
		{"PUT", usersPath + "/" + missingUser + "/password", `{"password":"` + userPassword + `"}`, 404, "NOT_FOUND"},
		{"PUT", usersPath + "/" + testkit.UserEdit + "/password", `{"password":"short"}`, 400, "INVALID_REQUEST"},
		{"PATCH", usersPath + "/" + testkit.UserAdmin, `{"active":false}`, 400, "INVALID_REQUEST"},
		{"PATCH", usersPath + "/" + testkit.UserAdmin, `{"is_platform_admin":false}`, 400, "INVALID_REQUEST"},
	} {
		w := f.request(tc.method, tc.path, tc.body, &f.admin, true)
		assertUserHTTPError(t, w, tc.status, tc.code)
	}
	assertUserHTTPError(t, f.request("POST", usersPath, `{}`, &f.admin, true, map[string]string{"Content-Type": "text/plain"}), 400, "INVALID_REQUEST")
}

func TestUserHTTPResetPromotionAndDeactivation(t *testing.T) {
	f := newUserHTTPFixture(t)
	user, err := f.users.Create(t.Context(), adminPrincipal, access.CreateUserInput{Username: "managed", Password: userPassword})
	if err != nil {
		t.Fatal(err)
	}
	first := f.login(t, user.Username, userPassword)
	second := f.login(t, user.Username, userPassword)
	const newPassword = "replacement password 456"
	f.clock.Advance(time.Minute)
	w := f.request("PUT", usersPath+"/"+user.ID+"/password", `{"password":"`+newPassword+`"}`, &f.admin, true)
	if w.Code != 204 || w.Body.Len() != 0 || w.Header().Get("Content-Type") != "" {
		t.Fatal("password reset must return an empty 204", w.Code, w.Body.String())
	}
	for _, session := range []*auth.Session{&first, &second} {
		assertUserHTTPError(t, f.request("GET", "/api/v1/auth/me", "", session, false), 401, "UNAUTHENTICATED")
	}
	if w := f.request("GET", "/api/v1/auth/me", "", &f.viewer, false); w.Code != 200 {
		t.Fatal("reset revoked an unrelated user's session")
	}
	assertUserHTTPError(t, f.loginRequest(t, user.Username, userPassword), 401, "UNAUTHENTICATED")
	current := f.login(t, user.Username, newPassword)
	w = f.request("PATCH", usersPath+"/"+user.ID, `{"is_platform_admin":true}`, &f.admin, true)
	if w.Code != 200 {
		t.Fatal("promotion failed", w.Body.String())
	}
	w = f.request("GET", usersPath, "", &current, false)
	if w.Code != 200 || len(w.Result().Cookies()) != 1 || w.Result().Cookies()[0].Value == current.Token {
		t.Fatal("promoted user's admin request did not rotate the token", w.Code)
	}
	current.Token = w.Result().Cookies()[0].Value
	w = f.request("PATCH", usersPath+"/"+user.ID, `{"is_platform_admin":false}`, &current, true)
	if w.Code != 200 {
		t.Fatal("self-demotion with another admin should succeed", w.Code)
	}
	assertUserHTTPError(t, f.request("GET", usersPath, "", &current, false), 401, "UNAUTHENTICATED")
	current = f.login(t, user.Username, newPassword)
	w = f.request("PATCH", usersPath+"/"+user.ID, `{"active":false}`, &f.admin, true)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"active":false`) {
		t.Fatal("deactivation failed", w.Code, w.Body.String())
	}
	assertUserHTTPError(t, f.request("GET", "/api/v1/auth/me", "", &current, false), 401, "UNAUTHENTICATED")
	assertUserHTTPError(t, f.loginRequest(t, user.Username, newPassword), 401, "UNAUTHENTICATED")
}

func TestUserHTTPAdminCanResetOwnPassword(t *testing.T) {
	f := newUserHTTPFixture(t)
	const password = "new admin password 456"
	w := f.request("PUT", usersPath+"/"+testkit.UserAdmin+"/password", `{"password":"`+password+`"}`, &f.admin, true)
	if w.Code != 204 {
		t.Fatal("last admin should be able to reset their password", w.Code)
	}
	assertUserHTTPError(t, f.request("GET", usersPath, "", &f.admin, false), 401, "UNAUTHENTICATED")
	current := f.login(t, "admin", password)
	if w := f.request("GET", usersPath, "", &current, false); w.Code != 200 {
		t.Fatal("admin could not log in after resetting their password", w.Code)
	}
	if err := f.db.Close(); err != nil {
		t.Fatal(err)
	}
	assertUserHTTPError(t, f.request("GET", usersPath, "", &current, false), 500, "INTERNAL_ERROR")
}
