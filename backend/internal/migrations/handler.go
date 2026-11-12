package migrations

import (
	"context"
	"net/http"
	"slices"
	"strconv"

	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/contracts"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/platform"
)

// JSON may encode a SQL byte as a six-byte escape. The SQL limit itself is
// checked after decoding each field.
const maxMigrationBody = 12*maxSQLBytes + 64*1024

type Handler struct {
	service   *Service
	principal func(context.Context) (contracts.Principal, bool)
}

func NewHandler(service *Service, principal func(context.Context) (contracts.Principal, bool)) *Handler {
	return &Handler{service: service, principal: principal}
}

func RegisterRoutes(mux *http.ServeMux, h *Handler) {
	const db = "/api/v1/databases/{id}"
	mux.HandleFunc("GET "+db+"/migrations", h.authorize(contracts.ViewDatabase, h.listMigrations))
	mux.HandleFunc("POST "+db+"/migrations", h.authorize(contracts.EditDatabase, h.createMigration))
	mux.HandleFunc("GET "+db+"/migrations/{mid}", h.authorize(contracts.ViewDatabase, h.getMigration))
	mux.HandleFunc("PATCH "+db+"/migrations/{mid}", h.authorize(contracts.EditDatabase, h.patchMigration))
	mux.HandleFunc("GET "+db+"/migrations/{mid}/revisions", h.authorize(contracts.ViewDatabase, h.listRevisions))
	mux.HandleFunc("POST "+db+"/migrations/{mid}/requests", h.authorize(contracts.EditDatabase, h.submitRequest))
	mux.HandleFunc("GET "+db+"/change-requests", h.authorize(contracts.ViewDatabase, h.listRequests))
	mux.HandleFunc("GET "+db+"/change-requests/{rid}", h.authorize(contracts.ViewDatabase, h.getRequest))
	mux.HandleFunc("POST "+db+"/change-requests/{rid}/cancel", h.authorize(contracts.EditDatabase, h.cancelRequest))
}

type authorizedHandler func(http.ResponseWriter, *http.Request, contracts.Principal, string)

func (h *Handler) authorize(action contracts.Action, next authorizedHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		actor, ok := h.principal(r.Context())
		if !ok || actor.UserID == "" {
			_ = platform.WriteError(w, r, problem("UNAUTHENTICATED", "Silakan login untuk melanjutkan."))
			return
		}
		databaseID := r.PathValue("id")
		if err := h.service.require(r.Context(), actor, action, databaseID); err != nil {
			_ = platform.WriteError(w, r, err)
			return
		}
		if mid := r.PathValue("mid"); mid != "" && !contracts.ValidID(mid) {
			_ = platform.WriteError(w, r, migrationNotFound())
			return
		}
		if rid := r.PathValue("rid"); rid != "" && !contracts.ValidID(rid) {
			_ = platform.WriteError(w, r, requestNotFound())
			return
		}
		next(w, r, actor, databaseID)
	}
}

func (h *Handler) createMigration(w http.ResponseWriter, r *http.Request, actor contracts.Principal, databaseID string) {
	var in createInput
	if err := platform.DecodeJSON(w, r, &in, maxMigrationBody); err != nil {
		_ = platform.WriteError(w, r, err)
		return
	}
	m, err := h.service.createMigration(r.Context(), actor, databaseID, in)
	writeResult(w, r, http.StatusCreated, m, err)
}

func (h *Handler) getMigration(w http.ResponseWriter, r *http.Request, _ contracts.Principal, databaseID string) {
	m, err := h.service.getMigration(r.Context(), databaseID, r.PathValue("mid"))
	writeResult(w, r, http.StatusOK, m, err)
}

func (h *Handler) patchMigration(w http.ResponseWriter, r *http.Request, actor contracts.Principal, databaseID string) {
	var in patchInput
	if err := platform.DecodeJSON(w, r, &in, maxMigrationBody); err != nil {
		_ = platform.WriteError(w, r, err)
		return
	}
	m, err := h.service.patchMigration(r.Context(), actor, databaseID, r.PathValue("mid"), in)
	writeResult(w, r, http.StatusOK, m, err)
}

