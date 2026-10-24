package schemadiff

import (
	"context"
	"errors"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/contracts"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/database"
)

var uuidPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

const maxVersion int64 = 1<<53 - 2

type Service struct {
	provider   TargetProvider
	inspector  database.Inspector
	access     contracts.AccessChecker
	head       HeadReader
	generators map[database.Driver]Generator
	timeout    time.Duration
}

func NewService(provider TargetProvider, inspector database.Inspector, access contracts.AccessChecker, head HeadReader, opts Options) *Service {
	if opts.Timeout <= 0 {
		opts.Timeout = 30 * time.Second
	}
	generators := make(map[database.Driver]Generator, len(opts.Generators))
	for driver, generator := range opts.Generators {
		generators[driver] = generator
	}
	return &Service{provider, inspector, access, head, generators, opts.Timeout}
}

func (s *Service) Diff(ctx context.Context, p contracts.Principal, sourceID string, in DiffInput) (DiffResult, error) {
	if p.UserID == "" {
		return DiffResult{}, problem("UNAUTHENTICATED", "Silakan login untuk melanjutkan.")
	}
	if !uuidPattern.MatchString(sourceID) || !uuidPattern.MatchString(in.DesiredDatabaseID) || !utf8.ValidString(in.Schema) || strings.TrimSpace(in.Schema) == "" || strings.ContainsRune(in.Schema, 0) {
		return DiffResult{}, problem("INVALID_REQUEST", "Database dan schema pembanding harus valid.")
	}
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	if err := s.access.Require(ctx, p, contracts.EditDatabase, sourceID); err != nil {
		return DiffResult{}, domain(err)
	}
	if err := s.access.Require(ctx, p, contracts.ViewDatabase, in.DesiredDatabaseID); err != nil {
		return DiffResult{}, domain(err)
	}
	if database.Reserved(in.Schema, "") {
		return DiffResult{}, problem("NOT_FOUND", "Schema tidak ditemukan.")
	}
	driver, err := s.provider.Driver(ctx, sourceID)
	if err != nil {
		return DiffResult{}, domain(err)
	}
	desiredDriver, err := s.provider.Driver(ctx, in.DesiredDatabaseID)
	if err != nil {
		return DiffResult{}, domain(err)
	}
	if !driver.Valid() || driver != desiredDriver || (driver == database.SQLite && in.Schema != "main") {
		return DiffResult{}, problem("DIFF_UNSUPPORTED", "Perbandingan memerlukan engine yang sama dan schema yang didukung.")
	}
	source, release, err := s.provider.Acquire(ctx, sourceID)
	if err != nil {
		return DiffResult{}, domain(err)
	}
	defer release()
	if source.Driver != driver || source.DatabaseID != sourceID {
		return DiffResult{}, problem("TARGET_UNAVAILABLE", "Konfigurasi target berubah selama perbandingan.")
	}
	if !source.MigrationReady {
		return DiffResult{}, problem("TARGET_NOT_READY", "Uji koneksi source untuk menyiapkan ledger terlebih dahulu.")
	}
	desired := source
	if sourceID != in.DesiredDatabaseID {
		var done func()
		desired, done, err = s.provider.Acquire(ctx, in.DesiredDatabaseID)
		if err != nil {
			return DiffResult{}, domain(err)
		}
		defer done()
	}
	if desired.Driver != driver || desired.DatabaseID != in.DesiredDatabaseID {
		return DiffResult{}, problem("TARGET_UNAVAILABLE", "Konfigurasi pembanding berubah selama perbandingan.")
	}
	version, err := s.readVersion(ctx, p, sourceID)
	if err != nil {
		return DiffResult{}, err
	}
	from, err := s.snapshot(ctx, source)
	if err != nil {
		return DiffResult{}, err
	}
	to := from
	if sourceID != in.DesiredDatabaseID {
		to, err = s.snapshot(ctx, desired)
		if err != nil {
			return DiffResult{}, err
		}
	}
	fromSchema, fromFound := findSchema(from, in.Schema)
	toSchema, toFound := findSchema(to, in.Schema)
	if !fromFound || !toFound {
		return DiffResult{}, problem("NOT_FOUND", "Schema harus tersedia pada source dan pembanding.")
	}
	changes, warnings, err := compare(fromSchema, toSchema)
	if err != nil {
		return DiffResult{}, domain(err)
	}
	result := DiffResult{Driver: driver, ExpectedVersion: version, SourceFingerprint: from.Fingerprint, DesiredFingerprint: to.Fingerprint, Changes: changes, Warnings: warnings, Executable: len(changes) == 0}
	if len(changes) > 0 || s.generators[driver] != nil {
		if generator := s.generators[driver]; generator != nil {
			candidate, err := generator.Generate(ctx, Comparison{from, to, in.Schema, append([]DiffChange{}, changes...)})
			if err != nil {
				var domainError *contracts.DomainError
				if ctx.Err() != nil {
					return DiffResult{}, domain(ctx.Err())
				}
				if errors.As(err, &domainError) {
					return DiffResult{}, domain(err)
				}
				mapped := domain(err)
				if errors.As(mapped, &domainError) && domainError.Code != "INTERNAL_ERROR" {
					return DiffResult{}, mapped
				}
				return DiffResult{}, &contracts.DomainError{Code: "DIFF_UNAVAILABLE", Message: "Kandidat SQL belum dapat dibuat.", Cause: err}
			}
			result.Changes = append(result.Changes, candidate.Changes...)
			sort.Slice(result.Changes, func(i, j int) bool {
				if result.Changes[i].Object != result.Changes[j].Object {
					return result.Changes[i].Object < result.Changes[j].Object
				}
				return result.Changes[i].Kind < result.Changes[j].Kind
			})
			if candidate.Executable && (!utf8.ValidString(candidate.SQL) || strings.ContainsRune(candidate.SQL, 0) || len(candidate.SQL) > 256<<10 || (len(result.Changes) > 0 && strings.TrimSpace(candidate.SQL) == "")) {
				return DiffResult{}, problem("DIFF_UNAVAILABLE", "Kandidat SQL tidak dapat disimpan sebagai draft.")
			}
			result.Executable = candidate.Executable
			result.Warnings = append(result.Warnings, candidate.Warnings...)
			if candidate.Executable {
				result.CandidateSQL = candidate.SQL
			} else if len(candidate.Warnings) == 0 {
				result.Warnings = append(result.Warnings, "Perubahan schema ini belum didukung generator SQL.")
			}
		} else {
			result.Warnings = append(result.Warnings, "Generator kandidat SQL untuk engine ini belum tersedia.")
		}
	}
	if driver == database.Postgres && s.generators[driver] != nil {
		for _, target := range []struct {
			handle      database.TargetHandle
			fingerprint string
		}{{source, from.Fingerprint}, {desired, to.Fingerprint}} {
			snap, err := s.snapshot(ctx, target.handle)
			if err != nil {
				return DiffResult{}, err
			}
			if snap.Fingerprint != target.fingerprint {
				return DiffResult{}, problem("SCHEMA_CONFLICT", "Schema berubah selama Atlas membuat kandidat; ulangi perbandingan.")
			}
		}
	}
	latest, err := s.readVersion(ctx, p, sourceID)
	if err != nil {
		return DiffResult{}, err
	}
	if latest != version {
		return DiffResult{}, problem("VERSION_CONFLICT", "Head source berubah selama perbandingan; ulangi perbandingan.")
	}
	result.Warnings = uniqueWarnings(result.Warnings)
	return result, nil
}

