package migrations

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/contracts"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/database"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/database/postgres"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/schemadiff"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	sqlite3 "github.com/mattn/go-sqlite3"
)

type DryRunReport struct {
	ExecutionID   string                  `json:"execution_id"`
	Revision      int64                   `json:"revision"`
	Status        string                  `json:"status"`
	Reversible    *bool                   `json:"reversible"`
	SchemaChanges []schemadiff.DiffChange `json:"schema_changes"`
	Message       *string                 `json:"message"`
	DurationMS    float64                 `json:"duration_ms"`
	FinishedAt    time.Time               `json:"finished_at"`
}

func (s *Service) enqueueDryRun(ctx context.Context, actor contracts.Principal, databaseID, migrationID string) (Execution, bool, error) {
	result, err := inTx(ctx, s.store.DB, nil, func(tx *sql.Tx) (enqueueResult, error) {
		if err := lockDatabase(ctx, tx, databaseID); err != nil {
			return enqueueResult{}, err
		}
		if err := lockMigration(ctx, tx, databaseID, migrationID); err != nil {
			return enqueueResult{}, err
		}
		m, err := readMigration(ctx, tx, databaseID, migrationID)
		if err != nil {
			return enqueueResult{}, err
		}
		existing, err := scanExecution(tx.QueryRowContext(ctx, executionSelect+`
			WHERE e.database_id=$1 AND e.migration_id=$2 AND e.migration_revision=$3
			AND e.action='dry_run' AND e.status IN ('queued','running','held') LIMIT 1`, databaseID, migrationID, m.Current.Number))
		if err == nil {
			return enqueueResult{executionRow: existing, reused: true}, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return enqueueResult{}, internal(err)
		}
		var size *int
		if m.Current.Batch != nil {
			size = &m.Current.Batch.BatchSize
		}
		id := uuid.Must(uuid.NewV7()).String()
		if _, err := tx.ExecContext(ctx, `INSERT INTO migration_executions
			(id,database_id,number,migration_id,action,direction,mode,migration_revision,content_hash,status,max_attempts,chain_root_id,
			batch_size,enqueued_by,enqueued_by_name,enqueued_at)
			VALUES ($1,$2,(SELECT COALESCE(max(number),0)+1 FROM migration_executions WHERE database_id=$2),
			$3,'dry_run','up',$4,$5,$6,'queued',$7,$1,$8,$9,$10,$11)`,
			id, databaseID, migrationID, m.Current.Mode, m.Current.Number, m.contentHash, s.maxAttempts, size, actor.UserID, actor.Username, s.store.now()); err != nil {
			return enqueueResult{}, internal(err)
		}
		if err := s.enqueuer.Enqueue(ctx, tx, contracts.JobMessage{ExecutionID: id, DatabaseID: databaseID}); err != nil {
			return enqueueResult{}, internal(err)
		}
		e, err := readExecution(ctx, tx, databaseID, id, false)
		return enqueueResult{executionRow: e}, err
	})
	if err != nil {
		return Execution{}, false, err
	}
	if !result.reused {
		s.recordExecution(ctx, "execution.enqueued", result.Execution)
		if s.notifyJobs != nil {
			s.notifyJobs()
		}
	}
	return result.Execution, result.reused, nil
}

func (x *Executor) finishDryRun(ctx context.Context, e executionRow, report DryRunReport, release func()) contracts.JobResult {
	report.ExecutionID, report.Revision, report.FinishedAt = e.ID, e.MigrationRevision, x.service.store.now()
	if e.StartedAt != nil {
		report.DurationMS = float64(report.FinishedAt.Sub(*e.StartedAt)) / float64(time.Millisecond)
	}
	if report.SchemaChanges == nil {
		report.SchemaChanges = []schemadiff.DiffChange{}
	}
	raw, err := json.Marshal(report)
	if err != nil {
		return retry("retry", x.options.RetryBaseDelay)
	}
	status := "succeeded"
	if report.Status == "failed" || report.Status == "timeout" {
		status = "failed"
	}
	return x.transition(ctx, e, executionTransition{status: status, attempt: e.Attempt, report: raw, releaseLock: release}, false)
}

