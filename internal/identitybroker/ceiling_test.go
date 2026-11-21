package identitybroker_test

import (
	"context"
	"testing"

	"github.com/stacklok/mecatl/internal/identitybroker"
	"github.com/stacklok/mecatl/internal/identitybroker/conformance"
)

// ceilingFixture is a per-call fixture with a chain of agents, each in a run
// delegated from its parent's: a code reviewer under the main agent (R42 to
// R43, D1), a deployer under the reviewer (R43 to R44, D2), and a built-in
// explorer under the deployer (R44 to R45, D3); and a tool-less agent under
// the main agent (R42 to R46, D4).
type ceilingFixture struct {
	perCallFixture
	main, reviewer, deployer, explorer, toolLess           identitybroker.AgentRef
	reviewerCred, deployerCred, explorerCred, toolLessCred identitybroker.Token
}

func newCeilingFixture(t *testing.T) ceilingFixture {
	t.Helper()
	f := ceilingFixture{perCallFixture: newPerCall(t)}
	main, err := f.b.RegisterAgent(context.Background(), testWorkload, mainAgent(f.s))
	if err != nil {
		t.Fatalf("RegisterAgent main: %v", err)
	}
	f.main = main.Agent
	register := func(host, incarnation string, def identitybroker.DefinitionRef, parent identitybroker.AgentRef, parentHost string) identitybroker.Registration {
		t.Helper()
		reg, err := f.b.RegisterAgent(context.Background(), testWorkload, identitybroker.RegisterAgentRequest{
			SessionID: f.s.ID, HostSessionID: host, Owner: testOwner, Incarnation: incarnation,
			Definition: def, Parent: parent, ParentHostSessionID: parentHost,
		})
		if err != nil {
			t.Fatalf("RegisterAgent %s: %v", def.Name, err)
		}
		return reg
	}
	reviewer := register("hostS-reviewer", "inc_cbcdefghijklmnopqrstuvwxya", conformance.ReviewerDefinition, f.main, f.s.HostSessionID)
	deployer := register("hostS-deployer", "inc_dbcdefghijklmnopqrstuvwxya", conformance.DeployerDefinition, reviewer.Agent, "hostS-reviewer")
	explorer := register("hostS-explorer", "inc_ebcdefghijklmnopqrstuvwxya", conformance.ExplorerDefinition, deployer.Agent, "hostS-deployer")
	// An empty ceiling still registers; its calls all deny.
	toolLess := register("hostS-tool-less", "inc_fbcdefghijklmnopqrstuvwxya", conformance.ToolLessDefinition, f.main, f.s.HostSessionID)
	f.reviewer, f.reviewerCred = reviewer.Agent, reviewer.Credential
	f.deployer, f.deployerCred = deployer.Agent, deployer.Credential
	f.explorer, f.explorerCred = explorer.Agent, explorer.Credential
	f.toolLess, f.toolLessCred = toolLess.Agent, toolLess.Credential
	for _, d := range []identitybroker.ReportDelegationRequest{
		{DelegationID: "D1", DelegatingRun: "R42", DelegatingAgent: f.main, ReceivingRun: "R43", ReceivingAgent: f.reviewer},
		{DelegationID: "D2", DelegatingRun: "R43", DelegatingAgent: f.reviewer, ReceivingRun: "R44", ReceivingAgent: f.deployer},
		{DelegationID: "D3", DelegatingRun: "R44", DelegatingAgent: f.deployer, ReceivingRun: "R45", ReceivingAgent: f.explorer},
		{DelegationID: "D4", DelegatingRun: "R42", DelegatingAgent: f.main, ReceivingRun: "R46", ReceivingAgent: f.toolLess},
	} {
		d.SessionID, d.Owner = f.s.ID, testOwner
		if err := f.b.ReportDelegation(context.Background(), testWorkload, d); err != nil {
			t.Fatalf("ReportDelegation(%s): %v", d.DelegationID, err)
		}
	}
	return f
}

