package migrations

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/contracts"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/platform"
)

type PrincipalFunc func(context.Context) (contracts.Principal, bool)

type Handler struct {
	service   *Service
	principal PrincipalFunc
}

func NewHandler(service *Service, principal ...PrincipalFunc) *Handler {
	h := &Handler{service: service, principal: func(context.Context) (contracts.Principal, bool) { return contracts.Principal{}, false }}
	if len(principal) > 0 && principal[0] != nil {
		h.principal = principal[0]
	}
	return h
}

func RegisterRoutes(mux *http.ServeMux, h *Handler) {
	mux.HandleFunc("GET /api/v1/databases/{id}/migration-drafts", h.listDrafts)
	mux.HandleFunc("POST /api/v1/databases/{id}/migration-drafts", h.createDraft)
	mux.HandleFunc("GET /api/v1/databases/{id}/migration-drafts/{draftId}", h.getDraft)
	mux.HandleFunc("PATCH /api/v1/databases/{id}/migration-drafts/{draftId}", h.updateDraft)
	mux.HandleFunc("POST /api/v1/databases/{id}/migration-drafts/{draftId}/execute", h.execute)
	mux.HandleFunc("GET /api/v1/databases/{id}/migration-runs", h.listRuns)
	mux.HandleFunc("GET /api/v1/databases/{id}/migration-runs/{runId}", h.getRun)
	mux.HandleFunc("POST /api/v1/databases/{id}/migration-runs/{runId}/reconcile", h.reconcile)
	mux.HandleFunc("GET /api/v1/databases/{id}/migration-head", h.head)
}

func (h *Handler) authorize(w http.ResponseWriter, r *http.Request, action contracts.Action) (contracts.Principal, bool) {
	p, ok := h.principal(r.Context())
	if !ok {
		_ = platform.WriteError(w, r, problem("UNAUTHENTICATED", "Silakan login untuk melanjutkan."))
		return p, false
	}
	if err := h.service.authorize(r.Context(), p, r.PathValue("id"), action); err != nil {
		_ = platform.WriteError(w, r, err)
		return p, false
	}
	return p, true
}

func respond(w http.ResponseWriter, r *http.Request, data any, err error, status int) {
	if err != nil {
		_ = platform.WriteError(w, r, domain(err))
		return
	}
	_ = platform.WriteJSON(w, r, status, data, nil)
}

type draftBody struct {
	Title             *string         `json:"title"`
	SQL               *string         `json:"sql"`
	Revision          *int64          `json:"revision,omitempty"`
	ExpectedVersion   *int64          `json:"expected_version"`
	SourceFingerprint json.RawMessage `json:"source_fingerprint"`
}

func decodeDraft(w http.ResponseWriter, r *http.Request, update bool) (UpdateDraftInput, error) {
	var body draftBody
	if err := platform.DecodeJSON(w, r, &body, 0); err != nil {
		return UpdateDraftInput{}, err
	}
	if body.Title == nil || body.SQL == nil || body.ExpectedVersion == nil || (update && body.Revision == nil) || (!update && body.Revision != nil) {
		return UpdateDraftInput{}, problem("INVALID_REQUEST", "title, sql, expected_version, dan revision saat update wajib diisi.")
	}
	in := UpdateDraftInput{DraftInput: DraftInput{Title: *body.Title, SQL: *body.SQL, ExpectedVersion: *body.ExpectedVersion}, FingerprintProvided: len(body.SourceFingerprint) > 0}
	if body.Revision != nil {
		in.Revision = *body.Revision
	}
	if in.FingerprintProvided {
		if err := json.Unmarshal(body.SourceFingerprint, &in.SourceFingerprint); err != nil {
			return in, problem("INVALID_REQUEST", "Fingerprint harus string atau null.")
		}
	}
	return in, nil
}

func (h *Handler) createDraft(w http.ResponseWriter, r *http.Request) {
	p, ok := h.authorize(w, r, contracts.EditDatabase)
	if !ok {
		return
	}
	in, err := decodeDraft(w, r, false)
	if err != nil {
		respond(w, r, nil, err, 0)
		return
	}
	d, err := h.service.CreateDraft(r.Context(), p, r.PathValue("id"), in.DraftInput)
	respond(w, r, d, err, http.StatusCreated)
}

func (h *Handler) updateDraft(w http.ResponseWriter, r *http.Request) {
	p, ok := h.authorize(w, r, contracts.EditDatabase)
	if !ok {
		return
	}
	in, err := decodeDraft(w, r, true)
	if err != nil {
		respond(w, r, nil, err, 0)
		return
	}
	d, err := h.service.UpdateDraft(r.Context(), p, r.PathValue("id"), r.PathValue("draftId"), in)
	respond(w, r, d, err, http.StatusOK)
}

