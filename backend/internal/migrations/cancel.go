package migrations

import (
	"context"
	"database/sql"

	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/contracts"
)

func (s *Service) cancelExecution(ctx context.Context, actor contracts.Principal, databaseID, id string) (Execution, error) {
	e, err := inTx(ctx, s.store.DB, nil, func(tx *sql.Tx) (executionRow, error) {
		if err := lockDatabase(ctx, tx, databaseID); err != nil {
			return executionRow{}, err
		}
		e, err := readExecution(ctx, tx, databaseID, id, true)
		if err != nil {
			return e, err
		}
		// A queued reconcile still represents an unresolved commit.
		if e.reconcile || (e.Status != "queued" && e.Status != "held") {
			return e, problem("EXECUTION_NOT_ACTIVE", "Hanya eksekusi queued atau held yang dapat dibatalkan.")
		}
		// A retry can be queued after committing one or more target checkpoints.
		// Reopening the request would let a new chain apply those batches again.
		if e.Batch != nil && e.Batch.BatchesCommitted > 0 {
			return e, problem("EXECUTION_NOT_ACTIVE", "Batch sudah menyimpan perubahan. Gunakan Stop ketika eksekusi kembali berjalan.")
		}
		now := s.store.now()
		if e.ChangeRequestID != nil {
			if err := lockMigration(ctx, tx, databaseID, e.MigrationID); err != nil {
				return e, err
			}
			m, err := readMigration(ctx, tx, databaseID, e.MigrationID)
			if err != nil {
				return e, err
			}
			if m.openRequestID != nil {
				return e, problem("REQUEST_EXISTS", "Batalkan change request yang terbuka sebelum membatalkan eksekusi ini.")
			}
			if _, err := tx.ExecContext(ctx, `UPDATE change_requests SET status='approved', closed_at=NULL, updated_at=$2
				WHERE id=$1 AND status='executed'`, *e.ChangeRequestID, now); err != nil {
				return e, internal(err)
			}
		}
		if _, err := tx.ExecContext(ctx, `UPDATE migration_executions SET status='cancelled', finished_at=$2,
			next_attempt_at=NULL, held_reason=NULL, error_code=NULL, error_message=NULL WHERE id=$1`, id, now); err != nil {
			return e, internal(err)
		}
		block, err := findBlock(ctx, tx, databaseID)
		if err != nil {
			return e, err
		}
		if block == nil {
			if err := s.wakeNext(ctx, tx, databaseID); err != nil {
				return e, err
			}
		}
		return readExecution(ctx, tx, databaseID, id, false)
	})
	if err != nil {
		return Execution{}, err
	}
	s.auditChange(ctx, actor, "execution.cancelled", databaseID, "execution", id, map[string]any{
		"migration_id": e.MigrationID, "revision": e.MigrationRevision, "action": e.Action, "status": e.Status,
	})
	s.publish(ctx, databaseID, "execution.updated", id, e.Status)
	if e.ChangeRequestID != nil {
		s.publish(ctx, databaseID, "request.updated", *e.ChangeRequestID, "approved")
	}
	return e.Execution, nil
}
