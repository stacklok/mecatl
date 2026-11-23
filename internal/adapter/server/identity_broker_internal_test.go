package server

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/agentcredential"
	"github.com/stacklok/mecatl/internal/identitybroker"
	"github.com/stacklok/mecatl/internal/identitybroker/conformance"
)

func TestIdentityBrokerOpensOnlyForRootSessions(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	core, err := identitybroker.New(identitybroker.Config{
		TrustDomain: "agents.example.com", Audience: "https://broker.example.com", CredentialLifetime: 5 * time.Minute,
	}, conformance.Definitions(), key)
	if err != nil {
		t.Fatal(err)
	}
	broker := identitybroker.NewInProcess(identitybroker.UnverifiedAuthenticator{Name: "mecak8s-test"}, core)
	svc, err := NewService(Config{
		Engine:            brokerEngineResult().Engine,
		Store:             memstore.New(),
		PlacementProvider: brokerPlacementProvider{},
		PlacementScope:    "test",
		IdentityBroker:    broker, IdentityDefinitions: conformance.DefinitionRefs(),
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	t.Cleanup(svc.Close)

	ref := session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"}
	owner := &session.Principal{Issuer: "https://login.example.com", Subject: "alice-204", GrantType: session.GrantTypeUser}
	at := time.Unix(1, 0)
	root := session.New("root", session.ModeDefault, ref, session.Limits{}, at)
	children := map[string]func() (*session.Session, error){
		"subagent": func() (*session.Session, error) {
			return session.NewSubagent("child-subagent", session.ModeDefault, ref, session.Limits{}, at, root.ID, root.Incarnation(), "call-1")
		},
		"parallel branch": func() (*session.Session, error) {
			return session.NewParallelBranch("child-branch", session.ModeDefault, ref, session.Limits{}, at, root.ID, root.Incarnation(), "call-2", 0)
		},
		"team member": func() (*session.Session, error) {
			return session.NewTeamMember("child-member", session.ModeDefault, ref, session.Limits{}, at, "team-1", "lead", root.ID, root.Incarnation())
		},
	}
	for name, build := range children {
		child, err := build()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if err := child.RestoreLabels(owner, session.Authority{}); err != nil {
			t.Fatal(err)
		}
		if err := svc.openIdentityBrokerSession(t.Context(), child); err != nil {
			t.Fatalf("%s: openIdentityBrokerSession: %v", name, err)
		}
		if _, ok, err := broker.FindSession(t.Context(), identitybroker.FindSessionRequest{RootHostSessionID: string(child.ID)}); err != nil || ok {
			t.Fatalf("%s got a broker session of its own (found=%v, err=%v); children belong to their root's tree", name, ok, err)
		}
	}

	// Positive control: the same owner's root session does get one.
	if err := root.RestoreLabels(owner, session.Authority{}); err != nil {
		t.Fatal(err)
	}
	if err := svc.openIdentityBrokerSession(t.Context(), root); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := broker.FindSession(t.Context(), identitybroker.FindSessionRequest{RootHostSessionID: string(root.ID)}); err != nil || !ok {
		t.Fatalf("the root session got no broker session (found=%v, err=%v)", ok, err)
	}
}

func TestIdentityAgentDefinition(t *testing.T) {
	ref := session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"}
	main := identitybroker.DefinitionRef{Source: identitybroker.SourceSystem, Name: identitybroker.BuiltinMain, Revision: "v1.2.3"}
	reviewer := identitybroker.DefinitionRef{Source: identitybroker.SourceGlobal, Name: "reviewer", Revision: "r7"}
	loaded := []identitybroker.DefinitionRef{
		{Source: identitybroker.SourceSystem, Name: identitybroker.BuiltinExplorer, Revision: "v1.2.3"},
		main,
		reviewer,
	}
	cases := []struct {
		name       string
		def        string
		loaded     []identitybroker.DefinitionRef
		want       identitybroker.DefinitionRef
		wantLoaded bool
	}{
		{name: "an ordinary session runs the loaded built-in main", loaded: loaded, want: main, wantLoaded: true},
		{name: "a named session runs the loaded global definition", def: "reviewer", loaded: loaded, want: reviewer, wantLoaded: true},
		{name: "a name the broker did not load is sent by name only", def: "planner", loaded: loaded, want: identitybroker.DefinitionRef{Name: "planner"}},
		{name: "a name is never matched to a built-in", def: identitybroker.BuiltinExplorer, loaded: loaded, want: identitybroker.DefinitionRef{Name: identitybroker.BuiltinExplorer}},
		{name: "with nothing loaded the built-in main has no revision", want: identitybroker.DefinitionRef{Source: identitybroker.SourceSystem, Name: identitybroker.BuiltinMain}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sess := session.New("s", session.ModeDefault, ref, session.Limits{}, time.Unix(1, 0))
			sess.AgentDefinitionName = tc.def
			if got, ok := identityAgentDefinition(tc.loaded, sess); got != tc.want || ok != tc.wantLoaded {
				t.Fatalf("definition = %+v, %v; want %+v, %v", got, ok, tc.want, tc.wantLoaded)
			}
		})
	}
}

func TestAnUnloadedDefinitionIsLoggedAndRefused(t *testing.T) {
	const logged = "identity broker loaded no definition the session's agent runs; the broker refuses its registration"
	diag := &attemptRecordingDiagnostics{}
	svc, sess := newRegisteringService(t, func(cfg *Config) { cfg.Diagnostics = diag })
	// Positive control: the built-in main is loaded, registers, and logs nothing.
	if err := svc.openIdentityBrokerSession(t.Context(), sess); err != nil {
		t.Fatalf("registration of the built-in main: %v", err)
	}
	if slices.Contains(diag.entries, logged) {
		t.Fatalf("a loaded definition was logged as unloaded: %q", diag.entries)
	}
	ref := session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"}
	named := session.New("named", session.ModeDefault, ref, session.Limits{}, time.Unix(1, 0))
	named.AgentDefinitionName = "planner"
	if err := named.RestoreLabels(&session.Principal{Issuer: "https://login.example.com", Subject: "alice-204", GrantType: session.GrantTypeUser}, session.Authority{}); err != nil {
		t.Fatal(err)
	}
	if err := svc.openIdentityBrokerSession(t.Context(), named); err == nil {
		t.Fatal("a session running a definition the broker did not load registered its agent")
	}
	if !slices.Contains(diag.entries, logged) {
		t.Fatalf("the unloaded definition was not logged: %q", diag.entries)
	}
}

// newRegisteringService returns a Service with the real identity broker, and
// a saved-looking root session with an owner. configure, if given, adjusts the
// Config before the Service is built.
func newRegisteringService(t *testing.T, configure ...func(*Config)) (*Service, *session.Session) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	core, err := identitybroker.New(identitybroker.Config{
		TrustDomain: "agents.example.com", Audience: "https://broker.example.com", CredentialLifetime: 5 * time.Minute,
	}, conformance.Definitions(), key)
	if err != nil {
		t.Fatal(err)
	}
	cfg := Config{
		Engine:            brokerEngineResult().Engine,
		Store:             memstore.New(),
		PlacementProvider: brokerPlacementProvider{},
		PlacementScope:    "test",
		IdentityBroker:    identitybroker.NewInProcess(identitybroker.UnverifiedAuthenticator{Name: "mecak8s-test"}, core), IdentityDefinitions: conformance.DefinitionRefs(),
	}
	for _, c := range configure {
		c(&cfg)
	}
	svc, err := NewService(cfg)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	t.Cleanup(svc.Close)
	ref := session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"}
	sess := session.New("root", session.ModeDefault, ref, session.Limits{}, time.Unix(1, 0))
	if err := sess.RestoreLabels(&session.Principal{Issuer: "https://login.example.com", Subject: "alice-204", GrantType: session.GrantTypeUser}, session.Authority{}); err != nil {
		t.Fatal(err)
	}
	return svc, sess
}

