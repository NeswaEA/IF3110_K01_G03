package connections

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strconv"

	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/contracts"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/database"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/platform"
)

// PrincipalFunc reads the principal the session middleware put on the request
// context (auth.PrincipalFromContext when wired). false means no valid session.
type PrincipalFunc func(context.Context) (contracts.Principal, bool)

// Handler serves the admin registry and the per-user database list. Session
// and CSRF middleware are mounted on the router; the handler still checks the
// action on every request.
type Handler struct {
	svc       *Service
	access    contracts.AccessChecker
	principal PrincipalFunc
}

func NewHandler(svc *Service, access contracts.AccessChecker, principal PrincipalFunc) *Handler {
	return &Handler{svc: svc, access: access, principal: principal}
}

func RegisterRoutes(mux *http.ServeMux, h *Handler) {
	mux.HandleFunc("GET /api/v1/admin/connections", h.list)
	mux.HandleFunc("POST /api/v1/admin/connections", h.create)
	mux.HandleFunc("GET /api/v1/admin/connections/{id}", h.get)
	mux.HandleFunc("PATCH /api/v1/admin/connections/{id}", h.update)
	mux.HandleFunc("POST /api/v1/admin/connections/{id}/test", h.test)
	mux.HandleFunc("DELETE /api/v1/admin/connections/{id}", h.deactivate)
	mux.HandleFunc("GET /api/v1/databases", h.databases)
	mux.HandleFunc("GET /api/v1/databases/{id}", h.database)
}

// Database is the public view of a registration for a signed-in user.
type Database struct {
	ID              string                `json:"id"`
	Name            string                `json:"name"`
	Driver          database.Driver       `json:"driver"`
	Revision        int64                 `json:"revision"`
	EffectiveAccess contracts.AccessLevel `json:"effective_access"`
	Capabilities    Capabilities          `json:"capabilities"`
}

var errUnauthenticated = &contracts.DomainError{Code: "UNAUTHENTICATED", Message: "Silakan login untuk melanjutkan."}

var uuidRe = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// validID rejects a malformed path id before it reaches a uuid column.
func validID(w http.ResponseWriter, r *http.Request, id string) bool {
	if uuidRe.MatchString(id) {
		return true
	}
	_ = platform.WriteError(w, r, &contracts.DomainError{Code: "INVALID_REQUEST", Message: "ID tidak valid.",
		Fields: map[string]string{"id": "harus UUID huruf kecil"}})
	return false
}

// authorize resolves the principal, validates databaseID when given and checks
// action; on failure it has already written the error response.
func (h *Handler) authorize(w http.ResponseWriter, r *http.Request, action contracts.Action, databaseID string) (contracts.Principal, bool) {
	p, ok := h.principal(r.Context())
	if !ok {
		_ = platform.WriteError(w, r, errUnauthenticated)
		return p, false
	}
	if databaseID != "" && !validID(w, r, databaseID) {
		return p, false
	}
	if action != "" {
		if err := h.access.Require(r.Context(), p, action, databaseID); err != nil {
			_ = platform.WriteError(w, r, err)
			return p, false
		}
	}
	return p, true
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.authorize(w, r, contracts.ManagePlatform, ""); !ok {
		return
	}
	limit, offset, err := parsePage(r)
	if err != nil {
		_ = platform.WriteError(w, r, err)
		return
	}
	items, more, err := h.svc.List(r.Context(), limit, offset)
	if err != nil {
		_ = platform.WriteError(w, r, domainError(err, msgConnectionNotFound))
		return
	}
	_ = platform.WriteList(w, r, items, platform.Page{Limit: limit, Offset: offset, HasMore: more})
}

func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.authorize(w, r, contracts.ManagePlatform, ""); !ok {
		return
	}
	var body struct {
		Name   string          `json:"name"`
		Driver database.Driver `json:"driver"`
		Config json.RawMessage `json:"config"`
	}
	if err := platform.DecodeJSON(w, r, &body, 0); err != nil {
		_ = platform.WriteError(w, r, err)
		return
	}
	c, err := h.svc.Register(r.Context(), RegisterInput{Name: body.Name, Driver: body.Driver, Config: body.Config})
	if err != nil {
		_ = platform.WriteError(w, r, domainError(err, msgConnectionNotFound))
		return
	}
	_ = platform.WriteJSON(w, r, http.StatusCreated, c, nil)
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.authorize(w, r, contracts.ManagePlatform, ""); !ok {
		return
	}
	if !validID(w, r, r.PathValue("id")) {
		return
	}
	c, err := h.svc.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		_ = platform.WriteError(w, r, domainError(err, msgConnectionNotFound))
		return
	}
	_ = platform.WriteJSON(w, r, http.StatusOK, c, nil)
}

