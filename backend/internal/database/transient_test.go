package database

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"net"
	"syscall"
	"testing"
	"time"
)

type stateErr string

func (e stateErr) Error() string    { return "engine error " + string(e) }
func (e stateErr) SQLState() string { return string(e) }

func TestIsTransient(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"connection exception class", stateErr("08006"), true},
		{"connection failure wrapped", fmt.Errorf("exec: %w", stateErr("08001")), true},
		{"serialization failure", stateErr("40001"), true},
		{"deadlock", stateErr("40P01"), true},
		{"lock not available", stateErr("55P03"), true},
		{"admin shutdown", stateErr("57P01"), true},
		{"crash shutdown", stateErr("57P02"), true},
		{"cannot connect now", stateErr("57P03"), true},
		{"too many connections", stateErr("53300"), true},
		{"statement timeout", stateErr("57014"), false},
		{"syntax error", stateErr("42601"), false},
		{"unique violation", stateErr("23505"), false},
		{"insufficient privilege", stateErr("42501"), false},
		{"bad connection", driver.ErrBadConn, true},
		{"connection refused", &net.OpError{Op: "dial", Err: syscall.ECONNREFUSED}, true},
		{"connection reset", fmt.Errorf("read: %w", syscall.ECONNRESET), true},
		{"target unavailable", ErrTargetUnavailable, true},
		{"connect timeout", ErrTargetTimeout, true},
		{"cancelled", context.Canceled, false},
		{"plain error", errors.New("boom"), false},
	}
	for _, c := range cases {
		if got := IsTransient(c.err); got != c.want {
			t.Errorf("%s: IsTransient = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestSQLState(t *testing.T) {
	if got := SQLState(fmt.Errorf("x: %w", stateErr("40001"))); got != "40001" {
		t.Fatalf("SQLState = %q", got)
	}
	if got := SQLState(errors.New("x")); got != "" {
		t.Fatalf("SQLState of a non-engine error = %q", got)
	}
}

func TestBackoffStaysWithinFullJitterBounds(t *testing.T) {
	base, max := 2*time.Second, 30*time.Second
	for attempt := 1; attempt <= 70; attempt++ {
		ceiling := max
		if attempt <= 4 {
			ceiling = base << (attempt - 1) // 2s, 4s, 8s, 16s
		}
		for i := 0; i < 200; i++ {
			if d := Backoff(base, max, attempt); d < 0 || d > ceiling {
				t.Fatalf("attempt %d: %v outside [0, %v]", attempt, d, ceiling)
			}
		}
	}
	if d := Backoff(0, max, 1); d != 0 {
		t.Fatalf("zero base gave %v", d)
	}
}
