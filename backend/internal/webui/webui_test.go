package webui_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/connections"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/contracts"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/database"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/viewer"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/webui"
)

const manifest = `{
  "_shared.js": {"file": "assets/shared-1.js", "css": ["assets/shared-1.css"]},
  "src/pages/layout.ts": {"file": "assets/layout-1.js", "name": "layout", "isEntry": true, "imports": ["_shared.js"]},
  "src/pages/connections.ts": {"file": "assets/connections-1.js", "name": "connections", "isEntry": true, "imports": ["_shared.js"], "css": ["assets/connections-1.css"]},
  "src/pages/migrations.ts": {"file": "assets/migrations-1.js", "name": "migrations", "isEntry": true},
  "src/pages/schemadiff.tsx": {"file": "assets/schemadiff-1.js", "name": "schemadiff", "isEntry": true}
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
	tags, err := assets.Tags("connections")
	want := `<link rel="stylesheet" href="/assets/shared-1.css"><link rel="stylesheet" href="/assets/connections-1.css"><script type="module" src="/assets/connections-1.js"></script>`
	if err != nil || string(tags) != want {
		t.Fatalf("connections tags: %q %v", tags, err)
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

// fixture reads the "data" member of a shared API fixture, the same payloads the frontend tests use.
func fixture[T any](t *testing.T, name string) T {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "tests", "fixtures", "api", name))
	if err != nil {
		t.Fatal(err)
	}
	var body struct{ Data T }
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	return body.Data
}

type fakeAccess struct{ err error }

func (f fakeAccess) Require(context.Context, contracts.Principal, contracts.Action, string) error {
	return f.err
}

func (fakeAccess) Effective(context.Context, string, string) (contracts.AccessLevel, error) {
	return contracts.AccessNone, nil
}

// sources fakes the connections and viewer services behind the pages.
type sources struct {
	dbs       []connections.Database
	listErr   error
	conns     []connections.AdminConnection
	snap      database.SchemaSnapshot
	schemaErr error
	data      database.TableData
	hasMore   bool
	dataErr   error
	reads     []string
}

func (s *sources) Visible(context.Context, contracts.AccessChecker, string) ([]connections.Database, error) {
	return s.dbs, s.listErr
}

func (s *sources) Database(_ context.Context, _ contracts.AccessChecker, _ string, id string) (connections.Database, error) {
	for _, db := range s.dbs {
		if db.ID == id {
			return db, nil
		}
	}
	return connections.Database{}, database.ErrNotFound
}

func (s *sources) List(_ context.Context, limit, offset int) ([]connections.AdminConnection, bool, error) {
	if offset >= len(s.conns) {
		return nil, false, nil
	}
	end := min(len(s.conns), offset+limit)
	return s.conns[offset:end], end < len(s.conns), nil
}

func (s *sources) Get(_ context.Context, id string) (connections.AdminConnection, error) {
	for _, connection := range s.conns {
		if connection.ID == id {
			return connection, nil
		}
	}
	return connections.AdminConnection{}, database.ErrNotFound
}

func (s *sources) Schema(context.Context, string) (database.SchemaSnapshot, error) {
	return s.snap, s.schemaErr
}

func (s *sources) Data(_ context.Context, _, schema, table string, limit, offset int) (database.TableData, viewer.Page, error) {
	s.reads = append(s.reads, fmt.Sprintf("%s.%s limit=%d offset=%d", schema, table, limit, offset))
	return s.data, viewer.Page{Limit: limit, Offset: offset, HasMore: s.hasMore}, s.dataErr
}

func newSources(t *testing.T) *sources {
	return &sources{
		dbs:  fixture[[]connections.Database](t, "databases_list.json"),
		snap: fixture[database.SchemaSnapshot](t, "viewer_schema_populated.json"),
		data: fixture[database.TableData](t, "viewer_data_populated.json"),
	}
}

const databaseID = "30000000-0000-4000-8000-000000000001"

var (
	admin      = &contracts.Principal{UserID: "u1", Username: "admin", IsPlatformAdmin: true, SessionID: "secret-session-id"}
	viewerUser = &contracts.Principal{UserID: "u2", Username: "viewer", SessionID: "secret-session-id"}
)

type harness struct {
	t         *testing.T
	src       *sources
	principal *contracts.Principal
	access    error
}

func (h harness) get(path string) (int, string) {
	return h.getMigrations(path, newMigrationSource(h.t))
}

func (h harness) getMigrations(path string, source webui.MigrationSource) (int, string) {
	h.t.Helper()
	assets, err := webui.LoadAssets(writeManifest(h.t, manifest))
	if err != nil {
		h.t.Fatal(err)
	}
	renderer, err := webui.NewRenderer(assets)
	if err != nil {
		h.t.Fatal(err)
	}
	principal := func(context.Context) (contracts.Principal, bool) {
		if h.principal == nil {
			return contracts.Principal{}, false
		}
		return *h.principal, true
	}
	login := func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/login", http.StatusFound) }
	guarded := 0
	guard := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { guarded++; next.ServeHTTP(w, r) })
	}
	mux := http.NewServeMux()
	webui.RegisterRoutes(mux, webui.NewPages(renderer, fakeAccess{h.access}, principal, login, h.src, h.src, nil, nil).WithMigrations(source), guard)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
	if guarded != 1 {
		h.t.Fatalf("%s bypassed the page guard", path)
	}
	if w.Code != http.StatusFound && w.Header().Get("Content-Type") != "text/html; charset=utf-8" {
		h.t.Fatalf("%s is not HTML", path)
	}
	if strings.Contains(w.Body.String(), "secret-session-id") {
		h.t.Fatalf("%s exposed the session id", path)
	}
	return w.Code, w.Body.String()
}

func expect(t *testing.T, body string, contains []string, excludes ...string) {
	t.Helper()
	for _, s := range contains {
		if !strings.Contains(body, s) {
			t.Errorf("missing %q", s)
		}
	}
	for _, s := range excludes {
		if strings.Contains(body, s) {
			t.Errorf("unexpected %q", s)
		}
	}
}

func TestLayoutAndPolicy(t *testing.T) {
	forbidden := &contracts.DomainError{Code: "FORBIDDEN", Message: "x"}
	notFound := &contracts.DomainError{Code: "NOT_FOUND", Message: "x"}
	hostile := &contracts.Principal{UserID: "u3", Username: `<script>alert(1)</script>`}
	for _, tc := range []struct {
		name      string
		principal *contracts.Principal
		access    error
		path      string
		status    int
		contains  []string
		excludes  []string
	}{
		{"admin navigation", admin, nil, "/databases", 200,
			[]string{"<title>Database", `href="/databases" aria-current="page"`, `href="/admin/connections"`, "/assets/layout-1.js"}, nil},
		{"username is escaped and admin links hidden", hostile, nil, "/databases", 200,
			[]string{"&lt;script&gt;alert(1)&lt;/script&gt;"}, []string{"<script>alert(1)</script>", `href="/admin/connections"`}},
		{"root redirects", viewerUser, nil, "/", 302, nil, nil},
		{"guard rejected", nil, nil, "/databases", 302, nil, nil},
		{"database without grant", viewerUser, notFound, "/databases/" + databaseID, 404, []string{"Halaman tidak ditemukan"}, []string{"Migrasi dan riwayat"}},
		{"malformed database id", viewerUser, nil, "/databases/not-a-uuid", 404, []string{"Halaman tidak ditemukan"}, nil},
		{"policy failure", viewerUser, errors.New("database down"), "/databases/" + databaseID, 500, []string{"Terjadi kesalahan"}, []string{"database down"}},
		{"deactivated user", viewerUser, &contracts.DomainError{Code: "UNAUTHENTICATED"}, "/admin/connections", 302, nil, nil},
		{"migrations page", viewerUser, nil, "/databases/" + databaseID + "/migrations", 200,
			[]string{`data-widget="migrations"`, `data-database-id="` + databaseID + `"`, "/assets/migrations-1.js"}, nil},
		{"migrations without grant", viewerUser, notFound, "/databases/" + databaseID + "/migrations", 404, []string{"Halaman tidak ditemukan"}, []string{"data-widget"}},
		{"connections for non-admin", viewerUser, forbidden, "/admin/connections", 403, []string{"Akses ditolak"}, []string{`data-widget="connections"`}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status, body := harness{t, newSources(t), tc.principal, tc.access}.get(tc.path)
			if status != tc.status {
				t.Fatalf("%s returned %d: %s", tc.path, status, body)
			}
			expect(t, body, tc.contains, tc.excludes...)
		})
	}
}

func TestDatabaseListIsServerRendered(t *testing.T) {
	src := newSources(t)
	src.dbs[1].Name = `<img src=x onerror=alert(1)>`
	status, body := harness{t, src, viewerUser, nil}.get("/databases")
	if status != 200 {
		t.Fatal(status)
	}
	expect(t, body, []string{
		`<a href="/databases/` + databaseID + `">PostgreSQL latihan</a> (postgres, akses lihat dan edit)`,
		"&lt;img src=x onerror=alert(1)&gt;", "(sqlite, akses lihat)",
	}, "<img src=x", "<script type=\"module\" src=\"/assets/connections")

	src.dbs = []connections.Database{}
	if _, body := (harness{t, src, viewerUser, nil}).get("/databases"); !strings.Contains(body, "Belum ada database yang dapat Anda akses.") {
		t.Fatal("empty list has no empty state")
	}
	src.listErr = errors.New("metadata down")
	if status, body := (harness{t, src, viewerUser, nil}).get("/databases"); status != 500 || strings.Contains(body, "metadata down") {
		t.Fatal("list failure was not a clean 500", status)
	}
}

func TestDatabaseViewerIsServerRendered(t *testing.T) {
	src := newSources(t)
	base := "/databases/" + databaseID
	status, body := harness{t, src, viewerUser, nil}.get(base)
	if status != 200 || len(src.reads) != 0 {
		t.Fatal("overview read table data", status, src.reads)
	}
	expect(t, body, []string{
		"<title>PostgreSQL latihan", "Engine postgres, akses lihat dan edit", src.snap.Fingerprint,
		`href="` + base + `/migrations"`, `href="` + base + `?schema=public&amp;table=items"`, "Pilih tabel",
	}, "<table")

	status, body = harness{t, src, viewerUser, nil}.get(base + "?schema=public&table=items")
	if status != 200 || len(src.reads) != 1 || src.reads[0] != "public.items limit=50 offset=0" {
		t.Fatal("table page", status, src.reads)
	}
	expect(t, body, []string{
		`aria-current="page">items</a>`, "<h2>public.items</h2>",
		"<td>1</td><td>id</td><td>bigint</td><td>tidak</td><td>tidak ada</td>",
		"primary_key items_pkey: <code>PRIMARY KEY (id)</code>",
		// bigint beyond 2^53 stays the exact string, NULL is distinct from text, binary stays base64
		"<td>9007199254740993</td>", "<em>NULL</em>", `<span title="Data biner (base64)">3q2&#43;7w==</span>`,
		"Baris 1-2",
	}, "tidak dijamin", `rel="prev"`, `rel="next"`)
}

