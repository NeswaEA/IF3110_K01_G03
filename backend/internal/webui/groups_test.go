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

const groupID = "20000000-0000-4000-8000-000000000001"

type groupSource struct {
	groups        []access.Group
	more          bool
	err           error
	calls, offset int
	actor         contracts.Principal
}

func (s *groupSource) List(_ context.Context, actor contracts.Principal, limit, offset int) ([]access.Group, bool, error) {
	if limit != 50 {
		return nil, false, errors.New("unexpected page size")
	}
	s.calls++
	s.offset, s.actor = offset, actor
	return s.groups, s.more, s.err
}

func (*groupSource) Get(context.Context, contracts.Principal, string) (access.Group, error) {
	return access.Group{}, errors.New("unexpected group lookup")
}

func (*groupSource) Members(context.Context, contracts.Principal, string, int, int) ([]access.User, bool, error) {
	return nil, false, errors.New("unexpected member lookup")
}

func groupPage(t *testing.T, source *groupSource, principal *contracts.Principal, policyErr error, path string) *httptest.ResponseRecorder {
	t.Helper()
	renderer, err := webui.NewRenderer(webui.Assets{
		"layout": template.HTML(`<script type="module" src="/assets/layout.js"></script>`),
		"groups": template.HTML(`<script type="module" src="/assets/groups.js"></script>`),
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
	webui.RegisterRoutes(mux, webui.NewPages(renderer, fakeAccess{policyErr}, fromContext, auth.RedirectToLogin, nil, nil, nil, source), guard)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
	return w
}

func TestGroupsPageRendersNamesAndForms(t *testing.T) {
	principal := &contracts.Principal{UserID: accountID, Username: "admin", IsPlatformAdmin: true, SessionID: "private-session"}
	now := time.Date(2026, 10, 23, 15, 30, 0, 0, time.FixedZone("local", 7*60*60))
	source := &groupSource{groups: []access.Group{{ID: groupID, Name: `<script>alert("group")</script>`, CreatedAt: now, UpdatedAt: now}}}
	w := groupPage(t, source, principal, nil, "/admin/groups?group="+groupID+"&notice=updated")
	if w.Code != 200 || w.Header().Get("Content-Type") != "text/html; charset=utf-8" || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("group page did not render protected HTML", w.Code)
	}
	expect(t, w.Body.String(), []string{
		`aria-current="page">Administrasi`, `href="/admin/users">Pengguna`, `aria-current="page">Grup`,
		`<caption>Grup pada halaman ini</caption>`, `&lt;script&gt;alert(&#34;group&#34;)&lt;/script&gt;`,
		`23 Oct 2026 08:30 UTC`, `data-operation="create"`, `data-operation="update"`, `data-operation="delete"`,
		`name="confirm" type="checkbox"`, `required`, `type="submit" disabled`, `seluruh sesi`,
		`Nama grup berhasil diperbarui.`, `/assets/groups.js`,
		`href="/admin/groups/` + groupID + `/members"`,
	}, "private-session", "SessionID", "password_hash", `<script>alert`)
	if source.calls != 1 || source.actor != *principal {
		t.Fatal("page did not pass the principal to the group service")
	}
}

func TestGroupsPagePaginationAndEmptyState(t *testing.T) {
	source := &groupSource{groups: []access.Group{{ID: groupID, Name: "readers"}}, more: true}
	w := groupPage(t, source, admin, nil, "/admin/groups?offset=50&group="+groupID)
	if w.Code != 200 || source.offset != 50 {
		t.Fatal("page did not use the requested offset", w.Code)
	}
	expect(t, w.Body.String(), []string{
		`href="/admin/groups?offset=100"`, `href="/admin/groups">Sebelumnya`,
		`Menampilkan grup 51 sampai 51.`, `group=` + groupID + `&amp;offset=50#group-settings`,
	}, `notice=updated`)
	w = groupPage(t, &groupSource{}, admin, nil, "/admin/groups?offset=100000&notice=%3Cscript%3E")
	if w.Code != 200 {
		t.Fatal("empty page failed", w.Code)
	}
	expect(t, w.Body.String(), []string{`Tidak ada grup pada halaman ini.`, `data-operation="create"`, `Halaman pertama`},
		`<tbody>`, `<script>`, `data-operation="delete"`, `Menampilkan grup`, `Berikutnya`)
}

func TestGroupsPageGuardsAndFailures(t *testing.T) {
	forbidden := &contracts.DomainError{Code: "FORBIDDEN", Message: "Denied"}
	for _, tc := range []struct {
		name, path string
		principal  *contracts.Principal
		policyErr  error
		serviceErr error
		status     int
		reads      int
	}{
		{"anonymous", "/admin/groups?offset=50", nil, nil, nil, 302, 0},
		{"non_admin", "/admin/groups", viewerUser, forbidden, nil, 403, 0},
		{"invalid_offset", "/admin/groups?offset=bad", admin, nil, nil, 400, 0},
		{"negative_offset", "/admin/groups?offset=-1", admin, nil, nil, 400, 0},
		{"large_offset", "/admin/groups?offset=100001", admin, nil, nil, 400, 0},
		{"invalid_id", "/admin/groups?group=invalid", admin, nil, nil, 400, 0},
		{"missing_group", "/admin/groups?group=" + groupID, admin, nil, nil, 404, 1},
		{"storage_error", "/admin/groups", admin, nil, errors.New("private storage error"), 500, 1},
		{"lost_access", "/admin/groups", admin, nil, forbidden, 403, 1},
		{"expired_session", "/admin/groups", admin, nil, &contracts.DomainError{Code: "UNAUTHENTICATED"}, 302, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source := &groupSource{err: tc.serviceErr}
			w := groupPage(t, source, tc.principal, tc.policyErr, tc.path)
			if w.Code != tc.status || source.calls != tc.reads {
				t.Fatal("unexpected page result", w.Code, source.calls)
			}
			if tc.name == "anonymous" && w.Header().Get("Location") != "/login?next=%2Fadmin%2Fgroups%3Foffset%3D50" {
				t.Fatal("login redirect lost the return path")
			}
			if strings.Contains(w.Body.String(), "private storage error") || strings.Contains(w.Body.String(), "secret-session-id") {
				t.Fatal("page exposed internal details")
			}
		})
	}
}
