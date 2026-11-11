package telemetry

import (
	"context"
	"net/http"
	"strings"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

type routeKey struct{}

// HTTPHandler traces and measures every request. The span name and the
// http_route label use the matched ServeMux pattern ("GET /api/v1/databases/{id}"),
// never the raw path. Muxes nested behind middleware are wrapped with Route
// so their pattern is seen here.
func HTTPHandler(next http.Handler) http.Handler {
	capture := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		route := new(string)
		inner := r.WithContext(context.WithValue(r.Context(), routeKey{}, route))
		next.ServeHTTP(w, inner)
		if *route == "" {
			*route = inner.Pattern // next was the outermost mux itself
		}
		// otelhttp reads the pattern of the request it passed to us.
		pattern := *route
		r.Pattern = pattern[strings.IndexByte(pattern, ' ')+1:] // the method is a separate label
	})
	return otelhttp.NewHandler(capture, "http.server",
		otelhttp.WithSpanNameFormatter(func(_ string, r *http.Request) string {
			if r.Pattern == "" {
				return r.Method
			}
			return r.Method + " " + r.Pattern
		}))
}

// Route reports the pattern its mux matched to the enclosing HTTPHandler;
// the innermost mux wins.
func Route(mux http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mux.ServeHTTP(w, r)
		if route, ok := r.Context().Value(routeKey{}).(*string); ok && *route == "" {
			*route = r.Pattern
		}
	})
}
