package auth_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/auth"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/platform"
)

const authOrigin = "http://localhost:8080"
const authPassword = "example password 123"

type authHTTPFixture struct {
	sessionFixture
	handler http.Handler
}

func newAuthHTTPFixture(t *testing.T) authHTTPFixture {
	t.Helper()
	f := newSessionFixture(t)
	hash, err := (auth.PasswordHasher{}).Hash(t.Context(), authPassword)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec(`UPDATE users SET password_hash=$1`, hash); err != nil {
		t.Fatal(err)
	}
	h, err := auth.NewHTTPHandler(f.store, authOrigin, false)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	mux.Handle("POST /api/v1/protected", h.RequireSession(h.RequireCSRF(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, ok := auth.PrincipalFromContext(r.Context())
		if !ok || p.UserID != sessionUser {
			t.Error("middleware did not provide the authenticated principal")
		}
		w.WriteHeader(http.StatusNoContent)
	}))))
	return authHTTPFixture{sessionFixture: f, handler: platform.RequestID(mux)}
}

func (f authHTTPFixture) request(method, path string, cookie *http.Cookie, csrf, origin, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if cookie != nil {
		r.AddCookie(cookie)
	}
	if csrf != "" {
		r.Header.Set("X-CSRF-Token", csrf)
	}
	if origin != "" {
		r.Header.Set("Origin", origin)
	}
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	f.handler.ServeHTTP(w, r)
	return w
}

func (f authHTTPFixture) csrf(t *testing.T, cookie *http.Cookie) (*http.Cookie, string) {
	t.Helper()
	w := f.request("GET", "/api/v1/auth/csrf", cookie, "", "", "")
	if w.Code != http.StatusOK || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("CSRF endpoint returned %d", w.Code)
	}
	var result struct {
		Data struct {
			Token string `json:"csrf_token"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil || len(result.Data.Token) != 43 {
		t.Fatal("invalid CSRF response")
	}
	if cookies := w.Result().Cookies(); len(cookies) != 0 {
		if len(cookies) != 1 {
			t.Fatal("multiple cookies issued")
		}
		cookie = cookies[0]
	}
	return cookie, result.Data.Token
}

func (f authHTTPFixture) login(t *testing.T) (*http.Cookie, string) {
	t.Helper()
	prelogin, csrf := f.csrf(t, nil)
	w := f.request("POST", "/api/v1/auth/login", prelogin, csrf, authOrigin, `{"username":" Editor ","password":"`+authPassword+`"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("login returned %d", w.Code)
	}
	cookies := w.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Value == prelogin.Value {
		t.Fatal("login did not replace the cookie")
	}
	var body struct {
		Data struct {
			User map[string]any `json:"user"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || len(body.Data.User) != 3 || body.Data.User["id"] != sessionUser || body.Data.User["username"] != "editor" || body.Data.User["is_platform_admin"] != false {
		t.Fatal("incorrect login response")
	}
	for _, secret := range []string{prelogin.Value, cookies[0].Value, csrf, authPassword} {
		if strings.Contains(w.Body.String(), secret) {
			t.Fatal("login exposed a secret")
		}
	}
	if w := f.request("GET", "/api/v1/auth/me", prelogin, "", "", ""); w.Code != http.StatusUnauthorized {
		t.Fatal("prelogin token obtained authenticated access")
	}
	cookie, nextCSRF := f.csrf(t, cookies[0])
	if csrf == nextCSRF {
		t.Fatal("login reused the CSRF secret")
	}
	return cookie, nextCSRF
}

func assertHTTPError(t *testing.T, w *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	var body struct {
		Error struct {
			Code      string            `json:"code"`
			RequestID string            `json:"request_id"`
			Fields    map[string]string `json:"fields"`
		} `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || w.Code != status || body.Error.Code != code ||
		body.Error.RequestID == "" || body.Error.RequestID != w.Header().Get("X-Request-ID") || body.Error.Fields == nil {
		t.Fatalf("expected %d %s, got %d", status, code, w.Code)
	}
}

func TestAuthHTTPLifecycle(t *testing.T) {
	f := newAuthHTTPFixture(t)
	if _, ok := auth.PrincipalFromContext(t.Context()); ok {
		t.Fatal("missing context produced a principal")
	}
	assertHTTPError(t, f.request("GET", "/api/v1/auth/me", nil, "", "", ""), 401, "UNAUTHENTICATED")
	prelogin, preCSRF := f.csrf(t, nil)
	if prelogin.Name != "session" || prelogin.Path != "/" || !prelogin.HttpOnly || prelogin.Secure || prelogin.SameSite != http.SameSiteLaxMode || prelogin.Domain != "" {
		t.Fatal("incorrect local cookie attributes")
	}
	_, sameCSRF := f.csrf(t, prelogin)
	if sameCSRF != preCSRF {
		t.Fatal("CSRF changed within a session")
	}
	assertHTTPError(t, f.request("POST", "/api/v1/protected", prelogin, preCSRF, authOrigin, "{}"), 401, "UNAUTHENTICATED")
	cookie, csrf := f.login(t)
	w := f.request("GET", "/api/v1/auth/me", cookie, "", "", "")
	var me struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &me); err != nil || w.Code != 200 || len(me.Data) != 3 || me.Data["id"] != sessionUser || len(w.Result().Cookies()) != 0 {
		t.Fatal("incorrect current user response")
	}
	if w := f.request("POST", "/api/v1/protected", cookie, csrf, authOrigin, "{}"); w.Code != 204 {
		t.Fatal("valid protected request rejected")
	}
	assertHTTPError(t, f.request("POST", "/api/v1/auth/logout", cookie, preCSRF, authOrigin, "{}"), 403, "CSRF_REJECTED")
	w = f.request("POST", "/api/v1/auth/logout", cookie, csrf, authOrigin, "{}")
	deleted := w.Result().Cookies()
	if w.Code != 204 || w.Body.Len() != 0 || len(deleted) != 1 || deleted[0].MaxAge != -1 || deleted[0].Value != "" || !deleted[0].HttpOnly || deleted[0].Path != "/" {
		t.Fatal("logout did not clear the cookie")
	}
	assertHTTPError(t, f.request("GET", "/api/v1/auth/me", cookie, "", "", ""), 401, "UNAUTHENTICATED")
}

func TestAuthHTTPCSRFRejections(t *testing.T) {
	f := newAuthHTTPFixture(t)
	cookie, csrf := f.login(t)
	_, foreignCSRF := f.csrf(t, nil)
	for _, tc := range []struct{ name, origin, token string }{
		{"missing origin", "", csrf}, {"foreign origin", "https://other.example", csrf},
		{"null origin", "null", csrf}, {"origin path", authOrigin + "/", csrf},
		{"missing token", authOrigin, ""}, {"invalid token", authOrigin, "wrong"},
		{"different session", authOrigin, foreignCSRF},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assertHTTPError(t, f.request("POST", "/api/v1/protected", cookie, tc.token, tc.origin, "{}"), 403, "CSRF_REJECTED")
		})
	}
	for _, header := range []string{"Origin", "X-CSRF-Token"} {
		r := httptest.NewRequest("POST", "/api/v1/protected", strings.NewReader("{}"))
		r.AddCookie(cookie)
		r.Header.Set("Origin", authOrigin)
		r.Header.Set("X-CSRF-Token", csrf)
		r.Header.Add(header, r.Header.Get(header))
		w := httptest.NewRecorder()
		f.handler.ServeHTTP(w, r)
		assertHTTPError(t, w, 403, "CSRF_REJECTED")
	}
	assertHTTPError(t, f.request("POST", "/api/v1/auth/login", nil, csrf, authOrigin, "{}"), 403, "CSRF_REJECTED")
}

