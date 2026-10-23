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
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/connections"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/contracts"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/database"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/webui"
)

const grantDatabaseID = "30000000-0000-4000-8000-000000000001"

type grantSource struct {
	memberSource
	grants      []access.Grant
	grantsErr   error
	grantReads  int
	grantOffset int
	grantMore   bool
}

func (s *grantSource) Grants(_ context.Context, actor contracts.Principal, id string, limit, offset int) ([]access.Grant, bool, error) {
	s.grantReads++
	s.grantOffset, s.actor = offset, actor
	if id != groupID || limit != 50 {
		return nil, false, errors.New("unexpected grant query")
	}
	return s.grants, s.grantMore, s.grantsErr
}

type grantConnections struct {
	sources
	listError, getError error
	lists, gets, offset int
	more                bool
}

func (s *grantConnections) List(_ context.Context, limit, offset int) ([]connections.AdminConnection, bool, error) {
	s.lists++
	s.offset = offset
	if limit != 50 {
		return nil, false, errors.New("unexpected database query")
	}
	return s.conns, s.more, s.listError
}

func (s *grantConnections) Get(ctx context.Context, id string) (connections.AdminConnection, error) {
	s.gets++
	if s.getError != nil {
		return connections.AdminConnection{}, s.getError
	}
	return s.sources.Get(ctx, id)
}

