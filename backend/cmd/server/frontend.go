package main

import (
	"errors"
	"net/http"
	"os"
	"path"
	"strings"
)

func newFrontend(dir string) (http.Handler, func(), error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, nil, errors.New("frontend build directory is unavailable; build web and set WEB_DIST_DIR")
	}
	index, err := root.Stat("index.html")
	if err != nil || !index.Mode().IsRegular() {
		root.Close()
		return nil, nil, errors.New("frontend index.html is unavailable; build web first")
	}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		name := strings.TrimPrefix(r.URL.Path, "/")
		for _, part := range strings.Split(name, "/") {
			if strings.HasPrefix(part, ".") {
				http.NotFound(w, r)
				return
			}
		}
		if name == "" {
			name = "index.html"
		}
		file, err := root.Open(name)
		if os.IsNotExist(err) && path.Ext(name) == "" && !strings.HasPrefix(name, "assets/") && strings.Contains(r.Header.Get("Accept"), "text/html") {
			name = "index.html"
			file, err = root.Open(name)
		}
		if err != nil {
			http.NotFound(w, r)
			return
		}
		defer file.Close()
		info, err := file.Stat()
		if err != nil || !info.Mode().IsRegular() {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Cache-Control", "no-cache")
		http.ServeContent(w, r, name, info.ModTime(), file)
	})
	return handler, func() { root.Close() }, nil
}
