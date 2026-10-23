package webui

import (
	"fmt"
	"net/http"

	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/connections"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/contracts"
)

// connectionListLimit matches the page size the connections widget loads.
const connectionListLimit = 200

type connectionRow struct {
	Name, Driver, Target, Status string
}

type connectionsView struct {
	Rows    []connectionRow
	HasMore bool
}

// connectionsPage renders the registry as a plain table; the widget replaces it
// with the interactive version (create, edit, test, deactivate) once loaded.
func (p *Pages) connectionsPage(w http.ResponseWriter, r *http.Request) {
	user, ok := p.authorize(w, r, contracts.ManagePlatform, "")
	if !ok {
		return
	}
	list, more, err := p.databases.List(r.Context(), connectionListLimit, 0)
	if err != nil {
		p.render.Status(w, http.StatusInternalServerError, user)
		return
	}
	view := connectionsView{HasMore: more}
	for _, c := range list {
		view.Rows = append(view.Rows, connectionRow{Name: c.Name, Driver: string(c.Driver), Target: connectionTarget(c), Status: connectionStatus(c)})
	}
	p.render.Render(w, http.StatusOK, "connections/index", Page{Title: "Koneksi database", User: user, Active: "connections", Entry: "connections", Data: view})
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
