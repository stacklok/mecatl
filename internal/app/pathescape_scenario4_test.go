package app

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/osfs"
)

// pathescape_scenario4_test.go pins the path-escape-posture Scenario 4
// acceptance criteria (docs/acceptance/path-escape-posture.md): at strict and
// trusted an out-of-root Read or Write resolves ASK (never today's hard
// ErrPathEscape dead-end), the ask rides the ordinary surfaceAsk spine
// (EvPermissionAsk → verdict → EvToolResult), plan mode still hard-denies a
// write escape BEFORE any escape ask, and a headless escape ask never hangs —
// the await ends on cancellation, never a fabricated "denied by user". All
// offline (mockllm + a real osfs workspace under t.TempDir).
//
// The e2e tests install the relaxed workspace EXPLICITLY (via
// installRelaxedWorkspace, the same pair the factory builds at every posture)
// so the POLICY decision under test is exercised even if the composition
// factory's relaxed-serving gate regresses; the factory half is pinned
// separately by TestPathEscapePosture_Scenario4_FactoryServesApprovedEscape.

// installRelaxedWorkspace swaps the session's run workspace for the relaxed
// serving pair (osfs.WithRelaxedReads/Writes + the escape classifier wrapped
// by newEscapeWorkspace) — the SAME shape osfsWorkspaceFactory builds for the
// main session at every posture. The Scenario-4 e2e tests call it so the
// POLICY ask decision is exercised against the serving workspace the
// approval relies on, decoupled from the factory wiring (which
// TestPathEscapePosture_Scenario4_FactoryServesApprovedEscape pins).
func installRelaxedWorkspace(t *testing.T, built *Built, sessID session.SessionID, root string) {
	t.Helper()
	clf, err := newEscapeClassifier(root)
	if err != nil {
		t.Fatalf("newEscapeClassifier: %v", err)
	}
	base, err := osfs.NewWorkspace(root, osfs.WithRelaxedReads(), osfs.WithRelaxedWrites())
	if err != nil {
		t.Fatalf("NewWorkspace: %v", err)
	}
	env, err := tool.NewEnvironment(session.EnvironmentRef{Kind: session.EnvKindLocal, ID: root},
		newEscapeWorkspace(base, clf), nil)
	if err != nil {
		t.Fatalf("NewEnvironment: %v", err)
	}
	built.Service.SetSessionEnvironment(sessID, env)
}

