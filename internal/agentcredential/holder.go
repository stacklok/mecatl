// Package agentcredential holds the identity broker credentials of registered
// agents, in memory only, for the server, which registers a session's main
// agent at run entry and each helper agent when it is created, and the MCP
// broker, which presents them on gateway calls. A credential is
// never persisted and never given to the engine or the model.
package agentcredential

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/stacklok/mecatl/internal/identitybroker"
)

// Key names a held credential. For a session's main agent both IDs are the
// root host session; for a helper agent, AgentSessionID is its own session.
type Key struct{ RootSessionID, AgentSessionID string }

// Credential is a held agent credential and what it was registered with.
type Credential struct {
	// BrokerSessionID is the broker session the agent was registered to; the
	// credential verifies only there.
	BrokerSessionID string
	Agent           identitybroker.AgentRef
	// Owner is the owner the agent was registered with: the run owner under
	// the #2557 shortcut.
	Owner     identitybroker.Owner
	Token     identitybroker.Token
	ExpiresAt time.Time
}

var (
	// ErrNoCredential is a lookup's error when nothing is held for the key; a
	// lookup never registers.
	ErrNoCredential = errors.New("agentcredential: no credential held")
	// ErrDropped refuses to store a registration whose root was dropped, or
	// the holder closed, while it was in flight.
	ErrDropped = errors.New("agentcredential: entry dropped during registration")
	// ErrOtherAgent refuses a registration that returned an agent other than
	// the one registered.
	ErrOtherAgent = errors.New("agentcredential: broker returned another agent")
	// ErrExpired refuses a registration that returned an expired credential.
	ErrExpired = errors.New("agentcredential: broker returned an expired credential")
)

// entry is one held registration.
type entry struct {
	// req is the request the agent was registered with; renewal sends it
	// again, so it names the held broker session and incarnation.
	req identitybroker.RegisterAgentRequest
	reg identitybroker.Registration
	// renewAt is when half the credential's lifetime has passed.
	renewAt time.Time
}

func (e *entry) credential() Credential {
	return Credential{
		BrokerSessionID: e.req.SessionID,
		Agent:           e.reg.Agent,
		Owner:           e.req.Owner,
		Token:           e.reg.Credential,
		ExpiresAt:       e.reg.ExpiresAt,
	}
}

// root holds the entries of one root session. Drop replaces it, so a store
// started before the drop finds another root (or none) and is refused: the
// root's identity is its epoch.
type root struct {
	entries map[string]*entry
}

// renewalTimeout bounds a renewal, which runs apart from any one lookup's
// context because every lookup of its key shares it.
const renewalTimeout = 30 * time.Second

// renewal is one in-flight renewal, which concurrent lookups of its key wait
// for.
type renewal struct {
	done chan struct{}
	cred Credential
	err  error
}

// Holder holds agent credentials. Callers that take their own lock around
// Drop take it before the holder's; the holder never calls out while holding
// its own lock.
type Holder struct {
	broker identitybroker.Service
	now    func() time.Time

	mu       sync.Mutex
	closed   bool
	roots    map[string]*root
	renewals map[Key]*renewal
}

// New returns a Holder registering through broker, with now as its clock.
func New(broker identitybroker.Service, now func() time.Time) *Holder {
	return &Holder{broker: broker, now: now, roots: make(map[string]*root), renewals: make(map[Key]*renewal)}
}

