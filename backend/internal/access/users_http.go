package access

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/contracts"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/platform"
)

type UserHandler struct {
	users     *UserService
	principal func(context.Context) (contracts.Principal, bool)
}

func NewUserHandler(users *UserService, principal func(context.Context) (contracts.Principal, bool)) *UserHandler {
	return &UserHandler{users: users, principal: principal}
}

func RegisterUserRoutes(mux *http.ServeMux, h *UserHandler) {
	mux.HandleFunc("GET /api/v1/admin/users", h.list)
	mux.HandleFunc("POST /api/v1/admin/users", h.create)
	mux.HandleFunc("PATCH /api/v1/admin/users/{userId}", h.update)
	mux.HandleFunc("PUT /api/v1/admin/users/{userId}/password", h.resetPassword)
}

func requestPrincipal(w http.ResponseWriter, r *http.Request, principalFromContext func(context.Context) (contracts.Principal, bool)) (contracts.Principal, bool) {
	principal, ok := principalFromContext(r.Context())
	if !ok || principal.UserID == "" {
		_ = platform.WriteError(w, r, &contracts.DomainError{Code: "UNAUTHENTICATED", Message: "Silakan login untuk melanjutkan."})
		return contracts.Principal{}, false
	}
	return principal, true
}

func (h *UserHandler) list(w http.ResponseWriter, r *http.Request) {
	principal, ok := requestPrincipal(w, r, h.principal)
	if !ok {
		return
	}
	limit, offset, err := parsePage(r)
	if err != nil {
		_ = platform.WriteError(w, r, err)
		return
	}
	users, more, err := h.users.List(r.Context(), principal, limit, offset)
	if err != nil {
		_ = platform.WriteError(w, r, err)
		return
	}
	_ = platform.WriteList(w, r, users, platform.Page{Limit: limit, Offset: offset, HasMore: more})
}

func (h *UserHandler) create(w http.ResponseWriter, r *http.Request) {
	principal, ok := requestPrincipal(w, r, h.principal)
	if !ok {
		return
	}
	var body struct {
		Username        string          `json:"username"`
		Password        string          `json:"password"`
		IsPlatformAdmin json.RawMessage `json:"is_platform_admin"`
	}
	if err := platform.DecodeJSON(w, r, &body, 0); err != nil {
		_ = platform.WriteError(w, r, err)
		return
	}
	admin, err := userFlag(body.IsPlatformAdmin, "is_platform_admin")
	if err != nil {
		_ = platform.WriteError(w, r, err)
		return
	}
	input := CreateUserInput{Username: body.Username, Password: body.Password}
	if admin != nil {
		input.IsPlatformAdmin = *admin
	}
	user, err := h.users.Create(r.Context(), principal, input)
	if err != nil {
		_ = platform.WriteError(w, r, err)
		return
	}
	_ = platform.WriteJSON(w, r, http.StatusCreated, user, nil)
}

func (h *UserHandler) update(w http.ResponseWriter, r *http.Request) {
	principal, ok := requestPrincipal(w, r, h.principal)
	if !ok {
		return
	}
	var body struct {
		Active          json.RawMessage `json:"active"`
		IsPlatformAdmin json.RawMessage `json:"is_platform_admin"`
	}
	if err := platform.DecodeJSON(w, r, &body, 0); err != nil {
		_ = platform.WriteError(w, r, err)
		return
	}
	active, err := userFlag(body.Active, "active")
	if err != nil {
		_ = platform.WriteError(w, r, err)
		return
	}
	admin, err := userFlag(body.IsPlatformAdmin, "is_platform_admin")
	if err != nil {
		_ = platform.WriteError(w, r, err)
		return
	}
	user, err := h.users.Update(r.Context(), principal, r.PathValue("userId"), UpdateUserInput{Active: active, IsPlatformAdmin: admin})
	if err != nil {
		_ = platform.WriteError(w, r, err)
		return
	}
	_ = platform.WriteJSON(w, r, http.StatusOK, user, nil)
}

func (h *UserHandler) resetPassword(w http.ResponseWriter, r *http.Request) {
	principal, ok := requestPrincipal(w, r, h.principal)
	if !ok {
		return
	}
	var body struct {
		Password string `json:"password"`
	}
	if err := platform.DecodeJSON(w, r, &body, 0); err != nil {
		_ = platform.WriteError(w, r, err)
		return
	}
	if err := h.users.ResetPassword(r.Context(), principal, r.PathValue("userId"), body.Password); err != nil {
		_ = platform.WriteError(w, r, err)
		return
	}
	_ = platform.WriteJSON(w, r, http.StatusNoContent, nil, nil)
}

func userFlag(raw json.RawMessage, field string) (*bool, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var value bool
	if string(raw) == "null" || json.Unmarshal(raw, &value) != nil {
		return nil, invalidUserField(field, "Gunakan true atau false.")
	}
	return &value, nil
}
