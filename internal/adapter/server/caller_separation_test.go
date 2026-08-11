package server_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

// TestCallerSeparation_Scenario1_AtomicCreationBindsVerifiedOwner pins ADR
// 0102's creation rule: the verified owner is persisted before visibility, the
// same immutable request is retry-idempotent, and another owner learns only
// absence from a colliding caller-selected ID.
func TestCallerSeparation_Scenario1_AtomicCreationBindsVerifiedOwner(t *testing.T) {
	store := memstore.New()
	svc, err := server.NewService(server.Config{
		Engine: agent.NewEngine(agent.Deps{
			LLM:     mockllm.New(),
			Catalog: tool.NewCatalog(),
			Policy:  permpolicy.NewPolicy(nil, nil),
			Model:   "test-model",
		}),
		Store:             store,
		Workspaces:        func(root string) tool.Workspace { return memfs.NewWorkspace(root) },
		Now:               func() time.Time { return time.Unix(0, 0) },
		OwnershipEnforced: true,
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}

	id := session.SessionID("caller-selected-id")
	alice := &session.Principal{Issuer: "https://idp.example/alice", Subject: "same", GrantType: session.GrantTypeUser}
	bob := &session.Principal{Issuer: "https://idp.example/bob", Subject: "same", GrantType: session.GrantTypeUser}
	request := func(ctx context.Context, workspace string) (*session.Session, error) {
		return svc.CreateSessionWithProfile(ctx, workspace, session.ModeDefault, session.Limits{}, server.ProviderSelector{}, server.ProfileDefault, server.WithSessionID(id))
	}

	created, err := request(session.WithPrincipal(context.Background(), alice), "/ws")
	if err != nil {
		t.Fatalf("first create: %v", err)
	}
	if created.Owner == nil || !created.Owner.SameIdentity(alice) {
		t.Fatalf("created owner = %+v, want Alice's verified identity", created.Owner)
	}

	retry, err := request(session.WithPrincipal(context.Background(), &session.Principal{
		Issuer: alice.Issuer, Subject: alice.Subject, GrantType: session.GrantTypeClientCredentials, Name: "untrusted display",
	}), "/ws")
	if err != nil {
		t.Fatalf("same-owner identical retry: %v", err)
	}
	if retry.ID != created.ID || retry.Owner == nil || !retry.Owner.SameIdentity(alice) {
		t.Fatalf("retry = %+v, want the original owned session", retry)
	}

	if _, err := request(session.WithPrincipal(context.Background(), alice), "/other"); !errors.Is(err, server.ErrInvalidArgument) {
		t.Fatalf("same owner different immutable request error = %v, want ErrInvalidArgument", err)
	}
	if _, err := request(session.WithPrincipal(context.Background(), bob), "/ws"); !errors.Is(err, server.ErrNotFound) {
		t.Fatalf("cross-owner collision error = %v, want ErrNotFound", err)
	}
	persisted, err := store.Load(context.Background(), id)
	if err != nil {
		t.Fatalf("load original: %v", err)
	}
	if persisted.Owner == nil || !persisted.Owner.SameIdentity(alice) {
		t.Fatalf("cross-owner collision overwrote or adopted owner: %+v", persisted.Owner)
	}
}
