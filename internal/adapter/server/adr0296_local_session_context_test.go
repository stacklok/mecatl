package server_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/memledger"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/nofs"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

type localContextPlacementProvider struct {
	defaultBinding server.PlacementBinding
	reattach       func(server.PlacementReattachRequest) (server.PlacementBinding, error)
	reattachCalls  int
	closed         int
}

func (p *localContextPlacementProvider) Bind(_ context.Context, _ server.PlacementBindRequest) (server.PlacementBinding, error) {
	return p.defaultBinding, nil
}

func (p *localContextPlacementProvider) Reattach(_ context.Context, req server.PlacementReattachRequest) (server.PlacementBinding, error) {
	p.reattachCalls++
	return p.reattach(req)
}

func localContextService(t *testing.T, provider *localContextPlacementProvider) (*server.Service, context.Context, context.Context, *memstore.Store) {
	t.Helper()
	store := memstore.New()
	svc, err := server.NewService(server.Config{
		Engine:            agent.NewEngine(agent.Deps{LLM: mockllm.New(), Catalog: tool.NewCatalog()}),
		Store:             store,
		PlacementProvider: provider,
		PlacementScope:    "local-context-test",
		OwnershipEnforced: true,
		Now:               func() time.Time { return time.Unix(0, 0) },
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	owner := session.WithPrincipal(context.Background(), &session.Principal{Issuer: "https://idp.example", Subject: "alice", GrantType: session.GrantTypeUser})
	other := session.WithPrincipal(context.Background(), &session.Principal{Issuer: "https://idp.example", Subject: "bob", GrantType: session.GrantTypeUser})
	return svc, owner, other, store
}

func localContextBinding(ref session.EnvironmentRef, root string) server.PlacementBinding {
	return server.PlacementBinding{Ref: ref, Environment: tool.MustEnvironment(ref, memfs.NewWorkspace(root), memledger.New(), nil)}
}

func saveLocalContextSession(t *testing.T, store *memstore.Store, id string, ref session.EnvironmentRef, owner *session.Principal) {
	t.Helper()
	sess := session.New(session.SessionID(id), session.ModeDefault, ref, session.Limits{}, time.Unix(0, 0))
	sess.Owner = owner.Clone()
	if err := store.Save(context.Background(), sess); err != nil {
		t.Fatalf("Save: %v", err)
	}
}

func TestADR_0296_LocalContextClosesProvisionalBinding(t *testing.T) {
	ref := session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "opaque", Revision: "v1"}
	provider := &localContextPlacementProvider{defaultBinding: localContextBinding(ref, "/daemon")}
	provider.reattach = func(req server.PlacementReattachRequest) (server.PlacementBinding, error) {
		binding := localContextBinding(req.Ref, "/private/root")
		binding.Close = func() error {
			provider.closed++
			return nil
		}
		return binding, nil
	}
	svc, owner, _, store := localContextService(t, provider)
	saveLocalContextSession(t, store, "owned", ref, session.PrincipalFromContext(owner))

	got, err := server.NewLocalSessionContextServer(svc).GetLocalSessionContext(owner, &mecatlv1.GetLocalSessionContextRequest{SessionId: "owned"})
	if err != nil {
		t.Fatalf("GetLocalSessionContext: %v", err)
	}
	if got.GetWorkspacePath() != "/private/root" {
		t.Fatalf("workspace path = %q, want reattached root", got.GetWorkspacePath())
	}
	if provider.closed != 1 {
		t.Fatalf("provisional placement binding closes = %d, want 1", provider.closed)
	}
}

func TestADR_0296_LocalContextUsesExactReattachment(t *testing.T) {
	ref := session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "opaque-default", Revision: "v1"}
	provider := &localContextPlacementProvider{defaultBinding: localContextBinding(ref, "/daemon-launch")}
	provider.reattach = func(req server.PlacementReattachRequest) (server.PlacementBinding, error) {
		if req.Ref != ref {
			t.Fatalf("Reattach ref = %+v, want %+v", req.Ref, ref)
		}
		binding := localContextBinding(ref, "/exact-session-root")
		binding.Close = func() error {
			provider.closed++
			return nil
		}
		return binding, nil
	}
	svc, owner, _, store := localContextService(t, provider)
	saveLocalContextSession(t, store, "owned", ref, session.PrincipalFromContext(owner))

	got, err := server.NewLocalSessionContextServer(svc).GetLocalSessionContext(owner, &mecatlv1.GetLocalSessionContextRequest{SessionId: "owned"})
	if err != nil {
		t.Fatalf("GetLocalSessionContext: %v", err)
	}
	if got.GetWorkspacePath() != "/exact-session-root" {
		t.Fatalf("workspace path = %q, want exact reattached root", got.GetWorkspacePath())
	}
	if provider.reattachCalls != 1 {
		t.Fatalf("Reattach calls = %d, want 1", provider.reattachCalls)
	}
	if provider.closed != 1 {
		t.Fatalf("provisional binding close calls = %d, want 1", provider.closed)
	}
}

