package agentcredential_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stacklok/mecatl/internal/agentcredential"
	"github.com/stacklok/mecatl/internal/identitybroker"
)

func TestRunFromContext(t *testing.T) {
	owner := identitybroker.Owner{Issuer: "https://login.example.com", Subject: "alice-204"}
	if _, ok := agentcredential.RunFromContext(context.Background()); ok {
		t.Fatal("a bare context carries a run")
	}
	ctx := agentcredential.WithRun(context.Background(), agentcredential.Run{ID: "run_a", Owner: owner})
	if got, ok := agentcredential.RunFromContext(ctx); !ok || got.ID != "run_a" || got.Owner != owner {
		t.Fatalf("RunFromContext = %+v, %v; want run_a", got, ok)
	}
	// A zero run masks one an outer context carried.
	if _, ok := agentcredential.RunFromContext(agentcredential.WithRun(ctx, agentcredential.Run{})); ok {
		t.Fatal("a zero run did not mask the outer run")
	}
}

// TestHolderKeepsDelegationsWhileItsHelpersAreHeld pins that a helper's
// current delegation is held only with its credential, that every delegation
// is listed in the order recorded, and that dropping the root forgets them.
func TestHolderKeepsDelegationsWhileItsHelpersAreHeld(t *testing.T) {
	h, _, _ := newHolder(t)
	childKey := agentcredential.Key{RootSessionID: "root", AgentSessionID: "child"}
	first := agentcredential.Delegation{ID: "dlg_1", Run: agentcredential.Run{ID: "run_c1"}, DelegatingRun: "run_root", DelegatingAgentSession: "root"}
	if err := h.SetDelegation(childKey, first); !errors.Is(err, agentcredential.ErrNoCredential) {
		t.Fatalf("SetDelegation with no credential held = %v, want ErrNoCredential", err)
	}

	mustEnsure(t, h, "bs_1")
	main, _ := h.Held(key)
	if err := h.Ensure(t.Context(), childKey, helperRequest("bs_1", "child", "root", main.Agent), admitAll); err != nil {
		t.Fatalf("Ensure child: %v", err)
	}
	if err := h.SetDelegation(childKey, first); err != nil {
		t.Fatalf("SetDelegation: %v", err)
	}
	second := agentcredential.Delegation{ID: "dlg_2", Run: agentcredential.Run{ID: "run_c2"}, DelegatingRun: "run_root2", DelegatingAgentSession: "root"}
	if err := h.SetDelegation(childKey, second); err != nil {
		t.Fatalf("SetDelegation again: %v", err)
	}
	if got, ok := h.Delegation(childKey); !ok || got != second {
		t.Fatalf("Delegation = %+v, %v; want the latest %+v", got, ok, second)
	}
	all := h.Delegations("root")
	if len(all) != 2 || all[0].Delegation != first || all[1].Delegation != second || all[0].AgentSessionID != "child" {
		t.Fatalf("Delegations = %+v, want both in order", all)
	}

	h.Drop("root")
	if _, ok := h.Delegation(childKey); ok {
		t.Fatal("a dropped root's delegation is held")
	}
	if all := h.Delegations("root"); len(all) != 0 {
		t.Fatalf("a dropped root lists %+v", all)
	}
}
