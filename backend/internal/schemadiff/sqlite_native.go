package schemadiff

import (
	"context"
	"database/sql"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/database"
	sqliteadapter "github.com/NeswaEA/IF3110_K01_G03/backend/internal/database/sqlite"
)

type SQLiteGenerator struct{ Provider database.TargetProvider }

var _ Generator = SQLiteGenerator{}

type sqliteObject struct{ kind, table, sql string }

func (g SQLiteGenerator) Generate(ctx context.Context, c Comparison) (Candidate, error) {
	if err := ctx.Err(); err != nil {
		return Candidate{}, err
	}
	if c.Schema != "main" || c.Source.Driver != database.SQLite || c.Desired.Driver != database.SQLite {
		return Candidate{}, database.ErrDiffUnsupported
	}
	if g.Provider == nil {
		return Candidate{}, problem("DIFF_UNAVAILABLE", "Pembaca deklarasi SQLite belum tersedia.")
	}
	before, err := g.catalog(ctx, c.Source)
	if err != nil {
		return Candidate{}, err
	}
	after := before
	if c.Source.DatabaseID != c.Desired.DatabaseID {
		after, err = g.catalog(ctx, c.Desired)
		if err != nil {
			return Candidate{}, err
		}
	}
	from, ok := findSchema(c.Source, "main")
	to, found := findSchema(c.Desired, "main")
	if !ok || !found {
		return Candidate{}, database.ErrInvalidSnapshot
	}
	changes, warnings, err := compare(from, to)
	if err != nil {
		return Candidate{}, err
	}
	extra, notes := sqliteUnsupportedObjects(before, after, changes)
	warnings = append(warnings, notes...)
	if len(notes) > 0 {
		return Candidate{Warnings: uniqueWarnings(warnings), Changes: extra}, nil
	}
	if len(changes) == 0 {
		return Candidate{Executable: true}, nil
	}
	sql, notes := sqlitePlan(from, to)
	if len(notes) > 0 {
		return Candidate{Warnings: uniqueWarnings(append(warnings, notes...))}, nil
	}
	if err := verifySQLitePlan(ctx, from, c.Desired, sql); err != nil {
		if ctx.Err() != nil {
			return Candidate{}, ctx.Err()
		}
		return Candidate{Warnings: uniqueWarnings(append(warnings, "Deklarasi atau kandidat tidak dapat mereproduksi schema desired dengan operasi native yang didukung."))}, nil
	}
	return Candidate{SQL: sql, Warnings: warnings, Executable: true}, nil
}

func (g SQLiteGenerator) catalog(ctx context.Context, snap database.SchemaSnapshot) (map[string]sqliteObject, error) {
	h, release, err := g.Provider.Acquire(ctx, snap.DatabaseID)
	if err != nil {
		return nil, err
	}
	defer release()
	if h.Driver != database.SQLite || h.DatabaseID != snap.DatabaseID || h.DB == nil {
		return nil, database.ErrTargetUnavailable
	}
	tx, err := h.DB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, database.ClassifyTargetError(ctx, err)
	}
	defer tx.Rollback()
	current, err := (sqliteadapter.Inspector{}).InspectWith(ctx, h, tx)
	if err != nil {
		return nil, err
	}
	expected, err := database.Fingerprint(snap)
	if err != nil {
		return nil, err
	}
	if current.Fingerprint != expected {
		return nil, problem("SCHEMA_CONFLICT", "Schema SQLite berubah selama perbandingan; ulangi perbandingan.")
	}
	rows, err := tx.QueryContext(ctx, `SELECT type,name,tbl_name,COALESCE(sql,'') FROM sqlite_schema WHERE name NOT LIKE 'sqlite\_%' ESCAPE '\' ORDER BY type,name`)
	if err != nil {
		return nil, database.ClassifyTargetError(ctx, err)
	}
	defer rows.Close()
	out := map[string]sqliteObject{}
	for rows.Next() {
		var o sqliteObject
		var name string
		if err := rows.Scan(&o.kind, &name, &o.table, &o.sql); err != nil {
			return nil, database.ClassifyTargetError(ctx, err)
		}
		if !database.Reserved("main", name) && !database.Reserved("main", o.table) {
			out[o.kind+":"+name] = o
		}
	}
	return out, database.ClassifyTargetError(ctx, rows.Err())
}

