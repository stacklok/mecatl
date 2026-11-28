package main

import (
	"context"
	"fmt"
	"iter"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/internal/adapter/mockscript"
	"github.com/stacklok/mecatl/internal/app"
	"github.com/stacklok/mecatl/internal/identitybroker"
	"github.com/stacklok/mecatl/internal/identitybroker/adapter/as1server/as1servertest"
	"github.com/stacklok/mecatl/internal/testutil/identityharness"
)

// The tests in this file run each kind of helper agent's gateway call
// through mecak8s's startup, the real AS-1 and AS-2, and the embedded vMCP,
// with the identity broker's run, owner and delegation checks on (#2557). A
// helper is not a root instance, so the broker refuses its call unless
// mecak8s reported the delegation that started its run: a completed call by
// a helper proves the report reached the broker and was accepted. Each
// script's first turn is a gateway call by the main agent, made before any
// helper exists, so the first ID-JAG names the main agent. Their stand-ins
// are listed on TestIdentityBrokerGatewayCallAgainstRealAS1.

// lateScript is a mock model that loads its script at its first turn, so a
// test can write a script naming the session it created.
type lateScript struct {
	load func() (port.LLMProvider, error)
}

func newLateScript(path string) *lateScript {
	return &lateScript{load: sync.OnceValues(func() (port.LLMProvider, error) { return mockscript.Load(path) })}
}

func (*lateScript) Capabilities() port.ProviderCapabilities { return port.ProviderCapabilities{} }

func (l *lateScript) Stream(ctx context.Context, req port.LLMRequest) (iter.Seq2[port.Chunk, error], error) {
	inner, err := l.load()
	if err != nil {
		return nil, err
	}
	return inner.Stream(ctx, req)
}

// delegationServe is mecak8s serving the gateway with teams on, one session
// Alice created, and the mock model's script file.
type delegationServe struct {
	*gatewayServe
	diag    *gatewayCallDiagnostics
	script  string
	session string
}

// serveDelegations serves mecak8s with teams on and extraArgs as further
// flags.
func serveDelegations(t *testing.T, extraArgs ...string) *delegationServe {
	t.Helper()
	return serveDelegationsWith(t, append([]string{"--enable-teams"}, extraArgs...)...)
}

// serveDelegationsWith serves mecak8s with args as its only further flags.
func serveDelegationsWith(t *testing.T, args ...string) *delegationServe {
	t.Helper()
	d := &delegationServe{diag: &gatewayCallDiagnostics{}, script: filepath.Join(t.TempDir(), "script.json")}
	model := newLateScript(d.script)
	// No workspace: the broker refuses one on this process's filesystem, so
	// parallel branches and API-created teams, which need one, run only with
	// Kubernetes execution, which this suite cannot start. They report their
	// delegations through the same RegisterHelper path these tests drive.
	d.gatewayServe = serveGatewayIdentityBroker(t, notesRoute(t),
		args,
		func(cfg *app.Config) {
			cfg.MockProvider = model
			cfg.Diagnostics = d.diag
		})
	d.session = createSession(t, d.api, d.alice)
	return d
}

