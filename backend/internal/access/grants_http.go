package access

import (
	"net/http"

	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/contracts"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/platform"
)

func (h *GroupHandler) grants(w http.ResponseWriter, r *http.Request) {
	if !authorizeAdmin(w, r, h.access, h.principal) {
		return
	}
	limit, offset, err := parsePage(r)
	if err != nil {
		_ = platform.WriteError(w, r, err)
		return
	}
	grants, more, err := h.groups.Grants(r.Context(), r.PathValue("groupId"), limit, offset)
	if err != nil {
		_ = platform.WriteError(w, r, err)
		return
	}
	_ = platform.WriteList(w, r, grants, platform.Page{Limit: limit, Offset: offset, HasMore: more})
}

func (h *GroupHandler) setGrant(w http.ResponseWriter, r *http.Request) {
	if !authorizeAdmin(w, r, h.access, h.principal) {
		return
	}
	var body struct {
		Level contracts.AccessLevel `json:"level"`
	}
	if err := platform.DecodeJSON(w, r, &body, 0); err != nil {
		_ = platform.WriteError(w, r, err)
		return
	}
	grant, err := h.groups.SetGrant(r.Context(), r.PathValue("groupId"), r.PathValue("databaseId"), body.Level)
	if err != nil {
		_ = platform.WriteError(w, r, err)
		return
	}
	_ = platform.WriteJSON(w, r, http.StatusOK, grant, nil)
}

func (h *GroupHandler) removeGrant(w http.ResponseWriter, r *http.Request) {
	if !authorizeAdmin(w, r, h.access, h.principal) {
		return
	}
	if err := h.groups.RemoveGrant(r.Context(), r.PathValue("groupId"), r.PathValue("databaseId")); err != nil {
		_ = platform.WriteError(w, r, err)
		return
	}
	_ = platform.WriteJSON(w, r, http.StatusNoContent, nil, nil)
}