func (s *Service) readVersion(ctx context.Context, p contracts.Principal, id string) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, domain(err)
	}
	if s.head == nil {
		return 0, problem("DIFF_UNAVAILABLE", "Pembaca versi migrasi belum tersedia.")
	}
	version, err := s.head(ctx, p, id)
	if err != nil {
		return 0, domain(err)
	}
	if version < 0 || version > maxVersion {
		return 0, problem("INVALID_REQUEST", "Versi source tidak dapat dipakai untuk draft baru.")
	}
	return version, nil
}

func (s *Service) snapshot(ctx context.Context, h database.TargetHandle) (database.SchemaSnapshot, error) {
	snap, err := s.inspector.Inspect(ctx, h)
	if err != nil {
		return database.SchemaSnapshot{}, domain(err)
	}
	if snap.Driver != h.Driver || snap.DatabaseID != h.DatabaseID {
		return database.SchemaSnapshot{}, domain(database.ErrInvalidSnapshot)
	}
	snap.Fingerprint, err = database.Fingerprint(snap)
	if err != nil {
		return database.SchemaSnapshot{}, domain(err)
	}
	return snap, nil
}

func findSchema(snap database.SchemaSnapshot, name string) (database.Schema, bool) {
	for _, schema := range snap.Schemas {
		if schema.Name == name {
			return schema, true
		}
	}
	return database.Schema{}, false
}

func uniqueWarnings(warnings []string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, warning := range warnings {
		if warning != "" && !seen[warning] {
			out = append(out, warning)
			seen[warning] = true
		}
	}
	sort.Strings(out)
	return out
}

func problem(code, message string) *contracts.DomainError {
	return &contracts.DomainError{Code: code, Message: message}
}

func domain(err error) error {
	var d *contracts.DomainError
	if errors.As(err, &d) {
		return err
	}
	code, message := "INTERNAL_ERROR", "Perbandingan schema tidak dapat diselesaikan."
	switch {
	case errors.Is(err, database.ErrNotFound):
		code, message = "NOT_FOUND", "Database atau schema tidak ditemukan."
	case errors.Is(err, database.ErrTargetTimeout), errors.Is(err, context.DeadlineExceeded):
		code, message = "TARGET_TIMEOUT", "Batas waktu perbandingan database terlampaui."
	case errors.Is(err, database.ErrTargetUnavailable), errors.Is(err, context.Canceled):
		code, message = "TARGET_UNAVAILABLE", "Database tidak dapat dihubungi."
	case errors.Is(err, database.ErrTargetPermission):
		code, message = "FORBIDDEN", "Role target tidak memiliki akses ke schema."
	case errors.Is(err, database.ErrTargetNotReady):
		code, message = "TARGET_NOT_READY", "Ledger source belum siap."
	case errors.Is(err, database.ErrLedgerIncompatible):
		code, message = "LEDGER_INCOMPATIBLE", "Ledger source tidak sesuai."
	case errors.Is(err, database.ErrInvalidSnapshot):
		code, message = "INVALID_REQUEST", "Snapshot schema tidak valid."
	case errors.Is(err, database.ErrDiffUnsupported):
		code, message = "DIFF_UNSUPPORTED", "Perbandingan ini belum didukung."
	}
	return &contracts.DomainError{Code: code, Message: message, Cause: err}
}
