// Package sqlite is the SQLite target adapter: PRAGMA-based inspector and the
// read-only table reader. Viewer reads run on a pinned connection with a
// read-only authorizer that is removed before the connection returns to the pool.
package sqlite

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"sort"
	"strings"

	sqlite3 "github.com/mattn/go-sqlite3"

	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/database"
)

// SchemaName is the only logical schema exposed for SQLite (ATTACH is not supported).
const SchemaName = "main"

type Inspector struct{}

var _ database.Inspector = Inspector{}

// readOnlyPragmas are the PRAGMAs the inspector and reader need.
var readOnlyPragmas = map[string]bool{
	"table_xinfo": true, "table_info": true, "foreign_key_list": true,
	"index_list": true, "index_info": true, "index_xinfo": true,
}

// readOnlyAuthorizer allows reads, read-only PRAGMAs, functions and
// BEGIN/ROLLBACK; everything else (writes, DDL, ATTACH, other PRAGMAs) is denied.
func readOnlyAuthorizer(op int, a1, a2, a3 string) int {
	switch op {
	case sqlite3.SQLITE_READ, sqlite3.SQLITE_SELECT, sqlite3.SQLITE_FUNCTION, sqlite3.SQLITE_TRANSACTION:
		return sqlite3.SQLITE_OK
	case sqlite3.SQLITE_PRAGMA:
		if readOnlyPragmas[strings.ToLower(a1)] {
			return sqlite3.SQLITE_OK
		}
	}
	return sqlite3.SQLITE_DENY
}

// withReadOnlyConn pins one connection, installs the authorizer, runs fn
// inside a deferred (read) transaction, then rolls back and clears the
// authorizer so the pooled connection is unchanged for the next user.
func withReadOnlyConn(ctx context.Context, db *sql.DB, fn func(tx *sql.Tx) error) error {
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
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
	if err := set(readOnlyAuthorizer); err != nil {
		return err
	}
	defer func() {
		if err := set(nil); err != nil {
			_ = conn.Raw(func(any) error { return driver.ErrBadConn }) // discard the connection rather than reuse it with an authorizer
		}
	}()
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	return fn(tx)
}

func (Inspector) Inspect(ctx context.Context, t database.TargetHandle) (database.SchemaSnapshot, error) {
	var snap database.SchemaSnapshot
	err := withReadOnlyConn(ctx, t.DB, func(tx *sql.Tx) error {
		var err error
		snap, err = Inspector{}.InspectWith(ctx, t, tx)
		return err
	})
	if err != nil {
		return database.SchemaSnapshot{}, database.ClassifyTargetError(ctx, err)
	}
	return snap, nil
}

// InspectWith uses the caller's connection or transaction (the runner's, for
// fingerprint checks) and does not touch its authorizer or transaction.
func (Inspector) InspectWith(ctx context.Context, t database.TargetHandle, q database.SQLExecutor) (database.SchemaSnapshot, error) {
	tables, err := inspect(ctx, q)
	if err != nil {
		return database.SchemaSnapshot{}, database.ClassifyTargetError(ctx, err)
	}
	snap := database.NormalizeSnapshot(database.SchemaSnapshot{
		DatabaseID: t.DatabaseID,
		Driver:     database.SQLite,
		Schemas:    []database.Schema{{Name: SchemaName, Tables: tables}},
	})
	if snap.Fingerprint, err = database.Fingerprint(snap); err != nil {
		return database.SchemaSnapshot{}, err
	}
	return snap, nil
}

type tableInfo struct {
	name, sql    string
	cols         []database.Column
	pk           []string // in PRIMARY KEY order
	integerRowPK bool     // single INTEGER PRIMARY KEY (rowid alias, implicitly NOT NULL)
}

