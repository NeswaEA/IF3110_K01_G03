package migrations

import (
	"context"
	"database/sql"
)

type Store struct{ DB *sql.DB }

func NewStore(db *sql.DB) *Store { return &Store{DB: db} }

// HasHistory reports whether a registration already has migration executions;
// such a registration may not be pointed at another target.
func (s *Store) HasHistory(ctx context.Context, databaseID string) (bool, error) {
	var exists bool
	err := s.DB.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM migration_executions WHERE database_id=$1)`, databaseID).Scan(&exists)
	return exists, err
}

// Head is the version after the latest committed history entry in the metadata mirror (0 when empty).
func (s *Store) Head(ctx context.Context, databaseID string) (int64, error) {
	var head int64
	err := s.DB.QueryRowContext(ctx, `SELECT coalesce((SELECT head_after FROM migration_history WHERE database_id=$1 ORDER BY seq DESC LIMIT 1), 0)`, databaseID).Scan(&head)
	return head, err
}
