package migrations

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"time"

	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/contracts"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/database"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/telemetry"
	"go.opentelemetry.io/otel/trace"
)

type ExecutorOptions struct {
	WorkerID                         string
	HeartbeatInterval, StaleAfter    time.Duration
	LockRetryMaxDelay, OrderingDelay time.Duration
	RetryBaseDelay, RetryMaxDelay    time.Duration
	StatementTimeout, LockTimeout    time.Duration
}

type Executor struct {
	service   *Service
	provider  database.TargetProvider
	inspector database.Inspector
	lockers   map[database.Driver]database.MigrationLocker
	options   ExecutorOptions
}

var (
	_ contracts.JobHandler     = (*Executor)(nil)
	_ contracts.StaleJobSource = (*Executor)(nil)
)

func NewExecutor(s *Service, provider database.TargetProvider, inspector database.Inspector,
	lockers map[database.Driver]database.MigrationLocker, o ExecutorOptions) *Executor {
	if o.HeartbeatInterval <= 0 {
		o.HeartbeatInterval = 5 * time.Second
	}
	if o.StaleAfter <= 0 {
		o.StaleAfter = 2 * time.Minute
	}
	if o.LockRetryMaxDelay <= 0 {
		o.LockRetryMaxDelay = 30 * time.Second
	}
	if o.OrderingDelay <= 0 {
		o.OrderingDelay = time.Second
	}
	if o.RetryBaseDelay <= 0 {
		o.RetryBaseDelay = 2 * time.Second
	}
	if o.RetryMaxDelay <= 0 {
		o.RetryMaxDelay = 30 * time.Second
	}
	if o.StatementTimeout <= 0 {
		o.StatementTimeout = 30 * time.Minute
	}
	if o.LockTimeout <= 0 {
		o.LockTimeout = 5 * time.Second
	}
	return &Executor{service: s, provider: provider, inspector: inspector, lockers: lockers, options: o}
}

func ack(reason string) contracts.JobResult { return contracts.JobResult{Ack: true, Reason: reason} }

func retry(reason string, delay time.Duration) contracts.JobResult {
	return contracts.JobResult{Reason: reason, Delay: delay}
}

type executionGate struct {
	executionRow
	result    contracts.JobResult
	newlyHeld bool
	recover   bool
	exhausted bool
}

