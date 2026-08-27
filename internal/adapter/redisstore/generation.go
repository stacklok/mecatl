package redisstore

import (
	"context"
	"errors"
	"sync"

	"github.com/redis/go-redis/v9"
)

var errStoreClosed = errors.New("redisstore: store closed")

type generationContextKey struct{}

type clientGeneration struct {
	owner    *clientGenerations
	client   redis.UniversalClient
	refs     int
	retired  bool
	closed   bool
	closedCh chan struct{}
}

type clientGenerations struct {
	mu        sync.Mutex
	current   *clientGeneration
	closed    bool
	all       map[*clientGeneration]struct{}
	closeOnce sync.Once
}

func newClientGenerations(client redis.UniversalClient) *clientGenerations {
	manager := &clientGenerations{all: make(map[*clientGeneration]struct{})}
	generation := &clientGeneration{owner: manager, client: client, closedCh: make(chan struct{})}
	manager.current = generation
	manager.all[generation] = struct{}{}
	return manager
}

func (m *clientGenerations) acquire() (*clientGeneration, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed || m.current == nil {
		return nil, errStoreClosed
	}
	m.current.refs++
	return m.current, nil
}

func (m *clientGenerations) finishClose(generation *clientGeneration) {
	_ = generation.client.Close()
	close(generation.closedCh)
	m.mu.Lock()
	delete(m.all, generation)
	m.mu.Unlock()
}

func (m *clientGenerations) release(generation *clientGeneration) {
	var closeGeneration *clientGeneration
	m.mu.Lock()
	if generation.refs > 0 {
		generation.refs--
	}
	if generation.retired && generation.refs == 0 && !generation.closed {
		generation.closed = true
		closeGeneration = generation
	}
	m.mu.Unlock()
	if closeGeneration != nil {
		m.finishClose(closeGeneration)
	}
}

func (m *clientGenerations) swap(client redis.UniversalClient) error {
	candidate := &clientGeneration{owner: m, client: client, closedCh: make(chan struct{})}
	var closeGeneration *clientGeneration
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return errStoreClosed
	}
	old := m.current
	m.current = candidate
	m.all[candidate] = struct{}{}
	old.retired = true
	if old.refs == 0 && !old.closed {
		old.closed = true
		closeGeneration = old
	}
	m.mu.Unlock()
	if closeGeneration != nil {
		m.finishClose(closeGeneration)
	}
	return nil
}

func (m *clientGenerations) close() error {
	m.closeOnce.Do(func() {
		m.mu.Lock()
		m.closed = true
		if m.current != nil {
			m.current.retired = true
			m.current = nil
		}
		generations := make([]*clientGeneration, 0, len(m.all))
		var ready []*clientGeneration
		for generation := range m.all {
			generations = append(generations, generation)
			if generation.retired && generation.refs == 0 && !generation.closed {
				generation.closed = true
				ready = append(ready, generation)
			}
		}
		m.mu.Unlock()
		for _, generation := range ready {
			m.finishClose(generation)
		}
		for _, generation := range generations {
			<-generation.closedCh
		}
	})
	return nil
}

func (st *Store) pin(ctx context.Context) (context.Context, func(), error) {
	if generation, ok := ctx.Value(generationContextKey{}).(*clientGeneration); ok && generation != nil && generation.owner == st.clients {
		return ctx, func() {}, nil
	}
	generation, err := st.clients.acquire()
	if err != nil {
		return ctx, nil, err
	}
	return context.WithValue(ctx, generationContextKey{}, generation), func() { st.clients.release(generation) }, nil
}

func (s *scheduleStore) pin(ctx context.Context) (context.Context, func(), error) {
	if generation, ok := ctx.Value(generationContextKey{}).(*clientGeneration); ok && generation != nil && generation.owner == s.clients {
		return ctx, func() {}, nil
	}
	generation, err := s.clients.acquire()
	if err != nil {
		return ctx, nil, err
	}
	return context.WithValue(ctx, generationContextKey{}, generation), func() { s.clients.release(generation) }, nil
}

func (st *Store) redis(ctx context.Context) redis.UniversalClient {
	generation, ok := ctx.Value(generationContextKey{}).(*clientGeneration)
	if !ok || generation == nil || generation.owner != st.clients {
		panic("redisstore: Redis client used without a pinned generation")
	}
	return generation.client
}

func (s *scheduleStore) redis(ctx context.Context) redis.UniversalClient {
	generation, ok := ctx.Value(generationContextKey{}).(*clientGeneration)
	if !ok || generation == nil || generation.owner != s.clients {
		panic("redisstore: Redis client used without a pinned generation")
	}
	return generation.client
}
