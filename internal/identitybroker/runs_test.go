package identitybroker_test

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/stacklok/mecatl/internal/identitybroker"
	"github.com/stacklok/mecatl/internal/identitybroker/conformance"
)

// runFixture is a per-call fixture whose main agent has a registered child.
type runFixture struct {
	perCallFixture
	main, child identitybroker.AgentRef
	childCred   identitybroker.Token
}

func newRunFixture(t *testing.T) runFixture {
	t.Helper()
	f := newPerCall(t)
	main, err := f.b.RegisterAgent(context.Background(), testWorkload, mainAgent(f.s))
	if err != nil {
		t.Fatalf("RegisterAgent main: %v", err)
	}
	child, err := f.b.RegisterAgent(context.Background(), testWorkload, identitybroker.RegisterAgentRequest{
		SessionID: f.s.ID, HostSessionID: "hostS-child", Owner: testOwner,
		Incarnation: "inc_bbcdefghijklmnopqrstuvwxya", Definition: conformance.MainDefinition,
		Parent: main.Agent, ParentHostSessionID: f.s.HostSessionID,
	})
	if err != nil {
		t.Fatalf("RegisterAgent child: %v", err)
	}
	return runFixture{perCallFixture: f, main: main.Agent, child: child.Agent, childCred: child.Credential}
}

// delegate reports the main agent's run R42 delegating id to the child's run.
func (f runFixture) delegate(t *testing.T, id, run string) {
	t.Helper()
	err := f.b.ReportDelegation(context.Background(), testWorkload, identitybroker.ReportDelegationRequest{
		SessionID: f.s.ID, DelegationID: id, DelegatingRun: "R42", DelegatingAgent: f.main,
		ReceivingRun: run, ReceivingAgent: f.child, Owner: testOwner,
	})
	if err != nil {
		t.Fatalf("ReportDelegation(%s): %v", id, err)
	}
}

// childCall is a call by the child in run under delegation.
func (f runFixture) childCall(run, delegation string) identitybroker.AuthorizeCallRequest {
	req := callRequest(f.s.ID, f.childCred)
	req.RunID, req.DelegationID = run, delegation
	return req
}

// wantRunDenied fails t unless the call is refused at checks 4 or 5 with
// detail, recorded as a denial, before any exchange.
func (f runFixture) wantRunDenied(t *testing.T, req identitybroker.AuthorizeCallRequest, detail string) {
	t.Helper()
	exchanges, redemptions, decisions := len(f.issuer.requests()), len(f.as2.redeemed()), len(f.rec.Decisions())
	got, err := f.authorize(req)
	wantError(t, err, identitybroker.CodePermissionDenied, identitybroker.ReasonDenied, f.cred, f.childCred)
	wantDetail(t, err, detail)
	if !got.GatewayToken.IsZero() {
		t.Fatal("a refused call returned a gateway token")
	}
	f.wantCounts(t, exchanges, redemptions)
	d := f.rec.Decisions()
	if len(d) != decisions+1 {
		t.Fatalf("recorded %d decisions, want %d", len(d), decisions+1)
	}
	last := d[len(d)-1]
	if last.Reason != identitybroker.ReasonDenied || last.RunID != req.RunID || last.DelegationID != req.DelegationID || last.RunOwner != req.RunOwner {
		t.Fatalf("decision = %+v, want a denial naming run %s, delegation %q, and owner %+v", last, req.RunID, req.DelegationID, req.RunOwner)
	}
}

func TestAuthorizeCallRecordsTheRunInTheIDJAGRecord(t *testing.T) {
	f := newRunFixture(t)
	if _, err := f.authorize(callRequest(f.s.ID, f.cred)); err != nil {
		t.Fatalf("root run: %v", err)
	}
	f.delegate(t, "D1", "R43")
	if _, err := f.authorize(f.childCall("R43", "D1")); err != nil {
		t.Fatalf("delegated run: %v", err)
	}
	got := f.rec.IDJAGs()
	if len(got) != 2 {
		t.Fatalf("recorded %d ID-JAGs, want 2", len(got))
	}
	for i, want := range []struct{ run, delegation string }{{"R42", ""}, {"R43", "D1"}} {
		r := got[i]
		if r.RunID != want.run || r.DelegationID != want.delegation || r.RunOwner != testOwner || r.Owner != testOwner {
			t.Errorf("ID-JAG record %d = run %s, delegation %q, run owner %+v, owner %+v; want %s, %q, and %+v twice",
				i, r.RunID, r.DelegationID, r.RunOwner, r.Owner, want.run, want.delegation, testOwner)
		}
	}
}