func TestACredentialHeldForAnotherIncarnationIsReplaced(t *testing.T) {
	broker := &hookedBroker{}
	svc, sess := newRegisteringService(t, withHookedBroker(broker))
	// Left from an earlier incarnation of this session ID, as a session
	// recreated elsewhere would leave it.
	staleIncarnation := string(session.NewIncarnationID())
	stale := identitybroker.Registration{
		Agent:      identitybroker.AgentRef{Subject: "spiffe://agents.example.com/agent/ag_aaaaaaaaaaaaaaaaaaaaaaaaaa"},
		Credential: unsignedTestCredential("spiffe://agents.example.com/agent/ag_aaaaaaaaaaaaaaaaaaaaaaaaaa"),
		ExpiresAt:  time.Now().Add(time.Hour),
	}
	broker.register = func(identitybroker.RegisterAgentRequest) (identitybroker.Registration, error) { return stale, nil }
	key := agentcredential.Key{RootSessionID: string(sess.ID), AgentSessionID: string(sess.ID)}
	if err := svc.cfg.IdentityAgents.Ensure(t.Context(), key, identitybroker.RegisterAgentRequest{
		SessionID: "bs_stale", HostSessionID: string(sess.ID), Incarnation: staleIncarnation,
	}, func() error { return nil }); err != nil {
		t.Fatalf("plant the stale credential: %v", err)
	}
	broker.register = nil
	if err := svc.openIdentityBrokerSession(t.Context(), sess); err != nil {
		t.Fatalf("openIdentityBrokerSession with a stale credential held: %v", err)
	}
	got, _ := heldIdentityAgent(svc, sess.ID)
	if identitybroker.CheckRegistration(identitybroker.Registration{Agent: got.Agent, Credential: got.Token}) != nil || got.Agent == stale.Agent {
		t.Fatalf("held credential's agent = %q, want the current incarnation's agent", got.Agent.Subject)
	}
}

