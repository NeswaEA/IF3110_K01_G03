package review

import (
	"context"
	"net/http"

	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/contracts"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/platform"
)

// RenderRequest returns the ChangeRequest DTO, which the migrations module owns.
type RenderRequest func(ctx context.Context, actor contracts.Principal, databaseID, requestID string) (any, error)

type Handler struct {
	reviews   *Service
	access    contracts.AccessChecker
	principal func(context.Context) (contracts.Principal, bool)
	render    RenderRequest
}

func NewHandler(reviews *Service, access contracts.AccessChecker, principal func(context.Context) (contracts.Principal, bool), render RenderRequest) *Handler {
	return &Handler{reviews: reviews, access: access, principal: principal, render: render}
}

func RegisterRoutes(mux *http.ServeMux, h *Handler) {
	mux.HandleFunc("POST /api/v1/databases/{id}/change-requests/{rid}/approve", h.approve)
	mux.HandleFunc("POST /api/v1/databases/{id}/change-requests/{rid}/reject", h.reject)
}

func (h *Handler) approve(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Comment *string `json:"comment"`
	}
	h.handle(w, r, &body, func(actor contracts.Principal, databaseID, requestID string) error {
		return h.reviews.Approve(r.Context(), actor, databaseID, requestID, body.Comment)
	})
}

func (h *Handler) reject(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Comment string `json:"comment"`
	}
	h.handle(w, r, &body, func(actor contracts.Principal, databaseID, requestID string) error {
		return h.reviews.Reject(r.Context(), actor, databaseID, requestID, body.Comment)
	})
}

func (h *Handler) handle(w http.ResponseWriter, r *http.Request, body any, decide func(contracts.Principal, string, string) error) {
	actor, ok := h.principal(r.Context())
	if !ok || actor.UserID == "" {
		_ = platform.WriteError(w, r, &contracts.DomainError{Code: "UNAUTHENTICATED", Message: "Silakan login untuk melanjutkan."})
		return
	}
	databaseID, requestID := r.PathValue("id"), r.PathValue("rid")
	if !contracts.ValidID(databaseID) || !contracts.ValidID(requestID) {
		_ = platform.WriteError(w, r, notFound())
		return
	}
	// Without view access the database stays hidden; leading a granted group implies view access.
	if err := h.access.Require(r.Context(), actor, contracts.ViewDatabase, databaseID); err != nil {
		_ = platform.WriteError(w, r, err)
		return
	}
	if err := platform.DecodeJSON(w, r, body, 0); err != nil {
		_ = platform.WriteError(w, r, err)
		return
	}
	if err := decide(actor, databaseID, requestID); err != nil {
		_ = platform.WriteError(w, r, err)
		return
	}
	request, err := h.render(r.Context(), actor, databaseID, requestID)
	if err != nil {
		_ = platform.WriteError(w, r, err)
		return
	}
	_ = platform.WriteJSON(w, r, http.StatusOK, request, nil)
}
