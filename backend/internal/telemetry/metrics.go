package telemetry

import (
	"context"
	"log/slog"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// Instruments are created on the global (delegating) meter, so they work
// whether they are used before or after Setup. Labels are bounded enums only:
// never user, execution or database ids, SQL or raw paths.
var instruments = newInstruments()

type metricSet struct {
	finished   metric.Int64Counter
	retries    metric.Int64Counter
	deliveries metric.Int64Counter
	queueWait  metric.Float64Histogram
	lockWait   metric.Float64Histogram
	duration   metric.Float64Histogram
	batches    metric.Int64Counter
	rows       metric.Int64Counter
}

// secondsBuckets covers a fast queue claim up to a long migration.
var secondsBuckets = metric.WithExplicitBucketBoundaries(0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60, 120, 300, 900, 1800)

func meter() metric.Meter { return otel.Meter(instrumentation) }

func newInstruments() metricSet {
	m := meter()
	var s metricSet
	s.finished, _ = m.Int64Counter("migration_jobs_finished", metric.WithDescription("Executions that reached a final status."))
	s.retries, _ = m.Int64Counter("migration_job_retries", metric.WithDescription("Attempts scheduled again after a transient failure."))
	s.deliveries, _ = m.Int64Counter("migration_job_deliveries", metric.WithDescription("Job messages handled, by outcome."))
	s.queueWait, _ = m.Float64Histogram("migration_queue_wait", metric.WithUnit("s"), secondsBuckets,
		metric.WithDescription("Time from enqueue to the first claim."))
	s.lockWait, _ = m.Float64Histogram("migration_lock_wait", metric.WithUnit("s"), secondsBuckets,
		metric.WithDescription("Time from the first lock request to acquiring the migration lock."))
	s.duration, _ = m.Float64Histogram("migration_duration", metric.WithUnit("s"), secondsBuckets,
		metric.WithDescription("Execution duration from start to final status."))
	s.batches, _ = m.Int64Counter("migration_batches", metric.WithDescription("Batches committed or failed."))
	s.rows, _ = m.Int64Counter("migration_rows", metric.WithDescription("Rows changed by committed batches."))
	return s
}

// RecordQueueWait: enqueue to first claim of an execution.
func RecordQueueWait(ctx context.Context, action string, d time.Duration) {
	instruments.queueWait.Record(ctx, d.Seconds(), metric.WithAttributes(attribute.String("action", action)))
}

// RecordLockWait: first lock request to lock acquired.
func RecordLockWait(ctx context.Context, driver string, d time.Duration) {
	instruments.lockWait.Record(ctx, d.Seconds(), metric.WithAttributes(attribute.String("driver", driver)))
}

// RecordDelivery counts one handled job message by JobResult.Reason.
func RecordDelivery(ctx context.Context, reason string) {
	instruments.deliveries.Add(ctx, 1, metric.WithAttributes(attribute.String("reason", reason)))
}

// RecordRetry counts one retry scheduled after a transient failure.
func RecordRetry(ctx context.Context, action, mode string) {
	instruments.retries.Add(ctx, 1, metric.WithAttributes(attribute.String("action", action), attribute.String("mode", mode)))
}

// RecordFinished counts an execution reaching a final status and its duration.
func RecordFinished(ctx context.Context, action, mode, status string, d time.Duration) {
	attrs := metric.WithAttributes(attribute.String("action", action), attribute.String("mode", mode), attribute.String("status", status))
	instruments.finished.Add(ctx, 1, attrs)
	instruments.duration.Record(ctx, d.Seconds(), attrs)
}

// RecordBatch counts one batch (status "committed" or "failed") and, when
// committed, its rows.
func RecordBatch(ctx context.Context, direction, status string, rows int64) {
	instruments.batches.Add(ctx, 1, metric.WithAttributes(attribute.String("direction", direction), attribute.String("status", status)))
	if status == "committed" && rows > 0 {
		instruments.rows.Add(ctx, rows, metric.WithAttributes(attribute.String("direction", direction)))
	}
}

// RegisterJobGauge exports migration_jobs{status}: count returns the number
// of executions per active status. Register it in one process only (the API).
func RegisterJobGauge(count func(ctx context.Context) (map[string]int64, error)) error {
	gauge, err := meter().Int64ObservableGauge("migration_jobs", metric.WithDescription("Executions per active status."))
	if err != nil {
		return err
	}
	_, err = meter().RegisterCallback(func(ctx context.Context, o metric.Observer) error {
		counts, err := count(ctx)
		if err != nil {
			slog.WarnContext(ctx, "job gauge query failed", "event", "telemetry.error", "error", err.Error())
			return nil
		}
		for status, n := range counts {
			o.ObserveInt64(gauge, n, metric.WithAttributes(attribute.String("status", status)))
		}
		return nil
	}, gauge)
	return err
}