func sqliteUnsupportedObjects(before, after map[string]sqliteObject, changes []DiffChange) ([]DiffChange, []string) {
	extra, warnings := []DiffChange{}, []string{}
	for _, key := range joinedNames(before, after) {
		old, existed := before[key]
		next, exists := after[key]
		o := next
		if !exists {
			o = old
		}
		changed := !existed || !exists || !reflect.DeepEqual(sqliteSignature(old.sql), sqliteSignature(next.sql))
		affected := false
		for _, change := range changes {
			if change.Object == "main."+o.table || strings.HasPrefix(change.Object, "main."+o.table+".") {
				affected = true
			}
		}
		unsupported := o.kind == "view" || o.kind == "trigger"
		if o.kind == "view" && len(changes) > 0 {
			affected = true
		}
		if o.kind == "table" {
			unsupported = sqliteSpecialTable(old.sql) || sqliteSpecialTable(next.sql)
		}
		if unsupported && (changed || affected) {
			name := strings.TrimPrefix(key, o.kind+":")
			extra = append(extra, DiffChange{Other, "main." + name, true})
			warnings = append(warnings, "Objek main."+name+" memakai deklarasi di luar cakupan native (view, trigger, virtual/generated column, atau opsi tabel).")
		}
	}
	return extra, warnings
}

func sqliteSignature(sql string) []sqliteToken {
	tokens, ok := sqliteTokens(sql)
	if !ok {
		return []sqliteToken{{value: sql, quoted: true}}
	}
	return tokens
}

func sqliteSpecialTable(sql string) bool {
	tokens, ok := sqliteTokens(sql)
	if !ok {
		return sql != ""
	}
	for _, token := range tokens {
		if token.quoted {
			continue
		}
		switch token.value {
		case "VIRTUAL", "GENERATED", "AS", "WITHOUT", "STRICT", "AUTOINCREMENT", "COLLATE", "CONFLICT", "DEFERRABLE", "ASC", "DESC":
			return true
		}
	}
	return false
}

