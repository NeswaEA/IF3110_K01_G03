package explain

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"unicode"

	sqlite3 "github.com/mattn/go-sqlite3"

	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/database"
)

// SQLite has no AST parser, so statement kind comes from the authorizer at
// prepare time and the statement boundary from the engine's tail. This is
// explain policy: it only uses the driver, never the target adapter.

// sqliteRecursive is SQLITE_RECURSIVE (WITH RECURSIVE), which go-sqlite3 does not export.
const sqliteRecursive = 33

func sqliteEngineErr(ctx context.Context, err error) error {
	var se sqlite3.Error
	if errors.As(err, &se) && se.Code != sqlite3.ErrBusy && se.Code != sqlite3.ErrLocked {
		return &database.EngineError{Message: se.Error()}
	}
	return database.ClassifyTargetError(ctx, err)
}

// sqliteKind is what sqliteClassify decided about the input.
type sqliteKind int

const (
	sqlitePlannable   sqliteKind = iota // SELECT (incl. WITH, VALUES), INSERT/REPLACE, UPDATE, DELETE
	sqliteNoPlan                        // DDL: CREATE, ALTER, DROP, REINDEX
	sqliteUnsupported                   // anything else, or a reserved _platform_ table
	sqliteMultiple                      // the engine left another statement after the first
)

// ddlActions are the authorizer codes of statements that are valid but have no plan.
var ddlActions = map[int]bool{
	sqlite3.SQLITE_CREATE_INDEX: true, sqlite3.SQLITE_CREATE_TABLE: true, sqlite3.SQLITE_CREATE_TEMP_INDEX: true,
	sqlite3.SQLITE_CREATE_TEMP_TABLE: true, sqlite3.SQLITE_CREATE_TEMP_TRIGGER: true, sqlite3.SQLITE_CREATE_TEMP_VIEW: true,
	sqlite3.SQLITE_CREATE_TRIGGER: true, sqlite3.SQLITE_CREATE_VIEW: true, sqlite3.SQLITE_CREATE_VTABLE: true,
	sqlite3.SQLITE_DROP_INDEX: true, sqlite3.SQLITE_DROP_TABLE: true, sqlite3.SQLITE_DROP_TEMP_INDEX: true,
	sqlite3.SQLITE_DROP_TEMP_TABLE: true, sqlite3.SQLITE_DROP_TEMP_TRIGGER: true, sqlite3.SQLITE_DROP_TEMP_VIEW: true,
	sqlite3.SQLITE_DROP_TRIGGER: true, sqlite3.SQLITE_DROP_VIEW: true, sqlite3.SQLITE_DROP_VTABLE: true,
	sqlite3.SQLITE_ALTER_TABLE: true, sqlite3.SQLITE_REINDEX: true,
}

// planActions are the only codes a plannable statement may raise.
var planActions = map[int]bool{
	sqlite3.SQLITE_READ: true, sqlite3.SQLITE_SELECT: true, sqlite3.SQLITE_FUNCTION: true, sqliteRecursive: true,
	sqlite3.SQLITE_INSERT: true, sqlite3.SQLITE_UPDATE: true, sqlite3.SQLITE_DELETE: true,
}

// sqliteClassify prepares (never steps) the first statement of query on a pinned
// connection while an authorizer records what it would do, then asks the
// engine what text is left after it. It returns the first statement's text.
// Engine rejections (syntax, missing table) are EngineError.
func sqliteClassify(ctx context.Context, db *sql.DB, query string) (string, sqliteKind, error) {
	conn, err := db.Conn(ctx)
	if err != nil {
		return "", 0, database.ClassifyTargetError(ctx, err)
	}
	defer conn.Close()

	var tail string
	var params int
	actions := map[int]bool{}
	reserved := false
	err = conn.Raw(func(dc any) error {
		c, ok := dc.(*sqlite3.SQLiteConn)
		if !ok {
			return fmt.Errorf("sqlite: unexpected driver connection %T", dc)
		}
		c.RegisterAuthorizer(func(op int, a1, a2, a3 string) int {
			actions[op] = true
			if strings.HasPrefix(strings.ToLower(a1), "_platform_") {
				reserved = true
				return sqlite3.SQLITE_DENY
			}
			return sqlite3.SQLITE_OK
		})
		defer c.RegisterAuthorizer(nil)
		st, err := c.Prepare(query)
		if err != nil {
			return err
		}
		defer st.Close()
		params = st.NumInput()
		tail, err = stmtTail(st)
		return err
	})
	switch {
	case reserved:
		return "", sqliteUnsupported, nil
	case err != nil:
		return "", 0, sqliteEngineErr(ctx, err)
	case tail != "" && !commentOnly(tail):
		return "", sqliteMultiple, nil
	case params > 0:
		// the driver cannot run it without arguments; report it as the engine's rejection
		return "", 0, &database.EngineError{Message: "parameter placeholder tanpa nilai tidak didukung"}
	}

	first := strings.TrimSuffix(strings.TrimRightFunc(query, unicode.IsSpace), tail)
	switch firstWord(first) {
	case "EXPLAIN", "VACUUM": // neither raises a distinguishing authorizer code
		return "", sqliteUnsupported, nil
	}
	if len(actions) == 0 {
		return "", sqliteUnsupported, nil
	}
	for op := range actions {
		if ddlActions[op] {
			return "", sqliteNoPlan, nil
		}
	}
	for op := range actions {
		if !planActions[op] {
			return "", sqliteUnsupported, nil // transaction control, PRAGMA, ATTACH/DETACH, ANALYZE, ...
		}
	}
	return first, sqlitePlannable, nil
}

// stmtTail returns the text SQLite left unparsed after the first statement.
// shortcut: go-sqlite3 keeps the engine's tail in an unexported field; this
// reads it by reflection (pinned v1.14.52, covered by tests). Upgrade to a
// cgo sqlite3_prepare_v2 call if the driver changes.
func stmtTail(st driver.Stmt) (string, error) {
	v := reflect.ValueOf(st)
	if v.Kind() == reflect.Pointer {
		v = v.Elem()
	}
	if v.Kind() == reflect.Struct {
		if f := v.FieldByName("t"); f.IsValid() && f.Kind() == reflect.String {
			return f.String(), nil
		}
	}
	return "", errors.New("sqlite: statement tail unavailable from driver")
}

// commentOnly reports whether s holds only whitespace and comments.
func commentOnly(s string) bool {
	for {
		s = strings.TrimSpace(s)
		switch {
		case s == "":
			return true
		case strings.HasPrefix(s, "--"):
			j := strings.IndexByte(s, '\n')
			if j < 0 {
				return true
			}
			s = s[j+1:]
		case strings.HasPrefix(s, "/*"):
			j := strings.Index(s, "*/")
			if j < 0 {
				return true // SQLite accepts an unterminated trailing block comment
			}
			s = s[j+2:]
		default:
			return false
		}
	}
}

// firstWord returns the leading keyword after comments, upper-cased.
func firstWord(s string) string {
	for {
		s = strings.TrimSpace(s)
		switch {
		case strings.HasPrefix(s, "--"):
			j := strings.IndexByte(s, '\n')
			if j < 0 {
				return ""
			}
			s = s[j+1:]
		case strings.HasPrefix(s, "/*"):
			j := strings.Index(s, "*/")
			if j < 0 {
				return ""
			}
			s = s[j+2:]
		default:
			end := strings.IndexFunc(s, func(r rune) bool { return !unicode.IsLetter(r) })
			if end < 0 {
				end = len(s)
			}
			return strings.ToUpper(s[:end])
		}
	}
}
