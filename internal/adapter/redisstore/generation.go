package redisstore

import (
	"errors"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

var errStoreClosed = errors.New("redisstore: store closed")

type clientGeneration struct {
	client   redis.UniversalClient
	refs     int
	retired  bool
	closing  bool
	closedCh chan struct{}
}

type clientGenerations struct {
	mu        sync.Mutex
	current   *clientGeneration
	closed    bool
	all       map[*clientGeneration]struct{}
	allClosed chan struct{}
	closeOnce sync.Once
	closeWait int
}

func newClientGenerations(client redis.UniversalClient) *clientGenerations {
	manager := &clientGenerations{
		all:       make(map[*clientGeneration]struct{}),
		allClosed: make(chan struct{}),
	}
	generation := &clientGeneration{client: client, closedCh: make(chan struct{})}
	manager.current = generation
	manager.all[generation] = struct{}{}
	return manager
}

func (m *clientGenerations) acquire() (redis.UniversalClient, func(), error) {
	m.mu.Lock()
	if m.closed || m.current == nil {
		m.mu.Unlock()
		return nil, nil, errStoreClosed
	}
	generation := m.current
	generation.refs++
	m.mu.Unlock()

	var once sync.Once
	return generation.client, func() {
		once.Do(func() { m.release(generation) })
	}, nil
}

// retireAndClaimCloseLocked centralizes retirement and the retired -> closing
// transition. A claimed close is started asynchronously after m.mu is released.
func retireAndClaimCloseLocked(generation *clientGeneration, retire bool) *clientGeneration {
	if retire {
		generation.retired = true
	}
	if !generation.retired || generation.refs != 0 || generation.closing {
		return nil
	}
	generation.closing = true
	return generation
}

func (m *clientGenerations) startClose(generation *clientGeneration) {
	go func() {
		_ = generation.client.Close()
		m.mu.Lock()
		delete(m.all, generation)
		close(generation.closedCh)
		if m.closed && len(m.all) == 0 {
			close(m.allClosed)
		}
		m.mu.Unlock()
	}()
}

func (m *clientGenerations) release(generation *clientGeneration) {
	m.mu.Lock()
	if generation.refs > 0 {
		generation.refs--
	}
	closeGeneration := retireAndClaimCloseLocked(generation, false)
	m.mu.Unlock()
	if closeGeneration != nil {
		m.startClose(closeGeneration)
	}
}

func (m *clientGenerations) swap(client redis.UniversalClient) error {
	candidate := &clientGeneration{client: client, closedCh: make(chan struct{})}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return errStoreClosed
	}
	old := m.current
	m.current = candidate
	m.all[candidate] = struct{}{}
	closeGeneration := retireAndClaimCloseLocked(old, true)
	m.mu.Unlock()
	if closeGeneration != nil {
		m.startClose(closeGeneration)
	}
	return nil
}

func (m *clientGenerations) rejectNew() {
	m.mu.Lock()
	m.closed = true
	m.mu.Unlock()
}

// close rejects new acquisitions and swaps immediately, starts every eligible
// client close asynchronously, and waits at most grace for both leases and close
// completion. Live leases are never force-closed; a timed-out generation closes
// eventually after its final release or blocking client Close returns.
func (m *clientGenerations) close(grace time.Duration) int {
	m.closeOnce.Do(func() {
		m.mu.Lock()
		m.closed = true
		var ready []*clientGeneration
		if m.current != nil {
			if generation := retireAndClaimCloseLocked(m.current, true); generation != nil {
				ready = append(ready, generation)
			}
			m.current = nil
		}
		if len(m.all) == 0 {
			close(m.allClosed)
		}
		m.mu.Unlock()
		for _, generation := range ready {
			m.startClose(generation)
		}

		timer := time.NewTimer(grace)
		defer timer.Stop()
		select {
		case <-m.allClosed:
		case <-timer.C:
			m.mu.Lock()
			m.closeWait = len(m.all)
			m.mu.Unlock()
		}
	})
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.closeWait
}
