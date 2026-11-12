package migrations

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"database/sql/driver"
	"errors"
	"log/slog"
	"strconv"
	"time"

	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/contracts"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/database"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/database/postgres"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/telemetry"
	sqlite3 "github.com/mattn/go-sqlite3"
)

type attemptFailure struct {
	detail               *RunError
	transient, uncertain bool
}

func failed(code, message string) *attemptFailure {
	return &attemptFailure{detail: runFailure(code, message)}
}

func targetFailure(err error) *attemptFailure {
	switch database.SQLState(err) {
	case "57014":
		return failed("STATEMENT_TIMEOUT", "Waktu maksimum statement terlampaui.")
	case "55P03":
		return &attemptFailure{detail: runFailure("LOCK_TIMEOUT", "Waktu maksimum lock statement terlampaui."), transient: true}
	}
	switch {
	case errors.Is(err, database.ErrTargetNotReady):
		return failed("TARGET_NOT_READY", "Database target belum siap untuk migrasi.")
	case errors.Is(err, database.ErrLedgerIncompatible):
		return failed("LEDGER_INCOMPATIBLE", "Riwayat migrasi target tidak kompatibel.")
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, database.ErrTargetTimeout):
		return &attemptFailure{detail: runFailure("TARGET_TIMEOUT", "Waktu akses database target terlampaui."), transient: true}
	case database.IsTransient(err):
		return &attemptFailure{detail: runFailure("TARGET_UNAVAILABLE", "Database target tidak dapat diakses."), transient: true}
	}
	var engine sqlite3.Error
	if errors.As(err, &engine) {
		switch engine.Code {
		case sqlite3.ErrBusy, sqlite3.ErrLocked:
			return &attemptFailure{detail: runFailure("LOCK_TIMEOUT", "Database target masih terkunci."), transient: true}
		case sqlite3.ErrIoErr, sqlite3.ErrCantOpen:
			return &attemptFailure{detail: runFailure("TARGET_UNAVAILABLE", "Database target tidak dapat diakses."), transient: true}
		}
	}
	return failed("SQL_EXECUTION_FAILED", "Database menolak operasi SQL; transaksi dibatalkan.")
}

func metadataFailure() *attemptFailure {
	return &attemptFailure{detail: runFailure("INTERNAL_ERROR", "Metadata eksekusi belum dapat diperiksa."), transient: true}
}

func (x *Executor) checkInputs(ctx context.Context, q contracts.Querier, e executionRow) (migrationRow, *attemptFailure) {
	s := x.service
	level, err := s.access.Effective(ctx, e.Actor.ID, e.DatabaseID)
	if err != nil {
		return migrationRow{}, metadataFailure()
	}
	if level != contracts.AccessEdit {
		return migrationRow{}, failed("ACCESS_REVOKED", "Pengguna tidak lagi memiliki akses edit.")
	}
	if e.ChangeRequestID == nil || e.Mode != "normal" || (e.Action != "apply" && e.Action != "rollback" && e.Action != "forward") {
		return migrationRow{}, failed("INTERNAL_ERROR", "Jenis eksekusi tidak didukung worker ini.")
	}
	subject, err := s.store.LoadSubject(ctx, q, *e.ChangeRequestID)
	if err != nil {
		return migrationRow{}, metadataFailure()
	}
	state, err := s.review.Evaluate(ctx, q, subject)
	if err != nil {
		return migrationRow{}, metadataFailure()
	}
	if state.Status != "approved" || subject.Status != "executed" {
		return migrationRow{}, failed("APPROVAL_REVOKED", "Approval wajib berubah sebelum SQL dijalankan.")
	}
	m, err := readMigration(ctx, q, e.DatabaseID, e.MigrationID)
	if err != nil {
		return m, metadataFailure()
	}
	if subject.DatabaseID != e.DatabaseID || subject.MigrationID != e.MigrationID || subject.Action != e.Action ||
		subject.Revision != e.MigrationRevision || m.Current.Number != e.MigrationRevision ||
		!bytes.Equal(subject.ContentHash, e.contentHash) || !bytes.Equal(m.contentHash, e.contentHash) {
		return m, failed("REVISION_CHANGED", "Revisi migrasi berubah sebelum SQL dijalankan.")
	}
	return m, nil
}

