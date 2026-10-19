package app

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/memledger"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

type placementContextKey struct{}

type compositionPlacementProvider struct {
	binding       server.PlacementBinding
	err           error
	calls         []server.PlacementBindRequest
	reattachCalls []server.PlacementReattachRequest
	contextSeen   any
}

func (p *compositionPlacementProvider) Bind(ctx context.Context, req server.PlacementBindRequest) (server.PlacementBinding, error) {
	p.calls = append(p.calls, req)
	p.contextSeen = ctx.Value(placementContextKey{})
	return p.binding, p.err
}

func (p *compositionPlacementProvider) Reattach(_ context.Context, req server.PlacementReattachRequest) (server.PlacementBinding, error) {
	p.reattachCalls = append(p.reattachCalls, req)
	return p.binding, p.err
}

type scriptedWorktreeLister struct {
	mu        sync.Mutex
	responses [][]server.Worktree
}

func (l *scriptedWorktreeLister) List(context.Context, string) ([]server.Worktree, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.responses) == 0 {
		return nil, nil
	}
	out := append([]server.Worktree(nil), l.responses[0]...)
	if len(l.responses) > 1 {
		l.responses = l.responses[1:]
	}
	return out, nil
}

type policyTestAccessProvider struct {
	*compositionPlacementProvider
	runs, successors int
}

func (*policyTestAccessProvider) Applies(session.EnvironmentRef) bool { return true }
func (p *policyTestAccessProvider) AcquireRun(context.Context, server.ExecutionRunRequest) (server.ExecutionRunHandle, error) {
	p.runs++
	return nil, nil
}

func (p *policyTestAccessProvider) ReserveSuccessor(context.Context, server.PlacementSuccessorRequest) (server.PlacementBinding, error) {
	p.successors++
	return p.binding, nil
}

func TestRemoteDefaultBindHonorsHostOwnerPolicy(t *testing.T) {
	remote := &policyTestAccessProvider{compositionPlacementProvider: &compositionPlacementProvider{}}
	provider := &profilePlacementProvider{remote: remote, local: &localPlacementProvider{}, allowed: func(p *session.Principal, _, _ string) bool {
		return p != nil && p.Subject == "alice"
	}, ownerAllowed: func(p *session.Principal) bool { return p != nil && p.Subject == "alice" }}
	for _, selector := range []server.PlacementSelector{server.DefaultPlacement(), server.SelectTemplate("go", "v1-"+strings.Repeat("a", 64))} {
		if _, err := provider.Bind(t.Context(), server.PlacementBindRequest{Selector: selector, Principal: &session.Principal{Subject: "bob"}, Scope: "test", Operation: server.PlacementOperationCreate}); !errors.Is(err, server.ErrPlacementNotFound) {
			t.Fatalf("unauthorized selection %+v returned %v", selector, err)
		}
	}
	if len(remote.calls) != 0 {
		t.Fatal("unauthorized caller reached the provider")
	}
	ref := session.EnvironmentRef{Kind: "kubernetes", ID: "owned", Revision: "rev"}
	if _, err := provider.Reattach(t.Context(), server.PlacementReattachRequest{Ref: ref, Principal: &session.Principal{Subject: "bob"}, Scope: "test"}); !errors.Is(err, server.ErrPlacementNotFound) || len(remote.reattachCalls) != 0 {
		t.Fatalf("revoked owner reattached or touched provider: %v", err)
	}
	if _, err := provider.AcquireRun(t.Context(), server.ExecutionRunRequest{Ref: ref, Principal: &session.Principal{Subject: "bob"}}); !errors.Is(err, server.ErrPlacementNotFound) || remote.runs != 0 {
		t.Fatalf("revoked owner acquired remote command authority: %v", err)
	}
	if _, err := provider.Bind(t.Context(), server.PlacementBindRequest{Selector: server.DefaultPlacement(), Principal: &session.Principal{Subject: "alice"}, Scope: "test", Operation: server.PlacementOperationCreate}); err != nil || len(remote.calls) != 1 {
		t.Fatalf("authorized caller did not reach provider: %v", err)
	}
}

