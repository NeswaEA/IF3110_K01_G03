package jobs

import (
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"

	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/telemetry"
)

type RelayOptions struct {
	PollInterval time.Duration // OUTBOX_POLL_INTERVAL, default 500ms
	BatchSize    int           // rows per transaction, default 100
	Retention    time.Duration // published rows are deleted after this, default 7 days
}

// Relay publishes unpublished outbox rows to JetStream. Several relays may
// run at once: each claims rows with FOR UPDATE SKIP LOCKED. A crash between
// publish and commit republishes the row with the same Nats-Msg-Id, which
// JetStream drops as a duplicate inside its window.
type Relay struct {
	db      *sql.DB
	js      jetstream.JetStream
	o       RelayOptions
	notify  chan struct{}
	cleaned time.Time
}

func NewRelay(db *sql.DB, js jetstream.JetStream, o RelayOptions) *Relay {
	if o.PollInterval <= 0 {
		o.PollInterval = 500 * time.Millisecond
	}
	if o.BatchSize <= 0 {
		o.BatchSize = 100
	}
	if o.Retention <= 0 {
		o.Retention = 7 * 24 * time.Hour
	}
	return &Relay{db: db, js: js, o: o, notify: make(chan struct{}, 1)}
}

// Notify wakes the relay now instead of at the next poll.
func (r *Relay) Notify() {
	select {
	case r.notify <- struct{}{}:
	default:
	}
}

// Run publishes until ctx ends; failures are logged and retried next round.
func (r *Relay) Run(ctx context.Context) error {
	ticker := time.NewTicker(r.o.PollInterval)
	defer ticker.Stop()
	for {
		for {
			n, err := r.publishBatch(ctx)
			if err != nil && ctx.Err() == nil {
				slog.WarnContext(ctx, "outbox relay round failed", "event", "outbox.failed", "error", err.Error())
			}
			if err != nil || n < r.o.BatchSize {
				break
			}
		}
		r.housekeep(ctx)
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		case <-r.notify:
		}
	}
}

type outboxRow struct {
	id, subject      string
	payload, headers []byte
}

// publishBatch publishes up to BatchSize rows in id order and returns how
// many it published; it stops at the first failure so order is kept.
func (r *Relay) publishBatch(ctx context.Context) (int, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT id, subject, payload, headers FROM outbox_messages
		WHERE published_at IS NULL ORDER BY id LIMIT $1 FOR UPDATE SKIP LOCKED`, r.o.BatchSize)
	if err != nil {
		return 0, err
	}
	var batch []outboxRow
	for rows.Next() {
		var row outboxRow
		if err := rows.Scan(&row.id, &row.subject, &row.payload, &row.headers); err != nil {
			rows.Close()
			return 0, err
		}
		batch = append(batch, row)
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}
	published := 0
	for _, row := range batch {
		if err := r.publish(ctx, row); err != nil {
			if _, uerr := tx.ExecContext(ctx, `UPDATE outbox_messages SET publish_attempts = publish_attempts + 1, last_error = $2 WHERE id = $1`,
				row.id, sanitize(err)); uerr != nil {
				return published, uerr
			}
			break
		}
		if _, err := tx.ExecContext(ctx, `UPDATE outbox_messages SET published_at = now() WHERE id = $1`, row.id); err != nil {
			return published, err
		}
		published++
	}
	return published, tx.Commit()
}

func (r *Relay) publish(ctx context.Context, row outboxRow) error {
	carrier := propagation.MapCarrier{}
	_ = json.Unmarshal(row.headers, &carrier)
	var p payload
	_ = json.Unmarshal(row.payload, &p)
	enqueued := trace.SpanContextFromContext(otel.GetTextMapPropagator().Extract(context.Background(), carrier))
	ctx, span := telemetry.Tracer().Start(ctx, "outbox.publish", trace.WithSpanKind(trace.SpanKindProducer),
		trace.WithLinks(trace.Link{SpanContext: enqueued}),
		trace.WithAttributes(attribute.String("execution_id", p.ExecutionID), attribute.String("database_id", p.DatabaseID)))
	defer span.End()

	msg := nats.NewMsg(row.subject)
	msg.Data = row.payload
	for k, v := range carrier { // the enqueue trace context, so the worker continues that trace
		msg.Header.Set(k, v)
	}
	pctx, cancel := context.WithTimeout(ctx, 5*time.Second) // a stalled publish must not hold the row locks forever
	defer cancel()
	if _, err := r.js.PublishMsg(pctx, msg, jetstream.WithMsgID(row.id)); err != nil {
		span.SetStatus(codes.Error, "publish failed")
		return err
	}
	slog.InfoContext(ctx, "job published", "event", "outbox.published", "execution_id", p.ExecutionID,
		"job_id", p.ExecutionID, "database_id", p.DatabaseID)
	return nil
}

func (r *Relay) housekeep(ctx context.Context) {
	if time.Since(r.cleaned) < time.Hour {
		return
	}
	r.cleaned = time.Now()
	if _, err := r.db.ExecContext(ctx, `DELETE FROM outbox_messages WHERE published_at < now() - make_interval(secs => $1)`,
		r.o.Retention.Seconds()); err != nil && ctx.Err() == nil {
		slog.WarnContext(ctx, "outbox cleanup failed", "event", "outbox.failed", "error", err.Error())
	}
}

// sanitize keeps a bounded error message; NATS errors carry no credentials.
func sanitize(err error) string {
	msg := err.Error()
	if len(msg) > 500 {
		msg = msg[:500]
	}
	return msg
}
