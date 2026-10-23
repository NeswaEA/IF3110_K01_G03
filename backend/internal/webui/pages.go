package webui

import (
	"context"
	"errors"
	"net/http"
	"regexp"

	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/contracts"
)

// PrincipalFunc reads the principal the page guard put on the request context
// (auth.PrincipalFromContext when wired).
type PrincipalFunc func(context.Context) (contracts.Principal, bool)

// Pages serves the HTML pages. The guard (auth RequirePage when wired) runs
// before every signed-in page; each handler still checks the same policy as the API.
type Pages struct {
	render    *Renderer
	access    contracts.AccessChecker
	principal PrincipalFunc
	login     http.HandlerFunc
}

func NewPages(render *Renderer, access contracts.AccessChecker, principal PrincipalFunc, login http.HandlerFunc) *Pages {
	return &Pages{render: render, access: access, principal: principal, login: login}
}

func RegisterRoutes(mux *http.ServeMux, p *Pages, guard func(http.Handler) http.Handler) {
	page := func(h http.HandlerFunc) http.Handler { return guard(h) }
	mux.Handle("GET /{$}", page(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/databases", http.StatusFound)
	}))
	mux.Handle("GET /databases", page(p.databases))
	mux.Handle("GET /databases/{id}", page(p.database))
	mux.Handle("GET /admin/connections", page(p.connections))
}

var uuidRe = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// authorize returns the signed-in user after the policy allowed action; on
// failure it has already written the error page or redirect.
func (p *Pages) authorize(w http.ResponseWriter, r *http.Request, action contracts.Action, databaseID string) (*PageUser, bool) {
	principal, ok := p.principal(r.Context())
	if !ok {
		p.login(w, r)
		return nil, false
	}
	user := UserOf(principal)
	if action == "" {
		return user, true
	}
	if action != contracts.ManagePlatform && !uuidRe.MatchString(databaseID) {
		p.render.Status(w, http.StatusNotFound, user)
		return user, false
	}
	if err := p.access.Require(r.Context(), principal, action, databaseID); err != nil {
		p.fail(w, r, user, err)
		return user, false
	}
	return user, true
}

// fail maps a domain error to the page status; 403 and 404 stay distinct as in the API.
func (p *Pages) fail(w http.ResponseWriter, r *http.Request, user *PageUser, err error) {
	var domain *contracts.DomainError
	code := ""
	if errors.As(err, &domain) && domain != nil {
		code = domain.Code
	}
	switch code {
	case "UNAUTHENTICATED":
		p.login(w, r)
	case "FORBIDDEN":
		p.render.Status(w, http.StatusForbidden, user)
	case "NOT_FOUND":
		p.render.Status(w, http.StatusNotFound, user)
	default:
		p.render.Status(w, http.StatusInternalServerError, user)
	}
}

func (p *Pages) databases(w http.ResponseWriter, r *http.Request) {
	user, ok := p.authorize(w, r, "", "")
	if !ok {
		return
	}
	p.render.Render(w, http.StatusOK, "viewer/databases", Page{Title: "Database", User: user, Active: "databases", Entry: "databases"})
}

func (p *Pages) database(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	user, ok := p.authorize(w, r, contracts.ViewDatabase, id)
	if !ok {
		return
	}
	p.render.Render(w, http.StatusOK, "viewer/database", Page{Title: "Database", User: user, Active: "databases", Entry: "database",
		Data: struct{ ID string }{id}})
}

func (p *Pages) connections(w http.ResponseWriter, r *http.Request) {
	user, ok := p.authorize(w, r, contracts.ManagePlatform, "")
	if !ok {
		return
	}
	p.render.Render(w, http.StatusOK, "connections/index", Page{Title: "Koneksi database", User: user, Active: "connections", Entry: "connections"})
}
