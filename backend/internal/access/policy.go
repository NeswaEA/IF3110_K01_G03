package access

import (
	"context"
	"database/sql"
	"errors"

	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/contracts"
)

type Policy struct{ db *sql.DB }

var _ contracts.AccessChecker = (*Policy)(nil)

func NewPolicy(db *sql.DB) *Policy { return &Policy{db: db} }

func (p *Policy) Effective(ctx context.Context, userID, databaseID string) (contracts.AccessLevel, error) {
	if err := ctx.Err(); err != nil {
		return contracts.AccessNone, failure(err)
	}
	if userID == "" || databaseID == "" {
		return contracts.AccessNone, nil
	}
	var level contracts.AccessLevel
	err := p.db.QueryRowContext(ctx, `SELECT CASE
		WHEN bool_or(g.level='edit') THEN 'edit'
		WHEN bool_or(g.level='view') THEN 'view'
		ELSE 'none' END
		FROM users u
		JOIN group_memberships m ON m.user_id=u.id
		JOIN group_database_access g ON g.group_id=m.group_id
		JOIN database_connections d ON d.id=g.database_id
		WHERE u.id=$1 AND d.id=$2 AND u.active AND d.active`, userID, databaseID).Scan(&level)
	if err != nil {
		return contracts.AccessNone, failure(err)
	}
	return level, nil
}

func (p *Policy) Require(ctx context.Context, principal contracts.Principal, action contracts.Action, databaseID string) error {
	if err := ctx.Err(); err != nil {
		return failure(err)
	}
	if principal.UserID == "" {
		return &contracts.DomainError{Code: "UNAUTHENTICATED", Message: "Silakan login untuk melanjutkan."}
	}
	if action != contracts.ManagePlatform && action != contracts.ViewDatabase && action != contracts.EditDatabase {
		return forbidden()
	}
	var active, admin bool
	err := p.db.QueryRowContext(ctx, `SELECT active, is_platform_admin FROM users WHERE id=$1`, principal.UserID).Scan(&active, &admin)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && !active) {
		return &contracts.DomainError{Code: "UNAUTHENTICATED", Message: "Silakan login untuk melanjutkan."}
	}
	if err != nil {
		return failure(err)
	}
	if action == contracts.ManagePlatform {
		if !admin || databaseID != "" {
			return forbidden()
		}
		return nil
	}
	level, err := p.Effective(ctx, principal.UserID, databaseID)
	if err != nil {
		return err
	}
	if level == contracts.AccessNone {
		return &contracts.DomainError{Code: "NOT_FOUND", Message: "Database tidak ditemukan."}
	}
	if action == contracts.EditDatabase && level != contracts.AccessEdit {
		return forbidden()
	}
	return nil
}

func forbidden() error {
	return &contracts.DomainError{Code: "FORBIDDEN", Message: "Akses ditolak."}
}

func failure(err error) error {
	return &contracts.DomainError{Code: "INTERNAL_ERROR", Message: "Pemeriksaan akses gagal.", Cause: err}
}
