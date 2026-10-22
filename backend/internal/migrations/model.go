package migrations

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"time"

	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/contracts"
)

const (
	MaxSQLBytes          = 256 << 10
	maxInteger     int64 = 1<<53 - 1
	Running              = "running"
	Succeeded            = "succeeded"
	Failed               = "failed"
	OutcomeUnknown       = "outcome_unknown"
)

var uuidPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
var fingerprintPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

type MigrationDraft struct {
	ID                string     `json:"id"`
	DatabaseID        string     `json:"database_id"`
	Title             string     `json:"title"`
	SQL               string     `json:"sql"`
	Revision          int64      `json:"revision"`
	ExpectedVersion   int64      `json:"expected_version"`
	SourceFingerprint *string    `json:"source_fingerprint"`
	AppliedAt         *time.Time `json:"applied_at"`
	CreatedBy         string     `json:"created_by"`
	UpdatedBy         string     `json:"updated_by"`
	CreatedAt         time.Time  `json:"created_at"`
	UpdatedAt         time.Time  `json:"updated_at"`
}

type Actor struct {
	ID       string `json:"id"`
	Username string `json:"username"`
}

type RunError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type MigrationRun struct {
	ID               string     `json:"id"`
	DatabaseID       string     `json:"database_id"`
	DraftID          string     `json:"draft_id"`
	DraftRevision    int64      `json:"draft_revision"`
	CandidateVersion int64      `json:"candidate_version"`
	Version          *int64     `json:"version"`
	SQL              string     `json:"sql"`
	SQLHash          string     `json:"sql_hash"`
	Actor            Actor      `json:"actor"`
	Status           string     `json:"status"`
	StartedAt        time.Time  `json:"started_at"`
	FinishedAt       *time.Time `json:"finished_at"`
	Error            *RunError  `json:"error"`
	inputHash        []byte
}

type MigrationHead struct {
	Version   int64   `json:"version"`
	LastRunID *string `json:"last_run_id"`
}

type DraftInput struct {
	Title             string
	SQL               string
	ExpectedVersion   int64
	SourceFingerprint *string
}

type UpdateDraftInput struct {
	DraftInput
	Revision            int64
	FingerprintProvided bool
}

type ExecuteInput struct {
	DatabaseID      string
	DraftID         string
	Revision        int64
	ExpectedVersion int64
	IdempotencyKey  string
}

func newID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = b[6]&15 | 64
	b[8] = b[8]&63 | 128
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[:4], b[4:6], b[6:8], b[8:10], b[10:]), nil
}

func sqlHash(query string) string {
	h := sha256.Sum256([]byte(query))
	return "sha256:" + hex.EncodeToString(h[:])
}

func problem(code, message string) *contracts.DomainError {
	return &contracts.DomainError{Code: code, Message: message}
}

func internal(err error) error {
	return &contracts.DomainError{Code: "INTERNAL_ERROR", Message: "Operasi migrasi tidak dapat diselesaikan.", Cause: err}
}

func unresolved(r MigrationRun) bool { return r.Status == Running || r.Status == OutcomeUnknown }
