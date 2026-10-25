package viewer

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/contracts"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/database"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/platform"
)

// PrincipalFunc reads the principal the session middleware put on the request
// context (auth.PrincipalFromContext when wired). false means no valid session.
type PrincipalFunc func(context.Context) (contracts.Principal, bool)

type Handler struct {
	svc       *Service
	access    contracts.AccessChecker
	principal PrincipalFunc
}

func NewHandler(svc *Service, access contracts.AccessChecker, principal PrincipalFunc) *Handler {
	return &Handler{svc: svc, access: access, principal: principal}
}

func RegisterRoutes(mux *http.ServeMux, h *Handler) {
	mux.HandleFunc("GET /api/v1/databases/{id}/schema", h.schema)
	mux.HandleFunc("GET /api/v1/databases/{id}/data", h.data)
}

// authorize requires a session, a well-formed id and view access; on failure
// it has already written the error response.
func (h *Handler) authorize(w http.ResponseWriter, r *http.Request) (string, bool) {
	p, ok := h.principal(r.Context())
	if !ok {
		_ = platform.WriteError(w, r, &contracts.DomainError{Code: "UNAUTHENTICATED", Message: "Silakan login untuk melanjutkan."})
		return "", false
	}
	id := r.PathValue("id")
	if !contracts.ValidID(id) {
		_ = platform.WriteError(w, r, invalid(map[string]string{"id": "harus UUID huruf kecil"}))
		return "", false
	}
	if err := h.access.Require(r.Context(), p, contracts.ViewDatabase, id); err != nil {
		_ = platform.WriteError(w, r, err)
		return "", false
	}
	return id, true
}

func (h *Handler) schema(w http.ResponseWriter, r *http.Request) {
	id, ok := h.authorize(w, r)
	if !ok {
		return
	}
	snap, err := h.svc.Schema(r.Context(), id)
	if err != nil {
		_ = platform.WriteError(w, r, domainError(err, "Database tidak ditemukan."))
		return
	}
	_ = platform.WriteJSON(w, r, http.StatusOK, snap, nil)
}

func (h *Handler) data(w http.ResponseWriter, r *http.Request) {
	id, ok := h.authorize(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	fields := map[string]string{}
	for _, k := range []string{"schema", "table"} {
		if q.Get(k) == "" {
			fields[k] = "wajib diisi"
		}
	}
	limit, offset := DefaultLimit, 0
	if v := q.Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err != nil || n < 1 || n > MaxLimit {
			fields["limit"] = "harus bilangan bulat 1 sampai 200"
		} else {
			limit = n
		}
	}
	if v := q.Get("offset"); v != "" {
		if n, err := strconv.Atoi(v); err != nil || n < 0 || n > MaxOffset {
			fields["offset"] = "harus bilangan bulat 0 sampai 100000"
		} else {
			offset = n
		}
	}
	if len(fields) > 0 {
		_ = platform.WriteError(w, r, invalid(fields))
		return
	}
	data, page, err := h.svc.Data(r.Context(), id, q.Get("schema"), q.Get("table"), limit, offset)
	if err != nil {
		_ = platform.WriteError(w, r, domainError(err, "Database atau tabel tidak ditemukan."))
		return
	}
	_ = platform.WriteJSON(w, r, http.StatusOK, data, &page)
}

func invalid(fields map[string]string) error {
	return &contracts.DomainError{Code: "INVALID_REQUEST", Message: "Parameter viewer tidak valid.", Fields: fields}
}

// domainError maps viewer and target errors to API codes with fixed messages
// that never carry engine, host or DSN detail. Anything else becomes INTERNAL_ERROR.
func domainError(err error, notFound string) error {
	d := &contracts.DomainError{Cause: err}
	switch {
	case errors.Is(err, ErrInvalid):
		d.Code, d.Message = "INVALID_REQUEST", "Parameter viewer tidak valid."
	case errors.Is(err, database.ErrNotFound):
		d.Code, d.Message = "NOT_FOUND", notFound
	case errors.Is(err, database.ErrTargetPermission):
		d.Code, d.Message = "FORBIDDEN", "Role target tidak berhak membaca tabel ini."
	case errors.Is(err, database.ErrTargetTimeout):
		d.Code, d.Message = "TARGET_TIMEOUT", "Target tidak merespons dalam batas waktu."
	case errors.Is(err, database.ErrTargetUnavailable):
		d.Code, d.Message = "TARGET_UNAVAILABLE", "Target tidak dapat dihubungi."
	default:
		return err
	}
	return d
}
