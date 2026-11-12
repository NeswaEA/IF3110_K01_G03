package migrations

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/contracts"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/database"
)

const (
	maxSQLBytes         = 256 * 1024
	maxRevision         = 9007199254740991
	DefaultBatchSizeMax = 100000
)

var fingerprintPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

type Actor struct {
	ID       string `json:"id"`
	Username string `json:"username"`
}

// BatchConfig fields are ordered by JSON key for the canonical revision hash.
type BatchConfig struct {
	BatchSize int    `json:"batch_size"`
	KeyColumn string `json:"key_column"`
	PauseMS   int    `json:"pause_ms"`
	Schema    string `json:"schema"`
	Table     string `json:"table"`
}

func (b *BatchConfig) UnmarshalJSON(raw []byte) error {
	type fields BatchConfig
	input := struct {
		*fields
		PauseMS *int `json:"pause_ms"`
	}{fields: (*fields)(b)}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		return err
	}
	if input.PauseMS == nil {
		return invalidField("batch", "Isi pause_ms pada konfigurasi batch.")
	}
	b.PauseMS = *input.PauseMS
	return nil
}

type revisionContent struct {
	UpSQL              string       `json:"up_sql"`
	DownSQL            *string      `json:"down_sql"`
	IrreversibleReason *string      `json:"irreversible_reason"`
	Mode               string       `json:"mode"`
	Batch              *BatchConfig `json:"batch"`
}

type Revision struct {
	Number int64 `json:"revision"`
	revisionContent
	ContentHash string        `json:"content_hash"`
	Lint        []LintFinding `json:"lint"`
	CreatedBy   Actor         `json:"created_by"`
	CreatedAt   time.Time     `json:"created_at"`
}

type Migration struct {
	ID                string          `json:"id"`
	DatabaseID        string          `json:"database_id"`
	Title             string          `json:"title"`
	Description       string          `json:"description"`
	Status            string          `json:"status"`
	Version           *int64          `json:"version"`
	Locked            bool            `json:"locked"`
	Current           Revision        `json:"current"`
	SourceFingerprint *string         `json:"source_fingerprint"`
	OpenRequest       *ChangeRequest  `json:"open_request"`
	LatestDryRun      json.RawMessage `json:"latest_dry_run"`
	CreatedBy         Actor           `json:"created_by"`
	UpdatedBy         Actor           `json:"updated_by"`
	CreatedAt         time.Time       `json:"created_at"`
	UpdatedAt         time.Time       `json:"updated_at"`
}

type createInput struct {
	Title             string  `json:"title"`
	Description       string  `json:"description"`
	SourceFingerprint *string `json:"source_fingerprint"`
	revisionContent
}

// patchField distinguishes omission from an explicit null when clearing SQL down,
// the irreversible reason, or batch settings.
type patchField[T any] struct {
	Present bool
	Value   *T
}

func (f *patchField[T]) UnmarshalJSON(raw []byte) error {
	f.Present = true
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	return decoder.Decode(&f.Value)
}

type patchInput struct {
	Revision           int64                   `json:"revision"`
	Title              patchField[string]      `json:"title"`
	Description        patchField[string]      `json:"description"`
	UpSQL              patchField[string]      `json:"up_sql"`
	DownSQL            patchField[string]      `json:"down_sql"`
	IrreversibleReason patchField[string]      `json:"irreversible_reason"`
	Mode               patchField[string]      `json:"mode"`
	Batch              patchField[BatchConfig] `json:"batch"`
}

