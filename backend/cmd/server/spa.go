package main

import (
	"errors"
	"net/http"
	"os"
	"strings"

	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/contracts"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/platform"
)

const contentSecurityPolicy = "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; " +
	"font-src 'self'; connect-src 'self'; frame-ancestors 'none'; base-uri 'self'; form-action 'self'"

// newSPA serves the Vite build in dir: hashed assets as immutable, other build files directly,
// and index.html for every other page the browser asks for, so the client router can take over.
func newSPA(dir string) (http.Handler, func(), error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, nil, errors.New("frontend build directory is unavailable; build web and set WEB_DIST_DIR")
	}
	if info, err := root.Stat("index.html"); err != nil || !info.Mode().IsRegular() {
		root.Close()
		return nil, nil, errors.New("frontend build has no index.html; build web and set WEB_DIST_DIR")
	}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		if path == "/api" || strings.HasPrefix(path, "/api/") {
			_ = platform.WriteError(w, r, &contracts.DomainError{Code: "NOT_FOUND", Message: "Endpoint tidak ditemukan."})
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		name := strings.TrimPrefix(path, "/")
		if serveFile(w, r, root, name) {
			return
		}
		// Missing assets and health paths stay 404 so a stale script never receives HTML.
		if strings.HasPrefix(path, "/assets/") || path == "/assets" || path == "/healthz" || path == "/readyz" ||
			!strings.Contains(r.Header.Get("Accept"), "text/html") {
			http.NotFound(w, r)
			return
		}
		serveFile(w, r, root, "index.html")
	})
	return handler, func() { root.Close() }, nil
}

// serveFile writes the regular file name from root and reports whether it did; dotfiles are never served.
func serveFile(w http.ResponseWriter, r *http.Request, root *os.Root, name string) bool {
	for _, part := range strings.Split(name, "/") {
		if part == "" || strings.HasPrefix(part, ".") {
			return false
		}
	}
	file, err := root.Open(name)
	if err != nil {
		return false
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	if strings.HasPrefix(name, "assets/") {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		w.Header().Set("Cache-Control", "no-cache")
	}
	http.ServeContent(w, r, name, info.ModTime(), file)
	return true
}

// securityHeaders sets the browser policy on every response.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", contentSecurityPolicy)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "same-origin")
		next.ServeHTTP(w, r)
	})
}