func TestADR_0296_LocalContextReturnsSelectedWorktreeRoot(t *testing.T) {
	ref := session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "opaque-worktree", Revision: "worktree-v3"}
	provider := &localContextPlacementProvider{defaultBinding: localContextBinding(ref, "/embedded-daemon-workspace")}
	provider.reattach = func(server.PlacementReattachRequest) (server.PlacementBinding, error) {
		return localContextBinding(ref, "/selected/worktree"), nil
	}
	svc, owner, _, store := localContextService(t, provider)
	saveLocalContextSession(t, store, "successor", ref, session.PrincipalFromContext(owner))

	got, err := server.NewLocalSessionContextServer(svc).GetLocalSessionContext(owner, &mecatlv1.GetLocalSessionContextRequest{SessionId: "successor"})
	if err != nil {
		t.Fatalf("GetLocalSessionContext: %v", err)
	}
	if got.GetWorkspacePath() != "/selected/worktree" {
		t.Fatalf("workspace path = %q, want selected worktree root", got.GetWorkspacePath())
	}
}

func TestADR_0296_LocalContextOwnerIsolation(t *testing.T) {
	ref := session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "opaque", Revision: "v1"}
	provider := &localContextPlacementProvider{defaultBinding: localContextBinding(ref, "/daemon")}
	provider.reattach = func(server.PlacementReattachRequest) (server.PlacementBinding, error) {
		return localContextBinding(ref, "/private/root"), nil
	}
	svc, owner, other, store := localContextService(t, provider)
	saveLocalContextSession(t, store, "owned", ref, session.PrincipalFromContext(owner))
	api := server.NewLocalSessionContextServer(svc)

	for _, ctx := range []context.Context{owner, other} {
		id := "missing"
		if ctx == other {
			id = "owned"
		}
		got, err := api.GetLocalSessionContext(ctx, &mecatlv1.GetLocalSessionContextRequest{SessionId: id})
		if got != nil || status.Code(err) != codes.NotFound {
			t.Fatalf("GetLocalSessionContext(%q) = %#v, %v; want nil NOT_FOUND", id, got, err)
		}
		if strings.Contains(status.Convert(err).Message(), "/") || strings.Contains(status.Convert(err).Message(), "opaque") {
			t.Fatalf("NOT_FOUND message leaked detail: %q", status.Convert(err).Message())
		}
	}
	if provider.reattachCalls != 0 {
		t.Fatalf("foreign or missing request reattached %d times, want 0", provider.reattachCalls)
	}
}

