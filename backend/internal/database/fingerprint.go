package database

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
)

// Fingerprint is the only schema-fingerprint algorithm (viewer, diff and the
// migration runner must all call it). It hashes a canonical JSON form of the
// snapshot: objects sorted lexicographically, columns by ordinal, constraint
// and index column lists in declared order, reserved platform objects, data,
// database_id and timestamps excluded. It does no I/O.
func Fingerprint(s SchemaSnapshot) (string, error) {
	if !s.Driver.Valid() {
		return "", fmt.Errorf("%w: driver %q", ErrInvalidSnapshot, s.Driver)
	}
	c := canonSnapshot{Driver: s.Driver, Schemas: []canonSchema{}}
	seenSchema := map[string]bool{}
	for _, sc := range s.Schemas {
		if sc.Name == "" || seenSchema[sc.Name] {
			return "", fmt.Errorf("%w: schema name %q", ErrInvalidSnapshot, sc.Name)
		}
		seenSchema[sc.Name] = true
		cs := canonSchema{Name: sc.Name, Tables: []canonTable{}}
		seenTable := map[string]bool{}
		for _, t := range sc.Tables {
			if t.Name == "" || seenTable[t.Name] {
				return "", fmt.Errorf("%w: table name %q in %q", ErrInvalidSnapshot, t.Name, sc.Name)
			}
			seenTable[t.Name] = true
			if Reserved(sc.Name, t.Name) {
				continue
			}
			ct, err := canonTableOf(t)
			if err != nil {
				return "", err
			}
			cs.Tables = append(cs.Tables, ct)
		}
		if sc.Name == "_platform" {
			continue
		}
		sort.Slice(cs.Tables, func(i, j int) bool { return cs.Tables[i].Name < cs.Tables[j].Name })
		c.Schemas = append(c.Schemas, cs)
	}
	sort.Slice(c.Schemas, func(i, j int) bool { return c.Schemas[i].Name < c.Schemas[j].Name })

	b, err := json.Marshal(c)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrInvalidSnapshot, err)
	}
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

type canonSnapshot struct {
	Driver  Driver        `json:"driver"`
	Schemas []canonSchema `json:"schemas"`
}

type canonSchema struct {
	Name   string       `json:"name"`
	Tables []canonTable `json:"tables"`
}

type canonTable struct {
	Name        string            `json:"name"`
	Columns     []Column          `json:"columns"`
	Constraints []canonConstraint `json:"constraints"`
	Indexes     []Index           `json:"indexes"`
}

type canonConstraint struct {
	Name       *string           `json:"name"`
	Kind       ConstraintKind    `json:"kind"`
	Columns    []string          `json:"columns"`
	Definition string            `json:"definition"`
	References *ForeignReference `json:"references"`
}

func canonTableOf(t Table) (canonTable, error) {
	ct := canonTable{Name: t.Name, Columns: []Column{}, Constraints: []canonConstraint{}, Indexes: []Index{}}

	seenCol := map[string]bool{}
	for _, col := range t.Columns {
		if col.Name == "" || seenCol[col.Name] || col.Ordinal < 1 {
			return ct, fmt.Errorf("%w: column %q in table %q", ErrInvalidSnapshot, col.Name, t.Name)
		}
		seenCol[col.Name] = true
		ct.Columns = append(ct.Columns, col)
	}
	sort.Slice(ct.Columns, func(i, j int) bool { return ct.Columns[i].Ordinal < ct.Columns[j].Ordinal })

	for _, k := range t.Constraints {
		switch k.Kind {
		case PrimaryKey, ForeignKey, Unique, Check:
		default:
			return ct, fmt.Errorf("%w: constraint kind %q in table %q", ErrInvalidSnapshot, k.Kind, t.Name)
		}
		cols := append([]string{}, k.Columns...)
		var ref *ForeignReference
		if k.References != nil {
			r := *k.References
			r.Columns = append([]string{}, r.Columns...)
			ref = &r
		}
		ct.Constraints = append(ct.Constraints, canonConstraint{k.Name, k.Kind, cols, k.Definition, ref})
	}
	sort.Slice(ct.Constraints, func(i, j int) bool {
		a, b := ct.Constraints[i], ct.Constraints[j]
		an, bn := deref(a.Name), deref(b.Name)
		if (a.Name == nil) != (b.Name == nil) {
			return a.Name != nil // named first
		}
		if an != bn {
			return an < bn
		}
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		return a.Definition < b.Definition
	})

	for _, ix := range t.Indexes {
		ix.Columns = append([]string{}, ix.Columns...)
		ct.Indexes = append(ct.Indexes, ix)
	}
	sort.Slice(ct.Indexes, func(i, j int) bool { return ct.Indexes[i].Name < ct.Indexes[j].Name })
	return ct, nil
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
