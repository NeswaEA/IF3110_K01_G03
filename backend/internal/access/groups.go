package access

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/contracts"
	"github.com/jackc/pgx/v5/pgconn"
)

type Group struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type GroupService struct {
	db       *sql.DB
	sessions contracts.SessionInvalidator
	policy   contracts.AccessChecker
	clock    contracts.Clock
}

func NewGroupService(db *sql.DB, sessions contracts.SessionInvalidator, policy contracts.AccessChecker, clock contracts.Clock) *GroupService {
	return &GroupService{db: db, sessions: sessions, policy: policy, clock: clock}
}

const groupColumns = "id, name, created_at, updated_at"

func scanGroup(row interface{ Scan(...any) error }) (Group, error) {
	var group Group
	err := row.Scan(&group.ID, &group.Name, &group.CreatedAt, &group.UpdatedAt)
	group.CreatedAt, group.UpdatedAt = group.CreatedAt.UTC(), group.UpdatedAt.UTC()
	return group, err
}

func (s *GroupService) List(ctx context.Context, actor contracts.Principal, limit, offset int) ([]Group, bool, error) {
	if err := s.policy.Require(ctx, actor, contracts.ManagePlatform, ""); err != nil {
		return nil, false, err
	}
	if err := validatePage(limit, offset); err != nil {
		return nil, false, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+groupColumns+` FROM groups ORDER BY name, id LIMIT $1 OFFSET $2`, limit+1, offset)
	if err != nil {
		return nil, false, failure(err)
	}
	defer rows.Close()
	groups := []Group{}
	for rows.Next() {
		group, err := scanGroup(rows)
		if err != nil {
			return nil, false, failure(err)
		}
		groups = append(groups, group)
	}
	if err := rows.Err(); err != nil {
		return nil, false, failure(err)
	}
	more := len(groups) > limit
	if more {
		groups = groups[:limit]
	}
	return groups, more, nil
}

func (s *GroupService) Get(ctx context.Context, actor contracts.Principal, id string) (Group, error) {
	if err := s.policy.Require(ctx, actor, contracts.ManagePlatform, ""); err != nil {
		return Group{}, err
	}
	if err := validGroupID(id); err != nil {
		return Group{}, err
	}
	group, err := scanGroup(s.db.QueryRowContext(ctx, `SELECT `+groupColumns+` FROM groups WHERE id=$1`, id))
	return group, groupError(err)
}

func (s *GroupService) Create(ctx context.Context, actor contracts.Principal, name string) (Group, error) {
	if err := s.policy.Require(ctx, actor, contracts.ManagePlatform, ""); err != nil {
		return Group{}, err
	}
	name, err := normalizeGroupName(name)
	if err != nil {
		return Group{}, err
	}
	now := s.clock.Now().UTC().Truncate(time.Microsecond)
	group, err := scanGroup(s.db.QueryRowContext(ctx, `INSERT INTO groups(id, name, created_at, updated_at)
		VALUES(gen_random_uuid(), $1, $2, $2) RETURNING `+groupColumns, name, now))
	if err != nil {
		return Group{}, groupError(err)
	}
	return group, nil
}

func (s *GroupService) Update(ctx context.Context, actor contracts.Principal, id, name string) (Group, error) {
	if err := s.policy.Require(ctx, actor, contracts.ManagePlatform, ""); err != nil {
		return Group{}, err
	}
	if err := validGroupID(id); err != nil {
		return Group{}, err
	}
	name, err := normalizeGroupName(name)
	if err != nil {
		return Group{}, err
	}
	group, err := scanGroup(s.db.QueryRowContext(ctx, `UPDATE groups
		SET name=$2, updated_at=CASE WHEN name=$2 THEN updated_at ELSE $3 END
		WHERE id=$1 RETURNING `+groupColumns, id, name, s.clock.Now().UTC().Truncate(time.Microsecond)))
	if err != nil {
		return Group{}, groupError(err)
	}
	return group, nil
}

