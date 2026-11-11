package audit

import (
	"context"
	"log/slog"
	"slices"
	"strings"

	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/contracts"
)

var actions = []string{
	"migration.created", "migration.updated",
	"request.submitted", "request.approved", "request.rejected", "request.cancelled",
	"execution.enqueued", "execution.held", "execution.started", "execution.finished",
	"execution.stop_requested", "execution.cancelled", "execution.reconcile_requested",
	"history.recorded", "group.leader_changed", "grant.changed", "user.changed", "connection.changed",
}

// sqlKeys never reach the log: the details carry content_hash and revision instead of SQL text.
var sqlKeys = []string{"sql", "up_sql", "down_sql", "query", "statement"}

// Recorder writes audit events to the application log; nothing is stored in the database.
type Recorder struct{ logger *slog.Logger }

var _ contracts.AuditRecorder = (*Recorder)(nil)

func NewRecorder(logger *slog.Logger) *Recorder { return &Recorder{logger: logger} }

// Record logs one event=audit line. Call it only after the business transaction committed.
func (r *Recorder) Record(ctx context.Context, e contracts.AuditEvent) {
	if !slices.Contains(actions, e.Action) {
		r.logger.ErrorContext(ctx, "audit event rejected", "event", "audit.rejected", "action", e.Action)
		return
	}
	attrs := []slog.Attr{slog.String("event", "audit"), slog.String("action", e.Action)}
	for key, value := range map[string]*string{"actor_id": e.ActorID, "actor_name": e.ActorName, "database_id": e.DatabaseID} {
		if value != nil {
			attrs = append(attrs, slog.String(key, *value))
		}
	}
	attrs = append(attrs, slog.String("resource_type", e.ResourceType), slog.String("resource_id", e.ResourceID),
		slog.Attr{Key: "details", Value: slog.GroupValue(detailAttrs(e.Details)...)})
	r.logger.LogAttrs(ctx, slog.LevelInfo, "audit", attrs...)
}

// detailAttrs becomes group members so the logger's key redaction covers nested values too.
func detailAttrs(details map[string]any) []slog.Attr {
	keys := make([]string, 0, len(details))
	for key := range details {
		if !slices.Contains(sqlKeys, strings.ToLower(key)) {
			keys = append(keys, key)
		}
	}
	slices.Sort(keys)
	attrs := make([]slog.Attr, len(keys))
	for i, key := range keys {
		if nested, ok := details[key].(map[string]any); ok {
			attrs[i] = slog.Attr{Key: key, Value: slog.GroupValue(detailAttrs(nested)...)}
		} else {
			attrs[i] = slog.Any(key, details[key])
		}
	}
	return attrs
}
