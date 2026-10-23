package server_test

import (
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/sessnap"
	"github.com/stacklok/mecatl/engine/port"
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
		// Only accepted registrations count: successor registration is best
		// effort, so one refused with ALREADY_EXISTS would still be attempted.
		var found bool
		for _, req := range broker.acceptedRegistrations() {
			if req.SessionID == brokerSession.ID && req.HostSessionID == string(id) && req.Incarnation == string(sess.Incarnation()) {
				found = true
			}
		}
		if !found {
			t.Fatalf("session %s has no accepted registration in its broker session", id)
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

func TestIdentityBrokerForkAndClearStayBestEffort(t *testing.T) {
	broker := newRecordingBroker()
	llm := mockllm.New(mockllm.TextTurn("forked"), mockllm.TextTurn("cleared"))
	svc := newIdentityBrokerService(t, broker, llm)
	ctx := aliceContext(t)
	source, err := svc.CreateSession(ctx, session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	broker.set(func(b *recordingBroker) { b.registerUnavailable = true })
	forked, err := svc.ForkSessionSuccessor(ctx, server.ForkSuccessorRequest{Source: source.ID})
	if err != nil {
		t.Fatalf("fork with registration down: %v", err)
	}
	cleared, err := svc.ClearSessionSuccessor(ctx, source.ID, server.SuccessorPlacement{})
	if err != nil {
		t.Fatalf("clear with registration down: %v", err)
	}
	if n := len(broker.acceptedRegistrations()); n != 1 {
		t.Fatalf("accepted registrations with registration down = %d, want only the source's", n)
	}
	// Once the broker recovers, each successor's first run registers its agent.
	broker.set(func(b *recordingBroker) { b.registerUnavailable = false })
	for _, id := range []session.SessionID{forked, cleared} {
		if err := runToEnd(ctx, t, svc, id); err != nil {
			t.Fatalf("first run of successor %s: %v", id, err)
		}
		successor, err := svc.GetSession(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		wantRegistered(t, broker, wantBrokerSession(t, broker, successor), successor)
	}
}

func TestIdentityBrokerPermanentRegistrationErrorsAreNotRetryable(t *testing.T) {
	for _, code := range []identitybroker.Code{identitybroker.CodeAlreadyExists, identitybroker.CodePermissionDenied, identitybroker.CodeInvalidArgument} {
		t.Run(string(code), func(t *testing.T) {
			broker := newRecordingBroker()
			llm := mockllm.New(mockllm.TextTurn("must not run"))
			svc := newIdentityBrokerService(t, broker, llm)
			ctx := aliceContext(t)
			broker.set(func(b *recordingBroker) { b.registerErr = identitybroker.NewError(code, "", "test broker refuses") })
			sess, err := svc.CreateSession(ctx, session.ModeDefault, session.Limits{})
			if err != nil {
				t.Fatalf("create with registration refused: %v", err)
			}
			err = runToEnd(ctx, t, svc, sess.ID)
			if !errors.Is(err, server.ErrInternal) || errors.Is(err, server.ErrIdentityBrokerUnavailable) {
				t.Fatalf("run with registration refused with %s: %v, want ErrInternal", code, err)
			}
			if n := llm.Calls(); n != 0 {
				t.Fatalf("the agent ran unregistered: %d model requests", n)
			}
		})
	}
}

func TestIdentityBrokerRefusesAnExpiredCredential(t *testing.T) {
	broker := newRecordingBroker()
	llm := mockllm.New(mockllm.TextTurn("must not run"))
	svc := newIdentityBrokerService(t, broker, llm) // the server's clock reads time.Unix(0, 0)
	ctx := aliceContext(t)
	broker.set(func(b *recordingBroker) {
		b.editRegistration = func(reg *identitybroker.Registration) { reg.ExpiresAt = time.Unix(0, 0) }
	})
	sess, err := svc.CreateSession(ctx, session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if _, held := svc.HeldIdentityCredentialForTest(sess.ID); held {
		t.Fatal("an expired credential is held")
	}
	if err := runToEnd(ctx, t, svc, sess.ID); !errors.Is(err, server.ErrInternal) {
		t.Fatalf("run when the broker returns an expired credential: %v, want ErrInternal", err)
	}
	if n := llm.Calls(); n != 0 {
		t.Fatalf("the agent ran with an expired credential: %d model requests", n)
	}
}

func TestIdentityBrokerRegistrationAtCreateIsBounded(t *testing.T) {
	restore := server.SetEngineCloseTimeoutForTest(50 * time.Millisecond)
	defer restore()
	broker := &hangingFindBroker{recordingBroker: newRecordingBroker(), hang: true}
	svc := newIdentityBrokerService(t, broker, mockllm.New())
	done := make(chan error, 1)
	go func() {
		_, err := svc.CreateSession(aliceContext(t), session.ModeDefault, session.Limits{})
		done <- err
	}()
	select {
	case err := <-done:
		// Registration is best effort at create: the failure is logged.
		if err != nil {
			t.Fatalf("create with a hung identity broker: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("create waited on a hung identity broker")
	}
}

func TestIdentityBrokerCredentialStaysInMemory(t *testing.T) {
	broker := newRecordingBroker() // real broker: the credential is a real JWT
	var mu sync.Mutex
	var requests []string
	observe := func(req port.LLMRequest) {
		body, err := json.Marshal(req)
		if err != nil {
			t.Errorf("marshal model request: %v", err)
		}
		mu.Lock()
		requests = append(requests, req.System.Render()+string(body))
		mu.Unlock()
	}
	llm := mockllm.NewWith([]mockllm.Option{mockllm.WithRequestObserver(observe)}, mockllm.TextTurn("done"))
	var store *memstore.Store
	svc := newIdentityBrokerService(t, broker, llm, func(cfg *server.Config) { store = cfg.Store.(*memstore.Store) })
	ctx := aliceContext(t)
	sess, err := svc.CreateSession(ctx, session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	// Releasing the session drops the credential from create, so run entry
	// registers on the session the run then persists and sends to the model.
	svc.CloseSession(sess.ID)
	run, err := svc.StartRun(ctx, sess.ID, "hello")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	for range run.Events() {
	}
	svc.FinishRun(sess.ID, run)
	credential, held := svc.HeldIdentityCredentialForTest(sess.ID)
	if !held || strings.Count(credential, ".") != 2 {
		t.Fatalf("held credential = %q, %v; want a JWT", credential, held)
	}
	loaded, err := store.Load(ctx, sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	persisted, err := sessnap.Marshal(loaded)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(persisted), credential) {
		t.Fatal("the persisted session contains the agent's credential")
	}
	mu.Lock()
	seen := append([]string(nil), requests...)
	mu.Unlock()
	if len(seen) == 0 {
		t.Fatal("the model saw no request")
	}
	for _, req := range seen {
		// Positive control: the observed text carries the conversation.
		if !strings.Contains(req, "hello") {
			t.Fatalf("model request does not carry the prompt: %s", req)
		}
		if strings.Contains(req, credential) {
			t.Fatal("the model's input contains the agent's credential")
		}
	}

	if err := svc.DeleteSession(ctx, sess.ID); err != nil {
		t.Fatal(err)
	}
	if _, held := svc.HeldIdentityCredentialForTest(sess.ID); held {
		t.Fatal("a deleted session kept its agent's credential")
	}

	open, err := svc.CreateSession(ctx, session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if _, held := svc.HeldIdentityCredentialForTest(open.ID); !held {
		t.Fatal("no credential is held after create")
	}
	svc.Close()
	if _, held := svc.HeldIdentityCredentialForTest(open.ID); held {
		t.Fatal("a closed server kept an agent's credential")
	}
}

// TestIdentityBrokerHeldCredentialVerifiesForItsSession is the slice's
// testable result: a real session's agent holds a credential that the broker
// verifies for the session's broker session and maps to the shared ag_
// identifier, as AS-1's in-process check will.
func TestIdentityBrokerHeldCredentialVerifiesForItsSession(t *testing.T) {
	broker := newRecordingBroker()
	svc := newIdentityBrokerService(t, broker, mockllm.New(mockllm.TextTurn("done")))
	ctx := aliceContext(t)
	sess, err := svc.CreateSession(ctx, session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if err := runToEnd(ctx, t, svc, sess.ID); err != nil {
		t.Fatalf("run: %v", err)
	}
	credential, held := svc.HeldIdentityCredentialForTest(sess.ID)
	if !held {
		t.Fatal("the session's agent holds no credential")
	}
	brokerSession := wantBrokerSession(t, broker, sess)
	verify := func(sessionID string) (identitybroker.VerifiedAgent, error) {
		return broker.verifier.VerifyCredential(t.Context(), identitybroker.VerifyCredentialRequest{SessionID: sessionID, Credential: identitybroker.NewToken(credential)})
	}
	got, err := verify(brokerSession.ID)
	if err != nil {
		t.Fatalf("VerifyCredential of the held credential: %v", err)
	}
	if !strings.HasSuffix(got.Agent.Subject, "/inst/"+string(sess.Incarnation())) {
		t.Fatalf("verified agent = %q, want the session's agent", got.Agent.Subject)
	}
	if !strings.HasPrefix(got.AgentID, "ag_") {
		t.Fatalf("verified ag_ identifier = %q", got.AgentID)
	}
	if !got.ExpiresAt.After(time.Now()) {
		t.Fatalf("verified credential expires at %v, want a future time", got.ExpiresAt)
	}
	// Negative control: the credential verifies only for its own broker
	// session, not another session's.
	other, err := svc.CreateSession(ctx, session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := verify(wantBrokerSession(t, broker, other).ID); err == nil {
		t.Fatal("the held credential verified for another session's broker session")
	}
}
