package server_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/server"
	"github.com/stacklok/mecatl/internal/identitybroker"
)

// wantRegistered checks the last registration registered sess's main agent in
// brokerSession, and returns it.
func wantRegistered(t *testing.T, broker *recordingBroker, brokerSession identitybroker.Session, sess *session.Session) identitybroker.RegisterAgentRequest {
	t.Helper()
	regs := broker.registerRequests()
	if len(regs) == 0 {
		t.Fatalf("session %s registered no agent", sess.ID)
	}
	got := regs[len(regs)-1]
	want := identitybroker.RegisterAgentRequest{
		SessionID:     brokerSession.ID,
		HostSessionID: string(sess.ID),
		Owner:         identitybroker.Owner{Issuer: brokerAlice.Issuer, Subject: brokerAlice.Subject},
		Incarnation:   string(sess.Incarnation()),
		Definition:    identitybroker.DefinitionRef{Source: "system", Name: "main"},
	}
	if got != want {
		t.Fatalf("registration = %+v, want %+v", got, want)
	}
	return got
}

// clock is a controllable time source for the server.
type clock struct{ now time.Time }

func (c *clock) Now() time.Time { return c.now }

func TestIdentityBrokerRegistersTheMainAgentOnCreate(t *testing.T) {
	broker := newRecordingBroker()
	svc := newIdentityBrokerService(t, broker, mockllm.New(mockllm.TextTurn("done")))
	ctx := aliceContext(t)
	sess, err := svc.CreateSession(ctx, session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	wantRegistered(t, broker, wantBrokerSession(t, broker, sess), sess)
	// The held credential is still current, so run entry does not register again.
	if err := runToEnd(ctx, t, svc, sess.ID); err != nil {
		t.Fatalf("run: %v", err)
	}
	if n := len(broker.registerRequests()); n != 1 {
		t.Fatalf("registrations = %d, want 1", n)
	}
}

func TestIdentityBrokerRegistersAgainPastHalfTheCredentialLifetime(t *testing.T) {
	broker := newRecordingBroker() // five-minute credentials
	c := &clock{now: time.Now()}
	svc := newIdentityBrokerService(t, broker, mockllm.New(mockllm.TextTurn("one"), mockllm.TextTurn("two")), func(cfg *server.Config) { cfg.Now = c.Now })
	ctx := aliceContext(t)
	sess, err := svc.CreateSession(ctx, session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	c.now = c.now.Add(2 * time.Minute) // under half
	if err := runToEnd(ctx, t, svc, sess.ID); err != nil {
		t.Fatalf("run: %v", err)
	}
	if n := len(broker.registerRequests()); n != 1 {
		t.Fatalf("registrations before half the lifetime = %d, want 1", n)
	}
	c.now = c.now.Add(time.Minute) // past half
	if err := runToEnd(ctx, t, svc, sess.ID); err != nil {
		t.Fatalf("run: %v", err)
	}
	if n := len(broker.registerRequests()); n != 2 {
		t.Fatalf("registrations past half the lifetime = %d, want 2", n)
	}
}

func TestIdentityBrokerRegistersAgainInAReopenedBrokerSession(t *testing.T) {
	broker := newRecordingBroker()
	svc := newIdentityBrokerService(t, broker, mockllm.New(mockllm.TextTurn("done")))
	ctx := aliceContext(t)
	sess, err := svc.CreateSession(ctx, session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	first := wantBrokerSession(t, broker, sess)
	if err := broker.inner.CloseSession(t.Context(), identitybroker.CloseSessionRequest{SessionID: first.ID, Reason: identitybroker.ReasonSessionClosed}); err != nil {
		t.Fatal(err)
	}
	if err := runToEnd(ctx, t, svc, sess.ID); err != nil {
		t.Fatalf("run: %v", err)
	}
	reopened := wantBrokerSession(t, broker, sess)
	wantRegistered(t, broker, reopened, sess)
	if reopened.ID == first.ID {
		t.Fatal("the agent registered into the closed broker session")
	}
}

func TestIdentityBrokerRegistersAgainAfterARelease(t *testing.T) {
	// Releasing a session locally, as a restart does, drops the held credential.
	broker := newRecordingBroker()
	svc := newIdentityBrokerService(t, broker, mockllm.New(mockllm.TextTurn("done")))
	ctx := aliceContext(t)
	sess, err := svc.CreateSession(ctx, session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	svc.CloseSession(sess.ID)
	if err := runToEnd(ctx, t, svc, sess.ID); err != nil {
		t.Fatalf("run: %v", err)
	}
	if n := len(broker.registerRequests()); n != 2 {
		t.Fatalf("registrations = %d, want 2", n)
	}
	wantRegistered(t, broker, wantBrokerSession(t, broker, sess), sess)
}

func TestIdentityBrokerRegistrationUnavailableStopsTheRun(t *testing.T) {
	broker := newRecordingBroker()
	llm := mockllm.New(mockllm.TextTurn("done"))
	svc := newIdentityBrokerService(t, broker, llm)
	ctx := aliceContext(t)
	broker.set(func(b *recordingBroker) { b.registerUnavailable = true })
	sess, err := svc.CreateSession(ctx, session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatalf("create with registration down: %v", err)
	}
	if err := runToEnd(ctx, t, svc, sess.ID); !errors.Is(err, server.ErrIdentityBrokerUnavailable) {
		t.Fatalf("run with registration down: %v, want ErrIdentityBrokerUnavailable", err)
	}
	if n := llm.Calls(); n != 0 {
		t.Fatalf("the agent ran unregistered: %d model requests", n)
	}
	broker.set(func(b *recordingBroker) { b.registerUnavailable = false })
	if err := runToEnd(ctx, t, svc, sess.ID); err != nil {
		t.Fatalf("run after registration recovered: %v", err)
	}
	wantRegistered(t, broker, wantBrokerSession(t, broker, sess), sess)
}

func TestIdentityBrokerReopensOnceWhenRegistrationFindsTheSessionClosed(t *testing.T) {
	broker := newRecordingBroker()
	svc := newIdentityBrokerService(t, broker, mockllm.New())
	closed := false
	broker.set(func(b *recordingBroker) {
		b.beforeRegister = func(req identitybroker.RegisterAgentRequest) {
			if !closed {
				closed = true
				_ = b.inner.CloseSession(t.Context(), identitybroker.CloseSessionRequest{SessionID: req.SessionID, Reason: identitybroker.ReasonSessionClosed})
			}
		}
	})
	sess, err := svc.CreateSession(aliceContext(t), session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	reopened := wantBrokerSession(t, broker, sess)
	wantRegistered(t, broker, reopened, sess)
	if n := broker.openCount(); n != 2 {
		t.Fatalf("broker sessions opened = %d, want 2", n)
	}
}

func TestIdentityBrokerRefusesAnotherAgent(t *testing.T) {
	broker := newRecordingBroker()
	llm := mockllm.New(mockllm.TextTurn("must not run"))
	svc := newIdentityBrokerService(t, broker, llm)
	ctx := aliceContext(t)
	broker.set(func(b *recordingBroker) { b.otherAgent = true })
	sess, err := svc.CreateSession(ctx, session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := runToEnd(ctx, t, svc, sess.ID); !errors.Is(err, server.ErrInternal) {
		t.Fatalf("run when the broker returns another agent: %v, want ErrInternal", err)
	}
	if n := llm.Calls(); n != 0 {
		t.Fatalf("the agent ran with another agent's credential: %d model requests", n)
	}
}

func TestIdentityBrokerRefusesAnotherAgentOnRenewal(t *testing.T) {
	// The renewed agent's ID still ends in the incarnation, so only the
	// comparison with the agent held before catches it.
	broker := newRecordingBroker()
	llm := mockllm.New(mockllm.TextTurn("must not run"))
	svc := newIdentityBrokerService(t, broker, llm)
	ctx := aliceContext(t)
	sess, err := svc.CreateSession(ctx, session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	first := wantBrokerSession(t, broker, sess)
	// A reopen makes run entry register again.
	if err := broker.inner.CloseSession(t.Context(), identitybroker.CloseSessionRequest{SessionID: first.ID, Reason: identitybroker.ReasonSessionClosed}); err != nil {
		t.Fatal(err)
	}
	broker.set(func(b *recordingBroker) { b.renamedAgent = true })
	if err := runToEnd(ctx, t, svc, sess.ID); !errors.Is(err, server.ErrInternal) {
		t.Fatalf("run when a renewal returns another agent: %v, want ErrInternal", err)
	}
	if n := llm.Calls(); n != 0 {
		t.Fatalf("the agent ran with another agent's credential: %d model requests", n)
	}
}

func TestIdentityBrokerSessionClosedTwiceDuringRegistrationIsRetryable(t *testing.T) {
	broker := newRecordingBroker()
	llm := mockllm.New(mockllm.TextTurn("must not run"))
	svc := newIdentityBrokerService(t, broker, llm)
	ctx := aliceContext(t)
	sess, err := svc.CreateSession(ctx, session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	first := wantBrokerSession(t, broker, sess)
	if err := broker.inner.CloseSession(t.Context(), identitybroker.CloseSessionRequest{SessionID: first.ID, Reason: identitybroker.ReasonSessionClosed}); err != nil {
		t.Fatal(err)
	}
	// Every registration finds its broker session closed.
	broker.set(func(b *recordingBroker) {
		b.beforeRegister = func(req identitybroker.RegisterAgentRequest) {
			_ = b.inner.CloseSession(t.Context(), identitybroker.CloseSessionRequest{SessionID: req.SessionID, Reason: identitybroker.ReasonSessionClosed})
		}
	})
	if err := runToEnd(ctx, t, svc, sess.ID); !errors.Is(err, server.ErrIdentityBrokerUnavailable) {
		t.Fatalf("run when the broker session keeps closing: %v, want ErrIdentityBrokerUnavailable", err)
	}
	if n := llm.Calls(); n != 0 {
		t.Fatalf("the agent ran unregistered: %d model requests", n)
	}
}

func TestIdentityBrokerSuccessorsRegisterTheirOwnAgents(t *testing.T) {
	broker := newRecordingBroker()
	svc := newIdentityBrokerService(t, broker, mockllm.New())
	ctx := aliceContext(t)
	source, err := svc.CreateSession(ctx, session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	forked, err := svc.ForkSessionSuccessor(ctx, server.ForkSuccessorRequest{Source: source.ID})
	if err != nil {
		t.Fatal(err)
	}
	cleared, err := svc.ClearSessionSuccessor(ctx, source.ID, server.SuccessorPlacement{})
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]session.SessionID{}
	for _, id := range []session.SessionID{source.ID, forked, cleared} {
		sess, err := svc.GetSession(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		brokerSession := wantBrokerSession(t, broker, sess)
		var found bool
		for _, req := range broker.registerRequests() {
			if req.SessionID == brokerSession.ID && req.Incarnation == string(sess.Incarnation()) {
				found = true
			}
		}
		if !found {
			t.Fatalf("session %s registered no agent in its broker session", id)
		}
		if other, dup := seen[string(sess.Incarnation())]; dup {
			t.Fatalf("sessions %s and %s share incarnation %s", other, id, sess.Incarnation())
		}
		seen[string(sess.Incarnation())] = id
	}
	if !strings.HasPrefix(string(source.Incarnation()), "inc_") {
		t.Fatalf("source incarnation %q is not minted", source.Incarnation())
	}
}
