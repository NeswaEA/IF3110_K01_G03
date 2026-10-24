package schemadiff

import (
	"encoding/json"
	"errors"
	"math/rand/v2"
	"reflect"
	"testing"

	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/database"
)

func comparisonTable() database.Table {
	return database.Table{Name: "items",
		Columns:     []database.Column{{Name: "id", DataType: "INTEGER", Ordinal: 1}, {Name: "label", DataType: "TEXT", Nullable: true, Ordinal: 2}},
		Constraints: []database.Constraint{{Name: ptr("items_fk"), Kind: database.ForeignKey, Columns: []string{"id", "label"}, Definition: "FOREIGN KEY (id,label) REFERENCES parent(id,label)", References: &database.ForeignReference{Schema: "main", Table: "parent", Columns: []string{"id", "label"}, OnDelete: "NO ACTION", OnUpdate: "NO ACTION"}}},
		Indexes:     []database.Index{{Name: "items_idx", Columns: []string{"label", "id"}, Definition: "CREATE INDEX items_idx ON items(label,id)"}},
	}
}

func TestComparisonDetectsEveryPublishedField(t *testing.T) {
	for _, tc := range []struct {
		name        string
		change      func(*database.Table)
		kind        DiffChangeKind
		object      string
		destructive bool
	}{
		{"add column", func(t *database.Table) {
			t.Columns = append(t.Columns, database.Column{Name: "status", DataType: "TEXT", Ordinal: 3})
		}, AddColumn, "status", false},
		{"drop column", func(t *database.Table) { t.Columns = t.Columns[:1] }, DropColumn, "label", true},
		{"column type", func(t *database.Table) { t.Columns[1].DataType = "INTEGER" }, AlterColumn, "label", true},
		{"column nullability", func(t *database.Table) { t.Columns[1].Nullable = false }, AlterColumn, "label", true},
		{"column default", func(t *database.Table) { t.Columns[1].Default = ptr("'new'") }, AlterColumn, "label", false},
		{"column ordinal", func(t *database.Table) { t.Columns[1].Ordinal = 3 }, AlterColumn, "label", false},
		{"index unique", func(t *database.Table) { t.Indexes[0].Unique = true }, AlterIndex, "items_idx", true},
		{"index primary", func(t *database.Table) { t.Indexes[0].Primary = true }, AlterIndex, "items_idx", true},
		{"index column order", func(t *database.Table) { t.Indexes[0].Columns = []string{"id", "label"} }, AlterIndex, "items_idx", true},
		{"index definition", func(t *database.Table) { t.Indexes[0].Definition += " WHERE id>0" }, AlterIndex, "items_idx", true},
		{"drop index", func(t *database.Table) { t.Indexes = nil }, DropIndex, "items_idx", true},
		{"add index", func(t *database.Table) {
			t.Indexes = append(t.Indexes, database.Index{Name: "new_idx", Columns: []string{"id"}})
		}, AddIndex, "new_idx", false},
		{"constraint kind", func(t *database.Table) { t.Constraints[0].Kind = database.Unique }, AlterConstraint, "items_fk", true},
		{"constraint definition", func(t *database.Table) { t.Constraints[0].Definition += " DEFERRABLE" }, AlterConstraint, "items_fk", true},
		{"constraint column order", func(t *database.Table) { t.Constraints[0].Columns = []string{"label", "id"} }, AlterConstraint, "items_fk", true},
		{"reference removed", func(t *database.Table) { t.Constraints[0].References = nil }, AlterConstraint, "items_fk", true},
		{"reference schema", func(t *database.Table) { t.Constraints[0].References.Schema = "other" }, AlterConstraint, "items_fk", true},
		{"reference table", func(t *database.Table) { t.Constraints[0].References.Table = "other" }, AlterConstraint, "items_fk", true},
		{"reference column order", func(t *database.Table) { t.Constraints[0].References.Columns = []string{"label", "id"} }, AlterConstraint, "items_fk", true},
		{"reference delete", func(t *database.Table) { t.Constraints[0].References.OnDelete = "CASCADE" }, AlterConstraint, "items_fk", true},
		{"reference update", func(t *database.Table) { t.Constraints[0].References.OnUpdate = "CASCADE" }, AlterConstraint, "items_fk", true},
		{"drop constraint", func(t *database.Table) { t.Constraints = nil }, DropConstraint, "items_fk", true},
		{"add constraint", func(t *database.Table) {
			t.Constraints = append(t.Constraints, database.Constraint{Name: ptr("new_check"), Kind: database.Check, Definition: "CHECK (id>0)"})
		}, AddConstraint, "new_check", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			old := comparisonTable()
			next := clone(old)
			tc.change(&next)
			changes, warnings, err := compare(database.Schema{Name: "main", Tables: []database.Table{old}}, database.Schema{Name: "main", Tables: []database.Table{next}})
			want := []DiffChange{{tc.kind, "main.items." + tc.object, tc.destructive}}
			if err != nil || !reflect.DeepEqual(changes, want) || (tc.destructive && len(warnings) == 0) {
				t.Fatal(changes, warnings, err)
			}
		})
	}
}

