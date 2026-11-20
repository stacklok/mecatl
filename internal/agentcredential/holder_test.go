package agentcredential_test

import (
	"context"
	"crypto/sha256"
	"encoding/base32"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stacklok/mecatl/internal/agentcredential"
	"github.com/stacklok/mecatl/internal/identitybroker"
)

const incarnation = "inc_abcdefghijklmnopqrstuvwxyz"

var key = agentcredential.Key{RootSessionID: "root", AgentSessionID: "root"}

// clock is a settable clock.
type clock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *clock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// fakeBroker answers RegisterAgent only. By default it returns the agent of
// the request's incarnation with a fresh credential valid for ten minutes;
// register, if set, answers instead.
type fakeBroker struct {
	identitybroker.Service
	clock *clock

	mu       sync.Mutex
	reqs     []identitybroker.RegisterAgentRequest
	register func(ctx context.Context, req identitybroker.RegisterAgentRequest) (identitybroker.Registration, error)
}

func (b *fakeBroker) RegisterAgent(ctx context.Context, req identitybroker.RegisterAgentRequest) (identitybroker.Registration, error) {
	b.mu.Lock()
	b.reqs = append(b.reqs, req)
	n, register := len(b.reqs), b.register
	b.mu.Unlock()
	if register != nil {
		return register(ctx, req)
	}
	return registration(req.Incarnation, fmt.Sprintf("credential-%d", n), b.clock.Now().Add(10*time.Minute)), nil
}

func (b *fakeBroker) setRegister(f func(context.Context, identitybroker.RegisterAgentRequest) (identitybroker.Registration, error)) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.register = f
}

func (b *fakeBroker) requests() []identitybroker.RegisterAgentRequest {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]identitybroker.RegisterAgentRequest(nil), b.reqs...)
}

// registration is the fake broker's answer for incarnation inc: an agent ID
// the fake derives from inc, standing in for the one a broker gives it, and a
// credential naming that agent, labelled label (see label).
func registration(inc, label string, expiresAt time.Time) identitybroker.Registration {
	sum := sha256.Sum256([]byte(inc))
	subject := "spiffe://agents.example.com/agent/ag_" + strings.ToLower(base32.StdEncoding.EncodeToString(sum[:]))[:26]
	return identitybroker.Registration{
		Agent:      identitybroker.AgentRef{Subject: subject},
		Credential: credential(subject, label),
		ExpiresAt:  expiresAt,
	}
}

// credential returns an unsigned JWT naming subject, labelled label. The
// holder reads a credential's sub without verifying it.
func credential(subject, label string) identitybroker.Token {
	enc := base64.RawURLEncoding
	claims, _ := json.Marshal(map[string]string{"sub": subject, "jti": label})
	return identitybroker.NewToken(enc.EncodeToString([]byte(`{"alg":"ES256"}`)) + "." + enc.EncodeToString(claims) + ".sig")
}

// label returns the label credential gave tok.
func label(tok identitybroker.Token) string {
	parts := strings.Split(tok.Value(), ".")
	if len(parts) != 3 {
		return ""
	}
	raw, _ := base64.RawURLEncoding.DecodeString(parts[1])
	var claims map[string]string
	_ = json.Unmarshal(raw, &claims)
	return claims["jti"]
}

func request(brokerSession string) identitybroker.RegisterAgentRequest {
	return identitybroker.RegisterAgentRequest{
		SessionID:     brokerSession,
		HostSessionID: "root",
		Owner:         identitybroker.Owner{Issuer: "https://login.example.com", Subject: "alice-204"},
		Incarnation:   incarnation,
		Definition:    identitybroker.DefinitionRef{Source: "system", Name: "main"},
	}
}

func admitAll() error { return nil }

func newHolder(t *testing.T) (*agentcredential.Holder, *fakeBroker, *clock) {
	t.Helper()
	c := &clock{now: time.Unix(1_000_000, 0)}
	b := &fakeBroker{clock: c}
	return agentcredential.New(b, c.Now), b, c
}

// blockRegistration makes the next registrations signal entered and wait
// for release.
func blockRegistration(b *fakeBroker) (entered chan struct{}, release chan struct{}) {
	entered, release = make(chan struct{}, 16), make(chan struct{})
	b.setRegister(func(_ context.Context, req identitybroker.RegisterAgentRequest) (identitybroker.Registration, error) {
		entered <- struct{}{}
		<-release
		return registration(req.Incarnation, "credential-blocked", b.clock.Now().Add(10*time.Minute)), nil
	})
	return entered, release
}

