package testkit

import (
	"sync"
	"time"

	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/contracts"
)

type Clock struct {
	mu  sync.Mutex
	now time.Time
}

var _ contracts.Clock = (*Clock)(nil)

func NewClock(now time.Time) *Clock { return &Clock{now: now} }

func (c *Clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *Clock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}