// heldIdentityAgent returns the credential held for session id's main agent.
func heldIdentityAgent(svc *Service, id session.SessionID) (agentcredential.Credential, bool) {
	return svc.cfg.IdentityAgents.Held(agentcredential.Key{RootSessionID: string(id), AgentSessionID: string(id)})
}

func TestClosingASessionDropsItsHeldCredential(t *testing.T) {
	svc, sess := newRegisteringService(t)
	if err := svc.openIdentityBrokerSession(t.Context(), sess); err != nil {
		t.Fatal(err)
	}
	held := func() bool {
		_, ok := heldIdentityAgent(svc, sess.ID)
		return ok
	}
	if !held() {
		t.Fatal("no credential is held after registration")
	}
	svc.CloseSession(sess.ID)
	if held() {
		t.Fatal("a closed session kept its agent's credential")
	}
}

func TestEndingAScheduledRunDropsItsHeldCredential(t *testing.T) {
	svc, sess := newRegisteringService(t)
	sess.Kind = session.SessionKindScheduled
	if err := svc.openIdentityBrokerSession(t.Context(), sess); err != nil {
		t.Fatal(err)
	}
	svc.EndScheduledRun(t.Context(), sess)
	_, ok := heldIdentityAgent(svc, sess.ID)
	if ok {
		t.Fatal("an ended scheduled run kept its agent's credential")
	}
}

