package connections

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib" // registers "pgx"
	_ "github.com/mattn/go-sqlite3"    // registers "sqlite3" (CGO)

	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/database"
)

type RegistryOptions struct {
	ConnectTimeout time.Duration // TARGET_CONNECT_TIMEOUT, default 5s
	SQLiteDir      string        // SQLITE_TARGET_DIR
}

// Registry implements database.TargetProvider and database.DiffConfigProvider.
// It keeps one pool per (registration, revision): a config update gives later
// requests a new pool, and the old one closes after its last lease is released.
// It does not authorise users.
type Registry struct {
	meta   *sql.DB
	store  *Store
	cipher *Cipher
	opts   RegistryOptions

	mu    sync.Mutex
	pools map[string]*poolEntry // current pool per registration id
}

type poolEntry struct {
	id       string
	rev      int64
	db       *sql.DB
	leases   int
	retired  bool
	closed   bool
	verified atomic.Bool
	editor   string // PostgreSQL editor_role of this revision
}

var (
	_ database.TargetProvider     = (*Registry)(nil)
	_ database.DiffConfigProvider = (*Registry)(nil)
)

func NewRegistry(meta *sql.DB, c *Cipher, opts RegistryOptions) *Registry {
	if opts.ConnectTimeout <= 0 {
		opts.ConnectTimeout = 5 * time.Second
	}
	return &Registry{meta: meta, store: &Store{DB: meta}, cipher: c, opts: opts, pools: map[string]*poolEntry{}}
}

func (r *Registry) config(row Row) (Config, error) {
	plain, err := r.cipher.Open(row.ConfigEncrypted, []byte(row.ID))
	if err != nil {
		return Config{}, err
	}
	return decodeConfig(row.Driver, plain)
}

func (r *Registry) openDB(row Row) (*sql.DB, string, error) {
	cfg, err := r.config(row)
	if err != nil {
		return nil, "", err
	}
	switch row.Driver {
	case database.Postgres:
		db, err := sql.Open("pgx", cfg.Postgres.DSN(int(r.opts.ConnectTimeout/time.Second)))
		if err != nil {
			return nil, "", err
		}
		db.SetMaxOpenConns(5)
		db.SetMaxIdleConns(2)
		db.SetConnMaxLifetime(30 * time.Minute)
		return db, cfg.Postgres.EditorRole, nil
	case database.SQLite:
		path, err := ResolveSQLitePath(r.opts.SQLiteDir, cfg.SQLite.File)
		if err != nil {
			return nil, "", err
		}
		// mode=rw: a missing file is "unavailable", never silently created.
		// _foreign_keys is a DSN option so every pooled connection enforces FKs.
		q := url.Values{"mode": {"rw"}, "_foreign_keys": {"1"}, "_busy_timeout": {"5000"}}
		db, err := sql.Open("sqlite3", "file:"+filepath.ToSlash(path)+"?"+q.Encode())
		if err != nil {
			return nil, "", err
		}
		db.SetMaxOpenConns(1)
		db.SetMaxIdleConns(1)
		db.SetConnMaxLifetime(0)
		return db, "", nil
	}
	return nil, "", fmt.Errorf("connections: unknown driver %q", row.Driver)
}

// Acquire leases the pool of an active registration. Errors never contain a DSN.
func (r *Registry) Acquire(ctx context.Context, id string) (database.TargetHandle, func(), error) {
	row, err := r.store.Get(ctx, r.meta, id)
	if err != nil {
		return database.TargetHandle{}, nil, err
	}
	if !row.Active {
		return database.TargetHandle{}, nil, database.ErrNotFound
	}

	e, err := r.lease(row)
	if err != nil {
		return database.TargetHandle{}, nil, err
	}
	if !e.verified.Load() {
		if err := r.ping(ctx, e); err != nil {
			r.unlease(e)
			return database.TargetHandle{}, nil, err
		}
		e.verified.Store(true)
	}

	var once sync.Once
	release := func() { once.Do(func() { r.unlease(e) }) }
	return database.TargetHandle{
		DatabaseID: row.ID, Driver: row.Driver, Revision: row.Revision,
		MigrationReady: row.MigrationReady, DB: e.db, EditorRole: e.editor,
	}, release, nil
}

// Driver reports the engine of an active registration without opening the
// target, so callers can reject input before any connection is made.
func (r *Registry) Driver(ctx context.Context, id string) (database.Driver, error) {
	row, err := r.activeRow(ctx, id)
	if err != nil {
		return "", err
	}
	return row.Driver, nil
}