func TestAuthHTTPInvalidCredentialsAndBodies(t *testing.T) {
	f := newAuthHTTPFixture(t)
	cookie, csrf := f.csrf(t, nil)
	if _, err := f.db.Exec(`UPDATE users SET active=false WHERE id=$1`, otherSessionUser); err != nil {
		t.Fatal(err)
	}
	var denied string
	for _, body := range []string{
		`{"username":"missing","password":"` + authPassword + `"}`,
		`{"username":"editor","password":"wrong password"}`,
		`{"username":"viewer","password":"` + authPassword + `"}`,
		`{"username":"?","password":"` + authPassword + `"}`,
		`{"username":"editor","password":""}`,
	} {
		w := f.request("POST", "/api/v1/auth/login", cookie, csrf, authOrigin, body)
		assertHTTPError(t, w, 401, "UNAUTHENTICATED")
		var envelope map[string]map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &envelope); err != nil {
			t.Fatal(err)
		}
		delete(envelope["error"], "request_id")
		data, err := json.Marshal(envelope)
		if err != nil {
			t.Fatal(err)
		}
		if denied != "" && denied != string(data) {
			t.Fatal("credential errors reveal account status")
		}
		denied = string(data)
		if len(w.Result().Cookies()) != 0 {
			t.Fatal("failed login issued a cookie")
		}
	}
	for _, body := range []string{`null`, `{`, `{"username":"editor","password":"hidden","extra":true}`, `{} {}`} {
		assertHTTPError(t, f.request("POST", "/api/v1/auth/login", cookie, csrf, authOrigin, body), 400, "INVALID_REQUEST")
	}
	r := httptest.NewRequest("POST", "/api/v1/auth/login", strings.NewReader(`{}`))
	r.AddCookie(cookie)
	r.Header.Set("Origin", authOrigin)
	r.Header.Set("X-CSRF-Token", csrf)
	r.Header.Set("Content-Type", "text/plain")
	w := httptest.NewRecorder()
	f.handler.ServeHTTP(w, r)
	assertHTTPError(t, w, 400, "INVALID_REQUEST")
	assertHTTPError(t, f.request("POST", "/api/v1/auth/login", cookie, csrf, authOrigin, `{"password":"`+strings.Repeat("x", 1<<20)+`"}`), 413, "PAYLOAD_TOO_LARGE")
}