func TestDatabaseViewerPagesAndProblems(t *testing.T) {
	base := "/databases/" + databaseID
	table := base + "?schema=public&table=items"

	src := newSources(t)
	src.hasMore = true
	status, body := harness{t, src, viewerUser, nil}.get(table + "&offset=50")
	if status != 200 || src.reads[0] != "public.items limit=50 offset=50" {
		t.Fatal("second page", status, src.reads)
	}
	expect(t, body, []string{
		`href="` + base + `?schema=public&amp;table=items" rel="prev"`,
		`href="` + base + `?offset=100&amp;schema=public&amp;table=items" rel="next"`, "Baris 51-52",
	})

	src = newSources(t)
	src.data = fixture[database.TableData](t, "viewer_data_unordered.json")
	_, body = harness{t, src, viewerUser, nil}.get(table)
	expect(t, body, []string{"tidak memiliki primary key", "<td>2026-10-21T01:00:00Z</td>"})

	src = newSources(t)
	src.data = fixture[database.TableData](t, "viewer_data_empty.json")
	_, body = harness{t, src, viewerUser, nil}.get(table)
	expect(t, body, []string{"Tabel tidak memiliki baris"}, "<em>NULL</em>")

	src = newSources(t)
	src.data.Rows = [][]any{{"<script>alert(1)</script>", map[string]any{"k": "<b>"}, nil, nil}}
	_, body = harness{t, src, viewerUser, nil}.get(table)
	expect(t, body, []string{"&lt;script&gt;alert(1)&lt;/script&gt;", "{&#34;k&#34;:&#34;\\u003cb\\u003e&#34;}"}, "<script>alert(1)", "<b>")

	for _, tc := range []struct {
		name     string
		setup    func(*sources)
		path     string
		status   int
		contains string
	}{
		{"target role may not read the table", func(s *sources) { s.dataErr = fmt.Errorf("wrap: %w", database.ErrTargetPermission) }, table, 200, "Akses ditolak: role target"},
		{"data timeout", func(s *sources) { s.dataErr = database.ErrTargetTimeout }, table, 200, "Target tidak merespons"},
		{"target down", func(s *sources) { s.schemaErr = errors.New("dial tcp 10.0.0.5: secret dsn") }, base, 500, "Terjadi kesalahan internal"},
		{"target unavailable", func(s *sources) { s.schemaErr = database.ErrTargetUnavailable }, base, 502, "Target tidak dapat dihubungi"},
		{"schema timeout", func(s *sources) { s.schemaErr = database.ErrTargetTimeout }, base, 504, "Target tidak merespons"},
		{"unknown table", func(*sources) {}, base + "?schema=public&table=missing", 404, "Halaman tidak ditemukan"},
		{"reserved namespace is not in the snapshot", func(*sources) {}, base + "?schema=_platform&table=migration_ledger", 404, "Halaman tidak ditemukan"},
		{"bad offset", func(*sources) {}, table + "&offset=-1", 400, "Permintaan tidak valid"},
		{"huge offset", func(*sources) {}, table + "&offset=100001", 400, "Permintaan tidak valid"},
		{"inactive database", func(s *sources) { s.dbs = nil }, base, 404, "Halaman tidak ditemukan"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := newSources(t)
			tc.setup(src)
			status, body := harness{t, src, viewerUser, nil}.get(tc.path)
			if status != tc.status || !strings.Contains(body, tc.contains) || strings.Contains(body, "secret dsn") {
				t.Fatalf("%s returned %d: %s", tc.path, status, body)
			}
		})
	}
}

