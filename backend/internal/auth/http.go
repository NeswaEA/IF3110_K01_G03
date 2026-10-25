package auth

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"errors"
	"net/http"
	"net/url"
	"time"

	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/contracts"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/platform"
)

type HTTPHandler struct {
	sessions *SessionStore
	origin   string
	secure   bool
}

type sessionContextKey struct{}

type userResponse struct {
	ID              string `json:"id"`
	Username        string `json:"username"`
	IsPlatformAdmin bool   `json:"is_platform_admin"`
}

func NewHTTPHandler(sessions *SessionStore, origin string, secure bool) (*HTTPHandler, error) {
	u, err := url.Parse(origin)
	if sessions == nil || err != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") ||
		u.User != nil || u.Path != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return nil, errors.New("auth requires a session store and an HTTP(S) origin")
	}
	return &HTTPHandler{sessions: sessions, origin: origin, secure: secure || u.Scheme == "https"}, nil
}

func (h *HTTPHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/auth/csrf", h.csrf)
	mux.Handle("POST /api/v1/auth/login", h.RequireCSRF(http.HandlerFunc(h.login)))
	mux.Handle("GET /api/v1/auth/me", h.RequireSession(http.HandlerFunc(h.me)))
	mux.Handle("POST /api/v1/auth/logout", h.RequireSession(h.RequireCSRF(http.HandlerFunc(h.logout))))
}

func PrincipalFromContext(ctx context.Context) (contracts.Principal, bool) {
	session, ok := ctx.Value(sessionContextKey{}).(Session)
	return session.Principal, ok && session.Principal.UserID != ""
}

func (h *HTTPHandler) RequireSession(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r, err := h.withSession(w, r)
		if err != nil {
			_ = platform.WriteError(w, r, err)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// RequirePage guards HTML pages: a visitor without a signed-in session is sent
// to the login page instead of receiving the JSON 401 used by the API.
func (h *HTTPHandler) RequirePage(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r, err := h.withSession(w, r)
		if isUnauthenticated(err) {
			RedirectToLogin(w, r)
			return
		}
		if err != nil {
			http.Error(w, "Terjadi kesalahan internal.", http.StatusInternalServerError)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// OptionalPage attaches the signed-in session when there is one and otherwise
// serves the page anonymously, for pages such as the 404 where a login redirect is wrong.
func (h *HTTPHandler) OptionalPage(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if signedIn, err := h.withSession(w, r); err == nil {
			r = signedIn
		}
		next.ServeHTTP(w, r)
	})
}

// RedirectToLogin sends the browser to the login page with the current local
// path as the return target.
func RedirectToLogin(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/login?next="+url.QueryEscape(SafeNext(r.URL.RequestURI())), http.StatusFound)
}

func (h *HTTPHandler) withSession(w http.ResponseWriter, r *http.Request) (*http.Request, error) {
	w.Header().Set("Cache-Control", "no-store")
	session, err := h.resolve(r)
	if err == nil && session.Principal.UserID == "" {
		err = unauthenticated()
	}
	if err != nil {
		return r, err
	}
	h.setCookie(w, session)
	return r.WithContext(context.WithValue(r.Context(), sessionContextKey{}, session)), nil
}

func (h *HTTPHandler) RequireCSRF(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if r.Method == http.MethodGet || r.Method == http.MethodHead || r.Method == http.MethodOptions {
			next.ServeHTTP(w, r)
			return
		}
		if len(r.Header.Values("Origin")) != 1 || r.Header.Get("Origin") != h.origin || len(r.Header.Values("X-CSRF-Token")) != 1 {
			_ = platform.WriteError(w, r, csrfRejected())
			return
		}
		session, err := h.resolve(r)
		if isUnauthenticated(err) {
			err = csrfRejected()
		}
		if err == nil && subtle.ConstantTimeCompare([]byte(r.Header.Get("X-CSRF-Token")), []byte(session.CSRFToken)) != 1 {
			err = csrfRejected()
		}
		if err != nil {
			_ = platform.WriteError(w, r, err)
			return
		}
		if _, cached := r.Context().Value(sessionContextKey{}).(Session); !cached {
			h.setCookie(w, session)
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), sessionContextKey{}, session)))
	})
}