func TestAuthorizeCallRunOwnerMustBeTheGrantOwner(t *testing.T) {
	f := newRunFixture(t)
	other := identitybroker.Owner{Issuer: testOwner.Issuer, Subject: "bob-117"}
	req := callRequest(f.s.ID, f.cred)
	req.RunOwner = other
	f.wantRunDenied(t, req, "owner of the grant")
	// The refused first call recorded nothing: the run's first good call
	// records it with the grant's owner.
	if _, err := f.authorize(callRequest(f.s.ID, f.cred)); err != nil {
		t.Fatalf("after a refused first call, AuthorizeCall: %v", err)
	}
	// From then on the run keeps that owner.
	f.wantRunDenied(t, req, "owner of the grant")
}

func TestAuthorizeCallRunOwnerIsCheckedAgainstTheVerifiedGrantNotTheReport(t *testing.T) {
	// The verified owner AS-1 reports differs from the grant's: the grant's
	// owner, the one the exchange uses, is the one a run must match.
	f := newRunFixture(t)
	identitybroker.SetVerifiedOwner(f.b, f.s.ID, identitybroker.Owner{Issuer: testOwner.Issuer, Subject: "alice-verified"})
	if _, err := f.authorize(callRequest(f.s.ID, f.cred)); err != nil {
		t.Fatalf("AuthorizeCall: %v", err)
	}
}

func TestAuthorizeCallNewRunWithoutADelegationNeedsARootInstance(t *testing.T) {
	f := newRunFixture(t)
	f.wantRunDenied(t, f.childCall("R50", ""), "root instance")
}

func TestAuthorizeCallRunIsBoundToItsAgent(t *testing.T) {
	f := newRunFixture(t)
	if _, err := f.authorize(callRequest(f.s.ID, f.cred)); err != nil {
		t.Fatalf("root run: %v", err)
	}
	f.wantRunDenied(t, f.childCall("R42", ""), "bound to another agent")

	f.delegate(t, "D1", "R43")
	main := callRequest(f.s.ID, f.cred)
	main.RunID, main.DelegationID = "R43", "D1"
	f.wantRunDenied(t, main, "bound to another agent")
}

func TestAuthorizeCallUnreportedDelegationIsDenied(t *testing.T) {
	f := newRunFixture(t)
	f.wantRunDenied(t, f.childCall("R43", "D9"), "not reported")
	// A delegation reported for another run starts no new run: one
	// delegation is one run.
	f.delegate(t, "D1", "R43")
	f.wantRunDenied(t, f.childCall("R99", "D1"), "not reported")
}

func TestAuthorizeCallDelegatedRunKeepsItsDelegationAndOwner(t *testing.T) {
	f := newRunFixture(t)
	f.delegate(t, "D1", "R43")
	f.delegate(t, "D2", "R44")
	if _, err := f.authorize(f.childCall("R43", "D1")); err != nil {
		t.Fatalf("delegated run: %v", err)
	}
	f.wantRunDenied(t, f.childCall("R43", "D2"), "another delegation")
	f.wantRunDenied(t, f.childCall("R43", ""), "another delegation")

	changed := f.childCall("R43", "D1")
	changed.RunOwner = identitybroker.Owner{Issuer: testOwner.Issuer, Subject: "bob-117"}
	f.wantRunDenied(t, changed, "owner of the grant")
}

