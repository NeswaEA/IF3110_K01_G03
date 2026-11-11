package jobs

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/contracts"
)

// EventBus carries "refetch" signals from any process (API or worker) to the
// SSE streams of every API replica over core NATS. Delivery is best effort:
// a missed event is covered by the client's refetch on reconnect and polling.
type EventBus struct{ nc *nats.Conn }

var _ contracts.EventPublisher = (*EventBus)(nil)

func NewEventBus(nc *nats.Conn) *EventBus { return &EventBus{nc: nc} }

type eventJSON struct {
	Type       string    `json:"type,omitempty"`
	DatabaseID string    `json:"database_id"`
	ResourceID string    `json:"resource_id"`
	Status     string    `json:"status"`
	At         time.Time `json:"at"`
}

func (b *EventBus) Publish(ctx context.Context, e contracts.DatabaseEvent) {
	if e.At.IsZero() {
		e.At = time.Now()
	}
	data, err := json.Marshal(eventJSON{Type: e.Type, DatabaseID: e.DatabaseID, ResourceID: e.ResourceID, Status: e.Status, At: e.At.UTC()})
	if err == nil {
		err = b.nc.Publish(EventSubject(e.DatabaseID), data)
	}
	if err != nil {
		slog.WarnContext(ctx, "event publish failed", "event", "event.publish_failed", "database_id", e.DatabaseID, "error", err.Error())
	}
}

// subscribe delivers the events of one database to fn until unsubscribe.
func (b *EventBus) subscribe(databaseID string, fn func(contracts.DatabaseEvent)) (unsubscribe func(), err error) {
	sub, err := b.nc.Subscribe(EventSubject(databaseID), func(m *nats.Msg) {
		var e eventJSON
		if json.Unmarshal(m.Data, &e) != nil || e.DatabaseID != databaseID {
			return
		}
		fn(contracts.DatabaseEvent{DatabaseID: e.DatabaseID, Type: e.Type, ResourceID: e.ResourceID, Status: e.Status, At: e.At})
	})
	if err != nil {
		return nil, err
	}
	return func() { _ = sub.Unsubscribe() }, nil
}
