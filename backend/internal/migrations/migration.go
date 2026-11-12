package migrations

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"
	"strings"

	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/contracts"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/database"
	"github.com/google/uuid"
)

type migrationRow struct {
	Migration
	openRequestID *string
	contentHash   []byte
}

type revisionData struct {
	batch, lint, hash []byte
}

func (d *revisionData) destinations(r *Revision) []any {
	return []any{&r.Number, &r.UpSQL, &r.DownSQL, &r.IrreversibleReason, &r.Mode, &d.batch, &d.hash, &d.lint,
		&r.CreatedBy.ID, &r.CreatedBy.Username, &r.CreatedAt}
}

func (d revisionData) decode(r *Revision) error {
	r.ContentHash = hashString(d.hash)
	if d.batch != nil {
		if err := json.Unmarshal(d.batch, &r.Batch); err != nil {
			return err
		}
	}
	return json.Unmarshal(d.lint, &r.Lint)
}

type scanner interface{ Scan(...any) error }

const revisionColumns = `r.revision, r.up_sql, r.down_sql, r.irreversible_reason, r.mode, r.batch, r.content_hash, r.lint,
	r.created_by, author.username, r.created_at`

const migrationSelect = `SELECT m.id, m.database_id, m.title, m.description, m.status, m.version, m.locked_at IS NOT NULL,
	m.source_fingerprint, m.created_by, creator.username, m.updated_by, updater.username, m.created_at, m.updated_at,
	(SELECT cr.id FROM change_requests cr WHERE cr.migration_id=m.id AND cr.status IN ('pending_review','approved')),
	(SELECT e.result FROM migration_executions e
		WHERE e.migration_id=m.id AND e.action='dry_run' AND e.migration_revision=r.revision AND e.result IS NOT NULL
		ORDER BY e.number DESC LIMIT 1), ` + revisionColumns + `
	FROM migrations m
	JOIN migration_revisions r ON r.migration_id=m.id AND r.revision=m.current_revision
	JOIN users author ON author.id=r.created_by
	JOIN users creator ON creator.id=m.created_by
	JOIN users updater ON updater.id=m.updated_by`

func migrationNotFound() error {
	return problem("NOT_FOUND", "Migrasi tidak ditemukan.")
}

func scanMigration(row scanner) (migrationRow, error) {
	var m migrationRow
	var revision revisionData
	var latestDryRun []byte
	dest := []any{&m.ID, &m.DatabaseID, &m.Title, &m.Description, &m.Status, &m.Version, &m.Locked,
		&m.SourceFingerprint, &m.CreatedBy.ID, &m.CreatedBy.Username, &m.UpdatedBy.ID, &m.UpdatedBy.Username,
		&m.CreatedAt, &m.UpdatedAt, &m.openRequestID, &latestDryRun}
	if err := row.Scan(append(dest, revision.destinations(&m.Current)...)...); err != nil {
		return m, err
	}
	m.LatestDryRun = latestDryRun
	m.contentHash = revision.hash
	return m, revision.decode(&m.Current)
}

func readMigration(ctx context.Context, q contracts.Querier, databaseID, migrationID string) (migrationRow, error) {
	m, err := scanMigration(q.QueryRowContext(ctx, migrationSelect+` WHERE m.database_id=$1 AND m.id=$2`, databaseID, migrationID))
	if errors.Is(err, sql.ErrNoRows) {
		return m, migrationNotFound()
	}
	if err != nil {
		return m, internal(err)
	}
	return m, nil
}

func lockMigration(ctx context.Context, tx *sql.Tx, databaseID, migrationID string) error {
	// Take the lock before joining the current revision. A concurrent edit may have inserted
	// that revision after the lock statement's snapshot was taken.
	var id string
	err := tx.QueryRowContext(ctx, `SELECT id FROM migrations WHERE database_id=$1 AND id=$2 FOR UPDATE`,
		databaseID, migrationID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return migrationNotFound()
	}
	if err != nil {
		return internal(err)
	}
	return nil
}

func (s *Service) renderMigration(ctx context.Context, q contracts.Querier, m migrationRow) (Migration, error) {
	if m.openRequestID != nil {
		r, err := s.readRequest(ctx, q, m.DatabaseID, *m.openRequestID)
		if err != nil {
			return Migration{}, err
		}
		m.OpenRequest = &r
	}
	return m.Migration, nil
}

func (s *Service) getMigration(ctx context.Context, databaseID, migrationID string) (Migration, error) {
	return inTx(ctx, s.store.DB, &snapshot, func(tx *sql.Tx) (Migration, error) {
		m, err := readMigration(ctx, tx, databaseID, migrationID)
		if err != nil {
			return Migration{}, err
		}
		return s.renderMigration(ctx, tx, m)
	})
}

