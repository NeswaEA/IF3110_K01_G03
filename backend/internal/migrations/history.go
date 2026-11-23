package migrations

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/contracts"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/database"
)

type HistoryRecord struct {
	Seq               int64     `json:"seq"`
	ExecutionID       string    `json:"execution_id"`
	MigrationID       string    `json:"migration_id"`
	MigrationRevision int64     `json:"migration_revision"`
	Action            string    `json:"action"`
	Version           int64     `json:"version"`
	HeadAfter         int64     `json:"head_after"`
	RevertsExecution  *string   `json:"reverts_execution_id"`
	SQLExecuted       string    `json:"sql_executed"`
	SQLHash           string    `json:"sql_hash"`
	Mode              string    `json:"mode"`
	Batches           int       `json:"batches"`
	Rows              string    `json:"rows"`
	Actor             Actor     `json:"actor"`
	CommittedAt       time.Time `json:"committed_at"`
}

// AttemptRecord is the migration history of one attempt, kept whether or not it committed.
type AttemptRecord struct {
	ExecutionID       string    `json:"execution_id"`
	MigrationID       string    `json:"migration_id"`
	MigrationRevision int64     `json:"migration_revision"`
	Action            string    `json:"action"`
	Attempt           int       `json:"attempt"`
	Status            string    `json:"status"`
	RetryScheduled    bool      `json:"retry_scheduled"`
	SQLExecuted       *string   `json:"sql_executed"`
	Error             *RunError `json:"error"`
	RevertsExecution  *string   `json:"reverts_execution_id"`
	HistorySeq        *int64    `json:"history_seq"`
	Actor             Actor     `json:"actor"`
	RecordedAt        time.Time `json:"recorded_at"`
}

type TimelineEntry struct {
	Execution    Execution       `json:"execution"`
	Committed    *HistoryRecord  `json:"committed"`
	Attempts     []AttemptRecord `json:"attempts"`
	AttemptsOnly bool            `json:"attempts_only,omitempty"`
}

// recordAttempt appends the attempt that a transition ends. A scheduled retry
// still running is an unverified commit; one back in the queue has failed.
func recordAttempt(ctx context.Context, tx *sql.Tx, e executionRow, change executionTransition,
	sent, code, message *string, seq *int64, now time.Time) error {
	status := change.status
	if change.next != nil {
		status = "failed"
		if change.status == "running" {
			status = "outcome_unknown"
		}
	}
	attempt := change.attempt
	if change.history != nil && e.commitAttempt != nil && (attempt != *e.commitAttempt || sent == nil) {
		attempt = *e.commitAttempt
		sent = nil
		err := tx.QueryRowContext(ctx, `SELECT sql_executed FROM migration_attempts
			WHERE execution_id=$1 AND attempt=$2 AND sql_executed IS NOT NULL ORDER BY id DESC LIMIT 1`, e.ID, attempt).Scan(&sent)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return internal(err)
		}
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO migration_attempts (database_id, execution_id, migration_id,
		migration_revision, action, attempt, status, retry_scheduled, sql_executed, error_code, error_message,
		reverts_execution_id, history_seq, actor_id, actor_name, recorded_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)`,
		e.DatabaseID, e.ID, e.MigrationID, e.MigrationRevision, e.Action, attempt, status, change.next != nil,
		sent, code, message, e.RevertsExecution, seq, e.Actor.ID, e.Actor.Username, now)
	if err != nil {
		return internal(err)
	}
	return nil
}

func recordInterruptedAttempt(ctx context.Context, tx *sql.Tx, e executionRow, now time.Time) error {
	if e.Action == "dry_run" || e.Status != "running" || e.Attempt == 0 {
		return nil
	}
	var recorded bool
	err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM migration_attempts WHERE execution_id=$1 AND attempt=$2)`,
		e.ID, e.Attempt).Scan(&recorded)
	if err != nil {
		return internal(err)
	}
	if recorded {
		return nil
	}
	code, message := "EXECUTION_INTERRUPTED", "Worker berhenti sebelum hasil attempt dapat dicatat."
	return recordAttempt(ctx, tx, e, executionTransition{status: "running", attempt: e.Attempt, next: &now},
		e.sqlSent, &code, &message, nil, now)
}

