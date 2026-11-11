package platform

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLogRequestsRecordsPatternStatusAndActor(t *testing.T) {
	var buf bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(NewLogger(&buf, slog.LevelInfo, nil))
	t.Cleanup(func() { slog.SetDefault(previous) })

	inner := http.NewServeMux()
	inner.HandleFunc("GET /api/v1/databases/{id}", func(w http.ResponseWriter, r *http.Request) {
		*r = *r.WithContext(WithActorID(r.Context(), "10000000-0000-4000-8000-000000000003"))
		if err := http.NewResponseController(w).Flush(); err != nil {
			t.Error("flush must reach the real writer", err)
		}
		w.WriteHeader(http.StatusTeapot)
		w.WriteHeader(http.StatusOK)
	})
	// The session middleware hands the inner mux a copied request, as in cmd/server.
	session := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { next.ServeHTTP(w, r.WithContext(r.Context())) })
	}
	outer := http.NewServeMux()
	outer.Handle("/api/", session(Route(inner)))
	outer.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })
	handler := RequestID(LogRequests(outer))

	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/api/v1/databases/30000000-0000-4000-8000-000000000001?token=secret", nil))
	line := logLine(t, &buf)
	if line["event"] != "http.request" || line["method"] != "GET" || line["route"] != "GET /api/v1/databases/{id}" ||
		line["status"] != float64(http.StatusTeapot) || line["actor_id"] != "10000000-0000-4000-8000-000000000003" ||
		line["request_id"] == nil || line["duration_ms"] == nil {
		t.Fatal("incorrect request line", line)
	}

	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/health", nil))
	if line := logLine(t, &buf); line["route"] != "GET /health" || line["status"] != float64(200) || line["actor_id"] != nil {
		t.Fatal("outer mux route and implicit 200", line)
	}

	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("POST", "/nowhere?password=x", strings.NewReader(`{"password":"hunter2"}`)))
	out := buf.String()
	if line := logLine(t, &buf); line["route"] != "unmatched" || line["status"] != float64(404) {
		t.Fatal("unmatched request", line)
	}
	for _, leaked := range []string{"30000000-0000-4000-8000-000000000001", "secret", "hunter2", "/nowhere"} {
		if strings.Contains(out, leaked) {
			t.Fatalf("%q leaked in %s", leaked, out)
		}
	}
}
