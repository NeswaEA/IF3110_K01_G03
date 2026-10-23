package auth_test

import (
	"fmt"
	"html"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/auth"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/webui"
)

func TestPageAuthentication(t *testing.T) {
	f := newSessionFixture(t)
	h, err := auth.NewHTTPHandler(f.store, authOrigin, false)
	if err != nil {
		t.Fatal(err)
	}
	page := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, ok := auth.PrincipalFromContext(r.Context())
		if !ok {
			t.Error("missing principal")
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, html.EscapeString(fmt.Sprintf("%+v", webui.UserOf(p))))
	})
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	mux.Handle("GET /protected", h.RequirePage(page))
	renderer, err := webui.NewRenderer(nil)
	if err != nil {
		t.Fatal(err)
	}
	mux.HandleFunc("/", renderer.NotFound)
	request := func(path, token string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", path, nil)
		if token != "" {
			r.AddCookie(&http.Cookie{Name: "session", Value: token})
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}
	w := request("/protected?token=secret", "")
	if w.Code != 302 || w.Header().Get("Location") != "/login?next=%2Fprotected%3Ftoken%3Dsecret" || len(w.Result().Cookies()) != 0 {
		t.Fatalf("unexpected anonymous response: %d %v", w.Code, w.Header())
	}
	var count int
	if err := f.db.QueryRow("SELECT COUNT(*) FROM sessions").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("page request created anonymous session")
	}
	w = request("/api/v1/auth/me", "")
	if w.Code != 401 || !strings.HasPrefix(w.Header().Get("Content-Type"), "application/json") {
		t.Fatal("API no longer returns JSON 401")
	}
	w = request("/unknown", "")
	if w.Code != 404 || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/html") {
		t.Fatal("unknown page must return HTML 404")
	}
	prelogin := f.create(t, "")
	if w := request("/protected", prelogin.Token); w.Code != 302 {
		t.Fatal("anonymous session accessed protected page")
	}
	session := f.create(t, sessionUser)
	w = request("/protected", session.Token)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "editor") {
		t.Fatal("authenticated page unavailable")
	}
	for _, secret := range []string{session.ID, session.Token, session.CSRFToken, "SessionID"} {
		if strings.Contains(w.Body.String(), secret) {
			t.Fatal("page exposed session data")
		}
	}
	f.clock.Advance(15 * time.Minute)
	w = request("/protected", session.Token)
	if w.Code != 200 || len(w.Result().Cookies()) != 1 {
		t.Fatal("HTML request did not rotate session")
	}
	if w := request("/protected", session.Token); w.Code != 200 || len(w.Result().Cookies()) != 0 {
		t.Fatal("rotation grace failed")
	}
	f.clock.Advance(31 * time.Second)
	if w := request("/protected", session.Token); w.Code != 302 {
		t.Fatal("expired rotation token accepted")
	}
	if err := f.db.Close(); err != nil {
		t.Fatal(err)
	}
	w = request("/protected", w.Result().Cookies()[0].Value)
	if w.Code != 500 || strings.Contains(w.Body.String(), "database is closed") {
		t.Fatal("storage failure must return a safe 500")
	}
}
