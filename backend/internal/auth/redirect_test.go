package auth_test

import (
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/auth"
)

func TestSafeNext(t *testing.T) {
	for _, value := range []string{"", "https://example.com", "//example.com", "/%2fexample.com", "/%5cexample.com", "/%252fexample.com", "/a/..//example.com", "/%2e%2e/admin", "/a%0d%0aLocation:evil", "relative"} {
		if got := auth.SafeNext(value); got != "/databases" {
			t.Errorf("SafeNext(%q) = %q", value, got)
		}
	}
	for _, value := range []string{"/admin/connections", "/databases/abc?tab=data&limit=25", "/"} {
		if got := auth.SafeNext(value); got != value {
			t.Errorf("SafeNext(%q) = %q", value, got)
		}
	}
}

func TestRedirectToLoginRejectsExternalNext(t *testing.T) {
	r := httptest.NewRequest("GET", "//example.com", nil)
	w := httptest.NewRecorder()
	auth.RedirectToLogin(w, r)
	location, err := url.Parse(w.Header().Get("Location"))
	if err != nil || w.Code != 302 || location.Path != "/login" || location.Query().Get("next") != "/databases" {
		t.Fatalf("unsafe login redirect: %d %q", w.Code, w.Header().Get("Location"))
	}
}