// TestPathEscapePosture_Scenario4_FactoryServesApprovedEscape pins the
// COMPOSITION half the e2e tests bypass: the REAL workspace factory (the one
// Build wires into server.Config.Workspaces) must produce the relaxed
// escape-capable workspace at EVERY posture — at strict/trusted it is what
// lets an APPROVED escape ask execute; without it the ask is approved and the
// tool body still dead-ends on ErrPathEscape. A default Build at strict +
// CreateSession (no SetSessionEnvironment override) runs the escape read on an
// allow verdict and the contents MUST come back.
func TestPathEscapePosture_Scenario4_FactoryServesApprovedEscape(t *testing.T) {
	t.Parallel()
	f := setupEscapeFS(t)
	built, err := Build(context.Background(), escapeCfg(t, f, PostureStrict, readEscapeTurns(f.target)...))
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer built.Close()
	sess, err := built.Service.CreateSession(context.Background(), f.workspace, session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	// NO SetSessionEnvironment: the run rides the factory-built workspace.
	run, err := built.Service.StartRun(context.Background(), sess.ID, "read the file outside the workspace")
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	var askSeen bool
	var result *session.ToolResult
	for ev := range run.Events() {
		if ev.Type == session.EvPermissionAsk && ev.Ask != nil && !askSeen {
			askSeen = true
			run.Approve(ev.Ask.AskID, session.VerdictAllowOnce)
		}
		if ev.Type == session.EvToolResult && ev.ToolResult != nil {
			result = ev.ToolResult
		}
	}
	built.Service.FinishRun(sess.ID, run)
	if !askSeen {
		t.Fatal("no EvPermissionAsk — the strict escape must ask (precondition for the factory-serving assertion)")
	}
	if result == nil || result.IsError || !strings.Contains(result.Content, f.content) {
		t.Fatalf("approved strict escape via the FACTORY workspace = %+v — the factory must build the relaxed serving workspace at every posture (an approved escape must execute)", result)
	}
}

// TestPathEscapePosture_Scenario4_StrictReadEscapeAsks pins AC4.1: at posture
// strict, a Read escape surfaces an EvPermissionAsk (never a silent allow and
// never the ErrPathEscape dead-end); on an allow verdict the read executes
// through the ordinary FS tool and returns the file's contents.
//
// The relaxed serving workspace is installed explicitly (the POLICY ask
// decision is what this test exercises; the composition factory's relaxed
// serving is pinned separately by
// TestPathEscapePosture_Scenario4_FactoryServesApprovedEscape). The POLICY
// under test is the REAL strict-wired sharedPolicy — the ask decision is what
// Scenario 4 adds, and the relaxed workspace is what the approval serves
// through.
func TestPathEscapePosture_Scenario4_StrictReadEscapeAsks(t *testing.T) {
	t.Parallel()
	f := setupEscapeFS(t)
	built, err := Build(context.Background(), escapeCfg(t, f, PostureStrict, readEscapeTurns(f.target)...))
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer built.Close()
	sess, err := built.Service.CreateSession(context.Background(), f.workspace, session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	installRelaxedWorkspace(t, built, sess.ID, f.workspace)

	run, err := built.Service.StartRun(context.Background(), sess.ID, "read the file outside the workspace")
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	var askSeen bool
	var result *session.ToolResult
	for ev := range run.Events() {
		if ev.Type == session.EvPermissionAsk && ev.Ask != nil {
			askSeen = true
			if ev.Ask.Tool != "Read" {
				t.Fatalf("ask surfaced for tool %q, want the Read escape (the ask must name the FS tool, not a Bash workaround)", ev.Ask.Tool)
			}
			if !strings.Contains(ev.Ask.Reason, f.target) || !strings.Contains(ev.Ask.Reason, "outside the workspace") {
				t.Fatalf("escape ask reason = %q, want it to name the path and that it lies outside the workspace", ev.Ask.Reason)
			}
			run.Approve(ev.Ask.AskID, session.VerdictAllowOnce)
		}
		if ev.Type == session.EvToolResult && ev.ToolResult != nil {
			result = ev.ToolResult
		}
	}
	built.Service.FinishRun(sess.ID, run)
	if !askSeen {
		t.Fatal("strict Read escape never surfaced an EvPermissionAsk — a strict escape must ASK, not dead-end on ErrPathEscape")
	}
	if result == nil {
		t.Fatal("no EvToolResult emitted after the allow verdict — the approved read must execute")
	}
	if result.IsError {
		t.Fatalf("strict Read escape errored after allow: %q (want the file contents)", result.Content)
	}
	if !strings.Contains(result.Content, f.content) {
		t.Fatalf("strict Read escape content after allow = %q, want it to contain %q", result.Content, f.content)
	}
}

// TestPathEscapePosture_Scenario4_TrustedWriteEscapeAsks pins AC4.2: at
// posture trusted, a Write escape surfaces an EvPermissionAsk; on a DENY
// verdict a deny result is recorded and NOTHING is written (never a silent
// un-asked mutation at any posture below yolo).
func TestPathEscapePosture_Scenario4_TrustedWriteEscapeAsks(t *testing.T) {
	t.Parallel()
	f := setupWriteFS(t)
	call := scenario3Call("w1", "Write", map[string]string{"path": f.target, "content": "trusted-must-never-land"})
	built, err := Build(context.Background(), writeEscapeCfg(t, f, PostureTrusted,
		mockllm.ToolCallTurn(call),
		mockllm.TextTurn("done"),
	))
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer built.Close()
	sess, err := built.Service.CreateSession(context.Background(), f.workspace, session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	installRelaxedWorkspace(t, built, sess.ID, f.workspace)

	run, err := built.Service.StartRun(context.Background(), sess.ID, "write outside the workspace")
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	var askSeen, denyResultSeen bool
	for ev := range run.Events() {
		if ev.Type == session.EvPermissionAsk && ev.Ask != nil {
			askSeen = true
			if ev.Ask.Tool != "Write" {
				t.Fatalf("ask surfaced for tool %q, want the Write escape", ev.Ask.Tool)
			}
			if !strings.Contains(ev.Ask.Reason, f.target) || !strings.Contains(ev.Ask.Reason, "outside the workspace") {
				t.Fatalf("escape ask reason = %q, want it to name the path and that it lies outside the workspace", ev.Ask.Reason)
			}
			run.Approve(ev.Ask.AskID, session.VerdictDeny)
		}
		if ev.Type == session.EvToolResult && ev.ToolResult != nil && ev.ToolResult.IsError {
			denyResultSeen = true
		}
	}
	built.Service.FinishRun(sess.ID, run)
	if !askSeen {
		t.Fatal("trusted Write escape never surfaced an EvPermissionAsk — a write escape must ASK below yolo")
	}
	if !denyResultSeen {
		t.Fatal("no deny tool result recorded after the deny verdict — the refused write must surface a deny result")
	}
	if _, err := os.Stat(f.target); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Stat(%q) = %v — the denied write escape must leave NOTHING written", f.target, err)
	}
}

// TestPathEscapePosture_Scenario4_HeadlessEscapeAskDoesNotHang pins AC4.3: a
// HEADLESS main-engine escape Ask at strict does NOT block forever — with no
// approver wired the run surfaces an actionable stop (the existing headless
// main-ask behaviour: the await ends on CANCELLATION), never a silent hang
// and never a fabricated "denied by user".
//
// Determinism: the test drives the run's OWN events — it cancels the run the
// instant the EvPermissionAsk lands (proving the ask was surfaced headless),
// then the run MUST terminate promptly (StopCancelled) with no deny tool
// result on the abandoned call (a cancellation is not a verdict).
func TestPathEscapePosture_Scenario4_HeadlessEscapeAskDoesNotHang(t *testing.T) {
	t.Parallel()
	f := setupEscapeFS(t)
	built, err := Build(context.Background(), escapeCfg(t, f, PostureStrict, readEscapeTurns(f.target)...))
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer built.Close()
	sess, err := built.Service.CreateSession(context.Background(), f.workspace, session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	installRelaxedWorkspace(t, built, sess.ID, f.workspace)

	run, err := built.Service.StartRun(context.Background(), sess.ID, "read the file outside the workspace")
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	var askSeen, cancelled bool
	var denyResult *session.ToolResult
	done := make(chan struct{})
	go func() {
		defer close(done)
		for ev := range run.Events() {
			if ev.Type == session.EvPermissionAsk && ev.Ask != nil && !askSeen {
				askSeen = true
				run.Cancel() // no approver wired: the await must END, not hang
			}
			if ev.Type == session.EvToolResult && ev.ToolResult != nil && ev.ToolResult.IsError {
				denyResult = ev.ToolResult
			}
			if ev.Type == session.EvResult && ev.Result != nil && ev.Result.Stop == session.StopCancelled {
				cancelled = true
			}
		}
	}()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("the headless escape-ask run did not terminate within 30s of cancellation — the await must end on cancellation, never hang")
	}
	built.Service.FinishRun(sess.ID, run)
	if !askSeen {
		t.Fatal("the strict escape never surfaced an EvPermissionAsk headless — the ask must surface even with no approver (the client/operator sees it, then the run ends)")
	}
	if !cancelled {
		t.Fatal("the run did not end StopCancelled after cancellation of the parked ask — a headless escape ask must resolve to an actionable stop")
	}
	if denyResult != nil && strings.Contains(denyResult.Content, "denied by user") {
		t.Fatalf("tool result = %q — a cancelled headless await must NEVER be misreported as a user deny", denyResult.Content)
	}
}

// TestPathEscapePosture_Scenario4_PlanModeWriteEscapeDenied pins AC4.4 at the
// wrapper level (the decision the loop's dispatch consumes): in plan mode a
// write escape is hard-denied BEFORE any escape Ask is surfaced — the wrapper
// consults the INNER policy first, so the plan-mode hard-deny inside
// permpolicy.Evaluate (EvaluateWith's plan gate) precedes the escape decision
// (plan-mode precedence AND deny-dominance both hold). The positive control:
// the SAME wrapper outside plan mode resolves the identical write escape to
// the escape Ask, and a plan-mode READ escape follows the read row (allow —
// reads are not mutations).
func TestPathEscapePosture_Scenario4_PlanModeWriteEscapeDenied(t *testing.T) {
	t.Parallel()
	f := setupWriteFS(t)
	ws, err := osfs.NewWorkspace(f.workspace)
	if err != nil {
		t.Fatalf("NewWorkspace: %v", err)
	}
	writeCall := scenario3Call("w1", "Write", map[string]string{"path": f.target, "content": "x"})
	readCall := scenario3Call("r1", "Read", map[string]string{"path": f.target})

	for _, posture := range []Posture{PostureStrict, PostureTrusted} {
		posture := posture
		t.Run(posture.String(), func(t *testing.T) {
			t.Parallel()
			p := newEscapePolicy(permpolicy.NewPolicy(defaultRules(), nil), posture)

			d := p.Evaluate(context.Background(), session.SessionID("s1"), session.ModePlan, writeCall, ws)
			if d.Effect != governance.Deny {
				t.Fatalf("plan-mode write escape at %s = %v, want Deny — the plan-mode hard-deny must precede any escape Ask", posture, d.Effect)
			}

			// Positive control 1: the SAME wrapper, default mode → the escape
			// Ask (the deny above is plan-mode precedence, not a blanket
			// strict/trusted deny).
			d = p.Evaluate(context.Background(), session.SessionID("s1"), session.ModeDefault, writeCall, ws)
			if d.Effect != governance.Ask {
				t.Fatalf("default-mode write escape at %s = %v, want the escape Ask", posture, d.Effect)
			}
			if d.ConfiguredAsk || d.FlooredConfiguredAllow {
				t.Fatalf("escape Ask at %s carries ConfiguredAsk=%v/FlooredConfiguredAllow=%v — it must surface to a human (A2/floored-allow key off those bits)", posture, d.ConfiguredAsk, d.FlooredConfiguredAllow)
			}

			// Positive control 2: a plan-mode READ escape follows the read row
			// (allow — plan mode hard-denies mutations only).
			d = p.Evaluate(context.Background(), session.SessionID("s1"), session.ModePlan, readCall, ws)
			if d.Effect == governance.Deny {
				t.Fatalf("plan-mode read escape at %s = Deny (%q) — plan mode denies mutations only; a read escape must not be hard-denied", posture, d.Reason)
			}
		})
	}
}

// TestPathEscapePosture_Scenario4_ConfiguredRulesStillWin pins the
// deny-dominance + configured-Ask-floor halves the Scenario-4 fold must
// preserve: at strict/trusted a CONFIGURED Deny on the tool still wins over
// the escape Ask, and a CONFIGURED Ask is never replaced by the (unconfigured)
// escape Ask — the wrapper delegates to the inner policy FIRST.
func TestPathEscapePosture_Scenario4_ConfiguredRulesStillWin(t *testing.T) {
	t.Parallel()
	f := setupEscapeFS(t)
	ws, err := osfs.NewWorkspace(f.workspace)
	if err != nil {
		t.Fatalf("NewWorkspace: %v", err)
	}
	call := scenario3Call("r1", "Read", map[string]string{"path": f.target})

	t.Run("configured deny wins over the strict escape ask", func(t *testing.T) {
		t.Parallel()
		inner := permpolicy.NewPolicy([]governance.Rule{
			{Scope: governance.ScopeUser, Tool: "Read", Effect: governance.Deny},
		}, nil)
		p := newEscapePolicy(inner, PostureStrict)
		d := p.Evaluate(context.Background(), session.SessionID("s1"), session.ModeDefault, call, ws)
		if d.Effect != governance.Deny {
			t.Fatalf("effect = %v, want Deny — a configured Deny must win over the escape Ask", d.Effect)
		}
	})

	t.Run("configured ask is never replaced by the escape ask", func(t *testing.T) {
		t.Parallel()
		inner := permpolicy.NewPolicy([]governance.Rule{
			{Scope: governance.ScopeUser, Tool: "Read", Effect: governance.Ask},
		}, nil)
		p := newEscapePolicy(inner, PostureTrusted)
		d := p.Evaluate(context.Background(), session.SessionID("s1"), session.ModeDefault, call, ws)
		if d.Effect != governance.Ask || !d.ConfiguredAsk {
			t.Fatalf("effect = %+v, want the CONFIGURED Ask — the escape Ask must never replace a configured Ask", d)
		}
	})
}
