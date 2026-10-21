package platform

import (
	"context"
	"net/http"
	"time"
)

type Pinger interface {
	PingContext(context.Context) error
}

func RegisterHealthRoutes(mux *http.ServeMux, metadata Pinger) {
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		ensureRequestID(w, r)
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		ensureRequestID(w, r)
		w.Header().Set("Cache-Control", "no-store")
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		status, message := http.StatusOK, "ready"
		if err := metadata.PingContext(ctx); err != nil {
			status, message = http.StatusServiceUnavailable, "not_ready"
		}
		writeJSON(w, status, map[string]string{"status": message})
	})
}