func (in patchInput) apply(m *Migration) error {
	for _, field := range []struct {
		name   string
		patch  patchField[string]
		target *string
	}{
		{"title", in.Title, &m.Title},
		{"description", in.Description, &m.Description},
		{"up_sql", in.UpSQL, &m.Current.UpSQL},
		{"mode", in.Mode, &m.Current.Mode},
	} {
		if field.patch.Present {
			if field.patch.Value == nil {
				return invalidField(field.name, "Nilai tidak boleh null.")
			}
			*field.target = *field.patch.Value
		}
	}
	if in.DownSQL.Present {
		m.Current.DownSQL = in.DownSQL.Value
	}
	if in.IrreversibleReason.Present {
		m.Current.IrreversibleReason = in.IrreversibleReason.Value
	}
	if in.Batch.Present {
		m.Current.Batch = in.Batch.Value
	}
	return nil
}

func invalidField(field, message string) error {
	return &contracts.DomainError{
		Code: "INVALID_REQUEST", Message: "Data migrasi tidak valid.",
		Fields: map[string]string{field: message},
	}
}

func validText(value string, min, max int) bool {
	n := utf8.RuneCountInString(value)
	return n >= min && n <= max && utf8.ValidString(value) && !strings.ContainsRune(value, 0)
}

func validateLabels(title, description string) error {
	if !validText(title, 1, 200) {
		return invalidField("title", "Isi judul 1–200 karakter.")
	}
	if !validText(description, 0, 2000) {
		return invalidField("description", "Deskripsi maksimal 2000 karakter.")
	}
	return nil
}

func (r revisionContent) validate(driver database.Driver, batchSizeMax int) error {
	if (r.DownSQL == nil) == (r.IrreversibleReason == nil) {
		return invalidField("down_sql", "Isi SQL down atau alasan irreversible.")
	}
	if r.IrreversibleReason != nil && !validText(*r.IrreversibleReason, 1, 500) {
		return invalidField("irreversible_reason", "Isi alasan 1–500 karakter.")
	}
	for _, sql := range []struct {
		field string
		value *string
	}{{"up_sql", &r.UpSQL}, {"down_sql", r.DownSQL}} {
		if sql.value == nil {
			continue
		}
		if len(*sql.value) > maxSQLBytes {
			return &contracts.DomainError{Code: "PAYLOAD_TOO_LARGE", Message: "SQL melebihi 256 KiB.",
				Fields: map[string]string{sql.field: "SQL maksimal 256 KiB."}}
		}
		if strings.TrimSpace(*sql.value) == "" || strings.ContainsRune(*sql.value, 0) {
			return invalidField(sql.field, "Isi SQL yang valid.")
		}
	}
	switch r.Mode {
	case "normal":
		if r.Batch != nil {
			return invalidField("batch", "Konfigurasi batch hanya tersedia pada mode batch.")
		}
	case "batch":
		if driver != database.Postgres {
			return problem("UNSUPPORTED_MODE", "Database ini tidak mendukung mode batch.")
		}
		b := r.Batch
		if b == nil || !validBatchIdentifier(b.Schema) || !validBatchIdentifier(b.Table) ||
			!validBatchIdentifier(b.KeyColumn) || b.BatchSize < 1 || b.BatchSize > batchSizeMax ||
			b.PauseMS < 0 || b.PauseMS > 60000 {
			return invalidField("batch", "Konfigurasi batch tidak valid.")
		}
	default:
		return invalidField("mode", "Pilih mode normal atau batch.")
	}
	return nil
}

func validBatchIdentifier(value string) bool {
	return strings.TrimSpace(value) != "" && !strings.ContainsRune(value, 0)
}

func (r *revisionContent) trimReason() {
	if r.IrreversibleReason != nil {
		trimmed := strings.TrimSpace(*r.IrreversibleReason)
		r.IrreversibleReason = &trimmed
	}
}

func (r revisionContent) hash() ([sha256.Size]byte, error) {
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode([5]any{r.UpSQL, r.DownSQL, r.IrreversibleReason, r.Mode, r.Batch}); err != nil {
		return [sha256.Size]byte{}, err
	}
	return sha256.Sum256(bytes.TrimSuffix(buf.Bytes(), []byte("\n"))), nil
}

func hashString(hash []byte) string {
	return "sha256:" + hex.EncodeToString(hash)
}
