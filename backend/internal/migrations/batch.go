package migrations

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/contracts"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/database"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/database/postgres"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/telemetry"
)

var errInvalidBatch = errors.New("hasil atau konfigurasi batch tidak valid")

type batchRange struct {
	from, to       int64
	rows           int
	keyFingerprint []byte
}

type checkpoint struct {
	batchRange
	number int
	at     time.Time
}

func (x *Executor) validateBatchKey(ctx context.Context, target normalTarget, b BatchConfig) error {
	snapshot, err := x.inspector.InspectWith(ctx, target.target, target)
	if err != nil {
		return err
	}
	for _, schema := range snapshot.Schemas {
		if schema.Name != b.Schema {
			continue
		}
		for _, table := range schema.Tables {
			if table.Name != b.Table {
				continue
			}
			for _, col := range table.Columns {
				if col.Name != b.KeyColumn || col.Nullable || (col.DataType != "smallint" && col.DataType != "integer" && col.DataType != "bigint") {
					continue
				}
				for _, c := range table.Constraints {
					if (c.Kind == database.PrimaryKey || c.Kind == database.Unique) && len(c.Columns) == 1 && c.Columns[0] == col.Name {
						return nil
					}
				}
				for _, i := range table.Indexes {
					if i.Unique && len(i.Columns) == 1 && i.Columns[0] == col.Name && !strings.Contains(strings.ToUpper(i.Definition), " WHERE ") {
						return nil
					}
				}
			}
		}
	}
	return errInvalidBatch
}

func batchRole(ctx context.Context, target normalTarget) error {
	if target.target.EditorRole == "" {
		return database.ErrTargetNotReady
	}
	return target.exec(ctx, "SET LOCAL ROLE "+postgres.QuoteIdent(target.target.EditorRole))
}

func readScope(ctx context.Context, t normalTarget, b BatchConfig) (batchRange, error) {
	if err := batchRole(ctx, t); err != nil {
		return batchRange{}, err
	}
	key := postgres.QuoteIdent(b.KeyColumn)
	table := postgres.QuoteIdent(b.Schema) + "." + postgres.QuoteIdent(b.Table)
	var lo, hi *int64
	if err := t.QueryRowContext(ctx, "SELECT min("+key+"),max("+key+") FROM "+table).Scan(&lo, &hi); err != nil {
		return batchRange{}, err
	}
	if err := t.exec(ctx, "RESET ROLE"); err != nil {
		return batchRange{}, err
	}
	if lo == nil {
		return batchRange{}, nil
	}
	if *lo == math.MinInt64 {
		return batchRange{}, errInvalidBatch
	}
	return batchRange{from: *lo - 1, to: *hi}, nil
}

func runBatchSQL(ctx context.Context, t normalTarget, query string, from, to int64, size int) (batchRange, error) {
	if err := batchRole(ctx, t); err != nil {
		return batchRange{}, err
	}
	if err := ctx.Err(); err != nil {
		return batchRange{}, err
	}
	rows, err := t.QueryContext(telemetry.WithUserSQL(ctx), query, from, to, size)
	if rows != nil {
		defer rows.Close()
	}
	if t.onUserSQL != nil {
		if saveErr := t.onUserSQL(ctx, query); saveErr != nil {
			return batchRange{}, saveErr
		}
	}
	if err != nil {
		return batchRange{}, err
	}
	b := batchRange{from: from, to: from}
	keys := make(map[int64]struct{})
	for rows.Next() {
		var key int64
		if err := rows.Scan(&key); err != nil {
			return b, errInvalidBatch
		}
		_, duplicate := keys[key]
		if duplicate || key <= from || key > to || b.rows >= size {
			return b, errInvalidBatch
		}
		keys[key] = struct{}{}
		b.rows++
		b.to = max(b.to, key)
	}
	if err := rows.Err(); err != nil {
		return b, err
	}
	rows.Close()
	// RETURNING has no fixed order. Hash the sorted, fixed-width keys.
	hash := sha256.New()
	var encoded [8]byte
	for _, key := range slices.Sorted(maps.Keys(keys)) {
		binary.BigEndian.PutUint64(encoded[:], uint64(key))
		_, _ = hash.Write(encoded[:])
	}
	b.keyFingerprint = hash.Sum(nil)
	return b, t.exec(ctx, "RESET ROLE")
}

