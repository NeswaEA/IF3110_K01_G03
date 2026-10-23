package webui_test

import (
	"context"
	"errors"
	"html/template"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/access"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/auth"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/contracts"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/webui"
)

const accountID = "10000000-0000-4000-8000-000000000001"

type userSource struct {
	users         []access.User
	more          bool
	err           error
	calls, offset int
	actor         contracts.Principal
}

func (s *userSource) List(_ context.Context, actor contracts.Principal, limit, offset int) ([]access.User, bool, error) {
	if limit != 50 {
		return nil, false, errors.New("unexpected page size")
	}
	s.calls++
	s.offset, s.actor = offset, actor
	return s.users, s.more, s.err
}

func userPage(t *testing.T, source *userSource, principal *contracts.Principal, policyErr error, path string) *httptest.ResponseRecorder {
	t.Helper()
	renderer, err := webui.NewRenderer(webui.Assets{
		"layout": template.HTML(`<script type="module" src="/assets/layout.js"></script>`),
		"users":  template.HTML(`<script type="module" src="/assets/users.js"></script>`),
	})
	if err != nil {
		t.Fatal(err)
	}
	fromContext := func(context.Context) (contracts.Principal, bool) {
		if principal == nil {
			return contracts.Principal{}, false
		}
		return *principal, true
	}
	guard := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if principal == nil {
				auth.RedirectToLogin(w, r)
				return
			}
			w.Header().Set("Cache-Control", "no-store")
			next.ServeHTTP(w, r)
		})
	}
	mux := http.NewServeMux()
	webui.RegisterRoutes(mux, webui.NewPages(renderer, fakeAccess{policyErr}, fromContext, auth.RedirectToLogin, nil, nil, source, nil), guard)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
	return w
}

func TestUsersPageRendersAccountsAndForms(t *testing.T) {
	principal := &contracts.Principal{UserID: accountID, Username: "admin", IsPlatformAdmin: true, SessionID: "private-session"}
	now := time.Date(2026, 10, 23, 15, 30, 0, 0, time.FixedZone("local", 7*60*60))
	source := &userSource{users: []access.User{
		{ID: accountID, Username: "admin", Active: true, IsPlatformAdmin: true, CreatedAt: now, UpdatedAt: now},
		{ID: "10000000-0000-4000-8000-000000000002", Username: `<script>alert(1)</script>`, Active: false, CreatedAt: now, UpdatedAt: now},
	}}
	w := userPage(t, source, principal, nil, "/admin/users?user="+accountID+"&notice=updated")
	if w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("Content-Type") != "text/html; charset=utf-8" {
		t.Fatal("admin page did not render HTML", w.Code)
	}
	expect(t, w.Body.String(), []string{
		`aria-current="page">Administrasi`, `<caption>Pengguna pada halaman ini</caption>`,
		`<th scope="row">admin</th>`, `<td>Nonaktif</td>`, `23 Oct 2026 08:30 UTC`,
		`&lt;script&gt;alert(1)&lt;/script&gt;`, `name="active" type="checkbox" checked`,
		`data-operation="create"`, `data-operation="update"`, `data-operation="password"`,
		`Anda sedang mengatur akun sendiri.`, `Status dan peran pengguna berhasil diperbarui.`,
		`autocomplete="new-password"`, `type="submit" disabled`, `/assets/users.js`,
	}, "private-session", "SessionID", "password_hash", `value="private`, `<script>alert(1)</script>`)
	if source.calls != 1 || source.actor != *principal {
		t.Fatal("page did not pass the authenticated principal to the service")
	}
}

func TestUsersPagePaginationAndEmptyState(t *testing.T) {
	source := &userSource{users: []access.User{{ID: accountID, Username: "admin", Active: true}}, more: true}
	w := userPage(t, source, admin, nil, "/admin/users?offset=50")
	if w.Code != 200 || source.offset != 50 {
		t.Fatal("page did not pass the offset to the service")
	}
	expect(t, w.Body.String(), []string{`href="/admin/users?offset=100"`, `href="/admin/users">Sebelumnya`, `Menampilkan pengguna 51 sampai 51.`}, `data-operation="update"`)
	w = userPage(t, &userSource{}, admin, nil, "/admin/users?notice=%3Cscript%3E")
	if w.Code != 200 {
		t.Fatal("empty page failed", w.Code)
	}
	expect(t, w.Body.String(), []string{`Tidak ada pengguna pada halaman ini.`, `data-operation="create"`}, `<tbody>`, `<script>`, `data-operation="password"`)
}

func TestUsersPageGuardsAndFailures(t *testing.T) {
	forbidden := &contracts.DomainError{Code: "FORBIDDEN", Message: "Denied"}
	for _, tc := range []struct {
		name, path string
		principal  *contracts.Principal
		policyErr  error
		serviceErr error
		status     int
		reads      int
	}{
		{"anonymous", "/admin/users?offset=50", nil, nil, nil, 302, 0},
		{"non_admin", "/admin/users", viewerUser, forbidden, nil, 403, 0},
		{"admin_redirect", "/admin", admin, nil, nil, 302, 0},
		{"non_admin_redirect", "/admin", viewerUser, forbidden, nil, 403, 0},
		{"invalid_offset", "/admin/users?offset=bad", admin, nil, nil, 400, 0},
		{"negative_offset", "/admin/users?offset=-1", admin, nil, nil, 400, 0},
		{"large_offset", "/admin/users?offset=100001", admin, nil, nil, 400, 0},
		{"invalid_user", "/admin/users?user=invalid", admin, nil, nil, 400, 0},
		{"missing_user", "/admin/users?user=" + accountID, admin, nil, nil, 404, 1},
		{"storage_error", "/admin/users", admin, nil, errors.New("private storage error"), 500, 1},
		{"lost_access", "/admin/users", admin, nil, forbidden, 403, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source := &userSource{err: tc.serviceErr}
			w := userPage(t, source, tc.principal, tc.policyErr, tc.path)
			if w.Code != tc.status || source.calls != tc.reads {
				t.Fatal("unexpected page result", w.Code, source.calls)
			}
			if tc.name == "anonymous" && w.Header().Get("Location") != "/login?next=%2Fadmin%2Fusers%3Foffset%3D50" {
				t.Fatal("login redirect lost the return path")
			}
			if tc.name == "admin_redirect" && w.Header().Get("Location") != "/admin/users" {
				t.Fatal("admin navigation has no destination")
			}
			if strings.Contains(w.Body.String(), "private storage error") || strings.Contains(w.Body.String(), "secret-session-id") {
				t.Fatal("page exposed internal details")
			}
		})
	}
}
