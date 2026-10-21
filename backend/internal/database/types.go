// Package database defines the target-database model and adapter seams shared
// by connections, viewer, explain, migrations and schemadiff. It imports the
// standard library only.
package database

import (
	"database/sql"
	"strings"
)

type Driver string

const (
	Postgres Driver = "postgres"
	SQLite   Driver = "sqlite"
)

func (d Driver) Valid() bool { return d == Postgres || d == SQLite }

// TargetHandle is a lease on a registered target. DB is a pool shared between
// requests: callers must not close it.
type TargetHandle struct {
	DatabaseID     string
	Driver         Driver
	Revision       int64
	MigrationReady bool
	DB             *sql.DB
}

type ConstraintKind string

const (
	PrimaryKey ConstraintKind = "primary_key"
	ForeignKey ConstraintKind = "foreign_key"
	Unique     ConstraintKind = "unique"
	Check      ConstraintKind = "check"
)

type Encoding string

const (
	EncText          Encoding = "text"
	EncDecimalString Encoding = "decimal_string"
	EncBoolean       Encoding = "boolean"
	EncNumber        Encoding = "number"
	EncTimestamp     Encoding = "timestamp"
	EncJSON          Encoding = "json"
	EncBase64        Encoding = "base64"
)

// SchemaSnapshot and the types below are the shared schema DTOs. Nullable
// fields are pointers so they serialise as null; collections are never nil
// once produced by an inspector (use NormalizeSnapshot).
type SchemaSnapshot struct {
	DatabaseID  string   `json:"database_id"`
	Driver      Driver   `json:"driver"`
	Fingerprint string   `json:"fingerprint"`
	Schemas     []Schema `json:"schemas"`
}

type Schema struct {
	Name   string  `json:"name"`
	Tables []Table `json:"tables"`
}

type Table struct {
	Name        string       `json:"name"`
	Columns     []Column     `json:"columns"`
	Constraints []Constraint `json:"constraints"`
	Indexes     []Index      `json:"indexes"`
}

type Column struct {
	Name     string  `json:"name"`
	DataType string  `json:"data_type"`
	Nullable bool    `json:"nullable"`
	Default  *string `json:"default"`
	Ordinal  int     `json:"ordinal"`
}

type Constraint struct {
	Name       *string           `json:"name"`
	Kind       ConstraintKind    `json:"kind"`
	Columns    []string          `json:"columns"`
	Definition string            `json:"definition"`
	References *ForeignReference `json:"references"`
}

type ForeignReference struct {
	Schema   string   `json:"schema"`
	Table    string   `json:"table"`
	Columns  []string `json:"columns"`
	OnUpdate string   `json:"on_update"`
	OnDelete string   `json:"on_delete"`
}

type Index struct {
	Name       string   `json:"name"`
	Unique     bool     `json:"unique"`
	Primary    bool     `json:"primary"`
	Columns    []string `json:"columns"`
	Definition string   `json:"definition"`
}

type DataColumn struct {
	Name     string   `json:"name"`
	DataType string   `json:"data_type"`
	Encoding Encoding `json:"encoding"`
}

// TableData rows hold one JSON-compatible value per column; NULL stays nil.
type TableData struct {
	Columns     []DataColumn `json:"columns"`
	Rows        [][]any      `json:"rows"`
	OrderStable bool         `json:"order_stable"`
}

// Reserved reports whether an object belongs to the platform namespace
// (PostgreSQL schema _platform, SQLite names prefixed _platform_). Inspectors,
// the viewer and Fingerprint all hide these objects.
func Reserved(schema, table string) bool {
	return schema == "_platform" || strings.HasPrefix(table, "_platform_")
}

// NormalizeSnapshot replaces nil collections with empty ones so JSON never
// alternates between null and [].
func NormalizeSnapshot(s SchemaSnapshot) SchemaSnapshot {
	if s.Schemas == nil {
		s.Schemas = []Schema{}
	}
	for i := range s.Schemas {
		if s.Schemas[i].Tables == nil {
			s.Schemas[i].Tables = []Table{}
		}
		for j := range s.Schemas[i].Tables {
			t := &s.Schemas[i].Tables[j]
			if t.Columns == nil {
				t.Columns = []Column{}
			}
			if t.Constraints == nil {
				t.Constraints = []Constraint{}
			}
			if t.Indexes == nil {
				t.Indexes = []Index{}
			}
			for k := range t.Constraints {
				if t.Constraints[k].Columns == nil {
					t.Constraints[k].Columns = []string{}
				}
				if r := t.Constraints[k].References; r != nil && r.Columns == nil {
					r.Columns = []string{}
				}
			}
			for k := range t.Indexes {
				if t.Indexes[k].Columns == nil {
					t.Indexes[k].Columns = []string{}
				}
			}
		}
	}
	return s
}

// PlanResult and PlanNode are the shared plan DTOs. Metrics the engine
// does not provide stay nil; actual_* are always nil in estimate mode.
type PlanResult struct {
	Driver   Driver     `json:"driver"`
	Mode     string     `json:"mode"`
	Nodes    []PlanNode `json:"nodes"`
	Raw      any        `json:"raw"`
	Warnings []string   `json:"warnings"`
}

type PlanNode struct {
	ID            string   `json:"id"`
	ParentID      *string  `json:"parent_id"`
	Operation     string   `json:"operation"`
	Relation      *string  `json:"relation"`
	Index         *string  `json:"index"`
	EstimatedRows *float64 `json:"estimated_rows"`
	EstimatedCost *float64 `json:"estimated_cost"`
	ActualTimeMS  *float64 `json:"actual_time_ms"`
	ActualRows    *float64 `json:"actual_rows"`
}

// EngineError carries the engine's own message for SQL it rejected
// (SQL_EXECUTION_FAILED). Only the primary message is kept, never a DSN.
type EngineError struct{ Message string }

func (e *EngineError) Error() string { return e.Message }
