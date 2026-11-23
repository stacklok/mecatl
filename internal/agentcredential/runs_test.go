package agentcredential_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
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

// TestHolderForgetsTheOldestDelegationAtTheCap pins that a root lists at most
// MaxDelegationsPerRoot delegations, forgetting the oldest first, while each
// helper's current delegation stays held.
func TestHolderForgetsTheOldestDelegationAtTheCap(t *testing.T) {
	h, _, _ := newHolder(t)
	mustEnsure(t, h, "bs_1")
	main, _ := h.Held(key)
	childKey := agentcredential.Key{RootSessionID: "root", AgentSessionID: "child"}
	if err := h.Ensure(t.Context(), childKey, helperRequest("bs_1", "child", "root", main.Agent), admitAll); err != nil {
		t.Fatalf("Ensure child: %v", err)
	}
	delegation := func(i int) agentcredential.Delegation {
		return agentcredential.Delegation{ID: fmt.Sprintf("dlg_%d", i), Run: agentcredential.Run{ID: fmt.Sprintf("run_%d", i)}, DelegatingRun: "run_root", DelegatingAgentSession: "root"}
	}
	for i := range agentcredential.MaxDelegationsPerRoot + 1 {
		if err := h.SetDelegation(childKey, delegation(i)); err != nil {
			t.Fatalf("SetDelegation %d: %v", i, err)
		}
	}
	all := h.Delegations("root")
	if len(all) != agentcredential.MaxDelegationsPerRoot || all[0].Delegation != delegation(1) || all[len(all)-1].Delegation != delegation(agentcredential.MaxDelegationsPerRoot) {
		t.Fatalf("Delegations lists %d, first %+v; want %d from %+v", len(all), all[0].Delegation, agentcredential.MaxDelegationsPerRoot, delegation(1))
	}
	if got, ok := h.Delegation(childKey); !ok || got != delegation(agentcredential.MaxDelegationsPerRoot) {
		t.Fatalf("Delegation = %+v, %v; want the latest", got, ok)
	}
}

// movedTree is a holder with a main agent and a child and grandchild helper,
// each delegated from its parent's run, all registered in bs_1.
type movedTree struct {
	h                  *agentcredential.Holder
	b                  *fakeBroker
	childKey, grandKey agentcredential.Key
	childDlg, grandDlg agentcredential.Delegation
}

func newMovedTree(t *testing.T) movedTree {
	t.Helper()
	h, b, _ := newHolder(t)
	mustEnsure(t, h, "bs_1")
	main, _ := h.Held(key)
	f := movedTree{
		h: h, b: b,
		childKey: agentcredential.Key{RootSessionID: "root", AgentSessionID: "child"},
		grandKey: agentcredential.Key{RootSessionID: "root", AgentSessionID: "grand"},
		childDlg: agentcredential.Delegation{ID: "dlg_c", Run: agentcredential.Run{ID: "run_c"}, DelegatingRun: "run_root", DelegatingAgentSession: "root"},
		grandDlg: agentcredential.Delegation{ID: "dlg_g", Run: agentcredential.Run{ID: "run_g"}, DelegatingRun: "run_c", DelegatingAgentSession: "child"},
	}
	if err := h.Ensure(t.Context(), f.childKey, helperRequest("bs_1", "child", "root", main.Agent), admitAll); err != nil {
		t.Fatalf("Ensure child: %v", err)
	}
	child, _ := h.Held(f.childKey)
	if err := h.Ensure(t.Context(), f.grandKey, helperRequest("bs_1", "grand", "child", child.Agent), admitAll); err != nil {
		t.Fatalf("Ensure grandchild: %v", err)
	}
	for k, d := range map[agentcredential.Key]agentcredential.Delegation{f.childKey: f.childDlg, f.grandKey: f.grandDlg} {
		if err := h.SetDelegation(k, d); err != nil {
			t.Fatalf("SetDelegation: %v", err)
		}
	}
	return f
}

// wantReportedInOrder fails t unless each of want was reported into
// brokerSession, each after the one before it.
func wantReportedInOrder(t *testing.T, b *fakeBroker, brokerSession string, want ...string) {
	t.Helper()
	got := b.reportsTo(brokerSession)
	at := -1
	for _, id := range want {
		i := slices.Index(got[at+1:], id)
		if i < 0 {
			t.Fatalf("reports into %s = %v, want %v in that order", brokerSession, got, want)
		}
		at += i + 1
	}
}

