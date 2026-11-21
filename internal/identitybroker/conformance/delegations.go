package conformance

import (
	"context"
	"testing"

	"github.com/stacklok/mecatl/internal/identitybroker"
)

// The runs and delegations the delegation cases name.
const (
	delegatingRun   = "R41"
	receivingRun    = "R42"
	otherRun        = "R43"
	otherDelegation = "D43"
)

// delegationFixture is an open session with its main agent and two helpers:
// reviewer, the main agent's child, and checker, the reviewer's child.
type delegationFixture struct {
	svc                     identitybroker.Service
	s                       identitybroker.Session
	main, reviewer, checker identitybroker.AgentRef
}

func newDelegationFixture(t *testing.T, factory serviceFactory) delegationFixture {
	t.Helper()
	svc := factory(t)
	s := mustOpen(t, svc, openRequest("hostS42", alice))
	main := mustRegister(t, svc, registerRequest(s)).Agent
	reviewer := mustRegister(t, svc, childRequest(s, main, "reviewer")).Agent
	checker := childRequest(s, reviewer, "checker")
	checker.ParentHostSessionID = s.HostSessionID + "-reviewer"
	return delegationFixture{svc: svc, s: s, main: main, reviewer: reviewer, checker: mustRegister(t, svc, checker).Agent}
}

// report is the main agent's run R41 delegating D42 to the reviewer's run
// R42, for alice.
func (f delegationFixture) report() identitybroker.ReportDelegationRequest {
	return identitybroker.ReportDelegationRequest{
		SessionID: f.s.ID, DelegationID: "D42",
		DelegatingRun: delegatingRun, DelegatingAgent: f.main,
		ReceivingRun: receivingRun, ReceivingAgent: f.reviewer,
		Owner: alice,
	}
}

func (f delegationFixture) mustReport(t *testing.T, req identitybroker.ReportDelegationRequest) {
	t.Helper()
	if err := f.svc.ReportDelegation(context.Background(), req); err != nil {
		t.Fatalf("ReportDelegation(%s): %v", req.DelegationID, err)
	}
}

func (f delegationFixture) wantRefused(t *testing.T, call string, req identitybroker.ReportDelegationRequest) {
	t.Helper()
	err := f.svc.ReportDelegation(context.Background(), req)
	wantError(t, call, err, identitybroker.CodePermissionDenied, identitybroker.ReasonDenied)
}

