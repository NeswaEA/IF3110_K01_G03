package webui_test

import (
	"context"
	"errors"
	"html/template"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/access"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/auth"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/contracts"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/webui"
)

type memberSource struct {
	groupSource
	group               access.Group
	members             []access.User
	more                bool
	getErr, membersErr  error
	gets, reads, offset int
	actor               contracts.Principal
}

func (s *memberSource) Get(_ context.Context, actor contracts.Principal, id string) (access.Group, error) {
	s.gets++
	s.actor = actor
	if id != groupID {
		return access.Group{}, errors.New("unexpected group ID")
	}
	return s.group, s.getErr
}

func (s *memberSource) Members(_ context.Context, actor contracts.Principal, id string, limit, offset int) ([]access.User, bool, error) {
	s.reads++
	s.offset, s.actor = offset, actor
	if id != groupID || limit != 50 {
		return nil, false, errors.New("unexpected member query")
	}
	return s.members, s.more, s.membersErr
}

func memberPage(t *testing.T, source *memberSource, users *userSource, principal *contracts.Principal, policyErr error, query string) *httptest.ResponseRecorder {
	t.Helper()
	renderer, err := webui.NewRenderer(webui.Assets{
		"layout":  template.HTML(`<script type="module" src="/assets/layout.js"></script>`),
		"members": template.HTML(`<script type="module" src="/assets/members.js"></script>`),
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
	webui.RegisterRoutes(mux, webui.NewPages(renderer, fakeAccess{policyErr}, fromContext, auth.RedirectToLogin, nil, nil, users, source), guard)
	w := httptest.NewRecorder()
	path := "/admin/groups/" + groupID + "/members" + query
	if query == "/invalid" {
		path = "/admin/groups/invalid/members"
	}
	mux.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
	return w
}

func TestMembersPageRendersSafeNamesAndForms(t *testing.T) {
	principal := &contracts.Principal{UserID: accountID, Username: "admin", IsPlatformAdmin: true, SessionID: "private-session"}
	source := &memberSource{group: access.Group{ID: groupID, Name: `<script>alert("group")</script>`},
		members: []access.User{{ID: accountID, Username: `<img src=x>`, IsPlatformAdmin: true, Active: true}}}
	users := &userSource{users: []access.User{{ID: accountID, Username: `<img src=x>`, Active: false}}}
	w := memberPage(t, source, users, principal, nil, "?member="+accountID+"&notice=added")
	if w.Code != 200 || w.Header().Get("Content-Type") != "text/html; charset=utf-8" || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("members page did not render protected HTML", w.Code)
	}
	expect(t, w.Body.String(), []string{
		`&lt;script&gt;alert(&#34;group&#34;)&lt;/script&gt;`, `&lt;img src=x&gt;`, ` (nonaktif)</option>`,
		`<caption>Anggota pada halaman ini</caption>`, `<td>Aktif</td>`, `<td>Admin platform</td>`,
		`data-operation="add"`, `data-operation="remove"`, `Anda akan mengeluarkan akun sendiri`,
		`name="confirm" type="checkbox"`, `required`, `type="submit" disabled`,
		`Keanggotaan pengguna telah tersimpan.`, `/assets/members.js`,
	}, "private-session", "SessionID", "password_hash", `<img src=x>`, `<script>alert`)
	if source.gets != 1 || source.reads != 1 || users.calls != 1 || source.actor != *principal || users.actor != *principal {
		t.Fatal("page did not pass the principal to its services")
	}
}

func TestMembersPageKeepsPaginationIndependent(t *testing.T) {
	source := &memberSource{group: access.Group{ID: groupID, Name: "readers"}, members: []access.User{{ID: accountID, Username: "member"}}, more: true}
	users := &userSource{users: []access.User{{ID: accountID, Username: "candidate"}}, more: true}
	w := memberPage(t, source, users, admin, nil, "?offset=50&users_offset=100&member="+accountID)
	if w.Code != 200 || source.offset != 50 || users.offset != 100 {
		t.Fatal("page mixed member and user offsets", w.Code, source.offset, users.offset)
	}
	base := "/admin/groups/" + groupID + "/members"
	expect(t, w.Body.String(), []string{
		`href="` + base + `?offset=100&amp;users_offset=100#members"`,
		`href="` + base + `?users_offset=100#members"`,
		`href="` + base + `?offset=50&amp;users_offset=150#add-member"`,
		`href="` + base + `?offset=50&amp;users_offset=50#add-member"`,
		`member=` + accountID + `&amp;offset=50&amp;users_offset=100#remove-member`,
		`Menampilkan anggota 51 sampai 51.`, `Menampilkan pilihan pengguna 101 sampai 101.`,
	}, `notice=added`)
}

func TestMembersPageEmptyStates(t *testing.T) {
	w := memberPage(t, &memberSource{group: access.Group{ID: groupID, Name: "empty"}}, &userSource{}, admin, nil,
		"?offset=100000&users_offset=100000&notice=%3Cscript%3E")
	if w.Code != 200 {
		t.Fatal("empty lists failed", w.Code)
	}
	expect(t, w.Body.String(), []string{
		`Tidak ada anggota pada halaman ini.`, `Tidak ada pilihan pengguna pada halaman ini.`,
		`aria-label="Halaman anggota"`, `aria-label="Halaman pilihan pengguna"`, `Halaman pertama`,
	}, `<tbody>`, `<script>`, `data-operation="add"`, `data-operation="remove"`, `Menampilkan anggota`, `Berikutnya`)
}

func TestMembersPageGuardsAndFailures(t *testing.T) {
	forbidden := &contracts.DomainError{Code: "FORBIDDEN"}
	for _, tc := range []struct {
		name, query                   string
		principal                     *contracts.Principal
		policyErr, getErr, membersErr error
		usersErr                      error
		status, gets, reads, users    int
	}{
		{"anonymous", "?offset=50", nil, nil, nil, nil, nil, 302, 0, 0, 0},
		{"non_admin", "", viewerUser, forbidden, nil, nil, nil, 403, 0, 0, 0},
		{"invalid_group", "/invalid", admin, nil, nil, nil, nil, 404, 0, 0, 0},
		{"invalid_offset", "?offset=bad", admin, nil, nil, nil, nil, 400, 0, 0, 0},
		{"negative_offset", "?offset=-1", admin, nil, nil, nil, nil, 400, 0, 0, 0},
		{"large_user_offset", "?users_offset=100001", admin, nil, nil, nil, nil, 400, 0, 0, 0},
		{"invalid_member", "?member=bad", admin, nil, nil, nil, nil, 400, 0, 0, 0},
		{"missing_group", "", admin, nil, &contracts.DomainError{Code: "NOT_FOUND"}, nil, nil, 404, 1, 0, 0},
		{"deleted_group", "", admin, nil, nil, &contracts.DomainError{Code: "NOT_FOUND"}, nil, 404, 1, 1, 0},
		{"missing_member", "?member=" + accountID, admin, nil, nil, nil, nil, 404, 1, 1, 0},
		{"storage_error", "", admin, nil, nil, errors.New("private storage error"), nil, 500, 1, 1, 0},
		{"users_error", "", admin, nil, nil, nil, errors.New("private users error"), 500, 1, 1, 1},
		{"lost_access", "", admin, nil, nil, nil, forbidden, 403, 1, 1, 1},
		{"expired_session", "", admin, nil, &contracts.DomainError{Code: "UNAUTHENTICATED"}, nil, nil, 302, 1, 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source := &memberSource{getErr: tc.getErr, membersErr: tc.membersErr}
			users := &userSource{err: tc.usersErr}
			w := memberPage(t, source, users, tc.principal, tc.policyErr, tc.query)
			if w.Code != tc.status || source.gets != tc.gets || source.reads != tc.reads || users.calls != tc.users {
				t.Fatal("unexpected page result", w.Code, source.gets, source.reads, users.calls)
			}
			if tc.name == "anonymous" && !strings.HasPrefix(w.Header().Get("Location"), "/login?next=%2Fadmin%2Fgroups%2F"+groupID+"%2Fmembers%3Foffset%3D50") {
				t.Fatal("login redirect lost the return path")
			}
			if strings.Contains(w.Body.String(), "private ") || strings.Contains(w.Body.String(), "secret-session-id") {
				t.Fatal("page exposed internal details")
			}
		})
	}
}
