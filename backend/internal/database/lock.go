package database

import (
	"context"
	"database/sql"
	"errors"
)

// ErrLockBusy: another worker holds the migration lock of the target. It is a
// signal to retry later, never an HTTP error.
var ErrLockBusy = errors.New("migration lock busy")

// MigrationLock is the exclusive right to run migrations on one target.
type MigrationLock struct {
	// Conn is the pinned target connection that holds the lock; every
	// transaction of the execution must run on it.
	Conn *sql.Conn
	// Lost is closed when the lock connection fails; the executor must stop.
	Lost <-chan struct{}
	// Release unlocks and returns or discards the connection; idempotent.
	Release func()
}

// MigrationLocker takes the lock without waiting: ErrLockBusy when held elsewhere.
type MigrationLocker interface {
	Acquire(ctx context.Context, h TargetHandle) (MigrationLock, error)
}