type normalTarget struct {
	conn   *sql.Conn
	target database.TargetHandle
}

func (t normalTarget) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	start := time.Now()
	result, err := t.conn.ExecContext(ctx, query, args...)
	rows := int64(-1)
	if result != nil {
		if affected, countErr := result.RowsAffected(); countErr == nil {
			rows = affected
		}
	}
	t.observe(ctx, query, args, start, rows, err)
	return result, err
}

func (t normalTarget) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	start := time.Now()
	rows, err := t.conn.QueryContext(ctx, query, args...)
	t.observe(ctx, query, args, start, -1, err)
	return rows, err
}

func (t normalTarget) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	start := time.Now()
	row := t.conn.QueryRowContext(ctx, query, args...)
	t.observe(ctx, query, args, start, -1, row.Err())
	return row
}

func (t normalTarget) observe(ctx context.Context, query string, args []any, start time.Time, rows int64, err error) {
	if t.target.Driver == database.SQLite {
		telemetry.ObserveSQL(ctx, telemetry.SQLEvent{Pool: "target", DatabaseID: t.target.DatabaseID,
			Query: query, Args: args, Start: start, Rows: rows, Err: err})
	}
}

func (t normalTarget) exec(ctx context.Context, query string, args ...any) error {
	_, err := t.ExecContext(ctx, query, args...)
	return err
}

func (t normalTarget) rollback(ctx context.Context) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if t.target.Driver == database.SQLite {
		var active bool
		err := t.conn.Raw(func(raw any) error {
			conn, ok := raw.(*sqlite3.SQLiteConn)
			if !ok {
				return errors.New("unexpected SQLite driver")
			}
			active = !conn.AutoCommit()
			return nil
		})
		if err == nil && !active {
			return
		}
	}
	if err := t.exec(ctx, "ROLLBACK"); err != nil {
		t.discard()
	}
}

func (t normalTarget) discard() {
	_ = t.conn.Raw(func(any) error { return driver.ErrBadConn })
}

func (t normalTarget) prepare(ctx context.Context, statementTimeout, lockTimeout time.Duration) error {
	if t.target.Driver == database.SQLite {
		return t.exec(ctx, "BEGIN IMMEDIATE")
	}
	if err := t.exec(ctx, "BEGIN"); err != nil {
		return err
	}
	return t.exec(ctx, `SELECT set_config('statement_timeout',$1,true), set_config('lock_timeout',$2,true)`,
		strconv.FormatInt(max(1, statementTimeout.Milliseconds()), 10),
		strconv.FormatInt(max(1, lockTimeout.Milliseconds()), 10))
}

func (t normalTarget) checkPosition(ctx context.Context, e executionRow, m Migration) (HistoryRecord, *attemptFailure) {
	table := targetHistoryTable(t.target.Driver)
	var seq, head int64
	err := t.QueryRowContext(ctx, `SELECT seq,head_after FROM `+table+` ORDER BY seq DESC LIMIT 1`).Scan(&seq, &head)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return HistoryRecord{}, targetFailure(err)
	}
	h := HistoryRecord{Seq: seq + 1, ExecutionID: e.ID, MigrationID: e.MigrationID, MigrationRevision: e.MigrationRevision,
		Action: e.Action, Mode: e.Mode, Actor: e.Actor, Rows: "0", SQLExecuted: m.Current.UpSQL}
	positionChanged := failed("POSITION_CHANGED", "Posisi migrasi berubah sebelum eksekusi.")
	switch e.Action {
	case "apply":
		if m.Status != "pending" {
			return h, positionChanged
		}
		for _, finding := range m.Current.Lint {
			if finding.Severity == "error" {
				return h, failed("REVISION_CHANGED", "Revisi tidak lolos lint.")
			}
		}
		h.Version, h.HeadAfter = head+1, head+1
	case "rollback":
		if m.Status != "applied" || m.Version == nil || *m.Version != head || m.Current.DownSQL == nil || !m.Locked {
			return h, positionChanged
		}
		var previous, migrationID string
		err := t.QueryRowContext(ctx, `SELECT execution_id,migration_id FROM `+table+`
			WHERE version=$1 AND action IN ('apply','forward') ORDER BY seq DESC LIMIT 1`, head).Scan(&previous, &migrationID)
		if errors.Is(err, sql.ErrNoRows) {
			return h, positionChanged
		}
		if err != nil {
			return h, targetFailure(err)
		}
		if e.RevertsExecution == nil || previous != *e.RevertsExecution || migrationID != m.ID {
			return h, positionChanged
		}
		h.Version, h.HeadAfter, h.RevertsExecution, h.SQLExecuted = head, head-1, e.RevertsExecution, *m.Current.DownSQL
	case "forward":
		if m.Status != "rolled_back" || m.Version == nil || *m.Version != head+1 || !m.Locked {
			return h, positionChanged
		}
		h.Version, h.HeadAfter = *m.Version, *m.Version
	}
	sum := sha256.Sum256([]byte(h.SQLExecuted))
	h.SQLHash = hashString(sum[:])
	return h, nil
}

