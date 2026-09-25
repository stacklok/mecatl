package server

import (
	"context"
	"errors"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/memledger"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/prompt"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/mcp"
)

type failedCreateSourceResolver struct{ retired []session.SessionID }

func (*failedCreateSourceResolver) Borrow(context.Context, session.SessionID, *session.Principal, string) (CommandSourceBinding, func(), error) {
	return prompt.NoopExpander{}, func() {}, nil
}
func (*failedCreateSourceResolver) Activate(context.Context, session.SessionID, *session.Principal, string) error {
	return nil
}
func (r *failedCreateSourceResolver) Retire(id session.SessionID) { r.retired = append(r.retired, id) }

func TestHarnessContextUnpublishedCreateRetiresBinding(t *testing.T) {
	resolver := &failedCreateSourceResolver{}
	ref := session.EnvironmentRef{Kind: session.EnvKindMem, ID: "placement", Revision: "v1"}
	eng := repairEngine()
	var bound session.SessionID
	closed := false
	svc, err := NewService(Config{
		Engine: eng, Store: failingPlacementStore{Store: memstore.New(), err: errors.New("create failed")},
		PlacementProvider: &repairPlacementProvider{binding: PlacementBinding{Ref: ref, Environment: tool.MustEnvironment(ref, memfs.NewWorkspace("/bound"), memledger.New(), nil)}}, PlacementScope: "test", SharedEngineRoot: "/bound",
		Commands: resolver,
		SessionEngine: func(context.Context, ProviderSelector, []mcp.ServerConfig, SessionProfile, string, session.PermissionMode) (SessionEngineResult, error) {
			t.Fatal("legacy factory called")
			return SessionEngineResult{}, nil
		},
		SessionContextEngine: func(_ context.Context, id session.SessionID, _ *session.Principal, _ ExecutionFilesAcquirer, _ ProviderSelector, _ []mcp.ServerConfig, _ SessionProfile, _ string, _ session.PermissionMode, _ []tool.Tool) (SessionEngineResult, error) {
			bound = id
			return SessionEngineResult{Engine: eng, Close: func() error { closed = true; return nil }}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CreateSession(t.Context(), session.ModeDefault, session.Limits{}); err == nil {
		t.Fatal("failed store accepted session")
	}
	if bound == "" || !closed {
		t.Fatal("fixture did not bind and release per-session engine")
	}
	if len(resolver.retired) != 1 || resolver.retired[0] != bound {
		t.Fatalf("unpublished source binding not retired: %v", resolver.retired)
	}
}