// Ensure is the run-entry path. It registers the agent with req unless the
// held entry has the same incarnation and broker session and is before
// renewAt. A registration must return an unexpired credential that
// identitybroker.CheckRegistration accepts, for an agent that, with a
// credential held for the same incarnation, is the held agent; otherwise Ensure returns ErrExpired or
// ErrOtherAgent and keeps what it held. admit is called, without the holder's
// lock, only once such a registration succeeded, and its error is returned
// unchanged; the registration is then stored only if key's root was not
// dropped since Ensure started and the holder is not closed (ErrDropped).
// A broker error is returned unchanged.
func (h *Holder) Ensure(ctx context.Context, key Key, req identitybroker.RegisterAgentRequest, admit func() error) error {
	now := h.now()
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return ErrDropped
	}
	r := h.roots[key.RootSessionID]
	if r == nil {
		r = &root{entries: make(map[string]*entry)}
		h.roots[key.RootSessionID] = r
	}
	held := r.entries[key.AgentSessionID]
	h.mu.Unlock()
	// A credential held for another incarnation of this session ID belongs to
	// an agent that no longer exists here: replace it rather than compare.
	if held != nil && held.req.Incarnation != req.Incarnation {
		held = nil
	}
	if held != nil && held.req.SessionID == req.SessionID && now.Before(held.renewAt) {
		return nil
	}
	reg, err := h.broker.RegisterAgent(ctx, req)
	if err != nil {
		return err
	}
	// The registration may have taken a while: check it against the time now.
	now = h.now()
	if err := checkRegistration(reg, held, now); err != nil {
		return err
	}
	if err := admit(); err != nil {
		return err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed || h.roots[key.RootSessionID] != r {
		return ErrDropped
	}
	r.entries[key.AgentSessionID] = newEntry(req, reg, now)
	return nil
}

// checkRegistration applies run entry's checks to reg.
// held, if not nil, is the entry held for the same incarnation.
func checkRegistration(reg identitybroker.Registration, held *entry, now time.Time) error {
	// The in-process broker never returns an expired credential; this guards a
	// remote broker, whose credential would otherwise be renewed at no point.
	if !reg.ExpiresAt.After(now) {
		return ErrExpired
	}
	// A mismatch keeps the held credential, so every later registration fails
	// the same way until the root is dropped. That is deliberate: an
	// incarnation keeps its agent ID for good, so the ID changes only if the
	// broker misbehaves, and dropping the held agent would let the next
	// registration accept the wrong one. While the broker runs in process, a
	// legitimate change, such as another trust domain, comes with a restart,
	// which clears what is held; a remote broker can change without one. The
	// comparison lasts only while this process holds the credential. Agent IDs
	// are opaque, so across a restart, release, or lease loss nothing ties the
	// returned agent to the incarnation but the broker's own check.
	if identitybroker.CheckRegistration(reg) != nil || (held != nil && reg.Agent != held.reg.Agent) {
		return ErrOtherAgent
	}
	return nil
}

func newEntry(req identitybroker.RegisterAgentRequest, reg identitybroker.Registration, now time.Time) *entry {
	return &entry{req: req, reg: reg, renewAt: now.Add(reg.ExpiresAt.Sub(now) / 2)}
}

// Credential is the MCP broker's lookup. With no entry for key it returns
// ErrNoCredential: it never opens, reopens, or registers. Past renewAt, or
// with renew set (after svid_expired), it renews the credential. A helper
// agent's credential is also registered again when its root's main agent has
// moved to another broker session since: the helper moves there too, after
// its parent, keeping its identity. Renewal is single-flight per key: every
// lookup, the first included, waits for the one in flight or for its own
// context, and one lookup's cancellation does not end the renewal. A renewal
// is stored only if key's root was not dropped and the holder not closed
// since it started (ErrDropped otherwise). If it fails otherwise, a lookup
// past half-life gets the held credential while it has not expired; a lookup
// with renew, made after the broker refused the held credential, or for a
// helper that could not move, gets the renewal's error.
func (h *Holder) Credential(ctx context.Context, key Key, renew bool) (Credential, error) {
	h.mu.Lock()
	r := h.roots[key.RootSessionID]
	var e *entry
	if r != nil {
		e = r.entries[key.AgentSessionID]
	}
	if e == nil {
		h.mu.Unlock()
		return Credential{}, ErrNoCredential
	}
	target := movedTo(r, key, e)
	if !renew && target == "" && h.now().Before(e.renewAt) {
		h.mu.Unlock()
		return e.credential(), nil
	}
	f := h.renewals[key]
	if f == nil {
		f = &renewal{done: make(chan struct{})}
		h.renewals[key] = f
		go func() {
			rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), renewalTimeout)
			defer cancel()
			f.cred, f.err = h.renew(rctx, key, r, e, target)
			h.mu.Lock()
			delete(h.renewals, key)
			h.mu.Unlock()
			close(f.done)
		}()
	}
	h.mu.Unlock()
	select {
	case <-f.done:
		return f.result(renew || target != "")
	case <-ctx.Done():
		return Credential{}, ctx.Err()
	}
}