// runDelegations covers the design's "Delegation report" checks, in order.
func runDelegations(t *testing.T, factory serviceFactory) {
	ctx := context.Background()

	t.Run("a root run delegates to a helper", func(t *testing.T) {
		f := newDelegationFixture(t, factory)
		f.mustReport(t, f.report())
	})

	t.Run("a repeated report with the same content succeeds", func(t *testing.T) {
		f := newDelegationFixture(t, factory)
		f.mustReport(t, f.report())
		f.mustReport(t, f.report())
	})

	t.Run("a repeated report with different content already exists", func(t *testing.T) {
		for name, mutate := range map[string]func(*identitybroker.ReportDelegationRequest){
			"another receiving run":   func(r *identitybroker.ReportDelegationRequest) { r.ReceivingRun = otherRun },
			"another receiving agent": func(r *identitybroker.ReportDelegationRequest) { r.ReceivingAgent = r.DelegatingAgent },
		} {
			t.Run(name, func(t *testing.T) {
				f := newDelegationFixture(t, factory)
				f.mustReport(t, f.report())
				req := f.report()
				mutate(&req)
				err := f.svc.ReportDelegation(ctx, req)
				wantError(t, "ReportDelegation with different content", err, identitybroker.CodeAlreadyExists, "")
			})
		}
	})

	t.Run("a delegated run delegates again", func(t *testing.T) {
		f := newDelegationFixture(t, factory)
		f.mustReport(t, f.report())
		f.mustReport(t, identitybroker.ReportDelegationRequest{
			SessionID: f.s.ID, DelegationID: otherDelegation,
			DelegatingRun: receivingRun, DelegatingAgent: f.reviewer,
			ReceivingRun: otherRun, ReceivingAgent: f.checker, Owner: alice,
		})
	})

	t.Run("a root run delegates to several helpers", func(t *testing.T) {
		f := newDelegationFixture(t, factory)
		f.mustReport(t, f.report())
		second := f.report()
		second.DelegationID, second.ReceivingRun, second.ReceivingAgent = otherDelegation, otherRun, f.checker
		f.mustReport(t, second)
	})

	t.Run("check 3: a delegating run bound to another agent is refused", func(t *testing.T) {
		f := newDelegationFixture(t, factory)
		f.mustReport(t, f.report())
		req := identitybroker.ReportDelegationRequest{
			SessionID: f.s.ID, DelegationID: otherDelegation,
			DelegatingRun: delegatingRun, DelegatingAgent: f.reviewer,
			ReceivingRun: otherRun, ReceivingAgent: f.checker, Owner: alice,
		}
		f.wantRefused(t, "ReportDelegation from a run bound to another agent", req)
	})

	t.Run("check 3: an unseen delegating run of a child instance is refused", func(t *testing.T) {
		f := newDelegationFixture(t, factory)
		req := identitybroker.ReportDelegationRequest{
			SessionID: f.s.ID, DelegationID: otherDelegation,
			DelegatingRun: "R50", DelegatingAgent: f.reviewer,
			ReceivingRun: "R51", ReceivingAgent: f.checker, Owner: alice,
		}
		f.wantRefused(t, "ReportDelegation from an unseen run of a child instance", req)
		// Nothing was recorded: the root run that should have delegated
		// still can, to the same receiving run.
		req.DelegatingRun, req.DelegatingAgent = delegatingRun, f.main
		f.mustReport(t, req)
	})

	t.Run("check 3: an unregistered delegating agent is refused", func(t *testing.T) {
		f := newDelegationFixture(t, factory)
		req := f.report()
		req.DelegatingAgent = identitybroker.AgentRef{Subject: "spiffe://agents.example.com/agent/ag_aaaaaaaaaaaaaaaaaaaaaaaaaa"}
		f.wantRefused(t, "ReportDelegation from an unregistered agent", req)
	})

	t.Run("check 3: another session's root instance is refused", func(t *testing.T) {
		f := newDelegationFixture(t, factory)
		other := mustOpen(t, f.svc, openRequest("hostS43", alice))
		otherMain := mustRegister(t, f.svc, registerRequest(other)).Agent
		req := f.report()
		req.DelegatingAgent = otherMain
		f.wantRefused(t, "ReportDelegation from another session's main agent", req)
	})

	t.Run("check 4: an unregistered receiving agent is refused", func(t *testing.T) {
		f := newDelegationFixture(t, factory)
		req := f.report()
		req.ReceivingAgent = identitybroker.AgentRef{Subject: "spiffe://agents.example.com/agent/ag_aaaaaaaaaaaaaaaaaaaaaaaaaa"}
		f.wantRefused(t, "ReportDelegation to an unregistered agent", req)
		f.mustReport(t, f.report())
	})

	t.Run("check 4: another session's agent cannot receive", func(t *testing.T) {
		f := newDelegationFixture(t, factory)
		other := mustOpen(t, f.svc, openRequest("hostS43", alice))
		req := f.report()
		req.ReceivingAgent = mustRegister(t, f.svc, registerRequest(other)).Agent
		f.wantRefused(t, "ReportDelegation to another session's agent", req)
	})

	t.Run("check 5: the owner cannot change on the way down", func(t *testing.T) {
		f := newDelegationFixture(t, factory)
		f.mustReport(t, f.report())
		req := identitybroker.ReportDelegationRequest{
			SessionID: f.s.ID, DelegationID: otherDelegation,
			DelegatingRun: receivingRun, DelegatingAgent: f.reviewer,
			ReceivingRun: otherRun, ReceivingAgent: f.checker, Owner: bob,
		}
		f.wantRefused(t, "ReportDelegation changing the owner", req)
		// Nor from a root run the first report recorded with alice.
		second := f.report()
		second.DelegationID, second.ReceivingRun, second.Owner = "D44", "R44", bob
		f.wantRefused(t, "ReportDelegation changing a root run's owner", second)
	})

	t.Run("a receiving run cannot be delegated twice", func(t *testing.T) {
		f := newDelegationFixture(t, factory)
		f.mustReport(t, f.report())
		req := f.report()
		req.DelegationID = otherDelegation
		f.wantRefused(t, "ReportDelegation to an already recorded run", req)
		// Nor can a receiving run take over a root run.
		req = f.report()
		req.DelegationID, req.ReceivingRun = "D44", delegatingRun
		req.DelegatingRun = "R40"
		f.wantRefused(t, "ReportDelegation to a recorded root run", req)
	})

	t.Run("delegations are kept per session", func(t *testing.T) {
		f := newDelegationFixture(t, factory)
		f.mustReport(t, f.report())
		other := mustOpen(t, f.svc, openRequest("hostS43", alice))
		otherMain := mustRegister(t, f.svc, registerRequest(other)).Agent
		otherReviewer := mustRegister(t, f.svc, childRequest(other, otherMain, "other-reviewer")).Agent
		req := f.report()
		req.SessionID, req.DelegatingAgent, req.ReceivingAgent = other.ID, otherMain, otherReviewer
		f.mustReport(t, req)
	})

	t.Run("a closed session refuses with its reason", func(t *testing.T) {
		f := newDelegationFixture(t, factory)
		f.mustReport(t, f.report())
		mustClose(t, f.svc, f.s.ID)
		err := f.svc.ReportDelegation(ctx, f.report())
		wantError(t, "ReportDelegation in a closed session", err, identitybroker.CodeFailedPrecondition, identitybroker.ReasonSessionClosed)
	})

	t.Run("an invalid report is refused", func(t *testing.T) {
		f := newDelegationFixture(t, factory)
		for name, mutate := range map[string]func(*identitybroker.ReportDelegationRequest){
			"no broker session":          func(r *identitybroker.ReportDelegationRequest) { r.SessionID = "" },
			"no delegation":              func(r *identitybroker.ReportDelegationRequest) { r.DelegationID = "" },
			"a spaced delegation":        func(r *identitybroker.ReportDelegationRequest) { r.DelegationID = "D 42" },
			"no delegating run":          func(r *identitybroker.ReportDelegationRequest) { r.DelegatingRun = "" },
			"no receiving run":           func(r *identitybroker.ReportDelegationRequest) { r.ReceivingRun = "" },
			"no delegating agent":        func(r *identitybroker.ReportDelegationRequest) { r.DelegatingAgent = identitybroker.AgentRef{} },
			"no receiving agent":         func(r *identitybroker.ReportDelegationRequest) { r.ReceivingAgent = identitybroker.AgentRef{} },
			"no owner":                   func(r *identitybroker.ReportDelegationRequest) { r.Owner = identitybroker.Owner{} },
			"a run delegating to itself": func(r *identitybroker.ReportDelegationRequest) { r.ReceivingRun = r.DelegatingRun },
		} {
			req := f.report()
			mutate(&req)
			err := f.svc.ReportDelegation(ctx, req)
			wantError(t, "ReportDelegation with "+name, err, identitybroker.CodeInvalidArgument, "")
		}
	})
}
