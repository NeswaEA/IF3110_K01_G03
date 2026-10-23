package webui

import (
	"net/http"

	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/auth"
)

func (p *Pages) loginPage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	p.render.Render(w, http.StatusOK, "auth/login", Page{
		Title: "Masuk", Entry: "login",
		Data: struct{ Next string }{auth.SafeNext(r.URL.Query().Get("next"))},
	})
}
