package migrations

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/contracts"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/database"
	sqlite3 "github.com/mattn/go-sqlite3"
)

func (s *Service) Execute(ctx context.Context, p contracts.Principal, in ExecuteInput) (MigrationRun, error) {
	if err := s.authorize(ctx, p, in.DatabaseID, contracts.EditDatabase); err != nil {
		return MigrationRun{}, err
	}
	if !uuidPattern.MatchString(in.DraftID) || !uuidPattern.MatchString(in.IdempotencyKey) || in.Revision < 1 || in.Revision > maxInteger || in.ExpectedVersion < 0 || in.ExpectedVersion >= maxInteger {
		return MigrationRun{}, problem("INVALID_REQUEST", "Draft, revision, versi, atau Idempotency-Key tidak valid.")
	}
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	hash := inputHash(in)
	if r, found, err := findKey(ctx, s.store.DB, in.DatabaseID, in.IdempotencyKey); err != nil || found {
		if err == nil {
			r, err = compareKey(r, hash)
		}
		return r, err
	}
	release, ok := s.lease(in.DatabaseID)
	if !ok {
		return MigrationRun{}, problem("RUN_IN_PROGRESS", "Eksekusi database masih berlangsung.")
	}
	defer release()
	r, d, fresh, err := s.createRun(ctx, p, in, hash)
	if err != nil || !fresh {
		return r, err
	}
	if err := s.access.Require(ctx, p, contracts.EditDatabase, in.DatabaseID); err != nil {
		return s.failRun(r, err)
	}
	h, done, err := s.provider.Acquire(ctx, in.DatabaseID)
	if err != nil {
		return s.failRun(r, err)
	}
	defer done()
	if !h.MigrationReady {
		return s.failRun(r, problem("TARGET_NOT_READY", "Uji koneksi untuk menyiapkan ledger terlebih dahulu."))
	}
	var statements []string
	if h.Driver == database.Postgres {
		statements, err = postgresStatements(r.SQL)
		if err != nil {
			return s.failRun(r, err)
		}
	}
	if err := s.access.Require(ctx, p, contracts.EditDatabase, in.DatabaseID); err != nil {
		return s.failRun(r, err)
	}
	tx, err := beginTarget(ctx, h)
	if err != nil {
		return s.failRun(r, err)
	}
	defer tx.close()
	err = s.apply(ctx, tx, h, r, d, statements)
	if err != nil {
		if rollbackErr := tx.rollback(); rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
			return s.unknown(r), nil
		}
		return s.failRun(r, err)
	}
	if err := tx.commit(ctx); err != nil {
		var pg interface{ SQLState() string }
		knownRollback := errors.As(err, &pg) && len(pg.SQLState()) >= 2 && pg.SQLState()[:2] == "23"
		if h.Driver == database.SQLite {
			active := false
			rawErr := tx.conn.Raw(func(c any) error { active = !c.(*sqlite3.SQLiteConn).AutoCommit(); return nil })
			knownRollback = rawErr == nil && active && tx.rollback() == nil
		}
		if knownRollback {
			return s.failRun(r, userError(ctx, err))
		}
		return s.unknown(r), nil
	}
	r = finish(r, Succeeded, s.clock.Now(), nil)
	r.Version = &r.CandidateVersion
	finalCtx, finalCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer finalCancel()
	if err := s.finalize(finalCtx, r); err != nil {
		return s.unknown(r), nil
	}
	return r, nil
}

