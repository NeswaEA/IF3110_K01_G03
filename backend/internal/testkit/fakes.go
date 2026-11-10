package testkit

import (
	"context"
	"database/sql"
	"sync"

	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/contracts"
)

// ReviewEvaluator returns whatever State holds and records dismissals.
type ReviewEvaluator struct {
	mu        sync.Mutex
	State     contracts.ReviewState
	Err       error
	Dismissed []string
}

var _ contracts.ReviewEvaluator = (*ReviewEvaluator)(nil)

func (r *ReviewEvaluator) Evaluate(ctx context.Context, _ contracts.Querier, _ contracts.ReviewSubject) (contracts.ReviewState, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return contracts.ReviewState{}, err
	}
	return r.State, r.Err
}

func (r *ReviewEvaluator) DismissApprovals(ctx context.Context, _ *sql.Tx, requestID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	r.Dismissed = append(r.Dismissed, requestID)
	return r.Err
}

// AuditRecorder keeps every recorded event in order.
type AuditRecorder struct {
	mu     sync.Mutex
	events []contracts.AuditEvent
}

var _ contracts.AuditRecorder = (*AuditRecorder)(nil)

func (a *AuditRecorder) Record(_ context.Context, e contracts.AuditEvent) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.events = append(a.events, e)
}

func (a *AuditRecorder) Events() []contracts.AuditEvent {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]contracts.AuditEvent(nil), a.events...)
}

// JobEnqueuer keeps every enqueued message; it does not touch tx.
type JobEnqueuer struct {
	mu       sync.Mutex
	messages []contracts.JobMessage
	Err      error
}

var _ contracts.JobEnqueuer = (*JobEnqueuer)(nil)

func (j *JobEnqueuer) Enqueue(ctx context.Context, _ *sql.Tx, m contracts.JobMessage) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if j.Err != nil {
		return j.Err
	}
	j.messages = append(j.messages, m)
	return nil
}

func (j *JobEnqueuer) Messages() []contracts.JobMessage {
	j.mu.Lock()
	defer j.mu.Unlock()
	return append([]contracts.JobMessage(nil), j.messages...)
}

// EventPublisher keeps every published event.
type EventPublisher struct {
	mu     sync.Mutex
	events []contracts.DatabaseEvent
}

var _ contracts.EventPublisher = (*EventPublisher)(nil)

func (p *EventPublisher) Publish(_ context.Context, e contracts.DatabaseEvent) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.events = append(p.events, e)
}

func (p *EventPublisher) Events() []contracts.DatabaseEvent {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]contracts.DatabaseEvent(nil), p.events...)
}