func mustEnsure(t *testing.T, h *agentcredential.Holder, brokerSession string) {
	t.Helper()
	if err := h.Ensure(t.Context(), key, request(brokerSession), admitAll); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
}

func TestHolderRenewsPastHalfLifetime(t *testing.T) {
	h, b, c := newHolder(t)
	mustEnsure(t, h, "bs_1")

	c.Advance(4 * time.Minute)
	got, err := h.Credential(t.Context(), key, false)
	if err != nil || label(got.Token) != "credential-1" || got.BrokerSessionID != "bs_1" {
		t.Fatalf("Credential before half its lifetime = %q in %q, %v; want credential-1 in bs_1", label(got.Token), got.BrokerSessionID, err)
	}
	// Run entry before renewAt in the same broker session does not register.
	mustEnsure(t, h, "bs_1")
	if n := len(b.requests()); n != 1 {
		t.Fatalf("registrations before half the lifetime = %d, want 1", n)
	}

	c.Advance(2 * time.Minute)
	got, err = h.Credential(t.Context(), key, false)
	if err != nil || label(got.Token) != "credential-2" {
		t.Fatalf("Credential past half its lifetime = %q, %v; want the renewed credential-2", label(got.Token), err)
	}
	reqs := b.requests()
	if len(reqs) != 2 || reqs[1] != request("bs_1") {
		t.Fatalf("renewal requests = %+v, want the held request again in bs_1", reqs)
	}
	if got.Owner != request("bs_1").Owner || got.ExpiresAt != c.Now().Add(10*time.Minute) {
		t.Fatalf("renewed credential = %+v, want the held owner and the new expiry", got)
	}
}

func TestHolderRenewsOnSVIDExpired(t *testing.T) {
	h, b, c := newHolder(t)
	mustEnsure(t, h, "bs_1")
	got, err := h.Credential(t.Context(), key, true)
	if err != nil || label(got.Token) != "credential-2" {
		t.Fatalf("Credential with renew = %q, %v; want the renewed credential-2", label(got.Token), err)
	}

	// A failed renewal after the broker refused the held credential returns
	// the renewal's error, not the refused credential...
	refused := identitybroker.ReasonError(identitybroker.ReasonSessionClosed, "closed")
	b.setRegister(func(context.Context, identitybroker.RegisterAgentRequest) (identitybroker.Registration, error) {
		return identitybroker.Registration{}, refused
	})
	if _, err := h.Credential(t.Context(), key, true); !errors.Is(err, refused) {
		t.Fatalf("Credential with renew and a failed renewal = %v, want the broker's error", err)
	}
	// ...a failed renewal past half-life returns the held credential while
	// it has not expired...
	c.Advance(6 * time.Minute)
	got, err = h.Credential(t.Context(), key, false)
	if err != nil || label(got.Token) != "credential-2" {
		t.Fatalf("Credential with a failed renewal = %q, %v; want the held credential-2", label(got.Token), err)
	}
	// ...and the broker's error once it has.
	c.Advance(10 * time.Minute)
	if _, err := h.Credential(t.Context(), key, false); !errors.Is(err, refused) {
		t.Fatalf("Credential with a failed renewal of an expired credential = %v, want the broker's error", err)
	}
}

// doneSignal reports when a lookup starts waiting: only a waiter for
// another lookup's renewal asks for its context's Done channel.
type doneSignal struct {
	context.Context
	once    sync.Once
	waiting chan<- struct{}
}

func (c *doneSignal) Done() <-chan struct{} {
	c.once.Do(func() { c.waiting <- struct{}{} })
	return c.Context.Done()
}

func TestHolderRenewalIsSingleFlight(t *testing.T) {
	h, b, c := newHolder(t)
	mustEnsure(t, h, "bs_1")
	c.Advance(6 * time.Minute)
	entered, release := blockRegistration(b)

	const callers = 8
	waiting := make(chan struct{}, callers)
	type result struct {
		cred agentcredential.Credential
		err  error
	}
	results := make(chan result, callers)
	for range callers {
		ctx := &doneSignal{Context: t.Context(), waiting: waiting}
		go func() {
			cred, err := h.Credential(ctx, key, false)
			results <- result{cred, err}
		}()
	}
	// One caller registers; every other one waits for it.
	<-entered
	for range callers - 1 {
		select {
		case <-waiting:
		case <-entered:
			t.Fatal("a second registration started while one was in flight")
		}
	}
	close(release)
	for range callers {
		r := <-results
		if r.err != nil || label(r.cred.Token) != "credential-blocked" {
			t.Fatalf("Credential = %q, %v; want the one renewal's credential", label(r.cred.Token), r.err)
		}
	}
	if n := len(b.requests()); n != 2 {
		t.Fatalf("registrations = %d, want Ensure's and one renewal", n)
	}
}