func (h *Handler) listMigrations(w http.ResponseWriter, r *http.Request, _ contracts.Principal, databaseID string) {
	status := r.URL.Query().Get("status")
	if status != "" && !slices.Contains([]string{"pending", "applied", "rolled_back", "superseded", "partial"}, status) {
		_ = platform.WriteError(w, r, invalidField("status", "Status migrasi tidak valid."))
		return
	}
	page, err := parsePage(r)
	if err != nil {
		_ = platform.WriteError(w, r, err)
		return
	}
	items, err := h.service.listMigrations(r.Context(), databaseID, status, page.Limit+1, page.Offset)
	writePage(w, r, page, items, err)
}

func (h *Handler) listRevisions(w http.ResponseWriter, r *http.Request, _ contracts.Principal, databaseID string) {
	items, err := h.service.listRevisions(r.Context(), databaseID, r.PathValue("mid"))
	writeResult(w, r, http.StatusOK, items, err)
}

func (h *Handler) submitRequest(w http.ResponseWriter, r *http.Request, actor contracts.Principal, databaseID string) {
	var in requestInput
	if err := platform.DecodeJSON(w, r, &in, 16*1024); err != nil {
		_ = platform.WriteError(w, r, err)
		return
	}
	request, err := h.service.submitRequest(r.Context(), actor, databaseID, r.PathValue("mid"), in)
	writeResult(w, r, http.StatusCreated, request, err)
}

func (h *Handler) listRequests(w http.ResponseWriter, r *http.Request, _ contracts.Principal, databaseID string) {
	status := r.URL.Query().Get("status")
	if status != "" && !slices.Contains([]string{"pending_review", "approved", "rejected", "cancelled", "executed"}, status) {
		_ = platform.WriteError(w, r, invalidField("status", "Status change request tidak valid."))
		return
	}
	migrationID := r.URL.Query().Get("migration_id")
	if migrationID != "" && !contracts.ValidID(migrationID) {
		_ = platform.WriteError(w, r, invalidField("migration_id", "ID migrasi tidak valid."))
		return
	}
	page, err := parsePage(r)
	if err != nil {
		_ = platform.WriteError(w, r, err)
		return
	}
	items, err := h.service.listRequests(r.Context(), databaseID, status, migrationID, page.Limit+1, page.Offset)
	writePage(w, r, page, items, err)
}

func (h *Handler) getRequest(w http.ResponseWriter, r *http.Request, _ contracts.Principal, databaseID string) {
	request, err := h.service.getRequest(r.Context(), databaseID, r.PathValue("rid"))
	writeResult(w, r, http.StatusOK, request, err)
}

func (h *Handler) cancelRequest(w http.ResponseWriter, r *http.Request, actor contracts.Principal, databaseID string) {
	if err := platform.DecodeJSON(w, r, &struct{}{}, 1024); err != nil {
		_ = platform.WriteError(w, r, err)
		return
	}
	request, err := h.service.cancelRequest(r.Context(), actor, databaseID, r.PathValue("rid"))
	writeResult(w, r, http.StatusOK, request, err)
}

func parsePage(r *http.Request) (platform.Page, error) {
	page := platform.Page{Limit: 50}
	query := r.URL.Query()
	for _, field := range []struct {
		name     string
		value    *int
		min, max int
		message  string
	}{
		{"limit", &page.Limit, 1, 200, "Gunakan bilangan bulat 1 sampai 200."},
		{"offset", &page.Offset, 0, 100000, "Gunakan bilangan bulat 0 sampai 100000."},
	} {
		if raw := query.Get(field.name); raw != "" {
			n, err := strconv.Atoi(raw)
			if err != nil || n < field.min || n > field.max {
				return platform.Page{}, invalidField(field.name, field.message)
			}
			*field.value = n
		}
	}
	return page, nil
}

func writeResult(w http.ResponseWriter, r *http.Request, status int, data any, err error) {
	if err != nil {
		_ = platform.WriteError(w, r, err)
		return
	}
	_ = platform.WriteJSON(w, r, status, data, nil)
}

func writePage[T any](w http.ResponseWriter, r *http.Request, page platform.Page, items []T, err error) {
	if err != nil {
		_ = platform.WriteError(w, r, err)
		return
	}
	if len(items) > page.Limit {
		page.HasMore = true
		items = items[:page.Limit]
	}
	_ = platform.WriteList(w, r, items, page)
}