// movedTo returns the broker session a helper agent's entry e must move to:
// the one its root's main agent is registered to now, if that differs from
// e's. It returns "" for a main agent, or a helper still in its root's broker
// session. The caller holds h.mu.
func movedTo(r *root, key Key, e *entry) string {
	if key.AgentSessionID == key.RootSessionID {
		return ""
	}
	main := r.entries[key.RootSessionID]
	if main == nil || main.req.SessionID == e.req.SessionID {
		return ""
	}
	return main.req.SessionID
}

// result is a renewal's answer to a lookup: on a failure with an unexpired
// held credential, the held one, unless the lookup must not fall back to it.
func (f *renewal) result(noFallback bool) (Credential, error) {
	if f.err == nil || (!noFallback && !f.cred.ExpiresAt.IsZero()) {
		return f.cred, nil
	}
	return Credential{}, f.err
}

// renew registers e's agent again and stores the result in r, if r is still
// key's root and e its entry. With a target broker session, e's helper agent
// moves there, after its parent if that is another helper. A failure returns
// its error, and the held credential too while it has not expired.
func (h *Holder) renew(ctx context.Context, key Key, r *root, e *entry, target string) (Credential, error) {
	req := e.req
	if target != "" {
		// Parents first: the broker refuses a child whose parent is not
		// registered to the broker session. The main agent moved already.
		if parent := req.ParentHostSessionID; parent != key.RootSessionID {
			if _, err := h.Credential(ctx, Key{RootSessionID: key.RootSessionID, AgentSessionID: parent}, false); err != nil {
				return Credential{}, err
			}
		}
		req.SessionID = target
	}
	// Shortcut: a credential is renewed by registering again in the held broker session; removed by #2562 (a renew call).
	reg, err := h.broker.RegisterAgent(ctx, req)
	now := h.now()
	if err == nil {
		err = checkRegistration(reg, e, now)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed || h.roots[key.RootSessionID] != r {
		return Credential{}, ErrDropped
	}
	// Run entry may have stored a newer registration meanwhile; it wins.
	if current := r.entries[key.AgentSessionID]; current != e {
		return current.credential(), nil
	}
	if err != nil {
		if e.reg.ExpiresAt.After(h.now()) {
			return e.credential(), err
		}
		return Credential{}, err
	}
	n := newEntry(req, reg, now)
	r.entries[key.AgentSessionID] = n
	return n.credential(), nil
}

// Held returns the credential held for key without renewing it, and whether
// one is held.
func (h *Holder) Held(key Key) (Credential, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if r := h.roots[key.RootSessionID]; r != nil {
		if e := r.entries[key.AgentSessionID]; e != nil {
			return e.credential(), true
		}
	}
	return Credential{}, false
}

// Drop drops every credential held for root's session tree, and refuses to
// store any registration or renewal for it already in flight. Callers that
// guard their own state with a lock call it holding that lock. A nil Holder
// holds nothing.
func (h *Holder) Drop(root string) {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.roots, root)
}

// Close drops every credential; later and in-flight stores are refused with
// ErrDropped, and lookups return ErrNoCredential. A nil Holder holds nothing.
func (h *Holder) Close() {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.closed = true
	h.roots = make(map[string]*root)
}
