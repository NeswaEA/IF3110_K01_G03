package connections

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/database"
)

// Querier is satisfied by *sql.DB and *sql.Tx.
type Querier interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

type Row struct {
	ID              string
	Name            string
	Driver          database.Driver
	ConfigEncrypted []byte
	Revision        int64
	Active          bool
	MigrationReady  bool
	CreatedAt       time.Time
	UpdatedAt       time.Time
	LastCheckedAt   *time.Time
	LastErrorCode   *string
}

// Store is the metadata repository for database_connections. All SQL is parameterised.
type Store struct{ DB *sql.DB }

const cols = `id, name, driver, config_encrypted, revision, active, migration_ready,
	created_at, updated_at, last_checked_at, last_error_code`

func scan(s interface{ Scan(...any) error }) (Row, error) {
	var r Row
	var drv string
	var checked sql.NullTime
	var code sql.NullString
	if err := s.Scan(&r.ID, &r.Name, &drv, &r.ConfigEncrypted, &r.Revision, &r.Active, &r.MigrationReady,
		&r.CreatedAt, &r.UpdatedAt, &checked, &code); err != nil {
		return Row{}, err
	}
	r.Driver = database.Driver(drv)
	if checked.Valid {
		t := checked.Time.UTC()
		r.LastCheckedAt = &t
	}
	if code.Valid {
		r.LastErrorCode = &code.String
	}
	r.CreatedAt, r.UpdatedAt = r.CreatedAt.UTC(), r.UpdatedAt.UTC()
	return r, nil
}

func (s *Store) Insert(ctx context.Context, q Querier, r Row) error {
	_, err := q.ExecContext(ctx,
		`INSERT INTO database_connections (id, name, driver, config_encrypted) VALUES ($1, $2, $3, $4)`,
		r.ID, r.Name, string(r.Driver), r.ConfigEncrypted)
	return err
}

// Get returns the row whether or not it is active; ErrNotFound if absent.
func (s *Store) Get(ctx context.Context, q Querier, id string) (Row, error) {
	r, err := scan(q.QueryRowContext(ctx, `SELECT `+cols+` FROM database_connections WHERE id = $1`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Row{}, database.ErrNotFound
	}
	return r, err
}

// List orders by name then id.
func (s *Store) List(ctx context.Context, q Querier, limit, offset int) ([]Row, error) {
	rows, err := q.QueryContext(ctx,
		`SELECT `+cols+` FROM database_connections ORDER BY name, id LIMIT $1 OFFSET $2`, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Row{}
	for rows.Next() {
		r, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ListActive is used for duplicate detection; registrations are few, so this
// decrypts them all instead of storing a separate identity column.
func (s *Store) ListActive(ctx context.Context, q Querier) ([]Row, error) {
	rows, err := q.QueryContext(ctx, `SELECT `+cols+` FROM database_connections WHERE active ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Row{}
	for rows.Next() {
		r, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

type RowUpdate struct {
	Name            *string
	ConfigEncrypted []byte // nil keeps the stored value
	MigrationReady  *bool
}

// Update applies fields with compare-and-swap on revision and bumps it. Returns
// ErrRevisionConflict when the row exists but the revision moved, ErrNotFound if absent.
func (s *Store) Update(ctx context.Context, q Querier, id string, expectedRev int64, u RowUpdate) (Row, error) {
	var name, ready, cfg any
	if u.Name != nil {
		name = *u.Name
	}
	if u.MigrationReady != nil {
		ready = *u.MigrationReady
	}
	if u.ConfigEncrypted != nil {
		cfg = u.ConfigEncrypted
	}
	r, err := scan(q.QueryRowContext(ctx,
		`UPDATE database_connections
		    SET name = COALESCE($3, name),
		        config_encrypted = COALESCE($4, config_encrypted),
		        migration_ready = COALESCE($5, migration_ready),
		        revision = revision + 1, updated_at = now()
		  WHERE id = $1 AND revision = $2 AND active
		  RETURNING `+cols, id, expectedRev, name, cfg, ready))
	if errors.Is(err, sql.ErrNoRows) {
		cur, gerr := s.Get(ctx, q, id)
		if gerr != nil {
			return Row{}, gerr
		}
		if !cur.Active {
			return Row{}, database.ErrNotFound
		}
		return Row{}, ErrRevisionConflict
	}
	return r, err
}

// Deactivate marks the registration inactive and bumps the revision. It is
// idempotent for an already inactive row.
func (s *Store) Deactivate(ctx context.Context, q Querier, id string) error {
	res, err := q.ExecContext(ctx,
		`UPDATE database_connections SET active = false, revision = revision + 1, updated_at = now()
		  WHERE id = $1 AND active`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		_, err := s.Get(ctx, q, id)
		return err // nil when it exists and is already inactive
	}
	return nil
}

// MarkChecked records a connection test. It does not bump the revision, so it
// neither replaces pools nor invalidates an admin's pending edit.
func (s *Store) MarkChecked(ctx context.Context, q Querier, id string, revision int64, ready bool, errCode *string, at time.Time) error {
	var code any
	if errCode != nil {
		code = *errCode
	}
	result, err := q.ExecContext(ctx,
		`UPDATE database_connections SET migration_ready = $2, last_checked_at = $3, last_error_code = $4
		  WHERE id = $1 AND revision = $5 AND active`, id, ready, at, code, revision)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count == 0 {
		return ErrRevisionConflict
	}
	return nil
}

// newUUID returns a random lowercase UUID v4.
func newUUID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}
