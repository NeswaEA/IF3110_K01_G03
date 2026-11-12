package migrations

import (
	"context"
	"database/sql"

	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/contracts"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/database"
)

type DriverLookup interface {
	Driver(ctx context.Context, id string) (database.Driver, error)
}

type ServiceOptions struct {
	BatchSizeMax int
	MaxAttempts  int
	Enqueuer     contracts.JobEnqueuer
	NotifyJobs   func()
}

type Service struct {
	store        *Store
	drivers      DriverLookup
	access       contracts.AccessChecker
	review       contracts.ReviewEvaluator
	audit        contracts.AuditRecorder
	events       contracts.EventPublisher
	batchSizeMax int
	maxAttempts  int
	enqueuer     contracts.JobEnqueuer
	notifyJobs   func()
}

var _ contracts.ReviewSubjects = (*Service)(nil)

func NewService(store *Store, drivers DriverLookup, access contracts.AccessChecker, review contracts.ReviewEvaluator,
	audit contracts.AuditRecorder, events contracts.EventPublisher, options ServiceOptions) *Service {
	if options.BatchSizeMax == 0 {
		options.BatchSizeMax = DefaultBatchSizeMax
	}
	if options.MaxAttempts == 0 {
		options.MaxAttempts = 3
	}
	return &Service{store: store, drivers: drivers, access: access, review: review, audit: audit, events: events,
		batchSizeMax: options.BatchSizeMax, maxAttempts: options.MaxAttempts, enqueuer: options.Enqueuer, notifyJobs: options.NotifyJobs}
}

func (s *Service) LoadSubject(ctx context.Context, q contracts.Querier, requestID string) (contracts.ReviewSubject, error) {
	return s.store.LoadSubject(ctx, q, requestID)
}

func (s *Service) ApplyReviewState(ctx context.Context, tx *sql.Tx, requestID, status string) error {
	return s.store.ApplyReviewState(ctx, tx, requestID, status)
}

func (s *Service) require(ctx context.Context, actor contracts.Principal, action contracts.Action, databaseID string) error {
	if !contracts.ValidID(databaseID) {
		return problem("NOT_FOUND", "Database tidak ditemukan.")
	}
	return s.access.Require(ctx, actor, action, databaseID)
}

func (s *Service) auditChange(ctx context.Context, actor contracts.Principal, action, databaseID, kind, id string, details map[string]any) {
	s.audit.Record(ctx, contracts.AuditEvent{
		Action: action, ActorID: &actor.UserID, ActorName: &actor.Username,
		DatabaseID: &databaseID, ResourceType: kind, ResourceID: id, Details: details,
	})
}

func (s *Service) publish(ctx context.Context, databaseID, event, resourceID, status string) {
	s.events.Publish(ctx, contracts.DatabaseEvent{
		DatabaseID: databaseID, Type: event, ResourceID: resourceID, Status: status, At: s.store.now(),
	})
}

func inTx[T any](ctx context.Context, db *sql.DB, opts *sql.TxOptions, work func(*sql.Tx) (T, error)) (T, error) {
	var zero T
	tx, err := db.BeginTx(ctx, opts)
	if err != nil {
		return zero, internal(err)
	}
	defer tx.Rollback()
	result, err := work(tx)
	if err != nil {
		return zero, err
	}
	if err := tx.Commit(); err != nil {
		return zero, internal(err)
	}
	return result, nil
}

var snapshot = sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true}