func sqlitePlan(from, to database.Schema) (string, []string) {
	before, err := byName(visibleTables(from), func(t database.Table) string { return t.Name })
	if err != nil {
		return "", []string{"Snapshot source tidak valid."}
	}
	after, err := byName(visibleTables(to), func(t database.Table) string { return t.Name })
	if err != nil {
		return "", []string{"Snapshot desired tidak valid."}
	}
	phases := [5][]string{}
	warnings := []string{}
	for _, name := range joinedNames(before, after) {
		old, existed := before[name]
		next, exists := after[name]
		if !existed {
			ddl, err := sqliteCreateTable(next, after)
			if err != nil {
				warnings = append(warnings, "Tabel main."+name+": "+err.Error())
			} else {
				phases[1] = append(phases[1], ddl)
			}
		} else if !exists {
			incoming := false
			for _, table := range before {
				for _, constraint := range table.Constraints {
					if constraint.Kind == database.ForeignKey && constraint.References != nil && strings.EqualFold(constraint.References.Table, name) {
						incoming = true
					}
				}
			}
			if incoming {
				warnings = append(warnings, "Tabel main."+name+" masih menjadi tujuan foreign key pada source.")
			} else {
				phases[4] = append(phases[4], "DROP TABLE "+sqliteadapter.QuoteIdent(name)+";")
			}
		} else {
			columns, _ := compareColumns("main."+name, old.Columns, next.Columns)
			constraints, _ := compareConstraints("main."+name, old.Constraints, next.Constraints)
			if len(constraints) > 0 {
				warnings = append(warnings, "Perubahan constraint tabel existing main."+name+" memerlukan migrasi manual.")
			}
			added := map[string]bool{}
			for _, change := range columns {
				if change.Kind != AddColumn {
					warnings = append(warnings, "Perubahan kolom existing "+change.Object+" tidak didukung tanpa rebuild.")
					continue
				}
				added[strings.TrimPrefix(change.Object, "main."+name+".")] = true
			}
			ordered := append([]database.Column{}, next.Columns...)
			sort.Slice(ordered, func(i, j int) bool { return ordered[i].Ordinal < ordered[j].Ordinal })
			n := len(old.Columns)
			for _, column := range ordered {
				if !added[column.Name] {
					continue
				}
				n++
				allowed := column.Ordinal == n
				for _, constraint := range next.Constraints {
					for _, used := range constraint.Columns {
						if used == column.Name {
							allowed = false
						}
					}
				}
				literal, null := true, true
				if column.Default != nil {
					literal, null = sqliteLiteral(*column.Default)
				}
				if !allowed || !literal || (!column.Nullable && null) {
					warnings = append(warnings, "Kolom main."+name+"."+column.Name+" tidak memenuhi batas native ADD COLUMN.")
					continue
				}
				ddl, err := sqliteColumn(column)
				if err != nil {
					warnings = append(warnings, "Kolom main."+name+"."+column.Name+": "+err.Error())
				} else {
					phases[2] = append(phases[2], "ALTER TABLE "+sqliteadapter.QuoteIdent(name)+" ADD COLUMN "+ddl+";")
				}
			}
		}
		oldIndexes, _ := byName(old.Indexes, func(i database.Index) string { return i.Name })
		newIndexes, _ := byName(next.Indexes, func(i database.Index) string { return i.Name })
		for _, index := range joinedNames(oldIndexes, newIndexes) {
			a, had := oldIndexes[index]
			b, has := newIndexes[index]
			switch {
			case had && has:
				changed, _ := compareIndexes("main."+name, []database.Index{a}, []database.Index{b})
				if len(changed) > 0 {
					warnings = append(warnings, "Perubahan index main."+name+"."+index+" memerlukan migrasi manual.")
				}
			case had:
				if !sqlitePlainIndex(a.Definition, name, a.Name, a.Columns, a.Unique) || a.Primary {
					warnings = append(warnings, "Index main."+name+"."+index+" bukan index kolom biasa.")
				} else {
					phases[0] = append(phases[0], "DROP INDEX "+sqliteadapter.QuoteIdent(index)+";")
				}
			case has:
				if !sqlitePlainIndex(b.Definition, name, b.Name, b.Columns, b.Unique) || b.Primary {
					warnings = append(warnings, "Index main."+name+"."+index+" bukan index kolom biasa.")
				} else {
					phases[3] = append(phases[3], strings.TrimSuffix(strings.TrimSpace(b.Definition), ";")+";")
				}
			}
		}
	}
	if len(warnings) > 0 {
		return "", warnings
	}
	statements := []string{}
	for _, phase := range phases {
		statements = append(statements, phase...)
	}
	return strings.Join(statements, "\n") + "\n", nil
}

func sqliteColumn(c database.Column) (string, error) {
	ddl := sqliteadapter.QuoteIdent(c.Name)
	if c.DataType != "" {
		ddl += " " + sqliteadapter.QuoteIdent(c.DataType)
	}
	if !c.Nullable {
		ddl += " NOT NULL"
	}
	if c.Default != nil {
		if !sqliteExpression(*c.Default) {
			return "", fmt.Errorf("default bukan ekspresi tunggal yang didukung")
		}
		literal, _ := sqliteLiteral(*c.Default)
		value := *c.Default
		if !literal && !strings.EqualFold(value, "CURRENT_TIME") && !strings.EqualFold(value, "CURRENT_DATE") && !strings.EqualFold(value, "CURRENT_TIMESTAMP") {
			value = "(" + value + ")"
		}
		ddl += " DEFAULT " + value
	}
	return ddl, nil
}

func sqliteNames(names []string) string {
	out := make([]string, len(names))
	for i, name := range names {
		out[i] = sqliteadapter.QuoteIdent(name)
	}
	return strings.Join(out, ", ")
}

