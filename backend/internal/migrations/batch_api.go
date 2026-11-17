package migrations

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/contracts"
	"github.com/google/uuid"
)

type BatchRecord struct {
	Number      int       `json:"batch_no"`
	Direction   string    `json:"direction"`
	CursorFrom  string    `json:"cursor_from"`
	CursorTo    string    `json:"cursor_to"`
	Rows        int       `json:"rows"`
	CommittedAt time.Time `json:"committed_at"`
}

func (s *Service) listBatches(ctx context.Context, databaseID, id string, limit, offset int) ([]BatchRecord, error) {
	if _, err := readExecution(ctx, s.store.DB, databaseID, id, false); err != nil {
		return nil, err
	}
	rows, err := s.store.DB.QueryContext(ctx, `SELECT batch_no,direction,cursor_from::text,cursor_to::text,rows,committed_at
		FROM migration_batches WHERE execution_id=$1 ORDER BY batch_no DESC LIMIT $2 OFFSET $3`, id, limit, offset)
	if err != nil {
		return nil, internal(err)
	}
	return collectRows(rows, func(row scanner) (BatchRecord, error) {
		var b BatchRecord
		err := row.Scan(&b.Number, &b.Direction, &b.CursorFrom, &b.CursorTo, &b.Rows, &b.CommittedAt)
		return b, err
	})
}

func (s *Service) stopExecution(ctx context.Context, actor contracts.Principal, databaseID, id string) (Execution, error) {
	e, err := inTx(ctx, s.store.DB, nil, func(tx *sql.Tx) (executionRow, error) {
		e, err := readExecution(ctx, tx, databaseID, id, true)
		if err != nil {
			return e, err
		}
		if e.Mode != "batch" || e.Status != "running" || e.Action == "dry_run" || e.reconcile {
			return e, problem("EXECUTION_NOT_ACTIVE", "Stop hanya tersedia untuk eksekusi batch yang sedang berjalan.")
		}
		if _, err := tx.ExecContext(ctx, `UPDATE migration_executions SET stop_requested_at=COALESCE(stop_requested_at,$2)
			WHERE id=$1`, id, s.store.now()); err != nil {
			return e, internal(err)
		}
		return readExecution(ctx, tx, databaseID, id, false)
	})
	if err != nil {
		return Execution{}, err
	}
	s.auditChange(ctx, actor, "execution.stop_requested", databaseID, "execution", id, nil)
	s.publish(ctx, databaseID, "execution.updated", id, e.Status)
	return e.Execution, nil
}

func (s *Service) resumeExecution(ctx context.Context, actor contracts.Principal, databaseID, id string, batchSize *int) (Execution, bool, error) {
	if batchSize != nil && (*batchSize < 1 || *batchSize > s.batchSizeMax) {
		return Execution{}, false, invalidField("batch_size", "Ukuran batch tidak valid.")
	}
	result, err := inTx(ctx, s.store.DB, nil, func(tx *sql.Tx) (enqueueResult, error) {
		if err := lockDatabase(ctx, tx, databaseID); err != nil {
			return enqueueResult{}, err
		}
		e, err := readExecution(ctx, tx, databaseID, id, false)
		if err != nil {
			return enqueueResult{}, err
		}
		m, err := readMigration(ctx, tx, databaseID, e.MigrationID)
		if err != nil {
			return enqueueResult{}, err
		}
		existing, err := scanExecution(tx.QueryRowContext(ctx, executionSelect+` WHERE e.resumes_execution_id=$1
			AND e.status IN ('queued','held','running')`, id))
		if err == nil {
			return enqueueResult{executionRow: existing, reused: true}, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return enqueueResult{}, internal(err)
		}
		block, err := findBlock(ctx, tx, databaseID)
		if err != nil {
			return enqueueResult{}, err
		}
		if m.Status != "partial" || e.Batch == nil || !clearsPartialBlock(block, &id) ||
			(e.Status != "partial" && e.Status != "partially_reverted") {
			return enqueueResult{}, problem("RESUME_NOT_ALLOWED", "Lanjutkan eksekusi partial terakhir.")
		}
		var resolving bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM migration_executions
			WHERE database_id=$1 AND migration_id=$2 AND status IN ('queued','held','running') AND action<>'dry_run')`,
			databaseID, e.MigrationID).Scan(&resolving); err != nil {
			return enqueueResult{}, internal(err)
		}
		if resolving {
			return enqueueResult{}, problem("RESUME_NOT_ALLOWED", "Pemulihan batch lain sudah dijadwalkan.")
		}
		size := e.Batch.BatchSize
		if batchSize != nil {
			size = *batchSize
		}
		newID := uuid.Must(uuid.NewV7()).String()
		if _, err := tx.ExecContext(ctx, `INSERT INTO migration_executions
			(id,database_id,number,migration_id,action,direction,mode,migration_revision,content_hash,status,max_attempts,
			reverts_execution_id,resumes_execution_id,chain_root_id,batch_size,scope_from,scope_to,cursor,enqueued_by,enqueued_by_name,enqueued_at)
			SELECT $2,database_id,(SELECT MAX(number)+1 FROM migration_executions WHERE database_id=$3),
			migration_id,'resume',direction,mode,migration_revision,content_hash,'queued',$4,
			reverts_execution_id,id,chain_root_id,$5,scope_from,scope_to,cursor,$6,$7,$8
			FROM migration_executions WHERE id=$1`, id, newID, databaseID, s.maxAttempts, size, actor.UserID, actor.Username, s.store.now()); err != nil {
			return enqueueResult{}, internal(err)
		}
		if err := s.enqueuer.Enqueue(ctx, tx, contracts.JobMessage{ExecutionID: newID, DatabaseID: databaseID}); err != nil {
			return enqueueResult{}, internal(err)
		}
		next, err := readExecution(ctx, tx, databaseID, newID, false)
		return enqueueResult{executionRow: next}, err
	})
	if err == nil && !result.reused {
		s.recordExecution(ctx, "execution.enqueued", result.Execution)
		if s.notifyJobs != nil {
			s.notifyJobs()
		}
	}
	return result.Execution, result.reused, err
}

func clearsPartialBlock(block *executionBlock, id *string) bool {
	return block != nil && block.reason == "partial" && block.id != nil && sameString(block.id, id)
}
