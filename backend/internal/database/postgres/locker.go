package postgres

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/stdlib"

	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/database"
)

// lockKey is the same on every target database; advisory locks are scoped to
// one database, so targets on a shared server do not block each other.
const lockKey = `hashtextextended('if3110.migration', 0)`

// Locker takes a session advisory lock on the target itself, held by a pinned
// connection that also runs every migration transaction. If the worker dies,
// the server ends its session and the lock goes with it.
type Locker struct {
	// PingInterval is how often an idle lock connection is checked; default 5s.
	PingInterval time.Duration
}

var _ database.MigrationLocker = Locker{}

func (l Locker) Acquire(ctx context.Context, h database.TargetHandle) (database.MigrationLock, error) {
	conn, err := h.DB.Conn(ctx)
	if err != nil {
		return database.MigrationLock{}, database.ClassifyTargetError(ctx, err)
	}
	var got bool
	if err := conn.QueryRowContext(ctx, `SELECT pg_try_advisory_lock(`+lockKey+`)`).Scan(&got); err != nil {
		discard(conn)
		return database.MigrationLock{}, database.ClassifyTargetError(ctx, err)
	}
	if !got {
		conn.Close()
		return database.MigrationLock{}, database.ErrLockBusy
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
				// a slow answer is not a dead session: allow at least 5s per ping
				if !alive(conn, max(interval, 5*time.Second)) {
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
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			var unlocked bool
			if err := conn.QueryRowContext(ctx, `SELECT pg_advisory_unlock(`+lockKey+`)`).Scan(&unlocked); err != nil || !unlocked {
				// Session state unknown (aborted transaction, broken socket): end
				// the session so the server drops the lock and any open transaction.
				discard(conn)
				return
			}
			conn.Close()
		})
	}
	return database.MigrationLock{Conn: conn, Lost: lost, Release: release}, nil
}

// alive pings the lock connection unless a statement is using it; a busy
// connection is alive by definition, and a dead one fails that statement.
func alive(conn *sql.Conn, timeout time.Duration) bool {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	err := conn.Raw(func(dc any) error {
		c, ok := dc.(*stdlib.Conn)
		if !ok {
			return errors.New("postgres: unexpected driver connection")
		}
		if c.Conn().PgConn().IsBusy() {
			return nil
		}
		return c.Conn().Ping(ctx)
	})
	return err == nil
}

// discard closes conn and tells database/sql not to reuse it, ending the session.
func discard(conn *sql.Conn) {
	_ = conn.Raw(func(any) error { return driver.ErrBadConn })
	conn.Close()
}