func TestAuthHTTPRotationExpiryAndRevocation(t *testing.T) {
	f := newAuthHTTPFixture(t)
	cookie, csrf := f.login(t)
	f.clock.Advance(15 * time.Minute)
	w := f.request("GET", "/api/v1/auth/me", cookie, "", "", "")
	cookies := w.Result().Cookies()
	if w.Code != 200 || len(cookies) != 1 || cookies[0].Value == cookie.Value || !cookies[0].Expires.Equal(cookie.Expires) {
		t.Fatal("HTTP rotation did not preserve the deadline")
	}
	rotated := cookies[0]
	w = f.request("GET", "/api/v1/auth/me", cookie, "", "", "")
	if w.Code != 200 || len(w.Result().Cookies()) != 0 {
		t.Fatal("old token was rejected during grace or reissued")
	}
	f.clock.Advance(30 * time.Second)
	w = f.request("GET", "/api/v1/auth/me", cookie, "", "", "")
	assertHTTPError(t, w, 401, "UNAUTHENTICATED")
	if len(w.Result().Cookies()) != 0 {
		t.Fatal("stale response could overwrite a rotated cookie")
	}
	_, nextCSRF := f.csrf(t, rotated)
	if nextCSRF != csrf {
		t.Fatal("rotation changed CSRF")
	}
	if _, err := f.db.Exec(`UPDATE users SET active=false WHERE id=$1`, sessionUser); err != nil {
		t.Fatal(err)
	}
	assertHTTPError(t, f.request("GET", "/api/v1/auth/me", rotated, "", "", ""), 401, "UNAUTHENTICATED")
	if _, err := f.db.Exec(`UPDATE users SET active=true WHERE id=$1`, sessionUser); err != nil {
		t.Fatal(err)
	}
	f.clock.Advance(30 * time.Minute)
	assertHTTPError(t, f.request("GET", "/api/v1/auth/me", rotated, "", "", ""), 401, "UNAUTHENTICATED")
}

func TestAuthHTTPConcurrentLoginConsumesPreloginOnce(t *testing.T) {
	f := newAuthHTTPFixture(t)
	cookie, csrf := f.csrf(t, nil)
	results := make(chan int, 2)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Go(func() {
			<-start
			w := f.request("POST", "/api/v1/auth/login", cookie, csrf, authOrigin, `{"username":"editor","password":"`+authPassword+`"}`)
			results <- w.Code
		})
	}
	close(start)
	wg.Wait()
	close(results)
	success := 0
	for status := range results {
		if status == 200 {
			success++
		} else if status != 401 && status != 403 {
			t.Fatalf("unexpected login status %d", status)
		}
	}
	var count int
	if err := f.db.QueryRow(`SELECT count(*) FROM sessions WHERE user_id=$1 AND revoked_at IS NULL`, sessionUser).Scan(&count); err != nil || count != 1 || success != 1 {
		t.Fatal("concurrent login created multiple authenticated sessions")
	}
}

func TestAuthHTTPSecureCookiesAndStorageFailure(t *testing.T) {
	f := newAuthHTTPFixture(t)
	for _, origin := range []string{"http://localhost:8080", "https://example.org"} {
		h, err := auth.NewHTTPHandler(f.store, origin, origin == "http://localhost:8080")
		if err != nil {
			t.Fatal(err)
		}
		mux := http.NewServeMux()
		h.RegisterRoutes(mux)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/auth/csrf", nil))
		cookies := w.Result().Cookies()
		if w.Code != 200 || len(cookies) != 1 || !cookies[0].Secure {
			t.Fatal("secure cookie flag missing")
		}
	}
	if err := f.db.Close(); err != nil {
		t.Fatal(err)
	}
	w := f.request("GET", "/api/v1/auth/csrf", nil, "", "", "")
	assertHTTPError(t, w, 500, "INTERNAL_ERROR")
	if len(w.Result().Cookies()) != 0 || strings.Contains(w.Body.String(), "database is closed") {
		t.Fatal("storage failure leaked details or issued a cookie")
	}
}
