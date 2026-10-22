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
