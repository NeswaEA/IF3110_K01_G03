package migrations

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/contracts"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/database"
)

type Options struct{ Timeout time.Duration }
type runFinalizer func(context.Context, MigrationRun) error

type Service struct {
	store     *Store
	provider  database.TargetProvider
	inspector database.Inspector
	access    contracts.AccessChecker
	clock     contracts.Clock
	timeout   time.Duration
	finalize  runFinalizer
	mu        sync.Mutex
	active    map[string]bool
}

func NewService(meta *sql.DB, provider database.TargetProvider, inspector database.Inspector, access contracts.AccessChecker, clock contracts.Clock, opts Options) *Service {
	if opts.Timeout <= 0 {
		opts.Timeout = 30 * time.Second
	}
	store := NewStore(meta)
	return &Service{store: store, provider: provider, inspector: inspector, access: access, clock: clock, timeout: opts.Timeout, finalize: store.finalize, active: map[string]bool{}}
}

func isCode(err error, code string) bool {
	var d *contracts.DomainError
	return errors.As(err, &d) && d.Code == code
}

func (s *Service) authorize(ctx context.Context, p contracts.Principal, databaseID string, action contracts.Action) error {
	if p.UserID == "" {
		return problem("UNAUTHENTICATED", "Silakan login untuk melanjutkan.")
	}
	if !contracts.ValidID(databaseID) {
		return problem("INVALID_REQUEST", "ID database tidak valid.")
	}
	return s.access.Require(ctx, p, action, databaseID)
}

func validateDraft(in DraftInput) error {
	if !utf8.ValidString(in.Title) || utf8.RuneCountInString(strings.TrimSpace(in.Title)) < 1 || utf8.RuneCountInString(strings.TrimSpace(in.Title)) > 200 {
		return problem("INVALID_REQUEST", "Judul harus 1 sampai 200 karakter.")
	}
	if len(in.SQL) > MaxSQLBytes {
		return problem("PAYLOAD_TOO_LARGE", "SQL maksimal 256 KiB.")
	}
	if !utf8.ValidString(in.SQL) || strings.TrimSpace(in.SQL) == "" || strings.ContainsRune(in.SQL, 0) {
		return problem("INVALID_REQUEST", "SQL tidak boleh kosong atau memuat NUL.")
	}
	if in.ExpectedVersion < 0 || in.ExpectedVersion >= maxInteger {
		return problem("INVALID_REQUEST", "Versi tidak valid.")
	}
	if in.SourceFingerprint != nil && !fingerprintPattern.MatchString(*in.SourceFingerprint) {
		return problem("INVALID_REQUEST", "Fingerprint tidak valid.")
	}
	return nil
}

func (s *Service) CreateDraft(ctx context.Context, p contracts.Principal, databaseID string, in DraftInput) (MigrationDraft, error) {
	if err := s.authorize(ctx, p, databaseID, contracts.EditDatabase); err != nil {
		return MigrationDraft{}, err
	}
	if err := validateDraft(in); err != nil {
		return MigrationDraft{}, err
	}
	id, err := newID()
	if err != nil {
		return MigrationDraft{}, internal(err)
	}
	now := s.clock.Now().UTC()
	return scanDraft(s.store.DB.QueryRowContext(ctx, `INSERT INTO migration_drafts
	 (id,database_id,title,sql,expected_version,source_fingerprint,created_by,updated_by,created_at,updated_at)
	 VALUES ($1,$2,$3,$4,$5,$6,$7,$7,$8,$8) RETURNING `+draftColumns, id, databaseID, strings.TrimSpace(in.Title), in.SQL, in.ExpectedVersion, in.SourceFingerprint, p.UserID, now))
}

func (s *Service) GetDraft(ctx context.Context, p contracts.Principal, databaseID, id string) (MigrationDraft, error) {
	if err := s.authorize(ctx, p, databaseID, contracts.EditDatabase); err != nil {
		return MigrationDraft{}, err
	}
	if !contracts.ValidID(id) {
		return MigrationDraft{}, problem("INVALID_REQUEST", "ID draft tidak valid.")
	}
	return getDraft(ctx, s.store.DB, databaseID, id, false)
}

