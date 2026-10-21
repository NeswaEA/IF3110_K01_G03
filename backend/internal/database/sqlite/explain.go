package sqlite

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"unicode"

	sqlite3 "github.com/mattn/go-sqlite3"

	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/database"
)

// Planner runs EXPLAIN QUERY PLAN. The explain package has already classified
// the input with Classify; as defence in depth the plan runs in a transaction
// that is always rolled back, under an authorizer that denies DDL, ATTACH,
// transaction control and non-read PRAGMAs. DML must stay allowed because
// preparing EXPLAIN QUERY PLAN INSERT/UPDATE/DELETE checks those actions.
type Planner struct{}

// PlanWarning is the fixed warning on every SQLite plan.
const PlanWarning = "SQLite tidak menyediakan estimasi cost atau jumlah row pada EXPLAIN QUERY PLAN."

// sqliteRecursive is SQLITE_RECURSIVE (WITH RECURSIVE), which go-sqlite3 does not export.
const sqliteRecursive = 33

func explainAuthorizer(op int, a1, a2, a3 string) int {
	switch op {
	case sqlite3.SQLITE_READ, sqlite3.SQLITE_SELECT, sqlite3.SQLITE_FUNCTION, sqliteRecursive,
		sqlite3.SQLITE_INSERT, sqlite3.SQLITE_UPDATE, sqlite3.SQLITE_DELETE:
		return sqlite3.SQLITE_OK
	case sqlite3.SQLITE_PRAGMA:
		if readOnlyPragmas[strings.ToLower(a1)] {
			return sqlite3.SQLITE_OK
		}
	}
	return sqlite3.SQLITE_DENY
}

// StatementKind is what Classify decided about the input.
type StatementKind int

const (
	Plannable   StatementKind = iota // SELECT (incl. WITH, VALUES), INSERT/REPLACE, UPDATE, DELETE
	NoPlan                           // DDL: CREATE, ALTER, DROP, REINDEX
	Unsupported                      // anything else, or a reserved _platform_ table
	Multiple                         // the engine left another statement after the first
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

// Classify prepares (never steps) the first statement of query on a pinned
// connection while an authorizer records what it would do, then asks the
// engine what text is left after it. It returns the first statement's text.
// Engine rejections (syntax, missing table) are EngineError.
func Classify(ctx context.Context, db *sql.DB, query string) (string, StatementKind, error) {
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
		return "", Unsupported, nil
	case err != nil:
		return "", 0, engineErr(ctx, err)
	case tail != "" && !commentOnly(tail):
		return "", Multiple, nil
	case params > 0:
		// the driver cannot run it without arguments; report it as the engine's rejection
		return "", 0, &database.EngineError{Message: "parameter placeholder tanpa nilai tidak didukung"}
	}

	first := strings.TrimSuffix(strings.TrimRightFunc(query, unicode.IsSpace), tail)
	switch firstWord(first) {
	case "EXPLAIN", "VACUUM": // neither raises a distinguishing authorizer code
		return "", Unsupported, nil
	}
	if len(actions) == 0 {
		return "", Unsupported, nil
	}
	for op := range actions {
		if ddlActions[op] {
			return "", NoPlan, nil
		}
	}
	for op := range actions {
		if !planActions[op] {
			return "", Unsupported, nil // transaction control, PRAGMA, ATTACH/DETACH, ANALYZE, ...
		}
	}
	return first, Plannable, nil
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

func (Planner) Explain(ctx context.Context, t database.TargetHandle, stmt string) (database.PlanResult, error) {
	conn, err := t.DB.Conn(ctx)
	if err != nil {
		return database.PlanResult{}, database.ClassifyTargetError(ctx, err)
	}
	defer conn.Close()
	tx, err := conn.BeginTx(ctx, nil) // BEGIN before the authorizer, which denies transaction control
	if err != nil {
		return database.PlanResult{}, database.ClassifyTargetError(ctx, err)
	}
	defer tx.Rollback()

	set := func(cb func(int, string, string, string) int) error {
		return conn.Raw(func(dc any) error {
			c, ok := dc.(*sqlite3.SQLiteConn)
			if !ok {
				return fmt.Errorf("sqlite: unexpected driver connection %T", dc)
			}
			c.RegisterAuthorizer(cb)
			return nil
		})
	}
	if err := set(explainAuthorizer); err != nil {
		return database.PlanResult{}, err
	}
	res, qerr := queryPlan(ctx, tx, stmt)
	if err := set(nil); err != nil {
		_ = conn.Raw(func(any) error { return driver.ErrBadConn })
	}
	return res, qerr
}

// queryPlan returns an empty Nodes slice when the engine gives no plan rows;
// the explain package reports that as PLAN_UNSUPPORTED.
func queryPlan(ctx context.Context, tx *sql.Tx, stmt string) (database.PlanResult, error) {
	rows, err := tx.QueryContext(ctx, "EXPLAIN QUERY PLAN "+stmt)
	if err != nil {
		return database.PlanResult{}, engineErr(ctx, err)
	}
	defer rows.Close()
	res := database.PlanResult{Driver: database.SQLite, Mode: "estimate", Nodes: []database.PlanNode{},
		Warnings: []string{PlanWarning}}
	raw := []any{}
	for rows.Next() {
		var id, parent, notused int64
		var detail string
		if err := rows.Scan(&id, &parent, &notused, &detail); err != nil {
			return database.PlanResult{}, engineErr(ctx, err)
		}
		raw = append(raw, map[string]any{"id": id, "parent": parent, "notused": notused, "detail": detail})
		n := database.PlanNode{ID: strconv.FormatInt(id, 10)}
		if parent != 0 { // 0 is the virtual root, not a node
			p := strconv.FormatInt(parent, 10)
			n.ParentID = &p
		}
		n.Operation, n.Relation, n.Index = describe(detail)
		res.Nodes = append(res.Nodes, n)
	}
	if err := rows.Err(); err != nil {
		return database.PlanResult{}, engineErr(ctx, err)
	}
	res.Raw = raw
	return res, nil
}

// describe normalises a detail such as "SEARCH items USING INDEX items_name (name=?)"
// into operation SEARCH, relation items, index items_name. SQLite prints the
// alias instead of the table when the query gives one ("SCAN o"), and relation
// is whatever it printed. Details that are not
// SCAN/SEARCH keep their full text as the operation. INTEGER PRIMARY KEY and
// AUTOMATIC indexes are not named indexes, so index stays nil.
func describe(detail string) (operation string, relation, index *string) {
	f := strings.Fields(detail)
	if len(f) < 2 || (f[0] != "SCAN" && f[0] != "SEARCH") {
		return detail, nil, nil
	}
	r := f[1]
	relation = &r
	for i := 2; i+1 < len(f); i++ {
		if f[i] != "USING" {
			continue
		}
		switch {
		case f[i+1] == "INDEX" && i+2 < len(f):
			ix := f[i+2]
			index = &ix
		case f[i+1] == "COVERING" && i+3 < len(f) && f[i+2] == "INDEX":
			ix := f[i+3]
			index = &ix
		}
		break
	}
	return f[0], relation, index
}

func engineErr(ctx context.Context, err error) error {
	var se sqlite3.Error
	if errors.As(err, &se) && se.Code != sqlite3.ErrBusy && se.Code != sqlite3.ErrLocked {
		return &database.EngineError{Message: se.Error()}
	}
	return database.ClassifyTargetError(ctx, err)
}
