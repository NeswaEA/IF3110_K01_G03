package webui

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/connections"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/contracts"
)

type diffInitial struct {
	Database          connections.Database   `json:"database"`
	Comparisons       []connections.Database `json:"comparisons"`
	Schemas           []string               `json:"schemas"`
	DesiredDatabaseID string                 `json:"desired_database_id"`
	Schema            string                 `json:"schema"`
	Problem           string                 `json:"problem"`
}

type diffView struct {
	diffInitial
	Initial string
}

func (p *Pages) schemaDiff(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	user, ok := p.authorize(w, r, contracts.EditDatabase, id)
	if !ok {
		return
	}
	query := r.URL.Query()
	desired, schema := query.Get("desired_database_id"), query.Get("schema")
	if len(query["desired_database_id"]) > 1 || len(query["schema"]) > 1 || desired != "" && !contracts.ValidID(desired) || !utf8.ValidString(schema) || strings.ContainsRune(schema, 0) {
		p.render.Status(w, http.StatusBadRequest, user)
		return
	}
	principal, _ := p.principal(r.Context())
	db, err := p.databases.Database(r.Context(), p.access, principal.UserID, id)
	if err != nil {
		p.targetFailure(w, r, user, err)
		return
	}
	if db.EffectiveAccess != contracts.AccessEdit {
		p.render.Status(w, http.StatusForbidden, user)
		return
	}
	visible, err := p.databases.Visible(r.Context(), p.access, principal.UserID)
	if err != nil {
		p.fail(w, r, user, err)
		return
	}
	initial := diffInitial{Database: db, Comparisons: []connections.Database{}, Schemas: []string{}, DesiredDatabaseID: desired, Schema: schema}
	for _, other := range visible {
		if other.ID != id && other.Driver == db.Driver && (other.EffectiveAccess == contracts.AccessView || other.EffectiveAccess == contracts.AccessEdit) {
			initial.Comparisons = append(initial.Comparisons, other)
		}
	}
	sort.Slice(initial.Comparisons, func(i, j int) bool {
		if initial.Comparisons[i].Name != initial.Comparisons[j].Name {
			return initial.Comparisons[i].Name < initial.Comparisons[j].Name
		}
		return initial.Comparisons[i].ID < initial.Comparisons[j].ID
	})
	if desired != "" {
		found := false
		for _, other := range initial.Comparisons {
			found = found || other.ID == desired
		}
		if !found {
			p.render.Status(w, http.StatusNotFound, user)
			return
		}
	} else if len(initial.Comparisons) > 0 {
		initial.DesiredDatabaseID = initial.Comparisons[0].ID
	}
	switch {
	case !db.Capabilities.SchemaDiff:
		initial.Problem = "Schema diff belum tersedia untuk koneksi ini. Untuk PostgreSQL, periksa pemasangan Atlas pada server."
	case !db.Capabilities.TransactionalMigrations:
		initial.Problem = "Admin perlu menguji koneksi source dan menyiapkan ledger sebelum membuat perbandingan."
	default:
		snap, err := p.schemas.Schema(r.Context(), id)
		if err != nil {
			_, initial.Problem = targetStatus(err)
		} else {
			for _, item := range snap.Schemas {
				initial.Schemas = append(initial.Schemas, item.Name)
			}
			sort.Strings(initial.Schemas)
			if schema != "" {
				found := false
				for _, item := range initial.Schemas {
					found = found || item == schema
				}
				if !found {
					p.render.Status(w, http.StatusNotFound, user)
					return
				}
			} else if len(initial.Schemas) > 0 {
				initial.Schema = initial.Schemas[0]
				for _, item := range initial.Schemas {
					if item == "public" || item == "main" {
						initial.Schema = item
						break
					}
				}
			}
		}
	}
	encoded, err := json.Marshal(initial)
	if err != nil {
		p.render.Status(w, http.StatusInternalServerError, user)
		return
	}
	p.render.Render(w, http.StatusOK, "schemadiff/index", Page{Title: "Perbandingan schema", User: user, Active: "databases", Entry: "schemadiff", Data: diffView{initial, string(encoded)}})
}
