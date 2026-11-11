package platform

import (
	"context"
	"log/slog"
	"net/http"
	"time"
)

type requestLogKey struct{}

// requestLog collects what inner handlers learn (route pattern, signed-in user) for the outer log line.
type requestLog struct{ route, actor string }

// LogRequests writes one http.request line per request: method, route pattern, status and duration.
// The raw path, query string and body are never logged.
func LogRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		entry := &requestLog{}
		recorder := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		inner := r.WithContext(context.WithValue(r.Context(), requestLogKey{}, entry))
		next.ServeHTTP(recorder, inner)
		if entry.route == "" {
			entry.route = inner.Pattern // next was the outermost mux itself
		}
		if entry.route == "" {
			entry.route = "unmatched"
		}
		ctx := r.Context()
		if entry.actor != "" {
			ctx = WithActorID(ctx, entry.actor)
		}
		slog.InfoContext(ctx, "HTTP request", "event", "http.request", "method", r.Method, "route", entry.route,
			"status", recorder.status, "duration_ms", float64(time.Since(start).Microseconds())/1000)
	})
}

// Route reports the pattern its mux matched to LogRequests; muxes behind middleware need it, and the innermost wins.
func Route(mux http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mux.ServeHTTP(w, r)
		if entry, ok := r.Context().Value(requestLogKey{}).(*requestLog); ok && entry.route == "" {
			entry.route = r.Pattern
		}
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (s *statusRecorder) WriteHeader(status int) {
	if !s.wroteHeader {
		s.status, s.wroteHeader = status, true
	}
	s.ResponseWriter.WriteHeader(status)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	s.wroteHeader = true
	return s.ResponseWriter.Write(b)
}

// Unwrap lets http.ResponseController reach Flush on the real writer (SSE).
func (s *statusRecorder) Unwrap() http.ResponseWriter { return s.ResponseWriter }
