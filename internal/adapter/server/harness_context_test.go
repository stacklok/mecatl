package server

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/memledger"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/prompt"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/mcp"
)

type failedCreateSourceResolver struct {
	retired   []session.SessionID
	activated int
}

func (*failedCreateSourceResolver) Borrow(context.Context, session.SessionID, *session.Principal, string) (CommandSourceBinding, func(), error) {
	return prompt.NoopExpander{}, func() {}, nil
}
func (r *failedCreateSourceResolver) Activate(context.Context, session.SessionID, *session.Principal, string) error {
	r.activated++
	return nil
}
func (r *failedCreateSourceResolver) Retire(id session.SessionID) { r.retired = append(r.retired, id) }

func TestHarnessContextGeneratedIDCollisionAfterClose(t *testing.T) {
	store := memstore.New()
	resolver := &failedCreateSourceResolver{}
	ref := session.EnvironmentRef{Kind: session.EnvKindMem, ID: "placement", Revision: "v1"}
	eng := repairEngine()
	builds, closes := 0, 0
	svc, err := NewService(Config{
		Engine: eng, Store: store, OwnershipEnforced: true,
		NewID:             func() session.SessionID { return "repeated-generated-id" },
		PlacementProvider: &repairPlacementProvider{binding: PlacementBinding{Ref: ref, Environment: tool.MustEnvironment(ref, memfs.NewWorkspace("/bound"), memledger.New(), nil)}}, PlacementScope: "test", SharedEngineRoot: "/bound",
		Commands: resolver, OnCloseSession: resolver.Retire,
		SessionEngine: func(context.Context, ProviderSelector, []mcp.ServerConfig, SessionProfile, string, session.PermissionMode) (SessionEngineResult, error) {
			t.Fatal("legacy factory called")
			return SessionEngineResult{}, nil
		},
		SessionContextEngine: func(context.Context, session.SessionID, *session.Principal, ExecutionFilesAcquirer, ProviderSelector, []mcp.ServerConfig, SessionProfile, string, session.PermissionMode, []tool.Tool) (SessionEngineResult, error) {
			builds++
			return SessionEngineResult{Engine: eng, Close: func() error { closes++; return nil }}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	ctx := session.WithPrincipal(t.Context(), &session.Principal{Issuer: "issuer", Subject: "owner", GrantType: session.GrantTypeUser})
	sess, err := svc.CreateSession(ctx, session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	svc.CloseSession(sess.ID)
	if closes != 1 || len(resolver.retired) != 1 {
		t.Fatal("fixture did not close the persisted session's engine and source binding")
	}
	before, err := store.Load(ctx, sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if collision, err := svc.CreateSession(ctx, session.ModeDefault, session.Limits{}); !errors.Is(err, ErrInvalidArgument) || collision != nil {
			t.Fatalf("generated ID collision = %v, %v", collision, err)
		}
		if len(svc.reservedIDs) != 0 {
			t.Fatal("collision retained an ID reservation")
		}
	}
	if builds != 1 || resolver.activated != 0 || len(resolver.retired) != 1 {
		t.Fatalf("collision changed source lifecycle: builds=%d activations=%d retirements=%d", builds, resolver.activated, len(resolver.retired))
	}
	after, err := store.Load(ctx, sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatal("generated ID collision changed the persisted session")
	}
}

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
