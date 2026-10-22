package connections

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/database"
)

// AccessLifecycle is satisfied by contracts.DatabaseAccessLifecycle; the
// registry core keeps its own copy so it depends on database only.
type AccessLifecycle interface {
	Disable(ctx context.Context, tx *sql.Tx, databaseID string) error
}

// HistoryChecker reports whether a registration already has migration runs.
// It is supplied by the migrations module at composition time; nil means "no history".
type HistoryChecker interface {
	HasHistory(ctx context.Context, databaseID string) (bool, error)
}

// Features tells Capabilities which engines have a runner, diff generator and
// EXPLAIN adapter wired in. Unimplemented features stay false.
type Features struct {
	Runner  map[database.Driver]bool
	Diff    map[database.Driver]bool
	Explain map[database.Driver]bool
}

type ServiceOptions struct {
	Policy Policy
	// SQLiteDir is SQLITE_TARGET_DIR.
	SQLiteDir string
	// MetadataIdentity is database identity of METADATA_DATABASE_URL (see IdentityFromDSN);
	// registering it as a target is rejected.
	MetadataIdentity string
	Features         Features
	// QueryTimeout bounds a connection test (TARGET_QUERY_TIMEOUT); default 10s.
	QueryTimeout time.Duration
	Now          func() time.Time
}

type Service struct {
	meta      *sql.DB
	store     *Store
	cipher    *Cipher
	reg       *Registry
	init      database.TargetInitializer
	lifecycle AccessLifecycle
	history   HistoryChecker
	opts      ServiceOptions
}

func NewService(meta *sql.DB, c *Cipher, reg *Registry, init database.TargetInitializer,
	lifecycle AccessLifecycle, history HistoryChecker, opts ServiceOptions) *Service {
	if opts.QueryTimeout <= 0 {
		opts.QueryTimeout = 10 * time.Second
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	return &Service{meta: meta, store: &Store{DB: meta}, cipher: c, reg: reg, init: init,
		lifecycle: lifecycle, history: history, opts: opts}
}

// ---- DTOs ----

type Capabilities struct {
	TransactionalMigrations bool `json:"transactional_migrations"`
	SchemaDiff              bool `json:"schema_diff"`
	Explain                 bool `json:"explain"`
	ExplainAnalyze          bool `json:"explain_analyze"`
}

type AdminConnection struct {
	ID             string          `json:"id"`
	Name           string          `json:"name"`
	Driver         database.Driver `json:"driver"`
	Revision       int64           `json:"revision"`
	Active         bool            `json:"active"`
	MigrationReady bool            `json:"migration_ready"`
	Config         any             `json:"config"`
	LastCheckedAt  *time.Time      `json:"last_checked_at"`
	LastErrorCode  *string         `json:"last_error_code"`
}

type TestResult struct {
	Reachable    bool         `json:"reachable"`
	LatencyMS    int64        `json:"latency_ms"`
	Capabilities Capabilities `json:"capabilities"`
}

type RegisterInput struct {
	Name   string
	Driver database.Driver
	Config json.RawMessage
}

// UpdateInput: Name and Config are optional; Config holds only the changed
// fields. An omitted password keeps the stored one.
type UpdateInput struct {
	Revision int64
	Name     *string
	Config   json.RawMessage
}

// Capabilities derives what the application can do for a registration. It is
// not a promise that the target is reachable or that every statement works.
func (s *Service) Capabilities(d database.Driver, migrationReady bool) Capabilities {
	f := s.opts.Features
	return Capabilities{
		TransactionalMigrations: f.Runner[d] && migrationReady,
		SchemaDiff:              f.Diff[d],
		Explain:                 f.Explain[d],
		ExplainAnalyze:          false,
	}
}

// ErrorCode maps a registry error to its API error code (empty if not a registry error).
func ErrorCode(err error) string {
	switch {
	case errors.Is(err, database.ErrNotFound):
		return "NOT_FOUND"
	case errors.Is(err, ErrInvalid):
		return "INVALID_REQUEST"
	case errors.Is(err, ErrRevisionConflict), errors.Is(err, ErrIdentityLocked):
		return "REVISION_CONFLICT"
	case errors.Is(err, ErrDuplicateTarget):
		return "DUPLICATE_TARGET"
	case errors.Is(err, database.ErrTargetTimeout):
		return "TARGET_TIMEOUT"
	case errors.Is(err, database.ErrTargetUnavailable):
		return "TARGET_UNAVAILABLE"
	case errors.Is(err, database.ErrTargetNotReady):
		return "TARGET_NOT_READY"
	case errors.Is(err, database.ErrLedgerIncompatible):
		return "LEDGER_INCOMPATIBLE"
	}
	return ""
}

func (s *Service) dto(r Row) (AdminConnection, error) {
	cfg, err := s.reg.config(r)
	if err != nil {
		return AdminConnection{}, err
	}
	return AdminConnection{ID: r.ID, Name: r.Name, Driver: r.Driver, Revision: r.Revision, Active: r.Active,
		MigrationReady: r.MigrationReady, Config: cfg.Public(), LastCheckedAt: r.LastCheckedAt, LastErrorCode: r.LastErrorCode}, nil
}

func validName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if n := utf8.RuneCountInString(name); n < 1 || n > 100 {
		return "", invalid("name", "nama harus 1 sampai 100 karakter")
	}
	return name, nil
}