func TestConnectionsPageListsRegistry(t *testing.T) {
	src := newSources(t)
	code := "TARGET_UNAVAILABLE"
	at := time.Date(2026, 10, 22, 1, 0, 0, 0, time.UTC)
	src.conns = []connections.AdminConnection{
		{ID: "c1", Name: "PostgreSQL demo", Driver: database.Postgres, Active: true, MigrationReady: true,
			Config: connections.PublicPostgresConfig{Host: "localhost", Port: 15433, Database: "demo", Username: "platform_runner", SSLMode: "disable", HasCredentials: true}},
		{ID: "c2", Name: "SQLite demo", Driver: database.SQLite, Active: true, LastCheckedAt: &at, LastErrorCode: &code,
			Config: connections.PublicSQLiteConfig{File: "demo.sqlite"}},
		{ID: "c3", Name: "Lama", Driver: database.SQLite, Config: connections.PublicSQLiteConfig{File: "old.sqlite"}},
	}
	status, body := harness{t, src, admin, nil}.get("/admin/connections")
	if status != 200 {
		t.Fatal(status)
	}
	expect(t, body, []string{
		`href="/admin/connections" aria-current="page"`, `data-widget="connections"`, "/assets/connections-1.js",
		"<td>PostgreSQL demo</td><td>postgres</td><td>platform_runner@localhost:15433/demo (sslmode disable)</td><td>Aktif, siap migrasi</td>",
		"<td>demo.sqlite</td><td>Aktif, uji terakhir gagal (TARGET_UNAVAILABLE)</td>",
		"<td>old.sqlite</td><td>Nonaktif</td>",
	})
	src.conns = nil
	if _, body := (harness{t, src, admin, nil}).get("/admin/connections"); !strings.Contains(body, "Belum ada koneksi terdaftar.") {
		t.Fatal("empty registry has no empty state")
	}
}

