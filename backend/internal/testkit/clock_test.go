package testkit_test

import (
	"sync"
	"testing"
	"time"

	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/testkit"
)

func TestClockConcurrentAdvance(t *testing.T) {
	start := time.Date(2026, 10, 21, 0, 0, 0, 0, time.UTC)
	clock := testkit.NewClock(start)
	if !clock.Now().Equal(start) {
		t.Fatal("unexpected initial time")
	}
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			clock.Advance(time.Second)
			clock.Now()
		}()
	}
	wg.Wait()
	if !clock.Now().Equal(start.Add(20 * time.Second)) {
		t.Fatal("concurrent advances were lost")
	}
}