func (x *Executor) dryRunBatch(ctx context.Context, t normalTarget, r Revision) DryRunReport {
	if r.Batch == nil {
		return dryRunError(ctx, errInvalidBatch)
	}
	if err := x.validateBatchKey(ctx, t, *r.Batch); err != nil {
		return dryRunError(ctx, err)
	}
	scope, err := readScope(ctx, t, *r.Batch)
	if err != nil {
		return dryRunError(ctx, err)
	}
	out := DryRunReport{Status: "passed"}
	if scope.from == scope.to {
		out.Message = new("Tidak ada baris untuk dry-run batch.")
		return out
	}
	up, err := runBatchSQL(ctx, t, r.UpSQL, scope.from, scope.to, r.Batch.BatchSize)
	if err != nil {
		return dryRunError(ctx, err)
	}
	if r.DownSQL != nil {
		out.Reversible = new(true)
		if up.rows > 0 {
			down, err := runBatchSQL(ctx, t, *r.DownSQL, up.from, up.to, up.rows)
			if err != nil {
				result := dryRunError(ctx, err)
				result.Reversible = new(false)
				return result
			}
			out.Reversible = new(bytes.Equal(down.keyFingerprint, up.keyFingerprint))
		}
	}
	return out
}

func batchFailure(err error) *attemptFailure {
	if errors.Is(err, errAttemptProgress) {
		return metadataFailure()
	}
	if errors.Is(err, errInvalidBatch) {
		return failed("INVALID_BATCH_RESULT", "Key atau hasil RETURNING batch tidak valid.")
	}
	return targetFailure(err)
}

func (x *Executor) handleBatch(ctx, work context.Context, target database.TargetHandle, lock database.MigrationLock, g executionGate) contracts.JobResult {
	e := g.executionRow
	sent := false
	t := normalTarget{conn: lock.Conn, target: target, onUserSQL: func(ctx context.Context, statement string) error {
		if sent {
			return nil
		}
		if err := x.saveAttemptProgress(ctx, e, &statement, false); err != nil {
			return err
		}
		sent = true
		return nil
	}}
	h, progress, failure := x.runBatch(work, t, lock, g)
	if failure == nil {
		return x.complete(ctx, e, "succeeded", nil, h, lock.Release)
	}
	if failure.outcomeUnknown {
		return x.complete(ctx, e, "outcome_unknown", failure.detail, nil, lock.Release)
	}
	if failure.uncertain || work.Err() != nil {
		return x.deferEvidence(ctx, e, failure.detail)
	}
	if failure.transient && e.Attempt < e.MaxAttempts && !e.reconcile {
		return x.schedule(ctx, e, "queued", e.Attempt, failure.detail, false)
	}
	status := "failed"
	if progress {
		status = "partial"
		if e.Direction == "down" {
			status = "partially_reverted"
		}
	}
	if failure.transient {
		failure.detail = runFailure("RETRY_EXHAUSTED", "Batas percobaan tercapai.")
	}
	return x.complete(ctx, e, status, failure.detail, nil, lock.Release)
}

