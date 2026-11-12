package migrations

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"strings"

	sqlite3 "github.com/mattn/go-sqlite3"
	pgquery "github.com/pganalyze/pg_query_go/v6"
)

func unsupported() error {
	return problem("UNSUPPORTED_SQL", "SQL memuat operasi di luar migrasi transaksional yang didukung.")
}

func postgresStatements(query string) ([]string, error) {
	tree, err := pgquery.Parse(query)
	if err != nil {
		return nil, problem("SQL_EXECUTION_FAILED", "Sintaks SQL PostgreSQL tidak valid.")
	}
	if len(tree.Stmts) == 0 {
		return nil, problem("INVALID_REQUEST", "SQL tidak memuat statement.")
	}
	ast, err := postgresJSONStatements(query)
	if err != nil {
		return nil, err
	}
	for i, raw := range tree.Stmts {
		params := make(map[int]bool)
		if !postgresStatementAllowed(raw.Stmt) || inspectPostgresAST(ast[i].Stmt, params) || len(params) != 0 {
			return nil, unsupported()
		}
	}
	statements := make([]string, 0, len(tree.Stmts))
	for _, raw := range tree.Stmts {
		start, end := int(raw.StmtLocation), len(query)
		if raw.StmtLen > 0 {
			end = start + int(raw.StmtLen)
		}
		statements = append(statements, query[start:end])
	}
	return statements, nil
}

func postgresStatementAllowed(statement *pgquery.Node) bool {
	switch n := statement.Node.(type) {
	case *pgquery.Node_SelectStmt:
		if n.SelectStmt.IntoClause != nil {
			return false
		}
	case *pgquery.Node_InsertStmt, *pgquery.Node_UpdateStmt, *pgquery.Node_DeleteStmt:
	case *pgquery.Node_CreateStmt:
		if n.CreateStmt.Relation.Relpersistence == "t" {
			return false
		}
	case *pgquery.Node_IndexStmt:
		if n.IndexStmt.Concurrent {
			return false
		}
	case *pgquery.Node_DropStmt:
		if n.DropStmt.Concurrent || (n.DropStmt.RemoveType != pgquery.ObjectType_OBJECT_TABLE && n.DropStmt.RemoveType != pgquery.ObjectType_OBJECT_INDEX) {
			return false
		}
	case *pgquery.Node_AlterTableStmt:
		for _, cmd := range n.AlterTableStmt.Cmds {
			switch cmd.GetAlterTableCmd().Subtype.String() {
			case "AT_AddColumn", "AT_DropColumn", "AT_AlterColumnType", "AT_ColumnDefault", "AT_DropNotNull", "AT_SetNotNull", "AT_AddConstraint", "AT_DropConstraint", "AT_ValidateConstraint":
			default:
				return false
			}
		}
	case *pgquery.Node_RenameStmt:
		switch n.RenameStmt.RenameType {
		case pgquery.ObjectType_OBJECT_TABLE, pgquery.ObjectType_OBJECT_COLUMN, pgquery.ObjectType_OBJECT_INDEX, pgquery.ObjectType_OBJECT_TABCONSTRAINT:
		default:
			return false
		}
	default:
		return false
	}
	return true
}

type postgresJSONStatement struct {
	Stmt map[string]any `json:"stmt"`
}

func postgresJSONStatements(query string) ([]postgresJSONStatement, error) {
	raw, err := pgquery.ParseToJSON(query)
	if err != nil {
		return nil, unsupported()
	}
	var ast struct {
		Stmts []postgresJSONStatement `json:"stmts"`
	}
	if err := json.Unmarshal([]byte(raw), &ast); err != nil {
		return nil, internal(err)
	}
	return ast.Stmts, nil
}

func inspectPostgresAST(value any, params map[int]bool) (forbidden bool) {
	switch v := value.(type) {
	case map[string]any:
		if v["schemaname"] == "_platform" || v["catalogname"] != nil && v["catalogname"] != "" {
			return true
		}
		if param, ok := v["ParamRef"].(map[string]any); ok {
			if number, ok := param["number"].(float64); ok {
				params[int(number)] = true
			}
		}
		if f, ok := v["FuncCall"].(map[string]any); ok {
			if parts, ok := f["funcname"].([]any); ok && len(parts) > 0 {
				name := astString(parts[len(parts)-1])
				if name == "set_config" || name == "lo_import" || name == "lo_export" || strings.HasPrefix(name, "dblink") || strings.HasPrefix(name, "pg_read_") || strings.HasPrefix(name, "pg_write_") {
					return true
				}
			}
		}
		for _, child := range v {
			forbidden = inspectPostgresAST(child, params) || forbidden
		}
	case []any:
		for i, child := range v {
			if i < len(v)-1 && astString(child) == "_platform" {
				return true
			}
			forbidden = inspectPostgresAST(child, params) || forbidden
		}
	}
	return forbidden
}

func astString(v any) string {
	if m, ok := v.(map[string]any); ok {
		if s, ok := m["String"].(map[string]any); ok {
			if name, ok := s["sval"].(string); ok {
				return name
			}
		}
	}
	return ""
}

func sqliteUserSQL(ctx context.Context, conn *sql.Conn, query string) error {
	if sqliteReservedName(query) {
		return unsupported()
	}
	denied := false
	action := false
	set := func(cb func(int, string, string, string) int) error {
		return conn.Raw(func(dc any) error {
			c, ok := dc.(*sqlite3.SQLiteConn)
			if !ok {
				return errors.New("unexpected SQLite driver")
			}
			c.RegisterAuthorizer(cb)
			return nil
		})
	}
	err := set(func(op int, a1, a2, a3 string) int {
		action = true
		result := migrationAuthorizer(op, a1, a2, a3)
		denied = denied || result == sqlite3.SQLITE_DENY
		return result
	})
	if err != nil {
		return internal(err)
	}
	_, execErr := conn.ExecContext(ctx, query)
	if err := set(nil); err != nil {
		_ = conn.Raw(func(any) error { return driver.ErrBadConn })
		return internal(err)
	}
	if denied {
		return unsupported()
	}
	if execErr == nil && !action {
		return problem("INVALID_REQUEST", "SQL tidak memuat statement.")
	}
	return execErr
}

func migrationAuthorizer(op int, a1, a2, _ string) int {
	if strings.HasPrefix(strings.ToLower(a1), "_platform_") || (op == sqlite3.SQLITE_ALTER_TABLE && strings.HasPrefix(strings.ToLower(a2), "_platform_")) {
		return sqlite3.SQLITE_DENY
	}
	switch op {
	case sqlite3.SQLITE_CREATE_TABLE, sqlite3.SQLITE_DROP_TABLE, sqlite3.SQLITE_CREATE_INDEX, sqlite3.SQLITE_DROP_INDEX, sqlite3.SQLITE_ALTER_TABLE,
		sqlite3.SQLITE_READ, sqlite3.SQLITE_SELECT, sqlite3.SQLITE_INSERT, sqlite3.SQLITE_UPDATE, sqlite3.SQLITE_DELETE, sqlite3.SQLITE_REINDEX, 33:
		return sqlite3.SQLITE_OK
	case sqlite3.SQLITE_FUNCTION:
		switch strings.ToLower(a2) {
		case "load_extension", "writefile", "readfile", "fts3_tokenizer":
			return sqlite3.SQLITE_DENY
		}
		return sqlite3.SQLITE_OK
	}
	return sqlite3.SQLITE_DENY
}