func sqliteCreateTable(table database.Table, tables map[string]database.Table) (string, error) {
	columns := append([]database.Column{}, table.Columns...)
	sort.Slice(columns, func(i, j int) bool { return columns[i].Ordinal < columns[j].Ordinal })
	parts := []string{}
	known := map[string]bool{}
	for i, c := range columns {
		if c.Ordinal != i+1 || known[c.Name] || c.Name == "" {
			return "", fmt.Errorf("kolom atau urutan kolom tidak valid")
		}
		known[c.Name] = true
		ddl, err := sqliteColumn(c)
		if err != nil {
			return "", err
		}
		parts = append(parts, ddl)
	}
	constraints := append([]database.Constraint{}, table.Constraints...)
	sort.Slice(constraints, func(i, j int) bool { return constraintValue(constraints[i]) < constraintValue(constraints[j]) })
	for _, c := range constraints {
		for _, column := range c.Columns {
			if !known[column] {
				return "", fmt.Errorf("kolom constraint tidak ditemukan")
			}
		}
		ddl := ""
		if c.Name != nil {
			ddl = "CONSTRAINT " + sqliteadapter.QuoteIdent(*c.Name) + " "
		}
		switch c.Kind {
		case database.PrimaryKey, database.Unique:
			if len(c.Columns) == 0 {
				return "", fmt.Errorf("constraint harus memiliki kolom")
			}
			kind := "PRIMARY KEY"
			if c.Kind == database.Unique {
				kind = "UNIQUE"
			}
			ddl += kind + " (" + sqliteNames(c.Columns) + ")"
		case database.ForeignKey:
			r := c.References
			if r == nil || r.Schema != "main" || len(r.Columns) == 0 || len(r.Columns) != len(c.Columns) {
				return "", fmt.Errorf("referensi foreign key tidak valid")
			}
			parent, ok := tables[r.Table]
			if !ok {
				return "", fmt.Errorf("tabel referensi tidak ditemukan pada desired")
			}
			unique := false
			for _, key := range parent.Constraints {
				if (key.Kind == database.PrimaryKey || key.Kind == database.Unique) && reflect.DeepEqual(key.Columns, r.Columns) {
					unique = true
				}
			}
			for _, index := range parent.Indexes {
				if index.Unique && reflect.DeepEqual(index.Columns, r.Columns) && sqlitePlainIndex(index.Definition, parent.Name, index.Name, index.Columns, true) {
					unique = true
				}
			}
			if !unique {
				return "", fmt.Errorf("referensi foreign key tidak menuju key unik yang didukung")
			}
			ddl += "FOREIGN KEY (" + sqliteNames(c.Columns) + ") REFERENCES " + sqliteadapter.QuoteIdent(r.Table) + " (" + sqliteNames(r.Columns) + ")"
			for _, action := range []struct{ name, value string }{{"UPDATE", r.OnUpdate}, {"DELETE", r.OnDelete}} {
				value := strings.ToUpper(action.value)
				switch value {
				case "NO ACTION", "RESTRICT", "SET NULL", "SET DEFAULT", "CASCADE":
				default:
					return "", fmt.Errorf("aksi foreign key tidak didukung")
				}
				ddl += " ON " + action.name + " " + value
			}
		case database.Check:
			if !sqliteCheck(c.Definition) {
				return "", fmt.Errorf("CHECK bukan deklarasi tunggal yang didukung")
			}
			ddl += c.Definition
		default:
			return "", fmt.Errorf("jenis constraint tidak didukung")
		}
		parts = append(parts, ddl)
	}
	if len(columns) == 0 {
		return "", fmt.Errorf("tabel harus memiliki kolom")
	}
	return "CREATE TABLE " + sqliteadapter.QuoteIdent(table.Name) + " (\n  " + strings.Join(parts, ",\n  ") + "\n);", nil
}

func verifySQLitePlan(ctx context.Context, source database.Schema, desired database.SchemaSnapshot, candidate string) error {
	db, err := sql.Open("sqlite3", ":memory:?_foreign_keys=1")
	if err != nil {
		return err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	tables, err := byName(visibleTables(source), func(t database.Table) string { return t.Name })
	if err != nil {
		return err
	}
	for _, name := range joinedNames(tables, tables) {
		ddl, err := sqliteCreateTable(tables[name], tables)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, ddl); err != nil {
			return err
		}
	}
	for _, name := range joinedNames(tables, tables) {
		for _, index := range tables[name].Indexes {
			if !sqlitePlainIndex(index.Definition, name, index.Name, index.Columns, index.Unique) {
				return database.ErrDiffUnsupported
			}
			if _, err := tx.ExecContext(ctx, index.Definition); err != nil {
				return err
			}
		}
	}
	if _, err := tx.ExecContext(ctx, candidate); err != nil {
		return err
	}
	actual, err := (sqliteadapter.Inspector{}).InspectWith(ctx, database.TargetHandle{Driver: database.SQLite}, tx)
	if err != nil {
		return err
	}
	want, err := database.Fingerprint(desired)
	if err != nil {
		return err
	}
	if actual.Fingerprint != want {
		return database.ErrInvalidSnapshot
	}
	return nil
}
