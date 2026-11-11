package jobs

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/contracts"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/platform"
)

// PrincipalFunc reads the principal the session middleware put on the request
// context (auth.PrincipalFromContext when wired). false means no valid session.
type PrincipalFunc func(context.Context) (contracts.Principal, bool)

// EventsHandler streams one database's events as Server-Sent Events. Events
// only tell the client what to refetch; they never carry SQL or target data.
type EventsHandler struct {
	bus       *EventBus
	access    contracts.AccessChecker
	principal PrincipalFunc

	Heartbeat time.Duration // SSE_HEARTBEAT_INTERVAL, default 15s
	Recheck   time.Duration // how often access is checked again, default 30s
	// Revalidate decides whether the stream may continue; it defaults to view
	// access, which also fails for deactivated users. Composition may add a
	// session check.
	Revalidate func(ctx context.Context, p contracts.Principal, databaseID string) error
}

func NewEventsHandler(bus *EventBus, access contracts.AccessChecker, principal PrincipalFunc) *EventsHandler {
	h := &EventsHandler{bus: bus, access: access, principal: principal, Heartbeat: 15 * time.Second, Recheck: 30 * time.Second}
	h.Revalidate = func(ctx context.Context, p contracts.Principal, id string) error {
		return access.Require(ctx, p, contracts.ViewDatabase, id)
	}
	return h
}

func RegisterRoutes(mux *http.ServeMux, h *EventsHandler) {
	mux.Handle("GET /api/v1/databases/{id}/events", h)
}

func (h *EventsHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p, ok := h.principal(ctx)
	if !ok {
		_ = platform.WriteError(w, r, &contracts.DomainError{Code: "UNAUTHENTICATED", Message: "Silakan login untuk melanjutkan."})
		return
	}
	id := r.PathValue("id")
	if !contracts.ValidID(id) {
		_ = platform.WriteError(w, r, &contracts.DomainError{Code: "INVALID_REQUEST", Message: "Permintaan tidak valid.",
			Fields: map[string]string{"id": "harus UUID huruf kecil"}})
		return
	}
	if err := h.access.Require(ctx, p, contracts.ViewDatabase, id); err != nil {
		_ = platform.WriteError(w, r, err)
		return
	}

	// A full buffer means this client is too slow; closing the stream makes it
	// reconnect and refetch, which is never staler than a dropped event.
	events := make(chan contracts.DatabaseEvent, 64)
	overflow := make(chan struct{})
	full := false
	unsubscribe, err := h.bus.subscribe(id, func(e contracts.DatabaseEvent) {
		if full {
			return
		}
		select {
		case events <- e:
		default:
			full = true
			close(overflow)
		}
	})
	if err != nil {
		_ = platform.WriteError(w, r, &contracts.DomainError{Code: "INTERNAL_ERROR", Message: "Stream event tidak tersedia.", Cause: err})
		return
	}
	defer unsubscribe()

	rc := http.NewResponseController(w)
	hd := w.Header()
	hd.Set("Content-Type", "text/event-stream")
	hd.Set("Cache-Control", "no-cache")
	hd.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	write := func(format string, args ...any) bool {
		// The server's write timeout is for ordinary responses; each write here
		// gets its own deadline instead.
		_ = rc.SetWriteDeadline(time.Now().Add(h.Heartbeat + 10*time.Second))
		if _, err := fmt.Fprintf(w, format, args...); err != nil {
			return false
		}
		return rc.Flush() == nil
	}
	if !write("retry: 3000\n\n") {
		return
	}
	heartbeat, recheck := time.NewTicker(h.Heartbeat), time.NewTicker(h.Recheck)
	defer heartbeat.Stop()
	defer recheck.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-overflow:
			return
		case <-heartbeat.C:
			if !write(": ping\n\n") {
				return
			}
		case <-recheck.C:
			if err := h.Revalidate(ctx, p, id); err != nil {
				slog.InfoContext(ctx, "event stream closed: access no longer valid", "event", "sse.closed", "database_id", id)
				return
			}
		case e := <-events:
			data, _ := json.Marshal(eventJSON{DatabaseID: e.DatabaseID, ResourceID: e.ResourceID, Status: e.Status, At: e.At})
			if !write("event: %s\nid: %s\ndata: %s\n\n", e.Type, e.ResourceID, data) {
				return
			}
		}
	}
}