func TestTemplateSelectionPolicyDoesNotStrandRetainedAllocation(t *testing.T) {
	ref := session.EnvironmentRef{Kind: "kubernetes", ID: "allocation", Revision: "allocation-revision"}
	remote := &policyTestAccessProvider{compositionPlacementProvider: &compositionPlacementProvider{binding: server.PlacementBinding{Ref: ref}}}
	provider := &profilePlacementProvider{remote: remote, local: &localPlacementProvider{},
		allowed: func(p *session.Principal, id, revision string) bool {
			return p != nil && p.Subject == "alice" && id == "go" && revision == "v1-eligible"
		}, ownerAllowed: func(p *session.Principal) bool { return p != nil && p.Subject == "alice" },
	}
	alice := &session.Principal{Subject: "alice"}
	for _, selector := range []server.PlacementSelector{server.SelectTemplate("go", "v1-old"), server.SelectTemplate("other", "v1-eligible")} {
		if _, err := provider.Bind(t.Context(), server.PlacementBindRequest{Selector: selector, Principal: alice}); !errors.Is(err, server.ErrPlacementNotFound) {
			t.Fatalf("ineligible selection %v admitted: %v", selector, err)
		}
	}
	if _, err := provider.Reattach(t.Context(), server.PlacementReattachRequest{Ref: ref, Principal: alice}); err != nil {
		t.Fatalf("retained allocation stranded: %v", err)
	}
	if _, err := provider.AcquireRun(t.Context(), server.ExecutionRunRequest{Ref: ref, Principal: alice}); err != nil || remote.runs != 1 {
		t.Fatalf("retained run denied: %v", err)
	}
	if _, err := provider.ReserveSuccessor(t.Context(), server.PlacementSuccessorRequest{Ref: ref, Principal: alice}); err != nil || remote.successors != 1 {
		t.Fatalf("retained successor denied: %v", err)
	}
	bob := &session.Principal{Subject: "bob"}
	if _, err := provider.Reattach(t.Context(), server.PlacementReattachRequest{Ref: ref, Principal: bob}); !errors.Is(err, server.ErrPlacementNotFound) {
		t.Fatalf("unauthorized reattach: %v", err)
	}
	if _, err := provider.AcquireRun(t.Context(), server.ExecutionRunRequest{Ref: ref, Principal: bob}); !errors.Is(err, server.ErrPlacementNotFound) {
		t.Fatalf("unauthorized run: %v", err)
	}
	if _, err := provider.ReserveSuccessor(t.Context(), server.PlacementSuccessorRequest{Ref: ref, Principal: bob}); !errors.Is(err, server.ErrPlacementNotFound) || remote.successors != 1 {
		t.Fatalf("unauthorized successor: %v", err)
	}
}

func TestInvariant_local_placement_identity_and_atomic_worktree_binding(t *testing.T) {
	rootA, rootB := t.TempDir(), t.TempDir()
	providerA := &localPlacementProvider{scope: "test", root: rootA, workspace: func(root string) tool.Workspace { return memfs.NewWorkspace(root) }}
	providerB := &localPlacementProvider{scope: "test", root: rootB, workspace: func(root string) tool.Workspace { return memfs.NewWorkspace(root) }}
	binding, err := providerA.Bind(context.Background(), server.PlacementBindRequest{Selector: server.DefaultPlacement(), Scope: "test", Operation: server.PlacementOperationCreate})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := providerB.Reattach(context.Background(), server.PlacementReattachRequest{Ref: binding.Ref, Scope: "test"}); !errors.Is(err, server.ErrPlacementNotFound) {
		t.Fatalf("root-A ref reattached under root B: %v", err)
	}

	key := make([]byte, 32)
	issuer, err := server.NewWorktreeSelectorIssuer(key)
	if err != nil {
		t.Fatal(err)
	}
	oldChoice := server.Worktree{Path: "/private/worktree", Branch: "feature", Head: "old"}
	newChoice := server.Worktree{Path: oldChoice.Path, Branch: oldChoice.Branch, Head: "new"}
	lister := &scriptedWorktreeLister{responses: [][]server.Worktree{{oldChoice}, {oldChoice}, {newChoice}}}
	workspaceCalls := 0
	provider := &localPlacementProvider{scope: "test", root: rootA, selectors: issuer, worktrees: lister, workspace: func(root string) tool.Workspace {
		workspaceCalls++
		return memfs.NewWorkspace(root)
	}}
	sourceRef := configuredLocalPlacementRef(rootA)
	choices, err := provider.ListWorktrees(context.Background(), server.PlacementDiscoveryRequest{Source: "source", SourceRef: sourceRef, Scope: "test"})
	if err != nil || len(choices) != 1 {
		t.Fatalf("ListWorktrees = %+v, %v", choices, err)
	}
	_, err = provider.Bind(context.Background(), server.PlacementBindRequest{Selector: server.SelectWorktree("source", sourceRef, choices[0].Selector), Scope: "test", Operation: server.PlacementOperationSuccessor})
	if !errors.Is(err, server.ErrPlacementChanged) || workspaceCalls != 0 {
		t.Fatalf("replacement bind = %v, workspace constructions = %d; want changed before access", err, workspaceCalls)
	}
}