func (h *Handler) update(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.authorize(w, r, contracts.ManagePlatform, ""); !ok {
		return
	}
	if !validID(w, r, r.PathValue("id")) {
		return
	}
	var body struct {
		Revision *int64          `json:"revision"`
		Name     *string         `json:"name"`
		Config   json.RawMessage `json:"config"`
	}
	if err := platform.DecodeJSON(w, r, &body, 0); err != nil {
		_ = platform.WriteError(w, r, err)
		return
	}
	if body.Revision == nil {
		_ = platform.WriteError(w, r, &contracts.DomainError{Code: "INVALID_REQUEST", Message: msgInvalid,
			Fields: map[string]string{"revision": "wajib diisi"}})
		return
	}
	if string(body.Config) == "null" {
		body.Config = nil // explicit null changes nothing, same as omitting it
	}
	c, err := h.svc.Update(r.Context(), r.PathValue("id"), UpdateInput{Revision: *body.Revision, Name: body.Name, Config: body.Config})
	if err != nil {
		_ = platform.WriteError(w, r, domainError(err, msgConnectionNotFound))
		return
	}
	_ = platform.WriteJSON(w, r, http.StatusOK, c, nil)
}

func (h *Handler) test(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.authorize(w, r, contracts.ManagePlatform, ""); !ok {
		return
	}
	if !validID(w, r, r.PathValue("id")) {
		return
	}
	res, err := h.svc.Test(r.Context(), r.PathValue("id"))
	if err != nil {
		_ = platform.WriteError(w, r, domainError(err, msgConnectionNotFound))
		return
	}
	_ = platform.WriteJSON(w, r, http.StatusOK, res, nil)
}

func (h *Handler) deactivate(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.authorize(w, r, contracts.ManagePlatform, ""); !ok {
		return
	}
	if !validID(w, r, r.PathValue("id")) {
		return
	}
	if err := h.svc.Deactivate(r.Context(), r.PathValue("id")); err != nil {
		_ = platform.WriteError(w, r, domainError(err, msgConnectionNotFound))
		return
	}
	_ = platform.WriteJSON(w, r, http.StatusNoContent, nil, nil)
}

// databases lists the active targets the user can view or edit, by name then id.
func (h *Handler) databases(w http.ResponseWriter, r *http.Request) {
	p, ok := h.authorize(w, r, "", "")
	if !ok {
		return
	}
	limit, offset, err := parsePage(r)
	if err != nil {
		_ = platform.WriteError(w, r, err)
		return
	}
	visible, err := h.svc.Visible(r.Context(), h.access, p.UserID)
	if err != nil {
		_ = platform.WriteError(w, r, err)
		return
	}
	page := platform.Page{Limit: limit, Offset: offset, HasMore: len(visible) > offset+limit}
	visible = visible[min(offset, len(visible)):min(offset+limit, len(visible))]
	_ = platform.WriteList(w, r, visible, page)
}

func (h *Handler) database(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	p, ok := h.authorize(w, r, contracts.ViewDatabase, id)
	if !ok {
		return
	}
	db, err := h.svc.Database(r.Context(), h.access, p.UserID, id)
	if err != nil {
		_ = platform.WriteError(w, r, domainError(err, msgDatabaseNotFound))
		return
	}
	_ = platform.WriteJSON(w, r, http.StatusOK, db, nil)
}

// Pagination bounds shared by every list endpoint.
const (
	defaultLimit = 50
	maxLimit     = 200
	maxOffset    = 100000
)

func parsePage(r *http.Request) (limit, offset int, err error) {
	q := r.URL.Query()
	fields := map[string]string{}
	limit, offset = defaultLimit, 0
	if v := q.Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err != nil || n < 1 || n > maxLimit {
			fields["limit"] = "harus bilangan bulat 1 sampai 200"
		} else {
			limit = n
		}
	}
	if v := q.Get("offset"); v != "" {
		if n, err := strconv.Atoi(v); err != nil || n < 0 || n > maxOffset {
			fields["offset"] = "harus bilangan bulat 0 sampai 100000"
		} else {
			offset = n
		}
	}
	if len(fields) > 0 {
		return 0, 0, &contracts.DomainError{Code: "INVALID_REQUEST", Message: "Parameter pagination tidak valid.", Fields: fields}
	}
	return limit, offset, nil
}

const (
	msgInvalid            = "Konfigurasi koneksi tidak valid."
	msgConnectionNotFound = "Koneksi tidak ditemukan."
	msgDatabaseNotFound   = "Database tidak ditemukan."
)

var messages = map[string]string{
	"TARGET_NOT_READY":    "Layanan migrasi belum siap untuk memeriksa target.",
	"INVALID_REQUEST":     msgInvalid,
	"REVISION_CONFLICT":   "Koneksi berubah sejak dibaca; muat ulang.",
	"DUPLICATE_TARGET":    "Target yang sama sudah terdaftar.",
	"TARGET_UNAVAILABLE":  "Target tidak dapat dihubungi.",
	"TARGET_TIMEOUT":      "Target tidak merespons dalam batas waktu.",
	"LEDGER_INCOMPATIBLE": "Ledger migrasi di target tidak cocok dengan format aplikasi.",
}

// domainError maps registry errors to their API code with a fixed message.
// Anything else is returned as is and becomes INTERNAL_ERROR.
func domainError(err error, notFound string) error {
	code := ErrorCode(err)
	if code == "" {
		return err
	}
	d := &contracts.DomainError{Code: code, Message: messages[code], Cause: err}
	switch {
	case code == "NOT_FOUND":
		d.Message = notFound
	case errors.Is(err, ErrIdentityLocked):
		d.Message = "Lokasi target tidak dapat diubah karena sudah memiliki riwayat migrasi."
	}
	var ve *ValidationError
	if errors.As(err, &ve) {
		d.Fields = ve.Fields
	}
	return d
}
