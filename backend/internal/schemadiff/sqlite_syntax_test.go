package schemadiff

import (
	"testing"

	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/database"
)

func TestSQLiteNativeLiteralAndCheckBoundaries(t *testing.T) {
	for _, value := range []string{"NULL", "null", "TRUE", "false", "-1", "+1.5e-2", "0xFF", ".25", "X'00ff'", "''", "'a''b;--'"} {
		if ok, _ := sqliteLiteral(value); !ok {
			t.Fatal("literal rejected", value)
		}
	}
	for _, value := range []string{`"'x'"`, "CURRENT_TIMESTAMP", "(1+2)", "random()", "1; DROP TABLE items", "'unclosed", "x'0'", "NULL -- comment", "?", "0x"} {
		if ok, _ := sqliteLiteral(value); ok {
			t.Fatal("non-literal accepted", value)
		}
	}
	for _, value := range []string{`CHECK (id>0)`, `CHECK (label != '); DROP TABLE x; --')`, `CHECK ((id + 1)>0 AND length(label)>0)`} {
		if !sqliteCheck(value) {
			t.Fatal("CHECK rejected", value)
		}
	}
	for _, value := range []string{`CHECK (id>0)); DROP TABLE items; --`, `CHECK(id>0), extra TEXT CHECK(1)`, `CHECK(id>0) ON CONFLICT REPLACE`, `CHECK(? > 0)`, `CHECK(id>0);`, `CHECK(id>0 /* comment */)`, `CHECK('unclosed)`, `CHECK(1) CHECK(2)`} {
		if sqliteCheck(value) {
			t.Fatal("fragment escaped CHECK declaration", value)
		}
	}
}

func TestSQLiteNativeIndexAllowsOnlyACompleteColumnDeclaration(t *testing.T) {
	for _, value := range []string{`CREATE INDEX idx ON items(a,b)`, `CREATE INDEX IF NOT EXISTS "idx" ON "items" ("a",[b]);`, "CREATE INDEX `idx` ON `items` (`a`,`b`)"} {
		if !sqlitePlainIndex(value, "items", "idx", []string{"a", "b"}, false) {
			t.Fatal("plain index rejected", value)
		}
	}
	for _, value := range []string{`CREATE INDEX idx ON items(a,abs(b))`, `CREATE INDEX idx ON items(a,b) WHERE a>0`, `CREATE INDEX idx ON items(a,b DESC)`, `CREATE INDEX idx ON items(a COLLATE NOCASE,b)`, `CREATE UNIQUE INDEX idx ON items(a,b)`, `CREATE INDEX idx ON items(a,b); DROP TABLE items;`, `CREATE INDEX idx ON other(a,b)`, `CREATE INDEX other ON items(a,b)`, `CREATE INDEX idx ON items(a)`, `CREATE INDEX idx ON items(b,a)`} {
		if sqlitePlainIndex(value, "items", "idx", []string{"a", "b"}, false) {
			t.Fatal("unsupported index accepted", value)
		}
	}
}

func TestSQLiteNativeRejectsInvalidReferencesAndInjectedDefinitions(t *testing.T) {
	parent := database.Table{Name: "parent", Columns: []database.Column{{Name: "id", DataType: "INTEGER", Ordinal: 1}}, Constraints: []database.Constraint{{Kind: database.PrimaryKey, Columns: []string{"id"}}}}
	for _, constraint := range []database.Constraint{
		{Kind: database.Check, Definition: `CHECK(id>0)); CREATE TABLE injected(id); --`},
		{Kind: database.ForeignKey, Columns: []string{"id"}, References: &database.ForeignReference{Schema: "main", Table: "missing", Columns: []string{"id"}}},
		{Kind: database.ForeignKey, Columns: []string{"id"}, References: &database.ForeignReference{Schema: "other", Table: "parent", Columns: []string{"id"}}},
		{Kind: database.ForeignKey, Columns: []string{"id"}, References: &database.ForeignReference{Schema: "main", Table: "parent", Columns: []string{"missing"}}},
		{Kind: database.ForeignKey, Columns: []string{"id"}, References: &database.ForeignReference{Schema: "main", Table: "parent", Columns: []string{"id"}, OnUpdate: "CASCADE; DROP TABLE parent", OnDelete: "CASCADE"}},
	} {
		child := database.Table{Name: "child", Columns: []database.Column{{Name: "id", DataType: "INTEGER", Ordinal: 1}}, Constraints: []database.Constraint{constraint}}
		if sql, err := sqliteCreateTable(child, map[string]database.Table{"parent": parent, "child": child}); err == nil {
			t.Fatal("unsafe declaration rendered", sql)
		}
	}
}
