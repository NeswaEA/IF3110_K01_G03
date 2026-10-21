// Package postgres is the PostgreSQL target adapter: catalog inspector and the
// read-only table reader used by the viewer. It imports database only.
package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/database"
)

// Inspector reads schema metadata from the PostgreSQL catalog.
type Inspector struct{}

var _ database.Inspector = Inspector{}

// userTables lists ordinary and partitioned tables outside system and reserved namespaces.
const userTables = `
	SELECT c.oid, n.nspname, c.relname
	  FROM pg_class c
	  JOIN pg_namespace n ON n.oid = c.relnamespace
	 WHERE c.relkind IN ('r', 'p')
	   AND NOT c.relispartition
	   AND n.nspname NOT IN ('pg_catalog', 'information_schema', '_platform')
	   AND n.nspname NOT LIKE 'pg\_%'`

// Inspect reads a consistent snapshot inside one read-only repeatable-read transaction.
func (Inspector) Inspect(ctx context.Context, t database.TargetHandle) (database.SchemaSnapshot, error) {
	tx, err := t.DB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return database.SchemaSnapshot{}, database.ClassifyTargetError(ctx, err)
	}
	defer tx.Rollback()
	return Inspector{}.InspectWith(ctx, t, tx)
}

// InspectWith runs on the caller's connection or transaction and never commits.
func (Inspector) InspectWith(ctx context.Context, t database.TargetHandle, q database.SQLExecutor) (database.SchemaSnapshot, error) {
	snap, err := inspect(ctx, q)
	if err != nil {
		return database.SchemaSnapshot{}, database.ClassifyTargetError(ctx, err)
	}
	snap.DatabaseID, snap.Driver = t.DatabaseID, database.Postgres
	snap = database.NormalizeSnapshot(snap)
	if snap.Fingerprint, err = database.Fingerprint(snap); err != nil {
		return database.SchemaSnapshot{}, err
	}
	return snap, nil
}

type tableRef struct {
	schema, name string
}

func inspect(ctx context.Context, q database.SQLExecutor) (database.SchemaSnapshot, error) {
	schemas := map[string]*database.Schema{}
	var order []string

	// Every visible user schema appears, even when empty.
	rows, err := q.QueryContext(ctx, `
		SELECT nspname FROM pg_namespace
		 WHERE nspname NOT IN ('pg_catalog', 'information_schema', '_platform')
		   AND nspname NOT LIKE 'pg\_%'
		 ORDER BY nspname`)
	if err != nil {
		return database.SchemaSnapshot{}, err
	}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return database.SchemaSnapshot{}, err
		}
		schemas[name] = &database.Schema{Name: name}
		order = append(order, name)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return database.SchemaSnapshot{}, err
	}

	tables := map[uint32]*database.Table{}
	owner := map[uint32]tableRef{}
	rows, err = q.QueryContext(ctx, userTables+` ORDER BY n.nspname, c.relname`)
	if err != nil {
		return database.SchemaSnapshot{}, err
	}
	var oids []uint32
	for rows.Next() {
		var oid uint32
		var ref tableRef
		if err := rows.Scan(&oid, &ref.schema, &ref.name); err != nil {
			rows.Close()
			return database.SchemaSnapshot{}, err
		}
		tables[oid] = &database.Table{Name: ref.name}
		owner[oid] = ref
		oids = append(oids, oid)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return database.SchemaSnapshot{}, err
	}

	if err := loadColumns(ctx, q, tables); err != nil {
		return database.SchemaSnapshot{}, err
	}
	if err := loadConstraints(ctx, q, tables); err != nil {
		return database.SchemaSnapshot{}, err
	}
	if err := loadIndexes(ctx, q, tables); err != nil {
		return database.SchemaSnapshot{}, err
	}

	for _, oid := range oids {
		ref := owner[oid]
		s := schemas[ref.schema]
		if s == nil { // schema created between the two queries
			s = &database.Schema{Name: ref.schema}
			schemas[ref.schema] = s
			order = append(order, ref.schema)
		}
		s.Tables = append(s.Tables, *tables[oid])
	}
	sort.Strings(order)
	snap := database.SchemaSnapshot{}
	for _, name := range order {
		snap.Schemas = append(snap.Schemas, *schemas[name])
	}
	return snap, nil
}

func loadColumns(ctx context.Context, q database.SQLExecutor, tables map[uint32]*database.Table) error {
	rows, err := q.QueryContext(ctx, `
		WITH t AS (`+userTables+`)
		SELECT a.attrelid, a.attname, format_type(a.atttypid, a.atttypmod), NOT a.attnotnull,
		       pg_get_expr(d.adbin, d.adrelid)
		  FROM pg_attribute a
		  JOIN t ON t.oid = a.attrelid
		  LEFT JOIN pg_attrdef d ON d.adrelid = a.attrelid AND d.adnum = a.attnum
		 WHERE a.attnum > 0 AND NOT a.attisdropped
		 ORDER BY a.attrelid, a.attnum`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var oid uint32
		var c database.Column
		var def sql.NullString
		if err := rows.Scan(&oid, &c.Name, &c.DataType, &c.Nullable, &def); err != nil {
			return err
		}
		if def.Valid {
			c.Default = &def.String
		}
		t := tables[oid]
		c.Ordinal = len(t.Columns) + 1 // dense ordinal: dropped columns leave no gap
		t.Columns = append(t.Columns, c)
	}
	return rows.Err()
}

