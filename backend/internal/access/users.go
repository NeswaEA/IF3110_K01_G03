package access

import (
	"context"
	"database/sql"
	"errors"
	"regexp"
	"time"

	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/auth"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/contracts"
	"github.com/jackc/pgx/v5/pgconn"
)

type User struct {
	ID              string    `json:"id"`
	Username        string    `json:"username"`
	IsPlatformAdmin bool      `json:"is_platform_admin"`
	Active          bool      `json:"active"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

type CreateUserInput struct {
	Username        string
	Password        string
	IsPlatformAdmin bool
}

type UpdateUserInput struct {
	Active          *bool
	IsPlatformAdmin *bool
}

type UserService struct {
	db       *sql.DB
	sessions contracts.SessionInvalidator
	clock    contracts.Clock
}

func NewUserService(db *sql.DB, sessions contracts.SessionInvalidator, clock contracts.Clock) *UserService {
	return &UserService{db: db, sessions: sessions, clock: clock}
}

const userColumns = "id, username, is_platform_admin, active, created_at, updated_at"

var userIDPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

func validUserID(id string) error {
	if !userIDPattern.MatchString(id) {
		return invalidUserField("userId", "Gunakan UUID huruf kecil.")
	}
	return nil
}

func scanUser(row interface{ Scan(...any) error }) (User, error) {
	var user User
	err := row.Scan(&user.ID, &user.Username, &user.IsPlatformAdmin, &user.Active, &user.CreatedAt, &user.UpdatedAt)
	user.CreatedAt, user.UpdatedAt = user.CreatedAt.UTC(), user.UpdatedAt.UTC()
	return user, err
}

func (s *UserService) List(ctx context.Context, limit, offset int) ([]User, bool, error) {
	if limit < 1 || limit > 200 {
		return nil, false, invalidUserField("limit", "Gunakan bilangan bulat 1 sampai 200.")
	}
	if offset < 0 || offset > 100000 {
		return nil, false, invalidUserField("offset", "Gunakan bilangan bulat 0 sampai 100000.")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+userColumns+` FROM users ORDER BY username, id LIMIT $1 OFFSET $2`, limit+1, offset)
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

func (s *UserService) Create(ctx context.Context, input CreateUserInput) (User, error) {
	username, err := auth.NormalizeUsername(input.Username)
	if err != nil {
		return User{}, err
	}
	hash, err := (auth.PasswordHasher{}).Hash(ctx, input.Password)
	if err != nil {
		return User{}, err
	}
	now := s.clock.Now().UTC().Truncate(time.Microsecond)
	user, err := scanUser(s.db.QueryRowContext(ctx, `INSERT INTO users
		(id, username, password_hash, is_platform_admin, created_at, updated_at)
		VALUES(gen_random_uuid(), $1, $2, $3, $4, $4) RETURNING `+userColumns,
		username, hash, input.IsPlatformAdmin, now))
	if err != nil {
		var pg *pgconn.PgError
		if errors.As(err, &pg) && pg.Code == "23505" && pg.ConstraintName == "users_username_key" {
			return User{}, &contracts.DomainError{Code: "ALREADY_EXISTS", Message: "Username sudah digunakan.",
				Fields: map[string]string{"username": "Pilih username lain."}}
		}
		return User{}, failure(err)
	}
	return user, nil
}

func (s *UserService) Update(ctx context.Context, id string, input UpdateUserInput) (User, error) {
	if err := validUserID(id); err != nil {
		return User{}, err
	}
	if input.Active == nil && input.IsPlatformAdmin == nil {
		return User{}, invalidUserField("body", "Isi active atau is_platform_admin.")
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return User{}, failure(err)
	}
	defer tx.Rollback()
	// Serialize status changes before counting admins; reads and session resolution can continue.
	if _, err := tx.ExecContext(ctx, `LOCK TABLE users IN SHARE ROW EXCLUSIVE MODE`); err != nil {
		return User{}, failure(err)
	}
	current, err := scanUser(tx.QueryRowContext(ctx, `SELECT `+userColumns+` FROM users WHERE id=$1`, id))
	if err != nil {
		return User{}, userLookupError(err)
	}
	updated := current
	if input.Active != nil {
		updated.Active = *input.Active
	}
	if input.IsPlatformAdmin != nil {
		updated.IsPlatformAdmin = *input.IsPlatformAdmin
	}
	if current.Active == updated.Active && current.IsPlatformAdmin == updated.IsPlatformAdmin {
		return current, nil
	}
	if current.Active && current.IsPlatformAdmin && (!updated.Active || !updated.IsPlatformAdmin) {
		var remaining bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM users WHERE active AND is_platform_admin AND id<>$1)`, id).Scan(&remaining); err != nil {
			return User{}, failure(err)
		}
		if !remaining {
			return User{}, &contracts.DomainError{Code: "INVALID_REQUEST", Message: "Admin aktif terakhir harus tetap aktif dan memiliki hak admin."}
		}
	}
	updated, err = scanUser(tx.QueryRowContext(ctx, `UPDATE users SET active=$2, is_platform_admin=$3, updated_at=$4
		WHERE id=$1 RETURNING `+userColumns, id, updated.Active, updated.IsPlatformAdmin, s.clock.Now().UTC().Truncate(time.Microsecond)))
	if err != nil {
		return User{}, failure(err)
	}
	if !updated.Active || (current.IsPlatformAdmin && !updated.IsPlatformAdmin) {
		err = s.sessions.RevokeUsers(ctx, tx, []string{id})
	} else {
		err = s.sessions.RequireRotation(ctx, tx, []string{id})
	}
	if err != nil {
		return User{}, failure(err)
	}
	if err := tx.Commit(); err != nil {
		return User{}, failure(err)
	}
	return updated, nil
}

func (s *UserService) ResetPassword(ctx context.Context, id, password string) error {
	if err := validUserID(id); err != nil {
		return err
	}
	hash, err := (auth.PasswordHasher{}).Hash(ctx, password)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return failure(err)
	}
	defer tx.Rollback()
	var updated string
	err = tx.QueryRowContext(ctx, `UPDATE users SET password_hash=$2, updated_at=$3 WHERE id=$1 RETURNING id`,
		id, hash, s.clock.Now().UTC().Truncate(time.Microsecond)).Scan(&updated)
	if err != nil {
		return userLookupError(err)
	}
	if err := s.sessions.RevokeUsers(ctx, tx, []string{id}); err != nil {
		return failure(err)
	}
	if err := tx.Commit(); err != nil {
		return failure(err)
	}
	return nil
}

func invalidUserField(field, message string) error {
	return &contracts.DomainError{Code: "INVALID_REQUEST", Message: "Data pengguna tidak valid.", Fields: map[string]string{field: message}}
}

func userLookupError(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return &contracts.DomainError{Code: "NOT_FOUND", Message: "Pengguna tidak ditemukan."}
	}
	return failure(err)
}