func (x *Executor) dryRun(ctx context.Context, e executionRow, target database.TargetHandle, lock database.MigrationLock) DryRunReport {
	s := x.service
	level, err := s.access.Effective(ctx, e.Actor.ID, e.DatabaseID)
	if err != nil || level != contracts.AccessEdit {
		return DryRunReport{Status: "failed", Message: new("Akses edit diperlukan untuk dry-run.")}
	}
	block, err := findBlock(ctx, s.store.DB, e.DatabaseID)
	if err != nil {
		return dryRunError(ctx, err)
	}
	if block != nil || !target.MigrationReady {
		return DryRunReport{Status: "skipped", Message: new("Database terblokir atau target belum siap.")}
	}
	m, err := readMigration(ctx, s.store.DB, e.DatabaseID, e.MigrationID)
	if err != nil {
		return dryRunError(ctx, err)
	}
	if m.Current.Number != e.MigrationRevision || m.Current.ContentHash != hashString(e.contentHash) {
		return DryRunReport{Status: "skipped", Message: new("Revisi migrasi telah berubah.")}
	}
	for _, finding := range m.Current.Lint {
		if finding.Severity == "error" {
			return DryRunReport{Status: "failed", Message: new("Perbaiki temuan lint sebelum dry-run.")}
		}
	}
	t := normalTarget{conn: lock.Conn, target: target}
	ctx, cancel := context.WithTimeout(ctx, x.options.DryRunTimeout)
	defer cancel()
	defer t.rollback(ctx)
	if err := t.prepare(ctx, x.options.DryRunTimeout, x.options.LockTimeout); err != nil {
		return dryRunError(ctx, err)
	}
	if e.Mode == "batch" {
		return x.dryRunBatch(ctx, t, m.Current)
	}
	execute := func(query string) error {
		statements, err := normalStatements(target.Driver, query)
		if err != nil {
			return err
		}
		if target.Driver == database.Postgres {
			if err := t.exec(ctx, "SET LOCAL ROLE "+postgres.QuoteIdent(target.EditorRole)); err != nil {
				return err
			}
		}
		if err := t.userSQL(ctx, query, statements); err != nil {
			return err
		}
		if target.Driver == database.Postgres {
			return t.exec(ctx, "RESET ROLE")
		}
		return nil
	}
	before, err := x.inspector.InspectWith(ctx, target, t)
	if err != nil {
		return dryRunError(ctx, err)
	}
	if err := execute(m.Current.UpSQL); err != nil {
		return dryRunError(ctx, err)
	}
	after, err := x.inspector.InspectWith(ctx, target, t)
	if err != nil {
		return dryRunError(ctx, err)
	}
	changes, err := schemadiff.CompareSnapshots(before, after)
	if err != nil {
		return dryRunError(ctx, err)
	}
	result := DryRunReport{Status: "passed", SchemaChanges: changes}
	if m.Current.DownSQL != nil {
		if err := execute(*m.Current.DownSQL); err != nil {
			report := dryRunError(ctx, err)
			report.SchemaChanges, report.Reversible = changes, new(false)
			return report
		}
		restored, err := x.inspector.InspectWith(ctx, target, t)
		if err != nil {
			return dryRunError(ctx, err)
		}
		result.Reversible = new(before.Fingerprint == restored.Fingerprint)
	}
	return result
}

func dryRunError(ctx context.Context, err error) DryRunReport {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) || database.SQLState(err) == "57014" {
		return DryRunReport{Status: "timeout", Message: new("Batas waktu dry-run terlampaui.")}
	}
	message := "Dry-run gagal."
	var pgErr *pgconn.PgError
	var sqliteErr sqlite3.Error
	switch {
	case errors.As(err, &pgErr):
		message = pgErr.Message
	case errors.As(err, &sqliteErr):
		message = sqliteErr.Error()
	default:
		var domain *contracts.DomainError
		if errors.As(err, &domain) {
			message = domain.Message
		}
	}
	message = strings.TrimSpace(message)
	return DryRunReport{Status: "failed", Message: &message}
}
