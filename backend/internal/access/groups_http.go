package access

import (
	"context"
	"net/http"

	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/contracts"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/platform"
)

type GroupHandler struct {
	groups    *GroupService
	access    contracts.AccessChecker
	principal func(context.Context) (contracts.Principal, bool)
}

func NewGroupHandler(groups *GroupService, policy contracts.AccessChecker, principal func(context.Context) (contracts.Principal, bool)) *GroupHandler {
	return &GroupHandler{groups: groups, access: policy, principal: principal}
}

func RegisterGroupRoutes(mux *http.ServeMux, h *GroupHandler) {
	mux.HandleFunc("GET /api/v1/admin/groups", h.list)
	mux.HandleFunc("POST /api/v1/admin/groups", h.create)
	mux.HandleFunc("PATCH /api/v1/admin/groups/{groupId}", h.update)
	mux.HandleFunc("DELETE /api/v1/admin/groups/{groupId}", h.delete)
	mux.HandleFunc("GET /api/v1/admin/groups/{groupId}/members", h.members)
	mux.HandleFunc("PUT /api/v1/admin/groups/{groupId}/members/{userId}", h.addMember)
	mux.HandleFunc("DELETE /api/v1/admin/groups/{groupId}/members/{userId}", h.removeMember)
	mux.HandleFunc("GET /api/v1/admin/groups/{groupId}/grants", h.grants)
	mux.HandleFunc("PUT /api/v1/admin/groups/{groupId}/grants/{databaseId}", h.setGrant)
	mux.HandleFunc("DELETE /api/v1/admin/groups/{groupId}/grants/{databaseId}", h.removeGrant)
}

func (h *GroupHandler) list(w http.ResponseWriter, r *http.Request) {
	if !authorizeAdmin(w, r, h.access, h.principal) {
		return
	}
	limit, offset, err := parsePage(r)
	if err != nil {
		_ = platform.WriteError(w, r, err)
		return
	}
	groups, more, err := h.groups.List(r.Context(), limit, offset)
	if err != nil {
		_ = platform.WriteError(w, r, err)
		return
	}
	_ = platform.WriteList(w, r, groups, platform.Page{Limit: limit, Offset: offset, HasMore: more})
}

func (h *GroupHandler) create(w http.ResponseWriter, r *http.Request) {
	if !authorizeAdmin(w, r, h.access, h.principal) {
		return
	}
	var body struct {
		Name string `json:"name"`
	}
	if err := platform.DecodeJSON(w, r, &body, 0); err != nil {
		_ = platform.WriteError(w, r, err)
		return
	}
	group, err := h.groups.Create(r.Context(), body.Name)
	if err != nil {
		_ = platform.WriteError(w, r, err)
		return
	}
	_ = platform.WriteJSON(w, r, http.StatusCreated, group, nil)
}

func (h *GroupHandler) update(w http.ResponseWriter, r *http.Request) {
	if !authorizeAdmin(w, r, h.access, h.principal) {
		return
	}
	var body struct {
		Name string `json:"name"`
	}
	if err := platform.DecodeJSON(w, r, &body, 0); err != nil {
		_ = platform.WriteError(w, r, err)
		return
	}
	group, err := h.groups.Update(r.Context(), r.PathValue("groupId"), body.Name)
	if err != nil {
		_ = platform.WriteError(w, r, err)
		return
	}
	_ = platform.WriteJSON(w, r, http.StatusOK, group, nil)
}

func (h *GroupHandler) delete(w http.ResponseWriter, r *http.Request) {
	if !authorizeAdmin(w, r, h.access, h.principal) {
		return
	}
	if err := h.groups.Delete(r.Context(), r.PathValue("groupId")); err != nil {
		_ = platform.WriteError(w, r, err)
		return
	}
	_ = platform.WriteJSON(w, r, http.StatusNoContent, nil, nil)
}

func (h *GroupHandler) members(w http.ResponseWriter, r *http.Request) {
	if !authorizeAdmin(w, r, h.access, h.principal) {
		return
	}
	limit, offset, err := parsePage(r)
	if err != nil {
		_ = platform.WriteError(w, r, err)
		return
	}
	users, more, err := h.groups.Members(r.Context(), r.PathValue("groupId"), limit, offset)
	if err != nil {
		_ = platform.WriteError(w, r, err)
		return
	}
	_ = platform.WriteList(w, r, users, platform.Page{Limit: limit, Offset: offset, HasMore: more})
}

func (h *GroupHandler) addMember(w http.ResponseWriter, r *http.Request) {
	if !authorizeAdmin(w, r, h.access, h.principal) {
		return
	}
	var body struct{}
	if err := platform.DecodeJSON(w, r, &body, 0); err != nil {
		_ = platform.WriteError(w, r, err)
		return
	}
	if err := h.groups.AddMember(r.Context(), r.PathValue("groupId"), r.PathValue("userId")); err != nil {
		_ = platform.WriteError(w, r, err)
		return
	}
	_ = platform.WriteJSON(w, r, http.StatusNoContent, nil, nil)
}

func (h *GroupHandler) removeMember(w http.ResponseWriter, r *http.Request) {
	if !authorizeAdmin(w, r, h.access, h.principal) {
		return
	}
	if err := h.groups.RemoveMember(r.Context(), r.PathValue("groupId"), r.PathValue("userId")); err != nil {
		_ = platform.WriteError(w, r, err)
		return
	}
	_ = platform.WriteJSON(w, r, http.StatusNoContent, nil, nil)
}