var fkAction = map[string]string{"a": "NO ACTION", "r": "RESTRICT", "c": "CASCADE", "n": "SET NULL", "d": "SET DEFAULT"}

func loadConstraints(ctx context.Context, q database.SQLExecutor, tables map[uint32]*database.Table) error {
	rows, err := q.QueryContext(ctx, `
		WITH t AS (`+userTables+`)
		SELECT con.conrelid, con.conname, con.contype, pg_get_constraintdef(con.oid),
		       COALESCE((SELECT json_agg(a.attname ORDER BY k.ord)
		                   FROM unnest(con.conkey) WITH ORDINALITY k(attnum, ord)
		                   JOIN pg_attribute a ON a.attrelid = con.conrelid AND a.attnum = k.attnum), '[]')::text,
		       fn.nspname, fc.relname,
		       COALESCE((SELECT json_agg(a.attname ORDER BY k.ord)
		                   FROM unnest(con.confkey) WITH ORDINALITY k(attnum, ord)
		                   JOIN pg_attribute a ON a.attrelid = con.confrelid AND a.attnum = k.attnum), '[]')::text,
		       con.confupdtype, con.confdeltype
		  FROM pg_constraint con
		  JOIN t ON t.oid = con.conrelid
		  LEFT JOIN pg_class fc ON fc.oid = con.confrelid
		  LEFT JOIN pg_namespace fn ON fn.oid = fc.relnamespace
		 WHERE con.contype IN ('p', 'f', 'u', 'c')
		 ORDER BY con.conrelid, con.conname`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var oid uint32
		var name, kind, def, colsJSON, refColsJSON string
		var refSchema, refTable, onUpd, onDel sql.NullString
		if err := rows.Scan(&oid, &name, &kind, &def, &colsJSON, &refSchema, &refTable, &refColsJSON, &onUpd, &onDel); err != nil {
			return err
		}
		c := database.Constraint{Name: &name, Definition: def}
		if err := json.Unmarshal([]byte(colsJSON), &c.Columns); err != nil {
			return err
		}
		switch kind {
		case "p":
			c.Kind = database.PrimaryKey
		case "u":
			c.Kind = database.Unique
		case "c":
			c.Kind = database.Check
		case "f":
			c.Kind = database.ForeignKey
			ref := &database.ForeignReference{Schema: refSchema.String, Table: refTable.String,
				OnUpdate: fkAction[onUpd.String], OnDelete: fkAction[onDel.String]}
			if err := json.Unmarshal([]byte(refColsJSON), &ref.Columns); err != nil {
				return err
			}
			c.References = ref
		}
		tables[oid].Constraints = append(tables[oid].Constraints, c)
	}
	return rows.Err()
}

func loadIndexes(ctx context.Context, q database.SQLExecutor, tables map[uint32]*database.Table) error {
	rows, err := q.QueryContext(ctx, `
		WITH t AS (`+userTables+`)
		SELECT i.indrelid, ic.relname, i.indisunique, i.indisprimary, pg_get_indexdef(i.indexrelid),
		       COALESCE((SELECT json_agg(a.attname ORDER BY k.ord)
		                   FROM unnest(i.indkey::int2[]) WITH ORDINALITY k(attnum, ord)
		                   JOIN pg_attribute a ON a.attrelid = i.indrelid AND a.attnum = k.attnum
		                  WHERE k.ord <= i.indnkeyatts AND k.attnum > 0), '[]')::text
		  FROM pg_index i
		  JOIN t ON t.oid = i.indrelid
		  JOIN pg_class ic ON ic.oid = i.indexrelid
		 ORDER BY i.indrelid, ic.relname`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var oid uint32
		var ix database.Index
		var colsJSON string
		if err := rows.Scan(&oid, &ix.Name, &ix.Unique, &ix.Primary, &ix.Definition, &colsJSON); err != nil {
			return err
		}
		// expression parts have no column name; columns lists the plain ones, definition keeps the full text
		if err := json.Unmarshal([]byte(colsJSON), &ix.Columns); err != nil {
			return err
		}
		tables[oid].Indexes = append(tables[oid].Indexes, ix)
	}
	return rows.Err()
}

// QuoteIdent quotes a PostgreSQL identifier. Callers validate names against the
// catalog first; quoting is the second line of defence, not the only one.
func QuoteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

func qualified(schema, table string) string {
	return fmt.Sprintf("%s.%s", QuoteIdent(schema), QuoteIdent(table))
}
