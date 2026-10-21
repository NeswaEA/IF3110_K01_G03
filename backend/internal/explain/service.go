// Package explain returns estimated execution plans (EXPLAIN without ANALYZE)
// for one statement. It never creates drafts, runs or ledger rows.
//
// Authorization (database.edit) and CSRF are enforced at the HTTP/service
// entry once contracts.AccessChecker is available; they come before Explain.
package explain

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/database"
)

var (
	// ErrInvalidRequest (INVALID_REQUEST): sql empty after trim or not text.
	ErrInvalidRequest = errors.New("explain: invalid request")
	// ErrPayloadTooLarge (PAYLOAD_TOO_LARGE): sql over MaxSQLBytes.
	ErrPayloadTooLarge = errors.New("explain: sql too large")
	// ErrUnsupportedSQL (UNSUPPORTED_SQL): mode other than estimate, no
	// statement, a reserved namespace, or a control/utility command.
	ErrUnsupportedSQL = errors.New("explain: unsupported sql")
	// ErrMultipleStatements is reported as UNSUPPORTED_SQL with a field hint.
	ErrMultipleStatements = errors.New("explain: more than one statement")
	// ErrPlanUnsupported (PLAN_UNSUPPORTED): one valid statement without a plan
	// (DDL, SELECT ... INTO, MERGE), no planner for the driver, or no plan rows.
	ErrPlanUnsupported = errors.New("explain: statement has no plan")
)

const MaxSQLBytes = 256 << 10

// Planner is implemented by database/postgres.Planner and database/sqlite.Planner.
type Planner interface {
	Explain(ctx context.Context, t database.TargetHandle, stmt string) (database.PlanResult, error)
}

// DriverLookup reports the engine of an active registration without opening
// the target (connections.Registry).
type DriverLookup interface {
	Driver(ctx context.Context, databaseID string) (database.Driver, error)
}

type Service struct {
	provider database.TargetProvider
	drivers  DriverLookup
	planners map[database.Driver]Planner
	timeout  time.Duration
}

// NewService takes TARGET_QUERY_TIMEOUT as timeout (default 10s).
func NewService(p database.TargetProvider, d DriverLookup, planners map[database.Driver]Planner, timeout time.Duration) *Service {
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	return &Service{provider: p, drivers: d, planners: planners, timeout: timeout}
}

// Explain validates the request, classifies the statement, then asks the
// engine for a plan. Each step stops at its first failure. EngineError means
// the engine (or parser) rejected the SQL: SQL_EXECUTION_FAILED.
func (s *Service) Explain(ctx context.Context, databaseID, sql, mode string) (database.PlanResult, error) {
	switch {
	case len(sql) > MaxSQLBytes:
		return database.PlanResult{}, ErrPayloadTooLarge
	case strings.TrimSpace(sql) == "", strings.ContainsRune(sql, 0):
		return database.PlanResult{}, ErrInvalidRequest // a NUL would silently cut the text the engine sees
	case mode != "estimate":
		return database.PlanResult{}, ErrUnsupportedSQL // ANALYZE executes the statement, so it is not offered
	}

	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	drv, err := s.drivers.Driver(ctx, databaseID)
	if err != nil {
		return database.PlanResult{}, err
	}
	planner, ok := s.planners[drv]
	if !ok {
		return database.PlanResult{}, ErrPlanUnsupported
	}
	var stmt string
	if drv == database.Postgres {
		if stmt, err = classifyPostgres(sql); err != nil {
			return database.PlanResult{}, err
		}
	}

	h, release, err := s.provider.Acquire(ctx, databaseID)
	if err != nil {
		return database.PlanResult{}, err
	}
	defer release()
	if h.Driver != drv {
		return database.PlanResult{}, database.ErrTargetUnavailable // registration changed engine mid-request
	}
	if h.Driver == database.SQLite {
		if stmt, err = classifySQLite(ctx, h.DB, sql); err != nil {
			return database.PlanResult{}, err
		}
	}
	res, err := planner.Explain(ctx, h, stmt)
	if err != nil {
		return database.PlanResult{}, err
	}
	if len(res.Nodes) == 0 {
		return database.PlanResult{}, ErrPlanUnsupported
	}
	return res, nil
}

// ErrorCode maps explain errors to API error codes (empty if not an explain error).
func ErrorCode(err error) string {
	var ee *database.EngineError
	switch {
	case errors.Is(err, ErrInvalidRequest):
		return "INVALID_REQUEST"
	case errors.Is(err, ErrPayloadTooLarge):
		return "PAYLOAD_TOO_LARGE"
	case errors.Is(err, ErrUnsupportedSQL), errors.Is(err, ErrMultipleStatements):
		return "UNSUPPORTED_SQL"
	case errors.Is(err, ErrPlanUnsupported):
		return "PLAN_UNSUPPORTED"
	case errors.As(err, &ee):
		return "SQL_EXECUTION_FAILED"
	}
	return ""
}
