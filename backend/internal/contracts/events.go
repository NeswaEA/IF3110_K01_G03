package contracts

import (
	"context"
	"time"
)

// DatabaseEvent only signals the UI to refetch; it never carries SQL or target data.
type DatabaseEvent struct {
	DatabaseID, Type, ResourceID, Status string
	At                                   time.Time
}

type EventPublisher interface {
	Publish(ctx context.Context, e DatabaseEvent) // best effort
}
