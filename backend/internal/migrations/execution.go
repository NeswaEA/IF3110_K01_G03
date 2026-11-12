package migrations

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"strconv"
	"time"

	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/contracts"
	"github.com/google/uuid"
)

type RunError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type ExecutionBatch struct {
	BatchSize        int     `json:"batch_size"`
	ScopeFrom        *string `json:"scope_from"`
	ScopeTo          *string `json:"scope_to"`
	Cursor           *string `json:"cursor"`
	BatchesCommitted int     `json:"batches_committed"`
	RowsCommitted    string  `json:"rows_committed"`
	EstimatedBatches *int    `json:"estimated_batches"`
	StopRequested    bool    `json:"stop_requested"`
}

type Execution struct {
	ID                string          `json:"id"`
	Number            int64           `json:"number"`
	DatabaseID        string          `json:"database_id"`
	MigrationID       string          `json:"migration_id"`
	MigrationTitle    string          `json:"migration_title"`
	ChangeRequestID   *string         `json:"change_request_id"`
	Action            string          `json:"action"`
	Direction         string          `json:"direction"`
	Mode              string          `json:"mode"`
	Status            string          `json:"status"`
	Attempt           int             `json:"attempt"`
	MaxAttempts       int             `json:"max_attempts"`
	MigrationRevision int64           `json:"migration_revision"`
	Version           *int64          `json:"version"`
	RevertsExecution  *string         `json:"reverts_execution_id"`
	ResumesExecution  *string         `json:"resumes_execution_id"`
	ChainRootID       string          `json:"chain_root_id"`
	Batch             *ExecutionBatch `json:"batch"`
	HeldReason        *string         `json:"held_reason"`
	Actor             Actor           `json:"actor"`
	EnqueuedAt        time.Time       `json:"enqueued_at"`
	StartedAt         *time.Time      `json:"started_at"`
	FinishedAt        *time.Time      `json:"finished_at"`
	Error             *RunError       `json:"error"`
	TraceID           *string         `json:"trace_id"`
	HistorySeq        *int64          `json:"history_seq"`
	DryRun            json.RawMessage `json:"dry_run"`
}

type executionRow struct {
	Execution
	contentHash   []byte
	nextAttemptAt *time.Time
	lockRequested *time.Time
	heartbeatAt   *time.Time
	workerID      *string
	reconcile     bool
}

const executionSelect = `SELECT e.id, e.number, e.database_id, e.migration_id, m.title, e.change_request_id, e.action, e.direction,
	e.mode, e.status, e.attempt, e.max_attempts, e.migration_revision, e.version, e.reverts_execution_id,
	e.resumes_execution_id, e.chain_root_id, e.held_reason, e.enqueued_by, e.enqueued_by_name, e.enqueued_at,
	e.started_at, e.finished_at, e.error_code, e.error_message, e.trace_id, e.history_seq, e.result,
	e.content_hash, e.next_attempt_at, e.lock_requested_at, e.heartbeat_at, e.worker_id, e.reconcile_requested,
	e.batch_size, e.scope_from, e.scope_to, e.cursor, e.batches_committed, e.rows_committed, e.stop_requested_at IS NOT NULL
	FROM migration_executions e JOIN migrations m ON m.id=e.migration_id`

