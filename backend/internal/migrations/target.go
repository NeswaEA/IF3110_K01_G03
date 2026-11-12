package migrations

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"strconv"
	"time"

	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/database"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/database/postgres"
	sqlite3 "github.com/mattn/go-sqlite3"
)

type executor interface {
	database.SQLExecutor
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

type targetTransaction struct {
	executor
	conn     *sql.Conn
	pg       *sql.Tx
	driver   database.Driver
	finished bool
}

func beginTarget(ctx context.Context, h database.TargetHandle) (*targetTransaction, error) {
	conn, err := h.DB.Conn(ctx)
	if err != nil {
		return nil, database.ClassifyTargetError(ctx, err)
	}
	t := &targetTransaction{conn: conn, driver: h.Driver}
	if h.Driver == database.Postgres {
		t.pg, err = conn.BeginTx(ctx, nil)
		if err == nil {
			t.executor = t.pg
			if deadline, ok := ctx.Deadline(); ok {
				_, err = t.ExecContext(ctx, `SELECT set_config('statement_timeout', $1, true)`, strconv.FormatInt(max(1, time.Until(deadline).Milliseconds()), 10))
			}
			if err == nil {
				_, err = t.ExecContext(ctx, `SELECT pg_advisory_xact_lock(`+postgres.MigrationLockKey+`)`)
			}
		}
	} else if h.Driver == database.SQLite {
		t.executor = conn
		busy := int64(5000)
		if deadline, ok := ctx.Deadline(); ok {
			busy = min(busy, max(1, time.Until(deadline).Milliseconds()))
		}
		_, err = conn.ExecContext(ctx, "PRAGMA busy_timeout = "+strconv.FormatInt(busy, 10))
		if err == nil {
			_, err = conn.ExecContext(ctx, "BEGIN IMMEDIATE")
		}
	} else {
		err = database.ErrTargetUnavailable
	}
	if err != nil {
		t.close()
		return nil, targetError(ctx, err)
	}
	return t, nil
}

func (t *targetTransaction) commit(ctx context.Context) error {
	var err error
	if t.pg != nil {
		err = t.pg.Commit()
	} else {
		_, err = t.ExecContext(ctx, "COMMIT")
	}
	if err == nil {
		t.finished = true
	}
	return err
}

func (t *targetTransaction) rollback() error {
	if t.finished {
		return nil
	}
	var err error
	if t.pg != nil {
		err = t.pg.Rollback()
	} else if t.executor != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		var active bool
		if rawErr := t.conn.Raw(func(c any) error { active = !c.(*sqlite3.SQLiteConn).AutoCommit(); return nil }); rawErr != nil {
			return rawErr
		}
		if active {
			_, err = t.ExecContext(ctx, "ROLLBACK")
		}
	}
	if err == nil {
		t.finished = true
	}
	return err
}

func (t *targetTransaction) close() {
	if err := t.rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
		_ = t.conn.Raw(func(any) error { return driver.ErrBadConn })
	}
	if t.driver == database.SQLite {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if _, err := t.conn.ExecContext(ctx, "PRAGMA busy_timeout = 5000"); err != nil {
			_ = t.conn.Raw(func(any) error { return driver.ErrBadConn })
		}
	}
	_ = t.conn.Close()
}

func targetError(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	var se sqlite3.Error
	if errors.As(err, &se) && (se.Code == sqlite3.ErrBusy || se.Code == sqlite3.ErrLocked || se.Code == sqlite3.ErrInterrupt) {
		return database.ErrTargetTimeout
	}
	return database.ClassifyTargetError(ctx, err)
}

func (t *targetTransaction) asEditor(ctx context.Context, role string) error {
	if role == "" {
		return database.ErrTargetUnavailable
	}
	_, err := t.ExecContext(ctx, "SET LOCAL ROLE "+postgres.QuoteIdent(role))
	err = targetError(ctx, err)
	if errors.Is(err, database.ErrTargetPermission) {
		return database.ErrTargetUnavailable
	}
	return err
}