func (x *Executor) runBatch(ctx context.Context, t normalTarget, lock database.MigrationLock, g executionGate) (*HistoryRecord, bool, *attemptFailure) {
	e := g.executionRow
	s := x.service
	root := e
	if e.Action == "resume" {
		var err error
		root, err = readExecution(ctx, s.store.DB, e.DatabaseID, e.ChainRootID, false)
		if err != nil {
			return nil, false, metadataFailure()
		}
	}
	if t.target.Driver != database.Postgres || e.Batch == nil {
		return nil, false, failed("TARGET_NOT_READY", "Batch membutuhkan PostgreSQL.")
	}
	// Evidence precedes revalidation on retry, including across worker restarts.
	n, err := x.syncBatchMirror(ctx, t, e)
	if err != nil {
		final := e.Attempt >= e.MaxAttempts || e.reconcile
		return nil, false, &attemptFailure{detail: runFailure("TARGET_UNAVAILABLE", "Checkpoint batch belum dapat diperiksa."), uncertain: !final, outcomeUnknown: final}
	}
	var batches int
	var rows int64
	var last *int64
	if err := t.QueryRowContext(ctx, `SELECT count(*),COALESCE(sum(rows),0),
		CASE WHEN $2='up' THEN max(cursor_to) ELSE min(cursor_from) END
		FROM _platform.migration_checkpoints WHERE chain_root_id=$1 AND direction=$2`,
		e.ChainRootID, e.Direction).Scan(&batches, &rows, &last); err != nil {
		if e.Attempt >= e.MaxAttempts || e.reconcile {
			result := failed("TARGET_UNAVAILABLE", "Checkpoint batch belum dapat diperiksa.")
			result.outcomeUnknown = true
			return nil, false, result
		}
		return nil, false, &attemptFailure{detail: runFailure("TARGET_UNAVAILABLE", "Checkpoint batch belum dapat diperiksa."), uncertain: true}
	}
	progress := batches > 0
	if g.recover {
		probe := e
		probe.Action = root.Action
		proof, err := x.readEvidence(ctx, t.target, t.conn, probe)
		if err != nil {
			result := failed("TARGET_UNAVAILABLE", "Bukti commit batch belum dapat diperiksa.")
			result.outcomeUnknown = e.Attempt >= e.MaxAttempts || e.reconcile
			result.uncertain = !result.outcomeUnknown
			return nil, progress, result
		}
		if proof != nil {
			return proof, progress, nil
		}
		if e.reconcile || g.exhausted {
			code, message := "RETRY_EXHAUSTED", "Batas percobaan tercapai."
			if e.reconcile {
				code, message = "EXECUTION_INTERRUPTED", "Eksekusi batch terputus."
			}
			return nil, progress, failed(code, message)
		}
	}
	m, failure := x.checkInputs(ctx, s.store.DB, e)
	if failure != nil {
		return nil, progress, failure
	}
	if !t.target.MigrationReady || m.Current.Batch == nil {
		return nil, progress, failed("TARGET_NOT_READY", "Database target belum siap untuk batch.")
	}
	if err := x.ensureCheckpointFingerprint(ctx, t); err != nil {
		return nil, progress, batchFailure(err)
	}
	spec := *m.Current.Batch
	scope, h, original, failure := x.batchPosition(ctx, t, e, root, m, progress)
	if failure != nil {
		return nil, progress, failure
	}
	if err := x.saveBatchScope(ctx, e, scope); err != nil {
		return nil, progress, metadataFailure()
	}
	cursor := scope.from
	if e.Direction == "down" {
		cursor = scope.to
	}
	if last != nil {
		cursor = *last
	}
	if failure := x.freezeRevision(ctx, e); failure != nil {
		return nil, progress, failure
	}
	x.log(ctx, e, "execution.revalidated")
	lastEvent := time.Time{}
	for {
		if failure := x.batchStop(ctx, e); failure != nil {
			return nil, progress, failure
		}
		r, failure := x.commitBatch(ctx, t, lock, e, &h, original, cursor, scope.to, n+1, batches, rows)
		if failure != nil {
			telemetry.RecordBatch(ctx, e.Direction, "failed", 0)
			if !failure.uncertain {
				outcome := e
				outcome.Status, outcome.Error = "failed", failure.detail
				x.log(ctx, outcome, "batch.failed", "batch", n+1)
			}
			return nil, progress, failure
		}
		if r == nil {
			h.Batches, h.Rows = batches, strconv.FormatInt(rows, 10)
			return &h, progress, nil
		}
		progress, n, batches, rows = true, r.number, batches+1, rows+int64(r.rows)
		cursor = r.to
		if e.Direction == "down" {
			cursor = r.from
		}
		if err := x.mirrorCheckpoint(ctx, e, *r); err != nil {
			return nil, progress, &attemptFailure{detail: runFailure("INTERNAL_ERROR", "Checkpoint sudah tersimpan pada target."), uncertain: true}
		}
		now := s.store.now()
		if now.Sub(lastEvent) >= time.Second {
			s.publish(ctx, e.DatabaseID, "execution.updated", e.ID, "running")
			lastEvent = now
		}
		if spec.PauseMS > 0 {
			until := time.Now().Add(time.Duration(spec.PauseMS) * time.Millisecond)
			for time.Now().Before(until) {
				timer := time.NewTimer(min(time.Until(until), 100*time.Millisecond))
				select {
				case <-timer.C:
				case <-ctx.Done():
					timer.Stop()
				}
				if failure := x.batchStop(ctx, e); failure != nil {
					return nil, progress, failure
				}
			}
		}
	}
}