// call is a github call of tool by the agent with cred, in run under
// delegation, as operation.
func (f ceilingFixture) call(cred identitybroker.Token, run, delegation, operation, tool string) identitybroker.AuthorizeCallRequest {
	req := callRequest(f.s.ID, cred)
	req.RunID, req.DelegationID, req.OperationID, req.Tool = run, delegation, operation, "github."+tool
	return req
}

// wantAllowed fails t unless req is authorized and its decision record names
// ceiling as the one it was checked against.
func (f ceilingFixture) wantAllowed(t *testing.T, req identitybroker.AuthorizeCallRequest, ceiling string) {
	t.Helper()
	if _, err := f.authorize(req); err != nil {
		t.Fatalf("AuthorizeCall(%s): %v", req.Tool, err)
	}
	d := f.rec.Decisions()
	if last := d[len(d)-1]; last.Outcome != identitybroker.OutcomeSuccess || last.Ceiling != ceiling {
		t.Fatalf("decision = %+v, want allowed under ceiling %q", last, ceiling)
	}
}

// wantCeilingDenied fails t unless req is refused at check 7 before any
// exchange, with a denial recorded naming ceiling.
func (f ceilingFixture) wantCeilingDenied(t *testing.T, req identitybroker.AuthorizeCallRequest, ceiling string) {
	t.Helper()
	exchanges, redemptions := len(f.issuer.requests()), len(f.as2.redeemed())
	_, err := f.authorize(req)
	wantError(t, err, identitybroker.CodePermissionDenied, identitybroker.ReasonDenied, f.cred, f.reviewerCred, f.deployerCred, f.explorerCred, f.toolLessCred)
	wantDetail(t, err, "tool limit")
	f.wantCounts(t, exchanges, redemptions)
	d := f.rec.Decisions()
	if last := d[len(d)-1]; last.Reason != identitybroker.ReasonDenied || last.Ceiling != ceiling || last.Tool != req.Tool {
		t.Fatalf("decision = %+v, want a denial of %s under ceiling %q", last, req.Tool, ceiling)
	}
}

// TestAuthorizeCallChecksTheEffectiveCeiling pins check 7 and the effective
// ceiling: the main agent's built-in ceiling grants every gateway server; a
// read-only reviewer's excludes merge; and a deployer started by the
// reviewer cannot merge, whatever its own definition allows, because its
// effective ceiling is its definition's narrowed by the reviewer's.
func TestAuthorizeCallChecksTheEffectiveCeiling(t *testing.T) {
	f := newCeilingFixture(t)
	const (
		full     = "calendar; github"
		narrowed = "calendar; github except " + conformance.ReviewerExcludedTool
	)
	f.wantAllowed(t, f.call(f.cred, "R42", "", "op-main", conformance.ReviewerExcludedTool), full)

	f.wantAllowed(t, f.call(f.reviewerCred, "R43", "D1", "op-reviewer-read", "pull_request_read"), narrowed)
	f.wantCeilingDenied(t, f.call(f.reviewerCred, "R43", "D1", "op-reviewer-merge", conformance.ReviewerExcludedTool), narrowed)

	f.wantAllowed(t, f.call(f.deployerCred, "R44", "D2", "op-deployer-read", "pull_request_read"), narrowed)
	f.wantCeilingDenied(t, f.call(f.deployerCred, "R44", "D2", "op-deployer-merge", conformance.ReviewerExcludedTool), narrowed)

	// The explorer's built-in ceiling grants everything, but it is narrowed
	// by the deployer's effective ceiling, not the deployer's definition's,
	// so the reviewer's exclusion carries down two levels.
	f.wantAllowed(t, f.call(f.explorerCred, "R45", "D3", "op-explorer-read", "pull_request_read"), narrowed)
	f.wantCeilingDenied(t, f.call(f.explorerCred, "R45", "D3", "op-explorer-merge", conformance.ReviewerExcludedTool), narrowed)

	// An agent whose ceiling is empty registered, and every call it makes
	// is refused.
	f.wantCeilingDenied(t, f.call(f.toolLessCred, "R46", "D4", "op-tool-less", "pull_request_read"), "none")
}