func TestInvariant_selected_successor_reattaches_after_restart(t *testing.T) {
	root := t.TempDir()
	choice := server.Worktree{Path: "/private/worktree", Branch: "feature", Head: "head-1"}
	lister := &scriptedWorktreeLister{responses: [][]server.Worktree{{choice}}}
	issuer, _ := server.NewWorktreeSelectorIssuer(make([]byte, 32))
	provider := &localPlacementProvider{scope: "test", root: root, selectors: issuer, worktrees: lister, workspace: func(root string) tool.Workspace { return memfs.NewWorkspace(root) }}
	sourceRef := configuredLocalPlacementRef(root)
	listed, err := provider.ListWorktrees(context.Background(), server.PlacementDiscoveryRequest{Source: "source", SourceRef: sourceRef, Scope: "test"})
	if err != nil {
		t.Fatal(err)
	}
	selected, err := provider.Bind(context.Background(), server.PlacementBindRequest{Selector: server.SelectWorktree("source", sourceRef, listed[0].Selector), Scope: "test", Operation: server.PlacementOperationSuccessor})
	if err != nil {
		t.Fatal(err)
	}
	restarted := &localPlacementProvider{scope: "test", root: root, worktrees: &scriptedWorktreeLister{responses: [][]server.Worktree{{choice}}}, workspace: func(root string) tool.Workspace { return memfs.NewWorkspace(root) }}
	rebound, err := restarted.Reattach(context.Background(), server.PlacementReattachRequest{Ref: selected.Ref, Scope: "test"})
	if err != nil || rebound.Ref != selected.Ref || rebound.Environment.Workspace().Root() != choice.Path {
		t.Fatalf("restart reattach = %+v, %v", rebound, err)
	}
}

