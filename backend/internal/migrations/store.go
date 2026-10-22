package migrations

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"time"
)

type Store struct{ DB *sql.DB }

func NewStore(db *sql.DB) *Store { return &Store{DB: db} }

func (s *Store) HasHistory(ctx context.Context, databaseID string) (bool, error) {
	var exists bool
	err := s.DB.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM migration_runs WHERE database_id=$1)`, databaseID).Scan(&exists)
	return exists, err
}

const draftColumns = `id,database_id,title,sql,revision,expected_version,source_fingerprint,applied_at,created_by,updated_by,created_at,updated_at`
const runColumns = `id,database_id,draft_id,draft_revision,candidate_version,version,sql_snapshot,sql_hash,actor_id,actor_name_snapshot,status,started_at,finished_at,error_code,error_message,input_hash`

type scanner interface{ Scan(...any) error }

func scanDraft(row scanner) (MigrationDraft, error) {
	var d MigrationDraft
	err := row.Scan(&d.ID, &d.DatabaseID, &d.Title, &d.SQL, &d.Revision, &d.ExpectedVersion, &d.SourceFingerprint, &d.AppliedAt, &d.CreatedBy, &d.UpdatedBy, &d.CreatedAt, &d.UpdatedAt)
	d.CreatedAt, d.UpdatedAt = d.CreatedAt.UTC(), d.UpdatedAt.UTC()
	if d.AppliedAt != nil {
		at := d.AppliedAt.UTC()
		d.AppliedAt = &at
	}
	return d, resourceError(err)
}

func scanRun(row scanner) (MigrationRun, error) {
	var r MigrationRun
	var hash []byte
	var code, message *string
	err := row.Scan(&r.ID, &r.DatabaseID, &r.DraftID, &r.DraftRevision, &r.CandidateVersion, &r.Version, &r.SQL, &hash, &r.Actor.ID, &r.Actor.Username, &r.Status, &r.StartedAt, &r.FinishedAt, &code, &message, &r.inputHash)
	r.SQLHash = "sha256:" + hex.EncodeToString(hash)
	r.StartedAt = r.StartedAt.UTC()
	if r.FinishedAt != nil {
		at := r.FinishedAt.UTC()
		r.FinishedAt = &at
	}
	if code != nil {
		r.Error = &RunError{Code: *code}
		if message != nil {
			r.Error.Message = *message
		}
	}
	return r, resourceError(err)
}

func resourceError(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return problem("NOT_FOUND", "Migrasi tidak ditemukan.")
	}
	if err != nil {
		return internal(err)
	}
	return nil
}

func getDraft(ctx context.Context, q databaseReader, databaseID, id string, lock bool) (MigrationDraft, error) {
	query := `SELECT ` + draftColumns + ` FROM migration_drafts WHERE database_id=$1 AND id=$2`
	if lock {
		query += ` FOR UPDATE`
	}
	return scanDraft(q.QueryRowContext(ctx, query, databaseID, id))
}

type databaseReader interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func (s *Store) run(ctx context.Context, databaseID, id string) (MigrationRun, error) {
	return scanRun(s.DB.QueryRowContext(ctx, `SELECT `+runColumns+` FROM migration_runs WHERE database_id=$1 AND id=$2`, databaseID, id))
}

func findKey(ctx context.Context, q databaseReader, databaseID, key string) (MigrationRun, bool, error) {
	r, err := scanRun(q.QueryRowContext(ctx, `SELECT `+runColumns+` FROM migration_runs WHERE database_id=$1 AND idempotency_key=$2`, databaseID, key))
	if err != nil {
		if isCode(err, "NOT_FOUND") {
			return MigrationRun{}, false, nil
		}
		return MigrationRun{}, false, err
	}
	return r, true, nil
}

func (s *Store) finalize(ctx context.Context, r MigrationRun) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var code, message any
	if r.Error != nil {
		code, message = r.Error.Code, r.Error.Message
	}
	res, err := tx.ExecContext(ctx, `UPDATE migration_runs SET status=$3,version=$4,finished_at=$5,error_code=$6,error_message=$7
	 WHERE database_id=$1 AND id=$2 AND status IN ('running','outcome_unknown')`, r.DatabaseID, r.ID, r.Status, r.Version, r.FinishedAt, code, message)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		current, e := scanRun(tx.QueryRowContext(ctx, `SELECT `+runColumns+` FROM migration_runs WHERE database_id=$1 AND id=$2`, r.DatabaseID, r.ID))
		if e != nil {
			return e
		}
		if current.Status != r.Status || (r.Version != nil && (current.Version == nil || *current.Version != *r.Version)) {
			return errors.New("migration outcome changed")
		}
	}
	if r.Status == Succeeded {
		_, err = tx.ExecContext(ctx, `UPDATE migration_drafts SET applied_at=$3 WHERE database_id=$1 AND id=$2`, r.DatabaseID, r.DraftID, r.FinishedAt)
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) markUnknown(ctx context.Context, r MigrationRun) {
	_, _ = s.DB.ExecContext(ctx, `UPDATE migration_runs SET status='outcome_unknown' WHERE database_id=$1 AND id=$2 AND status='running'`, r.DatabaseID, r.ID)
}

func finish(r MigrationRun, status string, at time.Time, err *RunError) MigrationRun {
	r.Status, r.Error = status, err
	at = at.UTC()
	r.FinishedAt = &at
	return r
}