func (s *GroupService) Delete(ctx context.Context, actor contracts.Principal, id string) error {
	if err := s.policy.Require(ctx, actor, contracts.ManagePlatform, ""); err != nil {
		return err
	}
	if err := validGroupID(id); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return failure(err)
	}
	defer tx.Rollback()
	if err := lockGroup(ctx, tx, id); err != nil {
		return err
	}
	users, err := groupMemberIDs(ctx, tx, id)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM groups WHERE id=$1`, id); err != nil {
		return failure(err)
	}
	if err := s.sessions.RevokeUsers(ctx, tx, users); err != nil {
		return failure(err)
	}
	if err := tx.Commit(); err != nil {
		return failure(err)
	}
	return nil
}

func (s *GroupService) Members(ctx context.Context, actor contracts.Principal, groupID string, limit, offset int) ([]User, bool, error) {
	if err := s.policy.Require(ctx, actor, contracts.ManagePlatform, ""); err != nil {
		return nil, false, err
	}
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
	rows, err := s.db.QueryContext(ctx, `SELECT `+userColumns+` FROM users
		WHERE id IN (SELECT user_id FROM group_memberships WHERE group_id=$1) ORDER BY id LIMIT $2 OFFSET $3`, groupID, limit+1, offset)
	if err != nil {
		return nil, false, failure(err)
	}
	defer rows.Close()
	users := []User{}
	for rows.Next() {
		user, err := scanUser(rows)
		if err != nil {
			return nil, false, failure(err)
		}
		users = append(users, user)
	}
	if err := rows.Err(); err != nil {
		return nil, false, failure(err)
	}
	more := len(users) > limit
	if more {
		users = users[:limit]
	}
	return users, more, nil
}

func (s *GroupService) AddMember(ctx context.Context, actor contracts.Principal, groupID, userID string) error {
	if err := s.policy.Require(ctx, actor, contracts.ManagePlatform, ""); err != nil {
		return err
	}
	return s.changeMember(ctx, groupID, userID, true)
}

func (s *GroupService) RemoveMember(ctx context.Context, actor contracts.Principal, groupID, userID string) error {
	if err := s.policy.Require(ctx, actor, contracts.ManagePlatform, ""); err != nil {
		return err
	}
	return s.changeMember(ctx, groupID, userID, false)
}

func (s *GroupService) changeMember(ctx context.Context, groupID, userID string, add bool) error {
	if err := validGroupID(groupID); err != nil {
		return err
	}
	if err := validUserID(userID); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return failure(err)
	}
	defer tx.Rollback()
	if err := lockGroup(ctx, tx, groupID); err != nil {
		return err
	}
	var found string
	if err := tx.QueryRowContext(ctx, `SELECT id FROM users WHERE id=$1 FOR KEY SHARE`, userID).Scan(&found); err != nil {
		return userLookupError(err)
	}
	var result sql.Result
	if add {
		result, err = tx.ExecContext(ctx, `INSERT INTO group_memberships(group_id, user_id, created_at)
			VALUES($1, $2, $3) ON CONFLICT(group_id, user_id) DO NOTHING`, groupID, userID, s.clock.Now().UTC().Truncate(time.Microsecond))
	} else {
		result, err = tx.ExecContext(ctx, `DELETE FROM group_memberships WHERE group_id=$1 AND user_id=$2`, groupID, userID)
	}
	if err != nil {
		return failure(err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return failure(err)
	}
	if changed != 0 {
		if add {
			err = s.sessions.RequireRotation(ctx, tx, []string{userID})
		} else {
			err = s.sessions.RevokeUsers(ctx, tx, []string{userID})
		}
		if err != nil {
			return failure(err)
		}
	}
	if err := tx.Commit(); err != nil {
		return failure(err)
	}
	return nil
}

func lockGroup(ctx context.Context, tx *sql.Tx, id string) error {
	// Membership mutations share this lock so deletion cannot miss a concurrent new member.
	var found string
	return groupError(tx.QueryRowContext(ctx, `SELECT id FROM groups WHERE id=$1 FOR UPDATE`, id).Scan(&found))
}

func groupMemberIDs(ctx context.Context, tx *sql.Tx, id string) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT user_id FROM group_memberships WHERE group_id=$1 ORDER BY user_id`, id)
	if err != nil {
		return nil, failure(err)
	}
	defer rows.Close()
	users := []string{}
	for rows.Next() {
		var userID string
		if err := rows.Scan(&userID); err != nil {
			return nil, failure(err)
		}
		users = append(users, userID)
	}
	if err := rows.Err(); err != nil {
		return nil, failure(err)
	}
	if err := rows.Close(); err != nil {
		return nil, failure(err)
	}
	return users, nil
}

func normalizeGroupName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if !utf8.ValidString(name) || utf8.RuneCountInString(name) < 1 || utf8.RuneCountInString(name) > 100 || strings.ContainsRune(name, 0) {
		return "", invalidGroupField("name", "Gunakan 1-100 karakter UTF-8 tanpa karakter NUL.")
	}
	return name, nil
}

func validGroupID(id string) error {
	if !accessIDPattern.MatchString(id) {
		return invalidGroupField("groupId", "Gunakan UUID huruf kecil.")
	}
	return nil
}

func invalidGroupField(field, message string) error {
	return &contracts.DomainError{Code: "INVALID_REQUEST", Message: "Data grup tidak valid.", Fields: map[string]string{field: message}}
}

func groupError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return &contracts.DomainError{Code: "NOT_FOUND", Message: "Grup tidak ditemukan."}
	}
	var pg *pgconn.PgError
	if errors.As(err, &pg) && pg.Code == "23505" && pg.ConstraintName == "groups_name_key" {
		return &contracts.DomainError{Code: "ALREADY_EXISTS", Message: "Nama grup sudah digunakan.", Fields: map[string]string{"name": "Pilih nama grup lain."}}
	}
	return failure(err)
}