func TestHolderRenewalOutlivesTheFirstLookupsCancellation(t *testing.T) {
	h, b, _ := newHolder(t)
	mustEnsure(t, h, "bs_1")
	var renewCtx context.Context
	entered, release := make(chan struct{}, 1), make(chan struct{})
	b.setRegister(func(ctx context.Context, req identitybroker.RegisterAgentRequest) (identitybroker.Registration, error) {
		renewCtx = ctx
		entered <- struct{}{}
		<-release
		return registration(req.Incarnation, "credential-renewed", b.clock.Now().Add(10*time.Minute)), nil
	})

	first, cancel := context.WithCancel(t.Context())
	firstDone := make(chan error, 1)
	go func() {
		_, err := h.Credential(first, key, true)
		firstDone <- err
	}()
	<-entered
	waiting := make(chan struct{}, 1)
	second := make(chan agentcredential.Credential, 1)
	go func() {
		cred, _ := h.Credential(&doneSignal{Context: t.Context(), waiting: waiting}, key, true)
		second <- cred
	}()
	<-waiting
	// The first lookup's run is cancelled: it returns, and the renewal goes on.
	cancel()
	select {
	case err := <-firstDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled lookup = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		close(release)
		t.Fatal("a cancelled lookup kept waiting for the shared renewal")
	}
	if renewCtx.Err() != nil {
		t.Fatal("the shared renewal was cancelled with the first lookup")
	}
	close(release)
	if got := <-second; label(got.Token) != "credential-renewed" {
		t.Fatalf("waiting lookup = %q, want the renewed credential", label(got.Token))
	}
}

func TestHolderChecksARegistrationAgainstTheTimeItReturns(t *testing.T) {
	h, b, c := newHolder(t)
	// The registration takes two minutes and returns a credential that
	// expired one minute into it.
	b.setRegister(func(_ context.Context, req identitybroker.RegisterAgentRequest) (identitybroker.Registration, error) {
		expiry := c.Now().Add(time.Minute)
		c.Advance(2 * time.Minute)
		return registration(req.Incarnation, "late", expiry), nil
	})
	if err := h.Ensure(t.Context(), key, request("bs_1"), admitAll); !errors.Is(err, agentcredential.ErrExpired) {
		t.Fatalf("Ensure with a credential that expired during registration = %v, want ErrExpired", err)
	}

	b.setRegister(nil)
	mustEnsure(t, h, "bs_1")
	b.setRegister(func(_ context.Context, req identitybroker.RegisterAgentRequest) (identitybroker.Registration, error) {
		expiry := c.Now().Add(time.Minute)
		c.Advance(2 * time.Minute)
		return registration(req.Incarnation, "late", expiry), nil
	})
	if _, err := h.Credential(t.Context(), key, true); !errors.Is(err, agentcredential.ErrExpired) {
		t.Fatalf("renewal with a credential that expired during registration = %v, want ErrExpired", err)
	}
	if held, _ := h.Held(key); label(held.Token) == "late" {
		t.Fatal("an expired renewal replaced the held credential")
	}
}

func TestHolderLookupNeverRegistersMissingEntry(t *testing.T) {
	h, b, _ := newHolder(t)
	for _, renew := range []bool{false, true} {
		if _, err := h.Credential(t.Context(), key, renew); !errors.Is(err, agentcredential.ErrNoCredential) {
			t.Fatalf("Credential(renew=%v) with nothing held = %v, want ErrNoCredential", renew, err)
		}
	}
	// Another agent of a held root is missing too.
	mustEnsure(t, h, "bs_1")
	child := agentcredential.Key{RootSessionID: "root", AgentSessionID: "child"}
	if _, err := h.Credential(t.Context(), child, true); !errors.Is(err, agentcredential.ErrNoCredential) {
		t.Fatalf("Credential for an unregistered child = %v, want ErrNoCredential", err)
	}
	h.Drop("root")
	if _, err := h.Credential(t.Context(), key, true); !errors.Is(err, agentcredential.ErrNoCredential) {
		t.Fatalf("Credential after Drop = %v, want ErrNoCredential", err)
	}
	if n := len(b.requests()); n != 1 {
		t.Fatalf("registrations = %d, want only Ensure's", n)
	}
}

