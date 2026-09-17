package server_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

type intentLifecycle struct {
	intents                     []server.ReferenceIntent
	mu                          sync.Mutex
	committed, confirmed, reset []session.SessionID
}

func (l *intentLifecycle) ListReferenceIntents(context.Context, int) ([]server.ReferenceIntent, error) {
	return append([]server.ReferenceIntent(nil), l.intents...), nil
}
func (l *intentLifecycle) CommitReferenceIntent(_ context.Context, i server.ReferenceIntent) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.committed = append(l.committed, i.BindingID)
	return nil
}
func (l *intentLifecycle) ConfirmReferenceIntentDelete(_ context.Context, i server.ReferenceIntent) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.confirmed = append(l.confirmed, i.BindingID)
	return nil
}
func (l *intentLifecycle) CancelReferenceIntentDelete(_ context.Context, i server.ReferenceIntent) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.reset = append(l.reset, i.BindingID)
	return nil
}

func TestReferenceIntentReconciliationIsOwnerAndRefExact(t *testing.T) {
	owner := &session.Principal{Issuer: "issuer", Subject: "alice", GrantType: session.GrantTypeUser}
	ref := session.EnvironmentRef{Kind: session.EnvironmentKind("kubernetes"), ID: "env", Revision: "rev"}
	otherRef := session.EnvironmentRef{Kind: ref.Kind, ID: ref.ID, Revision: "new-revision"}
	store := memstore.New()
	persist := func(id session.SessionID, placement session.EnvironmentRef) {
		sess := session.New(id, session.ModeDefault, placement, session.Limits{MaxTurns: 1}, time.Unix(1, 0))
		if err := sess.RestoreLabels(owner, session.Authority{}); err != nil {
			t.Fatal(err)
		}
		if err := store.Save(t.Context(), sess); err != nil {
			t.Fatal(err)
		}
	}
	persist("create-match", ref)
	persist("delete-restored", ref)
	persist("reused-id", otherRef)
	lifecycle := &intentLifecycle{intents: []server.ReferenceIntent{
		{Ref: ref, Principal: owner, BindingID: "create-match", OperationID: "create-op"},
		{Ref: ref, Principal: owner, BindingID: "delete-gone", OperationID: "delete-op", PendingDelete: true},
		{Ref: ref, Principal: owner, BindingID: "delete-restored", OperationID: "delete-restore-op", PendingDelete: true},
		{Ref: ref, Principal: owner, BindingID: "reused-id", OperationID: "old-delete-op", PendingDelete: true},
	}}
	engine := agent.NewEngine(agent.Deps{LLM: mockllm.New(), Catalog: tool.NewCatalog(), Policy: permpolicy.NewPolicy(nil, nil)})
	svc, err := newPlacementTestService(server.Config{Engine: engine, Store: store, PlacementProvider: resolverPlacementProvider{}, PlacementScope: "test", ReferenceIntents: lifecycle})
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	svc.ReconcileReferenceIntents(t.Context())
	if len(lifecycle.committed) != 1 || lifecycle.committed[0] != "create-match" {
		t.Fatalf("committed=%v", lifecycle.committed)
	}
	if len(lifecycle.confirmed) != 1 || lifecycle.confirmed[0] != "delete-gone" {
		t.Fatalf("confirmed=%v", lifecycle.confirmed)
	}
	if len(lifecycle.reset) != 1 || lifecycle.reset[0] != "delete-restored" {
		t.Fatalf("cancelled=%v", lifecycle.reset)
	}
}
