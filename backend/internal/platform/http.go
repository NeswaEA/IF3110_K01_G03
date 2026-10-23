package platform

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"unicode/utf8"

	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/contracts"
)

const DefaultMaxBodyBytes int64 = 1 << 20

type Page struct {
	Limit   int  `json:"limit"`
	Offset  int  `json:"offset"`
	HasMore bool `json:"has_more"`
}

func DecodeJSON(w http.ResponseWriter, r *http.Request, dst any, maxBytes int64) error {
	mediaType, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" || (params["charset"] != "" && params["charset"] != "utf-8" && params["charset"] != "UTF-8") {
		return &contracts.DomainError{Code: "INVALID_REQUEST", Message: "Content-Type harus application/json dengan UTF-8."}
	}
	body, err := readRequestBody(w, r, maxBytes)
	if err != nil {
		return err
	}
	body = bytes.TrimSpace(body)
	if len(body) == 0 || body[0] != '{' || !utf8.Valid(body) {
		return invalidJSON()
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	decoder.UseNumber()
	if err := decoder.Decode(dst); err != nil {
		return invalidJSON()
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return invalidJSON()
	}
	return nil
}

func RequireEmptyBody(w http.ResponseWriter, r *http.Request, maxBytes int64) error {
	body, err := readRequestBody(w, r, maxBytes)
	if err != nil {
		return err
	}
	if len(body) != 0 {
		return &contracts.DomainError{Code: "INVALID_REQUEST", Message: "Request ini tidak menerima body."}
	}
	return nil
}

func readRequestBody(w http.ResponseWriter, r *http.Request, maxBytes int64) ([]byte, error) {
	if maxBytes <= 0 {
		maxBytes = DefaultMaxBodyBytes
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return nil, &contracts.DomainError{Code: "PAYLOAD_TOO_LARGE", Message: "Body melebihi batas ukuran."}
		}
		return nil, &contracts.DomainError{Code: "INVALID_REQUEST", Message: "Body tidak dapat dibaca."}
	}
	return body, nil
}

func invalidJSON() error {
	return &contracts.DomainError{Code: "INVALID_REQUEST", Message: "Body harus satu object JSON dengan field yang valid."}
}

func WriteList[T any](w http.ResponseWriter, r *http.Request, items []T, page Page) error {
	if items == nil {
		items = []T{}
	}
	return WriteJSON(w, r, http.StatusOK, items, &page)
}

func WriteJSON(w http.ResponseWriter, r *http.Request, status int, data any, page *Page) error {
	ensureRequestID(w, r)
	if status == http.StatusNoContent {
		w.Header().Del("Content-Type")
		w.WriteHeader(status)
		return nil
	}
	return writeJSON(w, status, struct {
		Data any   `json:"data"`
		Page *Page `json:"page,omitempty"`
	}{Data: data, Page: page})
}

func writeJSON(w http.ResponseWriter, status int, value any) error {
	body, err := json.Marshal(value)
	if err != nil {
		return err
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, err = w.Write(append(body, '\n'))
	return err
}

func WriteError(w http.ResponseWriter, r *http.Request, err error) error {
	response := struct {
		Code      string            `json:"code"`
		Message   string            `json:"message"`
		Fields    map[string]string `json:"fields"`
		RequestID string            `json:"request_id"`
		RunID     *string           `json:"run_id,omitempty"`
	}{Code: "INTERNAL_ERROR", Message: "Terjadi kesalahan internal.", Fields: map[string]string{}, RequestID: ensureRequestID(w, r)}
	status := http.StatusInternalServerError
	var domain *contracts.DomainError
	if errors.As(err, &domain) && domain != nil {
		response.RunID = domain.RunID
		if mapped, ok := errorStatuses[domain.Code]; ok && mapped != http.StatusInternalServerError {
			status = mapped
			response.Code = domain.Code
			response.Message = domain.Message
			if domain.Fields != nil {
				response.Fields = domain.Fields
			}
		}
	}
	return writeJSON(w, status, struct {
		Error any `json:"error"`
	}{Error: response})
}

var errorStatuses = map[string]int{
	"INVALID_REQUEST": 400,
	"UNAUTHENTICATED": 401,
	"FORBIDDEN":       403, "CSRF_REJECTED": 403,
	"NOT_FOUND":         404,
	"REVISION_CONFLICT": 409, "VERSION_CONFLICT": 409, "RUN_IN_PROGRESS": 409,
	"IDEMPOTENCY_CONFLICT": 409, "TARGET_OUTCOME_UNKNOWN": 409,
	"SCHEMA_CONFLICT": 409, "DRAFT_APPLIED": 409, "DUPLICATE_TARGET": 409,
	"LEDGER_INCOMPATIBLE": 409, "ALREADY_EXISTS": 409,
	"PAYLOAD_TOO_LARGE": 413,
	"UNSUPPORTED_SQL":   422, "SQL_EXECUTION_FAILED": 422, "PLAN_UNSUPPORTED": 422, "DIFF_UNSUPPORTED": 422,
	"RATE_LIMITED":       429,
	"INTERNAL_ERROR":     500,
	"TARGET_UNAVAILABLE": 502,
	"DIFF_UNAVAILABLE":   503, "TARGET_NOT_READY": 503,
	"TARGET_TIMEOUT": 504,
}
