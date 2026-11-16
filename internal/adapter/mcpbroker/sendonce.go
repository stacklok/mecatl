package mcpbroker

import (
	"context"
	"errors"
	"sync"
)

// ErrAlreadySent refuses a claim on a call that is in flight or was sent.
var ErrAlreadySent = errors.New("mcpbroker: call already claimed or sent; automatic replay refused")

// errSendOnceFull refuses a claim when the record is at its limit.
var errSendOnceFull = errors.New("mcpbroker: send-once record is full")

// SendOnceRecord records the gateway calls that were claimed for sending, by
// operation ID and argument hash, so a call that may have run is never sent
// again automatically.
type SendOnceRecord interface {
	// Claim claims the call, or returns ErrAlreadySent while it is in flight
	// or once it was sent.
	Claim(ctx context.Context, operationID, argumentsSHA256 string) error
	// Release gives the claim up; callers release only a call no attempt of
	// which may have run.
	Release(ctx context.Context, operationID, argumentsSHA256 string) error
}

type sendOnceKey struct{ operationID, argumentsSHA256 string }

type memorySendOnce struct {
	limit   int
	mu      sync.Mutex
	claimed map[sendOnceKey]struct{}
}

// NewMemorySendOnceRecord returns a SendOnceRecord holding at most limit
// claims. At the limit Claim fails closed; nothing is evicted.
// Shortcut: the send-once record is in memory; removed by #2730 (durable gateway call state in mecak8s).
func NewMemorySendOnceRecord(limit int) SendOnceRecord {
	return &memorySendOnce{limit: limit, claimed: make(map[sendOnceKey]struct{})}
}

func (m *memorySendOnce) Claim(_ context.Context, operationID, argumentsSHA256 string) error {
	key := sendOnceKey{operationID, argumentsSHA256}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.claimed[key]; ok {
		return ErrAlreadySent
	}
	if len(m.claimed) >= m.limit {
		return errSendOnceFull
	}
	m.claimed[key] = struct{}{}
	return nil
}

func (m *memorySendOnce) Release(_ context.Context, operationID, argumentsSHA256 string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.claimed, sendOnceKey{operationID, argumentsSHA256})
	return nil
}
