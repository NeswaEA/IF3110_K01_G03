package sqlite

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"sync"
	"time"

	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/database"
)

// Locker serialises migrations on one SQLite target with a session advisory
// lock on a pinned metadata connection (SQLite has no session-scoped lock that
// dies with the worker). Conn is the target's pinned connection; transactions
// on it use BEGIN IMMEDIATE.
type Locker struct {
	Metadata *sql.DB
	// PingInterval is how often the metadata lock connection is checked; default 5s.
	PingInterval time.Duration
}

var _ database.MigrationLocker = Locker{}

func (l Locker) Acquire(ctx context.Context, h database.TargetHandle) (database.MigrationLock, error) {
	if l.Metadata == nil {
		return database.MigrationLock{}, errors.New("sqlite: locker needs the metadata database")
	}
	meta, err := l.Metadata.Conn(ctx)
	if err != nil {
		return database.MigrationLock{}, err
	}
	var got bool
	if err := meta.QueryRowContext(ctx, `SELECT pg_try_advisory_lock(hashtextextended('if3110.migration:' || $1, 0))`, h.DatabaseID).Scan(&got); err != nil {
		discard(meta)
		return database.MigrationLock{}, err
	}
	if !got {
		meta.Close()
		return database.MigrationLock{}, database.ErrLockBusy
	}
	target, err := h.DB.Conn(ctx)
	if err != nil {
		unlock(meta, h.DatabaseID)
		return database.MigrationLock{}, database.ClassifyTargetError(ctx, err)
	}

	interval := l.PingInterval
	if interval <= 0 {
		interval = 5 * time.Second
	}
	lost, done := make(chan struct{}), make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				pctx, cancel := context.WithTimeout(context.Background(), max(interval, 5*time.Second))
				err := meta.PingContext(pctx)
				cancel()
				if err != nil {
					close(lost)
					return
				}
			}
		}
	}()

	var once sync.Once
	release := func() {
		once.Do(func() {
			close(done)
			wg.Wait()
			target.Close()
			unlock(meta, h.DatabaseID)
		})
	}
	return database.MigrationLock{Conn: target, Lost: lost, Release: release}, nil
}

func unlock(meta *sql.Conn, databaseID string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var unlocked bool
	err := meta.QueryRowContext(ctx, `SELECT pg_advisory_unlock(hashtextextended('if3110.migration:' || $1, 0))`, databaseID).Scan(&unlocked)
	if err != nil || !unlocked {
		discard(meta) // ending the metadata session drops the lock
		return
	}
	meta.Close()
}

func discard(conn *sql.Conn) {
	_ = conn.Raw(func(any) error { return driver.ErrBadConn })
	conn.Close()
}