func (t normalTarget) userSQL(ctx context.Context, statements []string) error {
	ctx = telemetry.WithUserSQL(ctx)
	for _, statement := range statements {
		if err := ctx.Err(); err != nil {
			return err
		}
		if t.target.Driver == database.Postgres {
			if err := t.exec(ctx, statement); err != nil {
				return err
			}
			continue
		}
		start := time.Now()
		err := sqliteUserSQL(ctx, t.conn, statement)
		telemetry.ObserveSQL(ctx, telemetry.SQLEvent{Pool: "target", DatabaseID: t.target.DatabaseID,
			Query: statement, Start: start, Rows: -1, Err: err})
		if err != nil {
			return err
		}
	}
	return nil
}

func normalStatements(driver database.Driver, query string) ([]string, error) {
	if driver == database.Postgres {
		return postgresStatements(query)
	}
	var statements []string
	for _, statement := range sqliteStatements(query) {
		statements = append(statements, statement.query)
	}
	if len(statements) == 0 {
		return nil, unsupported()
	}
	return statements, nil
}

func (x *Executor) runNormal(ctx context.Context, target database.TargetHandle, lock database.MigrationLock, e executionRow) (*HistoryRecord, *attemptFailure) {
	t := normalTarget{conn: lock.Conn, target: target}
	work := ctx
	ctx, revalidate := telemetry.Tracer().Start(ctx, "execution.revalidate")
	defer revalidate.End()
	m, failure := x.checkInputs(ctx, x.service.store.DB, e)
	if failure != nil {
		return nil, failure
	}
	if !target.MigrationReady {
		return nil, failed("TARGET_NOT_READY", "Database target belum siap untuk migrasi.")
	}
	if err := t.prepare(ctx, x.options.StatementTimeout, x.options.LockTimeout); err != nil {
		t.rollback(ctx)
		return nil, targetFailure(err)
	}
	committed := false
	defer func() {
		if !committed {
			t.rollback(ctx)
		}
	}()
	h, failure := t.checkPosition(ctx, e, m.Migration)
	if failure != nil {
		return nil, failure
	}
	// A diff's source fingerprint describes the up SQL's starting schema.
	if m.SourceFingerprint != nil && e.Direction == "up" {
		snapshot, err := x.inspector.InspectWith(ctx, target, t)
		if err != nil {
			return nil, targetFailure(err)
		}
		fingerprint, err := database.Fingerprint(snapshot)
		if err != nil {
			return nil, failed("INTERNAL_ERROR", "Fingerprint schema tidak dapat dihitung.")
		}
		if fingerprint != *m.SourceFingerprint {
			return nil, failed("SCHEMA_CONFLICT", "Schema target berubah sejak migrasi dibuat.")
		}
	}
	statements, err := normalStatements(target.Driver, h.SQLExecuted)
	if err != nil {
		return nil, failed("SQL_EXECUTION_FAILED", "SQL melanggar batas migrasi transaksional.")
	}
	if target.Driver == database.Postgres {
		if target.EditorRole == "" {
			return nil, failed("TARGET_NOT_READY", "Role editor database target belum tersedia.")
		}
		if err := t.exec(ctx, "SET LOCAL ROLE "+postgres.QuoteIdent(target.EditorRole)); err != nil {
			return nil, targetFailure(err)
		}
	}
	if failure := x.freezeRevision(ctx, e); failure != nil {
		return nil, failure
	}
	x.log(ctx, e, "execution.revalidated")
	revalidate.End()
	ctx = work

	userCtx := ctx
	if target.Driver == database.SQLite {
		timed, cancel := context.WithTimeout(ctx, x.options.StatementTimeout)
		defer cancel()
		userCtx = timed
	}
	if err := t.userSQL(userCtx, statements); err != nil {
		if errors.Is(userCtx.Err(), context.DeadlineExceeded) && ctx.Err() == nil {
			return nil, failed("STATEMENT_TIMEOUT", "Waktu maksimum statement terlampaui.")
		}
		return nil, targetFailure(err)
	}
	if target.Driver == database.Postgres {
		if err := t.exec(ctx, "RESET ROLE"); err != nil {
			return nil, targetFailure(err)
		}
	}
	h.CommittedAt = x.service.store.now()
	if err := insertTargetHistory(ctx, t, target.Driver, h); err != nil {
		return nil, targetFailure(err)
	}
	select {
	case <-lock.Lost:
		return nil, &attemptFailure{detail: runFailure("TARGET_UNAVAILABLE", "Koneksi lock terputus."), uncertain: true}
	default:
	}
	if err := ctx.Err(); err != nil {
		return nil, targetFailure(err)
	}
	commitCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if err := t.exec(commitCtx, "COMMIT"); err != nil {
		failure := targetFailure(err)
		if !definitiveCommitFailure(err) {
			t.discard()
			committed = true // The connection was discarded. The next holder checks the evidence.
			failure.uncertain = true
		}
		return nil, failure
	}
	committed = true
	return &h, nil
}

