package redisstore

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/stacklok/toolhive-core/redisconn"
)

var errStoreClosed = errors.New("redisstore: store closed")

// clientPair is one atomically published credential generation. Durability and
// follow clients use identical credentials but independent connection pools.
type clientPair struct {
	durability redis.UniversalClient
	follow     redis.UniversalClient
}

func buildClientPair(
	ctx context.Context,
	base *redisconn.Config,
	followPoolSize int,
	factory clientFactory,
) (clientPair, error) {
	durabilityConfig := *base
	durabilityConfig.PoolSize = 0
	durabilityConfig.MaxActiveConns = 0
	durability, err := factory(ctx, &durabilityConfig)
	if err != nil {
		return clientPair{}, err
	}
	followConfig := *base
	followConfig.PoolSize = followPoolSize
	followConfig.MaxActiveConns = followPoolSize
	follow, err := factory(ctx, &followConfig)
	if err != nil {
		_ = durability.Close()
		return clientPair{}, err
	}
	return clientPair{durability: durability, follow: follow}, nil
}

func initialClientPair(ctx context.Context, cfg *redisconn.Config, poolSize int, deps storeDependencies) (clientPair, error) {
	// The single-client seam remains only for focused legacy tests. Production
	// dependencies always supply initialPair and therefore publish two pools.
	if deps.initialClient != nil {
		client, err := deps.initialClient(ctx, cfg)
		return clientPair{durability: client, follow: client}, err
	}
	if deps.initialPair == nil {
		return clientPair{}, errors.New("redisstore: missing initial client-pair factory")
	}
	return deps.initialPair(ctx, cfg, poolSize)
}

func candidateClientPair(ctx context.Context, cfg *redisconn.Config, poolSize int, deps storeDependencies) (clientPair, error) {
	// See initialClientPair: injected single-client candidates are a test seam;
	// the production path always constructs and verifies the complete pair.
	if deps.candidate != nil {
		client, err := deps.candidate(ctx, cfg)
		return clientPair{durability: client, follow: client}, err
	}
	if deps.candidatePair == nil {
		return clientPair{}, errors.New("redisstore: missing candidate client-pair factory")
	}
	return deps.candidatePair(ctx, cfg, poolSize)
}

func closeClientPair(pair clientPair) {
	if pair.durability != nil {
		_ = pair.durability.Close()
	}
	if pair.follow != nil && pair.follow != pair.durability {
		_ = pair.follow.Close()
	}
}

type managedClient struct {
	client redis.UniversalClient
	once   sync.Once
	done   chan struct{}
}

func newManagedClient(client redis.UniversalClient) *managedClient {
	return &managedClient{client: client, done: make(chan struct{})}
}

func (c *managedClient) closeAsync() {
	if c == nil {
		return
	}
	c.once.Do(func() {
		go func() {
			_ = c.client.Close()
			close(c.done)
		}()
	})
}

type managedClientPair struct {
	durability *managedClient
	follow     *managedClient
}

func manageClientPair(pair clientPair) managedClientPair {
	durability := newManagedClient(pair.durability)
	if pair.follow == pair.durability {
		return managedClientPair{durability: durability, follow: durability}
	}
	return managedClientPair{durability: durability, follow: newManagedClient(pair.follow)}
}

type clientGeneration struct {
	pair     managedClientPair
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
	beginOnce sync.Once
	closeOnce sync.Once
	closeWait int
}

func newClientGenerations(client redis.UniversalClient) *clientGenerations {
	return newClientGenerationsPair(clientPair{durability: client, follow: client})
}

func newClientGenerationsPair(pair clientPair) *clientGenerations {
	manager := &clientGenerations{
		all:       make(map[*clientGeneration]struct{}),
		allClosed: make(chan struct{}),
	}
	generation := &clientGeneration{pair: manageClientPair(pair), closedCh: make(chan struct{})}
	manager.current = generation
	manager.all[generation] = struct{}{}
	return manager
}

func (m *clientGenerations) acquire() (redis.UniversalClient, func(), error) {
	return m.acquireKind(false)
}

func (m *clientGenerations) acquireFollow() (redis.UniversalClient, func(), error) {
	return m.acquireKind(true)
}

func (m *clientGenerations) acquireKind(follow bool) (redis.UniversalClient, func(), error) {
	m.mu.Lock()
	if m.closed || m.current == nil {
		m.mu.Unlock()
		return nil, nil, errStoreClosed
	}
	generation := m.current
	generation.refs++
	client := generation.pair.durability.client
	if follow {
		client = generation.pair.follow.client
	}
	m.mu.Unlock()

	var once sync.Once
	return client, func() {
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
	generation.pair.durability.closeAsync()
	generation.pair.follow.closeAsync()
	go func() {
		<-generation.pair.durability.done
		if generation.pair.follow != generation.pair.durability {
			<-generation.pair.follow.done
		}
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
	return m.swapPair(clientPair{durability: client, follow: client})
}

func (m *clientGenerations) swapPair(pair clientPair) error {
	candidate := &clientGeneration{pair: manageClientPair(pair), closedCh: make(chan struct{})}
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

func (m *clientGenerations) beginClose() {
	m.beginOnce.Do(func() {
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
	})
}

// forceCloseFollow asynchronously closes only the isolated follow half of each
// still-owned generation. It never touches durability, even when a follower is
// pathological and continues to hold the generation lease.
func (m *clientGenerations) forceCloseFollow() {
	m.mu.Lock()
	clients := make([]*managedClient, 0, len(m.all))
	for generation := range m.all {
		clients = append(clients, generation.pair.follow)
	}
	m.mu.Unlock()
	for _, client := range clients {
		client.closeAsync()
	}
}

// close rejects new acquisitions and swaps immediately, starts every eligible
// pair close asynchronously, and waits at most grace for both leases and close
// completion. Live leases are never force-closed by this manager; Store.Close
// owns the narrower follow-only force-close policy.
func (m *clientGenerations) close(grace time.Duration) int {
	m.closeOnce.Do(func() {
		m.beginClose()
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