func (h *Handler) getDraft(w http.ResponseWriter, r *http.Request) {
	p, ok := h.authorize(w, r, contracts.EditDatabase)
	if !ok {
		return
	}
	d, err := h.service.GetDraft(r.Context(), p, r.PathValue("id"), r.PathValue("draftId"))
	respond(w, r, d, err, http.StatusOK)
}

func pagination(r *http.Request) (int, int, error) {
	limit, offset := 50, 0
	for _, item := range []struct {
		name string
		dest *int
	}{{"limit", &limit}, {"offset", &offset}} {
		if r.URL.Query().Has(item.name) {
			n, err := strconv.Atoi(r.URL.Query().Get(item.name))
			if err != nil {
				return 0, 0, problem("INVALID_REQUEST", "Pagination tidak valid.")
			}
			*item.dest = n
		}
	}
	if !validPage(limit, offset) {
		return 0, 0, problem("INVALID_REQUEST", "Pagination tidak valid.")
	}
	return limit, offset, nil
}

func (h *Handler) listDrafts(w http.ResponseWriter, r *http.Request) {
	p, ok := h.authorize(w, r, contracts.EditDatabase)
	if !ok {
		return
	}
	limit, offset, err := pagination(r)
	if err != nil {
		respond(w, r, nil, err, 0)
		return
	}
	items, more, err := h.service.ListDrafts(r.Context(), p, r.PathValue("id"), limit, offset)
	if err != nil {
		respond(w, r, nil, err, 0)
		return
	}
	_ = platform.WriteList(w, r, items, platform.Page{Limit: limit, Offset: offset, HasMore: more})
}

func (h *Handler) listRuns(w http.ResponseWriter, r *http.Request) {
	p, ok := h.authorize(w, r, contracts.ViewDatabase)
	if !ok {
		return
	}
	limit, offset, err := pagination(r)
	if err != nil {
		respond(w, r, nil, err, 0)
		return
	}
	items, more, err := h.service.ListRuns(r.Context(), p, r.PathValue("id"), limit, offset)
	if err != nil {
		respond(w, r, nil, err, 0)
		return
	}
	_ = platform.WriteList(w, r, items, platform.Page{Limit: limit, Offset: offset, HasMore: more})
}

func (h *Handler) getRun(w http.ResponseWriter, r *http.Request) {
	p, ok := h.authorize(w, r, contracts.ViewDatabase)
	if !ok {
		return
	}
	run, err := h.service.GetRun(r.Context(), p, r.PathValue("id"), r.PathValue("runId"))
	respond(w, r, run, err, http.StatusOK)
}

func runResponse(w http.ResponseWriter, r *http.Request, run MigrationRun, err error) {
	status := http.StatusOK
	if unresolved(run) {
		status = http.StatusAccepted
	}
	respond(w, r, run, err, status)
}

func (h *Handler) execute(w http.ResponseWriter, r *http.Request) {
	p, ok := h.authorize(w, r, contracts.EditDatabase)
	if !ok {
		return
	}
	var body struct {
		Revision        *int64 `json:"revision"`
		ExpectedVersion *int64 `json:"expected_version"`
	}
	if err := platform.DecodeJSON(w, r, &body, 0); err != nil {
		respond(w, r, nil, err, 0)
		return
	}
	if body.Revision == nil || body.ExpectedVersion == nil || len(r.Header.Values("Idempotency-Key")) != 1 {
		respond(w, r, nil, problem("INVALID_REQUEST", "revision, expected_version dan satu Idempotency-Key wajib diisi."), 0)
		return
	}
	run, err := h.service.Execute(r.Context(), p, ExecuteInput{DatabaseID: r.PathValue("id"), DraftID: r.PathValue("draftId"), Revision: *body.Revision, ExpectedVersion: *body.ExpectedVersion, IdempotencyKey: r.Header.Get("Idempotency-Key")})
	runResponse(w, r, run, err)
}

func (h *Handler) reconcile(w http.ResponseWriter, r *http.Request) {
	p, ok := h.authorize(w, r, contracts.EditDatabase)
	if !ok {
		return
	}
	if err := platform.DecodeJSON(w, r, &struct{}{}, 0); err != nil {
		respond(w, r, nil, err, 0)
		return
	}
	run, err := h.service.Reconcile(r.Context(), p, r.PathValue("id"), r.PathValue("runId"))
	runResponse(w, r, run, err)
}

func (h *Handler) head(w http.ResponseWriter, r *http.Request) {
	p, ok := h.authorize(w, r, contracts.ViewDatabase)
	if !ok {
		return
	}
	head, err := h.service.Head(r.Context(), p, r.PathValue("id"))
	respond(w, r, head, err, http.StatusOK)
}