func TestComparisonMatchesUnnamedConstraintsWithoutLosingDuplicates(t *testing.T) {
	check := func(def string) database.Constraint {
		return database.Constraint{Kind: database.Check, Definition: def}
	}
	for _, tc := range []struct {
		name      string
		old, next []database.Constraint
		kind      DiffChangeKind
		count     int
	}{
		{"reordered checks", []database.Constraint{check("CHECK (a>0)"), check("CHECK (b>0)")}, []database.Constraint{check("CHECK (b>0)"), check("CHECK (a>0)")}, "", 0},
		{"one check altered", []database.Constraint{check("CHECK (a>0)"), check("CHECK (b>0)")}, []database.Constraint{check("CHECK (b>0)"), check("CHECK (a>1)")}, AlterConstraint, 1},
		{"ambiguous altered checks", []database.Constraint{check("CHECK (a>0)"), check("CHECK (b>0)")}, []database.Constraint{check("CHECK (a>1)"), check("CHECK (b>1)")}, Other, 1},
		{"duplicate check removed", []database.Constraint{check("CHECK (a>0)"), check("CHECK (a>0)")}, []database.Constraint{check("CHECK (a>0)")}, DropConstraint, 1},
		{"two distinct checks added", nil, []database.Constraint{check("CHECK (a>0)"), check("CHECK (b>0)")}, AddConstraint, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			changes, warnings := compareConstraints("main.items", tc.old, tc.next)
			if len(changes) != tc.count {
				t.Fatal(changes)
			}
			for _, c := range changes {
				if c.Kind != tc.kind {
					t.Fatal(changes)
				}
			}
			if tc.kind == Other && len(warnings) == 0 {
				t.Fatal("ambiguous constraints need a warning")
			}
			if len(changes) == 2 && changes[0].Object == changes[1].Object {
				t.Fatal("distinct unnamed constraints lost their identity")
			}
		})
	}
}

func TestComparisonIsDeterministicAndDoesNotMutateSnapshots(t *testing.T) {
	before := database.Schema{Name: "main", Tables: []database.Table{comparisonTable(), {Name: "gone"}, {Name: "_platform_hidden"}}}
	after := clone(before)
	after.Tables[1].Name = "added"
	after.Tables[0].Columns[1].Default = ptr("'new'")
	after.Tables[0].Indexes = append(after.Tables[0].Indexes, database.Index{Name: "extra", Columns: []string{"label"}})
	originalBefore, _ := json.Marshal(before)
	originalAfter, _ := json.Marshal(after)
	want, warnings, err := compare(before, after)
	if err != nil || len(want) != 4 {
		t.Fatal(want, err)
	}
	rng := rand.New(rand.NewPCG(1, 2))
	shuffle := func(schema *database.Schema) {
		rng.Shuffle(len(schema.Tables), func(i, j int) { schema.Tables[i], schema.Tables[j] = schema.Tables[j], schema.Tables[i] })
		for i := range schema.Tables {
			table := &schema.Tables[i]
			rng.Shuffle(len(table.Columns), func(i, j int) { table.Columns[i], table.Columns[j] = table.Columns[j], table.Columns[i] })
			rng.Shuffle(len(table.Indexes), func(i, j int) { table.Indexes[i], table.Indexes[j] = table.Indexes[j], table.Indexes[i] })
			rng.Shuffle(len(table.Constraints), func(i, j int) {
				table.Constraints[i], table.Constraints[j] = table.Constraints[j], table.Constraints[i]
			})
		}
	}
	for i := 0; i < 30; i++ {
		a, b := clone(before), clone(after)
		shuffle(&a)
		shuffle(&b)
		got, notes, err := compare(a, b)
		if err != nil || !reflect.DeepEqual(got, want) || !reflect.DeepEqual(notes, warnings) {
			t.Fatal("ordering changed the diff", got, err)
		}
	}
	gotBefore, _ := json.Marshal(before)
	gotAfter, _ := json.Marshal(after)
	if string(gotBefore) != string(originalBefore) || string(gotAfter) != string(originalAfter) {
		t.Fatal("comparison changed its inputs")
	}
}

func TestComparisonNormalizesEmptyCollectionsAndDetectsRenames(t *testing.T) {
	old := database.Schema{Name: "main", Tables: []database.Table{{Name: "items", Constraints: []database.Constraint{{Kind: database.Check, Definition: "CHECK (1)", References: &database.ForeignReference{}}}, Indexes: []database.Index{{Name: "idx"}}}}}
	next := clone(old)
	next.Tables[0].Constraints[0].Columns = []string{}
	next.Tables[0].Constraints[0].References.Columns = []string{}
	next.Tables[0].Indexes[0].Columns = []string{}
	changes, _, err := compare(old, next)
	if err != nil || len(changes) != 0 {
		t.Fatal("nil and empty collections differ", changes, err)
	}
	next.Tables[0].Name = "renamed"
	changes, _, err = compare(old, next)
	if err != nil || !reflect.DeepEqual(changes, []DiffChange{{DropTable, "main.items", true}, {AddTable, "main.renamed", false}}) {
		t.Fatal("rename was guessed incorrectly", changes, err)
	}
}

func TestComparisonRejectsDuplicateObjects(t *testing.T) {
	for _, duplicate := range []string{"table", "column", "index"} {
		t.Run(duplicate, func(t *testing.T) {
			old := database.Schema{Name: "main", Tables: []database.Table{comparisonTable()}}
			next := clone(old)
			switch duplicate {
			case "table":
				next.Tables = append(next.Tables, next.Tables[0])
			case "column":
				next.Tables[0].Columns = append(next.Tables[0].Columns, next.Tables[0].Columns[0])
			case "index":
				next.Tables[0].Indexes = append(next.Tables[0].Indexes, next.Tables[0].Indexes[0])
			}
			_, _, err := compare(old, next)
			if !errors.Is(err, database.ErrInvalidSnapshot) {
				t.Fatal("duplicate object was silently discarded", err)
			}
		})
	}
}