func (x *Executor) ensureCheckpointFingerprint(ctx context.Context, t normalTarget) error {
	var exists bool
	err := t.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM pg_attribute
		WHERE attrelid='_platform.migration_checkpoints'::regclass AND attname='key_fingerprint' AND NOT attisdropped)`).Scan(&exists)
	if err != nil || exists {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, x.options.LockTimeout)
	defer cancel()
	return t.exec(ctx, addCheckpointFingerprint)
}

func (x *Executor) batchStop(ctx context.Context, e executionRow) *attemptFailure {
	if ctx.Err() != nil {
		return failed("EXECUTION_INTERRUPTED", "Eksekusi batch terputus.")
	}
	var stop bool
	if err := x.service.store.DB.QueryRowContext(ctx, `SELECT stop_requested_at IS NOT NULL FROM migration_executions
		WHERE id=$1`, e.ID).Scan(&stop); err != nil {
		return metadataFailure()
	}
	if stop {
		return failed("STOPPED", "Eksekusi dihentikan setelah batch terakhir.")
	}
	return nil
}

func (x *Executor) batchPosition(ctx context.Context, t normalTarget, e, root executionRow, m migrationRow, progress bool) (batchRange, HistoryRecord, string, *attemptFailure) {
	var scope batchRange
	var h HistoryRecord
	if err := t.exec(ctx, "BEGIN READ ONLY"); err != nil {
		return scope, h, "", batchFailure(err)
	}
	defer t.rollback(ctx)
	if err := t.configureTimeouts(ctx, x.options.BatchTimeout, x.options.LockTimeout); err != nil {
		return scope, h, "", batchFailure(err)
	}
	if err := x.validateBatchKey(ctx, t, *m.Current.Batch); err != nil {
		return scope, h, "", batchFailure(err)
	}
	original := ""
	var err error
	probe := e
	probe.Action = root.Action
	state := m.Migration
	if e.Action == "resume" && m.Status == "partial" {
		switch root.Action {
		case "apply":
			state.Status = "pending"
		case "forward":
			state.Status = "rolled_back"
		case "rollback":
			if m.Version != nil {
				state.Status = "applied"
			}
		}
	}
	if e.Direction == "down" {
		if e.RevertsExecution == nil || m.Current.DownSQL == nil {
			return scope, h, "", failed("POSITION_CHANGED", "Target rollback batch berubah.")
		}
		previous, err := readExecution(ctx, x.service.store.DB, e.DatabaseID, *e.RevertsExecution, false)
		if err != nil {
			return scope, h, "", metadataFailure()
		}
		original = previous.ChainRootID
		var missingKeys bool
		if err := t.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM _platform.migration_checkpoints
			WHERE chain_root_id=$1 AND direction='up' AND key_fingerprint IS NULL)`, original).Scan(&missingKeys); err != nil {
			return scope, h, "", batchFailure(err)
		}
		if missingKeys {
			return scope, h, "", failed("LEDGER_INCOMPATIBLE", "Checkpoint lama tidak menyimpan bukti key untuk rollback yang aman.")
		}
		if previous.Status == "partial" && previous.Direction == "up" && m.Status == "partial" {
			h, err = x.partialRevertHistory(ctx, t, e, m)
			if err != nil {
				return scope, h, "", batchFailure(err)
			}
		}
	}
	if h.Action == "" {
		var failure *attemptFailure
		h, failure = t.checkPosition(ctx, probe, state)
		if failure != nil {
			return scope, h, "", failure
		}
	}
	if e.Direction == "up" && m.SourceFingerprint != nil && !progress {
		snap, err := x.inspector.InspectWith(ctx, t.target, t)
		if err != nil {
			return scope, h, "", batchFailure(err)
		}
		if snap.Fingerprint != *m.SourceFingerprint {
			return scope, h, "", failed("SCHEMA_CONFLICT", "Schema target berubah sejak migrasi dibuat.")
		}
	}
	switch {
	case e.Batch.ScopeFrom != nil && e.Batch.ScopeTo != nil:
		scope.from, err = strconv.ParseInt(*e.Batch.ScopeFrom, 10, 64)
		if err == nil {
			scope.to, err = strconv.ParseInt(*e.Batch.ScopeTo, 10, 64)
		}
	case original != "":
		err = t.QueryRowContext(ctx, `SELECT COALESCE(min(cursor_from),0),COALESCE(max(cursor_to),0)
			FROM _platform.migration_checkpoints WHERE chain_root_id=$1 AND direction='up'`, original).Scan(&scope.from, &scope.to)
	default:
		scope, err = readScope(ctx, t, *m.Current.Batch)
	}
	if err != nil {
		return scope, h, "", batchFailure(err)
	}
	return scope, h, original, nil
}

