package main

import (
	"errors"
	"net/http"
	"os"
	"strings"
)

// newFrontend serves the built static files (hashed assets and public files)
// from dir. Pages are rendered by webui; any path that is not a regular file,
// including dotfiles such as the build manifest, goes to notFound.
func newFrontend(dir string, notFound http.Handler) (http.Handler, func(), error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, nil, errors.New("frontend build directory is unavailable; build web and set WEB_DIST_DIR")
	}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		name := strings.TrimPrefix(r.URL.Path, "/")
		for _, part := range strings.Split(name, "/") {
			if part == "" || strings.HasPrefix(part, ".") {
				notFound.ServeHTTP(w, r)
				return
			}
		}
		file, err := root.Open(name)
		if err != nil {
			notFound.ServeHTTP(w, r)
			return
		}
		defer file.Close()
		info, err := file.Stat()
		if err != nil || !info.Mode().IsRegular() {
			notFound.ServeHTTP(w, r)
			return
		}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Cache-Control", "no-cache")
		http.ServeContent(w, r, name, info.ModTime(), file)
	})
	return handler, func() { root.Close() }, nil
}
