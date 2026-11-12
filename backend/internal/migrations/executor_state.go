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
)

type executionTransition struct {
	status  string
	attempt int
	next    *time.Time
	detail  *RunError
	history *HistoryRecord
}

func (x *Executor) retryDelay(attempt int) time.Duration {
	return database.Backoff(x.options.RetryBaseDelay, x.options.RetryMaxDelay, attempt)
}

func (x *Executor) complete(ctx context.Context, e executionRow, status string, detail *RunError, history *HistoryRecord) contracts.JobResult {
	return x.transition(ctx, e, executionTransition{status: status, attempt: e.Attempt, detail: detail, history: history}, false)
}

func (x *Executor) deferEvidence(ctx context.Context, e executionRow, detail *RunError) contracts.JobResult {
	return x.schedule(ctx, e, "running", e.Attempt, detail, false)
}

func (x *Executor) retryAttempt(ctx context.Context, e executionRow, detail *RunError, uncertain bool) contracts.JobResult {
	if e.Attempt >= e.MaxAttempts || e.reconcile {
		if uncertain {
			return x.complete(ctx, e, "outcome_unknown", detail, nil)
		}
		return x.complete(ctx, e, "failed", runFailure("RETRY_EXHAUSTED", "Batas percobaan tercapai."), nil)
	}
	if uncertain {
		return x.deferEvidence(ctx, e, detail)
	}
	return x.schedule(ctx, e, "queued", e.Attempt, detail, false)
}

func (x *Executor) schedule(ctx context.Context, e executionRow, status string, attempt int, detail *RunError, staleOnly bool) contracts.JobResult {
	next := x.service.store.now().Add(x.retryDelay(attempt))
	return x.transition(ctx, e, executionTransition{status: status, attempt: attempt, next: &next, detail: detail}, staleOnly)
}

func (x *Executor) acquireFailed(ctx context.Context, e executionRow, err error) contracts.JobResult {
	if ctx.Err() != nil {
		return retry("retry", x.options.RetryBaseDelay)
	}
	// A delivery can race the live holder. A failed connection attempt alone
	// cannot revoke ownership or establish whether that holder committed.
	staleOnly := e.Status == "running"
	if staleOnly && e.heartbeatAt != nil && x.service.store.now().Sub(*e.heartbeatAt) < x.options.StaleAfter {
		return retry("retry", x.options.RetryBaseDelay)
	}
	attempt := min(e.Attempt+1, e.MaxAttempts)
	uncertain := staleOnly || e.reconcile
	failure := targetFailure(err)
	if errors.Is(err, database.ErrNotFound) {
		failure.detail = runFailure("ACCESS_REVOKED", "Akses ke database dicabut atau koneksi dinonaktifkan.")
	}
	if attempt < e.MaxAttempts && !e.reconcile && (failure.transient || uncertain) {
		return x.schedule(ctx, e, e.Status, attempt, failure.detail, staleOnly)
	}
	status := "failed"
	if uncertain {
		status = "outcome_unknown"
	} else if failure.transient {
		failure.detail = runFailure("RETRY_EXHAUSTED", "Batas percobaan tercapai.")
	}
	return x.transition(ctx, e, executionTransition{status: status, attempt: attempt, detail: failure.detail}, staleOnly)
}

type transitionResult struct {
	executionRow
	changed  bool
	released []string
}

