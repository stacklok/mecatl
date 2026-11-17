package mcpbroker

import (
	"container/list"
	"context"
	"errors"
	"sync"
)

// ErrAlreadySent refuses a claim on an operation that is in flight or was
// sent, with any arguments.
var ErrAlreadySent = errors.New("mcpbroker: call already claimed or sent; automatic replay refused")

// errSendOnceFull refuses a claim when the root session's share of the record
// is held entirely by calls in flight or with an unknown outcome.
var errSendOnceFull = errors.New("mcpbroker: send-once record is full for this session")

// SendOnceRecord records the gateway calls that were claimed for sending, by
// root session and operation ID, with the argument hash each was claimed
// with, so an operation that may have run is never sent again automatically,
// even with other arguments.
type SendOnceRecord interface {
	// Claim claims the operation, or returns ErrAlreadySent while it is in
	// flight or once it was sent, whatever the argument hash.
	Claim(ctx context.Context, rootSessionID, operationID, argumentsSHA256 string) error
	// Complete marks a claimed call as completed: the gateway answered it.
	// The record may then drop the claim to make room for a new one; the
	// engine never runs a call with a result again under the same call ID, so
	// the same operation ID is not claimed again.
	Complete(ctx context.Context, rootSessionID, operationID, argumentsSHA256 string) error
	// Release gives the claim up; callers release only a call no attempt of
	// which may have run.
	Release(ctx context.Context, rootSessionID, operationID, argumentsSHA256 string) error
	// Forget drops every claim of a root session that was deleted
	// permanently. A session that can resume keeps its claims.
	Forget(ctx context.Context, rootSessionID string) error
}

// sendOnceClaim is one operation's claim: the argument hash it was claimed
// with, and its place in completed once the call completed (nil while it is
// in flight or its outcome is unknown).
type sendOnceClaim struct {
	argumentsSHA256 string
	completed       *list.Element
}

// rootSendOnce is one root session's share of the record.
type rootSendOnce struct {
	claims map[string]*sendOnceClaim // by operation ID
	// completed holds the completed claims' operation IDs, oldest first.
	completed list.List
}

type memorySendOnce struct {
	perRoot int
	mu      sync.Mutex
	roots   map[string]*rootSendOnce
}

// NewMemorySendOnceRecord returns a SendOnceRecord holding at most perRoot
// claims for each root session. At the limit Claim drops the root session's
// oldest completed claim, and fails closed for that root session only when
// every claim held is in flight or has an unknown outcome; those are never
// dropped.
// Shortcut: the send-once record is in memory, holding calls in flight or with an unknown outcome until the root session is deleted and dropping the oldest completed calls when a root session's share is full; removed by #2730 (durable gateway call state in mecak8s).
func NewMemorySendOnceRecord(perRoot int) SendOnceRecord {
	return &memorySendOnce{perRoot: perRoot, roots: make(map[string]*rootSendOnce)}
}

func (m *memorySendOnce) Claim(_ context.Context, rootSessionID, operationID, argumentsSHA256 string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	root := m.roots[rootSessionID]
	if root == nil {
		if m.perRoot < 1 {
			return errSendOnceFull
		}
		root = &rootSendOnce{claims: make(map[string]*sendOnceClaim)}
		m.roots[rootSessionID] = root
	}
	if _, ok := root.claims[operationID]; ok {
		return ErrAlreadySent
	}
	if len(root.claims) >= m.perRoot {
		oldest := root.completed.Front()
		if oldest == nil {
			return errSendOnceFull
		}
		delete(root.claims, root.completed.Remove(oldest).(string))
	}
	root.claims[operationID] = &sendOnceClaim{argumentsSHA256: argumentsSHA256}
	return nil
}

// held returns the claim of operationID made with argumentsSHA256, or nil.
func (m *memorySendOnce) held(rootSessionID, operationID, argumentsSHA256 string) (*rootSendOnce, *sendOnceClaim) {
	root := m.roots[rootSessionID]
	if root == nil {
		return nil, nil
	}
	c := root.claims[operationID]
	if c == nil || c.argumentsSHA256 != argumentsSHA256 {
		return root, nil
	}
	return root, c
}

func (m *memorySendOnce) Complete(_ context.Context, rootSessionID, operationID, argumentsSHA256 string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if root, c := m.held(rootSessionID, operationID, argumentsSHA256); c != nil && c.completed == nil {
		c.completed = root.completed.PushBack(operationID)
	}
	return nil
}

func (m *memorySendOnce) Release(_ context.Context, rootSessionID, operationID, argumentsSHA256 string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	root, c := m.held(rootSessionID, operationID, argumentsSHA256)
	if c == nil {
		return nil
	}
	if c.completed != nil {
		root.completed.Remove(c.completed)
	}
	delete(root.claims, operationID)
	if len(root.claims) == 0 {
		delete(m.roots, rootSessionID)
	}
	return nil
}

func (m *memorySendOnce) Forget(_ context.Context, rootSessionID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.roots, rootSessionID)
	return nil
}
