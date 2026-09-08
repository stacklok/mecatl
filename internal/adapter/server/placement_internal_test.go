package server

import (
	"context"
	"errors"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/memledger"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

type exactPlacementProvider struct {
	binding PlacementBinding
	err     error
	calls   int
}

func (*exactPlacementProvider) Bind(context.Context, PlacementBindRequest) (PlacementBinding, error) {
	return PlacementBinding{}, errors.New("Bind must not be used for reattachment")
}

func (p *exactPlacementProvider) Reattach(_ context.Context, _ PlacementReattachRequest) (PlacementBinding, error) {
	p.calls++
	return p.binding, p.err
}

type affinityRunner struct{ root string }

func (affinityRunner) Run(context.Context, string) (tool.CommandResult, error) {
	return tool.CommandResult{}, nil
}
func (r affinityRunner) BoundWorkspaceRoot() string { return r.root }

func TestInvariant_persisted_placement_reattaches_exactly(t *testing.T) {
	ref := session.EnvironmentRef{Kind: "remote", ID: "private-7", Revision: "r3"}
	ws := memfs.NewWorkspace("/exact")
	env := tool.MustEnvironment(ref, ws, memledger.New(), affinityRunner{root: ws.Root()})
	provider := &exactPlacementProvider{binding: PlacementBinding{Environment: env, Ref: ref}}
	binder, err := NewPlacementBinder(provider)
	if err != nil {
		t.Fatal(err)
	}
	got, err := binder.Reattach(context.Background(), PlacementReattachRequest{Ref: ref, Scope: "tenant"})
	if err != nil {
		t.Fatalf("Reattach: %v", err)
	}
	if got.Ref != ref || got.Environment.Ref() != ref || got.Environment.Workspace().Root() != ws.Root() {
		t.Fatalf("reattached binding = %+v, want exact ref and root", got)
	}
	if provider.calls != 1 {
		t.Fatalf("Reattach calls = %d, want 1", provider.calls)
	}

	provider.binding.Ref.Revision = "r4"
	if _, err := binder.Reattach(context.Background(), PlacementReattachRequest{Ref: ref, Scope: "tenant"}); !errors.Is(err, ErrInvalidPlacementBinding) {
		t.Fatalf("revision mismatch = %v, want ErrInvalidPlacementBinding", err)
	}
	provider.binding = PlacementBinding{Ref: ref, Environment: tool.MustEnvironment(ref, ws, memledger.New(), affinityRunner{root: "/wrong"})}
	if _, err := binder.Reattach(context.Background(), PlacementReattachRequest{Ref: ref, Scope: "tenant"}); !errors.Is(err, ErrInvalidPlacementBinding) {
		t.Fatalf("runner/workspace mismatch = %v, want ErrInvalidPlacementBinding", err)
	}
	legacy, err := NewPlacementBinder(placementProviderFunc(func(context.Context, PlacementBindRequest) (PlacementBinding, error) {
		return PlacementBinding{}, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.Reattach(context.Background(), PlacementReattachRequest{Ref: ref, Scope: "tenant"}); !errors.Is(err, ErrPlacementUnavailable) {
		t.Fatalf("missing reattacher = %v, want ErrPlacementUnavailable", err)
	}
}

func TestADR_0291_WorktreeSelectorIsScopedAndMatchedAgainstCurrentInventory(t *testing.T) {
	key := make([]byte, worktreeSelectorKeySize)
	for i := range key {
		key[i] = byte(i + 1)
	}
	issuer, err := NewWorktreeSelectorIssuer(key)
	if err != nil {
		t.Fatal(err)
	}
	alice := &session.Principal{Issuer: "issuer", Subject: "alice"}
	bob := &session.Principal{Issuer: "issuer", Subject: "bob"}
	choice := Worktree{Path: "/private/wt", Branch: "feature", Head: "abc123"}
	token := issuer.Issue(alice, "source-a", choice)
	if token == "" || token == choice.Path {
		t.Fatalf("selector = %q, want opaque token", token)
	}
	got, err := issuer.Match(token, alice, "source-a", []Worktree{choice})
	if err != nil || got != choice {
		t.Fatalf("current Match = %+v, %v", got, err)
	}
	for name, tc := range map[string]struct {
		principal *session.Principal
		source    session.SessionID
		inventory []Worktree
	}{
		"wrong owner":  {bob, "source-a", []Worktree{choice}},
		"wrong source": {alice, "source-b", []Worktree{choice}},
		"stale":        {alice, "source-a", []Worktree{{Path: choice.Path, Branch: choice.Branch, Head: "new-head"}}},
		"absent":       {alice, "source-a", nil},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := issuer.Match(token, tc.principal, tc.source, tc.inventory); !errors.Is(err, ErrPlacementNotFound) {
				t.Fatalf("Match = %v, want ErrPlacementNotFound", err)
			}
		})
	}
	otherKey := append([]byte(nil), key...)
	otherKey[0] ^= 0xff
	afterRestart, _ := NewWorktreeSelectorIssuer(otherKey)
	if _, err := afterRestart.Match(token, alice, "source-a", []Worktree{choice}); !errors.Is(err, ErrPlacementNotFound) {
		t.Fatalf("restart Match = %v, want ErrPlacementNotFound", err)
	}
}
