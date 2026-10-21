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
		errors.Is(err, ErrLedgerIncompatible), errors.Is(err, ErrInvalidSnapshot), errors.Is(err, ErrDiffUnsupported):
		return err
	case errors.Is(err, context.DeadlineExceeded), errors.Is(ctx.Err(), context.DeadlineExceeded):
		return ErrTargetTimeout
	case errors.Is(err, context.Canceled):
		return err
	}
	return ErrTargetUnavailable
}
