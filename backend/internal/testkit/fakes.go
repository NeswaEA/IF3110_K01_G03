package testkit

import (
	"context"
	"sync"

	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/contracts"
)

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
