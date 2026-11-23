// Package agentcredential holds the identity broker credentials of registered
// agents, in memory only, for the server, which registers a session's main
// agent at run entry and each helper agent when it is created, and the MCP
// broker, which presents them on gateway calls. A credential is
// never persisted and never given to the engine or the model. It also holds
// the run each helper acts in, delegated to it, and carries the main agent's
// run on the context, so a gateway call names its run.
package agentcredential

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
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
	// Owner is the owner the agent was registered with.
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
	// current is each helper's current delegation, by helper session, and
	// delegations every delegation in the order recorded.
	current     map[string]Delegation
	delegations []HelperDelegation
}

// renewalTimeout bounds a renewal, which runs apart from any one lookup's
// context because every lookup of its key shares it.
const renewalTimeout = 30 * time.Second

// renewal is one in-flight renewal, which concurrent lookups of its key wait
// for.
type renewal struct {
	// target is the broker session the renewal moves a helper to; "" for a
	// renewal in place.
	target string
	done   chan struct{}
	cred   Credential
	err    error
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
		f = &renewal{target: target, done: make(chan struct{})}
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
		if f.target != target {
			// The renewal in flight began before the root moved, so it renewed
			// in the old broker session: look up again to move.
			return h.Credential(ctx, key, renew)
		}
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
	// A moved helper's calls name its current delegation, which the new
	// broker session has not seen: report it there before the move is
	// stored, so a failed report leaves the helper to move again on its next
	// lookup.
	var reported Delegation
	if err == nil && target != "" {
		reported, err = h.reportCurrent(ctx, key, r, reg.Agent, target)
	}
	h.mu.Lock()
	if h.closed || h.roots[key.RootSessionID] != r {
		h.mu.Unlock()
		return Credential{}, ErrDropped
	}
	// Run entry may have stored a newer registration meanwhile; it wins.
	if current := r.entries[key.AgentSessionID]; current != e {
		h.mu.Unlock()
		return current.credential(), nil
	}
	if err != nil {
		defer h.mu.Unlock()
		if e.reg.ExpiresAt.After(h.now()) {
			return e.credential(), err
		}
		return Credential{}, err
	}
	n := newEntry(req, reg, now)
	r.entries[key.AgentSessionID] = n
	// A delegation set while the move was in flight was reported to the old
	// broker session: report it to the new one too.
	again := target != "" && r.current[key.AgentSessionID] != reported
	h.mu.Unlock()
	if again {
		if _, err := h.reportCurrent(ctx, key, r, reg.Agent, target); err != nil {
			return n.credential(), err
		}
	}
	return n.credential(), nil
}

// ReportDelegationAgain reports key's helper's current delegation, and the
// delegations its delegating run descends from, into the broker session its
// credential is held in now. A caller that set the delegation after reporting
// it to a broker session the helper has since left calls it, so the session
// the helper moved to sees the delegation its calls name. It returns
// ErrNoCredential when nothing is held for key.
func (h *Holder) ReportDelegationAgain(ctx context.Context, key Key) error {
	h.mu.Lock()
	r := h.roots[key.RootSessionID]
	var e *entry
	if r != nil {
		e = r.entries[key.AgentSessionID]
	}
	h.mu.Unlock()
	if e == nil {
		return ErrNoCredential
	}
	_, err := h.reportCurrent(ctx, key, r, e.reg.Agent, e.req.SessionID)
	return err
}

// reportLink is one delegation to report, with the agents it names.
type reportLink struct {
	d                     Delegation
	delegating, receiving identitybroker.AgentRef
}

