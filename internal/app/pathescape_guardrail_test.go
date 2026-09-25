package app

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/osfs"
)

// pathescape_guardrail_test.go pins ADR 0080 / AC-W2-G2
// (docs/acceptance/path-escape-posture.md, deferred decision
// "Guardrail-routed escape checking"): at posture auto WITH the operator-tier
// escape knob (guardrails.escape) configured, an out-of-root escape is routed
// through the LLM guardrail checker as a composition-level PRE-CHECK inside
// the escape policy — a checker "unsafe" verdict DENIES the escape, a "safe"
// verdict falls back to the ordinary auto row (read Allow / write Ask), and a
// checker ERROR fails CLOSED to the write-escape Ask (the already-safe
// posture: it surfaces to a human and is deny-safe headless). The route is
// auto-only, main-engine-only, deny-dominant, and never suppresses a
// configured rule. All offline (mockllm drives the checker engine through the
// SAME UseMock path buildGuardrailsChecker uses).

// buildRoutedEscapePolicy constructs the REAL auto escape policy with the
// escape route armed over a mockllm-backed checker engine — the exact pair
// buildEngine builds when cfg.GuardrailsModel is set at PostureAuto.
func buildRoutedEscapePolicy(t *testing.T, cfg Config, checkerTurns ...mockllm.Turn) port.PermissionPolicy {
	t.Helper()
	checker := buildGuardrailsChecker(cfg, nil, mockllm.New(checkerTurns...), "mock", "m")
	if checker == nil {
		t.Fatal("buildGuardrailsChecker returned nil — the test scripts the checker via the same mockllm path the composition uses")
	}
	return newEscapePolicy(permpolicy.NewPolicy(defaultRules(), nil), PostureAuto, withEscapeGuardrailRoute(checker))
}

// evalEscapeAtAuto evaluates one call through the routed policy against a
// real osfs workspace rooted at the fixture workspace.
func evalEscapeAtAuto(t *testing.T, p port.PermissionPolicy, workspace string, c session.ToolCall) governance.PermissionDecision {
	t.Helper()
	ws, err := osfs.NewWorkspace(workspace)
	if err != nil {
		t.Fatalf("NewWorkspace: %v", err)
	}
	return p.Evaluate(context.Background(), session.SessionID("s1"), session.ModeDefault, c, ws)
}