func scanExecution(row scanner) (executionRow, error) {
	var e executionRow
	var code, message *string
	var result []byte
	var size, from, to, cursor *int64
	var rows int64
	var batch ExecutionBatch
	if err := row.Scan(&e.ID, &e.Number, &e.DatabaseID, &e.MigrationID, &e.MigrationTitle, &e.ChangeRequestID,
		&e.Action, &e.Direction, &e.Mode, &e.Status, &e.Attempt, &e.MaxAttempts, &e.MigrationRevision,
		&e.Version, &e.RevertsExecution, &e.ResumesExecution, &e.ChainRootID, &e.HeldReason, &e.Actor.ID,
		&e.Actor.Username, &e.EnqueuedAt, &e.StartedAt, &e.FinishedAt, &code, &message, &e.TraceID, &e.HistorySeq, &result,
		&e.contentHash, &e.nextAttemptAt, &e.lockRequested, &e.heartbeatAt, &e.workerID, &e.reconcile,
		&size, &from, &to, &cursor, &batch.BatchesCommitted, &rows, &batch.StopRequested); err != nil {
		return e, err
	}
	if code != nil {
		e.Error = &RunError{Code: *code}
		if message != nil {
			e.Error.Message = *message
		}
	}
	e.DryRun = result
	if size != nil {
		batch.BatchSize = int(*size)
		batch.ScopeFrom, batch.ScopeTo, batch.Cursor = decimal(from), decimal(to), decimal(cursor)
		batch.RowsCommitted = strconv.FormatInt(rows, 10)
		e.Batch = &batch
	}
	return e, nil
}

func decimal(value *int64) *string {
	if value == nil {
		return nil
	}
	return new(strconv.FormatInt(*value, 10))
}

func readExecution(ctx context.Context, q contracts.Querier, databaseID, id string, lock bool) (executionRow, error) {
	query := executionSelect + ` WHERE e.database_id=$1 AND e.id=$2`
	if lock {
		query += ` FOR UPDATE OF e`
	}
	e, err := scanExecution(q.QueryRowContext(ctx, query, databaseID, id))
	if errors.Is(err, sql.ErrNoRows) {
		return e, problem("NOT_FOUND", "Eksekusi tidak ditemukan.")
	}
	if err != nil {
		return e, internal(err)
	}
	return e, nil
}

func (s *Service) listExecutions(ctx context.Context, databaseID, status, action, migrationID string, limit, offset int) ([]Execution, error) {
	rows, err := s.store.DB.QueryContext(ctx, executionSelect+`
		WHERE e.database_id=$1 AND ($2='' OR e.status=$2) AND ($3='' OR e.action=$3)
		AND (NULLIF($4,'') IS NULL OR e.migration_id=NULLIF($4,'')::uuid)
		ORDER BY e.number DESC LIMIT $5 OFFSET $6`, databaseID, status, action, migrationID, limit, offset)
	if err != nil {
		return nil, internal(err)
	}
	return collectRows(rows, func(row scanner) (Execution, error) {
		e, err := scanExecution(row)
		return e.Execution, err
	})
}

func lockDatabase(ctx context.Context, tx *sql.Tx, databaseID string) error {
	var id string
	err := tx.QueryRowContext(ctx, `SELECT id FROM database_connections WHERE id=$1 FOR UPDATE`, databaseID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return problem("NOT_FOUND", "Database tidak ditemukan.")
	}
	if err != nil {
		return internal(err)
	}
	return nil
}

func executionActive(status string) bool {
	return status == "queued" || status == "held" || status == "running"
}

type enqueueResult struct {
	executionRow
	reused, reviewReset bool
}

