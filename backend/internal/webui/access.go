package webui

import (
	"context"
	"net/http"
	"net/url"
	"strconv"

	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/access"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/contracts"
)

type UserSource interface {
	List(context.Context, contracts.Principal, int, int) ([]access.User, bool, error)
}

const userPageSize = 50

type userRow struct {
	access.User
	URL              string
	Created, Updated string
}

type usersView struct {
	Rows               []userRow
	Selected           *access.User
	Self               bool
	First, Last        int
	Prev, Next, Notice string
	ReturnTo           string
}

func (p *Pages) adminPage(w http.ResponseWriter, r *http.Request) {
	if _, ok := p.authorize(w, r, contracts.ManagePlatform, ""); ok {
		http.Redirect(w, r, "/admin/users", http.StatusFound)
	}
}

func (p *Pages) usersPage(w http.ResponseWriter, r *http.Request) {
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
	selectedID := q.Get("user")
	if selectedID != "" && !uuidRe.MatchString(selectedID) {
		p.render.Status(w, http.StatusBadRequest, user)
		return
	}
	principal, _ := p.principal(r.Context())
	users, more, err := p.users.List(r.Context(), principal, userPageSize, offset)
	if err != nil {
		p.fail(w, r, user, err)
		return
	}
	view := usersView{First: offset + 1, Last: offset + len(users), ReturnTo: usersURL(offset, selectedID)}
	for _, account := range users {
		view.Rows = append(view.Rows, userRow{
			User: account, URL: usersURL(offset, account.ID) + "#user-settings",
			Created: account.CreatedAt.UTC().Format("02 Jan 2006 15:04 UTC"),
			Updated: account.UpdatedAt.UTC().Format("02 Jan 2006 15:04 UTC"),
		})
		if account.ID == selectedID {
			view.Selected = &account
			view.Self = account.ID == principal.UserID
		}
	}
	if selectedID != "" && view.Selected == nil {
		p.render.Status(w, http.StatusNotFound, user)
		return
	}
	if offset > 0 {
		view.Prev = usersURL(max(0, offset-userPageSize), "")
	}
	if more && offset+userPageSize <= 100000 {
		view.Next = usersURL(offset+userPageSize, "")
	}
	view.Notice = map[string]string{
		"created":  "Pengguna baru berhasil dibuat.",
		"updated":  "Status dan peran pengguna berhasil diperbarui.",
		"password": "Password berhasil diganti. Seluruh sesi akun tersebut telah dicabut.",
	}[q.Get("notice")]
	p.render.Render(w, http.StatusOK, "access/users", Page{
		Title: "Administrasi pengguna", User: user, Active: "admin", Entry: "users", Data: view,
	})
}

func usersURL(offset int, userID string) string {
	q := url.Values{}
	if offset > 0 {
		q.Set("offset", strconv.Itoa(offset))
	}
	if userID != "" {
		q.Set("user", userID)
	}
	if len(q) == 0 {
		return "/admin/users"
	}
	return "/admin/users?" + q.Encode()
}