func (x *Executor) gate(ctx context.Context, m contracts.JobMessage, claim bool) (executionGate, error) {
	s := x.service
	return inTx(ctx, s.store.DB, nil, func(tx *sql.Tx) (executionGate, error) {
		if err := lockDatabase(ctx, tx, m.DatabaseID); err != nil {
			return executionGate{}, err
		}
		e, err := readExecution(ctx, tx, m.DatabaseID, m.ExecutionID, true)
		if err != nil {
			return executionGate{}, err
		}
		g := executionGate{executionRow: e}
		switch e.Status {
		case "held":
			g.result = ack("held")
			return g, nil
		case "queued", "running":
		default:
			g.result = ack("duplicate")
			return g, nil
		}
		now := s.store.now()
		if e.nextAttemptAt != nil && e.nextAttemptAt.After(now) {
			g.result = retry("retry", e.nextAttemptAt.Sub(now))
			return g, nil
		}
		if !e.reconcile {
			block, err := findBlock(ctx, tx, e.DatabaseID)
			if err != nil {
				return g, err
			}
			if block != nil && e.Status == "queued" {
				if _, err := tx.ExecContext(ctx, `UPDATE migration_executions SET status='held', held_reason=$2 WHERE id=$1`, e.ID, block.reason); err != nil {
					return g, internal(err)
				}
				g.Status, g.HeldReason, g.result, g.newlyHeld = "held", &block.reason, ack("held"), true
				return g, nil
			}
			var earlier bool
			if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM migration_executions
				WHERE database_id=$1 AND number<$2 AND status IN ('queued','running'))`, e.DatabaseID, e.Number).Scan(&earlier); err != nil {
				return g, internal(err)
			}
			if earlier {
				g.result = retry("ordering", x.options.OrderingDelay)
				return g, nil
			}
		}
		if !claim {
			if e.lockRequested == nil {
				if _, err := tx.ExecContext(ctx, `UPDATE migration_executions SET lock_requested_at=$2 WHERE id=$1`, e.ID, now); err != nil {
					return g, internal(err)
				}
			}
			return g, nil
		}
		g.recover = e.Status == "running" || e.StartedAt != nil || e.reconcile
		g.exhausted = e.Attempt >= e.MaxAttempts
		g.Attempt = min(e.Attempt+1, e.MaxAttempts)
		g.Status = "running"
		g.workerID = &x.options.WorkerID
		g.heartbeatAt = &now
		if g.StartedAt == nil {
			g.StartedAt = &now
		}
		traceID := trace.SpanContextFromContext(ctx).TraceID()
		if traceID.IsValid() {
			g.TraceID = new(traceID.String())
		}
		_, err = tx.ExecContext(ctx, `UPDATE migration_executions SET status='running', attempt=$2,
			worker_id=$3, heartbeat_at=$4, started_at=$5, next_attempt_at=NULL, error_code=NULL,
			error_message=NULL, trace_id=$6 WHERE id=$1`,
			e.ID, g.Attempt, x.options.WorkerID, now, g.StartedAt, g.TraceID)
		if err != nil {
			return g, internal(err)
		}
		return g, nil
	})
}

func (x *Executor) Handle(ctx context.Context, m contracts.JobMessage) contracts.JobResult {
	if !contracts.ValidID(m.ExecutionID) || !contracts.ValidID(m.DatabaseID) {
		return ack("duplicate")
	}
	g, err := x.gate(ctx, m, false)
	if err != nil || g.result.Reason != "" {
		return x.gateResult(ctx, m, g, err)
	}
	target, release, err := x.provider.Acquire(ctx, m.DatabaseID)
	if err != nil {
		return x.acquireFailed(ctx, g.executionRow, err)
	}
	defer release()
	locker := x.lockers[target.Driver]
	if locker == nil {
		return x.acquireFailed(ctx, g.executionRow, database.ErrTargetNotReady)
	}
	lock, err := locker.Acquire(ctx, target)
	if errors.Is(err, database.ErrLockBusy) {
		x.log(ctx, g.executionRow, "lock.busy")
		delay := max(time.Second, database.Backoff(time.Second, x.options.LockRetryMaxDelay, 8))
		return retry("lock_busy", min(delay, x.options.LockRetryMaxDelay))
	}
	if err != nil {
		return x.acquireFailed(ctx, g.executionRow, err)
	}
	defer lock.Release()
	g, err = x.gate(ctx, m, true)
	if err != nil || g.result.Reason != "" {
		return x.gateResult(ctx, m, g, err)
	}
	e := g.executionRow
	ctx = telemetry.WithLogAttrs(ctx, slog.String("execution_id", e.ID), slog.String("job_id", e.ID),
		slog.String("actor_id", e.Actor.ID), slog.String("migration_id", e.MigrationID),
		slog.Int64("migration_revision", e.MigrationRevision), slog.Int("attempt", e.Attempt),
		slog.String("direction", e.Direction))
	if e.ChangeRequestID != nil {
		ctx = telemetry.WithLogAttrs(ctx, slog.String("change_request_id", *e.ChangeRequestID))
	}
	if e.lockRequested != nil {
		telemetry.RecordLockWait(ctx, string(target.Driver), x.service.store.now().Sub(*e.lockRequested))
	}
	if e.Attempt == 1 && !g.recover {
		telemetry.RecordQueueWait(ctx, e.Action, x.service.store.now().Sub(e.EnqueuedAt))
	}
	x.log(ctx, e, "lock.acquired")
	defer x.log(ctx, e, "lock.released")
	x.log(ctx, e, "execution.started")
	x.service.recordExecution(ctx, "execution.started", e.Execution)

	work, stop := x.track(ctx, lock, e)
	defer stop()
	if g.recover {
		proof, err := x.readEvidence(work, target, lock.Conn, e)
		if err != nil {
			return x.retryAttempt(ctx, e, runFailure("TARGET_UNAVAILABLE", "Bukti commit belum dapat diperiksa."), true)
		}
		if proof != nil {
			return x.complete(ctx, e, "succeeded", nil, proof)
		}
		if e.reconcile {
			return x.complete(ctx, e, "failed", runFailure("EXECUTION_INTERRUPTED", "Bukti commit tidak ditemukan."), nil)
		}
		if g.exhausted {
			return x.complete(ctx, e, "failed", runFailure("RETRY_EXHAUSTED", "Batas percobaan tercapai."), nil)
		}
	}
	history, failure := x.runNormal(work, target, lock, e)
	switch {
	case failure == nil:
		return x.complete(ctx, e, "succeeded", nil, history)
	case failure.uncertain:
		// Keep ownership recorded until another session can verify the commit.
		return x.deferEvidence(ctx, e, failure.detail)
	case work.Err() != nil:
		return x.deferEvidence(ctx, e, runFailure("EXECUTION_INTERRUPTED", "Eksekusi terputus."))
	case failure.transient:
		return x.retryAttempt(ctx, e, failure.detail, false)
	default:
		return x.complete(ctx, e, "failed", failure.detail, nil)
	}
}

func (x *Executor) gateResult(ctx context.Context, m contracts.JobMessage, g executionGate, err error) contracts.JobResult {
	if err != nil {
		var problem *contracts.DomainError
		if errors.As(err, &problem) && problem.Code == "NOT_FOUND" {
			return ack("duplicate")
		}
		slog.WarnContext(ctx, "execution state unavailable", "event", "execution.state_unavailable",
			"database_id", m.DatabaseID, "execution_id", m.ExecutionID)
		return retry("retry", x.options.RetryBaseDelay)
	}
	if g.newlyHeld {
		x.log(ctx, g.executionRow, "execution.held")
		x.service.recordExecution(ctx, "execution.held", g.Execution)
	}
	return g.result
}

func (x *Executor) track(ctx context.Context, lock database.MigrationLock, e executionRow) (context.Context, func()) {
	work, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(x.options.HeartbeatInterval)
		defer ticker.Stop()
		for {
			select {
			case <-work.Done():
				return
			case <-lock.Lost:
				cancel()
				return
			case <-ticker.C:
				beat, stop := context.WithTimeout(work, x.options.HeartbeatInterval)
				_, _ = x.service.store.DB.ExecContext(beat, `UPDATE migration_executions SET heartbeat_at=$4
					WHERE id=$1 AND worker_id=$2 AND attempt=$3 AND status='running'`,
					e.ID, x.options.WorkerID, e.Attempt, x.service.store.now())
				stop()
			}
		}
	}()
	return work, func() { cancel(); <-done }
}

func (x *Executor) StaleJobs(ctx context.Context, olderThan time.Duration) ([]contracts.JobMessage, error) {
	now := x.service.store.now()
	rows, err := x.service.store.DB.QueryContext(ctx, `SELECT e.id, e.database_id FROM migration_executions e
		WHERE (e.status='queued' AND COALESCE(e.next_attempt_at,e.enqueued_at)<=$1
			OR e.status='running' AND COALESCE(e.heartbeat_at,e.started_at,e.enqueued_at)<=$1)
		AND (e.next_attempt_at IS NULL OR e.next_attempt_at<=$2)
		AND NOT EXISTS (SELECT 1 FROM outbox_messages o WHERE o.published_at IS NULL AND o.payload->>'execution_id'=e.id::text)
		ORDER BY e.database_id, e.number LIMIT 256`, now.Add(-olderThan), now)
	if err != nil {
		return nil, internal(err)
	}
	return collectRows(rows, func(row scanner) (contracts.JobMessage, error) {
		var m contracts.JobMessage
		err := row.Scan(&m.ExecutionID, &m.DatabaseID)
		return m, err
	})
}

func (x *Executor) log(ctx context.Context, e executionRow, event string) {
	attrs := []any{"event", event, "execution_id", e.ID, "job_id", e.ID, "database_id", e.DatabaseID,
		"migration_id", e.MigrationID, "migration_revision", e.MigrationRevision, "attempt", e.Attempt,
		"actor_id", e.Actor.ID, "status", e.Status, "direction", e.Direction}
	if e.ChangeRequestID != nil {
		attrs = append(attrs, "change_request_id", *e.ChangeRequestID)
	}
	if e.Error != nil {
		attrs = append(attrs, "error_code", e.Error.Code)
	}
	slog.InfoContext(ctx, event, attrs...)
}

func runFailure(code, message string) *RunError { return &RunError{Code: code, Message: message} }
