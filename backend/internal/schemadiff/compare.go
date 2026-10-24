package schemadiff

import (
	"encoding/json"
	"reflect"
	"sort"
	"strings"

	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/database"
)

func compare(source, desired database.Schema) ([]DiffChange, []string, error) {
	changes, warnings := []DiffChange{}, []string{}
	before, err := byName(visibleTables(source), func(t database.Table) string { return t.Name })
	if err != nil {
		return nil, nil, err
	}
	after, err := byName(visibleTables(desired), func(t database.Table) string { return t.Name })
	if err != nil {
		return nil, nil, err
	}
	for _, name := range joinedNames(before, after) {
		old, existed := before[name]
		next, exists := after[name]
		object := source.Name + "." + name
		switch {
		case !existed:
			changes = append(changes, DiffChange{AddTable, object, false})
		case !exists:
			changes = append(changes, DiffChange{DropTable, object, true})
		default:
			columns, err := compareColumns(object, old.Columns, next.Columns)
			if err != nil {
				return nil, nil, err
			}
			indexes, err := compareIndexes(object, old.Indexes, next.Indexes)
			if err != nil {
				return nil, nil, err
			}
			constraints, notes := compareConstraints(object, old.Constraints, next.Constraints)
			changes = append(changes, columns...)
			changes = append(changes, indexes...)
			changes = append(changes, constraints...)
			warnings = append(warnings, notes...)
		}
	}
	sort.Slice(changes, func(i, j int) bool {
		if changes[i].Object != changes[j].Object {
			return changes[i].Object < changes[j].Object
		}
		return changes[i].Kind < changes[j].Kind
	})
	for _, change := range changes {
		if change.Destructive {
			warnings = append(warnings, "Periksa perubahan destruktif: "+string(change.Kind)+" "+change.Object+".")
		}
	}
	return changes, uniqueWarnings(warnings), nil
}

func visibleTables(schema database.Schema) []database.Table {
	out := []database.Table{}
	for _, table := range schema.Tables {
		if !database.Reserved(schema.Name, table.Name) {
			out = append(out, table)
		}
	}
	return out
}

func byName[T any](items []T, name func(T) string) (map[string]T, error) {
	out := make(map[string]T, len(items))
	for _, item := range items {
		key := name(item)
		if _, duplicate := out[key]; key == "" || duplicate {
			return nil, database.ErrInvalidSnapshot
		}
		out[key] = item
	}
	return out, nil
}

func joinedNames[T any](before, after map[string]T) []string {
	set := map[string]bool{}
	for name := range before {
		set[name] = true
	}
	for name := range after {
		set[name] = true
	}
	names := make([]string, 0, len(set))
	for name := range set {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func compareColumns(table string, source, desired []database.Column) ([]DiffChange, error) {
	before, err := byName(source, func(c database.Column) string { return c.Name })
	if err != nil {
		return nil, err
	}
	after, err := byName(desired, func(c database.Column) string { return c.Name })
	if err != nil {
		return nil, err
	}
	changes := []DiffChange{}
	for _, name := range joinedNames(before, after) {
		old, existed := before[name]
		next, exists := after[name]
		object := table + "." + name
		switch {
		case !existed:
			changes = append(changes, DiffChange{AddColumn, object, false})
		case !exists:
			changes = append(changes, DiffChange{DropColumn, object, true})
		case !reflect.DeepEqual(old, next):
			changes = append(changes, DiffChange{AlterColumn, object, old.DataType != next.DataType || (old.Nullable && !next.Nullable)})
		}
	}
	return changes, nil
}

func compareIndexes(table string, source, desired []database.Index) ([]DiffChange, error) {
	before, err := byName(source, func(i database.Index) string { return i.Name })
	if err != nil {
		return nil, err
	}
	after, err := byName(desired, func(i database.Index) string { return i.Name })
	if err != nil {
		return nil, err
	}
	changes := []DiffChange{}
	for _, name := range joinedNames(before, after) {
		old, existed := before[name]
		next, exists := after[name]
		object := table + "." + name
		old.Columns, next.Columns = append([]string{}, old.Columns...), append([]string{}, next.Columns...)
		switch {
		case !existed:
			changes = append(changes, DiffChange{AddIndex, object, false})
		case !exists:
			changes = append(changes, DiffChange{DropIndex, object, true})
		case !reflect.DeepEqual(old, next):
			changes = append(changes, DiffChange{AlterIndex, object, true})
		}
	}
	return changes, nil
}

func constraintValue(c database.Constraint) string {
	c.Columns = append([]string{}, c.Columns...)
	if c.References != nil {
		r := *c.References
		r.Columns = append([]string{}, r.Columns...)
		c.References = &r
	}
	value, _ := json.Marshal(c)
	return string(value)
}

func constraintGroups(constraints []database.Constraint) map[string][]database.Constraint {
	groups := map[string][]database.Constraint{}
	for _, c := range constraints {
		key := "name:"
		if c.Name != nil {
			key += *c.Name
		} else {
			columns, _ := json.Marshal(append([]string{}, c.Columns...))
			key = "unnamed:" + string(c.Kind) + ":" + string(columns)
		}
		groups[key] = append(groups[key], c)
	}
	return groups
}

func unmatchedConstraints(source, desired []database.Constraint) []database.Constraint {
	counts := map[string]int{}
	for _, c := range desired {
		counts[constraintValue(c)]++
	}
	unmatched := []database.Constraint{}
	for _, c := range source {
		key := constraintValue(c)
		if counts[key] > 0 {
			counts[key]--
		} else {
			unmatched = append(unmatched, c)
		}
	}
	return unmatched
}

func constraintObject(table string, c database.Constraint) string {
	if c.Name != nil {
		return table + "." + *c.Name
	}
	if c.Definition != "" {
		return table + "." + c.Definition
	}
	return table + "." + string(c.Kind) + "(" + strings.Join(c.Columns, ",") + ")"
}

func compareConstraints(table string, source, desired []database.Constraint) ([]DiffChange, []string) {
	before, after := constraintGroups(source), constraintGroups(desired)
	changes, warnings := []DiffChange{}, []string{}
	for _, key := range joinedNames(before, after) {
		old := unmatchedConstraints(before[key], after[key])
		next := unmatchedConstraints(after[key], before[key])
		switch {
		case len(old) == 1 && len(next) == 1:
			changes = append(changes, DiffChange{AlterConstraint, constraintObject(table, next[0]), true})
		case len(old) > 0 && len(next) > 0:
			changes = append(changes, DiffChange{Other, table + ".constraints", true})
			warnings = append(warnings, "Constraint pada "+table+" berubah dan tidak dapat dipasangkan secara pasti.")
		default:
			for _, c := range old {
				changes = append(changes, DiffChange{DropConstraint, constraintObject(table, c), true})
			}
			for _, c := range next {
				changes = append(changes, DiffChange{AddConstraint, constraintObject(table, c), false})
			}
		}
	}
	return changes, warnings
}
