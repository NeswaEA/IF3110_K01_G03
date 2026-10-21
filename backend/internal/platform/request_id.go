package platform

import (
	"context"
	"crypto/rand"
	"net/http"
)

type requestIDKey struct{}

func RequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := rand.Text()
		w.Header().Set("X-Request-ID", id)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), requestIDKey{}, id)))
	})
}

func RequestIDFromContext(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey{}).(string)
	return id
}

func ensureRequestID(w http.ResponseWriter, r *http.Request) string {
	id := RequestIDFromContext(r.Context())
	if id == "" {
		id = w.Header().Get("X-Request-ID")
	}
	if id == "" {
		id = rand.Text()
	}
	w.Header().Set("X-Request-ID", id)
	return id
}
