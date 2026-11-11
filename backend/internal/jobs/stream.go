// Package jobs moves migration jobs from the API to the workers: a
// transactional outbox in the metadata database, a relay that publishes it to
// a JetStream work queue, the consumer that hands each message to a
// contracts.JobHandler, a sweeper for lost messages, and the realtime event
// bus behind the SSE endpoint. The metadata database stays the source of
// truth for job state; messages only say "look at execution X".
package jobs

import (
	"context"
	"time"

	"github.com/nats-io/nats.go/jetstream"
)

const (
	StreamName   = "MIGRATIONS"
	ConsumerName = "migration-workers"
	jobSubjects  = "migrations.jobs.*"
)

// JobSubject is the subject of a job for one target database.
func JobSubject(databaseID string) string { return "migrations.jobs." + databaseID }

// EventSubject is the core NATS subject of UI events for one target database.
func EventSubject(databaseID string) string { return "migrations.events." + databaseID }

// EnsureStream creates or updates the work-queue stream; safe to call on
// every API and worker start.
func EnsureStream(ctx context.Context, js jetstream.JetStream) error {
	_, err := js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:       StreamName,
		Subjects:   []string{jobSubjects},
		Retention:  jetstream.WorkQueuePolicy,
		Storage:    jetstream.FileStorage,
		Replicas:   1,
		Duplicates: 2 * time.Minute,
		MaxAge:     7 * 24 * time.Hour,
	})
	return err
}
