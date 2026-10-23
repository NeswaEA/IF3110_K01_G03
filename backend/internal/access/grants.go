package access

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/contracts"
)

type Grant struct {
	DatabaseID string                `json:"database_id"`
	Level      contracts.AccessLevel `json:"level"`
}

func (s *GroupService) Grants(ctx context.Context, groupID string, limit, offset int) ([]Grant, bool, error) {
	if err := validGroupID(groupID); err != nil {
		return nil, false, err
	}
	if err := validatePage(limit, offset); err != nil {
		return nil, false, err
	}
	var found string
	if err := s.db.QueryRowContext(ctx, `SELECT id FROM groups WHERE id=$1`, groupID).Scan(&found); err != nil {
		return nil, false, groupError(err)
	}
	rows, err := s.db.QueryContext(ctx, `SELECT database_id, level FROM group_database_access
		WHERE group_id=$1 ORDER BY database_id LIMIT $2 OFFSET $3`, groupID, limit+1, offset)
	if err != nil {
		return nil, false, failure(err)
	}
	defer rows.Close()
	grants := []Grant{}
	for rows.Next() {
		var grant Grant
		if err := rows.Scan(&grant.DatabaseID, &grant.Level); err != nil {
			return nil, false, failure(err)
		}
		grants = append(grants, grant)
	}
	if err := rows.Err(); err != nil {
		return nil, false, failure(err)
	}
	more := len(grants) > limit
	if more {
		grants = grants[:limit]
	}
	return grants, more, nil
}

func (s *GroupService) SetGrant(ctx context.Context, groupID, databaseID string, level contracts.AccessLevel) (Grant, error) {
	if level != contracts.AccessView && level != contracts.AccessEdit {
		return Grant{}, invalidGrantField("level", "Gunakan view atau edit.")
	}
	if err := s.changeGrant(ctx, groupID, databaseID, level); err != nil {
		return Grant{}, err
	}
	return Grant{DatabaseID: databaseID, Level: level}, nil
}

func (s *GroupService) RemoveGrant(ctx context.Context, groupID, databaseID string) error {
	return s.changeGrant(ctx, groupID, databaseID, contracts.AccessNone)
}

func (s *GroupService) changeGrant(ctx context.Context, groupID, databaseID string, level contracts.AccessLevel) error {
	if err := validGroupID(groupID); err != nil {
		return err
	}
	if !accessIDPattern.MatchString(databaseID) {
		return invalidGrantField("databaseId", "Gunakan UUID huruf kecil.")
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return failure(err)
	}
	defer tx.Rollback()
	if err := lockGroup(ctx, tx, groupID); err != nil {
		return err
	}
	// Match connection deactivation's row lock before changing grants or sessions.
	var found string
	err = tx.QueryRowContext(ctx, `SELECT id FROM database_connections WHERE id=$1 FOR UPDATE`, databaseID).Scan(&found)
	if errors.Is(err, sql.ErrNoRows) {
		return &contracts.DomainError{Code: "NOT_FOUND", Message: "Database tidak ditemukan."}
	}
	if err != nil {
		return failure(err)
	}
	previous := contracts.AccessNone
	err = tx.QueryRowContext(ctx, `SELECT level FROM group_database_access WHERE group_id=$1 AND database_id=$2`, groupID, databaseID).Scan(&previous)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return failure(err)
	}
	if previous == level {
		return nil
	}
	if level == contracts.AccessNone {
		_, err = tx.ExecContext(ctx, `DELETE FROM group_database_access WHERE group_id=$1 AND database_id=$2`, groupID, databaseID)
	} else {
		_, err = tx.ExecContext(ctx, `INSERT INTO group_database_access(group_id, database_id, level, updated_at)
			VALUES($1, $2, $3, $4) ON CONFLICT(group_id, database_id)
			DO UPDATE SET level=EXCLUDED.level, updated_at=EXCLUDED.updated_at`, groupID, databaseID, level, s.clock.Now().UTC().Truncate(time.Microsecond))
	}
	if err != nil {
		return failure(err)
	}
	users, err := groupMemberIDs(ctx, tx, groupID)
	if err != nil {
		return err
	}
	if level == contracts.AccessNone || (previous == contracts.AccessEdit && level == contracts.AccessView) {
		err = s.sessions.RevokeUsers(ctx, tx, users)
	} else {
		err = s.sessions.RequireRotation(ctx, tx, users)
	}
	if err != nil {
		return failure(err)
	}
	if err := tx.Commit(); err != nil {
		return failure(err)
	}
	return nil
}

func invalidGrantField(field, message string) error {
	return &contracts.DomainError{Code: "INVALID_REQUEST", Message: "Data hak akses tidak valid.", Fields: map[string]string{field: message}}
}