func (s *Service) executeRequest(ctx context.Context, actor contracts.Principal, databaseID, requestID string) (Execution, bool, error) {
	result, err := inTx(ctx, s.store.DB, nil, func(tx *sql.Tx) (enqueueResult, error) {
		// Edits take the migration lock before the request lock.
		if err := lockDatabase(ctx, tx, databaseID); err != nil {
			return enqueueResult{}, err
		}
		var migrationID string
		err := tx.QueryRowContext(ctx, `SELECT migration_id FROM change_requests WHERE database_id=$1 AND id=$2`,
			databaseID, requestID).Scan(&migrationID)
		if errors.Is(err, sql.ErrNoRows) {
			return enqueueResult{}, requestNotFound()
		}
		if err != nil {
			return enqueueResult{}, internal(err)
		}
		if err := lockMigration(ctx, tx, databaseID, migrationID); err != nil {
			return enqueueResult{}, err
		}
		subject, err := s.store.LoadSubject(ctx, tx, requestID)
		if err != nil {
			return enqueueResult{}, err
		}
		if subject.Status == "executed" {
			e, err := findRequestExecution(ctx, tx, databaseID, requestID)
			if err != nil {
				return enqueueResult{}, err
			}
			if executionActive(e.Status) {
				return enqueueResult{executionRow: e, reused: true}, nil
			}
		}
		if subject.Status == "rejected" || subject.Status == "cancelled" || subject.Status == "executed" {
			return enqueueResult{}, requestNotOpen()
		}
		state, err := s.review.Evaluate(ctx, tx, subject)
		if err != nil {
			return enqueueResult{}, err
		}
		if state.Status != "approved" || subject.Status != "approved" {
			if subject.Status == "approved" {
				if err := s.store.ApplyReviewState(ctx, tx, requestID, "pending_review"); err != nil {
					return enqueueResult{}, err
				}
				return enqueueResult{reviewReset: true}, nil
			}
			return enqueueResult{}, problem("APPROVAL_REQUIRED", "Approval semua grup diperlukan.")
		}
		e, err := s.enqueueApproved(ctx, tx, actor, subject)
		return enqueueResult{executionRow: e}, err
	})
	if err != nil {
		return Execution{}, false, err
	}
	if result.reviewReset {
		s.publish(ctx, databaseID, "request.updated", requestID, "pending_review")
		return Execution{}, false, problem("APPROVAL_REQUIRED", "Approval semua grup diperlukan.")
	}
	if !result.reused {
		slog.InfoContext(ctx, "execution accepted", "event", "execution.enqueued", "execution_id", result.ID,
			"job_id", result.ID, "database_id", databaseID, "change_request_id", requestID,
			"migration_id", result.MigrationID, "migration_revision", result.MigrationRevision, "status", result.Status)
		s.recordExecution(ctx, "execution.enqueued", result.Execution)
		s.publish(ctx, databaseID, "request.updated", requestID, "executed")
		if result.Status == "queued" && s.notifyJobs != nil {
			s.notifyJobs()
		}
	}
	return result.Execution, result.reused, nil
}

func findRequestExecution(ctx context.Context, q contracts.Querier, databaseID, requestID string) (executionRow, error) {
	e, err := scanExecution(q.QueryRowContext(ctx, executionSelect+`
		WHERE e.database_id=$1 AND e.change_request_id=$2 ORDER BY e.number DESC LIMIT 1`, databaseID, requestID))
	if err != nil {
		return e, internal(err)
	}
	return e, nil
}

