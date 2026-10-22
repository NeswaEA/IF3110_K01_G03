package database

import (
	"context"
	"database/sql"
	"errors"
)

// Sentinel errors for adapters and providers. This package cannot import
// contracts, so callers map them to DomainError codes (NOT_FOUND,
// TARGET_UNAVAILABLE, TARGET_TIMEOUT, FORBIDDEN, LEDGER_INCOMPATIBLE,
// DIFF_UNSUPPORTED, INVALID_REQUEST).
var (
	ErrNotFound          = errors.New("database: registration not found or inactive")
	ErrTargetUnavailable = errors.New("database: target unavailable")
	ErrTargetNotReady    = errors.New("database: target initialization is not available")
	ErrTargetTimeout     = errors.New("database: target timeout")
	// ErrTargetPermission: the target role may not read the object (viewer
	// reads only; on user SQL the engine message is an EngineError instead).
	ErrTargetPermission   = errors.New("database: target role lacks privilege")
	ErrLedgerIncompatible = errors.New("database: ledger incompatible")
	ErrDiffUnsupported    = errors.New("database: diff unsupported for driver")
	ErrInvalidSnapshot    = errors.New("database: invalid schema snapshot")
)

// TargetProvider leases an active target. On success the release func is
// non-nil and must be called exactly once; it never closes the shared pool.
// On error the returned handle is unusable and release is nil. The provider
// does not authorise users: callers check access before Acquire.
type TargetProvider interface {
	Acquire(ctx context.Context, databaseID string) (TargetHandle, func(), error)
}

// SQLExecutor is the read subset shared by *sql.DB, *sql.Conn and *sql.Tx.
type SQLExecutor interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

// Inspector reads schema metadata and never mutates the target.
type Inspector interface {
	Inspect(ctx context.Context, target TargetHandle) (SchemaSnapshot, error)
	// InspectWith reuses a connection or transaction the caller already
	// holds; it opens no connection and never commits or rolls back.
	InspectWith(ctx context.Context, target TargetHandle, q SQLExecutor) (SchemaSnapshot, error)
}

// TargetInitializer creates or verifies the reserved migration ledger. It is
// idempotent and returns ErrLedgerIncompatible for a mismatching ledger.
type TargetInitializer interface {
	EnsureReady(ctx context.Context, target TargetHandle) error
}

// DiffConfigProvider gives fn an absolute temporary Atlas config path for two
// active PostgreSQL targets. The file is mode 0600 and always removed. SQLite
// targets return ErrDiffUnsupported.
type DiffConfigProvider interface {
	WithDiffConfig(ctx context.Context, sourceID, desiredID string,
		fn func(configPath string) error) error
}