func TestHolderRenewalCannotReAddDroppedEntry(t *testing.T) {
	t.Run("renewal", func(t *testing.T) {
		h, b, _ := newHolder(t)
		mustEnsure(t, h, "bs_1")
		entered, release := blockRegistration(b)
		done := make(chan error, 1)
		go func() {
			_, err := h.Credential(t.Context(), key, true)
			done <- err
		}()
		<-entered
		h.Drop("root")
		close(release)
		if err := <-done; !errors.Is(err, agentcredential.ErrDropped) {
			t.Fatalf("renewal finishing after Drop = %v, want ErrDropped", err)
		}
		if _, held := h.Held(key); held {
			t.Fatal("a renewal re-added a dropped entry")
		}
	})
	t.Run("run entry", func(t *testing.T) {
		h, b, _ := newHolder(t)
		entered, release := blockRegistration(b)
		done := make(chan error, 1)
		go func() { done <- h.Ensure(t.Context(), key, request("bs_1"), admitAll) }()
		<-entered
		h.Drop("root")
		close(release)
		if err := <-done; !errors.Is(err, agentcredential.ErrDropped) {
			t.Fatalf("Ensure finishing after Drop = %v, want ErrDropped", err)
		}
		if _, held := h.Held(key); held {
			t.Fatal("run entry re-added a dropped entry")
		}
		// Positive control: run entry after the drop stores again.
		b.setRegister(nil)
		mustEnsure(t, h, "bs_2")
		if got, held := h.Held(key); !held || got.BrokerSessionID != "bs_2" {
			t.Fatalf("held after a new run entry = %+v, %v; want bs_2", got, held)
		}
	})
}

func TestHolderRefusesOtherAgentAndExpired(t *testing.T) {
	h, b, c := newHolder(t)
	answer := func(reg identitybroker.Registration) {
		b.setRegister(func(context.Context, identitybroker.RegisterAgentRequest) (identitybroker.Registration, error) {
			return reg, nil
		})
	}
	malformed := registration(incarnation, "malformed", c.Now().Add(10*time.Minute))
	malformed.Agent.Subject = "spiffe://agents.example.com/mecatl/agent/shared/inst/" + incarnation
	malformed.Credential = credential(malformed.Agent.Subject, "malformed")
	answer(malformed)
	if err := h.Ensure(t.Context(), key, request("bs_1"), admitAll); !errors.Is(err, agentcredential.ErrOtherAgent) {
		t.Fatalf("Ensure returning an agent ID of another form = %v, want ErrOtherAgent", err)
	}
	misnamed := registration(incarnation, "misnamed", c.Now().Add(10*time.Minute))
	misnamed.Credential = registration("inc_zzzzzzzzzzzzzzzzzzzzzzzzzz", "misnamed", c.Now().Add(10*time.Minute)).Credential
	answer(misnamed)
	if err := h.Ensure(t.Context(), key, request("bs_1"), admitAll); !errors.Is(err, agentcredential.ErrOtherAgent) {
		t.Fatalf("Ensure returning a credential for another agent = %v, want ErrOtherAgent", err)
	}
	answer(registration(incarnation, "expired", c.Now()))
	if err := h.Ensure(t.Context(), key, request("bs_1"), admitAll); !errors.Is(err, agentcredential.ErrExpired) {
		t.Fatalf("Ensure returning an expired credential = %v, want ErrExpired", err)
	}
	if _, held := h.Held(key); held {
		t.Fatal("a refused registration was stored")
	}

	b.setRegister(nil)
	mustEnsure(t, h, "bs_1")
	held, _ := h.Held(key)
	// A well-formed registration of another agent: the held agent is kept.
	other := registration("inc_zzzzzzzzzzzzzzzzzzzzzzzzzz", "other", c.Now().Add(10*time.Minute))
	answer(other)
	if err := h.Ensure(t.Context(), key, request("bs_2"), admitAll); !errors.Is(err, agentcredential.ErrOtherAgent) {
		t.Fatalf("Ensure returning another agent than the held one = %v, want ErrOtherAgent", err)
	}
	// Renewal applies the same checks and keeps the held credential: a lookup
	// with renew gets the refusal, one past half-life the held credential.
	if _, err := h.Credential(t.Context(), key, true); !errors.Is(err, agentcredential.ErrOtherAgent) {
		t.Fatalf("Credential with renew when renewal returned another agent = %v, want ErrOtherAgent", err)
	}
	c.Advance(6 * time.Minute)
	got, err := h.Credential(t.Context(), key, false)
	if err != nil || got != held {
		t.Fatalf("Credential past half-life when renewal returned another agent = %+v, %v; want the held one", got, err)
	}
	if now, _ := h.Held(key); now != held {
		t.Fatalf("held after refused registrations = %+v, want %+v", now, held)
	}

	// admit refusing keeps nothing new.
	b.setRegister(nil)
	refused := errors.New("lease lost")
	if err := h.Ensure(t.Context(), key, request("bs_3"), func() error { return refused }); !errors.Is(err, refused) {
		t.Fatalf("Ensure with admit refusing = %v, want admit's error", err)
	}
	if now, _ := h.Held(key); now != held {
		t.Fatalf("held after admit refused = %+v, want %+v", now, held)
	}
}