func (x *Executor) partialRevertHistory(ctx context.Context, t normalTarget, e executionRow, m migrationRow) (HistoryRecord, error) {
	var seq, head int64
	err := t.QueryRowContext(ctx, `SELECT seq,head_after FROM _platform.migration_history ORDER BY seq DESC LIMIT 1`).Scan(&seq, &head)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return HistoryRecord{}, err
	}
	sum := sha256.Sum256([]byte(*m.Current.DownSQL))
	return HistoryRecord{Seq: seq + 1, ExecutionID: e.ID, MigrationID: e.MigrationID, MigrationRevision: e.MigrationRevision,
		Action: "rollback", Version: head + 1, HeadAfter: head, RevertsExecution: e.RevertsExecution, SQLExecuted: *m.Current.DownSQL,
		SQLHash: hashString(sum[:]), Mode: e.Mode, Actor: e.Actor, Rows: "0"}, nil
}

func (x *Executor) commitBatch(ctx context.Context, t normalTarget, lock database.MigrationLock, e executionRow, h *HistoryRecord,
	original string, cursor, end int64, number, batches int, total int64) (*checkpoint, *attemptFailure) {
	ctx, span := telemetry.Tracer().Start(ctx, "migration.batch")
	defer span.End()
	ctx = telemetry.WithLogAttrs(ctx, slog.Int("batch", number))
	if err := t.prepare(ctx, x.options.BatchTimeout, x.options.LockTimeout); err != nil {
		t.rollback(ctx)
		return nil, batchFailure(err)
	}
	committed := false
	defer func() {
		if !committed {
			t.rollback(ctx)
		}
	}()
	r := batchRange{}
	var err error
	if original == "" && cursor < end {
		r, err = runBatchSQL(ctx, t, h.SQLExecuted, cursor, end, e.Batch.BatchSize)
	} else if original != "" {
		var source batchRange
		err = t.QueryRowContext(ctx, `SELECT cursor_from,cursor_to,rows,key_fingerprint FROM _platform.migration_checkpoints
			WHERE chain_root_id=$1 AND direction='up' AND cursor_to<=$2 ORDER BY cursor_to DESC LIMIT 1`,
			original, cursor).Scan(&source.from, &source.to, &source.rows, &source.keyFingerprint)
		if errors.Is(err, sql.ErrNoRows) {
			err = nil
		} else if err == nil {
			r, err = runBatchSQL(ctx, t, h.SQLExecuted, source.from, source.to, source.rows)
			if err == nil && !bytes.Equal(r.keyFingerprint, source.keyFingerprint) {
				err = errInvalidBatch
			}
		}
	}
	if err != nil {
		return nil, batchFailure(err)
	}
	now := x.service.store.now()
	var cp *checkpoint
	if r.rows == 0 {
		h.Batches, h.Rows, h.CommittedAt = batches, strconv.FormatInt(total, 10), now
		err = insertTargetHistory(ctx, t, t.target.Driver, *h)
		if err == nil {
			err = x.saveAttemptProgress(ctx, e, nil, true)
		}
	} else {
		cp = &checkpoint{batchRange: r, number: number, at: now}
		err = t.exec(ctx, `INSERT INTO _platform.migration_checkpoints
			(execution_id,batch_no,chain_root_id,direction,cursor_from,cursor_to,rows,committed_at,key_fingerprint)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`, e.ID, number, e.ChainRootID, e.Direction, r.from, r.to, r.rows, now, r.keyFingerprint)
	}
	if err != nil {
		return nil, batchFailure(err)
	}
	select {
	case <-lock.Lost:
		return nil, &attemptFailure{detail: runFailure("TARGET_UNAVAILABLE", "Koneksi lock terputus."), uncertain: true}
	default:
	}
	if err := ctx.Err(); err != nil {
		return nil, batchFailure(err)
	}
	commit, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if err := t.exec(commit, "COMMIT"); err != nil {
		f := batchFailure(err)
		if !definitiveCommitFailure(err) {
			t.discard()
			committed = true
			f.uncertain = true
		}
		return nil, f
	}
	committed = true
	return cp, nil
}

