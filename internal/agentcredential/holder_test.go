package agentcredential_test

import (
	"context"
	"errors"
	"fmt"
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

func registration(inc, credential string, expiresAt time.Time) identitybroker.Registration {
	return identitybroker.Registration{
		Agent:      identitybroker.AgentRef{Subject: "spiffe://agents.example.com/mecatl/agent/shared/inst/" + inc},
		Credential: identitybroker.NewToken(credential),
		ExpiresAt:  expiresAt,
	}
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
	if err != nil || got.Token.Value() != "credential-1" || got.BrokerSessionID != "bs_1" {
		t.Fatalf("Credential before half its lifetime = %q in %q, %v; want credential-1 in bs_1", got.Token.Value(), got.BrokerSessionID, err)
	}
	// Run entry before renewAt in the same broker session does not register.
	mustEnsure(t, h, "bs_1")
	if n := len(b.requests()); n != 1 {
		t.Fatalf("registrations before half the lifetime = %d, want 1", n)
	}

	c.Advance(2 * time.Minute)
	got, err = h.Credential(t.Context(), key, false)
	if err != nil || got.Token.Value() != "credential-2" {
		t.Fatalf("Credential past half its lifetime = %q, %v; want the renewed credential-2", got.Token.Value(), err)
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
	if err != nil || got.Token.Value() != "credential-2" {
		t.Fatalf("Credential with renew = %q, %v; want the renewed credential-2", got.Token.Value(), err)
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
	if err != nil || got.Token.Value() != "credential-2" {
		t.Fatalf("Credential with a failed renewal = %q, %v; want the held credential-2", got.Token.Value(), err)
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
		if r.err != nil || r.cred.Token.Value() != "credential-blocked" {
			t.Fatalf("Credential = %q, %v; want the one renewal's credential", r.cred.Token.Value(), r.err)
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
	if got := <-second; got.Token.Value() != "credential-renewed" {
		t.Fatalf("waiting lookup = %q, want the renewed credential", got.Token.Value())
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
	if held, _ := h.Held(key); held.Token.Value() == "late" {
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
	answer(registration("inc_zzzzzzzzzzzzzzzzzzzzzzzzzz", "other", c.Now().Add(10*time.Minute)))
	if err := h.Ensure(t.Context(), key, request("bs_1"), admitAll); !errors.Is(err, agentcredential.ErrOtherAgent) {
		t.Fatalf("Ensure returning another incarnation's agent = %v, want ErrOtherAgent", err)
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
	// Same suffix, another agent: the held agent is kept.
	other := registration(incarnation, "other", c.Now().Add(10*time.Minute))
	other.Agent.Subject = "spiffe://elsewhere.example.com/mecatl/agent/shared/inst/" + incarnation
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
