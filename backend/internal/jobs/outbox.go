package jobs

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/propagation"

	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/contracts"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/telemetry"
)

// Outbox writes job messages in the caller's transaction, so a job exists
// exactly when its execution row commits.
type Outbox struct{}

var _ contracts.JobEnqueuer = (*Outbox)(nil)

// NewOutbox returns the enqueuer; rows are written through the caller's tx,
// so the database handle is only needed by the relay.
func NewOutbox(*sql.DB) *Outbox { return &Outbox{} }

type payload struct {
	ExecutionID string `json:"execution_id"`
	DatabaseID  string `json:"database_id"`
}

// Enqueue inserts the outbox row in tx and records the trace context of ctx
// in its headers, so the worker's span continues the caller's trace. The
// caller signals the relay (Relay.Notify) after commit.
func (o *Outbox) Enqueue(ctx context.Context, tx *sql.Tx, m contracts.JobMessage) error {
	if !contracts.ValidID(m.ExecutionID) || !contracts.ValidID(m.DatabaseID) {
		return errors.New("jobs: execution and database ids must be UUIDs")
	}
	ctx, span := telemetry.Tracer().Start(ctx, "outbox.enqueue")
	defer span.End()
	span.SetAttributes(attribute.String("execution_id", m.ExecutionID), attribute.String("database_id", m.DatabaseID))

	headers := propagation.MapCarrier{}
	otel.GetTextMapPropagator().Inject(ctx, headers)
	h, err := json.Marshal(headers)
	if err != nil {
		return err
	}
	p, err := json.Marshal(payload{ExecutionID: m.ExecutionID, DatabaseID: m.DatabaseID})
	if err != nil {
		return err
	}
	id, err := uuid.NewV7() // time-ordered, so the relay publishes in enqueue order
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO outbox_messages (id, subject, payload, headers) VALUES ($1, $2, $3, $4)`,
		id.String(), JobSubject(m.DatabaseID), p, h)
	return err
}
