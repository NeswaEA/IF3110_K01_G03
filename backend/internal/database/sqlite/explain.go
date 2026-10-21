package sqlite

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"strconv"
	"strings"

	sqlite3 "github.com/mattn/go-sqlite3"

	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/database"
)

// Planner runs EXPLAIN QUERY PLAN. The explain package has already checked
// that the input is one plannable statement; as defence in depth the plan runs
// in a transaction that is always rolled back, under an authorizer that denies
// DDL, ATTACH, transaction control and non-read PRAGMAs. DML must stay allowed
// because preparing EXPLAIN QUERY PLAN INSERT/UPDATE/DELETE checks those actions.
type Planner struct{}

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

func queryPlan(ctx context.Context, tx *sql.Tx, stmt string) (database.PlanResult, error) {
	rows, err := tx.QueryContext(ctx, "EXPLAIN QUERY PLAN "+stmt)
	if err != nil {
		return database.PlanResult{}, engineErr(ctx, err)
	}
	defer rows.Close()
	res := database.PlanResult{Driver: database.SQLite, Mode: "estimate", Nodes: []database.PlanNode{},
		Raw: []any{}, Warnings: []string{"SQLite tidak menyediakan estimasi biaya atau jumlah baris."}}
	raw := []any{}
	for rows.Next() {
		var id, parent, notused int64
		var detail string
		if err := rows.Scan(&id, &parent, &notused, &detail); err != nil {
			return database.PlanResult{}, engineErr(ctx, err)
		}
		raw = append(raw, map[string]any{"id": id, "parent": parent, "notused": notused, "detail": detail})
		n := database.PlanNode{ID: strconv.FormatInt(id, 10), Operation: detail}
		if parent != 0 { // 0 is the virtual root, not a node
			p := strconv.FormatInt(parent, 10)
			n.ParentID = &p
		}
		n.Relation, n.Index = describe(detail)
		res.Nodes = append(res.Nodes, n)
	}
	if err := rows.Err(); err != nil {
		return database.PlanResult{}, engineErr(ctx, err)
	}
	res.Raw = raw
	return res, nil
}

// describe extracts the table and index from details such as
// "SEARCH items USING INDEX items_name (name=?)" or "SCAN orders".
func describe(detail string) (relation, index *string) {
	f := strings.Fields(detail)
	if len(f) >= 2 && (f[0] == "SCAN" || f[0] == "SEARCH") {
		r := f[1]
		relation = &r
	}
	for i := 0; i+1 < len(f); i++ {
		if f[i] == "INDEX" && (i == 0 || f[i-1] == "USING" || f[i-1] == "COVERING") {
			ix := f[i+1]
			index = &ix
			break
		}
	}
	return relation, index
}

func engineErr(ctx context.Context, err error) error {
	var se sqlite3.Error
	if errors.As(err, &se) && se.Code != sqlite3.ErrBusy && se.Code != sqlite3.ErrLocked {
		return &database.EngineError{Message: se.Error()}
	}
	return database.ClassifyTargetError(ctx, err)
}
