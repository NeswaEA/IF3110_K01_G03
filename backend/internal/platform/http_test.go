package platform_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/contracts"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/platform"
)

func TestDecodeJSON(t *testing.T) {
	for _, tt := range []struct {
		name, body, contentType, code string
		limit                         int64
	}{
		{"valid", `{"name":"Example"}`, "application/json", "", 0},
		{"charset", `{}`, "application/json; charset=utf-8", "", 0},
		{"exact limit", `{}`, "application/json", "", 2},
		{"too large", `{} `, "application/json", "PAYLOAD_TOO_LARGE", 2},
		{"default limit", `{"name":"` + strings.Repeat("a", 1<<20) + `"}`, "application/json", "PAYLOAD_TOO_LARGE", 0},
		{"unknown field", `{"password":"secret"}`, "application/json", "INVALID_REQUEST", 0},
		{"nested unknown", `{"child":{"password":"secret"}}`, "application/json", "INVALID_REQUEST", 0},
		{"wrong type", `{"name":123}`, "application/json", "INVALID_REQUEST", 0},
		{"malformed", `{"name":`, "application/json", "INVALID_REQUEST", 0},
		{"second object", `{} {}`, "application/json", "INVALID_REQUEST", 0},
		{"trailing garbage", `{}secret`, "application/json", "INVALID_REQUEST", 0},
		{"null", `null`, "application/json", "INVALID_REQUEST", 0},
		{"array", `[]`, "application/json", "INVALID_REQUEST", 0},
		{"empty", ``, "application/json", "INVALID_REQUEST", 0},
		{"missing type", `{}`, "", "INVALID_REQUEST", 0},
		{"wrong type header", `{}`, "text/plain", "INVALID_REQUEST", 0},
		{"wrong encoding", `{}`, "application/json; charset=iso-8859-1", "INVALID_REQUEST", 0},
		{"invalid UTF8", "{\"name\":\"\xff\"}", "application/json", "INVALID_REQUEST", 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(tt.body))
			r.Header.Set("Content-Type", tt.contentType)
			var dst struct {
				Name  string `json:"name"`
				Child *struct {
					Name string `json:"name"`
				} `json:"child"`
			}
			err := platform.DecodeJSON(httptest.NewRecorder(), r, &dst, tt.limit)
			if tt.code == "" {
				if err != nil {
					t.Fatal(err)
				}
				if tt.name == "valid" && dst.Name != "Example" {
					t.Fatal("value not decoded")
				}
				return
			}
			var domain *contracts.DomainError
			if !errors.As(err, &domain) || domain.Code != tt.code {
				t.Fatalf("expected %s, got %v", tt.code, err)
			}
			if strings.Contains(err.Error(), "secret") {
				t.Fatal("input leaked")
			}
		})
	}
}

func TestRequireEmptyBody(t *testing.T) {
	for _, tc := range []struct {
		name, body, code string
		limit            int64
		chunked          bool
	}{
		{"absent", "", "", 0, false},
		{"empty_chunked", "", "", 0, true},
		{"object", "{}", "INVALID_REQUEST", 0, false},
		{"whitespace", " ", "INVALID_REQUEST", 0, false},
		{"malformed", "{", "INVALID_REQUEST", 0, false},
		{"custom_limit", "abc", "PAYLOAD_TOO_LARGE", 2, false},
		{"default_limit", strings.Repeat("x", 1<<20+1), "PAYLOAD_TOO_LARGE", 0, false},
		{"chunked_limit", "abc", "PAYLOAD_TOO_LARGE", 2, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodDelete, "/", strings.NewReader(tc.body))
			if tc.chunked {
				r.ContentLength = -1
				r.TransferEncoding = []string{"chunked"}
			}
			err := platform.RequireEmptyBody(httptest.NewRecorder(), r, tc.limit)
			if tc.code == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			var domain *contracts.DomainError
			if !errors.As(err, &domain) || domain.Code != tc.code {
				t.Fatalf("expected %s, got %v", tc.code, err)
			}
		})
	}
}

func TestSuccessResponses(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	if err := platform.WriteJSON(w, r, 201, map[string]string{"id": "one"}, nil); err != nil {
		t.Fatal(err)
	}
	if w.Code != 201 || strings.TrimSpace(w.Body.String()) != `{"data":{"id":"one"}}` || w.Header().Get("X-Request-ID") == "" {
		t.Fatal(w)
	}
	w = httptest.NewRecorder()
	if err := platform.WriteList[string](w, r, nil, platform.Page{Limit: 50}); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(w.Body.String()) != `{"data":[],"page":{"limit":50,"offset":0,"has_more":false}}` {
		t.Fatal(w.Body.String())
	}
	w = httptest.NewRecorder()
	if err := platform.WriteJSON(w, r, 204, "ignored", nil); err != nil {
		t.Fatal(err)
	}
	if w.Code != 204 || w.Body.Len() != 0 || w.Header().Get("Content-Type") != "" || w.Header().Get("X-Request-ID") == "" {
		t.Fatal(w)
	}
}

