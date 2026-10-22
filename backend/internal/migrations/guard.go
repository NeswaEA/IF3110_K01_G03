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
	for _, raw := range tree.Stmts {
		switch n := raw.Stmt.Node.(type) {
		case *pgquery.Node_SelectStmt:
			if n.SelectStmt.IntoClause != nil {
				return nil, unsupported()
			}
		case *pgquery.Node_InsertStmt, *pgquery.Node_UpdateStmt, *pgquery.Node_DeleteStmt:
		case *pgquery.Node_CreateStmt:
			if n.CreateStmt.Relation.Relpersistence == "t" {
				return nil, unsupported()
			}
		case *pgquery.Node_IndexStmt:
			if n.IndexStmt.Concurrent {
				return nil, unsupported()
			}
		case *pgquery.Node_DropStmt:
			if n.DropStmt.Concurrent || (n.DropStmt.RemoveType != pgquery.ObjectType_OBJECT_TABLE && n.DropStmt.RemoveType != pgquery.ObjectType_OBJECT_INDEX) {
				return nil, unsupported()
			}
		case *pgquery.Node_AlterTableStmt:
			for _, cmd := range n.AlterTableStmt.Cmds {
				switch cmd.GetAlterTableCmd().Subtype.String() {
				case "AT_AddColumn", "AT_DropColumn", "AT_AlterColumnType", "AT_ColumnDefault", "AT_DropNotNull", "AT_SetNotNull", "AT_AddConstraint", "AT_DropConstraint", "AT_ValidateConstraint":
				default:
					return nil, unsupported()
				}
			}
		case *pgquery.Node_RenameStmt:
			switch n.RenameStmt.RenameType {
			case pgquery.ObjectType_OBJECT_TABLE, pgquery.ObjectType_OBJECT_COLUMN, pgquery.ObjectType_OBJECT_INDEX, pgquery.ObjectType_OBJECT_TABCONSTRAINT:
			default:
				return nil, unsupported()
			}
		default:
			return nil, unsupported()
		}
	}
	raw, err := pgquery.ParseToJSON(query)
	if err != nil {
		return nil, unsupported()
	}
	var ast any
	if err := json.Unmarshal([]byte(raw), &ast); err != nil {
		return nil, internal(err)
	}
	if forbiddenAST(ast) {
		return nil, unsupported()
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

func forbiddenAST(value any) bool {
	switch v := value.(type) {
	case map[string]any:
		if v["schemaname"] == "_platform" || v["catalogname"] != nil && v["catalogname"] != "" {
			return true
		}
		if _, ok := v["ParamRef"]; ok {
			return true
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
			if forbiddenAST(child) {
				return true
			}
		}
	case []any:
		for i, child := range v {
			if i < len(v)-1 && astString(child) == "_platform" {
				return true
			}
			if forbiddenAST(child) {
				return true
			}
		}
	}
	return false
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
		deny := func() int { denied = true; return sqlite3.SQLITE_DENY }
		if strings.HasPrefix(strings.ToLower(a1), "_platform_") || (op == sqlite3.SQLITE_ALTER_TABLE && strings.HasPrefix(strings.ToLower(a2), "_platform_")) {
			return deny()
		}
		switch op {
		case sqlite3.SQLITE_CREATE_TABLE, sqlite3.SQLITE_DROP_TABLE, sqlite3.SQLITE_CREATE_INDEX, sqlite3.SQLITE_DROP_INDEX, sqlite3.SQLITE_ALTER_TABLE,
			sqlite3.SQLITE_READ, sqlite3.SQLITE_SELECT, sqlite3.SQLITE_INSERT, sqlite3.SQLITE_UPDATE, sqlite3.SQLITE_DELETE, sqlite3.SQLITE_REINDEX, 33:
			return sqlite3.SQLITE_OK
		case sqlite3.SQLITE_FUNCTION:
			switch strings.ToLower(a2) {
			case "load_extension", "writefile", "readfile", "fts3_tokenizer":
				return deny()
			}
			return sqlite3.SQLITE_OK
		}
		return deny()
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
	if execErr != nil {
		return userError(ctx, execErr)
	}
	return nil
}

func sqliteReservedName(query string) bool {
	previous, before := "", ""
	for i := 0; i < len(query); {
		if i+1 < len(query) && query[i:i+2] == "--" {
			if end := strings.IndexByte(query[i:], '\n'); end >= 0 {
				i += end + 1
				continue
			}
			break
		}
		if i+1 < len(query) && query[i:i+2] == "/*" {
			if end := strings.Index(query[i+2:], "*/"); end >= 0 {
				i += end + 4
				continue
			}
			break
		}
		quote := query[i]
		if quote == '\'' || quote == '"' || quote == '`' || quote == '[' {
			endQuote := quote
			if quote == '[' {
				endQuote = ']'
			}
			i++
			var token strings.Builder
			for i < len(query) {
				if query[i] == endQuote {
					i++
					if i < len(query) && query[i] == endQuote && quote != '[' {
						token.WriteByte(endQuote)
						i++
						continue
					}
					break
				}
				token.WriteByte(query[i])
				i++
			}
			if (quote != '\'' || before == "RENAME" && previous == "TO") && strings.HasPrefix(strings.ToLower(token.String()), "_platform_") {
				return true
			}
			before, previous = previous, strings.ToUpper(token.String())
			continue
		}
		start := i
		for i < len(query) && (query[i] == '_' || query[i] >= 'a' && query[i] <= 'z' || query[i] >= 'A' && query[i] <= 'Z' || query[i] >= '0' && query[i] <= '9' || query[i] >= 128) {
			i++
		}
		if i > start {
			token := query[start:i]
			if strings.HasPrefix(strings.ToLower(token), "_platform_") {
				return true
			}
			before, previous = previous, strings.ToUpper(token)
		} else {
			i++
		}
	}
	return false
}
