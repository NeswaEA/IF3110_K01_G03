package migrations

import (
	"context"
	"database/sql"
	"errors"

	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/contracts"
)

var _ contracts.ReviewSubjects = (*Store)(nil)

func requestNotFound() error {
	return problem("NOT_FOUND", "Change request tidak ditemukan.")
}

func requestNotOpen() error {
	return problem("REQUEST_NOT_OPEN", "Change request sudah tidak terbuka.")
}

func (s *Store) LoadSubject(ctx context.Context, q contracts.Querier, requestID string) (contracts.ReviewSubject, error) {
	var subject contracts.ReviewSubject
	if !contracts.ValidID(requestID) {
		return subject, requestNotFound()
	}
	query := `SELECT id, database_id, migration_id, action, migration_revision,
		content_hash, requested_by, status FROM change_requests WHERE id=$1`
	if _, transactional := q.(*sql.Tx); transactional {
		query += " FOR UPDATE"
	}
	err := q.QueryRowContext(ctx, query, requestID).Scan(&subject.RequestID, &subject.DatabaseID,
		&subject.MigrationID, &subject.Action, &subject.Revision, &subject.ContentHash,
		&subject.RequestedBy, &subject.Status)
	if errors.Is(err, sql.ErrNoRows) {
		return subject, requestNotFound()
	}
	if err != nil {
		return subject, internal(err)
	}
	return subject, nil
}

func (s *Store) ApplyReviewState(ctx context.Context, tx *sql.Tx, requestID, status string) error {
	switch status {
	case "pending_review", "approved", "rejected":
	default:
		return invalidField("status", "Status review tidak valid.")
	}
	return s.transitionOpenRequest(ctx, tx, requestID, status)
}

func (s *Store) transitionOpenRequest(ctx context.Context, tx *sql.Tx, requestID, status string) error {
	now := s.now()
	var id string
	err := tx.QueryRowContext(ctx, `UPDATE change_requests
		SET status=$2, updated_at=$3, closed_at=CASE WHEN $2 IN ('rejected','cancelled') THEN $3::timestamptz END
		WHERE id=$1 AND status IN ('pending_review','approved') RETURNING id`, requestID, status, now).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return requestNotOpen()
	}
	if err != nil {
		return internal(err)
	}
	return nil
}
