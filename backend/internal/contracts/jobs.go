package contracts

import (
	"context"
	"database/sql"
	"time"
)

type JobMessage struct{ ExecutionID, DatabaseID string }

type JobEnqueuer interface {
	// Enqueue writes the outbox row in the caller's tx; the execution row must
	// already be inserted in the same tx. Trace context comes from ctx.
	Enqueue(ctx context.Context, tx *sql.Tx, m JobMessage) error
}

type JobResult struct {
	Ack    bool // true: done, held or duplicate; false: redeliver after Delay
	Delay  time.Duration
	Reason string // done|retry|lock_busy|ordering|held|duplicate (metric label)
}

type JobHandler interface {
	// Handle never panics; errors are logged and mapped to a JobResult.
	Handle(ctx context.Context, m JobMessage) JobResult
}

type StaleJobSource interface {
	// StaleJobs lists queued executions without an open outbox row older than
	// olderThan, and running executions with a stale heartbeat.
	StaleJobs(ctx context.Context, olderThan time.Duration) ([]JobMessage, error)
}
