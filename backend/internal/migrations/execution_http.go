package migrations

import (
	"net/http"
	"slices"

	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/contracts"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/platform"
)

func (h *Handler) executeRequest(w http.ResponseWriter, r *http.Request, actor contracts.Principal, databaseID string) {
	if err := platform.DecodeJSON(w, r, &struct{}{}, 1024); err != nil {
		_ = platform.WriteError(w, r, err)
		return
	}
	e, reused, err := h.service.executeRequest(r.Context(), actor, databaseID, r.PathValue("rid"))
	writeExecution(w, r, e, reused, err)
}

func (h *Handler) reconcileExecution(w http.ResponseWriter, r *http.Request, actor contracts.Principal, databaseID string) {
	if err := platform.DecodeJSON(w, r, &struct{}{}, 1024); err != nil {
		_ = platform.WriteError(w, r, err)
		return
	}
	e, err := h.service.reconcileExecution(r.Context(), actor, databaseID, r.PathValue("eid"))
	writeExecution(w, r, e, false, err)
}

func writeExecution(w http.ResponseWriter, r *http.Request, e Execution, reused bool, err error) {
	status := http.StatusAccepted
	if reused {
		status = http.StatusOK
	}
	if err == nil {
		w.Header().Set("Location", "/api/v1/databases/"+e.DatabaseID+"/executions/"+e.ID)
	}
	writeResult(w, r, status, e, err)
}

func (h *Handler) getExecution(w http.ResponseWriter, r *http.Request, _ contracts.Principal, databaseID string) {
	e, err := readExecution(r.Context(), h.service.store.DB, databaseID, r.PathValue("eid"), false)
	writeResult(w, r, http.StatusOK, e.Execution, err)
}

func (h *Handler) listExecutions(w http.ResponseWriter, r *http.Request, _ contracts.Principal, databaseID string) {
	query := r.URL.Query()
	status, action, migrationID := query.Get("status"), query.Get("action"), query.Get("migration_id")
	if status != "" && !slices.Contains([]string{"queued", "held", "running", "succeeded", "failed", "partial", "partially_reverted", "outcome_unknown", "cancelled"}, status) {
		_ = platform.WriteError(w, r, invalidField("status", "Status eksekusi tidak valid."))
		return
	}
	if action != "" && !slices.Contains([]string{"apply", "rollback", "forward", "resume", "dry_run"}, action) {
		_ = platform.WriteError(w, r, invalidField("action", "Aksi eksekusi tidak valid."))
		return
	}
	if migrationID != "" && !contracts.ValidID(migrationID) {
		_ = platform.WriteError(w, r, invalidField("migration_id", "ID migrasi tidak valid."))
		return
	}
	page, err := parsePage(r)
	if err != nil {
		_ = platform.WriteError(w, r, err)
		return
	}
	items, err := h.service.listExecutions(r.Context(), databaseID, status, action, migrationID, page.Limit+1, page.Offset)
	writePage(w, r, page, items, err)
}

func (h *Handler) getHead(w http.ResponseWriter, r *http.Request, _ contracts.Principal, databaseID string) {
	head, err := h.service.getHead(r.Context(), databaseID)
	writeResult(w, r, http.StatusOK, head, err)
}

func (h *Handler) listHistory(w http.ResponseWriter, r *http.Request, _ contracts.Principal, databaseID string) {
	hide := r.URL.Query().Get("hide_rollback")
	if hide != "" && hide != "true" && hide != "false" {
		_ = platform.WriteError(w, r, invalidField("hide_rollback", "Gunakan true atau false."))
		return
	}
	page, err := parsePage(r)
	if err != nil {
		_ = platform.WriteError(w, r, err)
		return
	}
	items, err := h.service.listHistory(r.Context(), databaseID, hide == "true", page.Limit+1, page.Offset)
	writePage(w, r, page, items, err)
}