func TestHolderClosedRefusesStore(t *testing.T) {
	h, b, _ := newHolder(t)
	entered, release := blockRegistration(b)
	done := make(chan error, 1)
	go func() { done <- h.Ensure(t.Context(), key, request("bs_1"), admitAll) }()
	<-entered
	h.Close()
	close(release)
	if err := <-done; !errors.Is(err, agentcredential.ErrDropped) {
		t.Fatalf("Ensure finishing after Close = %v, want ErrDropped", err)
	}
	if _, held := h.Held(key); held {
		t.Fatal("a registration finishing after Close was stored")
	}
	b.setRegister(nil)
	if err := h.Ensure(t.Context(), key, request("bs_1"), admitAll); !errors.Is(err, agentcredential.ErrDropped) {
		t.Fatalf("Ensure after Close = %v, want ErrDropped", err)
	}
	if n := len(b.requests()); n != 1 {
		t.Fatalf("registrations = %d, want none after Close", n)
	}
	if _, err := h.Credential(t.Context(), key, true); !errors.Is(err, agentcredential.ErrNoCredential) {
		t.Fatalf("Credential after Close = %v, want ErrNoCredential", err)
	}
}

func TestNilHolderDropAndCloseAreNoOps(*testing.T) {
	var h *agentcredential.Holder
	h.Drop(key.RootSessionID)
	h.Close()
}

// helperRequest registers the helper running in host session agent, created
// under parentHost, with an incarnation derived from agent.
func helperRequest(brokerSession, agent, parentHost string, parent identitybroker.AgentRef) identitybroker.RegisterAgentRequest {
	sum := sha256.Sum256([]byte(agent))
	req := request(brokerSession)
	req.HostSessionID = agent
	req.Incarnation = "inc_" + strings.ToLower(base32.StdEncoding.EncodeToString(sum[:]))[:26]
	req.Definition = identitybroker.DefinitionRef{Source: "system", Name: "explorer"}
	req.Parent = parent
	req.ParentHostSessionID = parentHost
	return req
}

