package server

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/identitybroker"
)

func TestIdentityBrokerOpensOnlyForRootSessions(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	core, err := identitybroker.New(identitybroker.Config{
		TrustDomain: "agents.example.com", Audience: "https://broker.example.com", CredentialLifetime: 5 * time.Minute,
	}, nil, key)
	if err != nil {
		t.Fatal(err)
	}
	broker := identitybroker.NewInProcess(identitybroker.UnverifiedAuthenticator{Name: "mecak8s-test"}, core)
	svc, err := NewService(Config{
		Engine:            brokerEngineResult().Engine,
		Store:             memstore.New(),
		PlacementProvider: brokerPlacementProvider{},
		PlacementScope:    "test",
		IdentityBroker:    broker,
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
	sess := session.New("s", session.ModeDefault, ref, session.Limits{}, time.Unix(1, 0))
	if got := identityAgentDefinition(sess); got != (identitybroker.DefinitionRef{Source: "system", Name: "main"}) {
		t.Fatalf("definition of an ordinary session = %+v, want system/main", got)
	}
	sess.AgentDefinitionName = "reviewer"
	if got := identityAgentDefinition(sess); got != (identitybroker.DefinitionRef{Name: "reviewer"}) {
		t.Fatalf("definition of a named session = %+v, want reviewer with no source", got)
	}
}

// newRegisteringService returns a Service with the real identity broker, and
// a saved-looking root session with an owner.
func newRegisteringService(t *testing.T) (*Service, *session.Session) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	core, err := identitybroker.New(identitybroker.Config{
		TrustDomain: "agents.example.com", Audience: "https://broker.example.com", CredentialLifetime: 5 * time.Minute,
	}, nil, key)
	if err != nil {
		t.Fatal(err)
	}
	svc, err := NewService(Config{
		Engine:            brokerEngineResult().Engine,
		Store:             memstore.New(),
		PlacementProvider: brokerPlacementProvider{},
		PlacementScope:    "test",
		IdentityBroker:    identitybroker.NewInProcess(identitybroker.UnverifiedAuthenticator{Name: "mecak8s-test"}, core),
	})
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
	svc, sess := newRegisteringService(t)
	// Left from an earlier incarnation of this session ID, as a session
	// recreated elsewhere would leave it.
	stale := identityAgent{
		incarnation:     string(session.NewIncarnationID()),
		brokerSessionID: "bs_stale",
		registration:    identitybroker.Registration{Agent: identitybroker.AgentRef{Subject: "spiffe://agents.example.com/mecatl/agent/shared/inst/stale"}},
		renewAt:         time.Now().Add(time.Hour),
	}
	svc.mu.Lock()
	svc.identityAgents[sess.ID] = stale
	svc.mu.Unlock()
	if err := svc.openIdentityBrokerSession(t.Context(), sess); err != nil {
		t.Fatalf("openIdentityBrokerSession with a stale credential held: %v", err)
	}
	svc.mu.Lock()
	got := svc.identityAgents[sess.ID]
	svc.mu.Unlock()
	if got.incarnation != string(sess.Incarnation()) || got.registration.Agent == stale.registration.Agent {
		t.Fatalf("held credential = %+v, want the current incarnation's agent", got)
	}
}

func TestClosingASessionDropsItsHeldCredential(t *testing.T) {
	svc, sess := newRegisteringService(t)
	if err := svc.openIdentityBrokerSession(t.Context(), sess); err != nil {
		t.Fatal(err)
	}
	held := func() bool {
		svc.mu.Lock()
		defer svc.mu.Unlock()
		_, ok := svc.identityAgents[sess.ID]
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
	svc.mu.Lock()
	_, ok := svc.identityAgents[sess.ID]
	svc.mu.Unlock()
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
	go func() { done <- svc.openTeamRootIdentityBrokerSession(t.Context(), sess.ID) }()
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
