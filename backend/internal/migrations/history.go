package migrations

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
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

type TimelineEntry struct {
	Execution Execution      `json:"execution"`
	Committed *HistoryRecord `json:"committed"`
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
		) `+executionSelect+` WHERE e.database_id=$1 AND e.action<>'dry_run'
			AND NOT EXISTS (SELECT 1 FROM rollbacks r WHERE r.execution_id=e.id OR
				(r.chain_root_id=e.chain_root_id AND e.status IN ('succeeded','partial','partially_reverted')))
			ORDER BY e.number DESC LIMIT $3 OFFSET $4`, databaseID, hide, limit, offset)
		if err != nil {
			return nil, internal(err)
		}
		executions, err := collectRows(rows, scanExecution)
		if err != nil {
			return nil, err
		}
		items := make([]TimelineEntry, 0, len(executions))
		for _, e := range executions {
			item := TimelineEntry{Execution: e.Execution}
			if e.Status == "succeeded" {
				h, err := scanHistory(tx.QueryRowContext(ctx, `SELECT `+historyColumns+`
					FROM migration_history WHERE database_id=$1 AND execution_id=$2`, databaseID, e.ID), database.Postgres, true)
				switch {
				case err == nil:
					item.Committed = &h
				case !errors.Is(err, sql.ErrNoRows):
					return nil, internal(err)
				}
			}
			items = append(items, item)
		}
		return items, nil
	})
}
