package database

import (
	"context"
	"errors"
)

// ClassifyTargetError maps a driver error from a read on the target to the
// sentinel set. The driver text is dropped on purpose: it can contain host
// names, user names or SQL. Sentinels and caller cancellation pass through.
func ClassifyTargetError(ctx context.Context, err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, ErrNotFound), errors.Is(err, ErrTargetTimeout), errors.Is(err, ErrTargetUnavailable),
		errors.Is(err, ErrTargetPermission), errors.Is(err, ErrLedgerIncompatible), errors.Is(err, ErrInvalidSnapshot),
		errors.Is(err, ErrDiffUnsupported):
		return err
	case errors.Is(err, context.DeadlineExceeded), errors.Is(ctx.Err(), context.DeadlineExceeded):
		return ErrTargetTimeout
	case errors.Is(err, context.Canceled):
		return err
	}
	switch sqlState(err) {
	case "42501": // insufficient_privilege
		return ErrTargetPermission
	case "57014": // query_canceled: statement_timeout fired just before the request deadline
		return ErrTargetTimeout
	}
	return ErrTargetUnavailable
}

// sqlState returns the SQLSTATE of a driver error that carries one (pgconn.PgError), else "".
func sqlState(err error) string {
	var s interface{ SQLState() string }
	if errors.As(err, &s) {
		return s.SQLState()
	}
	return ""
}
