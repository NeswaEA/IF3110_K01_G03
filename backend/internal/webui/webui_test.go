package webui_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/contracts"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/webui"
)

const manifest = `{
  "_shared.js": {"file": "assets/shared-1.js", "css": ["assets/shared-1.css"]},
  "src/pages/layout.ts": {"file": "assets/layout-1.js", "name": "layout", "isEntry": true, "imports": ["_shared.js"]},
  "src/pages/databases.ts": {"file": "assets/databases-1.js", "name": "databases", "isEntry": true, "imports": ["_shared.js"], "css": ["assets/databases-1.css"]},
  "src/pages/database.ts": {"file": "assets/database-1.js", "name": "database", "isEntry": true},
  "src/pages/connections.ts": {"file": "assets/connections-1.js", "name": "connections", "isEntry": true}
}`

func writeManifest(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, ".vite"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".vite", "manifest.json"), []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestAssetsResolveEntriesFromManifest(t *testing.T) {
	assets, err := webui.LoadAssets(writeManifest(t, manifest))
	if err != nil {
		t.Fatal(err)
	}
	tags, err := assets.Tags("databases")
	want := `<link rel="stylesheet" href="/assets/shared-1.css"><link rel="stylesheet" href="/assets/databases-1.css"><script type="module" src="/assets/databases-1.js"></script>`
	if err != nil || string(tags) != want {
		t.Fatalf("databases tags: %q %v", tags, err)
	}
	if tags, err := assets.Tags(""); err != nil || tags != "" {
		t.Fatal("empty entry produced tags")
	}
	if _, err := assets.Tags("missing"); err == nil {
		t.Fatal("unknown entry accepted")
	}
	if _, err := webui.LoadAssets(t.TempDir()); err == nil {
		t.Fatal("missing manifest accepted")
	}
	if _, err := webui.LoadAssets(writeManifest(t, "{")); err == nil {
		t.Fatal("invalid manifest accepted")
	}
}

type fakeAccess struct{ err error }

func (f fakeAccess) Require(context.Context, contracts.Principal, contracts.Action, string) error {
	return f.err
}

func (fakeAccess) Effective(context.Context, string, string) (contracts.AccessLevel, error) {
	return contracts.AccessNone, nil
}

const databaseID = "30000000-0000-4000-8000-000000000001"

type pageCase struct {
	name      string
	principal *contracts.Principal
	access    error
	path      string
	status    int
	contains  []string
	excludes  []string
}

func TestPagesRenderWithPolicy(t *testing.T) {
	assets, err := webui.LoadAssets(writeManifest(t, manifest))
	if err != nil {
		t.Fatal(err)
	}
	renderer, err := webui.NewRenderer(assets)
	if err != nil {
		t.Fatal(err)
	}
	admin := &contracts.Principal{UserID: "u1", Username: "admin", IsPlatformAdmin: true, SessionID: "secret-session-id"}
	viewer := &contracts.Principal{UserID: "u2", Username: `<script>alert(1)</script>`, SessionID: "secret-session-id"}
	forbidden := &contracts.DomainError{Code: "FORBIDDEN", Message: "x"}
	notFound := &contracts.DomainError{Code: "NOT_FOUND", Message: "x"}
	for _, tc := range []pageCase{
		{"admin list", admin, nil, "/databases", 200,
			[]string{"<title>Database", `href="/databases" aria-current="page"`, `href="/admin/connections"`, "/assets/layout-1.js", "/assets/databases-1.js", `data-widget="database-list"`},
			[]string{"secret-session-id"}},
		{"viewer list escapes the username and hides admin links", viewer, nil, "/databases", 200,
			[]string{"&lt;script&gt;alert(1)&lt;/script&gt;"},
			[]string{"<script>alert(1)</script>", `href="/admin/connections"`, "secret-session-id"}},
		{"root redirects", viewer, nil, "/", 302, nil, nil},
		{"database page", viewer, nil, "/databases/" + databaseID, 200,
			[]string{`data-database-id="` + databaseID + `"`, "/assets/database-1.js"}, nil},
		{"database without grant", viewer, notFound, "/databases/" + databaseID, 404, []string{"Halaman tidak ditemukan"}, []string{"data-widget"}},
		{"malformed database id", viewer, nil, "/databases/not-a-uuid", 404, []string{"Halaman tidak ditemukan"}, nil},
		{"connections for admin", admin, nil, "/admin/connections", 200,
			[]string{`href="/admin/connections" aria-current="page"`, `data-widget="connections"`, "/assets/connections-1.js"}, nil},
		{"connections for non-admin", viewer, forbidden, "/admin/connections", 403, []string{"Akses ditolak"}, []string{`data-widget="connections"`}},
		{"policy failure", viewer, errors.New("database down"), "/databases/" + databaseID, 500, []string{"Terjadi kesalahan"}, []string{"database down"}},
		{"deactivated user", viewer, &contracts.DomainError{Code: "UNAUTHENTICATED"}, "/admin/connections", 302, nil, nil},
		{"guard rejected", nil, nil, "/databases", 302, nil, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			principal := func(context.Context) (contracts.Principal, bool) {
				if tc.principal == nil {
					return contracts.Principal{}, false
				}
				return *tc.principal, true
			}
			login := func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/login", http.StatusFound) }
			guarded := 0
			guard := func(next http.Handler) http.Handler {
				return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { guarded++; next.ServeHTTP(w, r) })
			}
			mux := http.NewServeMux()
			webui.RegisterRoutes(mux, webui.NewPages(renderer, fakeAccess{tc.access}, principal, login), guard)
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, httptest.NewRequest("GET", tc.path, nil))
			body := w.Body.String()
			if w.Code != tc.status || guarded != 1 {
				t.Fatalf("%s returned %d (guard ran %d times): %s", tc.path, w.Code, guarded, body)
			}
			if w.Code != 302 && w.Header().Get("Content-Type") != "text/html; charset=utf-8" {
				t.Fatal("page is not HTML")
			}
			for _, s := range tc.contains {
				if !strings.Contains(body, s) {
					t.Errorf("missing %q", s)
				}
			}
			for _, s := range tc.excludes {
				if strings.Contains(body, s) {
					t.Errorf("unexpected %q", s)
				}
			}
		})
	}
}

func TestStatusPagesAndMissingEntry(t *testing.T) {
	assets, err := webui.LoadAssets(writeManifest(t, `{"src/pages/layout.ts": {"file": "assets/layout-1.js", "name": "layout", "isEntry": true}}`))
	if err != nil {
		t.Fatal(err)
	}
	renderer, err := webui.NewRenderer(assets)
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	renderer.NotFound(w, httptest.NewRequest("GET", "/missing", nil))
	if w.Code != 404 || !strings.Contains(w.Body.String(), "Halaman tidak ditemukan") || strings.Contains(w.Body.String(), "<nav") {
		t.Fatalf("anonymous 404 page: %d %s", w.Code, w.Body.String())
	}
	// A page whose entry is absent from the build fails as a whole instead of rendering without its script.
	w = httptest.NewRecorder()
	renderer.Render(w, 200, "viewer/databases", webui.Page{Title: "Database", User: &webui.PageUser{Username: "u"}, Entry: "databases"})
	if w.Code != 500 || strings.Contains(w.Body.String(), "<html") {
		t.Fatalf("missing entry rendered: %d", w.Code)
	}
	w = httptest.NewRecorder()
	renderer.Render(w, 200, "missing/page", webui.Page{})
	if w.Code != 500 {
		t.Fatal("unknown template rendered")
	}
}
