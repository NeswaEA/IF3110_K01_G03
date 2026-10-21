// Package databasetest provides a fake TargetProvider for consumers (migrations,
// viewer, explain) that need a lease without a registry. It is test support,
// not production wiring.
package databasetest

import (
	"context"
	"sync"
	"sync/atomic"

	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/database"
)

type Provider struct {
	mu       sync.Mutex
	handles  map[string]database.TargetHandle
	failures map[string]error
	acquired atomic.Int64
	released atomic.Int64
}

func NewProvider(handles ...database.TargetHandle) *Provider {
	p := &Provider{handles: map[string]database.TargetHandle{}, failures: map[string]error{}}
	for _, h := range handles {
		p.handles[h.DatabaseID] = h
	}
	return p
}

// SetRevision simulates a config update: later leases carry the new revision.
func (p *Provider) SetRevision(id string, rev int64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	h := p.handles[id]
	h.Revision = rev
	p.handles[id] = h
}

// Fail makes Acquire return err for id until Fail(id, nil).
func (p *Provider) Fail(id string, err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err == nil {
		delete(p.failures, id)
		return
	}
	p.failures[id] = err
}

func (p *Provider) Acquire(ctx context.Context, id string) (database.TargetHandle, func(), error) {
	if err := ctx.Err(); err != nil {
		return database.TargetHandle{}, nil, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.failures[id]; err != nil {
		return database.TargetHandle{}, nil, err
	}
	h, ok := p.handles[id]
	if !ok {
		return database.TargetHandle{}, nil, database.ErrNotFound
	}
	p.acquired.Add(1)
	var once sync.Once
	return h, func() { once.Do(func() { p.released.Add(1) }) }, nil
}

func (p *Provider) Acquired() int64 { return p.acquired.Load() }
func (p *Provider) Released() int64 { return p.released.Load() }
