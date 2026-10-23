// Package webui renders the server-side HTML pages. Pages read their data from
// the same services and policy as the JSON API; interactive parts are mounted
// by the page's Vite entry.
package webui

import (
	"bytes"
	"embed"
	"html/template"
	"io/fs"
	"net/http"
	"strings"

	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/contracts"
)

//go:embed templates
var templateFiles embed.FS

// Page is the data every template receives; layout.gohtml reads all fields
// except Data, which belongs to the page's "content" template.
type Page struct {
	Title  string
	User   *PageUser // nil for anonymous pages: no navigation is rendered
	Active string    // navigation key marked aria-current: "databases", "admin", "connections"
	Entry  string    // Vite entry name of the page script, "" for none
	Data   any
}

// PageUser is the part of the principal a page may show; the session id never reaches HTML.
type PageUser struct {
	Username        string
	IsPlatformAdmin bool
}

func UserOf(p contracts.Principal) *PageUser {
	return &PageUser{Username: p.Username, IsPlatformAdmin: p.IsPlatformAdmin}
}

// Renderer holds one template set per page: the layout plus that page's
// "content" definition, so pages cannot override each other's blocks.
type Renderer struct {
	pages map[string]*template.Template
}

// NewRenderer parses templates/layout.gohtml with every templates/<feature>/<page>.gohtml;
// a page is addressed as "<feature>/<page>".
func NewRenderer(assets Assets) (*Renderer, error) {
	layout, err := template.New("layout.gohtml").Funcs(template.FuncMap{"assetTags": assets.Tags}).
		ParseFS(templateFiles, "templates/layout.gohtml")
	if err != nil {
		return nil, err
	}
	names, err := fs.Glob(templateFiles, "templates/*/*.gohtml")
	if err != nil {
		return nil, err
	}
	r := &Renderer{pages: map[string]*template.Template{}}
	for _, name := range names {
		page, err := layout.Clone()
		if err == nil {
			_, err = page.ParseFS(templateFiles, name)
		}
		if err != nil {
			return nil, err
		}
		r.pages[strings.TrimSuffix(strings.TrimPrefix(name, "templates/"), ".gohtml")] = page
	}
	return r, nil
}

// Render writes the page only after the whole template executed, so a template
// error becomes a clean 500 instead of a truncated document.
func (r *Renderer) Render(w http.ResponseWriter, status int, name string, page Page) {
	var body bytes.Buffer
	t, ok := r.pages[name]
	if !ok || t.ExecuteTemplate(&body, "layout", page) != nil {
		http.Error(w, "Terjadi kesalahan internal.", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_, _ = body.WriteTo(w)
}

var statusPages = map[int][2]string{
	http.StatusForbidden: {"Akses ditolak", "Anda tidak memiliki akses ke halaman ini."},
	http.StatusNotFound:  {"Halaman tidak ditemukan", "Halaman yang Anda minta tidak ada atau tidak dapat Anda akses."},
}

// Status renders the shared 403/404/500 page.
func (r *Renderer) Status(w http.ResponseWriter, status int, user *PageUser) {
	text, ok := statusPages[status]
	if !ok {
		status, text = http.StatusInternalServerError, [2]string{"Terjadi kesalahan", "Terjadi kesalahan internal. Coba lagi nanti."}
	}
	r.Render(w, status, "errors/status", Page{Title: text[0], User: user, Data: text[1]})
}

// NotFound serves the 404 page for any path no other handler claimed.
func (r *Renderer) NotFound(w http.ResponseWriter, _ *http.Request) {
	r.Status(w, http.StatusNotFound, nil)
}