func (x *Executor) saveBatchScope(ctx context.Context, e executionRow, scope batchRange) error {
	cursor := scope.from
	if e.Direction == "down" {
		cursor = scope.to
	}
	_, err := x.service.store.DB.ExecContext(ctx, `UPDATE migration_executions SET
		scope_from=COALESCE(scope_from,$2),scope_to=COALESCE(scope_to,$3),cursor=COALESCE(cursor,$4)
		WHERE id=$1 AND status='running'`, e.ID, scope.from, scope.to, cursor)
	return err
}

func (x *Executor) syncBatchMirror(ctx context.Context, t normalTarget, e executionRow) (int, error) {
	var latest int
	err := x.service.store.DB.QueryRowContext(ctx, `SELECT batches_committed FROM migration_executions WHERE id=$1`, e.ID).Scan(&latest)
	if err != nil {
		return 0, err
	}
	for {
		var cp checkpoint
		err = t.QueryRowContext(ctx, `SELECT batch_no,cursor_from,cursor_to,rows,committed_at FROM _platform.migration_checkpoints
			WHERE execution_id=$1 AND batch_no>$2 ORDER BY batch_no LIMIT 1`, e.ID, latest).Scan(&cp.number, &cp.from, &cp.to, &cp.rows, &cp.at)
		if errors.Is(err, sql.ErrNoRows) {
			return latest, nil
		}
		if err != nil {
			return latest, err
		}
		if err = x.mirrorCheckpoint(ctx, e, cp); err != nil {
			return latest, err
		}
		latest = cp.number
	}
}

func (x *Executor) mirrorCheckpoint(ctx context.Context, e executionRow, cp checkpoint) error {
	_, err := inTx(ctx, x.service.store.DB, nil, func(tx *sql.Tx) (struct{}, error) {
		if _, err := tx.ExecContext(ctx, `INSERT INTO migration_batches(execution_id,batch_no,direction,cursor_from,cursor_to,rows,committed_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7) ON CONFLICT(execution_id,batch_no) DO NOTHING`,
			e.ID, cp.number, e.Direction, cp.from, cp.to, cp.rows, cp.at); err != nil {
			return struct{}{}, err
		}
		cursor := cp.to
		if e.Direction == "down" {
			cursor = cp.from
		}
		res, err := tx.ExecContext(ctx, `UPDATE migration_executions SET batches_committed=$2, rows_committed=rows_committed+$3,
			cursor=$4, heartbeat_at=$5 WHERE id=$1 AND batches_committed=$2-1 AND status='running'`, e.ID, cp.number, cp.rows, cursor, x.service.store.now())
		if err != nil {
			return struct{}{}, err
		}
		n, err := res.RowsAffected()
		if err == nil && n != 1 {
			err = fmt.Errorf("checkpoint %d does not follow metadata progress", cp.number)
		}
		return struct{}{}, err
	})
	if err != nil {
		return err
	}
	// Both a normal commit and checkpoint recovery reach this point. The
	// metadata cursor prevents recovery from reporting the same batch again.
	telemetry.RecordBatch(ctx, e.Direction, "committed", int64(cp.rows))
	e.Status, e.Error = "committed", nil
	x.log(ctx, e, "batch.committed", "batch", cp.number, "rows", cp.rows)
	return nil
}
