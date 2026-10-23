package webui_test

import (
	"html"
	"html/template"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/webui"
)

func TestLoginPage(t *testing.T) {
	renderer, err := webui.NewRenderer(webui.Assets{"login": template.HTML(`<script type="module" src="/assets/login.js"></script>`)})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	guard := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			t.Error("public login page invoked session guard")
			next.ServeHTTP(w, r)
		})
	}
	webui.RegisterRoutes(mux, webui.NewPages(renderer, nil, nil, nil, nil, nil, nil, nil), guard)
	for _, tc := range []struct{ next, want string }{
		{"", "/databases"},
		{"/databases/abc?schema=main&table=items", "/databases/abc?schema=main&table=items"},
		{"https://example.com", "/databases"},
		{"//example.com", "/databases"},
		{"/%2fexample.com", "/databases"},
		{`/databases?table=<script>alert("x")</script>`, `/databases?table=<script>alert("x")</script>`},
	} {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("GET", "/login?next="+url.QueryEscape(tc.next), nil))
		if w.Code != 200 || w.Header().Get("Content-Type") != "text/html; charset=utf-8" || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("login page returned %d %v", w.Code, w.Header())
		}
		if len(w.Result().Cookies()) != 0 {
			t.Fatal("login page issued a session cookie")
		}
		expect(t, w.Body.String(), []string{
			`data-next="` + html.EscapeString(tc.want) + `"`,
			`<label for="login-username">Username</label>`, `autocomplete="current-password"`,
			`type="submit" disabled`, `/assets/login.js`, `role="status"`,
		}, `<nav`, `data-action="logout"`, `name="csrf`, `SessionID`, `<script>alert("x")</script>`)
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("POST", "/login", strings.NewReader("username=editor&password=secret")))
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatal("login page accepted native form POST")
	}
}