func definitiveCommitFailure(err error) bool {
	state := database.SQLState(err)
	if len(state) >= 2 && (state[:2] == "23" || state == "40001" || state == "40P01") {
		return true
	}
	var sqliteErr sqlite3.Error
	return errors.As(err, &sqliteErr) && (sqliteErr.Code == sqlite3.ErrConstraint || sqliteErr.Code == sqlite3.ErrBusy || sqliteErr.Code == sqlite3.ErrLocked)
}

func (x *Executor) freezeRevision(ctx context.Context, e executionRow) *attemptFailure {
	s := x.service
	failure, err := inTx(ctx, s.store.DB, nil, func(tx *sql.Tx) (*attemptFailure, error) {
		if err := lockMigration(ctx, tx, e.DatabaseID, e.MigrationID); err != nil {
			return nil, err
		}
		if _, failure := x.checkInputs(ctx, tx, e); failure != nil {
			return failure, nil
		}
		_, err := tx.ExecContext(ctx, `UPDATE migrations SET locked_at=$2, locked_revision=$3
			WHERE id=$1 AND locked_at IS NULL`, e.MigrationID, s.store.now(), e.MigrationRevision)
		return nil, err
	})
	if err != nil {
		return metadataFailure()
	}
	return failure
}

func (x *Executor) readEvidence(ctx context.Context, target database.TargetHandle, conn *sql.Conn, e executionRow) (*HistoryRecord, error) {
	t := normalTarget{conn: conn, target: target}
	h, err := scanHistory(t.QueryRowContext(ctx, `SELECT `+historyColumns+`
		FROM `+targetHistoryTable(target.Driver)+` WHERE execution_id=$1`, e.ID), target.Driver, false)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		slog.InfoContext(ctx, "commit evidence checked", "event", "recovery.evidence_checked", "execution_id", e.ID, "found", false)
		return nil, nil
	case err != nil:
		return nil, err
	}
	sum := sha256.Sum256([]byte(h.SQLExecuted))
	if h.MigrationID != e.MigrationID || h.MigrationRevision != e.MigrationRevision || h.Action != e.Action ||
		h.Mode != e.Mode || h.Actor.ID != e.Actor.ID || !sameString(h.RevertsExecution, e.RevertsExecution) ||
		h.SQLHash != hashString(sum[:]) {
		return nil, errors.New("invalid target commit evidence")
	}
	slog.InfoContext(ctx, "commit evidence checked", "event", "recovery.evidence_checked", "execution_id", e.ID, "found", true)
	return &h, nil
}