func TestCompositionConfiguresProviderOwnedPlacements(t *testing.T) {
	t.Parallel()

	t.Run("selector vocabulary is closed", func(t *testing.T) {
		selectors := []server.PlacementSelector{
			server.DefaultPlacement(),
			server.NoFSPlacement(),
		}
		for _, selector := range selectors {
			if !selector.Valid() {
				t.Fatalf("canonical selector %+v is invalid", selector)
			}
		}
		if (server.PlacementSelector{Kind: "path", ID: "/server/root"}).Valid() {
			t.Fatal("path selector widened the closed placement vocabulary")
		}
	})

	t.Run("first bind uses the session context", func(t *testing.T) {
		ref := session.EnvironmentRef{Kind: "remote", ID: "opaque-context-id", Revision: "r1"}
		provider := &compositionPlacementProvider{binding: server.PlacementBinding{
			Ref: ref,
			Environment: tool.MustEnvironment(
				ref,
				memfs.NewWorkspace("/private-context-root"), memledger.New(), nil,
			),
		}}
		ctx := context.WithValue(context.Background(), placementContextKey{}, "build-context")
		built, err := buildIsolated(t, ctx, Config{
			Workspace: rootForPlacementTest(t), UseMock: true,
			PlacementProvider: provider, PlacementScope: "deployment-a",
		})
		if err != nil {
			t.Fatalf("Build: %v", err)
		}
		defer built.Close()
		if _, err := built.Service.CreateSession(ctx, session.ModeDefault, session.Limits{}); err != nil {
			t.Fatalf("CreateSession: %v", err)
		}
		if provider.contextSeen != "build-context" {
			t.Fatalf("session provider context value = %v, want build-context", provider.contextSeen)
		}
	})

	t.Run("trusted local default uses opaque identity", func(t *testing.T) {
		root := t.TempDir()
		built, err := buildIsolated(t, context.Background(), Config{Workspace: root, UseMock: true})
		if err != nil {
			t.Fatalf("Build: %v", err)
		}
		defer built.Close()

		binding, err := built.Service.BindPlacement(context.Background(), server.DefaultPlacement(), server.PlacementOperationCreate)
		if err != nil {
			t.Fatalf("BindPlacement(default): %v", err)
		}
		if binding.Ref.Kind != "local" || binding.Ref.ID == "" || binding.Ref.Revision == "" {
			t.Fatalf("default ref = %+v, want complete local provider ref", binding.Ref)
		}
		if binding.Ref.ID != root {
			t.Fatalf("private default ID = %q, want exact configured root %q", binding.Ref.ID, root)
		}
		if binding.Environment.Workspace() == nil {
			t.Fatal("default binding has nil workspace")
		}
	})

	for _, kind := range []session.EnvironmentKind{"worktree", "remote"} {
		kind := kind
		t.Run(string(kind)+" provider extension", func(t *testing.T) {
			ref := session.EnvironmentRef{Kind: kind, ID: "opaque-provider-id", Revision: "inventory-r7"}
			provider := &compositionPlacementProvider{binding: server.PlacementBinding{
				Ref: ref,
				Environment: tool.MustEnvironment(
					ref,
					memfs.NewWorkspace("/private-provider-root"), memledger.New(), nil,
				),
				Metadata: server.PlacementMetadata{Label: "Provider placement"},
			}}
			built, err := buildIsolated(t, context.Background(), Config{
				Workspace: rootForPlacementTest(t), UseMock: true,
				PlacementProvider: provider, PlacementScope: "deployment-a",
			})
			if err != nil {
				t.Fatalf("Build: %v", err)
			}
			defer built.Close()
			if len(provider.calls) != 0 {
				t.Fatalf("Build eagerly bound provider: %+v", provider.calls)
			}

			binding, err := built.Service.BindPlacement(context.Background(), server.DefaultPlacement(), server.PlacementOperationCreate)
			if err != nil {
				t.Fatalf("BindPlacement(default): %v", err)
			}
			if binding.Ref != ref {
				t.Fatalf("provider ref = %+v, want stable exact %+v", binding.Ref, ref)
			}
			if len(provider.calls) != 1 || provider.calls[0].Selector.Kind != server.PlacementSelectorDefault {
				t.Fatalf("first Bind calls = %+v, want exactly one default Bind", provider.calls)
			}
		})
	}

	t.Run("invalid default fails first bind without startup allocation", func(t *testing.T) {
		provider := &compositionPlacementProvider{err: server.ErrPlacementUnavailable}
		built, err := buildIsolated(t, context.Background(), Config{
			Workspace: rootForPlacementTest(t), UseMock: true,
			PlacementProvider: provider, PlacementScope: "deployment-a",
		})
		if err != nil {
			t.Fatalf("Build: %v", err)
		}
		defer built.Close()
		if len(provider.calls) != 0 {
			t.Fatalf("Build eagerly bound invalid provider: %d calls", len(provider.calls))
		}
		_, err = built.Service.BindPlacement(context.Background(), server.DefaultPlacement(), server.PlacementOperationCreate)
		if !errors.Is(err, server.ErrPlacementUnavailable) {
			t.Fatalf("BindPlacement error = %v, want ErrPlacementUnavailable", err)
		}
		if len(provider.calls) != 1 {
			t.Fatalf("first Bind calls = %d, want 1", len(provider.calls))
		}
	})
}

func rootForPlacementTest(t *testing.T) string {
	t.Helper()
	return t.TempDir()
}