// serialize makes duplicate checks atomic across concurrent writers.
const duplicateLockKey = 727001

func (s *Service) lock(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, duplicateLockKey)
	return err
}

// checkTarget rejects the metadata database and any other active registration
// with the same target identity (except skipID).
func (s *Service) checkTarget(ctx context.Context, q Querier, cfg Config, skipID string) (string, error) {
	identity, err := cfg.TargetIdentity(s.opts.SQLiteDir)
	if err != nil {
		return "", err
	}
	if s.opts.MetadataIdentity != "" && identity == s.opts.MetadataIdentity {
		return "", invalid("config", "database metadata aplikasi tidak boleh didaftarkan sebagai target")
	}
	rows, err := s.store.ListActive(ctx, q)
	if err != nil {
		return "", err
	}
	for _, r := range rows {
		if r.ID == skipID {
			continue
		}
		other, err := s.reg.config(r)
		if err != nil {
			return "", err
		}
		if oid, err := other.TargetIdentity(s.opts.SQLiteDir); err == nil && oid == identity {
			return "", ErrDuplicateTarget
		}
	}
	return identity, nil
}

func (s *Service) Register(ctx context.Context, in RegisterInput) (AdminConnection, error) {
	name, err := validName(in.Name)
	if err != nil {
		return AdminConnection{}, err
	}
	cfg, err := ParseConfig(in.Driver, in.Config, s.opts.Policy)
	if err != nil {
		return AdminConnection{}, err
	}
	id, err := newUUID()
	if err != nil {
		return AdminConnection{}, err
	}
	plain, err := encodeConfig(cfg)
	if err != nil {
		return AdminConnection{}, err
	}
	enc, err := s.cipher.Seal(plain, []byte(id))
	if err != nil {
		return AdminConnection{}, err
	}

	tx, err := s.meta.BeginTx(ctx, nil)
	if err != nil {
		return AdminConnection{}, err
	}
	defer tx.Rollback()
	if err := s.lock(ctx, tx); err != nil {
		return AdminConnection{}, err
	}
	if _, err := s.checkTarget(ctx, tx, cfg, ""); err != nil {
		return AdminConnection{}, err
	}
	if err := s.store.Insert(ctx, tx, Row{ID: id, Name: name, Driver: in.Driver, ConfigEncrypted: enc}); err != nil {
		return AdminConnection{}, err
	}
	row, err := s.store.Get(ctx, tx, id)
	if err != nil {
		return AdminConnection{}, err
	}
	if err := tx.Commit(); err != nil {
		return AdminConnection{}, err
	}
	return s.dto(row)
}

func (s *Service) Get(ctx context.Context, id string) (AdminConnection, error) {
	row, err := s.store.Get(ctx, s.meta, id)
	if err != nil {
		return AdminConnection{}, err
	}
	return s.dto(row)
}

