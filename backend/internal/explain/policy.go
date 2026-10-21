package explain

import (
	"strings"

	pg_query "github.com/pganalyze/pg_query_go/v6"

	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/database"
)

// The explain policy is separate from the migration guard: it only
// decides whether one statement may be planned. Any rejection happens before
// the engine is touched.

// classifyPostgres uses the PostgreSQL 17 parser, so semicolons in strings or
// comments are not statement boundaries.
func classifyPostgres(sql string) (string, error) {
	tree, err := pg_query.Parse(sql)
	if err != nil {
		return "", &database.EngineError{Message: err.Error()} // syntax error text from the parser, no target involved
	}
	if len(tree.Stmts) != 1 {
		return "", ErrMultipleStatements
	}
	raw := tree.Stmts[0]
	switch raw.Stmt.Node.(type) {
	case *pg_query.Node_SelectStmt, *pg_query.Node_InsertStmt, *pg_query.Node_UpdateStmt,
		*pg_query.Node_DeleteStmt, *pg_query.Node_MergeStmt:
		// slice the original text so EXPLAIN sees exactly what the user wrote
		start, end := int(raw.StmtLocation), int(raw.StmtLocation+raw.StmtLen)
		if raw.StmtLen == 0 || end > len(sql) {
			end = len(sql)
		}
		return strings.TrimSpace(sql[start:end]), nil
	case *pg_query.Node_ExplainStmt, *pg_query.Node_TransactionStmt, *pg_query.Node_VariableSetStmt,
		*pg_query.Node_VariableShowStmt, *pg_query.Node_DiscardStmt, *pg_query.Node_DoStmt,
		*pg_query.Node_CallStmt, *pg_query.Node_CopyStmt, *pg_query.Node_PrepareStmt,
		*pg_query.Node_ExecuteStmt, *pg_query.Node_DeallocateStmt, *pg_query.Node_LockStmt,
		*pg_query.Node_ListenStmt, *pg_query.Node_NotifyStmt, *pg_query.Node_UnlistenStmt,
		*pg_query.Node_VacuumStmt, *pg_query.Node_CheckPointStmt, *pg_query.Node_LoadStmt:
		return "", ErrUnsupportedSQL // control or utility commands: out of scope, never sent
	}
	return "", ErrPlanUnsupported // a valid statement (DDL, GRANT, ...) that has no plan
}

// classifySQLite splits on statement boundaries outside literals, quoted
// identifiers and comments, then checks the leading keyword.
func classifySQLite(sql string) (string, error) {
	stmts := splitSQLite(sql)
	if len(stmts) == 0 {
		return "", ErrUnsupportedSQL
	}
	if len(stmts) != 1 {
		return "", ErrMultipleStatements
	}
	stmt := stmts[0]
	switch firstKeyword(stmt) {
	case "SELECT", "VALUES", "WITH", "INSERT", "REPLACE", "UPDATE", "DELETE":
		return stmt, nil
	case "CREATE", "ALTER", "DROP", "REINDEX", "ANALYZE":
		return "", ErrPlanUnsupported
	}
	// BEGIN/COMMIT/END/ROLLBACK/SAVEPOINT/RELEASE, EXPLAIN, PRAGMA, ATTACH, DETACH, VACUUM, unknown
	return "", ErrUnsupportedSQL
}

func splitSQLite(s string) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); {
		c := s[i]
		switch {
		case c == '\'' || c == '"' || c == '`':
			i = skipQuoted(s, i, c)
			continue
		case c == '[':
			if j := strings.IndexByte(s[i:], ']'); j >= 0 {
				i += j + 1
			} else {
				i = len(s)
			}
			continue
		case c == '-' && i+1 < len(s) && s[i+1] == '-':
			if j := strings.IndexByte(s[i:], '\n'); j >= 0 {
				i += j + 1
			} else {
				i = len(s)
			}
			continue
		case c == '/' && i+1 < len(s) && s[i+1] == '*':
			if j := strings.Index(s[i+2:], "*/"); j >= 0 {
				i += j + 4
			} else {
				i = len(s)
			}
			continue
		case c == ';':
			if stmt := strings.TrimSpace(s[start:i]); hasCode(stmt) {
				out = append(out, stmt)
			}
			start = i + 1
		}
		i++
	}
	if stmt := strings.TrimSpace(s[start:]); hasCode(stmt) {
		out = append(out, stmt)
	}
	return out
}

func skipQuoted(s string, i int, q byte) int {
	for j := i + 1; j < len(s); j++ {
		if s[j] == q {
			if j+1 < len(s) && s[j+1] == q {
				j++
				continue
			}
			return j + 1
		}
	}
	return len(s)
}

// stripComments removes leading comments and whitespace.
func stripComments(s string) string {
	for {
		s = strings.TrimSpace(s)
		switch {
		case strings.HasPrefix(s, "--"):
			if j := strings.IndexByte(s, '\n'); j >= 0 {
				s = s[j+1:]
			} else {
				return ""
			}
		case strings.HasPrefix(s, "/*"):
			if j := strings.Index(s, "*/"); j >= 0 {
				s = s[j+2:]
			} else {
				return ""
			}
		default:
			return s
		}
	}
}

func hasCode(stmt string) bool { return stripComments(stmt) != "" }

func firstKeyword(stmt string) string {
	s := stripComments(stmt)
	end := 0
	for end < len(s) && (s[end] >= 'a' && s[end] <= 'z' || s[end] >= 'A' && s[end] <= 'Z') {
		end++
	}
	return strings.ToUpper(s[:end])
}
