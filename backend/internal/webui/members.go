package webui

import (
	"net/http"
	"net/url"
	"strconv"

	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/access"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/contracts"
)

type memberRow struct {
	access.User
	URL string
}

type listPage struct {
	First, Last       int
	Start, Prev, Next string
}

type membersView struct {
	Group            access.Group
	Rows             []memberRow
	Candidates       []access.User
	Selected         *access.User
	Self             bool
	Members, Users   listPage
	ReturnTo, Notice string
}

func (p *Pages) membersPage(w http.ResponseWriter, r *http.Request) {
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
	for i, key := range []string{"offset", "users_offset"} {
		if raw := q.Get(key); raw != "" {
			value, err := strconv.Atoi(raw)
			if err != nil || value < 0 || value > 100000 {
				p.render.Status(w, http.StatusBadRequest, user)
				return
			}
			offsets[i] = value
		}
	}
	offset, usersOffset := offsets[0], offsets[1]
	selectedID := q.Get("member")
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
	members, more, err := p.groups.Members(r.Context(), principal, id, adminPageSize, offset)
	if err != nil {
		p.fail(w, r, user, err)
		return
	}
	view := membersView{
		Group: group, ReturnTo: membersURL(id, offset, usersOffset, selectedID),
		Members: listPage{First: offset + 1, Last: offset + len(members), Start: membersURL(id, 0, usersOffset, "") + "#members"},
	}
	for _, member := range members {
		view.Rows = append(view.Rows, memberRow{User: member, URL: membersURL(id, offset, usersOffset, member.ID) + "#remove-member"})
		if member.ID == selectedID {
			view.Selected = &member
			view.Self = member.ID == principal.UserID
		}
	}
	if selectedID != "" && view.Selected == nil {
		p.render.Status(w, http.StatusNotFound, user)
		return
	}
	users, usersMore, err := p.users.List(r.Context(), principal, adminPageSize, usersOffset)
	if err != nil {
		p.fail(w, r, user, err)
		return
	}
	view.Candidates = users
	view.Users = listPage{First: usersOffset + 1, Last: usersOffset + len(users), Start: membersURL(id, offset, 0, "") + "#add-member"}
	if offset > 0 {
		view.Members.Prev = membersURL(id, max(0, offset-adminPageSize), usersOffset, "") + "#members"
	}
	if more && offset+adminPageSize <= 100000 {
		view.Members.Next = membersURL(id, offset+adminPageSize, usersOffset, "") + "#members"
	}
	if usersOffset > 0 {
		view.Users.Prev = membersURL(id, offset, max(0, usersOffset-adminPageSize), "") + "#add-member"
	}
	if usersMore && usersOffset+adminPageSize <= 100000 {
		view.Users.Next = membersURL(id, offset, usersOffset+adminPageSize, "") + "#add-member"
	}
	view.Notice = map[string]string{
		"added":   "Keanggotaan pengguna telah tersimpan.",
		"removed": "Keanggotaan pengguna telah dihapus.",
	}[q.Get("notice")]
	p.render.Render(w, http.StatusOK, "access/members", Page{
		Title: "Anggota grup " + group.Name, User: user, Active: "admin", Entry: "members", Data: view,
	})
}

func membersURL(groupID string, offset, usersOffset int, memberID string) string {
	path := "/admin/groups/" + groupID + "/members"
	q := url.Values{}
	if offset > 0 {
		q.Set("offset", strconv.Itoa(offset))
	}
	if usersOffset > 0 {
		q.Set("users_offset", strconv.Itoa(usersOffset))
	}
	if memberID != "" {
		q.Set("member", memberID)
	}
	if len(q) == 0 {
		return path
	}
	return path + "?" + q.Encode()
}
