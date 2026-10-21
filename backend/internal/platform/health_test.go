package platform_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/platform"
)

type pingFunc func(context.Context) error

func (f pingFunc) PingContext(ctx context.Context) error { return f(ctx) }

func TestHealthRoutes(t *testing.T) {
	for _, tt := range []struct {
		name, path, method, body string
		pingError                error
		status, calls            int
	}{
		{"alive without metadata", "/healthz", "GET", `{"status":"ok"}`, errors.New("password=secret"), 200, 0},
		{"metadata ready", "/readyz", "GET", `{"status":"ready"}`, nil, 200, 1},
		{"metadata unavailable", "/readyz", "GET", `{"status":"not_ready"}`, errors.New("password=secret"), 503, 1},
		{"wrong method", "/readyz", "POST", "", nil, 405, 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			calls := 0
			mux := http.NewServeMux()
			platform.RegisterHealthRoutes(mux, pingFunc(func(ctx context.Context) error {
				calls++
				deadline, ok := ctx.Deadline()
				if !ok || time.Until(deadline) > 5*time.Second {
					t.Fatal("missing readiness deadline")
				}
				return tt.pingError
			}))
			w := httptest.NewRecorder()
			platform.RequestID(mux).ServeHTTP(w, httptest.NewRequest(tt.method, tt.path, nil))
			if w.Code != tt.status || calls != tt.calls {
				t.Fatalf("status %d, ping calls %d", w.Code, calls)
			}
			if tt.body != "" && strings.TrimSpace(w.Body.String()) != tt.body {
				t.Fatal(w.Body.String())
			}
			if w.Header().Get("X-Request-ID") == "" || strings.Contains(w.Body.String(), "secret") {
				t.Fatal("request ID missing or secret exposed")
			}
			if tt.body != "" && w.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("health response may be cached")
			}
		})
	}
}

func TestReadinessPropagatesCancellation(t *testing.T) {
	mux := http.NewServeMux()
	platform.RegisterHealthRoutes(mux, pingFunc(func(ctx context.Context) error { return ctx.Err() }))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/readyz", nil).WithContext(ctx))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatal("canceled probe reported ready")
	}
}
