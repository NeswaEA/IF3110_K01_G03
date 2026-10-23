package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testWeb(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, sub := range []string{"assets", ".vite"} {
		if err := os.Mkdir(filepath.Join(dir, sub), 0700); err != nil {
			t.Fatal(err)
		}
	}
	manifest := `{` +
		`"src/pages/layout.ts": {"file": "assets/layout.js", "name": "layout", "isEntry": true},` +
		`"src/pages/login.ts": {"file": "assets/login.js", "name": "login", "isEntry": true},` +
		`"src/pages/connections.ts": {"file": "assets/connections.js", "name": "connections", "isEntry": true},` +
		`"src/pages/migrations.ts": {"file": "assets/migrations.js", "name": "migrations", "isEntry": true}}`
	for name, body := range map[string]string{".vite/manifest.json": manifest, "assets/app.js": "console.log('loaded')", ".secret": "private"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestFrontendRoutesAndIsolation(t *testing.T) {
	dir := testWeb(t)
	secret := filepath.Join(t.TempDir(), "private.txt")
	if err := os.WriteFile(secret, []byte("private"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, filepath.Join(dir, "outside.txt")); err != nil {
		t.Fatal(err)
	}
	notFound := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "missing page", http.StatusNotFound) })
	h, close, err := newFrontend(dir, notFound)
	if err != nil {
		t.Fatal(err)
	}
	defer close()
	for _, tc := range []struct {
		method, path, accept string
		status               int
		contains             string
	}{
		{"GET", "/assets/app.js", "", 200, "loaded"}, {"HEAD", "/assets/app.js", "", 200, ""},
		{"GET", "/", "text/html", 404, "missing page"}, {"GET", "/databases/example", "text/html", 404, "missing page"},
		{"GET", "/assets/missing.js", "text/html", 404, "missing page"}, {"GET", "/assets/", "text/html", 404, "missing page"},
		{"GET", "/assets", "text/html", 404, "missing page"}, {"GET", "/.vite/manifest.json", "", 404, "missing page"},
		{"GET", "/.secret", "", 404, ""}, {"GET", "/outside.txt", "", 404, ""},
		{"GET", "/../private.txt", "", 404, ""}, {"GET", "/unknown", "application/json", 404, ""},
		{"POST", "/", "text/html", 405, ""},
	} {
		r := httptest.NewRequest(tc.method, tc.path, nil)
		r.Header.Set("Accept", tc.accept)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != tc.status || !strings.Contains(w.Body.String(), tc.contains) || strings.Contains(w.Body.String(), "private") {
			t.Fatalf("%s %s returned %d", tc.method, tc.path, w.Code)
		}
		if tc.method == "HEAD" && w.Body.Len() != 0 {
			t.Fatal("HEAD returned a body")
		}
	}
	if _, _, err := newFrontend(filepath.Join(dir, "absent"), notFound); err == nil {
		t.Fatal("missing build accepted")
	}
}

func TestRuntimeConfig(t *testing.T) {
	t.Setenv("SQLITE_TARGET_DIR", t.TempDir())
	t.Setenv("WEB_DIST_DIR", "")
	t.Setenv("TARGET_CONNECT_TIMEOUT", "")
	t.Setenv("TARGET_QUERY_TIMEOUT", "")
	cfg, err := loadRuntimeConfig()
	if err != nil || cfg.WebDir != "web/dist" {
		t.Fatal("invalid defaults")
	}
	t.Setenv("TARGET_QUERY_TIMEOUT", "-1s")
	if _, err := loadRuntimeConfig(); err == nil {
		t.Fatal("negative timeout accepted")
	}
	t.Setenv("TARGET_QUERY_TIMEOUT", "1s")
	t.Setenv("SQLITE_TARGET_DIR", "relative")
	if _, err := loadRuntimeConfig(); err == nil {
		t.Fatal("relative SQLite directory accepted")
	}
}