func TestConnectionsPagePagesPastTheFirst200(t *testing.T) {
	src := newSources(t)
	for i := range 201 {
		src.conns = append(src.conns, connections.AdminConnection{ID: fmt.Sprint(i), Name: fmt.Sprintf("Koneksi %03d", i), Driver: database.SQLite, Config: connections.PublicSQLiteConfig{File: "x.sqlite"}})
	}
	h := harness{t, src, admin, nil}
	_, body := h.get("/admin/connections")
	expect(t, body, []string{"Koneksi 199", "Menampilkan koneksi 1 sampai 200.", `href="/admin/connections?offset=200">Berikutnya`}, "Koneksi 200", "Sebelumnya")
	_, body = h.get("/admin/connections?offset=200")
	expect(t, body, []string{"Koneksi 200", "Menampilkan koneksi 201 sampai 201.", `href="/admin/connections">Sebelumnya`}, "Koneksi 199", "Berikutnya")
	_, body = h.get("/admin/connections?offset=400")
	expect(t, body, []string{"Tidak ada koneksi pada halaman ini.", "Halaman pertama"}, "Belum ada koneksi terdaftar.")
	for _, bad := range []string{"-1", "x", "100001"} {
		if status, _ := h.get("/admin/connections?offset=" + bad); status != 400 {
			t.Fatal(bad, status)
		}
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
	renderer.Render(w, 200, "connections/index", webui.Page{Title: "Koneksi", User: &webui.PageUser{Username: "u"}, Entry: "connections"})
	if w.Code != 500 || strings.Contains(w.Body.String(), "<html") {
		t.Fatalf("missing entry rendered: %d", w.Code)
	}
	w = httptest.NewRecorder()
	renderer.Render(w, 200, "missing/page", webui.Page{})
	if w.Code != 500 {
		t.Fatal("unknown template rendered")
	}
}
