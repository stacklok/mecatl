package memattempt_test

import (
	"sync"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/attemptconformance"
	"github.com/stacklok/mecatl/engine/adapter/memattempt"
)

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) set(now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = now
}

func TestMemattemptConformance(t *testing.T) {
	attemptconformance.Run(t, func(*testing.T) attemptconformance.Harness {
		clock := &fakeClock{}
		return attemptconformance.Harness{
			Repository: memattempt.New(clock),
			SetNow:     clock.set,
		}
	})
}