func (s *Service) apply(ctx context.Context, tx *targetTransaction, h database.TargetHandle, r MigrationRun, d MigrationDraft, statements []string) error {
	head, err := readHead(ctx, tx, h.Driver)
	if err != nil {
		return targetError(ctx, err)
	}
	if head.Version != r.CandidateVersion-1 {
		return problem("VERSION_CONFLICT", "Head database berubah; perbarui versi draft.")
	}
	if d.SourceFingerprint != nil {
		snap, err := s.inspector.InspectWith(ctx, h, tx)
		if err != nil {
			return err
		}
		fp, err := database.Fingerprint(snap)
		if err != nil {
			return err
		}
		if fp != *d.SourceFingerprint {
			return problem("SCHEMA_CONFLICT", "Schema berubah sejak kandidat dibuat; bandingkan ulang.")
		}
	}
	if h.Driver == database.Postgres {
		if err := tx.asEditor(ctx, h.EditorRole); err != nil {
			return err
		}
		for _, stmt := range statements {
			if _, err := tx.ExecContext(ctx, stmt); err != nil {
				return userError(ctx, err)
			}
		}
		if _, err := tx.ExecContext(ctx, "RESET ROLE"); err != nil {
			return targetError(ctx, err)
		}
	} else if err := sqliteUserSQL(ctx, tx.conn, r.SQL); err != nil {
		return err
	}
	at := s.clock.Now().UTC()
	var applied any = at
	if h.Driver == database.SQLite {
		applied = at.Format(time.RFC3339Nano)
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO `+ledgerTable(h.Driver)+`
	 (version,run_id,sql_hash,sql_snapshot,actor_id,actor_name_snapshot,applied_at) VALUES ($1,$2,$3,$4,$5,$6,$7)`,
		r.CandidateVersion, r.ID, r.SQLHash, r.SQL, r.Actor.ID, r.Actor.Username, applied)
	if err != nil {
		return targetError(ctx, err)
	}
	return nil
}

func domain(err error) *contracts.DomainError {
	var d *contracts.DomainError
	if errors.As(err, &d) {
		c := *d
		return &c
	}
	switch {
	case errors.Is(err, database.ErrNotFound):
		return problem("NOT_FOUND", "Database tidak ditemukan.")
	case errors.Is(err, database.ErrTargetTimeout), errors.Is(err, context.DeadlineExceeded):
		return problem("TARGET_TIMEOUT", "Batas waktu operasi database terlampaui.")
	case errors.Is(err, database.ErrLedgerIncompatible):
		return problem("LEDGER_INCOMPATIBLE", "Ledger target tidak sesuai.")
	case errors.Is(err, database.ErrTargetUnavailable), errors.Is(err, context.Canceled):
		return problem("TARGET_UNAVAILABLE", "Database target tidak dapat dihubungi.")
	case errors.Is(err, database.ErrTargetNotReady):
		return problem("TARGET_NOT_READY", "Ledger target belum siap.")
	default:
		return &contracts.DomainError{Code: "INTERNAL_ERROR", Message: "Operasi migrasi tidak dapat diselesaikan.", Cause: err}
	}
}

func (s *Service) failRun(r MigrationRun, err error) (MigrationRun, error) {
	d := domain(err)
	d.RunID = &r.ID
	r = finish(r, Failed, s.clock.Now(), &RunError{Code: d.Code, Message: d.Message})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.finalize(ctx, r); err != nil {
		return s.unknown(r), nil
	}
	return r, d
}

func (s *Service) unknown(r MigrationRun) MigrationRun {
	r.Status, r.Version, r.FinishedAt, r.Error = OutcomeUnknown, nil, nil, nil
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	s.store.markUnknown(ctx, r)
	return r
}

func (s *Service) Head(ctx context.Context, p contracts.Principal, databaseID string) (MigrationHead, error) {
	if err := s.authorize(ctx, p, databaseID, contracts.ViewDatabase); err != nil {
		return MigrationHead{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	h, release, err := s.provider.Acquire(ctx, databaseID)
	if err != nil {
		return MigrationHead{}, domain(err)
	}
	defer release()
	if !h.MigrationReady {
		return MigrationHead{}, problem("TARGET_NOT_READY", "Ledger target belum siap.")
	}
	head, err := readHead(ctx, h.DB, h.Driver)
	if err != nil {
		return MigrationHead{}, domain(targetError(ctx, err))
	}
	return head, nil
}

func (s *Service) Reconcile(ctx context.Context, p contracts.Principal, databaseID, runID string) (MigrationRun, error) {
	if err := s.authorize(ctx, p, databaseID, contracts.EditDatabase); err != nil {
		return MigrationRun{}, err
	}
	if !uuidPattern.MatchString(runID) {
		return MigrationRun{}, problem("INVALID_REQUEST", "ID run tidak valid.")
	}
	return s.reconcile(ctx, databaseID, runID)
}

func (s *Service) reconcile(ctx context.Context, databaseID, runID string) (MigrationRun, error) {
	release, ok := s.lease(databaseID)
	if !ok {
		return MigrationRun{}, problem("RUN_IN_PROGRESS", "Eksekusi database masih berlangsung.")
	}
	defer release()
	r, err := s.store.run(ctx, databaseID, runID)
	if err != nil || !unresolved(r) {
		return r, err
	}
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	h, done, err := s.provider.Acquire(ctx, databaseID)
	if err != nil {
		return s.unknown(r), nil
	}
	defer done()
	if !h.MigrationReady {
		return s.unknown(r), nil
	}
	tx, err := beginTarget(ctx, h)
	if err != nil {
		return s.unknown(r), nil
	}
	defer tx.close()
	var version int64
	var hash string
	var at time.Time
	var sqliteAt string
	var dest any = &at
	if h.Driver == database.SQLite {
		dest = &sqliteAt
	}
	err = tx.QueryRowContext(ctx, `SELECT version,sql_hash,applied_at FROM `+ledgerTable(h.Driver)+` WHERE run_id=$1`, r.ID).Scan(&version, &hash, dest)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		r = finish(r, Failed, s.clock.Now(), &RunError{Code: "EXECUTION_INTERRUPTED", Message: "Eksekusi terhenti tanpa perubahan committed."})
	case err != nil:
		return s.unknown(r), nil
	case hash != r.SQLHash || version != r.CandidateVersion:
		_ = s.unknown(r)
		d := problem("INTERNAL_ERROR", "Ledger dan metadata migrasi tidak konsisten.")
		d.RunID = &r.ID
		return r, d
	default:
		if h.Driver == database.SQLite {
			at, err = time.Parse(time.RFC3339Nano, sqliteAt)
			if err != nil {
				return r, internal(err)
			}
		}
		r = finish(r, Succeeded, at, nil)
		r.Version = &version
	}
	finalCtx, finalCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer finalCancel()
	if err := s.finalize(finalCtx, r); err != nil {
		return s.unknown(r), nil
	}
	return r, nil
}

func (s *Service) Recover(ctx context.Context) error {
	rows, err := s.store.DB.QueryContext(ctx, `SELECT database_id,id FROM migration_runs WHERE status IN ('running','outcome_unknown') ORDER BY started_at,id`)
	if err != nil {
		return internal(err)
	}
	type pending struct{ db, id string }
	var runs []pending
	for rows.Next() {
		var r pending
		if err := rows.Scan(&r.db, &r.id); err != nil {
			rows.Close()
			return internal(err)
		}
		runs = append(runs, r)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return internal(err)
	}
	for _, r := range runs {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if _, err := s.reconcile(ctx, r.db, r.id); err != nil {
			return fmt.Errorf("migration recovery failed: %w", err)
		}
	}
	return nil
}
