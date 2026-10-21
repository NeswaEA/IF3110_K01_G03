// Package explain returns estimated execution plans (EXPLAIN without ANALYZE)
// for one statement. It never creates drafts, runs or ledger rows.
//
// Authorization (database.edit) is enforced at the HTTP/service entry once
// contracts.AccessChecker is available.
package explain

import (
	"context"
	"errors"
	"time"

	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/database"
)

var (
	// ErrUnsupportedSQL (UNSUPPORTED_SQL): mode other than estimate, empty input,
	// or a control/utility command.
	ErrUnsupportedSQL = errors.New("explain: unsupported sql")
	// ErrMultipleStatements is reported as UNSUPPORTED_SQL with a field hint.
	ErrMultipleStatements = errors.New("explain: more than one statement")
	// ErrPlanUnsupported (PLAN_UNSUPPORTED): one valid statement without a plan (DDL).
	ErrPlanUnsupported = errors.New("explain: statement has no plan")
)

const MaxSQLBytes = 256 << 10

// Planner is implemented by database/postgres.Planner and database/sqlite.Planner.
type Planner interface {
	Explain(ctx context.Context, t database.TargetHandle, stmt string) (database.PlanResult, error)
}

type Service struct {
	provider database.TargetProvider
	planners map[database.Driver]Planner
	timeout  time.Duration
}

// NewService takes TARGET_QUERY_TIMEOUT as timeout (default 10s).
func NewService(p database.TargetProvider, planners map[database.Driver]Planner, timeout time.Duration) *Service {
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	return &Service{provider: p, planners: planners, timeout: timeout}
}

// Explain validates mode and statement, then asks the engine for a plan.
// EngineError means the engine (or parser) rejected the SQL: SQL_EXECUTION_FAILED.
func (s *Service) Explain(ctx context.Context, databaseID, sql, mode string) (database.PlanResult, error) {
	if mode != "estimate" {
		return database.PlanResult{}, ErrUnsupportedSQL // ANALYZE executes the statement, so it is not offered
	}
	if len(sql) == 0 || len(sql) > MaxSQLBytes {
		return database.PlanResult{}, ErrUnsupportedSQL
	}

	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	h, release, err := s.provider.Acquire(ctx, databaseID)
	if err != nil {
		return database.PlanResult{}, err
	}
	defer release()

	planner, ok := s.planners[h.Driver]
	if !ok {
		return database.PlanResult{}, ErrPlanUnsupported
	}
	var stmt string
	switch h.Driver {
	case database.Postgres:
		stmt, err = classifyPostgres(sql)
	case database.SQLite:
		stmt, err = classifySQLite(sql)
	default:
		err = ErrPlanUnsupported
	}
	if err != nil {
		return database.PlanResult{}, err
	}
	return planner.Explain(ctx, h, stmt)
}

// ErrorCode maps explain errors to API error codes (empty if not an explain error).
func ErrorCode(err error) string {
	var ee *database.EngineError
	switch {
	case errors.Is(err, ErrUnsupportedSQL), errors.Is(err, ErrMultipleStatements):
		return "UNSUPPORTED_SQL"
	case errors.Is(err, ErrPlanUnsupported):
		return "PLAN_UNSUPPORTED"
	case errors.As(err, &ee):
		return "SQL_EXECUTION_FAILED"
	}
	return ""
}