func (s *Service) enqueueApproved(ctx context.Context, tx *sql.Tx, actor contracts.Principal, subject contracts.ReviewSubject) (executionRow, error) {
	databaseID, migrationID := subject.DatabaseID, subject.MigrationID
	m, err := readMigration(ctx, tx, databaseID, migrationID)
	if err != nil {
		return executionRow{}, err
	}
	if m.Current.Mode != "normal" {
		return executionRow{}, problem("UNSUPPORTED_MODE", "Eksekusi mode batch belum tersedia.")
	}
	if subject.Revision != m.Current.Number || hashString(subject.ContentHash) != m.Current.ContentHash {
		return executionRow{}, problem("REVISION_CONFLICT", "Revisi change request sudah berubah.")
	}
	head, err := headVersion(ctx, tx, databaseID)
	if err != nil {
		return executionRow{}, internal(err)
	}
	if err := validateRequestAction(ctx, tx, m.Migration, head, requestInput{Action: subject.Action}); err != nil {
		return executionRow{}, err
	}
	direction := "up"
	var reverts *string
	if subject.Action == "rollback" {
		direction = "down"
		var id string
		err := tx.QueryRowContext(ctx, `SELECT execution_id FROM migration_history
			WHERE database_id=$1 AND migration_id=$2 AND version=$3 AND action IN ('apply','forward')
			ORDER BY seq DESC LIMIT 1`, databaseID, migrationID, head).Scan(&id)
		if err != nil {
			return executionRow{}, internal(err)
		}
		reverts = &id
	}
	status := "queued"
	var heldReason *string
	block, err := findBlock(ctx, tx, databaseID)
	if err != nil {
		return executionRow{}, err
	}
	if block != nil {
		status, heldReason = "held", &block.reason
	}
	id := uuid.Must(uuid.NewV7()).String()
	now := s.store.now()
	_, err = tx.ExecContext(ctx, `INSERT INTO migration_executions
		(id,database_id,number,migration_id,change_request_id,action,direction,mode,migration_revision,content_hash,
		 status,max_attempts,reverts_execution_id,chain_root_id,held_reason,enqueued_by,enqueued_by_name,enqueued_at)
		VALUES ($1,$2,(SELECT COALESCE(max(number),0)+1 FROM migration_executions WHERE database_id=$2),
		 $3,$4,$5,$6,'normal',$7,$8,$9,$10,$11,$1,$12,$13,$14,$15)`,
		id, databaseID, migrationID, subject.RequestID, subject.Action, direction, subject.Revision, subject.ContentHash,
		status, s.maxAttempts, reverts, heldReason, actor.UserID, actor.Username, now)
	if err != nil {
		return executionRow{}, internal(err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE change_requests SET status='executed', updated_at=$2, closed_at=$2 WHERE id=$1`, subject.RequestID, now); err != nil {
		return executionRow{}, internal(err)
	}
	if status == "queued" {
		if err := s.enqueuer.Enqueue(ctx, tx, contracts.JobMessage{ExecutionID: id, DatabaseID: databaseID}); err != nil {
			return executionRow{}, internal(err)
		}
	}
	return readExecution(ctx, tx, databaseID, id, false)
}

func (s *Service) reconcileExecution(ctx context.Context, actor contracts.Principal, databaseID, id string) (Execution, error) {
	e, err := inTx(ctx, s.store.DB, nil, func(tx *sql.Tx) (executionRow, error) {
		if err := lockDatabase(ctx, tx, databaseID); err != nil {
			return executionRow{}, err
		}
		e, err := readExecution(ctx, tx, databaseID, id, true)
		if err != nil {
			return e, err
		}
		if e.Status != "outcome_unknown" {
			return e, problem("EXECUTION_NOT_ACTIVE", "Reconcile hanya tersedia untuk hasil commit yang belum pasti.")
		}
		if _, err := tx.ExecContext(ctx, `UPDATE migration_executions SET status='queued', reconcile_requested=true,
			next_attempt_at=NULL, finished_at=NULL, lock_requested_at=NULL WHERE id=$1`, id); err != nil {
			return e, internal(err)
		}
		if err := s.enqueuer.Enqueue(ctx, tx, contracts.JobMessage{ExecutionID: id, DatabaseID: databaseID}); err != nil {
			return e, internal(err)
		}
		return readExecution(ctx, tx, databaseID, id, false)
	})
	if err != nil {
		return Execution{}, err
	}
	s.auditChange(ctx, actor, "execution.reconcile_requested", databaseID, "execution", id, nil)
	s.publish(ctx, databaseID, "execution.updated", id, e.Status)
	if s.notifyJobs != nil {
		s.notifyJobs()
	}
	return e.Execution, nil
}

func (s *Service) recordExecution(ctx context.Context, event string, e Execution) {
	s.auditChange(ctx, contracts.Principal{UserID: e.Actor.ID, Username: e.Actor.Username}, event, e.DatabaseID, "execution", e.ID,
		map[string]any{"migration_id": e.MigrationID, "revision": e.MigrationRevision, "action": e.Action, "status": e.Status})
	s.publish(ctx, e.DatabaseID, "execution.updated", e.ID, e.Status)
}