func listAttempts(ctx context.Context, q contracts.Querier, executionID string) ([]AttemptRecord, error) {
	rows, err := q.QueryContext(ctx, `SELECT execution_id, migration_id, migration_revision, action, attempt, status,
		retry_scheduled, sql_executed, error_code, error_message, reverts_execution_id, history_seq, actor_id,
		actor_name, recorded_at FROM migration_attempts WHERE execution_id=$1 ORDER BY id`, executionID)
	if err != nil {
		return nil, internal(err)
	}
	return collectRows(rows, func(row scanner) (AttemptRecord, error) {
		var a AttemptRecord
		var code, message *string
		err := row.Scan(&a.ExecutionID, &a.MigrationID, &a.MigrationRevision, &a.Action, &a.Attempt, &a.Status,
			&a.RetryScheduled, &a.SQLExecuted, &code, &message, &a.RevertsExecution, &a.HistorySeq,
			&a.Actor.ID, &a.Actor.Username, &a.RecordedAt)
		if code != nil && message != nil {
			a.Error = &RunError{Code: *code, Message: *message}
		}
		return a, err
	})
}

type DatabaseHead struct {
	Version             int64   `json:"version"`
	MigrationID         *string `json:"migration_id"`
	HistorySeq          int64   `json:"history_seq"`
	Blocked             bool    `json:"blocked"`
	BlockingExecutionID *string `json:"blocking_execution_id"`
}

type executionBlock struct {
	id     *string
	reason string
}

func findBlock(ctx context.Context, q contracts.Querier, databaseID string) (*executionBlock, error) {
	var block executionBlock
	err := q.QueryRowContext(ctx, `SELECT id, reason FROM (
		SELECT id, 'outcome_unknown' AS reason, 0 AS priority, number FROM migration_executions
		WHERE database_id=$1 AND (status='outcome_unknown' OR (reconcile_requested AND status IN ('queued','running')))
		UNION ALL
		SELECT e.id, 'partial', 1, e.number FROM migrations m LEFT JOIN LATERAL (
			SELECT id, number FROM migration_executions WHERE migration_id=m.id AND status IN ('partial','partially_reverted')
			ORDER BY number DESC LIMIT 1
		) e ON true WHERE m.database_id=$1 AND m.status='partial'
	) blocks ORDER BY priority, number DESC NULLS LAST LIMIT 1`, databaseID).Scan(&block.id, &block.reason)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, internal(err)
	}
	return &block, nil
}

const historyColumns = `seq, execution_id, migration_id, migration_revision, action, version, head_after,
	reverts_execution_id, sql_executed, sql_hash, mode, batches, rows, actor_id, actor_name, committed_at`

func targetHistoryTable(driver database.Driver) string {
	if driver == database.SQLite {
		return "_platform_migration_history"
	}
	return "_platform.migration_history"
}

func scanHistory(row scanner, driver database.Driver, mirror bool) (HistoryRecord, error) {
	var h HistoryRecord
	var rows int64
	var hash []byte
	var sqliteTime string
	var hashDst any = &h.SQLHash
	var timeDst any = &h.CommittedAt
	if mirror {
		hashDst = &hash
	}
	if driver == database.SQLite {
		timeDst = &sqliteTime
	}
	if err := row.Scan(&h.Seq, &h.ExecutionID, &h.MigrationID, &h.MigrationRevision, &h.Action, &h.Version,
		&h.HeadAfter, &h.RevertsExecution, &h.SQLExecuted, hashDst, &h.Mode, &h.Batches, &rows,
		&h.Actor.ID, &h.Actor.Username, timeDst); err != nil {
		return h, err
	}
	if mirror {
		h.SQLHash = hashString(hash)
	}
	if driver == database.SQLite {
		var err error
		h.CommittedAt, err = time.Parse(time.RFC3339Nano, sqliteTime)
		if err != nil {
			return h, err
		}
	}
	h.Rows = strconv.FormatInt(rows, 10)
	return h, nil
}

func insertTargetHistory(ctx context.Context, conn executor, driver database.Driver, h HistoryRecord) error {
	var committedAt any = h.CommittedAt
	if driver == database.SQLite {
		committedAt = h.CommittedAt.Format(time.RFC3339Nano)
	}
	rows, err := strconv.ParseInt(h.Rows, 10, 64)
	if err != nil {
		return err
	}
	_, err = conn.ExecContext(ctx, `INSERT INTO `+targetHistoryTable(driver)+` (`+historyColumns+`)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)`,
		h.Seq, h.ExecutionID, h.MigrationID, h.MigrationRevision, h.Action, h.Version, h.HeadAfter,
		h.RevertsExecution, h.SQLExecuted, h.SQLHash, h.Mode, h.Batches, rows, h.Actor.ID, h.Actor.Username, committedAt)
	return err
}

