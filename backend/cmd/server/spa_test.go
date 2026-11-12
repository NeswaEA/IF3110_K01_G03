package main

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testWeb(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "assets"), 0700); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{"assets/app.js": "console.log('loaded')", ".secret": "private",
		"index.html": `<!doctype html><div id="root"></div>`, "favicon.svg": "<svg/>"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestSPARoutesAndIsolation(t *testing.T) {
	dir := testWeb(t)
	secret := filepath.Join(t.TempDir(), "private.txt")
	if err := os.WriteFile(secret, []byte("private"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, filepath.Join(dir, "outside.txt")); err != nil {
		t.Fatal(err)
	}
	spa, close, err := newSPA(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer close()
	h := securityHeaders(spa)
	const immutable, revalidate = "public, max-age=31536000, immutable", "no-cache"
	for _, tc := range []struct {
		method, path, accept string
		status               int
		contains, cache      string
	}{
		{"GET", "/assets/app.js", "", 200, "loaded", immutable}, {"HEAD", "/assets/app.js", "", 200, "", immutable},
		{"GET", "/favicon.svg", "", 200, "<svg/>", revalidate},
		{"GET", "/", "text/html", 200, `id="root"`, revalidate},
		{"GET", "/databases/example?tab=data", "text/html,application/xhtml+xml", 200, `id="root"`, revalidate},
		{"HEAD", "/admin/users", "text/html", 200, "", revalidate},
		{"GET", "/assets/missing.js", "text/html", 404, "404 page not found", ""}, {"GET", "/assets/", "text/html", 404, "", ""},
		{"GET", "/assets", "text/html", 404, "", ""}, {"GET", "/healthz", "text/html", 404, "", ""},
		{"GET", "/.secret", "", 404, "", ""}, {"GET", "/outside.txt", "", 404, "", ""},
		{"GET", "/unknown", "application/json", 404, "", ""},
		{"GET", "/api/v1/unknown", "text/html", 404, `"code":"NOT_FOUND"`, ""},
		{"POST", "/", "text/html", 405, "", ""},
	} {
		r := httptest.NewRequest(tc.method, tc.path, nil)
		r.Header.Set("Accept", tc.accept)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != tc.status || !strings.Contains(w.Body.String(), tc.contains) || strings.Contains(w.Body.String(), "private") ||
			w.Header().Get("Cache-Control") != tc.cache {
			t.Fatalf("%s %s returned %d %q %q", tc.method, tc.path, w.Code, w.Header().Get("Cache-Control"), w.Body.String())
		}
		if tc.method == "HEAD" && w.Body.Len() != 0 {
			t.Fatal("HEAD returned a body")
		}
		if w.Header().Get("Content-Security-Policy") != contentSecurityPolicy || w.Header().Get("X-Content-Type-Options") != "nosniff" ||
			w.Header().Get("Referrer-Policy") != "same-origin" {
			t.Fatalf("%s %s lacks the security headers", tc.method, tc.path)
		}
	}
	if _, _, err := newSPA(filepath.Join(dir, "absent")); err == nil {
		t.Fatal("missing build accepted")
	}
	if err := os.Remove(filepath.Join(dir, "index.html")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := newSPA(dir); err == nil {
		t.Fatal("build without index.html accepted")
	}
}

func TestRuntimeConfig(t *testing.T) {
	t.Setenv("SQLITE_TARGET_DIR", t.TempDir())
	t.Setenv("WEB_DIST_DIR", "")
	t.Setenv("TARGET_CONNECT_TIMEOUT", "")
	t.Setenv("TARGET_QUERY_TIMEOUT", "")
	t.Setenv("DIFF_TIMEOUT", "")
	t.Setenv("ATLAS_BIN", "/custom/atlas")
	t.Setenv("NATS_URL", "")
	t.Setenv("OUTBOX_POLL_INTERVAL", "")
	t.Setenv("SSE_HEARTBEAT_INTERVAL", "")
	t.Setenv("BATCH_SIZE_MAX", "")
	if _, err := loadRuntimeConfig(); err == nil {
		t.Fatal("missing NATS_URL accepted")
	}
	t.Setenv("NATS_URL", "nats://localhost:4222")
	cfg, err := loadRuntimeConfig()
	if err != nil || cfg.WebDir != "web/dist" || cfg.DiffTimeout != 30*time.Second || cfg.AtlasBin != "/custom/atlas" {
		t.Fatal("invalid defaults")
	}
	t.Setenv("DIFF_TIMEOUT", "0s")
	if _, err := loadRuntimeConfig(); err == nil {
		t.Fatal("invalid diff timeout accepted")
	}
	t.Setenv("DIFF_TIMEOUT", "4s")
	if cfg, err := loadRuntimeConfig(); err != nil || cfg.DiffTimeout != 4*time.Second {
		t.Fatal("diff timeout not applied", err)
	}
	t.Setenv("OUTBOX_POLL_INTERVAL", "250ms")
	t.Setenv("SSE_HEARTBEAT_INTERVAL", "5s")
	if cfg, err := loadRuntimeConfig(); err != nil || cfg.OutboxPollInterval != 250*time.Millisecond || cfg.SSEHeartbeat != 5*time.Second || cfg.NATSURL != "nats://localhost:4222" {
		t.Fatal("relay and SSE settings not applied", err)
	}
	t.Setenv("BATCH_SIZE_MAX", "0")
	if _, err := loadRuntimeConfig(); err == nil {
		t.Fatal("invalid maximum batch size accepted")
	}
	t.Setenv("BATCH_SIZE_MAX", "2500")
	if cfg, err := loadRuntimeConfig(); err != nil || cfg.BatchSizeMax != 2500 {
		t.Fatal("maximum batch size not applied", err)
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