// List includes inactive rows (admin view). hasMore is computed by fetching one extra row.
func (s *Service) List(ctx context.Context, limit, offset int) (out []AdminConnection, hasMore bool, err error) {
	rows, err := s.store.List(ctx, s.meta, limit+1, offset)
	if err != nil {
		return nil, false, err
	}
	if len(rows) > limit {
		hasMore, rows = true, rows[:limit]
	}
	out = make([]AdminConnection, 0, len(rows))
	for _, r := range rows {
		d, err := s.dto(r)
		if err != nil {
			return nil, false, err
		}
		out = append(out, d)
	}
	return out, hasMore, nil
}

type postgresPatch struct {
	Host       *string `json:"host"`
	Port       *int    `json:"port"`
	Database   *string `json:"database"`
	Username   *string `json:"username"`
	Password   *string `json:"password"`
	SSLMode    *string `json:"sslmode"`
	EditorRole *string `json:"editor_role"`
}

type sqlitePatch struct {
	File *string `json:"file"`
}

func applyPatch(cur Config, raw json.RawMessage) (Config, error) {
	next := cur
	switch cur.Driver {
	case database.Postgres:
		var p postgresPatch
		if err := decodeStrict(raw, &p); err != nil {
			return Config{}, invalid("config", "objek konfigurasi tidak valid")
		}
		c := *cur.Postgres
		set := func(dst *string, v *string) {
			if v != nil {
				*dst = *v
			}
		}
		set(&c.Host, p.Host)
		set(&c.Database, p.Database)
		set(&c.Username, p.Username)
		set(&c.Password, p.Password) // empty string is kept and rejected by validation
		set(&c.SSLMode, p.SSLMode)
		set(&c.EditorRole, p.EditorRole)
		if p.Port != nil {
			c.Port = *p.Port
		}
		next.Postgres = &c
	case database.SQLite:
		var p sqlitePatch
		if err := decodeStrict(raw, &p); err != nil {
			return Config{}, invalid("config", "objek konfigurasi tidak valid")
		}
		c := *cur.SQLite
		if p.File != nil {
			c.File = *p.File
		}
		next.SQLite = &c
	}
	return next, nil
}

func (s *Service) Update(ctx context.Context, id string, in UpdateInput) (AdminConnection, error) {
	if in.Name == nil && len(in.Config) == 0 {
		return AdminConnection{}, invalid("revision", "sertakan minimal satu field yang diubah")
	}
	var name *string
	if in.Name != nil {
		n, err := validName(*in.Name)
		if err != nil {
			return AdminConnection{}, err
		}
		name = &n
	}

	tx, err := s.meta.BeginTx(ctx, nil)
	if err != nil {
		return AdminConnection{}, err
	}
	defer tx.Rollback()
	if err := s.lock(ctx, tx); err != nil {
		return AdminConnection{}, err
	}
	row, err := s.store.Get(ctx, tx, id)
	if err != nil {
		return AdminConnection{}, err
	}
	if !row.Active {
		return AdminConnection{}, database.ErrNotFound
	}
	if row.Revision != in.Revision {
		return AdminConnection{}, ErrRevisionConflict
	}

	upd := RowUpdate{Name: name}
	if len(in.Config) > 0 {
		cur, err := s.reg.config(row)
		if err != nil {
			return AdminConnection{}, err
		}
		next, err := applyPatch(cur, in.Config)
		if err != nil {
			return AdminConnection{}, err
		}
		if next.Driver == database.Postgres {
			if err := validatePostgres(*next.Postgres, s.opts.Policy); err != nil {
				return AdminConnection{}, err
			}
		} else if err := validateSQLite(*next.SQLite); err != nil {
			return AdminConnection{}, err
		}
		oldID, _ := cur.TargetIdentity(s.opts.SQLiteDir)
		newID, err := s.checkTarget(ctx, tx, next, id)
		if err != nil {
			return AdminConnection{}, err
		}
		if oldID != newID {
			if s.history != nil {
				has, err := s.history.HasHistory(ctx, id)
				if err != nil {
					return AdminConnection{}, err
				}
				if has {
					return AdminConnection{}, ErrIdentityLocked
				}
			}
			notReady := false
			upd.MigrationReady = &notReady // a new location needs a fresh ledger check
		}
		plain, err := encodeConfig(next)
		if err != nil {
			return AdminConnection{}, err
		}
		if upd.ConfigEncrypted, err = s.cipher.Seal(plain, []byte(id)); err != nil {
			return AdminConnection{}, err
		}
	}

	updated, err := s.store.Update(ctx, tx, id, in.Revision, upd)
	if err != nil {
		return AdminConnection{}, err
	}
	if err := tx.Commit(); err != nil {
		return AdminConnection{}, err
	}
	s.reg.Invalidate(id)
	return s.dto(updated)
}

