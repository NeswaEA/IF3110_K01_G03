package webui

import (
	"fmt"
	"net/http"
	"strconv"

	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/connections"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/contracts"
)

// connectionListLimit matches the page size the connections widget loads.
const connectionListLimit = 200

type connectionRow struct {
	Name, Driver, Target, Status string
}

type connectionsView struct {
	Rows        []connectionRow
	First, Last int
	Prev, Next  string
}

// connectionsPage renders the registry as a plain table; the widget replaces it
// with the interactive version (create, edit, test, deactivate) once loaded.
func (p *Pages) connectionsPage(w http.ResponseWriter, r *http.Request) {
	user, ok := p.authorize(w, r, contracts.ManagePlatform, "")
	if !ok {
		return
	}
	offset := 0
	if raw := r.URL.Query().Get("offset"); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 0 || value > 100000 {
			p.render.Status(w, http.StatusBadRequest, user)
			return
		}
		offset = value
	}
	list, more, err := p.databases.List(r.Context(), connectionListLimit, offset)
	if err != nil {
		p.render.Status(w, http.StatusInternalServerError, user)
		return
	}
	view := connectionsView{First: offset + 1, Last: offset + len(list)}
	if offset > 0 {
		view.Prev = connectionsURL(max(0, offset-connectionListLimit))
	}
	if more && offset+connectionListLimit <= 100000 {
		view.Next = connectionsURL(offset + connectionListLimit)
	}
	for _, c := range list {
		view.Rows = append(view.Rows, connectionRow{Name: c.Name, Driver: string(c.Driver), Target: connectionTarget(c), Status: connectionStatus(c)})
	}
	p.render.Render(w, http.StatusOK, "connections/index", Page{Title: "Koneksi database", User: user, Active: "connections", Entry: "connections", Data: view})
}

func connectionsURL(offset int) string {
	if offset == 0 {
		return "/admin/connections"
	}
	return "/admin/connections?offset=" + strconv.Itoa(offset)
}

func connectionTarget(c connections.AdminConnection) string {
	switch cfg := c.Config.(type) {
	case connections.PublicSQLiteConfig:
		return cfg.File
	case connections.PublicPostgresConfig:
		return fmt.Sprintf("%s@%s:%d/%s (sslmode %s)", cfg.Username, cfg.Host, cfg.Port, cfg.Database, cfg.SSLMode)
	}
	return ""
}

func connectionStatus(c connections.AdminConnection) string {
	switch {
	case !c.Active:
		return "Nonaktif"
	case c.MigrationReady:
		return "Aktif, siap migrasi"
	case c.LastErrorCode != nil:
		return "Aktif, uji terakhir gagal (" + *c.LastErrorCode + ")"
	}
	return "Aktif, belum diuji"
}