func (x *Executor) transition(ctx context.Context, previous executionRow, change executionTransition, staleOnly bool) contracts.JobResult {
	ctx, span := telemetry.Tracer().Start(context.WithoutCancel(ctx), "execution.finalize")
	defer span.End()
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	s := x.service
	result, err := inTx(ctx, s.store.DB, nil, func(tx *sql.Tx) (transitionResult, error) {
		if err := lockDatabase(ctx, tx, previous.DatabaseID); err != nil {
			return transitionResult{}, err
		}
		e, err := readExecution(ctx, tx, previous.DatabaseID, previous.ID, true)
		result := transitionResult{executionRow: e}
		if err != nil {
			return result, err
		}
		if e.Status != previous.Status || e.Attempt != previous.Attempt || !sameString(e.workerID, previous.workerID) {
			return result, nil
		}
		now := s.store.now()
		if staleOnly && e.heartbeatAt != nil && now.Sub(*e.heartbeatAt) < x.options.StaleAfter {
			return result, nil
		}
		var code, message *string
		if change.detail != nil {
			code, message = &change.detail.Code, &change.detail.Message
		}
		var finished *time.Time
		reconcile := e.reconcile
		if change.next == nil {
			finished, reconcile = &now, false
		}
		var version, seq *int64
		if change.history != nil {
			if err := finalizeHistory(ctx, tx, e, *change.history, now); err != nil {
				return result, err
			}
			version, seq = &change.history.Version, &change.history.Seq
		}
		_, err = tx.ExecContext(ctx, `UPDATE migration_executions SET status=$2, attempt=$3, next_attempt_at=$4,
			error_code=$5, error_message=$6, finished_at=$7, reconcile_requested=$8,
			version=COALESCE($9,version), history_seq=COALESCE($10,history_seq) WHERE id=$1`,
			e.ID, change.status, change.attempt, change.next, code, message, finished, reconcile, version, seq)
		if err != nil {
			return result, internal(err)
		}
		if change.next == nil {
			result.released, err = x.releaseHeld(ctx, tx, e.DatabaseID)
			if err != nil {
				return result, err
			}
		}
		result.executionRow, err = readExecution(ctx, tx, e.DatabaseID, e.ID, false)
		result.changed = err == nil
		return result, err
	})
	if err != nil {
		slog.WarnContext(ctx, "execution state could not be committed", "event", "execution.finalize_failed", "execution_id", previous.ID)
		return retry("retry", x.options.RetryBaseDelay)
	}
	e := result.executionRow
	if !result.changed {
		if !executionActive(e.Status) {
			return ack("duplicate")
		}
		return retry("retry", x.options.RetryBaseDelay)
	}
	if change.next != nil {
		x.log(ctx, e, "execution.retry_scheduled")
		telemetry.RecordRetry(ctx, e.Action, e.Mode)
		s.publish(ctx, e.DatabaseID, "execution.updated", e.ID, e.Status)
		return retry("retry", max(0, change.next.Sub(s.store.now())))
	}
	x.log(ctx, e, "execution.finished")
	s.recordExecution(ctx, "execution.finished", e.Execution)
	started := e.EnqueuedAt
	if e.StartedAt != nil {
		started = *e.StartedAt
	}
	telemetry.RecordFinished(ctx, e.Action, e.Mode, e.Status, e.FinishedAt.Sub(started))
	if change.history != nil {
		s.auditChange(ctx, contracts.Principal{UserID: e.Actor.ID, Username: e.Actor.Username},
			"history.recorded", e.DatabaseID, "execution", e.ID, map[string]any{"seq": *e.HistorySeq, "version": *e.Version})
		s.publish(ctx, e.DatabaseID, "migration.updated", e.MigrationID, migrationStatus(e.Action))
		s.publish(ctx, e.DatabaseID, "head.updated", e.DatabaseID, "")
	}
	for _, id := range result.released {
		s.publish(ctx, e.DatabaseID, "execution.updated", id, "queued")
	}
	return ack("done")
}

func sameString(a, b *string) bool {
	return a == nil && b == nil || a != nil && b != nil && *a == *b
}

func migrationStatus(action string) string {
	if action == "rollback" {
		return "rolled_back"
	}
	return "applied"
}

func finalizeHistory(ctx context.Context, tx *sql.Tx, e executionRow, h HistoryRecord, now time.Time) error {
	if err := mirrorHistory(ctx, tx, e.DatabaseID, h); err != nil {
		return err
	}
	if e.Action == "apply" {
		if _, err := tx.ExecContext(ctx, `UPDATE migrations SET status='superseded', updated_at=$3
			WHERE database_id=$1 AND status='rolled_back' AND version >= $2`, e.DatabaseID, h.Version, now); err != nil {
			return internal(err)
		}
	}
	_, err := tx.ExecContext(ctx, `UPDATE migrations SET status=$2, version=$3, updated_at=$4, updated_by=$5
		WHERE id=$1`, e.MigrationID, migrationStatus(e.Action), h.Version, now, e.Actor.ID)
	if err != nil {
		return internal(err)
	}
	return nil
}

func (x *Executor) releaseHeld(ctx context.Context, tx *sql.Tx, databaseID string) ([]string, error) {
	block, err := findBlock(ctx, tx, databaseID)
	if err != nil || block != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, `UPDATE migration_executions SET status='queued', held_reason=NULL
		WHERE database_id=$1 AND status='held' RETURNING id`, databaseID)
	if err != nil {
		return nil, internal(err)
	}
	ids, err := collectRows(rows, func(row scanner) (string, error) {
		var id string
		err := row.Scan(&id)
		return id, err
	})
	if err != nil {
		return nil, err
	}
	for _, id := range ids {
		if err := x.service.enqueuer.Enqueue(ctx, tx, contracts.JobMessage{ExecutionID: id, DatabaseID: databaseID}); err != nil {
			return nil, internal(err)
		}
	}
	return ids, nil
}
