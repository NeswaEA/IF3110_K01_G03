package explain

import (
	"context"
	"errors"
	"net/http"
	"regexp"

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

// RegisterRoutes mounts POST /explain; the router adds session and CSRF middleware.
func RegisterRoutes(mux *http.ServeMux, h *Handler) {
	mux.HandleFunc("POST /api/v1/databases/{id}/explain", h.explain)
}

var uuidRe = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// explain checks session and edit access before reading the body, so a
// user without edit access learns nothing from body validation.
func (h *Handler) explain(w http.ResponseWriter, r *http.Request) {
	p, ok := h.principal(r.Context())
	if !ok {
		_ = platform.WriteError(w, r, &contracts.DomainError{Code: "UNAUTHENTICATED", Message: "Silakan login untuk melanjutkan."})
		return
	}
	id := r.PathValue("id")
	if !uuidRe.MatchString(id) {
		_ = platform.WriteError(w, r, &contracts.DomainError{Code: "INVALID_REQUEST", Message: "ID tidak valid.",
			Fields: map[string]string{"id": "harus UUID huruf kecil"}})
		return
	}
	if err := h.access.Require(r.Context(), p, contracts.EditDatabase, id); err != nil {
		_ = platform.WriteError(w, r, err)
		return
	}

	var body struct {
		SQL  *string `json:"sql"`
		Mode *string `json:"mode"`
	}
	if err := platform.DecodeJSON(w, r, &body, 0); err != nil {
		_ = platform.WriteError(w, r, err)
		return
	}
	fields := map[string]string{}
	if body.SQL == nil {
		fields["sql"] = "wajib diisi"
	}
	if body.Mode == nil {
		fields["mode"] = "wajib diisi"
	}
	if len(fields) > 0 {
		_ = platform.WriteError(w, r, &contracts.DomainError{Code: "INVALID_REQUEST", Message: "Permintaan explain tidak valid.", Fields: fields})
		return
	}

	res, err := h.svc.Explain(r.Context(), id, *body.SQL, *body.Mode)
	if err != nil {
		_ = platform.WriteError(w, r, domainError(err, *body.Mode))
		return
	}
	_ = platform.WriteJSON(w, r, http.StatusOK, res, nil)
}

// domainError maps explain and target errors to API codes. Only an engine
// rejection carries the engine's own message, for the user who wrote the SQL;
// it is never logged. Anything else becomes INTERNAL_ERROR.
func domainError(err error, mode string) error {
	var ee *database.EngineError
	d := &contracts.DomainError{Code: ErrorCode(err), Cause: err}
	switch {
	case errors.Is(err, ErrInvalidRequest):
		d.Message, d.Fields = "Permintaan explain tidak valid.", map[string]string{"sql": "tidak boleh kosong atau memuat karakter NUL"}
	case errors.Is(err, ErrPayloadTooLarge):
		d.Message, d.Fields = "SQL melebihi batas ukuran.", map[string]string{"sql": "maksimal 256 KiB"}
	case errors.Is(err, ErrMultipleStatements):
		d.Message, d.Fields = "Pilih satu statement untuk dianalisis.", map[string]string{"sql": "lebih dari satu statement"}
	case errors.Is(err, ErrUnsupportedSQL) && mode != "estimate":
		d.Message, d.Fields = "Hanya mode estimate yang didukung.", map[string]string{"mode": "harus estimate"}
	case errors.Is(err, ErrUnsupportedSQL):
		d.Message = "Statement ini tidak dapat dianalisis."
	case errors.Is(err, ErrPlanUnsupported):
		d.Message = "Statement ini tidak mendukung rencana eksekusi."
	case errors.As(err, &ee):
		d.Message = ee.Message
	case errors.Is(err, database.ErrNotFound):
		d.Code, d.Message = "NOT_FOUND", "Database tidak ditemukan."
	case errors.Is(err, database.ErrTargetTimeout):
		d.Code, d.Message = "TARGET_TIMEOUT", "Target tidak merespons dalam batas waktu."
	case errors.Is(err, database.ErrTargetUnavailable):
		d.Code, d.Message = "TARGET_UNAVAILABLE", "Target tidak dapat dihubungi."
	default:
		return err
	}
	return d
}
