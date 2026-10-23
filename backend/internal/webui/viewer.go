package webui

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"

	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/connections"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/contracts"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/database"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/viewer"
)

var accessLabels = map[contracts.AccessLevel]string{
	contracts.AccessNone: "tanpa akses", contracts.AccessView: "lihat", contracts.AccessEdit: "lihat dan edit",
}

type databaseItem struct {
	connections.Database
	Access string
}

func (p *Pages) databaseList(w http.ResponseWriter, r *http.Request) {
	user, ok := p.authorize(w, r, "", "")
	if !ok {
		return
	}
	principal, _ := p.principal(r.Context())
	dbs, err := p.databases.Visible(r.Context(), p.access, principal.UserID)
	if err != nil {
		p.fail(w, r, user, err)
		return
	}
	items := make([]databaseItem, len(dbs))
	for i, db := range dbs {
		items[i] = databaseItem{db, accessLabels[db.EffectiveAccess]}
	}
	p.render.Render(w, http.StatusOK, "viewer/databases", Page{Title: "Database", User: user, Active: "databases", Data: items})
}

type databaseView struct {
	DB            databaseItem
	Fingerprint   string
	Schemas       []schemaLinks
	SchemaProblem string // the target could not be inspected; nothing else is shown
	Selected      *selectedTable
}

type schemaLinks struct {
	Name   string
	Tables []tableLink
}

type tableLink struct {
	Name, URL string
	Current   bool
}

type selectedTable struct {
	Schema      string
	Table       database.Table
	Data        *dataView
	DataProblem string // the rows could not be read, e.g. the target role may not read this table
}

type dataView struct {
	Columns     []database.DataColumn
	Rows        [][]cell
	OrderStable bool
	First, Last int
	Prev, Next  string // page URLs, empty when there is no such page
}

// cell is one value as text: NULL stays distinct from an empty string and
// binary columns stay base64, so nothing is reinterpreted on the way to HTML.
type cell struct {
	Text         string
	Null, Binary bool
}

func (p *Pages) databaseDetail(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	user, ok := p.authorize(w, r, contracts.ViewDatabase, id)
	if !ok {
		return
	}
	q := r.URL.Query()
	schema, table := q.Get("schema"), q.Get("table")
	offset := 0
	if v := q.Get("offset"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 || n > viewer.MaxOffset {
			p.render.Status(w, http.StatusBadRequest, user)
			return
		}
		offset = n
	}
	principal, _ := p.principal(r.Context())
	db, err := p.databases.Database(r.Context(), p.access, principal.UserID, id)
	if err != nil {
		p.targetFailure(w, r, user, err)
		return
	}
	view := databaseView{DB: databaseItem{db, accessLabels[db.EffectiveAccess]}}
	page := Page{Title: db.Name, User: user, Active: "databases", Data: &view}
	snap, err := p.schemas.Schema(r.Context(), id)
	if err != nil {
		status, message := targetStatus(err)
		if status == http.StatusNotFound {
			p.render.Status(w, status, user)
			return
		}
		view.SchemaProblem = message
		p.render.Render(w, status, "viewer/database", page)
		return
	}
	view.Fingerprint = snap.Fingerprint
	for _, s := range snap.Schemas {
		links := schemaLinks{Name: s.Name}
		for _, t := range s.Tables {
			current := s.Name == schema && t.Name == table
			links.Tables = append(links.Tables, tableLink{Name: t.Name, URL: tableURL(id, s.Name, t.Name, 0), Current: current})
			if current {
				view.Selected = &selectedTable{Schema: s.Name, Table: t}
			}
		}
		view.Schemas = append(view.Schemas, links)
	}
	if table != "" && view.Selected == nil {
		p.render.Status(w, http.StatusNotFound, user)
		return
	}
	if view.Selected != nil {
		data, pg, err := p.schemas.Data(r.Context(), id, schema, table, viewer.DefaultLimit, offset)
		if err != nil {
			_, view.Selected.DataProblem = targetStatus(err)
		} else {
			view.Selected.Data = newDataView(data, pg, tableURL(id, schema, table, max(0, offset-pg.Limit)), tableURL(id, schema, table, offset+pg.Limit))
		}
	}
	p.render.Render(w, http.StatusOK, "viewer/database", page)
}

func newDataView(data database.TableData, pg viewer.Page, prev, next string) *dataView {
	v := &dataView{Columns: data.Columns, OrderStable: data.OrderStable, First: pg.Offset + 1, Last: pg.Offset + len(data.Rows)}
	if pg.Offset > 0 {
		v.Prev = prev
	}
	if pg.HasMore {
		v.Next = next
	}
	for _, row := range data.Rows {
		cells := make([]cell, len(row))
		for i, value := range row {
			cells[i] = newCell(value, i < len(data.Columns) && data.Columns[i].Encoding == database.EncBase64)
		}
		v.Rows = append(v.Rows, cells)
	}
	return v
}

func newCell(value any, binary bool) cell {
	switch v := value.(type) {
	case nil:
		return cell{Null: true}
	case string:
		return cell{Text: v, Binary: binary}
	case bool, int, int32, int64, float32, float64, json.Number:
		return cell{Text: fmt.Sprint(v)}
	}
	// JSON documents and arrays are shown as their JSON text, never as markup.
	text, err := json.Marshal(value)
	if err != nil {
		return cell{Text: fmt.Sprint(value)}
	}
	return cell{Text: string(text)}
}

func tableURL(id, schema, table string, offset int) string {
	q := url.Values{"schema": {schema}, "table": {table}}
	if offset > 0 {
		q.Set("offset", strconv.Itoa(offset))
	}
	return "/databases/" + id + "?" + q.Encode()
}

// targetStatus maps viewer and target errors to a page status and a fixed
// message that never carries engine, host or DSN detail.
func targetStatus(err error) (int, string) {
	switch {
	case errors.Is(err, database.ErrNotFound):
		return http.StatusNotFound, "Database atau tabel tidak ditemukan."
	case errors.Is(err, viewer.ErrInvalid):
		return http.StatusBadRequest, "Parameter viewer tidak valid."
	case errors.Is(err, database.ErrTargetPermission):
		return http.StatusForbidden, "Akses ditolak: role target tidak berhak membaca tabel ini."
	case errors.Is(err, database.ErrTargetTimeout):
		return http.StatusGatewayTimeout, "Target tidak merespons dalam batas waktu."
	case errors.Is(err, database.ErrTargetUnavailable):
		return http.StatusBadGateway, "Target tidak dapat dihubungi."
	}
	return http.StatusInternalServerError, "Terjadi kesalahan internal."
}

// targetFailure renders a whole-page failure: policy errors as in fail, a
// missing or inactive database as 404.
func (p *Pages) targetFailure(w http.ResponseWriter, r *http.Request, user *PageUser, err error) {
	var domain *contracts.DomainError
	if errors.As(err, &domain) {
		p.fail(w, r, user, err)
		return
	}
	status, _ := targetStatus(err)
	p.render.Status(w, status, user)
}