func listTables(ctx context.Context, q database.SQLExecutor) ([]tableInfo, error) {
	rows, err := q.QueryContext(ctx, `
		SELECT name, COALESCE(sql, '') FROM sqlite_schema
		 WHERE type = 'table' AND name NOT LIKE 'sqlite\_%' ESCAPE '\'
		 ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []tableInfo
	for rows.Next() {
		var t tableInfo
		if err := rows.Scan(&t.name, &t.sql); err != nil {
			return nil, err
		}
		if !database.Reserved(SchemaName, t.name) {
			out = append(out, t)
		}
	}
	return out, rows.Err()
}

func loadColumns(ctx context.Context, q database.SQLExecutor, t *tableInfo) error {
	rows, err := q.QueryContext(ctx,
		`SELECT name, type, "notnull", dflt_value, pk, hidden FROM pragma_table_xinfo(?) ORDER BY cid`, t.name)
	if err != nil {
		return err
	}
	defer rows.Close()
	type pkCol struct {
		pos  int
		name string
		typ  string
	}
	var pks []pkCol
	for rows.Next() {
		var c database.Column
		var notNull, pk, hidden int
		var def sql.NullString
		if err := rows.Scan(&c.Name, &c.DataType, &notNull, &def, &pk, &hidden); err != nil {
			return err
		}
		if hidden == 1 { // hidden column of a virtual table
			continue
		}
		c.Nullable = notNull == 0
		if def.Valid {
			c.Default = &def.String
		}
		c.Ordinal = len(t.cols) + 1
		t.cols = append(t.cols, c)
		if pk > 0 {
			pks = append(pks, pkCol{pk, c.Name, c.DataType})
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	sort.Slice(pks, func(i, j int) bool { return pks[i].pos < pks[j].pos })
	for _, p := range pks {
		t.pk = append(t.pk, p.name)
	}
	if len(pks) == 1 && strings.EqualFold(pks[0].typ, "INTEGER") {
		t.integerRowPK = true
		for i := range t.cols {
			if t.cols[i].Name == pks[0].name {
				t.cols[i].Nullable = false
			}
		}
	}
	return nil
}

func inspect(ctx context.Context, q database.SQLExecutor) ([]database.Table, error) {
	infos, err := listTables(ctx, q)
	if err != nil {
		return nil, err
	}
	pkOf := map[string][]string{}
	for i := range infos {
		if err := loadColumns(ctx, q, &infos[i]); err != nil {
			return nil, err
		}
		pkOf[infos[i].name] = infos[i].pk
	}

	tables := make([]database.Table, 0, len(infos))
	for _, info := range infos {
		t := database.Table{Name: info.name, Columns: info.cols}
		if len(info.pk) > 0 {
			t.Constraints = append(t.Constraints, database.Constraint{
				Kind: database.PrimaryKey, Columns: info.pk,
				Definition: "PRIMARY KEY (" + quoteList(info.pk) + ")",
			})
		}
		fks, err := loadForeignKeys(ctx, q, info.name, pkOf)
		if err != nil {
			return nil, err
		}
		t.Constraints = append(t.Constraints, fks...)

		uniques, indexes, err := loadIndexes(ctx, q, info.name)
		if err != nil {
			return nil, err
		}
		t.Constraints = append(t.Constraints, uniques...)
		t.Constraints = append(t.Constraints, parseChecks(info.sql)...)
		t.Indexes = indexes
		tables = append(tables, t)
	}
	return tables, nil
}

func loadForeignKeys(ctx context.Context, q database.SQLExecutor, table string, pkOf map[string][]string) ([]database.Constraint, error) {
	rows, err := q.QueryContext(ctx,
		`SELECT id, "table", "from", "to", on_update, on_delete FROM pragma_foreign_key_list(?) ORDER BY id, seq`, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []database.Constraint
	byID := map[int]int{}
	for rows.Next() {
		var id int
		var refTable, from, onUpd, onDel string
		var to sql.NullString
		if err := rows.Scan(&id, &refTable, &from, &to, &onUpd, &onDel); err != nil {
			return nil, err
		}
		i, ok := byID[id]
		if !ok {
			out = append(out, database.Constraint{Kind: database.ForeignKey,
				References: &database.ForeignReference{Schema: SchemaName, Table: refTable,
					OnUpdate: strings.ToUpper(onUpd), OnDelete: strings.ToUpper(onDel)}})
			i = len(out) - 1
			byID[id] = i
		}
		c := &out[i]
		c.Columns = append(c.Columns, from)
		if to.Valid && to.String != "" {
			c.References.Columns = append(c.References.Columns, to.String)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		c := &out[i]
		if len(c.References.Columns) == 0 { // REFERENCES t without columns targets t's primary key
			c.References.Columns = append([]string{}, pkOf[c.References.Table]...)
		}
		c.Definition = fmt.Sprintf("FOREIGN KEY (%s) REFERENCES %s(%s)",
			quoteList(c.Columns), QuoteIdent(c.References.Table), quoteList(c.References.Columns))
	}
	return out, nil
}

func loadIndexes(ctx context.Context, q database.SQLExecutor, table string) (uniques []database.Constraint, indexes []database.Index, err error) {
	rows, err := q.QueryContext(ctx, `
		SELECT il.name, il."unique", il.origin, COALESCE(s.sql, '')
		  FROM pragma_index_list(?) il
		  LEFT JOIN sqlite_schema s ON s.type = 'index' AND s.name = il.name
		 ORDER BY il.name`, table)
	if err != nil {
		return nil, nil, err
	}
	type idx struct {
		name, origin, sql string
		unique            bool
	}
	var list []idx
	for rows.Next() {
		var x idx
		var unique int
		if err := rows.Scan(&x.name, &unique, &x.origin, &x.sql); err != nil {
			rows.Close()
			return nil, nil, err
		}
		x.unique = unique == 1
		list = append(list, x)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}

	for _, x := range list {
		cols, err := indexColumns(ctx, q, x.name)
		if err != nil {
			return nil, nil, err
		}
		switch x.origin {
		case "u": // UNIQUE constraint; its auto-index name is not a user identifier
			uniques = append(uniques, database.Constraint{Kind: database.Unique, Columns: cols,
				Definition: "UNIQUE (" + quoteList(cols) + ")"})
		case "c":
			indexes = append(indexes, database.Index{Name: x.name, Unique: x.unique, Columns: cols, Definition: x.sql})
		}
		// origin "pk" is the PRIMARY KEY auto-index, already reported as a constraint
	}
	return uniques, indexes, nil
}

func indexColumns(ctx context.Context, q database.SQLExecutor, index string) ([]string, error) {
	rows, err := q.QueryContext(ctx, `SELECT name FROM pragma_index_info(?) ORDER BY seqno`, index)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cols := []string{}
	for rows.Next() {
		var name sql.NullString
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		if name.Valid { // NULL for expression parts; the definition keeps them
			cols = append(cols, name.String)
		}
	}
	return cols, rows.Err()
}

// QuoteIdent quotes a SQLite identifier. Names are validated against
// sqlite_schema before use; quoting is the second line of defence.
func QuoteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

func quoteList(names []string) string {
	q := make([]string, len(names))
	for i, n := range names {
		q[i] = QuoteIdent(n)
	}
	return strings.Join(q, ", ")
}
