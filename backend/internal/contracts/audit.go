package contracts

import "context"

type AuditEvent struct {
	Action       string
	ActorID      *string
	ActorName    *string
	DatabaseID   *string
	ResourceType string
	ResourceID   string
	Details      map[string]any
}

// AuditRecorder writes an audit event to the application log. Callers invoke
// it only after the business transaction committed; request_id and trace_id
// come from ctx. It never fails the caller.
type AuditRecorder interface {
	Record(ctx context.Context, e AuditEvent)
}