// TestHolderReportsAMovedHelpersDelegationChain pins that a helper moved to
// its root's new broker session has its current delegation reported there,
// after the delegations its run descends from, so the new session knows
// every run the helper's calls depend on.
func TestHolderReportsAMovedHelpersDelegationChain(t *testing.T) {
	f := newMovedTree(t)
	mustEnsure(t, f.h, "bs_2")
	if got := f.b.reportsTo("bs_2"); len(got) != 0 {
		t.Fatalf("reports before any helper moved = %v", got)
	}
	cred, err := f.h.Credential(t.Context(), f.grandKey, false)
	if err != nil || cred.BrokerSessionID != "bs_2" {
		t.Fatalf("grandchild lookup = %+v, %v; want it moved to bs_2", cred, err)
	}
	wantReportedInOrder(t, f.b, "bs_2", "dlg_c", "dlg_g")
	if got := f.b.reportsTo("bs_1"); len(got) != 0 {
		t.Fatalf("reports into the old session = %v", got)
	}
}

// TestHolderMovesAgainWhenTheReportFails pins that a move whose report fails
// is not stored, so the helper's next lookup moves it and reports again,
// rather than leaving it in the new session with a delegation the session
// never saw.
func TestHolderMovesAgainWhenTheReportFails(t *testing.T) {
	f := newMovedTree(t)
	mustEnsure(t, f.h, "bs_2")
	f.b.setReportErr(identitybroker.NewError(identitybroker.CodeUnavailable, "", "down"))
	if _, err := f.h.Credential(t.Context(), f.childKey, false); err == nil {
		t.Fatal("a move whose report failed succeeded")
	}
	if held, _ := f.h.Held(f.childKey); held.BrokerSessionID != "bs_1" {
		t.Fatalf("held in %s after a failed report, want bs_1", held.BrokerSessionID)
	}
	f.b.setReportErr(nil)
	if cred, err := f.h.Credential(t.Context(), f.childKey, false); err != nil || cred.BrokerSessionID != "bs_2" {
		t.Fatalf("lookup after the broker recovered = %+v, %v; want it moved", cred, err)
	}
	wantReportedInOrder(t, f.b, "bs_2", "dlg_c")
}

// TestHolderReportsADelegationSetDuringAMove pins that a delegation set
// after its helper's move reported the previous one, and before the move was
// stored, is reported to the new session too: the caller that set it
// reported it to the session the helper was leaving.
func TestHolderReportsADelegationSetDuringAMove(t *testing.T) {
	f := newMovedTree(t)
	mustEnsure(t, f.h, "bs_2")
	next := agentcredential.Delegation{ID: "dlg_c2", Run: agentcredential.Run{ID: "run_c2"}, DelegatingRun: "run_root2", DelegatingAgentSession: "root"}
	var once sync.Once
	f.b.mu.Lock()
	f.b.onReport = func(req identitybroker.ReportDelegationRequest) {
		if req.SessionID == "bs_2" && req.DelegationID == "dlg_c" {
			once.Do(func() {
				if err := f.h.SetDelegation(f.childKey, next); err != nil {
					t.Error(err)
				}
			})
		}
	}
	f.b.mu.Unlock()
	if _, err := f.h.Credential(t.Context(), f.childKey, false); err != nil {
		t.Fatalf("move: %v", err)
	}
	wantReportedInOrder(t, f.b, "bs_2", "dlg_c", "dlg_c2")
}

// TestHolderReportsAnOlderParentRun pins that a helper delegated from an
// earlier run of its parent, not the parent's current one, has that earlier
// delegation reported first, so the new session knows the run it was
// delegated from.
func TestHolderReportsAnOlderParentRun(t *testing.T) {
	f := newMovedTree(t)
	// The child is delegated again; the grandchild still descends from its
	// first run.
	if err := f.h.SetDelegation(f.childKey, agentcredential.Delegation{ID: "dlg_c2", Run: agentcredential.Run{ID: "run_c2"}, DelegatingRun: "run_root2", DelegatingAgentSession: "root"}); err != nil {
		t.Fatal(err)
	}
	mustEnsure(t, f.h, "bs_2")
	if _, err := f.h.Credential(t.Context(), f.grandKey, false); err != nil {
		t.Fatalf("grandchild lookup: %v", err)
	}
	wantReportedInOrder(t, f.b, "bs_2", "dlg_c", "dlg_g")
}

// TestHolderReportDelegationAgain pins that ReportDelegationAgain reports a
// helper's chain into the session its credential is held in now.
func TestHolderReportDelegationAgain(t *testing.T) {
	f := newMovedTree(t)
	if err := f.h.ReportDelegationAgain(t.Context(), f.grandKey); err != nil {
		t.Fatal(err)
	}
	wantReportedInOrder(t, f.b, "bs_1", "dlg_c", "dlg_g")
	if err := f.h.ReportDelegationAgain(t.Context(), agentcredential.Key{RootSessionID: "root", AgentSessionID: "nobody"}); !errors.Is(err, agentcredential.ErrNoCredential) {
		t.Fatalf("ReportDelegationAgain with nothing held = %v, want ErrNoCredential", err)
	}
}