// Test probes the target, verifies the ledger through the injected initializer,
// and records the outcome. A failure never removes the registration.
func (s *Service) Test(ctx context.Context, id string) (TestResult, error) {
	row, err := s.store.Get(ctx, s.meta, id)
	if err != nil {
		return TestResult{}, err
	}
	if !row.Active {
		return TestResult{}, database.ErrNotFound
	}
	ctx, cancel := context.WithTimeout(ctx, s.opts.QueryTimeout)
	defer cancel()

	start := time.Now()
	probeErr := s.probe(ctx, row)
	latency := time.Since(start).Milliseconds()

	var code *string
	if probeErr != nil {
		c := ErrorCode(probeErr)
		if c == "" {
			c = "INTERNAL_ERROR"
		}
		code = &c
	}
	ready := probeErr == nil
	if merr := s.store.MarkChecked(context.WithoutCancel(ctx), s.meta, id, ready, code, s.opts.Now().UTC()); merr != nil && probeErr == nil {
		return TestResult{}, merr
	}
	if probeErr != nil {
		return TestResult{}, probeErr
	}
	return TestResult{Reachable: true, LatencyMS: latency, Capabilities: s.Capabilities(row.Driver, true)}, nil
}

func (s *Service) probe(ctx context.Context, row Row) error {
	h, release, err := s.reg.Acquire(ctx, row.ID)
	if err != nil {
		return err
	}
	defer release()
	if row.Driver == database.Postgres {
		if err := s.checkEditorRole(ctx, h, row); err != nil {
			return err
		}
	}
	if s.init == nil {
		return errors.New("connections: no ledger initializer configured")
	}
	if err := s.init.EnsureReady(ctx, h); err != nil {
		return classify(ctx, err)
	}
	return nil
}

// checkEditorRole requires the configured editor_role to exist and not be a superuser.
func (s *Service) checkEditorRole(ctx context.Context, h database.TargetHandle, row Row) error {
	cfg, err := s.reg.config(row)
	if err != nil {
		return err
	}
	var super bool
	err = h.DB.QueryRowContext(ctx, `SELECT rolsuper FROM pg_roles WHERE rolname = $1`, cfg.Postgres.EditorRole).Scan(&super)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return invalid("config.editor_role", "role tidak ditemukan di target")
	case err != nil:
		return classify(ctx, err)
	case super:
		return invalid("config.editor_role", "role editor tidak boleh superuser")
	}
	return nil
}

// classify maps driver errors to the sentinel set without keeping their text.
func classify(ctx context.Context, err error) error {
	switch {
	case errors.Is(err, database.ErrTargetNotReady), errors.Is(err, database.ErrLedgerIncompatible), errors.Is(err, database.ErrTargetTimeout),
		errors.Is(err, database.ErrTargetUnavailable), errors.Is(err, database.ErrNotFound):
		return err
	case errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded):
		return database.ErrTargetTimeout
	case errors.Is(err, context.Canceled):
		return err
	}
	return database.ErrTargetUnavailable
}

// Deactivate disables a registration and, in the same metadata transaction,
// lets the access module revoke the sessions of the affected users.
func (s *Service) Deactivate(ctx context.Context, id string) error {
	tx, err := s.meta.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := s.store.Deactivate(ctx, tx, id); err != nil {
		return err
	}
	if s.lifecycle != nil {
		if err := s.lifecycle.Disable(ctx, tx, id); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.reg.Invalidate(id)
	return nil
}
