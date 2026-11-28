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

// TestIdentityBrokerRecordsWhoActed pins that a real run records who acted:
// the broker session's open, the registrations of the main agent and a
// subagent, the subagent's delegation, and each gateway call's ID-JAG and
// decision, all accepted by the broker's record sink, which the test reads.
func TestIdentityBrokerRecordsWhoActed(t *testing.T) {
	d := serveDelegations(t)
	d.writeScript(t, gatewayTurn("main-call"),
		`{"tool_calls":[{"id":"delegate","name":"Subagent","args":{"prompt":"call the calendar"}}]}`,
		gatewayTurn("child-call"), scriptText, scriptText)
	promptApprovingAsks(t, d.api, d.alice, d.session, "go", nil)
	d.wantHelperGatewayCalls(t, 1)

	sink := d.built.IdentityBrokerRecords()
	if sink == nil {
		t.Fatal("mecak8s built no identity broker record sink")
	}
	var accepted []identitybroker.Record
	for _, r := range sink.Opens() {
		accepted = append(accepted, &r)
	}
	for _, r := range sink.Registrations() {
		accepted = append(accepted, &r)
	}
	for _, r := range sink.Delegations() {
		accepted = append(accepted, &r)
	}
	for _, r := range sink.IDJAGs() {
		accepted = append(accepted, &r)
	}
	for _, r := range sink.Decisions() {
		accepted = append(accepted, &r)
	}
	successes := map[string]int{}
	ids := map[string]bool{}
	for _, r := range accepted {
		h := r.Header()
		t.Logf("record %s %s %s", h.Type, h.AuditID, h.Outcome)
		if ids[h.AuditID] || !strings.HasPrefix(h.AuditID, "rec_") {
			t.Fatalf("record %+v repeats or lacks an AuditID", h)
		}
		ids[h.AuditID] = true
		if h.Outcome == identitybroker.OutcomeSuccess {
			successes[h.Type]++
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
}