// TestPathEscapePosture_GuardrailRoutedEscape pins AC-W2-G2: auto + the
// configured escape knob routes the escape through the checker; a blocked
// (unsafe) escape is DENIED.
func TestPathEscapePosture_GuardrailRoutedEscape(t *testing.T) {
	t.Parallel()
	f := setupEscapeFS(t)
	call := scenario3Call("r1", "Read", map[string]string{"path": f.target})
	cfg := Config{UseMock: true, GuardrailsModel: "checker-model", Posture: PostureAuto}

	t.Run("unsafe verdict denies the escape", func(t *testing.T) {
		t.Parallel()
		p := buildRoutedEscapePolicy(t, cfg, mockllm.TextTurn(`{"assessment":"prohibited","concerns":[{"ref":"C1","category":"authority_crossing","rationale":"targeted /etc shadow read","source_ref":"call"}],"evidence":[],"missing_evidence":[]}`))
		d := evalEscapeAtAuto(t, p, f.workspace, call)
		if d.Effect != governance.Deny {
			t.Fatalf("routed unsafe escape = %v (%q), want Deny — the checker's block must veto the escape", d.Effect, d.Reason)
		}
		if !strings.Contains(d.Reason, "guardrail") {
			t.Fatalf("deny reason = %q, want it to name the guardrail checker so the veto is legible", d.Reason)
		}
	})

	t.Run("safe verdict falls back to the auto read-allow row", func(t *testing.T) {
		t.Parallel()
		p := buildRoutedEscapePolicy(t, cfg, mockllm.TextTurn(`{"assessment":"acceptable","concerns":[],"evidence":[],"missing_evidence":[]}`))
		d := evalEscapeAtAuto(t, p, f.workspace, call)
		if d.Effect != governance.Allow {
			t.Fatalf("routed safe escape = %v (%q), want Allow — a safe verdict falls back to the ordinary auto row", d.Effect, d.Reason)
		}
	})

	t.Run("checker error fails closed to the write-escape ask", func(t *testing.T) {
		t.Parallel()
		// An unparseable checker reply is a checker FAILURE (ParseVerdict
		// requires the whole output to be one verdict object): the route must
		// fail CLOSED to the write-escape Ask — the already-safe posture that
		// surfaces to a human — never to a silent allow and never to a plain
		// pass-through of the read-allow row.
		p := buildRoutedEscapePolicy(t, cfg, mockllm.TextTurn("I cannot decide"))
		d := evalEscapeAtAuto(t, p, f.workspace, call)
		if d.Effect != governance.Ask {
			t.Fatalf("checker-error escape = %v (%q), want the fail-closed write-escape Ask", d.Effect, d.Reason)
		}
		if !strings.Contains(d.Reason, "guardrail checker") {
			t.Fatalf("ask reason = %q, want it to name the checker failure", d.Reason)
		}
	})

	t.Run("write escape routed too", func(t *testing.T) {
		t.Parallel()
		wcall := scenario3Call("w1", "Write", map[string]string{"path": f.target, "content": "x"})
		p := buildRoutedEscapePolicy(t, cfg, mockllm.TextTurn(`{"assessment":"prohibited","concerns":[{"ref":"C1","category":"authority_crossing","rationale":"destructive write outside the workspace","source_ref":"call"}],"evidence":[],"missing_evidence":[]}`))
		d := evalEscapeAtAuto(t, p, f.workspace, wcall)
		if d.Effect != governance.Deny {
			t.Fatalf("routed unsafe WRITE escape = %v (%q), want Deny — the route covers both read and write escapes", d.Effect, d.Reason)
		}
	})

	t.Run("yolo and strict never route", func(t *testing.T) {
		t.Parallel()
		// The route is the AUTO-only knob: yolo demotes guardrails to advisory
		// (ADR 0062) and never spends a checker call on a decision the posture
		// already made; strict/trusted keep their own Scenario-4 escape Ask.
		for _, posture := range []Posture{PostureYolo, PostureStrict, PostureTrusted} {
			posture := posture
			t.Run(posture.String(), func(t *testing.T) {
				t.Parallel()
				// Script a verdict that would DENY if consulted; assert the
				// posture row wins and the checker was never consulted (a
				// consumed verdict would flip the outcome).
				checker := buildGuardrailsChecker(Config{UseMock: true, GuardrailsModel: "checker-model"}, nil,
					mockllm.New(mockllm.TextTurn(`{"assessment":"prohibited","concerns":[{"ref":"C1","category":"authority_crossing","rationale":"must not be consulted","source_ref":"call"}],"evidence":[],"missing_evidence":[]}`)), "mock", "m")
				p := newEscapePolicy(permpolicy.NewPolicy(defaultRules(), nil), posture, withEscapeGuardrailRoute(checker))
				d := evalEscapeAtAuto(t, p, f.workspace, call)
				switch posture {
				case PostureYolo:
					if d.Effect != governance.Allow {
						t.Fatalf("yolo escape = %v, want Allow (the checker must never be consulted at yolo)", d.Effect)
					}
				default:
					if d.Effect != governance.Ask {
						t.Fatalf("%s escape = %v, want the Scenario-4 Ask (the route is auto-only)", posture, d.Effect)
					}
				}
			})
		}
	})

	t.Run("no knob configured leaves the auto row untouched", func(t *testing.T) {
		t.Parallel()
		// Without the escape knob the auto read row is a plain Allow (Shell
		// parity) with NO checker call — the route is strictly opt-in.
		p := newEscapePolicy(permpolicy.NewPolicy(defaultRules(), nil), PostureAuto)
		d := evalEscapeAtAuto(t, p, f.workspace, call)
		if d.Effect != governance.Allow {
			t.Fatalf("auto escape without the knob = %v, want Allow (no route, no checker spend)", d.Effect)
		}
	})

	t.Run("deny-dominance: a configured deny never reaches the checker", func(t *testing.T) {
		t.Parallel()
		inner := permpolicy.NewPolicy([]governance.Rule{
			{Scope: governance.ScopeUser, Tool: "Read", Effect: governance.Deny},
		}, nil)
		checker := buildGuardrailsChecker(cfg, nil, mockllm.New(mockllm.TextTurn(`{"assessment":"acceptable","concerns":[],"evidence":[],"missing_evidence":[]}`)), "mock", "m")
		p := newEscapePolicy(inner, PostureAuto, withEscapeGuardrailRoute(checker))
		d := evalEscapeAtAuto(t, p, f.workspace, call)
		if d.Effect != governance.Deny {
			t.Fatalf("configured deny + routed escape = %v, want Deny — the route runs only after the inner fold", d.Effect)
		}
	})

	t.Run("the composition wires the route from the knob", func(t *testing.T) {
		t.Parallel()
		// The factory-path half (the ADR-0070 discipline): a REAL Build at auto
		// with GuardrailsModel + the escape knob must DENY the escape when the
		// checker scripts unsafe — deleting the knob wiring in buildEngine
		// fails this, even though the policy-level subtests above stay green.
		// mockllm serves BOTH the agent turns and the checker turn in script
		// order through the same UseMock provider.
		f := setupEscapeFS(t)
		args, _ := json.Marshal(map[string]string{"path": f.target})
		turns := []mockllm.Turn{
			mockllm.ToolCallTurn(session.NewToolCall("r1", "Read", json.RawMessage(args))),
			mockllm.TextTurn(`{"assessment":"prohibited","concerns":[{"ref":"C1","category":"authority_crossing","rationale":"sensitive out-of-root read","source_ref":"call"}],"evidence":[],"missing_evidence":[]}`), // the checker's verdict
			mockllm.TextTurn("done"),
		}
		bcfg := escapeCfg(t, f, PostureAuto, turns...)
		bcfg.GuardrailsModel = "checker-model"
		bcfg.GuardrailsDisabled = false // escapeCfg declares off; this test configures a checker instead
		bcfg.GuardrailsEscape = true
		built, err := buildIsolated(t, context.Background(), bcfg)
		if err != nil {
			t.Fatalf("Build: %v", err)
		}
		defer built.Close()
		sess, err := built.Service.CreateSession(context.Background(), session.ModeDefault, session.Limits{})
		if err != nil {
			t.Fatalf("CreateSession: %v", err)
		}
		installRelaxedWorkspace(t, built, sess.ID, f.workspace)
		result, _ := runOneTurn(t, built, sess.ID)
		if result == nil || !result.IsError || !strings.Contains(result.Content, "guardrail") {
			t.Fatalf("result = %+v — the Build-wired route must DENY the checker-blocked escape (a guardrail-named deny result)", result)
		}
	})

	t.Run("no knob at Build leaves the auto row byte-identical", func(t *testing.T) {
		t.Parallel()
		// Guardrails configured (the model IS the opt-in to spend) but the
		// escape knob OFF: the auto read escape stays a plain Shell-parity
		// Allow — the route is opt-in, never implied by guardrails alone.
		f := setupEscapeFS(t)
		bcfg := escapeCfg(t, f, PostureAuto, readEscapeTurns(f.target)...)
		bcfg.GuardrailsModel = "checker-model"
		bcfg.GuardrailsDisabled = false // escapeCfg declares off; this test configures a checker instead
		// Isolate the escape-route assertion from ADR 0363's default inbound Read
		// coverage: an explicit unrelated rule keeps guardrails enabled without
		// reviewing this Read result through the ordinary tool boundary.
		bcfg.GuardrailsRules = []GuardrailRule{{Match: "Shell", Phases: []string{"pre"}, Mode: "block"}}
		built, err := buildIsolated(t, context.Background(), bcfg)
		if err != nil {
			t.Fatalf("Build: %v", err)
		}
		defer built.Close()
		sess, err := built.Service.CreateSession(context.Background(), session.ModeDefault, session.Limits{})
		if err != nil {
			t.Fatalf("CreateSession: %v", err)
		}
		installRelaxedWorkspace(t, built, sess.ID, f.workspace)
		result, _ := runOneTurn(t, built, sess.ID)
		if result == nil || result.IsError || !strings.Contains(result.Content, f.content) {
			t.Fatalf("result = %+v — guardrails WITHOUT the escape knob must leave the auto read row a plain allow", result)
		}
	})

	t.Run("checker rationale stays out of the decision surface", func(t *testing.T) {
		t.Parallel()
		// Reviewer rationale belongs only in the transient, owner-authorized detail
		// sink. The escape decision carries a generic machine-safe reason.
		secretRationale := strings.Repeat("r", 500)
		p := buildRoutedEscapePolicy(t, cfg, mockllm.TextTurn(`{"assessment":"prohibited","concerns":[{"ref":"C1","category":"authority_crossing","rationale":"`+secretRationale+`","source_ref":"call"}],"evidence":[],"missing_evidence":[]}`))
		d := evalEscapeAtAuto(t, p, f.workspace, call)
		if d.Effect != governance.Deny {
			t.Fatalf("prohibited escape = %v, want Deny", d.Effect)
		}
		if strings.Contains(d.Reason, secretRationale) || !strings.Contains(d.Reason, "guardrail") {
			t.Fatalf("decision reason leaked checker rationale or lost machine reason: %q", d.Reason)
		}
	})
}