func (s *Service) UpdateDraft(ctx context.Context, p contracts.Principal, databaseID, id string, in UpdateDraftInput) (MigrationDraft, error) {
	if err := s.authorize(ctx, p, databaseID, contracts.EditDatabase); err != nil {
		return MigrationDraft{}, err
	}
	if !contracts.ValidID(id) || in.Revision < 1 || in.Revision >= maxInteger {
		return MigrationDraft{}, problem("INVALID_REQUEST", "Draft atau revision tidak valid.")
	}
	if err := validateDraft(in.DraftInput); err != nil {
		return MigrationDraft{}, err
	}
	tx, err := s.store.DB.BeginTx(ctx, nil)
	if err != nil {
		return MigrationDraft{}, internal(err)
	}
	defer tx.Rollback()
	d, err := getDraft(ctx, tx, databaseID, id, true)
	if err != nil {
		return MigrationDraft{}, err
	}
	if d.AppliedAt != nil {
		return MigrationDraft{}, problem("DRAFT_APPLIED", "Draft sudah diterapkan; buat draft baru.")
	}
	if d.Revision != in.Revision {
		return MigrationDraft{}, problem("REVISION_CONFLICT", "Draft berubah sejak dibaca; muat ulang tanpa menimpa SQL lokal.")
	}
	var busy bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM migration_runs WHERE draft_id=$1 AND status IN ('running','outcome_unknown'))`, id).Scan(&busy); err != nil {
		return MigrationDraft{}, internal(err)
	}
	if busy {
		return MigrationDraft{}, problem("RUN_IN_PROGRESS", "Hasil eksekusi draft belum selesai.")
	}
	if !in.FingerprintProvided {
		in.SourceFingerprint = d.SourceFingerprint
	}
	if d.SourceFingerprint != nil && in.SourceFingerprint == nil {
		return MigrationDraft{}, problem("INVALID_REQUEST", "Fingerprint draft hasil diff tidak boleh dihapus.")
	}
	d, err = scanDraft(tx.QueryRowContext(ctx, `UPDATE migration_drafts SET title=$3,sql=$4,expected_version=$5,source_fingerprint=$6,
	 revision=revision+1,updated_by=$7,updated_at=$8 WHERE database_id=$1 AND id=$2 RETURNING `+draftColumns, databaseID, id, strings.TrimSpace(in.Title), in.SQL, in.ExpectedVersion, in.SourceFingerprint, p.UserID, s.clock.Now().UTC()))
	if err != nil {
		return MigrationDraft{}, err
	}
	if err := tx.Commit(); err != nil {
		return MigrationDraft{}, internal(err)
	}
	return d, nil
}

func validPage(limit, offset int) bool {
	return limit >= 1 && limit <= 200 && offset >= 0 && offset <= 100000
}

func (s *Service) ListDrafts(ctx context.Context, p contracts.Principal, databaseID string, limit, offset int) ([]MigrationDraft, bool, error) {
	if err := s.authorize(ctx, p, databaseID, contracts.EditDatabase); err != nil {
		return nil, false, err
	}
	if !validPage(limit, offset) {
		return nil, false, problem("INVALID_REQUEST", "Pagination tidak valid.")
	}
	rows, err := s.store.DB.QueryContext(ctx, `SELECT `+draftColumns+` FROM migration_drafts WHERE database_id=$1 ORDER BY updated_at DESC,id DESC LIMIT $2 OFFSET $3`, databaseID, limit+1, offset)
	if err != nil {
		return nil, false, internal(err)
	}
	defer rows.Close()
	out := []MigrationDraft{}
	for rows.Next() {
		d, e := scanDraft(rows)
		if e != nil {
			return nil, false, e
		}
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		return nil, false, internal(err)
	}
	more := len(out) > limit
	if more {
		out = out[:limit]
	}
	return out, more, nil
}

func (s *Service) ListRuns(ctx context.Context, p contracts.Principal, databaseID string, limit, offset int) ([]MigrationRun, bool, error) {
	if err := s.authorize(ctx, p, databaseID, contracts.ViewDatabase); err != nil {
		return nil, false, err
	}
	if !validPage(limit, offset) {
		return nil, false, problem("INVALID_REQUEST", "Pagination tidak valid.")
	}
	rows, err := s.store.DB.QueryContext(ctx, `SELECT `+runColumns+` FROM migration_runs WHERE database_id=$1 ORDER BY started_at DESC,id DESC LIMIT $2 OFFSET $3`, databaseID, limit+1, offset)
	if err != nil {
		return nil, false, internal(err)
	}
	defer rows.Close()
	out := []MigrationRun{}
	for rows.Next() {
		r, e := scanRun(rows)
		if e != nil {
			return nil, false, e
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, false, internal(err)
	}
	more := len(out) > limit
	if more {
		out = out[:limit]
	}
	return out, more, nil
}

func (s *Service) GetRun(ctx context.Context, p contracts.Principal, databaseID, id string) (MigrationRun, error) {
	if err := s.authorize(ctx, p, databaseID, contracts.ViewDatabase); err != nil {
		return MigrationRun{}, err
	}
	if !contracts.ValidID(id) {
		return MigrationRun{}, problem("INVALID_REQUEST", "ID run tidak valid.")
	}
	return s.store.run(ctx, databaseID, id)
}

func (s *Service) lease(id string) (func(), bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.active[id] {
		return nil, false
	}
	s.active[id] = true
	return func() { s.mu.Lock(); delete(s.active, id); s.mu.Unlock() }, true
}

func inputHash(in ExecuteInput) []byte {
	b, _ := json.Marshal([]any{in.DatabaseID, in.DraftID, in.Revision, in.ExpectedVersion})
	h := sha256.Sum256(b)
	return h[:]
}

func compareKey(r MigrationRun, hash []byte) (MigrationRun, error) {
	if !bytes.Equal(r.inputHash, hash) {
		return MigrationRun{}, problem("IDEMPOTENCY_CONFLICT", "Key sudah digunakan untuk input berbeda.")
	}
	return r, nil
}

func (s *Service) createRun(ctx context.Context, p contracts.Principal, in ExecuteInput, hash []byte) (MigrationRun, MigrationDraft, bool, error) {
	tx, err := s.store.DB.BeginTx(ctx, nil)
	if err != nil {
		return MigrationRun{}, MigrationDraft{}, false, internal(err)
	}
	defer tx.Rollback()
	var active bool
	err = tx.QueryRowContext(ctx, `SELECT active FROM database_connections WHERE id=$1 FOR UPDATE`, in.DatabaseID).Scan(&active)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && !active) {
		return MigrationRun{}, MigrationDraft{}, false, problem("NOT_FOUND", "Database tidak ditemukan.")
	}
	if err != nil {
		return MigrationRun{}, MigrationDraft{}, false, internal(err)
	}
	if r, found, e := findKey(ctx, tx, in.DatabaseID, in.IdempotencyKey); e != nil || found {
		if e == nil {
			r, e = compareKey(r, hash)
		}
		return r, MigrationDraft{}, false, e
	}
	d, err := getDraft(ctx, tx, in.DatabaseID, in.DraftID, true)
	if err != nil {
		return MigrationRun{}, d, false, err
	}
	if d.AppliedAt != nil {
		return MigrationRun{}, d, false, problem("DRAFT_APPLIED", "Draft sudah diterapkan; buat draft baru.")
	}
	if d.Revision != in.Revision {
		return MigrationRun{}, d, false, problem("REVISION_CONFLICT", "Revision draft berubah.")
	}
	if d.ExpectedVersion != in.ExpectedVersion {
		return MigrationRun{}, d, false, problem("VERSION_CONFLICT", "Versi draft berubah; muat ulang.")
	}
	var pending bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM migration_runs WHERE database_id=$1 AND status IN ('running','outcome_unknown'))`, in.DatabaseID).Scan(&pending); err != nil {
		return MigrationRun{}, d, false, internal(err)
	}
	if pending {
		return MigrationRun{}, d, false, problem("TARGET_OUTCOME_UNKNOWN", "Rekonsiliasi run sebelumnya sebelum menjalankan SQL baru.")
	}
	id, err := newID()
	if err != nil {
		return MigrationRun{}, d, false, internal(err)
	}
	sum := sha256.Sum256([]byte(d.SQL))
	r, err := scanRun(tx.QueryRowContext(ctx, `INSERT INTO migration_runs
	 (id,database_id,draft_id,draft_revision,idempotency_key,input_hash,sql_snapshot,sql_hash,actor_id,actor_name_snapshot,candidate_version,status,started_at)
	 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,'running',$12) RETURNING `+runColumns,
		id, in.DatabaseID, in.DraftID, in.Revision, in.IdempotencyKey, hash, d.SQL, sum[:], p.UserID, p.Username, in.ExpectedVersion+1, s.clock.Now().UTC()))
	if err != nil {
		return MigrationRun{}, d, false, err
	}
	if err := tx.Commit(); err != nil {
		return MigrationRun{}, d, false, internal(err)
	}
	return r, d, true, nil
}