func TestAuthorizeCallConcurrentFirstCallsKeepOneRun(t *testing.T) {
	f := newRunFixture(t)
	const calls = 8
	errs := make(chan error, calls)
	var wg sync.WaitGroup
	for i := range calls {
		wg.Go(func() {
			req := callRequest(f.s.ID, f.cred)
			req.OperationID = "op-" + string(rune('a'+i))
			_, err := f.authorize(req)
			errs <- err
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("a concurrent first call: %v", err)
		}
	}
	// One record, bound to the main agent.
	f.wantRunDenied(t, f.childCall("R42", ""), "bound to another agent")
}

// TestRunRecordsForgottenAtTheCap pins what the broker does once a session's
// run and delegation records reach their cap and the oldest are forgotten.
// A delegated run whose record was forgotten is recorded again from its
// delegation, for the delegation's receiving agent only; a run whose own
// delegation was forgotten can no longer delegate.
func TestRunRecordsForgottenAtTheCap(t *testing.T) {
	f := newRunFixture(t)
	f.delegate(t, "D1", "R43")
	report := func(delegation, delegatingRun string, delegating identitybroker.AgentRef, receivingRun string, receiving identitybroker.AgentRef) error {
		return f.b.ReportDelegation(context.Background(), testWorkload, identitybroker.ReportDelegationRequest{
			SessionID: f.s.ID, DelegationID: delegation, DelegatingRun: delegatingRun, DelegatingAgent: delegating,
			ReceivingRun: receivingRun, ReceivingAgent: receiving, Owner: testOwner,
		})
	}
	// Each report from an unseen root run records two runs and one
	// delegation, so R43's run record is forgotten while D1 is kept.
	for i := range identitybroker.MaxRunsPerSession/2 + 1 {
		if err := report(fmt.Sprintf("Dfill%d", i), fmt.Sprintf("Rroot%d", i), f.main, fmt.Sprintf("Rfill%d", i), f.child); err != nil {
			t.Fatalf("filling report %d: %v", i, err)
		}
	}
	main := callRequest(f.s.ID, f.cred)
	main.RunID, main.DelegationID = "R43", "D1"
	f.wantRunDenied(t, main, "not reported for this run and agent")
	// The forgotten run can still delegate: its kept delegation rebuilds it,
	// as a call's would.
	if err := report("D1a", "R43", f.child, "R49", f.main); err != nil {
		t.Fatalf("a delegation from a run whose record was forgotten: %v", err)
	}
	if _, err := f.authorize(f.childCall("R43", "D1")); err != nil {
		t.Fatalf("the delegated run after its record was forgotten: %v", err)
	}

	// R43 is now the newest run record. Delegations reported from one root
	// run forget D1 before R43.
	for i := range identitybroker.MaxRunsPerSession / 2 {
		if err := report(fmt.Sprintf("Dmore%d", i), "Rbase", f.main, fmt.Sprintf("Rmore%d", i), f.child); err != nil {
			t.Fatalf("report %d: %v", i, err)
		}
	}
	err := report("D2", "R43", f.child, "R50", f.main)
	wantError(t, err, identitybroker.CodePermissionDenied, identitybroker.ReasonDenied)
	wantDetail(t, err, "own delegation was not reported")
}

// TestDelegationsDoNotCrossBrokerSessions pins that a delegation reported in
// one broker session does not authorize a call in the session that replaces
// it: after a reopen, the helper's call naming it is refused until the
// delegation is reported in the new session.
func TestDelegationsDoNotCrossBrokerSessions(t *testing.T) {
	f := newRunFixture(t)
	f.delegate(t, "D1", "R43")
	ctx := context.Background()
	if err := f.b.CloseSession(ctx, testWorkload, identitybroker.CloseSessionRequest{SessionID: f.s.ID, Reason: identitybroker.ReasonSessionClosed}); err != nil {
		t.Fatal(err)
	}
	reopened, err := f.b.OpenSession(ctx, testWorkload, identitybroker.OpenSessionRequest{
		HostSessionID: openHost, Incarnation: openIncarnation, Owner: testOwner, AccessToken: unsignedToken(t, testOwner),
	})
	if err != nil || reopened.ID == f.s.ID || !reopened.HoldsRefreshToken {
		t.Fatalf("reopen = %+v, %v; want a new session holding a grant", reopened, err)
	}
	if _, err := f.b.RegisterAgent(ctx, testWorkload, mainAgent(reopened)); err != nil {
		t.Fatalf("RegisterAgent main: %v", err)
	}
	child, err := f.b.RegisterAgent(ctx, testWorkload, identitybroker.RegisterAgentRequest{
		SessionID: reopened.ID, HostSessionID: "hostS-child", Owner: testOwner,
		Incarnation: "inc_bbcdefghijklmnopqrstuvwxya", Definition: conformance.MainDefinition,
		Parent: f.main, ParentHostSessionID: f.s.HostSessionID,
	})
	if err != nil || child.Agent != f.child {
		t.Fatalf("RegisterAgent child = %+v, %v; want the same agent", child, err)
	}
	call := callRequest(reopened.ID, child.Credential)
	call.RunID, call.DelegationID = "R43", "D1"
	_, err = f.authorize(call)
	wantError(t, err, identitybroker.CodePermissionDenied, identitybroker.ReasonDenied, child.Credential)
	wantDetail(t, err, "not reported for this run and agent")

	if err := f.b.ReportDelegation(ctx, testWorkload, identitybroker.ReportDelegationRequest{
		SessionID: reopened.ID, DelegationID: "D1", DelegatingRun: "R42", DelegatingAgent: f.main,
		ReceivingRun: "R43", ReceivingAgent: f.child, Owner: testOwner,
	}); err != nil {
		t.Fatalf("ReportDelegation in the new session: %v", err)
	}
	call.OperationID = "op-after-report"
	if _, err := f.authorize(call); err != nil {
		t.Fatalf("the call once its delegation was reported in the new session: %v", err)
	}
}
