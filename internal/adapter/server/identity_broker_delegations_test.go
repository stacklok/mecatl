package server_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

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

	t.Run("a helper resumed by a parent with no run keeps no earlier delegation", func(t *testing.T) {
		f := newHelperFixture(t)
		child := newSubagentSession(t, "child-1", f.root.ID)
		if err := f.svc.RegisterHelper(f.runContext(t, "run_first"), child, port.ChildSessionInfo{}); err != nil {
			t.Fatalf("RegisterHelper: %v", err)
		}
		before := len(f.broker.delegationRequests())
		if err := f.svc.RegisterResumedHelper(f.rootContext(t), child, port.ChildSessionInfo{}); err != nil {
			t.Fatalf("RegisterResumedHelper: %v", err)
		}
		if n := len(f.broker.delegationRequests()); n != before {
			t.Fatalf("a resume with no run reported %d delegations", n-before)
		}
		if d, ok := f.svc.IdentityDelegationForTest(f.root.ID, "child-1"); ok {
			t.Fatalf("the resumed helper still acts under %+v, want no run so its gateway calls fail closed", d)
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
// root's broker session reopens, its run entry moves each helper there and
// reports its current delegation again, parents before the helpers they
// delegated to. A delegation can be reported more than once, as part of a
// later helper's chain; the broker accepts a repeat.
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
		if r.SessionID == reopened.ID && !slices.Contains(again, r) {
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

// TestAHelperThatFailsToMoveReportsItsDelegationWhenItMoves pins that a
// helper the reopen could not move, moved later by the MCP broker's lookup
// before a gateway call, has its delegation reported into the new session
// then, so the call's run checks out there.
func TestAHelperThatFailsToMoveReportsItsDelegationWhenItMoves(t *testing.T) {
	f := newHelperFixture(t)
	if err := f.svc.RegisterHelper(f.runContext(t, "run_root"), newSubagentSession(t, "child-1", f.root.ID), port.ChildSessionInfo{}); err != nil {
		t.Fatalf("RegisterHelper: %v", err)
	}
	delegation, ok := f.svc.IdentityDelegationForTest(f.root.ID, "child-1")
	if !ok {
		t.Fatal("the helper holds no delegation")
	}
	if err := f.broker.inner.CloseSession(t.Context(), identitybroker.CloseSessionRequest{SessionID: f.main.SessionID, Reason: identitybroker.ReasonSessionClosed}); err != nil {
		t.Fatal(err)
	}
	f.broker.set(func(b *recordingBroker) { b.helperUnavailable = true })
	if err := runToEnd(aliceContext(t), t, f.svc, f.root.ID); err != nil {
		t.Fatalf("run: %v", err)
	}
	reopened := wantBrokerSession(t, f.broker, f.root)
	reportedTo := func() bool {
		for _, r := range f.broker.delegationRequests() {
			if r.SessionID == reopened.ID && r.DelegationID == delegation.ID {
				return true
			}
		}
		return false
	}
	if reportedTo() {
		t.Fatal("the delegation of a helper that did not move was reported")
	}

	f.broker.set(func(b *recordingBroker) { b.helperUnavailable = false })
	cred, err := f.svc.IdentityAgentLookupForTest(t.Context(), f.root.ID, "child-1")
	if err != nil || cred.BrokerSessionID != reopened.ID {
		t.Fatalf("lookup = %+v, %v; want the helper moved to %s", cred, err, reopened.ID)
	}
	if !reportedTo() {
		t.Fatal("the moved helper's delegation was not reported into the new session")
	}
	// The broker recorded it: a changed repeat is refused.
	var repeat identitybroker.ReportDelegationRequest
	for _, r := range f.broker.delegationRequests() {
		if r.SessionID == reopened.ID && r.DelegationID == delegation.ID {
			repeat = r
		}
	}
	repeat.ReceivingRun = "run_other"
	if err := f.broker.inner.ReportDelegation(t.Context(), repeat); !errors.Is(err, identitybroker.NewError(identitybroker.CodeAlreadyExists, "", "")) {
		t.Fatalf("a changed repeat = %v, want ALREADY_EXISTS", err)
	}
}

// TestTeamMemberDelegationsMarkTheStandInParent pins that a member of a team
// created over the API, whose session records no parent, is reported with the
// root's main agent as a stand-in, both when it registers and in its
// delegation, and that a delegation reported again into a reopened broker
// session keeps the mark. A subagent's report carries none.
func TestTeamMemberDelegationsMarkTheStandInParent(t *testing.T) {
	f := newHelperFixture(t)
	ctx := f.runContext(t, "run_team")
	member, err := session.NewTeamMember("member-1", session.ModeDefault, helperRef(), session.Limits{}, time.Unix(1, 0), "team-1", "lead", "", "")
	if err != nil {
		t.Fatal(err)
	}
	labelAlice(t, member)
	if err := f.svc.RegisterHelper(ctx, member, port.ChildSessionInfo{}); err != nil {
		t.Fatalf("RegisterHelper(member): %v", err)
	}
	if reg := lastRegistration(t, f.broker); !reg.ParentStandIn {
		t.Fatalf("member registration = %+v, want the main agent as a stand-in", reg)
	}
	if d := lastDelegation(t, f.broker); !d.StandIn || d.DelegatingRun != "run_team" {
		t.Fatalf("member delegation = %+v, want the team run with a stand-in", d)
	}
	if err := f.svc.RegisterHelper(ctx, newSubagentSession(t, "child-1", f.root.ID), port.ChildSessionInfo{}); err != nil {
		t.Fatalf("RegisterHelper(child): %v", err)
	}
	if reg, d := lastRegistration(t, f.broker), lastDelegation(t, f.broker); reg.ParentStandIn || d.StandIn {
		t.Fatalf("subagent registration %+v and delegation %+v, want no stand-in", reg, d)
	}

	first := f.broker.delegationRequests()
	if err := f.broker.inner.CloseSession(t.Context(), identitybroker.CloseSessionRequest{SessionID: f.main.SessionID, Reason: identitybroker.ReasonSessionClosed}); err != nil {
		t.Fatal(err)
	}
	if err := runToEnd(aliceContext(t), t, f.svc, f.root.ID); err != nil {
		t.Fatalf("run: %v", err)
	}
	reopened := wantBrokerSession(t, f.broker, f.root)
	for _, r := range f.broker.delegationRequests()[len(first):] {
		if r.SessionID == reopened.ID && r.ReceivingRun == first[0].ReceivingRun {
			if !r.StandIn {
				t.Fatalf("member delegation reported again = %+v, want the stand-in kept", r)
			}
			return
		}
	}
	t.Fatal("the member's delegation was not reported into the reopened session")
}
