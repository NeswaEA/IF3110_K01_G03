package explain

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	pg_query "github.com/pganalyze/pg_query_go/v6"

	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/database"
)

// The explain policy is separate from the migration guard: it only
// decides whether one statement may be planned. Any rejection happens before
// a plan is requested.

// pgNoPlan are statement nodes that are valid DDL but have no plan.
// Everything not plannable and not listed here is UNSUPPORTED_SQL.
var pgNoPlan = map[string]bool{
	"CreateStmt": true, "CreateTableAsStmt": true, "IndexStmt": true, "ViewStmt": true,
	"CreateSchemaStmt": true, "CreateSeqStmt": true, "AlterSeqStmt": true,
	"CreateFunctionStmt": true, "AlterFunctionStmt": true, "CreateTrigStmt": true,
	"CreateEnumStmt": true, "AlterEnumStmt": true, "CompositeTypeStmt": true, "CreateDomainStmt": true,
	"AlterDomainStmt": true, "CreateRangeStmt": true, "DefineStmt": true, "RuleStmt": true,
	"CreatePolicyStmt": true, "AlterPolicyStmt": true, "CreateStatsStmt": true, "AlterStatsStmt": true,
	"AlterTableStmt": true, "AlterTypeStmt": true, "RenameStmt": true, "AlterObjectSchemaStmt": true,
	"AlterOwnerStmt": true, "DropStmt": true, "TruncateStmt": true, "CommentStmt": true, "ReindexStmt": true,
	"MergeStmt": true,
}

// classifyPostgres uses the PostgreSQL 17 parser without touching the target,
// so semicolons in strings or comments are not statement boundaries.
func classifyPostgres(sql string) (string, error) {
	tree, err := pg_query.Parse(sql)
	if err != nil {
		return "", &database.EngineError{Message: err.Error()} // syntax error text from the parser, no target involved
	}
	if len(tree.Stmts) != 1 {
		return "", ErrMultipleStatements
	}
	reserved, params, err := pgScan(sql)
	if err != nil || reserved {
		return "", ErrUnsupportedSQL
	}
	if params {
		// pgx would refuse to send it without arguments; report it as the engine's rejection
		return "", &database.EngineError{Message: "parameter placeholder tanpa nilai tidak didukung"}
	}
	raw := tree.Stmts[0]
	plannable := false
	switch n := raw.Stmt.Node.(type) {
	case *pg_query.Node_SelectStmt:
		if n.SelectStmt.IntoClause != nil {
			return "", ErrPlanUnsupported // SELECT ... INTO creates a table
		}
		plannable = true
	case *pg_query.Node_InsertStmt, *pg_query.Node_UpdateStmt, *pg_query.Node_DeleteStmt:
		plannable = true
	}
	if !plannable {
		if pgNoPlan[strings.TrimPrefix(fmt.Sprintf("%T", raw.Stmt.Node), "*pg_query.Node_")] {
			return "", ErrPlanUnsupported
		}
		return "", ErrUnsupportedSQL // control, session, privilege, utility and admin commands are never sent
	}
	// slice the original text so EXPLAIN sees exactly what the user wrote
	start, end := int(raw.StmtLocation), int(raw.StmtLocation+raw.StmtLen)
	if raw.StmtLen == 0 || end > len(sql) {
		end = len(sql)
	}
	return strings.TrimSpace(sql[start:end]), nil
}

// pgScan walks the whole parse tree, subqueries and CTEs included. reserved is
// a qualified reference to the _platform schema: a relation's schemaname, or a
// dotted name (_platform.fn(), _platform.t.col, a type) where it is any part
// but the last; a bare column or alias named _platform is not one. params is
// any $n placeholder.
func pgScan(sql string) (reserved, params bool, err error) {
	out, err := pg_query.ParseToJSON(sql)
	if err != nil {
		return false, false, err
	}
	var tree any
	if err := json.Unmarshal([]byte(out), &tree); err != nil {
		return false, false, err
	}
	var walk func(v any)
	walk = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			if x["schemaname"] == "_platform" {
				reserved = true
			}
			if _, ok := x["ParamRef"]; ok {
				params = true
			}
			for _, c := range x {
				walk(c)
			}
		case []any:
			for i, c := range x {
				if i < len(x)-1 {
					if m, ok := c.(map[string]any); ok {
						if s, ok := m["String"].(map[string]any); ok && s["sval"] == "_platform" {
							reserved = true
						}
					}
				}
				walk(c)
			}
		}
	}
	walk(tree)
	return reserved, params, nil
}

// classifySQLite asks the engine itself (see sqliteClassify).
func classifySQLite(ctx context.Context, db *sql.DB, query string) (string, error) {
	stmt, kind, err := sqliteClassify(ctx, db, query)
	if err != nil {
		return "", err
	}
	switch kind {
	case sqlitePlannable:
		return stmt, nil
	case sqliteNoPlan:
		return "", ErrPlanUnsupported
	case sqliteMultiple:
		return "", ErrMultipleStatements
	}
	return "", ErrUnsupportedSQL
}