func (s *Service) listMigrations(ctx context.Context, databaseID, status string, limit, offset int) ([]Migration, error) {
	return inTx(ctx, s.store.DB, &snapshot, func(tx *sql.Tx) ([]Migration, error) {
		rows, err := tx.QueryContext(ctx, migrationSelect+`
			WHERE m.database_id=$1 AND ($2='' OR m.status=$2)
			ORDER BY m.updated_at DESC, m.id DESC LIMIT $3 OFFSET $4`, databaseID, status, limit, offset)
		if err != nil {
			return nil, internal(err)
		}
		pending, err := collectRows(rows, scanMigration)
		if err != nil {
			return nil, err
		}
		items := make([]Migration, 0, len(pending))
		for _, m := range pending {
			item, err := s.renderMigration(ctx, tx, m)
			if err != nil {
				return nil, err
			}
			items = append(items, item)
		}
		return items, nil
	})
}

func (s *Service) listRevisions(ctx context.Context, databaseID, migrationID string) ([]Revision, error) {
	rows, err := s.store.DB.QueryContext(ctx, `SELECT `+revisionColumns+`
		FROM migrations m JOIN migration_revisions r ON r.migration_id=m.id
		JOIN users author ON author.id=r.created_by
		WHERE m.database_id=$1 AND m.id=$2 ORDER BY r.revision DESC`, databaseID, migrationID)
	if err != nil {
		return nil, internal(err)
	}
	revisions, err := collectRows(rows, func(row scanner) (Revision, error) {
		var r Revision
		var data revisionData
		if err := row.Scan(data.destinations(&r)...); err != nil {
			return r, err
		}
		return r, data.decode(&r)
	})
	if err == nil && len(revisions) == 0 {
		return nil, migrationNotFound()
	}
	return revisions, err
}

