package schemadiff

import (
	"context"
	"net/http"

	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/contracts"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/platform"
)

type PrincipalFunc func(context.Context) (contracts.Principal, bool)

type Handler struct {
	service   *Service
	principal PrincipalFunc
}

func NewHandler(service *Service, principal PrincipalFunc) *Handler {
	if principal == nil {
		principal = func(context.Context) (contracts.Principal, bool) { return contracts.Principal{}, false }
	}
	return &Handler{service, principal}
}

func RegisterRoutes(mux *http.ServeMux, h *Handler) {
	mux.HandleFunc("POST /api/v1/databases/{id}/schema-diff", h.diff)
}

func (h *Handler) diff(w http.ResponseWriter, r *http.Request) {
	p, ok := h.principal(r.Context())
	if !ok {
		_ = platform.WriteError(w, r, problem("UNAUTHENTICATED", "Silakan login untuk melanjutkan."))
		return
	}
	id := r.PathValue("id")
	if !uuidPattern.MatchString(id) {
		_ = platform.WriteError(w, r, problem("INVALID_REQUEST", "ID database harus UUID huruf kecil."))
		return
	}
	if err := h.service.access.Require(r.Context(), p, contracts.EditDatabase, id); err != nil {
		_ = platform.WriteError(w, r, domain(err))
		return
	}
	var body struct {
		DesiredDatabaseID *string `json:"desired_database_id"`
		Schema            *string `json:"schema"`
	}
	if err := platform.DecodeJSON(w, r, &body, 0); err != nil {
		_ = platform.WriteError(w, r, err)
		return
	}
	if body.DesiredDatabaseID == nil || body.Schema == nil {
		_ = platform.WriteError(w, r, problem("INVALID_REQUEST", "desired_database_id dan schema wajib diisi."))
		return
	}
	result, err := h.service.Diff(r.Context(), p, id, DiffInput{*body.DesiredDatabaseID, *body.Schema})
	if err != nil {
		_ = platform.WriteError(w, r, domain(err))
		return
	}
	_ = platform.WriteJSON(w, r, http.StatusOK, result, nil)
}