func (h *HTTPHandler) resolve(r *http.Request) (Session, error) {
	if session, ok := r.Context().Value(sessionContextKey{}).(Session); ok {
		return session, nil
	}
	cookies := r.CookiesNamed("session")
	if len(cookies) != 1 {
		return Session{}, unauthenticated()
	}
	return h.sessions.Resolve(r.Context(), cookies[0].Value)
}

func (h *HTTPHandler) csrf(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	session, err := h.resolve(r)
	if isUnauthenticated(err) {
		var tx *sql.Tx
		tx, err = h.sessions.db.BeginTx(r.Context(), nil)
		if err == nil {
			defer tx.Rollback()
			err = h.sessions.pruneInTx(r.Context(), tx)
		}
		if err == nil {
			session, err = h.sessions.CreateInTx(r.Context(), tx, "")
			if err == nil {
				err = tx.Commit()
			}
		}
	}
	if err != nil {
		_ = platform.WriteError(w, r, err)
		return
	}
	h.setCookie(w, session)
	_ = platform.WriteJSON(w, r, http.StatusOK, struct {
		CSRFToken string `json:"csrf_token"`
	}{session.CSRFToken}, nil)
}

func (h *HTTPHandler) login(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := platform.DecodeJSON(w, r, &input, platform.DefaultMaxBodyBytes); err != nil {
		_ = platform.WriteError(w, r, err)
		return
	}
	prelogin, _ := r.Context().Value(sessionContextKey{}).(Session)
	session, err := h.sessions.login(r.Context(), prelogin, input.Username, input.Password)
	if err != nil {
		_ = platform.WriteError(w, r, err)
		return
	}
	h.setCookie(w, session)
	_ = platform.WriteJSON(w, r, http.StatusOK, struct {
		User userResponse `json:"user"`
	}{userDTO(session.Principal)}, nil)
}

func (h *HTTPHandler) me(w http.ResponseWriter, r *http.Request) {
	principal, _ := PrincipalFromContext(r.Context())
	_ = platform.WriteJSON(w, r, http.StatusOK, userDTO(principal), nil)
}

func (h *HTTPHandler) logout(w http.ResponseWriter, r *http.Request) {
	if err := platform.DecodeJSON(w, r, &struct{}{}, platform.DefaultMaxBodyBytes); err != nil {
		_ = platform.WriteError(w, r, err)
		return
	}
	principal, _ := PrincipalFromContext(r.Context())
	tx, err := h.sessions.db.BeginTx(r.Context(), nil)
	if err == nil {
		defer tx.Rollback()
		err = h.sessions.RevokeInTx(r.Context(), tx, principal.SessionID)
		if err == nil {
			err = tx.Commit()
		}
	}
	if err != nil {
		_ = platform.WriteError(w, r, err)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: "session", Path: "/", HttpOnly: true,
		Secure: h.secure, SameSite: http.SameSiteLaxMode, MaxAge: -1, Expires: time.Unix(1, 0)})
	_ = platform.WriteJSON(w, r, http.StatusNoContent, nil, nil)
}

func (h *HTTPHandler) setCookie(w http.ResponseWriter, session Session) {
	if session.Token == "" {
		return
	}
	http.SetCookie(w, &http.Cookie{Name: "session", Value: session.Token, Path: "/", HttpOnly: true,
		Secure: h.secure, SameSite: http.SameSiteLaxMode, Expires: session.AbsoluteExpiresAt})
}

func userDTO(p contracts.Principal) userResponse {
	return userResponse{ID: p.UserID, Username: p.Username, IsPlatformAdmin: p.IsPlatformAdmin}
}

func isUnauthenticated(err error) bool {
	var domain *contracts.DomainError
	return errors.As(err, &domain) && domain.Code == "UNAUTHENTICATED"
}

func csrfRejected() error {
	return &contracts.DomainError{Code: "CSRF_REJECTED", Message: "Origin atau token CSRF tidak valid."}
}