func (r *Registry) ping(ctx context.Context, e *poolEntry) error {
	pctx, cancel := context.WithTimeout(ctx, r.opts.ConnectTimeout)
	defer cancel()
	err := e.db.PingContext(pctx)
	switch {
	case err == nil:
		return nil
	case ctx.Err() != nil && !errors.Is(ctx.Err(), context.DeadlineExceeded):
		return ctx.Err() // caller cancelled
	case errors.Is(err, context.DeadlineExceeded) || errors.Is(pctx.Err(), context.DeadlineExceeded):
		return database.ErrTargetTimeout
	default:
		return database.ErrTargetUnavailable // original error dropped on purpose: it may name host or user
	}
}

func (r *Registry) lease(row Row) (*poolEntry, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if cur := r.pools[row.ID]; cur != nil {
		if cur.rev == row.Revision {
			cur.leases++
			return cur, nil
		}
		r.retireLocked(cur)
		delete(r.pools, row.ID)
	}
	db, editor, err := r.openDB(row)
	if err != nil {
		// undecryptable config or a SQLite path that no longer validates: report as unavailable, no detail
		return nil, database.ErrTargetUnavailable
	}
	e := &poolEntry{id: row.ID, rev: row.Revision, db: db, leases: 1, editor: editor}
	r.pools[row.ID] = e
	return e, nil
}

func (r *Registry) unlease(e *poolEntry) {
	r.mu.Lock()
	e.leases--
	closeNow := e.retired && e.leases == 0 && !e.closed
	if closeNow {
		e.closed = true
	}
	r.mu.Unlock()
	if closeNow {
		_ = e.db.Close()
	}
}

// retireLocked closes the pool now if idle, otherwise after its last lease.
func (r *Registry) retireLocked(e *poolEntry) {
	e.retired = true
	if e.leases == 0 && !e.closed {
		e.closed = true
		go e.db.Close() // Close can block on in-flight connections; do not hold r.mu
	}
}

// Invalidate retires the current pool of a registration (config update, deactivate).
func (r *Registry) Invalidate(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if cur := r.pools[id]; cur != nil {
		r.retireLocked(cur)
		delete(r.pools, id)
	}
}

// Close retires every pool; used at shutdown.
func (r *Registry) Close() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for id, e := range r.pools {
		r.retireLocked(e)
		delete(r.pools, id)
	}
}

// WithDiffConfig writes a temporary Atlas config (mode 0600, private directory)
// for two active PostgreSQL registrations, runs fn, and always removes it.
// SQLite, or two different engines, return database.ErrDiffUnsupported.
func (r *Registry) WithDiffConfig(ctx context.Context, sourceID, desiredID string, fn func(configPath string) error) error {
	src, err := r.activeRow(ctx, sourceID)
	if err != nil {
		return err
	}
	dst, err := r.activeRow(ctx, desiredID)
	if err != nil {
		return err
	}
	if src.Driver != database.Postgres || dst.Driver != database.Postgres {
		return database.ErrDiffUnsupported
	}
	sc, err := r.config(src)
	if err != nil {
		return database.ErrTargetUnavailable
	}
	dc, err := r.config(dst)
	if err != nil {
		return database.ErrTargetUnavailable
	}
	srcURL, dstURL := sc.Postgres.DSN(int(r.opts.ConnectTimeout/time.Second)), dc.Postgres.DSN(int(r.opts.ConnectTimeout/time.Second))
	for _, s := range []string{srcURL, dstURL} {
		if strings.ContainsAny(s, "\"\\\n") || strings.Contains(s, "${") || strings.Contains(s, "%{") {
			return invalid("config", "konfigurasi tidak dapat dipakai untuk diff")
		}
	}

	dir, err := os.MkdirTemp("", "if3110-atlas-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)

	path := filepath.Join(dir, "atlas.hcl")
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, werr := fmt.Fprintf(f, "env \"source\" {\n  url = %q\n}\n\nenv \"desired\" {\n  url = %q\n}\n", srcURL, dstURL)
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		return werr
	}
	return redactErr(fn(path), sc.Postgres.Password, dc.Postgres.Password, srcURL, dstURL, path)
}

func (r *Registry) activeRow(ctx context.Context, id string) (Row, error) {
	row, err := r.store.Get(ctx, r.meta, id)
	if err != nil {
		return Row{}, err
	}
	if !row.Active {
		return Row{}, database.ErrNotFound
	}
	return row, nil
}

type redactedError struct {
	msg   string
	cause error
}

func (e *redactedError) Error() string { return e.msg }
func (e *redactedError) Unwrap() error { return e.cause }

// redactErr hides secrets in err's message while keeping errors.Is/As working.
func redactErr(err error, secrets ...string) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	for _, s := range secrets {
		if s != "" {
			msg = strings.ReplaceAll(msg, s, "***")
		}
	}
	if msg == err.Error() {
		return err
	}
	return &redactedError{msg: msg, cause: err}
}