func collectRows[T any](rows *sql.Rows, scan func(scanner) (T, error)) ([]T, error) {
	defer rows.Close()
	items := []T{}
	for rows.Next() {
		item, err := scan(rows)
		if err != nil {
			return nil, internal(err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, internal(err)
	}
	return items, nil
}

func (s *Service) lookupDriver(ctx context.Context, databaseID string) (database.Driver, error) {
	driver, err := s.drivers.Driver(ctx, databaseID)
	if errors.Is(err, database.ErrNotFound) {
		return "", problem("NOT_FOUND", "Database tidak ditemukan.")
	}
	if err != nil {
		return "", internal(err)
	}
	return driver, nil
}

func (s *Service) validateRevision(ctx context.Context, driver database.Driver, revision *Revision) error {
	if err := revision.revisionContent.validate(driver, s.batchSizeMax); err != nil {
		return err
	}
	findings, err := lintRevision(ctx, driver, revision.revisionContent)
	if err != nil {
		return internal(err)
	}
	revision.Lint = findings
	return nil
}

func insertRevision(ctx context.Context, tx *sql.Tx, migrationID string, r Revision, hash []byte) error {
	var batch []byte
	var err error
	if r.Batch != nil {
		batch, err = json.Marshal(r.Batch)
		if err != nil {
			return internal(err)
		}
	}
	lint, err := json.Marshal(r.Lint)
	if err != nil {
		return internal(err)
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO migration_revisions
		(migration_id, revision, up_sql, down_sql, irreversible_reason, mode, batch, content_hash, lint, created_by, created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`, migrationID, r.Number, r.UpSQL, r.DownSQL,
		r.IrreversibleReason, r.Mode, batch, hash, lint, r.CreatedBy.ID, r.CreatedAt)
	if err != nil {
		return internal(err)
	}
	return nil
}

func (s *Service) createMigration(ctx context.Context, actor contracts.Principal, databaseID string, in createInput) (Migration, error) {
	in.Title = strings.TrimSpace(in.Title)
	in.trimReason()
	if err := validateLabels(in.Title, in.Description); err != nil {
		return Migration{}, err
	}
	if in.SourceFingerprint != nil && !fingerprintPattern.MatchString(*in.SourceFingerprint) {
		return Migration{}, invalidField("source_fingerprint", "Fingerprint schema tidak valid.")
	}
	now := s.store.now()
	author := Actor{ID: actor.UserID, Username: actor.Username}
	r := Revision{Number: 1, revisionContent: in.revisionContent, CreatedBy: author, CreatedAt: now}
	driver, err := s.lookupDriver(ctx, databaseID)
	if err != nil {
		return Migration{}, err
	}
	if err := s.validateRevision(ctx, driver, &r); err != nil {
		return Migration{}, err
	}
	hash, err := r.hash()
	if err != nil {
		return Migration{}, internal(err)
	}
	r.ContentHash = hashString(hash[:])
	id := uuid.Must(uuid.NewV7()).String()
	m, err := inTx(ctx, s.store.DB, nil, func(tx *sql.Tx) (Migration, error) {
		_, err := tx.ExecContext(ctx, `INSERT INTO migrations
			(id, database_id, title, description, source_fingerprint, created_by, updated_by, created_at, updated_at)
			VALUES ($1,$2,$3,$4,$5,$6,$6,$7,$7)`, id, databaseID, in.Title, in.Description,
			in.SourceFingerprint, actor.UserID, now)
		if err != nil {
			return Migration{}, internal(err)
		}
		if err := insertRevision(ctx, tx, id, r, hash[:]); err != nil {
			return Migration{}, err
		}
		row, err := readMigration(ctx, tx, databaseID, id)
		return row.Migration, err
	})
	if err != nil {
		return Migration{}, err
	}
	s.recordMigration(ctx, actor, "migration.created", m)
	return m, nil
}

func (s *Service) patchMigration(ctx context.Context, actor contracts.Principal, databaseID, id string, in patchInput) (Migration, error) {
	if in.Revision < 1 || in.Revision > maxRevision {
		return Migration{}, invalidField("revision", "Nomor revisi tidak valid.")
	}
	driver, err := s.lookupDriver(ctx, databaseID)
	if err != nil {
		return Migration{}, err
	}
	type patchResult struct {
		migration Migration
		resetID   string
		changed   bool
	}
	result, err := inTx(ctx, s.store.DB, nil, func(tx *sql.Tx) (patchResult, error) {
		if err := lockMigration(ctx, tx, databaseID, id); err != nil {
			return patchResult{}, err
		}
		row, err := readMigration(ctx, tx, databaseID, id)
		if err != nil {
			return patchResult{}, err
		}
		if row.Current.Number != in.Revision {
			return patchResult{}, problem("REVISION_CONFLICT", "Revisi sudah berubah. Muat ulang migrasi.")
		}
		m := row.Migration
		if err := in.apply(&m); err != nil {
			return patchResult{}, err
		}
		m.Title = strings.TrimSpace(m.Title)
		if in.IrreversibleReason.Present {
			m.Current.trimReason()
		}
		if err := validateLabels(m.Title, m.Description); err != nil {
			return patchResult{}, err
		}
		contentChanged := !reflect.DeepEqual(m.Current.revisionContent, row.Current.revisionContent)
		if contentChanged && m.Locked {
			return patchResult{}, problem("MIGRATION_LOCKED", "SQL migrasi sudah terkunci.")
		}
		if !contentChanged && m.Title == row.Title && m.Description == row.Description {
			current, err := s.renderMigration(ctx, tx, row)
			return patchResult{migration: current}, err
		}
		now := s.store.now()
		var resetID string
		if contentChanged {
			if err := s.validateRevision(ctx, driver, &m.Current); err != nil {
				return patchResult{}, err
			}
			if m.Current.Number == maxRevision {
				return patchResult{}, problem("REVISION_CONFLICT", "Nomor revisi sudah mencapai batas.")
			}
			hash, err := m.Current.hash()
			if err != nil {
				return patchResult{}, internal(err)
			}
			m.Current.Number++
			m.Current.CreatedBy = Actor{ID: actor.UserID, Username: actor.Username}
			m.Current.CreatedAt = now
			if err := insertRevision(ctx, tx, id, m.Current, hash[:]); err != nil {
				return patchResult{}, err
			}
			resetID, err = s.resetOpenRequest(ctx, tx, id, m.Current.Number, hash[:])
			if err != nil {
				return patchResult{}, err
			}
		}
		_, err = tx.ExecContext(ctx, `UPDATE migrations SET title=$2, description=$3, current_revision=$4,
			updated_by=$5, updated_at=$6 WHERE id=$1`, id, m.Title, m.Description, m.Current.Number, actor.UserID, now)
		if err != nil {
			return patchResult{}, internal(err)
		}
		row, err = readMigration(ctx, tx, databaseID, id)
		if err != nil {
			return patchResult{}, err
		}
		current, err := s.renderMigration(ctx, tx, row)
		return patchResult{migration: current, changed: true, resetID: resetID}, err
	})
	if err != nil {
		return Migration{}, err
	}
	if result.changed {
		s.recordMigration(ctx, actor, "migration.updated", result.migration)
		if result.resetID != "" {
			s.publish(ctx, databaseID, "request.updated", result.resetID, "pending_review")
		}
	}
	return result.migration, nil
}

func (s *Service) recordMigration(ctx context.Context, actor contracts.Principal, action string, m Migration) {
	s.auditChange(ctx, actor, action, m.DatabaseID, "migration", m.ID,
		map[string]any{"revision": m.Current.Number, "content_hash": m.Current.ContentHash, "status": m.Status})
	s.publish(ctx, m.DatabaseID, "migration.updated", m.ID, m.Status)
}
