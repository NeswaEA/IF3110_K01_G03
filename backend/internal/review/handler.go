package review

import (
	"context"
	"fmt"
	"net/http"
	"strconv"

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
	pending   PendingRequests
}

func NewHandler(reviews *Service, access contracts.AccessChecker, principal func(context.Context) (contracts.Principal, bool), render RenderRequest, pending PendingRequests) *Handler {
	return &Handler{reviews: reviews, access: access, principal: principal, render: render, pending: pending}
}

func RegisterRoutes(mux *http.ServeMux, h *Handler) {
	mux.HandleFunc("POST /api/v1/databases/{id}/change-requests/{rid}/approve", h.approve)
	mux.HandleFunc("POST /api/v1/databases/{id}/change-requests/{rid}/reject", h.reject)
	mux.HandleFunc("GET /api/v1/reviews", h.inbox)
}

type reviewItem struct {
	Request  any        `json:"request"`
	Database NamedRef   `json:"database"`
	MyGroups []NamedRef `json:"my_groups"`
}

func (h *Handler) inbox(w http.ResponseWriter, r *http.Request) {
	actor, ok := h.principal(r.Context())
	if !ok || actor.UserID == "" {
		_ = platform.WriteError(w, r, &contracts.DomainError{Code: "UNAUTHENTICATED", Message: "Silakan login untuk melanjutkan."})
		return
	}
	page, err := parsePage(r)
	if err != nil {
		_ = platform.WriteError(w, r, err)
		return
	}
	// shortcut: the whole inbox is evaluated and paged in memory; fine for a course deployment, page in SQL if inboxes grow large.
	entries, err := h.reviews.Inbox(r.Context(), actor, h.pending)
	if err != nil {
		_ = platform.WriteError(w, r, err)
		return
	}
	entries = entries[min(page.Offset, len(entries)):]
	if len(entries) > page.Limit {
		page.HasMore = true
		entries = entries[:page.Limit]
	}
	items := make([]reviewItem, 0, len(entries))
	for _, e := range entries {
		request, err := h.render(r.Context(), actor, e.Database.ID, e.RequestID)
		if err != nil {
			_ = platform.WriteError(w, r, err)
			return
		}
		items = append(items, reviewItem{Request: request, Database: e.Database, MyGroups: e.MyGroups})
	}
	_ = platform.WriteList(w, r, items, page)
}

func parsePage(r *http.Request) (platform.Page, error) {
	page := platform.Page{Limit: 50}
	for name, field := range map[string]struct {
		value    *int
		min, max int
	}{"limit": {&page.Limit, 1, 100}, "offset": {&page.Offset, 0, 100000}} {
		if raw := r.URL.Query().Get(name); raw != "" {
			n, err := strconv.Atoi(raw)
			if err != nil || n < field.min || n > field.max {
				return platform.Page{}, &contracts.DomainError{Code: "INVALID_REQUEST", Message: "Parameter pagination tidak valid.",
					Fields: map[string]string{name: fmt.Sprintf("Gunakan bilangan bulat %d sampai %d.", field.min, field.max)}}
			}
			*field.value = n
		}
	}
	return page, nil
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
