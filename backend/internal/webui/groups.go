package webui

import (
	"context"
	"net/http"
	"net/url"
	"strconv"

	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/access"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/contracts"
)

type GroupSource interface {
	List(context.Context, contracts.Principal, int, int) ([]access.Group, bool, error)
	Get(context.Context, contracts.Principal, string) (access.Group, error)
	Members(context.Context, contracts.Principal, string, int, int) ([]access.User, bool, error)
}

type groupRow struct {
	access.Group
	URL              string
	Created, Updated string
}

type groupsView struct {
	Rows               []groupRow
	Selected           *access.Group
	First, Last        int
	Prev, Next, Notice string
	ReturnTo           string
}

func (p *Pages) groupsPage(w http.ResponseWriter, r *http.Request) {
	user, ok := p.authorize(w, r, contracts.ManagePlatform, "")
	if !ok {
		return
	}
	q := r.URL.Query()
	offset := 0
	if raw := q.Get("offset"); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 0 || value > 100000 {
			p.render.Status(w, http.StatusBadRequest, user)
			return
		}
		offset = value
	}
	selectedID := q.Get("group")
	if selectedID != "" && !uuidRe.MatchString(selectedID) {
		p.render.Status(w, http.StatusBadRequest, user)
		return
	}
	principal, _ := p.principal(r.Context())
	groups, more, err := p.groups.List(r.Context(), principal, adminPageSize, offset)
	if err != nil {
		p.fail(w, r, user, err)
		return
	}
	view := groupsView{First: offset + 1, Last: offset + len(groups), ReturnTo: groupsURL(offset, selectedID)}
	for _, group := range groups {
		view.Rows = append(view.Rows, groupRow{
			Group: group, URL: groupsURL(offset, group.ID) + "#group-settings",
			Created: group.CreatedAt.UTC().Format("02 Jan 2006 15:04 UTC"),
			Updated: group.UpdatedAt.UTC().Format("02 Jan 2006 15:04 UTC"),
		})
		if group.ID == selectedID {
			view.Selected = &group
		}
	}
	if selectedID != "" && view.Selected == nil {
		p.render.Status(w, http.StatusNotFound, user)
		return
	}
	if offset > 0 {
		view.Prev = groupsURL(max(0, offset-adminPageSize), "")
	}
	if more && offset+adminPageSize <= 100000 {
		view.Next = groupsURL(offset+adminPageSize, "")
	}
	view.Notice = map[string]string{
		"created": "Grup baru berhasil dibuat.",
		"updated": "Nama grup berhasil diperbarui.",
		"deleted": "Grup berhasil dihapus. Seluruh sesi anggotanya telah dicabut.",
	}[q.Get("notice")]
	p.render.Render(w, http.StatusOK, "access/groups", Page{
		Title: "Administrasi grup", User: user, Active: "admin", Entry: "groups", Data: view,
	})
}

func groupsURL(offset int, groupID string) string {
	q := url.Values{}
	if offset > 0 {
		q.Set("offset", strconv.Itoa(offset))
	}
	if groupID != "" {
		q.Set("group", groupID)
	}
	if len(q) == 0 {
		return "/admin/groups"
	}
	return "/admin/groups?" + q.Encode()
}