func TestTeamRootRegistrationWaitsForRunEntry(t *testing.T) {
	svc, sess := newRegisteringService(t)
	if err := svc.cfg.Store.Save(t.Context(), sess); err != nil {
		t.Fatal(err)
	}
	unlock := svc.runEntryMu.lock(sess.ID)
	done := make(chan error, 1)
	go func() { _, err := svc.startTeamRootRun(t.Context(), sess.ID); done <- err }()
	select {
	case err := <-done:
		unlock()
		t.Fatalf("team root registered while run entry held the session: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	unlock()
	if err := <-done; err != nil {
		t.Fatalf("team root registration: %v", err)
	}
}

func TestTeamRootRegistrationForADeletedRootIsNotFound(t *testing.T) {
	svc, sess := newRegisteringService(t)
	// The root is gone from the store, as a delete that won run entry's lock
	// leaves it: the team run is refused as not found, not as a server fault.
	_, err := svc.startTeamRootRun(t.Context(), sess.ID)
	if !errors.Is(err, ErrNotFound) || errors.Is(err, ErrInternal) {
		t.Fatalf("team root registration for a deleted root = %v, want ErrNotFound", err)
	}
}

func TestTeamRootRegistrationReportsAStoreFailureAsInternal(t *testing.T) {
	store := &oneSessionFailsStore{Store: memstore.New()}
	svc, sess := newRegisteringService(t, func(cfg *Config) { cfg.Store = store })
	store.failID = sess.ID
	_, err := svc.startTeamRootRun(t.Context(), sess.ID)
	if !errors.Is(err, ErrInternal) || errors.Is(err, ErrNotFound) {
		t.Fatalf("team root registration with a failing store = %v, want ErrInternal", err)
	}
}

// hookedBroker wraps a broker: beforeRegister runs before each RegisterAgent,
// and stallFind makes FindSession wait for its context, as a hung remote
// broker would.
type hookedBroker struct {
	identitybroker.Service
	beforeRegister func()
	// register, if set, answers RegisterAgent instead of the broker.
	register  func(identitybroker.RegisterAgentRequest) (identitybroker.Registration, error)
	stallFind bool
}

func (b *hookedBroker) RegisterAgent(ctx context.Context, req identitybroker.RegisterAgentRequest) (identitybroker.Registration, error) {
	if b.beforeRegister != nil {
		b.beforeRegister()
	}
	if b.register != nil {
		return b.register(req)
	}
	return b.Service.RegisterAgent(ctx, req)
}

func (b *hookedBroker) FindSession(ctx context.Context, req identitybroker.FindSessionRequest) (identitybroker.Session, bool, error) {
	if b.stallFind {
		<-ctx.Done()
		return identitybroker.Session{}, false, ctx.Err()
	}
	return b.Service.FindSession(ctx, req)
}

func withHookedBroker(b *hookedBroker) func(*Config) {
	return func(cfg *Config) {
		b.Service = cfg.IdentityBroker
		cfg.IdentityBroker = b
	}
}

func TestSavedRegistrationWaitsForRunEntry(t *testing.T) {
	// Create, fork, and clear register through openSavedIdentityBrokerSession.
	svc, sess := newRegisteringService(t)
	if err := svc.cfg.Store.Save(t.Context(), sess); err != nil {
		t.Fatal(err)
	}
	unlock := svc.runEntryMu.lock(sess.ID)
	done := make(chan error, 1)
	go func() { done <- svc.openSavedIdentityBrokerSession(t.Context(), sess.ID) }()
	select {
	case err := <-done:
		unlock()
		t.Fatalf("create, fork, or clear registered while run entry held the session: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	unlock()
	if err := <-done; err != nil {
		t.Fatalf("registration: %v", err)
	}
}

const savedRegistrationLoadFailed = "identity broker registration could not load the session"

func TestSavedRegistrationForADeletedSessionIsSilent(t *testing.T) {
	diag := &learnedSkillDiagnostics{}
	svc, sess := newRegisteringService(t, func(cfg *Config) { cfg.Diagnostics = diag })
	// A session that is gone from the store, as a delete that won run entry's
	// lock leaves it: an expected race, not an operator fact.
	if err := svc.openSavedIdentityBrokerSession(t.Context(), sess.ID); !errors.Is(err, port.ErrSessionNotFound) {
		t.Fatalf("registration for a deleted session = %v, want ErrSessionNotFound", err)
	}
	for _, r := range diag.records {
		if r.message == savedRegistrationLoadFailed {
			t.Fatalf("a deleted session was logged as a load failure: %+v", r)
		}
	}
}

func TestSavedRegistrationLogsAStoreFailure(t *testing.T) {
	diag := &learnedSkillDiagnostics{}
	store := &oneSessionFailsStore{Store: memstore.New()}
	svc, sess := newRegisteringService(t, func(cfg *Config) {
		cfg.Diagnostics = diag
		cfg.Store = store
	})
	store.failID = sess.ID
	if err := svc.openSavedIdentityBrokerSession(t.Context(), sess.ID); err == nil || err.Error() != "simulated transient load failure" {
		t.Fatalf("registration with a failing store = %v, want the store's error unchanged", err)
	}
	var warned []learnedSkillDiagnostic
	for _, r := range diag.records {
		if r.message == savedRegistrationLoadFailed {
			warned = append(warned, r)
		}
	}
	if len(warned) != 1 || warned[0].level != port.LevelWarn {
		t.Fatalf("store failure records = %+v, want exactly one warning", warned)
	}
	fields := warned[0].fields
	i := slices.Index(fields, any("err"))
	if i < 0 || i+1 >= len(fields) || !strings.Contains(fmt.Sprint(fields[i+1]), "simulated transient load failure") {
		t.Fatalf("store failure fields = %v, want err carrying the store's error", fields)
	}
}

func TestSavedRegistrationLogsWhatItsCallersDiscard(t *testing.T) {
	diag := &attemptRecordingDiagnostics{}
	svc, sess := newRegisteringService(t, func(cfg *Config) { cfg.Diagnostics = diag })
	if err := svc.cfg.Store.Save(t.Context(), sess); err != nil {
		t.Fatal(err)
	}
	svc.mu.Lock()
	svc.lostOwnership[sess.ID] = struct{}{}
	svc.mu.Unlock()
	if err := svc.openSavedIdentityBrokerSession(t.Context(), sess.ID); !errors.Is(err, ErrSessionLeasedElsewhere) {
		t.Fatalf("registration after a lost lease = %v, want ErrSessionLeasedElsewhere", err)
	}
	if !slices.Contains(diag.entries, "identity broker registration refused: the session's lease was lost or the server is closing") {
		t.Fatalf("a lost-lease refusal was not logged: %q", diag.entries)
	}
}

func TestARegistrationFinishingAfterCloseStoresNothing(t *testing.T) {
	broker := &hookedBroker{}
	svc, sess := newRegisteringService(t, withHookedBroker(broker))
	// The server shuts down while the broker call is in flight.
	broker.beforeRegister = svc.Close
	if err := svc.openIdentityBrokerSession(t.Context(), sess); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("registration finishing after Close = %v, want ErrUnavailable", err)
	}
	_, held := heldIdentityAgent(svc, sess.ID)
	if held {
		t.Fatal("a registration that finished after Close stored its credential")
	}
}

func TestARegistrationDroppedInFlightIsUnavailable(t *testing.T) {
	diag := &attemptRecordingDiagnostics{}
	broker := &hookedBroker{}
	svc, sess := newRegisteringService(t, func(cfg *Config) { cfg.Diagnostics = diag }, withHookedBroker(broker))
	// The session's credentials are dropped while the broker call is in
	// flight, with no lease loss or Close, as a delete in flight would.
	broker.beforeRegister = func() { svc.cfg.IdentityAgents.Drop(string(sess.ID)) }
	if err := svc.openIdentityBrokerSession(t.Context(), sess); !errors.Is(err, ErrIdentityBrokerUnavailable) {
		t.Fatalf("registration dropped in flight = %v, want ErrIdentityBrokerUnavailable", err)
	}
	if _, held := heldIdentityAgent(svc, sess.ID); held {
		t.Fatal("a registration dropped in flight stored its credential")
	}
	if !slices.Contains(diag.entries, "agent credential dropped during registration") {
		t.Fatalf("the drop was not logged: %q", diag.entries)
	}
}

func TestTeamRootRegistrationIsBounded(t *testing.T) {
	prev := engineCloseTimeout
	engineCloseTimeout = 50 * time.Millisecond
	t.Cleanup(func() { engineCloseTimeout = prev })
	broker := &hookedBroker{stallFind: true}
	svc, sess := newRegisteringService(t, withHookedBroker(broker))
	if err := svc.cfg.Store.Save(t.Context(), sess); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := svc.startTeamRootRun(t.Context(), sess.ID); done <- err }()
	select {
	case err := <-done:
		if !errors.Is(err, ErrIdentityBrokerUnavailable) {
			t.Fatalf("team root registration with a hung broker = %v, want ErrIdentityBrokerUnavailable", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("team root registration waited on a hung broker")
	}
}

func TestALostLeaseKeepsNoCredential(t *testing.T) {
	svc, sess := newRegisteringService(t)
	// The lease is lost while registration is in flight: the tombstone is
	// already set when the credential would be recorded.
	svc.mu.Lock()
	svc.lostOwnership[sess.ID] = struct{}{}
	svc.mu.Unlock()
	// The run must not start without a credential: registration refuses it.
	if err := svc.openIdentityBrokerSession(t.Context(), sess); !errors.Is(err, ErrSessionLeasedElsewhere) {
		t.Fatalf("registration after a lost lease = %v, want ErrSessionLeasedElsewhere", err)
	}
	_, held := heldIdentityAgent(svc, sess.ID)
	if held {
		t.Fatal("a session whose lease was lost holds its agent's credential")
	}
}

func TestLosingTheLeaseDropsTheHeldCredential(t *testing.T) {
	svc, sess := newRegisteringService(t, func(cfg *Config) {
		// Every renew reports the lease taken, so the renewer declares the
		// loss through onLeaseLost on its first tick.
		cfg.SessionLease = immediateLeaseLoss{}
		cfg.LeaseOwner = "test"
		cfg.LeaseTTL = time.Hour
		cfg.LeaseRenewInterval = time.Millisecond
	})
	if err := svc.openIdentityBrokerSession(t.Context(), sess); err != nil {
		t.Fatal(err)
	}
	_, held := heldIdentityAgent(svc, sess.ID)
	if !held {
		t.Fatal("no credential is held after registration")
	}
	if err := svc.acquireLease(t.Context(), sess.ID); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		svc.mu.Lock()
		_, lost := svc.lostOwnership[sess.ID]
		_, held = heldIdentityAgent(svc, sess.ID)
		svc.mu.Unlock()
		if lost {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the lease loss was never declared")
		}
		time.Sleep(time.Millisecond)
	}
	if held {
		t.Fatal("a session whose lease was lost kept its agent's credential")
	}
}

func TestADeletedSessionGetsNoBrokerSession(t *testing.T) {
	svc, sess := newRegisteringService(t)
	// A delete that won the lock before create's registration: the session is
	// gone from the store.
	if err := svc.openSavedIdentityBrokerSession(t.Context(), sess.ID); err == nil {
		t.Fatal("opening a broker session for an unsaved session succeeded")
	}
	if _, found, err := svc.cfg.IdentityBroker.FindSession(t.Context(), identitybroker.FindSessionRequest{RootHostSessionID: string(sess.ID)}); err != nil || found {
		t.Fatalf("a deleted session got a broker session (found=%v, err=%v)", found, err)
	}
	// Positive control: once saved, the same call opens one.
	if err := svc.cfg.Store.Save(t.Context(), sess); err != nil {
		t.Fatal(err)
	}
	if err := svc.openSavedIdentityBrokerSession(t.Context(), sess.ID); err != nil {
		t.Fatal(err)
	}
	if _, found, err := svc.cfg.IdentityBroker.FindSession(t.Context(), identitybroker.FindSessionRequest{RootHostSessionID: string(sess.ID)}); err != nil || !found {
		t.Fatalf("a saved session got no broker session (found=%v, err=%v)", found, err)
	}
}

// unsignedTestCredential returns an unsigned JWT naming subject: a
// credential the holder accepts as naming that agent, which no broker issued.
func unsignedTestCredential(subject string) identitybroker.Token {
	enc := base64.RawURLEncoding
	claims, _ := json.Marshal(map[string]string{"sub": subject})
	return identitybroker.NewToken(enc.EncodeToString([]byte(`{"alg":"ES256"}`)) + "." + enc.EncodeToString(claims) + ".sig")
}

// TestNewServiceRequiresIdentityDefinitionsWithABroker pins that a service
// with an identity broker and no definitions is refused at construction:
// otherwise every agent would register by name only and every run fail.
func TestNewServiceRequiresIdentityDefinitionsWithABroker(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	core, err := identitybroker.New(identitybroker.Config{
		TrustDomain: "agents.example.com", Audience: "https://broker.example.com", CredentialLifetime: 5 * time.Minute,
	}, conformance.Definitions(), key)
	if err != nil {
		t.Fatal(err)
	}
	cfg := Config{
		Engine: brokerEngineResult().Engine, Store: memstore.New(),
		PlacementProvider: brokerPlacementProvider{}, PlacementScope: "test",
		IdentityBroker: identitybroker.NewInProcess(identitybroker.UnverifiedAuthenticator{Name: "mecak8s-test"}, core),
	}
	if _, err := NewService(cfg); !errors.Is(err, ErrConfig) {
		t.Fatalf("NewService with a broker and no definitions = %v, want ErrConfig", err)
	}
	// Positive control: the same config with the broker's definitions builds.
	cfg.IdentityDefinitions = conformance.DefinitionRefs()
	svc, err := NewService(cfg)
	if err != nil {
		t.Fatalf("NewService with definitions: %v", err)
	}
	svc.Close()
}

// TestRootedRunContextCarriesTheSessionRun pins the run every server engine
// entry hands the engine (a new run, a retry, an approval resume, and an
// authorization continuation all pass through rootedRunContext): the
// session's run ID and owner, and no run, masking any the caller's context
// carried, when the session has no owner or no run ID.
func TestRootedRunContextCarriesTheSessionRun(t *testing.T) {
	alice := &session.Principal{Issuer: "https://login.example.com", Subject: "alice-204", GrantType: session.GrantTypeUser}
	outer := agentcredential.WithRun(t.Context(), agentcredential.Run{ID: "run_outer", Owner: identitybroker.Owner{Issuer: "https://login.example.com", Subject: "bob-117"}})
	newSession := func(t *testing.T, owner *session.Principal, runID string) *session.Session {
		t.Helper()
		sess := session.New("root-1", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "r1"}, session.Limits{}, time.Unix(1, 0))
		if owner != nil {
			if err := sess.RestoreLabels(owner, session.Authority{}); err != nil {
				t.Fatal(err)
			}
		}
		sess.BeginRun(runID)
		return sess
	}

	ctx := rootedRunContext(outer, newSession(t, alice, "run_a"), "/ws")
	want := agentcredential.Run{ID: "run_a", Owner: identitybroker.Owner{Issuer: alice.Issuer, Subject: alice.Subject}}
	if got, ok := agentcredential.RunFromContext(ctx); !ok || got != want {
		t.Fatalf("run = %+v, %v; want %+v", got, ok, want)
	}
	if root, _ := port.RootSessionIDFromContext(ctx); root != "root-1" {
		t.Fatalf("root = %q, want root-1", root)
	}
	for name, sess := range map[string]*session.Session{
		"no owner":  newSession(t, nil, "run_a"),
		"no run ID": newSession(t, alice, ""),
	} {
		if got, ok := agentcredential.RunFromContext(rootedRunContext(outer, sess, "/ws")); ok {
			t.Errorf("%s: run = %+v, want none, masking the caller's", name, got)
		}
	}
}