// writeScript writes the mock model's turns, each SESSION replaced by the
// session's ID, followed by text turns for whichever agent asks next.
func (d *delegationServe) writeScript(t *testing.T, turns ...string) {
	t.Helper()
	turns = append(turns, slices.Repeat([]string{scriptText}, 40)...)
	doc := `{"turns":[` + strings.ReplaceAll(strings.Join(turns, ","), "SESSION", d.session) + `]}`
	if err := os.WriteFile(d.script, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
}

const scriptText = `{"text":"done"}`

// gatewayTurn is a mock model turn calling the gateway route's tool.
func gatewayTurn(id string) string {
	return fmt.Sprintf(`{"tool_calls":[{"id":%q,"name":"mcp__calendar__echo","args":{"a":1}}]}`, id)
}

// wantHelperGatewayCalls fails t unless every gateway call reached the vMCP
// with an ID-JAG (wantStandInRefusal), the first naming the main agent, and
// at least minHelperCalls named another agent.
func (d *delegationServe) wantHelperGatewayCalls(t *testing.T, minHelperCalls int) {
	t.Helper()
	ops := d.diag.operations(d.session)
	for _, op := range ops {
		d.wantStandInRefusal(t, op, d.diag.call(op))
	}
	issued := d.idjags(t)
	if len(issued) != len(ops) || len(issued) == 0 {
		t.Fatalf("AS-1 issued %d ID-JAGs for %d gateway calls", len(issued), len(ops))
	}
	actor := func(i int) any { return issued[i]["act"].(map[string]any)["sub"] }
	helperCalls := 0
	for i := range issued {
		if actor(i) != actor(0) {
			helperCalls++
		}
		if issued[i]["sub"] != issued[0]["sub"] {
			t.Errorf("ID-JAG %d acts for %v, ID-JAG 1 for %v; all act for Alice", i+1, issued[i]["sub"], issued[0]["sub"])
		}
	}
	if helperCalls < minHelperCalls {
		t.Fatalf("%d of %d gateway calls were by a helper, want at least %d", helperCalls, len(issued), minHelperCalls)
	}
}

// TestIdentityBrokerTeamMemberGatewayCall pins that a member of a team the
// Team tool starts makes gateway calls under its reported delegation. The
// members are generic, so each gets every gateway tool; they share the mock
// script, so which calls first varies.
func TestIdentityBrokerTeamMemberGatewayCall(t *testing.T) {
	d := serveDelegations(t)
	d.writeScript(t, gatewayTurn("main-call"),
		`{"tool_calls":[{"id":"team","name":"Team","args":{"goal":"call the calendar","members":[{"name":"lead","role":"lead"},{"name":"rev","role":"review"}]}}]}`,
		gatewayTurn("member-call-1"), scriptText, gatewayTurn("member-call-2"), scriptText)
	promptApprovingAsks(t, d.api, d.alice, d.session, "go", nil)
	d.wantHelperGatewayCalls(t, 1)
}

// TestIdentityBrokerResumedHelperGatewayCallAfterReopen pins that a subagent
// resumed after its root's broker session closed is delegated again from the
// resuming run in the reopened broker session, so its gateway call passes.
func TestIdentityBrokerResumedHelperGatewayCallAfterReopen(t *testing.T) {
	d := serveDelegations(t)
	d.writeScript(t, gatewayTurn("main-call"),
		`{"tool_calls":[{"id":"delegate","name":"Subagent","args":{"prompt":"call the calendar"}}]}`,
		gatewayTurn("child-call"), scriptText, scriptText,
		`{"tool_calls":[{"id":"resume","name":"Subagent","args":{"resume":"subagent-SESSION-delegate","prompt":"call it again"}}]}`,
		gatewayTurn("resumed-call"), scriptText)
	promptApprovingAsks(t, d.api, d.alice, d.session, "go", nil)
	opened, ok, err := d.built.IdentityBroker.FindSession(t.Context(), identitybroker.FindSessionRequest{RootHostSessionID: d.session})
	if err != nil || !ok {
		t.Fatalf("FindSession = %v, %v; want the session's broker session", ok, err)
	}
	if err := d.built.IdentityBroker.CloseSession(t.Context(), identitybroker.CloseSessionRequest{SessionID: opened.ID, Reason: identitybroker.ReasonSessionClosed}); err != nil {
		t.Fatalf("close the broker session: %v", err)
	}
	// AS-1 converts an access token at one session open, so Alice signs in
	// again, as mecatui gets a fresh token for each.
	fresh := d.as1Server.SignIn(t, as1servertest.SignIn{User: identityharness.Alice}).Value()
	promptApprovingAsks(t, d.api, fresh, d.session, "resume it", nil)
	reopened, ok, err := d.built.IdentityBroker.FindSession(t.Context(), identitybroker.FindSessionRequest{RootHostSessionID: d.session})
	if err != nil || !ok || reopened.ID == opened.ID {
		t.Fatalf("after the resume, broker session = %+v, %v, %v; want a new one", reopened, ok, err)
	}
	d.wantHelperGatewayCalls(t, 2)
	// Both helper calls are the one subagent's.
	issued := d.idjags(t)
	if child, resumed := issued[1]["act"], issued[2]["act"]; fmt.Sprint(child) != fmt.Sprint(resumed) {
		t.Fatalf("the resumed subagent called as %v, before the reopen as %v; want the same agent", resumed, child)
	}
}

// TestIdentityBrokerRecordsWhoActed pins that a real run records who acted
// in the audit file, through the toolhive-core logger: the broker session's
// open, the registrations of the main agent and a subagent, the subagent's
// delegation, and each gateway call's ID-JAG and decision, as profile
// events; and the embedded gateway's own event of each call, naming the
// acting agent from the token's act as toolhive-core's delegation chain. No
// event carries a credential AS-1 issued or was sent, Alice's access token
// from the session open included.
func TestIdentityBrokerRecordsWhoActed(t *testing.T) {
	d := serveDelegations(t)
	d.writeScript(t, gatewayTurn("main-call"),
		`{"tool_calls":[{"id":"delegate","name":"Subagent","args":{"prompt":"call the calendar"}}]}`,
		gatewayTurn("child-call"), scriptText, scriptText)
	promptApprovingAsks(t, d.api, d.alice, d.session, "go", nil)
	d.wantHelperGatewayCalls(t, 1)
	// Close sends the records still queued.
	d.built.Close()

	events := readAuditFile(t, d.auditFile)
	successes := map[string]int{}
	ids := map[string]bool{}
	registrations := map[string]auditLine{}
	for _, ev := range events {
		if ev.Component != "mecatl-broker" {
			continue
		}
		t.Logf("record %s %s %s", ev.Type, ev.AuditID, ev.Outcome)
		if ids[ev.AuditID] || !strings.HasPrefix(ev.AuditID, "rec_") {
			t.Fatalf("record %s %s repeats or lacks an AuditID", ev.Type, ev.AuditID)
		}
		ids[ev.AuditID] = true
		if ev.Outcome == "success" {
			successes[ev.Type]++
		}
		if ev.Type == identitybroker.TypeInstanceRegistered && ev.Outcome == "success" {
			registrations[ev.AuditID] = ev
		}
	}
	for typ, want := range map[string]int{
		identitybroker.TypeSessionOpen:        1,
		identitybroker.TypeInstanceRegistered: 2,
		identitybroker.TypeDelegation:         1,
		identitybroker.TypeIDJAG:              2,
		identitybroker.TypeToolDecision:       2,
	} {
		if successes[typ] < want {
			t.Errorf("%d successful %s records, want at least %d", successes[typ], typ, want)
		}
	}
	// The subagent's registration names its ag_ identifier and SPIFFE ID,
	// and its parent's registration by AuditID.
	var child auditLine
	for _, reg := range registrations {
		if reg.Data.Lineage.ParentRecord != "" {
			child = reg
		}
	}
	if !strings.HasPrefix(child.Subjects[identitybroker.SubjectAgentActorID], "ag_") || child.Subjects[identitybroker.SubjectAgentSPIFFEID] == "" {
		t.Fatalf("subagent registration %+v lacks its ag_ identifier or SPIFFE ID", child)
	}
	if _, ok := registrations[child.Data.Lineage.ParentRecord]; !ok {
		t.Fatalf("subagent registration names parent record %q, which is no accepted registration", child.Data.Lineage.ParentRecord)
	}

	// The gateway's events of the calls name the acting agent as
	// toolhive-core's delegation chain of the token's act.
	wantChains := map[string]bool{}
	for _, claims := range d.idjags(t) {
		wantChains[delegationChainJSON(t, claims["act"])] = true
	}
	gatewayCalls := 0
	for _, ev := range events {
		if ev.Component != "vmcp-server" || ev.Type != "mcp_tool_call" {
			continue
		}
		gatewayCalls++
		if !wantChains[string(ev.Delegation)] {
			t.Errorf("gateway event delegation %s, want one of the ID-JAGs' act chains %v", ev.Delegation, wantChains)
		}
	}
	if gatewayCalls < 2 {
		t.Fatalf("%d gateway tool-call events, want one per call", gatewayCalls)
	}

	raw, err := os.ReadFile(d.auditFile)
	if err != nil {
		t.Fatal(err)
	}
	credentials := []string{d.alice}
	for _, r := range d.as1.Requests() {
		credentials = append(credentials, r.ClientAssertion, r.Assertion, r.ActorToken, r.IssuedToken)
	}
	for _, c := range credentials {
		if c != "" && strings.Contains(string(raw), c) {
			t.Fatal("an audit event carries a credential value")
		}
	}
}
