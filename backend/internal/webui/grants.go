package webui

import (
	"net/http"
	"net/url"
	"strconv"

	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/access"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/contracts"
)

type grantDatabase struct {
	ID, Name, Driver string
	Active           bool
}

type grantRow struct {
	Database grantDatabase
	Level    contracts.AccessLevel
	URL      string
}

type grantsView struct {
	Group             access.Group
	Rows              []grantRow
	Selected          *grantRow
	Candidates        []grantDatabase
	Grants, Databases listPage
	ReturnTo, Notice  string
}

func (p *Pages) grantsPage(w http.ResponseWriter, r *http.Request) {
	user, ok := p.authorize(w, r, contracts.ManagePlatform, "")
	if !ok {
		return
	}
	id := r.PathValue("id")
	if !contracts.ValidID(id) {
		p.render.Status(w, http.StatusNotFound, user)
		return
	}
	q := r.URL.Query()
	offsets := [2]int{}
	for i, key := range []string{"offset", "databases_offset"} {
		if raw := q.Get(key); raw != "" {
			value, err := strconv.Atoi(raw)
			if err != nil || value < 0 || value > 100000 {
				p.render.Status(w, http.StatusBadRequest, user)
				return
			}
			offsets[i] = value
		}
	}
	offset, databasesOffset := offsets[0], offsets[1]
	selectedID := q.Get("database")
	if selectedID != "" && !contracts.ValidID(selectedID) {
		p.render.Status(w, http.StatusBadRequest, user)
		return
	}
	principal, _ := p.principal(r.Context())
	group, err := p.groups.Get(r.Context(), principal, id)
	if err != nil {
		p.fail(w, r, user, err)
		return
	}
	grants, more, err := p.groups.Grants(r.Context(), principal, id, adminPageSize, offset)
	if err != nil {
		p.fail(w, r, user, err)
		return
	}
	view := grantsView{
		Group: group, ReturnTo: grantsURL(id, offset, databasesOffset, selectedID),
		Grants: listPage{First: offset + 1, Last: offset + len(grants), Start: grantsURL(id, 0, databasesOffset, "") + "#grants"},
	}
	for _, grant := range grants {
		connection, err := p.databases.Get(r.Context(), grant.DatabaseID)
		if err != nil {
			p.fail(w, r, user, err)
			return
		}
		row := grantRow{
			Database: grantDatabase{ID: connection.ID, Name: connection.Name, Driver: string(connection.Driver), Active: connection.Active},
			Level:    grant.Level, URL: grantsURL(id, offset, databasesOffset, grant.DatabaseID) + "#grant-settings",
		}
		view.Rows = append(view.Rows, row)
		if grant.DatabaseID == selectedID {
			view.Selected = &row
		}
	}
	if selectedID != "" && view.Selected == nil {
		p.render.Status(w, http.StatusNotFound, user)
		return
	}
	databases, databasesMore, err := p.databases.List(r.Context(), adminPageSize, databasesOffset)
	if err != nil {
		p.fail(w, r, user, err)
		return
	}
	for _, connection := range databases {
		view.Candidates = append(view.Candidates, grantDatabase{ID: connection.ID, Name: connection.Name, Driver: string(connection.Driver), Active: connection.Active})
	}
	view.Databases = listPage{First: databasesOffset + 1, Last: databasesOffset + len(databases), Start: grantsURL(id, offset, 0, "") + "#set-grant"}
	if offset > 0 {
		view.Grants.Prev = grantsURL(id, max(0, offset-adminPageSize), databasesOffset, "") + "#grants"
	}
	if more && offset+adminPageSize <= 100000 {
		view.Grants.Next = grantsURL(id, offset+adminPageSize, databasesOffset, "") + "#grants"
	}
	if databasesOffset > 0 {
		view.Databases.Prev = grantsURL(id, offset, max(0, databasesOffset-adminPageSize), "") + "#set-grant"
	}
	if databasesMore && databasesOffset+adminPageSize <= 100000 {
		view.Databases.Next = grantsURL(id, offset, databasesOffset+adminPageSize, "") + "#set-grant"
	}
	view.Notice = map[string]string{
		"saved":   "Hak akses database telah tersimpan.",
		"removed": "Hak akses database dari grup ini telah dicabut. Seluruh sesi anggotanya telah berakhir.",
	}[q.Get("notice")]
	p.render.Render(w, http.StatusOK, "access/grants", Page{
		Title: "Hak akses grup " + group.Name, User: user, Active: "admin", Entry: "grants", Data: view,
	})
}

func grantsURL(groupID string, offset, databasesOffset int, databaseID string) string {
	path := "/admin/groups/" + groupID + "/grants"
	q := url.Values{}
	if offset > 0 {
		q.Set("offset", strconv.Itoa(offset))
	}
	if databasesOffset > 0 {
		q.Set("databases_offset", strconv.Itoa(databasesOffset))
	}
	if databaseID != "" {
		q.Set("database", databaseID)
	}
	if len(q) == 0 {
		return path
	}
	return path + "?" + q.Encode()
}