func grantPage(t *testing.T, source *grantSource, databases *grantConnections, principal *contracts.Principal, policyErr error, query string) *httptest.ResponseRecorder {
	t.Helper()
	renderer, err := webui.NewRenderer(webui.Assets{
		"layout": template.HTML(`<script type="module" src="/assets/layout.js"></script>`),
		"grants": template.HTML(`<script type="module" src="/assets/grants.js"></script>`),
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
	webui.RegisterRoutes(mux, webui.NewPages(renderer, fakeAccess{policyErr}, fromContext, auth.RedirectToLogin, databases, nil, nil, source), guard)
	path := "/admin/groups/" + groupID + "/grants" + query
	if query == "/invalid" {
		path = "/admin/groups/invalid/grants"
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
	return w
}

func TestGrantsPageRendersSafeNamesAndForms(t *testing.T) {
	source := &grantSource{memberSource: memberSource{group: access.Group{ID: groupID, Name: `<script>group</script>`}},
		grants: []access.Grant{{DatabaseID: grantDatabaseID, Level: contracts.AccessEdit}}}
	databases := &grantConnections{sources: sources{conns: []connections.AdminConnection{{
		ID: grantDatabaseID, Name: `<img src=x>`, Driver: database.SQLite,
		Config: map[string]string{"file": "private-path", "password": "private-password"},
	}}}}
	w := grantPage(t, source, databases, admin, nil, "?database="+grantDatabaseID+"&notice=saved")
	if w.Code != 200 || w.Header().Get("Content-Type") != "text/html; charset=utf-8" || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("grants page did not render protected HTML", w.Code)
	}
	expect(t, w.Body.String(), []string{
		`&lt;script&gt;group&lt;/script&gt;`, `&lt;img src=x&gt;`, `<td>Nonaktif</td>`,
		`<caption>Grant pada halaman ini</caption>`, `<td>Edit (edit)</td>`, `(sqlite, nonaktif)</option>`,
		`data-operation="set"`, `data-operation="update"`, `data-operation="remove"`,
		`<option value="edit" selected>`, `name="confirm" type="checkbox"`, `required`, `type="submit" disabled`,
		`Grant dari grup lain tetap berlaku.`, `Hak akses database telah tersimpan.`, `/assets/grants.js`,
	}, `secret-session-id`, `SessionID`, `private-path`, `private-password`, `<img src=x>`, `<script>group`)
	if source.gets != 1 || source.grantReads != 1 || source.actor != *admin || databases.gets != 1 || databases.lists != 1 {
		t.Fatal("page did not use its services or principal")
	}
}

func TestGrantsPageKeepsPaginationIndependent(t *testing.T) {
	source := &grantSource{memberSource: memberSource{group: access.Group{ID: groupID, Name: "readers"}},
		grants: []access.Grant{{DatabaseID: grantDatabaseID, Level: contracts.AccessView}}, grantMore: true}
	databases := &grantConnections{sources: sources{conns: []connections.AdminConnection{{ID: grantDatabaseID, Name: "demo", Driver: database.Postgres, Active: true}}}, more: true}
	w := grantPage(t, source, databases, admin, nil, "?offset=50&databases_offset=100&database="+grantDatabaseID)
	if w.Code != 200 || source.grantOffset != 50 || databases.offset != 100 {
		t.Fatal("page mixed grant and database offsets", w.Code)
	}
	base := "/admin/groups/" + groupID + "/grants"
	expect(t, w.Body.String(), []string{
		`href="` + base + `?databases_offset=100&amp;offset=100#grants"`,
		`href="` + base + `?databases_offset=100#grants"`,
		`href="` + base + `?databases_offset=150&amp;offset=50#set-grant"`,
		`href="` + base + `?databases_offset=50&amp;offset=50#set-grant"`,
		`database=` + grantDatabaseID + `&amp;databases_offset=100&amp;offset=50#grant-settings`,
		`Menampilkan grant 51 sampai 51.`, `Menampilkan pilihan database 101 sampai 101.`,
		`<option value="view" selected>`, `<td>Aktif</td>`, `<td>Lihat (view)</td>`,
	}, `notice=saved`)
}

func TestGrantsPageEmptyStates(t *testing.T) {
	w := grantPage(t, &grantSource{memberSource: memberSource{group: access.Group{ID: groupID, Name: "empty"}}}, &grantConnections{}, admin, nil,
		"?offset=100000&databases_offset=100000&notice=%3Cscript%3E")
	if w.Code != 200 {
		t.Fatal("empty lists failed", w.Code)
	}
	expect(t, w.Body.String(), []string{
		`Tidak ada grant pada halaman ini.`, `Tidak ada pilihan database pada halaman ini.`,
		`aria-label="Halaman grant"`, `aria-label="Halaman pilihan database"`, `Halaman pertama`,
	}, `<tbody>`, `<script>`, `data-operation="set"`, `data-operation="remove"`, `Menampilkan grant`, `Berikutnya`)
}

func TestGrantsPageGuardsAndFailures(t *testing.T) {
	forbidden := &contracts.DomainError{Code: "FORBIDDEN"}
	for _, tc := range []struct {
		name, query                   string
		principal                     *contracts.Principal
		policyErr, groupErr, grantErr error
		databaseErr                   error
		status, groups, grants, lists int
	}{
		{"anonymous", "?offset=50", nil, nil, nil, nil, nil, 302, 0, 0, 0},
		{"non_admin", "", viewerUser, forbidden, nil, nil, nil, 403, 0, 0, 0},
		{"invalid_group", "/invalid", admin, nil, nil, nil, nil, 404, 0, 0, 0},
		{"invalid_offset", "?offset=bad", admin, nil, nil, nil, nil, 400, 0, 0, 0},
		{"negative_offset", "?offset=-1", admin, nil, nil, nil, nil, 400, 0, 0, 0},
		{"large_offset", "?offset=100001", admin, nil, nil, nil, nil, 400, 0, 0, 0},
		{"invalid_database_offset", "?databases_offset=bad", admin, nil, nil, nil, nil, 400, 0, 0, 0},
		{"negative_database_offset", "?databases_offset=-1", admin, nil, nil, nil, nil, 400, 0, 0, 0},
		{"large_database_offset", "?databases_offset=100001", admin, nil, nil, nil, nil, 400, 0, 0, 0},
		{"invalid_database", "?database=invalid", admin, nil, nil, nil, nil, 400, 0, 0, 0},
		{"missing_database", "?database=" + grantDatabaseID, admin, nil, nil, nil, nil, 404, 1, 1, 0},
		{"missing_group", "", admin, nil, &contracts.DomainError{Code: "NOT_FOUND"}, nil, nil, 404, 1, 0, 0},
		{"grant_error", "", admin, nil, nil, errors.New("private grant error"), nil, 500, 1, 1, 0},
		{"database_error", "", admin, nil, nil, nil, errors.New("private connection error"), 500, 1, 1, 1},
		{"lost_access", "", admin, nil, nil, forbidden, nil, 403, 1, 1, 0},
		{"expired_session", "", admin, nil, &contracts.DomainError{Code: "UNAUTHENTICATED"}, nil, nil, 302, 1, 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source := &grantSource{memberSource: memberSource{getErr: tc.groupErr}, grantsErr: tc.grantErr}
			databases := &grantConnections{listError: tc.databaseErr}
			w := grantPage(t, source, databases, tc.principal, tc.policyErr, tc.query)
			if w.Code != tc.status || source.gets != tc.groups || source.grantReads != tc.grants || databases.lists != tc.lists || databases.gets != 0 {
				t.Fatal("unexpected page result", w.Code, source.gets, source.grantReads, databases.lists, databases.gets)
			}
			if tc.name == "anonymous" && w.Header().Get("Location") != "/login?next=%2Fadmin%2Fgroups%2F"+groupID+"%2Fgrants%3Foffset%3D50" {
				t.Fatal("login redirect lost the return path")
			}
			if strings.Contains(w.Body.String(), "private ") || strings.Contains(w.Body.String(), "secret-session-id") {
				t.Fatal("page exposed internal details")
			}
		})
	}
}

func TestGrantsPageDoesNotHideConnectionLookupFailures(t *testing.T) {
	source := &grantSource{grants: []access.Grant{{DatabaseID: grantDatabaseID, Level: contracts.AccessView}}}
	databases := &grantConnections{getError: errors.New("private connection error")}
	w := grantPage(t, source, databases, admin, nil, "")
	if w.Code != 500 || databases.gets != 1 || databases.lists != 0 || strings.Contains(w.Body.String(), "private connection error") {
		t.Fatal("connection lookup failure was hidden or leaked", w.Code)
	}
}