func TestADR_0296_LocalContextFailsClosedForIneligibleOrUnavailablePlacement(t *testing.T) {
	local := session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "opaque-local", Revision: "v1"}
	provider := &localContextPlacementProvider{defaultBinding: localContextBinding(local, "/daemon")}
	provider.reattach = func(req server.PlacementReattachRequest) (server.PlacementBinding, error) {
		if req.Ref.ID == "unavailable" {
			return server.PlacementBinding{}, errors.New("provider local-fs at /secret is down: " + server.ErrPlacementUnavailable.Error())
		}
		return localContextBinding(req.Ref, "/should-not-disclose"), nil
	}
	svc, owner, _, store := localContextService(t, provider)
	ownerPrincipal := session.PrincipalFromContext(owner)
	saveLocalContextSession(t, store, "no-fs", session.EnvironmentRef{Kind: session.EnvKindNoFS, ID: "none", Revision: "v1"}, ownerPrincipal)
	saveLocalContextSession(t, store, "remote", session.EnvironmentRef{Kind: "remote", ID: "opaque-remote", Revision: "v1"}, ownerPrincipal)
	saveLocalContextSession(t, store, "unavailable", session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "unavailable", Revision: "v1"}, ownerPrincipal)
	api := server.NewLocalSessionContextServer(svc)

	for _, tc := range []struct {
		id   string
		want codes.Code
	}{{"no-fs", codes.FailedPrecondition}, {"remote", codes.FailedPrecondition}, {"unavailable", codes.Unavailable}} {
		got, err := api.GetLocalSessionContext(owner, &mecatlv1.GetLocalSessionContextRequest{SessionId: tc.id})
		if got != nil || status.Code(err) != tc.want {
			t.Fatalf("GetLocalSessionContext(%q) = %#v, %v; want nil %v", tc.id, got, err, tc.want)
		}
	}
}

func TestADR_0296_LocalContextRejectsInvalidReattachment(t *testing.T) {
	ref := session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "opaque", Revision: "v1"}
	provider := &localContextPlacementProvider{defaultBinding: localContextBinding(ref, "/daemon-default")}
	provider.reattach = func(req server.PlacementReattachRequest) (server.PlacementBinding, error) {
		switch req.Ref.ID {
		case "mismatch":
			wrong := req.Ref
			wrong.Revision = "other"
			return localContextBinding(wrong, "/wrong"), nil
		case "nil-workspace":
			return server.PlacementBinding{Ref: req.Ref}, nil
		default:
			return server.PlacementBinding{Ref: req.Ref, Environment: tool.MustEnvironment(req.Ref, nofs.New(), memledger.New(), nil)}, nil
		}
	}
	svc, owner, _, store := localContextService(t, provider)
	ownerPrincipal := session.PrincipalFromContext(owner)
	for _, id := range []string{"mismatch", "nil-workspace", "empty-root"} {
		saveLocalContextSession(t, store, id, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: id, Revision: "v1"}, ownerPrincipal)
	}
	api := server.NewLocalSessionContextServer(svc)
	for _, id := range []string{"mismatch", "nil-workspace", "empty-root"} {
		got, err := api.GetLocalSessionContext(owner, &mecatlv1.GetLocalSessionContextRequest{SessionId: id})
		if got != nil || status.Code(err) != codes.FailedPrecondition {
			t.Fatalf("GetLocalSessionContext(%q) = %#v, %v; want nil FAILED_PRECONDITION", id, got, err)
		}
	}
}

func TestADR_0296_LocalContextErrorsDoNotLeakPathOrEnvironmentRef(t *testing.T) {
	ref := session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "private-ref-id", Revision: "private-revision"}
	provider := &localContextPlacementProvider{defaultBinding: localContextBinding(ref, "/daemon")}
	provider.reattach = func(server.PlacementReattachRequest) (server.PlacementBinding, error) {
		return server.PlacementBinding{}, errors.New("provider named local-provider failed at /private/root for private-ref-id/private-revision: " + server.ErrPlacementUnavailable.Error())
	}
	svc, owner, other, store := localContextService(t, provider)
	saveLocalContextSession(t, store, "owned", ref, session.PrincipalFromContext(owner))
	api := server.NewLocalSessionContextServer(svc)

	for _, tc := range []struct {
		ctx  context.Context
		id   string
		want codes.Code
	}{{owner, "missing", codes.NotFound}, {other, "owned", codes.NotFound}, {owner, "owned", codes.Unavailable}} {
		got, err := api.GetLocalSessionContext(tc.ctx, &mecatlv1.GetLocalSessionContextRequest{SessionId: tc.id})
		if got != nil || status.Code(err) != tc.want {
			t.Fatalf("GetLocalSessionContext(%q) = %#v, %v; want nil %v", tc.id, got, err, tc.want)
		}
		message := status.Convert(err).Message()
		for _, secret := range []string{"/private/root", "private-ref-id", "private-revision", "local-provider", "provider named", "failed at"} {
			if strings.Contains(message, secret) {
				t.Fatalf("%v message leaked %q: %q", tc.want, secret, message)
			}
		}
	}
}
