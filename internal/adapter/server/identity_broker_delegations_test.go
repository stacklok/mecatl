package server_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/server"
	"github.com/stacklok/mecatl/internal/agentcredential"
	"github.com/stacklok/mecatl/internal/identitybroker"
)

// runContext is f.rootContext during the main agent's run runID, owned by
// the root's owner, as a server run entry hands the engine.
func (f helperFixture) runContext(t *testing.T, runID string) context.Context {
	return agentcredential.WithRun(f.rootContext(t), agentcredential.Run{ID: runID, Owner: f.main.Owner})
}

func lastDelegation(t *testing.T, broker *recordingBroker) identitybroker.ReportDelegationRequest {
	t.Helper()
	reports := broker.delegationRequests()
	if len(reports) == 0 {
		t.Fatal("no delegation reported")
	}
	return reports[len(reports)-1]
}

// wantDelegation fails t unless report delegates from run delegatingRun of
// delegating to a new run of helper in f's broker session, for the root's
// owner, and the service holds it as the helper's current delegation.
func (f helperFixture) wantDelegation(t *testing.T, report identitybroker.ReportDelegationRequest, delegatingRun string, delegating identitybroker.AgentRef, helper session.SessionID) {
	t.Helper()
	helperAgent, _ := f.svc.HeldIdentityAgentForTest(f.root.ID, helper)
	if report.SessionID != f.main.SessionID || report.DelegatingRun != delegatingRun || report.DelegatingAgent != delegating ||
		report.ReceivingAgent != helperAgent || report.Owner != f.main.Owner ||
		!strings.HasPrefix(report.DelegationID, "dlg_") || !strings.HasPrefix(report.ReceivingRun, "run_") {
		t.Fatalf("delegation = %+v, want run %s of %+v delegating to a new run_ of %+v in %s for %+v",
			report, delegatingRun, delegating, helperAgent, f.main.SessionID, f.main.Owner)
	}
	held, ok := f.svc.IdentityDelegationForTest(f.root.ID, helper)
	want := agentcredential.Delegation{
		ID: report.DelegationID, Run: agentcredential.Run{ID: report.ReceivingRun, Owner: report.Owner},
		DelegatingRun: delegatingRun,
	}
	if !ok || held.ID != want.ID || held.Run != want.Run || held.DelegatingRun != want.DelegatingRun {
		t.Fatalf("held delegation = %+v, %v; want %+v", held, ok, want)
	}
}

// TestRegisterHelperReportsItsDelegation pins that a helper's run is
// delegated from its creating agent's current run: the main agent's run on
// the context, or a parent helper's own delegated run.
func TestRegisterHelperReportsItsDelegation(t *testing.T) {
	t.Run("from the main agent's run", func(t *testing.T) {
		f := newHelperFixture(t)
		if err := f.svc.RegisterHelper(f.runContext(t, "run_root"), newSubagentSession(t, "child-1", f.root.ID), port.ChildSessionInfo{}); err != nil {
			t.Fatalf("RegisterHelper: %v", err)
		}
		f.wantDelegation(t, lastDelegation(t, f.broker), "run_root", f.mainAgent, "child-1")
	})

	t.Run("from a parent helper's run", func(t *testing.T) {
		f := newHelperFixture(t)
		ctx := f.runContext(t, "run_root")
		if err := f.svc.RegisterHelper(ctx, newSubagentSession(t, "child-1", f.root.ID), port.ChildSessionInfo{}); err != nil {
			t.Fatalf("RegisterHelper(child): %v", err)
		}
		child, _ := f.svc.HeldIdentityAgentForTest(f.root.ID, "child-1")
		childRun, _ := f.svc.IdentityDelegationForTest(f.root.ID, "child-1")
		// The grandchild's context still carries the main agent's run; its
		// delegation comes from the child's run, not that one.
		if err := f.svc.RegisterHelper(ctx, newSubagentSession(t, "grandchild-1", "child-1"), port.ChildSessionInfo{}); err != nil {
			t.Fatalf("RegisterHelper(grandchild): %v", err)
		}
		f.wantDelegation(t, lastDelegation(t, f.broker), childRun.Run.ID, child, "grandchild-1")
	})

	t.Run("a resumed helper gets a new delegation from the resuming run", func(t *testing.T) {
		f := newHelperFixture(t)
		child := newSubagentSession(t, "child-1", f.root.ID)
		if err := f.svc.RegisterHelper(f.runContext(t, "run_first"), child, port.ChildSessionInfo{}); err != nil {
			t.Fatalf("RegisterHelper: %v", err)
		}
		first := lastDelegation(t, f.broker)
		if err := f.svc.RegisterResumedHelper(f.runContext(t, "run_second"), child, port.ChildSessionInfo{}); err != nil {
			t.Fatalf("RegisterResumedHelper: %v", err)
		}
		second := lastDelegation(t, f.broker)
		f.wantDelegation(t, second, "run_second", f.mainAgent, "child-1")
		if second.DelegationID == first.DelegationID || second.ReceivingRun == first.ReceivingRun {
			t.Fatalf("the resumed helper reused delegation %s and run %s", first.DelegationID, first.ReceivingRun)
		}
	})

	t.Run("a parent with no run reports nothing and the helper still starts", func(t *testing.T) {
		f := newHelperFixture(t)
		if err := f.svc.RegisterHelper(f.rootContext(t), newSubagentSession(t, "child-1", f.root.ID), port.ChildSessionInfo{}); err != nil {
			t.Fatalf("RegisterHelper: %v", err)
		}
		if _, ok := f.svc.HeldIdentityAgentForTest(f.root.ID, "child-1"); !ok {
			t.Fatal("the helper did not register")
		}
		if reports := f.broker.delegationRequests(); len(reports) != 0 {
			t.Fatalf("delegation reports = %+v, want none from a parent with no run", reports)
		}
		if _, ok := f.svc.IdentityDelegationForTest(f.root.ID, "child-1"); ok {
			t.Fatal("a helper whose parent had no run holds a delegation")
		}
	})
}

