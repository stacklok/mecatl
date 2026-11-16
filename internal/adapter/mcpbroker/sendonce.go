package mcpbroker

import (
	"context"
	"errors"
	"sync"
)

// ErrAlreadySent refuses a claim on a call that is in flight or was sent.
var ErrAlreadySent = errors.New("mcpbroker: call already claimed or sent; automatic replay refused")

// errSendOnceFull refuses a claim when the root session's share of the record
// is at its limit.
var errSendOnceFull = errors.New("mcpbroker: send-once record is full for this session")

// SendOnceRecord records the gateway calls that were claimed for sending, by
// root session, operation ID, and argument hash, so a call that may have run
// is never sent again automatically.
type SendOnceRecord interface {
	// Claim claims the call, or returns ErrAlreadySent while it is in flight
	// or once it was sent.
	Claim(ctx context.Context, rootSessionID, operationID, argumentsSHA256 string) error
	// Release gives the claim up; callers release only a call no attempt of
	// which may have run.
	Release(ctx context.Context, rootSessionID, operationID, argumentsSHA256 string) error
	// Forget drops every claim of a root session that was deleted
	// permanently. A session that can resume keeps its claims.
	Forget(ctx context.Context, rootSessionID string) error
}

type sendOnceKey struct{ operationID, argumentsSHA256 string }

type memorySendOnce struct {
	perRoot int
	mu      sync.Mutex
	claimed map[string]map[sendOnceKey]struct{}
}

// NewMemorySendOnceRecord returns a SendOnceRecord holding at most perRoot
// claims for each root session. At the limit Claim fails closed for that root
// session only; nothing is evicted.
// Shortcut: the send-once record is in memory, held per root session until the session is deleted; removed by #2730 (durable gateway call state in mecak8s).
func NewMemorySendOnceRecord(perRoot int) SendOnceRecord {
	return &memorySendOnce{perRoot: perRoot, claimed: make(map[string]map[sendOnceKey]struct{})}
}

func (m *memorySendOnce) Claim(_ context.Context, rootSessionID, operationID, argumentsSHA256 string) error {
	key := sendOnceKey{operationID, argumentsSHA256}
	m.mu.Lock()
	defer m.mu.Unlock()
	claims := m.claimed[rootSessionID]
	if _, ok := claims[key]; ok {
		return ErrAlreadySent
	}
	if len(claims) >= m.perRoot {
		return errSendOnceFull
	}
	if claims == nil {
		claims = make(map[sendOnceKey]struct{})
		m.claimed[rootSessionID] = claims
	}
	claims[key] = struct{}{}
	return nil
}

func (m *memorySendOnce) Release(_ context.Context, rootSessionID, operationID, argumentsSHA256 string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	claims := m.claimed[rootSessionID]
	delete(claims, sendOnceKey{operationID, argumentsSHA256})
	if len(claims) == 0 {
		delete(m.claimed, rootSessionID)
	}
	return nil
}

func (m *memorySendOnce) Forget(_ context.Context, rootSessionID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.claimed, rootSessionID)
	return nil
}
