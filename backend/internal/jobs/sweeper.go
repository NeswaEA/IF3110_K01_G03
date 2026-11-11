package jobs

import (
	"context"
	"database/sql"
	"log/slog"
	"time"

	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/contracts"
)

type SweeperOptions struct {
	Interval  time.Duration // default 30s
	OlderThan time.Duration // default 2 × JOB_ACK_WAIT (120s)
}

// Sweeper re-enqueues executions the queue may have lost (purged stream,
// deleted NATS volume, a worker that died without redelivery). The metadata
// database stays the source of truth; a duplicate message is harmless.
type Sweeper struct {
	src contracts.StaleJobSource
	enq contracts.JobEnqueuer
	db  *sql.DB
	o   SweeperOptions
}

func NewSweeper(src contracts.StaleJobSource, enq contracts.JobEnqueuer, db *sql.DB, o SweeperOptions) *Sweeper {
	if o.Interval <= 0 {
		o.Interval = 30 * time.Second
	}
	if o.OlderThan <= 0 {
		o.OlderThan = 2 * time.Minute
	}
	return &Sweeper{src: src, enq: enq, db: db, o: o}
}

func (s *Sweeper) Run(ctx context.Context) error {
	ticker := time.NewTicker(s.o.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := s.sweep(ctx); err != nil && ctx.Err() == nil {
				slog.WarnContext(ctx, "stale job sweep failed", "event", "sweeper.failed", "error", err.Error())
			}
		}
	}
}

func (s *Sweeper) sweep(ctx context.Context) error {
	stale, err := s.src.StaleJobs(ctx, s.o.OlderThan)
	if err != nil {
		return err
	}
	for _, m := range stale {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if err := s.enq.Enqueue(ctx, tx, m); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		slog.InfoContext(ctx, "stale job re-enqueued", "event", "sweeper.requeued", "execution_id", m.ExecutionID,
			"job_id", m.ExecutionID, "database_id", m.DatabaseID)
	}
	return nil
}