func mirrorHistory(ctx context.Context, tx *sql.Tx, databaseID string, h HistoryRecord) error {
	hash, err := hex.DecodeString(strings.TrimPrefix(h.SQLHash, "sha256:"))
	if err != nil {
		return internal(err)
	}
	rows, err := strconv.ParseInt(h.Rows, 10, 64)
	if err != nil {
		return internal(err)
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO migration_history (database_id,`+historyColumns+`)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17)`, databaseID,
		h.Seq, h.ExecutionID, h.MigrationID, h.MigrationRevision, h.Action, h.Version, h.HeadAfter,
		h.RevertsExecution, h.SQLExecuted, hash, h.Mode, h.Batches, rows, h.Actor.ID, h.Actor.Username, h.CommittedAt)
	if err != nil {
		return internal(err)
	}
	return nil
}

func (s *Service) getHead(ctx context.Context, databaseID string) (DatabaseHead, error) {
	return inTx(ctx, s.store.DB, &snapshot, func(tx *sql.Tx) (DatabaseHead, error) {
		var head DatabaseHead
		err := tx.QueryRowContext(ctx, `SELECT latest.head_after, latest.seq,
			(SELECT migration_id FROM migration_history
			 WHERE database_id=$1 AND version=latest.head_after AND action IN ('apply','forward')
			 ORDER BY seq DESC LIMIT 1)
			FROM migration_history latest WHERE latest.database_id=$1 ORDER BY latest.seq DESC LIMIT 1`,
			databaseID).Scan(&head.Version, &head.HistorySeq, &head.MigrationID)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return head, internal(err)
		}
		block, err := findBlock(ctx, tx, databaseID)
		if block != nil {
			head.Blocked, head.BlockingExecutionID = true, block.id
		}
		return head, err
	})
}

func (s *Service) listHistory(ctx context.Context, databaseID string, hide bool, limit, offset int) ([]TimelineEntry, error) {
	return inTx(ctx, s.store.DB, &snapshot, func(tx *sql.Tx) ([]TimelineEntry, error) {
		rows, err := tx.QueryContext(ctx, `WITH rollbacks AS (
			SELECT h.execution_id, reverted.chain_root_id FROM migration_history h
			JOIN migration_executions reverted ON reverted.id=h.reverts_execution_id
			WHERE h.database_id=$1 AND h.action='rollback' AND $2
		) SELECT `+executionColumns+`, hidden.attempts_only`+executionFrom+`
			CROSS JOIN LATERAL (SELECT EXISTS (SELECT 1 FROM rollbacks r WHERE r.execution_id=e.id OR
				(r.chain_root_id=e.chain_root_id AND e.status IN ('succeeded','partial','partially_reverted'))) AS attempts_only) hidden
			WHERE e.database_id=$1 AND e.action<>'dry_run'
			AND (NOT hidden.attempts_only OR EXISTS (SELECT 1 FROM migration_attempts a
				WHERE a.execution_id=e.id AND a.status IN ('failed','outcome_unknown')))
			ORDER BY e.number DESC LIMIT $3 OFFSET $4`, databaseID, hide, limit, offset)
		if err != nil {
			return nil, internal(err)
		}
		items, err := collectRows(rows, func(row scanner) (TimelineEntry, error) {
			var item TimelineEntry
			e, err := scanExecutionWith(row, &item.AttemptsOnly)
			item.Execution = e.Execution
			return item, err
		})
		if err != nil {
			return nil, err
		}
		for i := range items {
			item := &items[i]
			e := item.Execution
			if item.Attempts, err = listAttempts(ctx, tx, e.ID); err != nil {
				return nil, err
			}
			if item.AttemptsOnly {
				item.Attempts = slices.DeleteFunc(item.Attempts, func(a AttemptRecord) bool {
					return a.Status != "failed" && a.Status != "outcome_unknown"
				})
			} else if e.Status == "succeeded" {
				h, err := scanHistory(tx.QueryRowContext(ctx, `SELECT `+historyColumns+`
					FROM migration_history WHERE database_id=$1 AND execution_id=$2`, databaseID, e.ID), database.Postgres, true)
				switch {
				case err == nil:
					item.Committed = &h
				case !errors.Is(err, sql.ErrNoRows):
					return nil, internal(err)
				}
			}
		}
		return items, nil
	})
}