// TestRegisterHelperDelegationRefusals pins that a failed delegation report
// stops the helper as a failed registration does, holding no delegation.
func TestRegisterHelperDelegationRefusals(t *testing.T) {
	for name, tc := range map[string]struct {
		err  error
		want error
	}{
		"unavailable":    {identitybroker.NewError(identitybroker.CodeUnavailable, "", "down"), server.ErrIdentityBrokerUnavailable},
		"session closed": {identitybroker.ReasonError(identitybroker.ReasonSessionClosed, "closed"), server.ErrIdentityBrokerUnavailable},
		"denied":         {identitybroker.ReasonError(identitybroker.ReasonDenied, "denied"), server.ErrInternal},
	} {
		t.Run(name, func(t *testing.T) {
			f := newHelperFixture(t)
			f.broker.set(func(b *recordingBroker) { b.reportErr = tc.err })
			err := f.svc.RegisterHelper(f.runContext(t, "run_root"), newSubagentSession(t, "child-1", f.root.ID), port.ChildSessionInfo{})
			if !errors.Is(err, tc.want) {
				t.Fatalf("RegisterHelper = %v, want %v", err, tc.want)
			}
			if _, ok := f.svc.IdentityDelegationForTest(f.root.ID, "child-1"); ok {
				t.Fatal("a refused delegation is held")
			}
		})
	}
}

// TestDelegationsAreReportedAgainInAReopenedBrokerSession pins that once a
// root's broker session reopens, its run entry reports every held delegation
// again into the new session, parents before the helpers they delegated to.
func TestDelegationsAreReportedAgainInAReopenedBrokerSession(t *testing.T) {
	f := newHelperFixture(t)
	ctx := f.runContext(t, "run_root")
	if err := f.svc.RegisterHelper(ctx, newSubagentSession(t, "child-1", f.root.ID), port.ChildSessionInfo{}); err != nil {
		t.Fatalf("RegisterHelper(child): %v", err)
	}
	if err := f.svc.RegisterHelper(ctx, newSubagentSession(t, "grandchild-1", "child-1"), port.ChildSessionInfo{}); err != nil {
		t.Fatalf("RegisterHelper(grandchild): %v", err)
	}
	first := f.broker.delegationRequests()
	if err := f.broker.inner.CloseSession(t.Context(), identitybroker.CloseSessionRequest{SessionID: f.main.SessionID, Reason: identitybroker.ReasonSessionClosed}); err != nil {
		t.Fatal(err)
	}
	if err := runToEnd(aliceContext(t), t, f.svc, f.root.ID); err != nil {
		t.Fatalf("run: %v", err)
	}
	reopened := wantBrokerSession(t, f.broker, f.root)
	var again []identitybroker.ReportDelegationRequest
	for _, r := range f.broker.delegationRequests()[len(first):] {
		if r.SessionID == reopened.ID {
			again = append(again, r)
		}
	}
	if len(again) != 2 {
		t.Fatalf("reports into the reopened session = %+v, want both delegations", again)
	}
	for i, want := range first {
		want.SessionID = reopened.ID
		if again[i] != want {
			t.Fatalf("report %d into the reopened session = %+v, want %+v", i, again[i], want)
		}
	}
	// The broker accepted them: the grandchild's delegating run, the child's,
	// is recorded in the new session, so a repeat succeeds and a different
	// report of the same delegation is refused.
	repeat := again[1]
	if err := f.broker.inner.ReportDelegation(t.Context(), repeat); err != nil {
		t.Fatalf("repeat of a reported delegation: %v", err)
	}
	repeat.ReceivingRun = "run_other"
	if err := f.broker.inner.ReportDelegation(t.Context(), repeat); !errors.Is(err, identitybroker.NewError(identitybroker.CodeAlreadyExists, "", "")) {
		t.Fatalf("a changed repeat = %v, want ALREADY_EXISTS", err)
	}
}