// TestHolderMovesHelpersAfterTheirRoot pins that a helper's credential follows
// its root's main agent into a new broker session, parents first, keeping its
// identity, and that a helper that cannot move gets no stale credential.
func TestHolderMovesHelpersAfterTheirRoot(t *testing.T) {
	h, b, _ := newHolder(t)
	mustEnsure(t, h, "bs_1")
	main, _ := h.Held(key)
	childKey := agentcredential.Key{RootSessionID: "root", AgentSessionID: "child"}
	grandKey := agentcredential.Key{RootSessionID: "root", AgentSessionID: "grandchild"}
	if err := h.Ensure(t.Context(), childKey, helperRequest("bs_1", "child", "root", main.Agent), admitAll); err != nil {
		t.Fatalf("Ensure child: %v", err)
	}
	child, _ := h.Held(childKey)
	if err := h.Ensure(t.Context(), grandKey, helperRequest("bs_1", "grandchild", "child", child.Agent), admitAll); err != nil {
		t.Fatalf("Ensure grandchild: %v", err)
	}
	grand, _ := h.Held(grandKey)

	// Before the root moves, a helper's lookup does not register.
	before := len(b.requests())
	if got, err := h.Credential(t.Context(), grandKey, false); err != nil || got.BrokerSessionID != "bs_1" {
		t.Fatalf("Credential before the root moved = %+v, %v; want the held one in bs_1", got, err)
	}
	if n := len(b.requests()); n != before {
		t.Fatalf("a helper's lookup before its root moved registered %d times", n-before)
	}

	// The main agent moves at run entry; the grandchild's lookup moves its
	// parent first, then itself.
	mustEnsure(t, h, "bs_2")
	before = len(b.requests())
	got, err := h.Credential(t.Context(), grandKey, false)
	if err != nil || got.BrokerSessionID != "bs_2" || got.Agent != grand.Agent {
		t.Fatalf("Credential after the root moved = %+v, %v; want %+v in bs_2", got, err, grand.Agent)
	}
	reqs := b.requests()[before:]
	if len(reqs) != 2 || reqs[0].HostSessionID != "child" || reqs[1].HostSessionID != "grandchild" {
		t.Fatalf("registrations after the root moved = %+v, want the child then the grandchild", reqs)
	}
	for _, req := range reqs {
		if req.SessionID != "bs_2" {
			t.Fatalf("a helper registered into %q, want bs_2", req.SessionID)
		}
	}
	if moved, _ := h.Held(childKey); moved.BrokerSessionID != "bs_2" || moved.Agent != child.Agent {
		t.Fatalf("the child is held as %+v, want %+v in bs_2", moved, child.Agent)
	}

	// A helper that cannot move gets the error, not its credential for the
	// closed broker session.
	mustEnsure(t, h, "bs_3")
	refused := errors.New("refused")
	b.setRegister(func(context.Context, identitybroker.RegisterAgentRequest) (identitybroker.Registration, error) {
		return identitybroker.Registration{}, refused
	})
	if got, err := h.Credential(t.Context(), childKey, false); !errors.Is(err, refused) {
		t.Fatalf("Credential of a helper that cannot move = %+v, %v; want the refusal", got, err)
	}
}

// TestHolderHelperLookupAfterTheRootMovedDoesNotJoinAStaleRenewal pins that a
// lookup made after the root moved, while an in-place renewal from before
// the move is in flight, still moves the helper.
func TestHolderHelperLookupAfterTheRootMovedDoesNotJoinAStaleRenewal(t *testing.T) {
	h, b, c := newHolder(t)
	mustEnsure(t, h, "bs_1")
	main, _ := h.Held(key)
	childKey := agentcredential.Key{RootSessionID: "root", AgentSessionID: "child"}
	if err := h.Ensure(t.Context(), childKey, helperRequest("bs_1", "child", "root", main.Agent), admitAll); err != nil {
		t.Fatalf("Ensure child: %v", err)
	}
	entered, release := make(chan struct{}, 1), make(chan struct{})
	b.setRegister(func(_ context.Context, req identitybroker.RegisterAgentRequest) (identitybroker.Registration, error) {
		if req.HostSessionID == "child" && req.SessionID == "bs_1" {
			entered <- struct{}{}
			<-release
		}
		return registration(req.Incarnation, "credential-"+req.SessionID, b.clock.Now().Add(10*time.Minute)), nil
	})
	// Past half its lifetime, the child renews in place, in bs_1.
	c.Advance(6 * time.Minute)
	stale := make(chan error, 1)
	go func() {
		_, err := h.Credential(context.Background(), childKey, false)
		stale <- err
	}()
	<-entered
	// The main agent moves; a lookup now must move the child.
	mustEnsure(t, h, "bs_2")
	moved := make(chan agentcredential.Credential, 1)
	go func() {
		got, err := h.Credential(context.Background(), childKey, false)
		if err != nil {
			t.Errorf("Credential after the root moved: %v", err)
		}
		moved <- got
	}()
	time.Sleep(50 * time.Millisecond) // let the second lookup join the renewal in flight
	close(release)
	if err := <-stale; err != nil {
		t.Fatalf("the in-place renewal: %v", err)
	}
	if got := <-moved; got.BrokerSessionID != "bs_2" {
		t.Fatalf("a lookup after the root moved got a credential in %q, want bs_2", got.BrokerSessionID)
	}
}