func TestMarshalFailureDoesNotWritePartialResponse(t *testing.T) {
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/", nil)
	if err := platform.WriteJSON(w, r, 201, make(chan int), nil); err == nil {
		t.Fatal("expected serialization error")
	}
	if w.Body.Len() != 0 {
		t.Fatal("partial response written")
	}
	if err := platform.WriteError(w, r, errors.New("secret")); err != nil {
		t.Fatal(err)
	}
	if w.Code != 500 || strings.Contains(w.Body.String(), "secret") {
		t.Fatal(w)
	}
}

func TestErrorStatusesAndRedaction(t *testing.T) {
	groups := map[int][]string{
		400: {"INVALID_REQUEST"}, 401: {"UNAUTHENTICATED"}, 403: {"FORBIDDEN", "CSRF_REJECTED"}, 404: {"NOT_FOUND"},
		409: {"REVISION_CONFLICT", "VERSION_CONFLICT", "RUN_IN_PROGRESS", "IDEMPOTENCY_CONFLICT", "TARGET_OUTCOME_UNKNOWN", "SCHEMA_CONFLICT", "DRAFT_APPLIED", "DUPLICATE_TARGET", "LEDGER_INCOMPATIBLE", "ALREADY_EXISTS", "LAST_ADMIN"},
		413: {"PAYLOAD_TOO_LARGE"}, 422: {"UNSUPPORTED_SQL", "SQL_EXECUTION_FAILED", "PLAN_UNSUPPORTED", "DIFF_UNSUPPORTED"}, 429: {"RATE_LIMITED"},
		500: {"INTERNAL_ERROR", "UNKNOWN_CODE", "NETWORK_ERROR", "EXECUTION_INTERRUPTED"}, 502: {"TARGET_UNAVAILABLE"}, 503: {"DIFF_UNAVAILABLE", "TARGET_NOT_READY"}, 504: {"TARGET_TIMEOUT"},
	}
	for status, codes := range groups {
		for _, code := range codes {
			t.Run(code, func(t *testing.T) {
				w := httptest.NewRecorder()
				runID := "50000000-0000-4000-8000-000000000001"
				message := "Pesan aman"
				if status == 500 {
					message = "secret"
				}
				err := fmt.Errorf("wrapped: %w", &contracts.DomainError{Code: code, Message: message, RunID: &runID, Cause: errors.New("secret")})
				if err := platform.WriteError(w, httptest.NewRequest("GET", "/", nil), err); err != nil {
					t.Fatal(err)
				}
				var body struct {
					Error struct {
						Code      string            `json:"code"`
						Fields    map[string]string `json:"fields"`
						RequestID string            `json:"request_id"`
						RunID     string            `json:"run_id"`
					} `json:"error"`
				}
				if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
					t.Fatal(err)
				}
				if status == 500 {
					code = "INTERNAL_ERROR"
				}
				if w.Code != status || body.Error.Code != code || body.Error.Fields == nil || body.Error.RunID != runID || body.Error.RequestID != w.Header().Get("X-Request-ID") || strings.Contains(w.Body.String(), "secret") {
					t.Fatal(w)
				}
			})
		}
	}
}

func TestErrorMatchesSharedFixture(t *testing.T) {
	fixture, err := os.ReadFile("../../../tests/fixtures/api/connections_error_validation.json")
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	w.Header().Set("X-Request-ID", "req-demo-002")
	err = platform.WriteError(w, httptest.NewRequest("GET", "/", nil), &contracts.DomainError{Code: "INVALID_REQUEST", Message: "Konfigurasi koneksi tidak valid.", Fields: map[string]string{"config.port": "harus antara 1 dan 65535"}})
	if err != nil {
		t.Fatal(err)
	}
	var want, got any
	if err := json.Unmarshal(fixture, &want); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(want, got) {
		t.Fatal(w.Body.String())
	}
}

func TestRequestIDMiddleware(t *testing.T) {
	seen := map[string]bool{}
	handler := platform.RequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := platform.RequestIDFromContext(r.Context())
		if id == "" || id == "untrusted" || id != w.Header().Get("X-Request-ID") || seen[id] {
			t.Fatal("invalid request ID")
		}
		seen[id] = true
		if err := platform.WriteError(w, r, errors.New("secret")); err != nil {
			t.Fatal(err)
		}
		if id != w.Header().Get("X-Request-ID") {
			t.Fatal("request ID changed")
		}
	}))
	for i := 0; i < 2; i++ {
		r := httptest.NewRequest("GET", "/", nil)
		r.Header.Set("X-Request-ID", "untrusted")
		handler.ServeHTTP(httptest.NewRecorder(), r)
	}
}