// TestAuthorizeCallToolMustBeNamedForItsServer pins that a call names the
// exact pair: a tool named for another server, or with no server, is an
// invalid request, refused before any check, so check 7 always sees a tool
// of the call's server.
func TestAuthorizeCallToolMustBeNamedForItsServer(t *testing.T) {
	f := newCeilingFixture(t)
	for _, tool := range []string{"pull_request_read", "calendar.pull_request_read", "github.", "GitHub.pull_request_read", "github_pull_request_read"} {
		req := callRequest(f.s.ID, f.cred)
		req.OperationID, req.Tool = "op-"+tool, tool
		_, err := f.authorize(req)
		wantError(t, err, identitybroker.CodeInvalidArgument, "", f.cred)
	}
}

// TestAuthorizeCallGatewayRouteIsCheckedBeforeTheCeiling pins the design's
// order: a server no gateway routes is refused at check 6, before the
// ceiling is consulted.
func TestAuthorizeCallGatewayRouteIsCheckedBeforeTheCeiling(t *testing.T) {
	f := newCeilingFixture(t)
	req := callRequest(f.s.ID, f.cred)
	req.Server, req.Tool = "slack", "slack.post"
	_, err := f.authorize(req)
	wantDetail(t, err, "no gateway routes the server")
	if d := f.rec.Decisions(); d[len(d)-1].Ceiling != "" {
		t.Fatalf("a call refused at check 6 names ceiling %q", d[len(d)-1].Ceiling)
	}
}

// TestAuthorizeCallSplitsOnlyTheServerPrefix takes everything after the
// server's name and dot as the tool's name on the server, so dots and double
// underscores in a tool name are part of it.
func TestAuthorizeCallSplitsOnlyTheServerPrefix(t *testing.T) {
	f := newCeilingFixture(t)
	const full = "calendar; github"
	for i, tool := range []string{"pr.merge", "a__b", "a.b__c"} {
		f.wantAllowed(t, f.call(f.cred, "R42", "", "op-split-"+string(rune('a'+i)), tool), full)
	}
	// The reviewer's exclusion is the exact name, not a prefix of a dotted
	// one.
	const narrowed = "calendar; github except " + conformance.ReviewerExcludedTool
	f.wantAllowed(t, f.call(f.reviewerCred, "R43", "D1", "op-reviewer-dotted", conformance.ReviewerExcludedTool+".dry_run"), narrowed)
}

// TestAuthorizeCallChildCannotGainAServerItsParentLacks registers a child
// whose definition grants github under a parent whose ceiling is empty: its
// effective ceiling is empty, so its calls are refused.
func TestAuthorizeCallChildCannotGainAServerItsParentLacks(t *testing.T) {
	f := newCeilingFixture(t)
	child, err := f.b.RegisterAgent(context.Background(), testWorkload, identitybroker.RegisterAgentRequest{
		SessionID: f.s.ID, HostSessionID: "hostS-under-tool-less", Owner: testOwner, Incarnation: "inc_gbcdefghijklmnopqrstuvwxya",
		Definition: conformance.DeployerDefinition, Parent: f.toolLess, ParentHostSessionID: "hostS-tool-less",
	})
	if err != nil {
		t.Fatalf("RegisterAgent: %v", err)
	}
	if err := f.b.ReportDelegation(context.Background(), testWorkload, identitybroker.ReportDelegationRequest{
		SessionID: f.s.ID, Owner: testOwner, DelegationID: "D5",
		DelegatingRun: "R46", DelegatingAgent: f.toolLess, ReceivingRun: "R47", ReceivingAgent: child.Agent,
	}); err != nil {
		t.Fatalf("ReportDelegation: %v", err)
	}
	exchanges, redemptions := len(f.issuer.requests()), len(f.as2.redeemed())
	_, err = f.authorize(f.call(child.Credential, "R47", "D5", "op-child", "pull_request_read"))
	wantError(t, err, identitybroker.CodePermissionDenied, identitybroker.ReasonDenied, f.cred, child.Credential)
	f.wantCounts(t, exchanges, redemptions)
	if d := f.rec.Decisions(); d[len(d)-1].Ceiling != "none" {
		t.Fatalf("decision = %+v, want a refusal under ceiling none", d[len(d)-1])
	}
}
