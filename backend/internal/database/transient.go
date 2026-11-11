package database

import (
	"context"
	"database/sql/driver"
	"errors"
	"math/rand/v2"
	"net"
	"strings"
	"syscall"
	"time"
)

// SQLState returns the SQLSTATE of an engine error (pgconn.PgError), or "".
func SQLState(err error) string {
	var s interface{ SQLState() string }
	if errors.As(err, &s) {
		return s.SQLState()
	}
	return ""
}

// IsTransient reports whether a failed migration attempt may succeed when
// retried unchanged: lost or refused connections, serialization failures,
// deadlocks, lock timeouts and server shutdown or overload. Statement timeouts
// (57014), syntax, constraint and permission errors are permanent.
func IsTransient(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) {
		return false
	}
	switch state := SQLState(err); {
	case strings.HasPrefix(state, "08"):
		return true
	case state != "":
		switch state {
		case "40001", "40P01", "55P03", "57P01", "57P02", "57P03", "53300":
			return true
		}
		return false
	}
	if errors.Is(err, driver.ErrBadConn) || errors.Is(err, ErrTargetUnavailable) || errors.Is(err, ErrTargetTimeout) ||
		errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.EPIPE) {
		return true
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	// pgconn wraps dial and I/O failures in types without a SQLSTATE; their
	// SafeToRetry/Timeout methods say whether the server ever saw the statement.
	var retry interface{ SafeToRetry() bool }
	if errors.As(err, &retry) && retry.SafeToRetry() {
		return true
	}
	var timeout interface{ Timeout() bool }
	return errors.As(err, &timeout) && timeout.Timeout()
}

// Backoff is the full-jitter delay before retry attempt (1-based):
// random in [0, min(max, base·2^(attempt-1))].
func Backoff(base, max time.Duration, attempt int) time.Duration {
	if base <= 0 || max <= 0 {
		return 0
	}
	ceiling := max
	if attempt < 1 {
		attempt = 1
	}
	if shift := attempt - 1; shift < 62 && base <= max>>shift {
		ceiling = base << shift
	}
	return rand.N(ceiling + 1)
}