// reportCurrent reports the current delegation of key's helper, whose agent
// is agent, into broker session target, after each delegation its delegating
// run descends from, so target can check every run in the chain: a helper's
// run may have been delegated from an earlier run of its parent, not the
// parent's current one. It returns the delegation reported, the zero
// Delegation when the helper has none. Reporting a delegation target already
// has succeeds, so the chain is reported whole each time. An ancestor the
// holder no longer keeps is skipped; target then refuses the report if it
// does not know the run.
func (h *Holder) reportCurrent(ctx context.Context, key Key, r *root, agent identitybroker.AgentRef, target string) (Delegation, error) {
	h.mu.Lock()
	d, ok := r.current[key.AgentSessionID]
	if !ok {
		h.mu.Unlock()
		return Delegation{}, nil
	}
	links, err := r.chainLocked(key, agent, d)
	h.mu.Unlock()
	if err != nil {
		return d, err
	}
	for _, l := range links {
		err := h.broker.ReportDelegation(ctx, identitybroker.ReportDelegationRequest{
			SessionID:    target,
			DelegationID: l.d.ID, DelegatingRun: l.d.DelegatingRun, DelegatingAgent: l.delegating,
			ReceivingRun: l.d.Run.ID, ReceivingAgent: l.receiving, Owner: l.d.Run.Owner,
		})
		if err != nil {
			return d, fmt.Errorf("report delegation %s again: %w", l.d.ID, err)
		}
	}
	return d, nil
}

// chainLocked returns d, the delegation of key's helper whose agent is
// agent, after the delegations its delegating run descends from, oldest
// first. The caller holds h.mu.
func (r *root) chainLocked(key Key, agent identitybroker.AgentRef, d Delegation) ([]reportLink, error) {
	agentOf := func(session string) (identitybroker.AgentRef, error) {
		if session == key.AgentSessionID {
			return agent, nil
		}
		if e := r.entries[session]; e != nil {
			return e.reg.Agent, nil
		}
		return identitybroker.AgentRef{}, fmt.Errorf("%w: agent %s of a delegation to report", ErrNoCredential, session)
	}
	chain := []HelperDelegation{{AgentSessionID: key.AgentSessionID, Delegation: d}}
	// Each step goes to an older delegation, so the walk ends; the bound
	// guards against a log that names a cycle.
	for cur := d; cur.DelegatingAgentSession != key.RootSessionID && len(chain) <= len(r.delegations); {
		i := slices.IndexFunc(r.delegations, func(p HelperDelegation) bool {
			return p.AgentSessionID == cur.DelegatingAgentSession && p.Run.ID == cur.DelegatingRun
		})
		if i < 0 {
			break
		}
		chain = append(chain, r.delegations[i])
		cur = r.delegations[i].Delegation
	}
	slices.Reverse(chain)
	links := make([]reportLink, len(chain))
	for i, c := range chain {
		delegating, err := agentOf(c.DelegatingAgentSession)
		if err != nil {
			return nil, err
		}
		receiving, err := agentOf(c.AgentSessionID)
		if err != nil {
			return nil, err
		}
		links[i] = reportLink{d: c.Delegation, delegating: delegating, receiving: receiving}
	}
	return links, nil
}

// MoveHelpers registers every helper agent held for root's session tree into
// the broker session root's main agent is registered to now, if it is not
// there already, parents first, keeping each helper's identity: what a
// reopened broker session needs once its main agent registered again. It
// returns the moves that failed, joined; a helper that failed to move gets
// no credential until a later lookup moves it.
func (h *Holder) MoveHelpers(ctx context.Context, root string) error {
	h.mu.Lock()
	r := h.roots[root]
	var keys []Key
	if r != nil {
		for agent, e := range r.entries {
			key := Key{RootSessionID: root, AgentSessionID: agent}
			if movedTo(r, key, e) != "" {
				keys = append(keys, key)
			}
		}
	}
	h.mu.Unlock()
	// Credential moves each helper's parent before the helper, so the order
	// here does not matter; it is sorted only to be deterministic.
	slices.SortFunc(keys, func(a, b Key) int { return strings.Compare(a.AgentSessionID, b.AgentSessionID) })
	var errs []error
	for _, key := range keys {
		if _, err := h.Credential(ctx, key, false); err != nil {
			errs = append(errs, fmt.Errorf("move helper %s: %w", key.AgentSessionID, err))
		}
	}
	return errors.Join(errs...)
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
