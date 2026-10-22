package access

import (
	"context"
	"database/sql"
	"errors"

	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/contracts"
)

type DatabaseAccessLifecycle struct{ sessions contracts.SessionInvalidator }

var _ contracts.DatabaseAccessLifecycle = (*DatabaseAccessLifecycle)(nil)

func NewDatabaseAccessLifecycle(sessions contracts.SessionInvalidator) *DatabaseAccessLifecycle {
	return &DatabaseAccessLifecycle{sessions: sessions}
}

func (l *DatabaseAccessLifecycle) Disable(ctx context.Context, tx *sql.Tx, databaseID string) error {
	if tx == nil || l.sessions == nil {
		return failure(errors.New("database access lifecycle requires a transaction and session invalidator"))
	}
	var active bool
	err := tx.QueryRowContext(ctx, `SELECT active FROM database_connections WHERE id=$1 FOR UPDATE`, databaseID).Scan(&active)
	if errors.Is(err, sql.ErrNoRows) {
		return &contracts.DomainError{Code: "NOT_FOUND", Message: "Database tidak ditemukan."}
	}
	if err != nil {
		return failure(err)
	}
	if active {
		return failure(errors.New("database must be inactive before revoking access"))
	}
	rows, err := tx.QueryContext(ctx, `SELECT DISTINCT m.user_id FROM group_memberships m
		JOIN group_database_access g ON g.group_id=m.group_id WHERE g.database_id=$1 ORDER BY m.user_id`, databaseID)
	if err != nil {
		return failure(err)
	}
	defer rows.Close()
	var users []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return failure(err)
		}
		users = append(users, id)
	}
	if err := rows.Err(); err != nil {
		return failure(err)
	}
	if err := rows.Close(); err != nil {
		return failure(err)
	}
	if err := l.sessions.RevokeUsers(ctx, tx, users); err != nil {
		return failure(err)
	}
	return nil
}
