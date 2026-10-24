package schemadiff

import (
	"context"
	"time"

	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/contracts"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/database"
)

type DiffChangeKind string

const (
	AddTable        DiffChangeKind = "add_table"
	DropTable       DiffChangeKind = "drop_table"
	AlterTable      DiffChangeKind = "alter_table"
	AddColumn       DiffChangeKind = "add_column"
	DropColumn      DiffChangeKind = "drop_column"
	AlterColumn     DiffChangeKind = "alter_column"
	AddIndex        DiffChangeKind = "add_index"
	DropIndex       DiffChangeKind = "drop_index"
	AlterIndex      DiffChangeKind = "alter_index"
	AddConstraint   DiffChangeKind = "add_constraint"
	DropConstraint  DiffChangeKind = "drop_constraint"
	AlterConstraint DiffChangeKind = "alter_constraint"
	Other           DiffChangeKind = "other"
)

type DiffInput struct {
	DesiredDatabaseID string `json:"desired_database_id"`
	Schema            string `json:"schema"`
}

type DiffChange struct {
	Kind        DiffChangeKind `json:"kind"`
	Object      string         `json:"object"`
	Destructive bool           `json:"destructive"`
}

type DiffResult struct {
	Driver             database.Driver `json:"driver"`
	ExpectedVersion    int64           `json:"expected_version"`
	SourceFingerprint  string          `json:"source_fingerprint"`
	DesiredFingerprint string          `json:"desired_fingerprint"`
	Changes            []DiffChange    `json:"changes"`
	CandidateSQL       string          `json:"candidate_sql"`
	Warnings           []string        `json:"warnings"`
	Executable         bool            `json:"executable"`
}

type TargetProvider interface {
	database.TargetProvider
	Driver(context.Context, string) (database.Driver, error)
}

type HeadReader func(context.Context, contracts.Principal, string) (int64, error)

type Comparison struct {
	Source, Desired database.SchemaSnapshot
	Schema          string
	Changes         []DiffChange
}

type Candidate struct {
	SQL        string
	Warnings   []string
	Executable bool
}

type Generator interface {
	Generate(context.Context, Comparison) (Candidate, error)
}

type Options struct {
	Timeout    time.Duration
	Generators map[database.Driver]Generator
}
