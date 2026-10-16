package app

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/session"
)

// TestAutoModeInjectsAllowAll proves that Policy.Evaluate with ModeAuto returns
// Allow for mutating tools (Shell/Edit) that ModeDefault floors at Ask. This
// verifies that allow-all is injected at evaluation time by the mode, not baked
// into the base ruleset at startup.
func TestAutoModeInjectsAllowAll(t *testing.T) {
	const sid = session.SessionID("s1")
	bashArgs, _ := json.Marshal(map[string]string{"command": "touch x"})
	bashCall := session.NewToolCall("c1", "Shell", bashArgs)
	editArgs, _ := json.Marshal(map[string]string{"file_path": "/x"})
	editCall := session.NewToolCall("c2", "Edit", editArgs)

	p := permpolicy.NewPolicy(mainRules(Config{}), nil, mainEvaluatorOptions(Config{})...)

	// ModeAuto injects allowAllCLIRules → Ask floor is loosened → Allow.
	for _, call := range []session.ToolCall{bashCall, editCall} {
		if got := p.Evaluate(context.Background(), sid, session.ModeAuto, call, nil); got.Decision.Effect != governance.Allow {
			t.Fatalf("ModeAuto %s: expected Allow, got %v (%s)", call.Name, got.Decision.Effect, got.Decision.Reason)
		}
	}

	// ModeDefault: no extra rules injected → built-in mutate-Ask floor stands.
	for _, call := range []session.ToolCall{bashCall, editCall} {
		if got := p.Evaluate(context.Background(), sid, session.ModeDefault, call, nil); got.Decision.Effect != governance.Ask {
			t.Fatalf("ModeDefault %s: expected Ask, got %v (%s)", call.Name, got.Decision.Effect, got.Decision.Reason)
		}
	}
}

// TestPolicyAutoAllowsAskFloor is the evaluator-level proof that ModeAuto
// actually flips the mutate-ask floor (Shell/Edit) from Ask to Allow. Benign
// commands only; nothing executes (the test stops at Evaluate).
func TestPolicyAutoAllowsAskFloor(t *testing.T) {
	const sid = session.SessionID("s1")
	bashArgs, _ := json.Marshal(map[string]string{"command": "touch x"})
	bashCall := session.NewToolCall("c1", "Shell", bashArgs)
	editArgs, _ := json.Marshal(map[string]string{"file_path": "/x"})
	editCall := session.NewToolCall("c2", "Edit", editArgs)

	p := permpolicy.NewPolicy(mainRules(Config{}), nil, mainEvaluatorOptions(Config{})...)

	if got := p.Evaluate(context.Background(), sid, session.ModeAuto, bashCall, nil); got.Decision.Effect != governance.Allow {
		t.Fatalf("ModeAuto Shell: expected Allow, got %v (%s)", got.Decision.Effect, got.Decision.Reason)
	}
	if got := p.Evaluate(context.Background(), sid, session.ModeAuto, editCall, nil); got.Decision.Effect != governance.Allow {
		t.Fatalf("ModeAuto Edit: expected Allow, got %v (%s)", got.Decision.Effect, got.Decision.Reason)
	}
	if got := p.Evaluate(context.Background(), sid, session.ModeDefault, bashCall, nil); got.Decision.Effect != governance.Ask {
		t.Fatalf("ModeDefault Shell: expected Ask, got %v (%s)", got.Decision.Effect, got.Decision.Reason)
	}
	if got := p.Evaluate(context.Background(), sid, session.ModeDefault, editCall, nil); got.Decision.Effect != governance.Ask {
		t.Fatalf("ModeDefault Edit: expected Ask, got %v (%s)", got.Decision.Effect, got.Decision.Reason)
	}
}

// TestYoloModeLoosensSubstitutionFloor proves that Policy.Evaluate with ModeYolo
// allows $(...)  substitutions that ModeDefault and ModeAuto floor at Ask. The
// loose-substitution evaluator is selected by ModeYolo at call time.
// Uses the innocuous stand-in `zap`; nothing executes.
func TestYoloModeLoosensSubstitutionFloor(t *testing.T) {
	const sid = session.SessionID("s1")
	args, _ := json.Marshal(map[string]string{"command": "cat $(zap)"})
	call := session.NewToolCall("c1", "Shell", args)

	// Build the policy the same way buildEngine builds the main policy.
	p := permpolicy.NewPolicy(mainRules(Config{}), nil, mainEvaluatorOptions(Config{})...)

	// ModeYolo: loose-substitution evaluator + allowAllCLIRules → Allow.
	if got := p.Evaluate(context.Background(), sid, session.ModeYolo, call, nil); got.Decision.Effect != governance.Allow {
		t.Fatalf("ModeYolo substitution: expected Allow (evalLoose must be wired), got %v (%s)", got.Decision.Effect, got.Decision.Reason)
	}

	// ModeAuto: standard evaluator + allowAllCLIRules → substitution still floors at Ask.
	if got := p.Evaluate(context.Background(), sid, session.ModeAuto, call, nil); got.Decision.Effect != governance.Ask {
		t.Fatalf("ModeAuto substitution: expected Ask (substitution floor stands), got %v (%s)", got.Decision.Effect, got.Decision.Reason)
	}

	// ModeDefault: no extra rules → Ask.
	if got := p.Evaluate(context.Background(), sid, session.ModeDefault, call, nil); got.Decision.Effect != governance.Ask {
		t.Fatalf("ModeDefault substitution: expected Ask (floor stands), got %v (%s)", got.Decision.Effect, got.Decision.Reason)
	}
}

// TestChildPolicyAutoAllowsAllMutates verifies that child policy with ModeDefault
// allows all mutates (the AllowAllFloorRules floor covers it), ModeAuto also
// allows mutates, and ModePlan still denies mutates.
func TestChildPolicyAutoAllowsAllMutates(t *testing.T) {
	const sid = session.SessionID("s1")
	args, _ := json.Marshal(map[string]string{"command": "python3 script.py"})
	call := session.NewToolCall("c1", "Shell", args)

	child := childPermPolicy(Config{})

	// ModeDefault: child floor is blanket allow-all → Allow.
	if got := child.Evaluate(context.Background(), sid, session.ModeDefault, call, nil); got.Decision.Effect != governance.Allow {
		t.Fatalf("child ModeDefault plain mutate: expected Allow (floor), got %v (%s)", got.Decision.Effect, got.Decision.Reason)
	}

	// ModeAuto: floor + allowAllCLIRules → Allow (floor already allows, mode is redundant but harmless).
	if got := child.Evaluate(context.Background(), sid, session.ModeAuto, call, nil); got.Decision.Effect != governance.Allow {
		t.Fatalf("child ModeAuto plain mutate: expected Allow, got %v (%s)", got.Decision.Effect, got.Decision.Reason)
	}

	// ModePlan: plan-mode hard-deny runs FIRST → Deny.
	if got := child.Evaluate(context.Background(), sid, session.ModePlan, call, nil); got.Decision.Effect != governance.Deny {
		t.Fatalf("child ModePlan plain mutate: expected Deny (plan-mode gate), got %v (%s)", got.Decision.Effect, got.Decision.Reason)
	}
}

// TestChildPolicyAutoApprovesNonSubstitution proves the child policy
// auto-approves a plain (non-read-only, non-substitution) mutate Shell command.
// This holds in ModeDefault because the child floor is a blanket allow-all;
// children have no mutate-ask floor to loosen. Innocuous non-read-only stand-in;
// nothing executes.
func TestChildPolicyAutoApprovesNonSubstitution(t *testing.T) {
	const sid = session.SessionID("s1")
	args, _ := json.Marshal(map[string]string{"command": "python3 script.py"})
	call := session.NewToolCall("c1", "Shell", args)

	p := childPermPolicy(Config{})
	if got := p.Evaluate(context.Background(), sid, session.ModeDefault, call, nil); got.Decision.Effect != governance.Allow {
		t.Fatalf("child plain mutate ModeDefault: expected Allow (blanket floor), got %v (%s)", got.Decision.Effect, got.Decision.Reason)
	}
}

// TestYoloChildSubstitutionStillFailsSafe is the SAFETY kill test: under
// ModeDefault a child's substitution command with a non-read-only inner ($(zap))
// floors at Ask. Under ModeYolo, the evalLoose evaluator allows it.
func TestYoloChildSubstitutionStillFailsSafe(t *testing.T) {
	const sid = session.SessionID("s1")
	child := childPermPolicy(Config{})

	badArgs, _ := json.Marshal(map[string]string{"command": "cat $(zap)"}) // non-read-only inner
	badCall := session.NewToolCall("c1", "Shell", badArgs)

	// ModeDefault: substitution floors at Ask. The child floor is AllowAllFloorRules at
	// ScopeBuiltinDefault, so flooredAllowSafe fails for the non-read-only inner (zap),
	// flooredBuiltin++ → AskProvenanceBuiltinSubstitutionFloor.
	if got := child.Evaluate(context.Background(), sid, session.ModeDefault, badCall, nil); got.Decision.Effect != governance.Ask || got.Decision.AskProvenance != governance.AskProvenanceBuiltinSubstitutionFloor {
		t.Fatalf("child ModeDefault substitution (bad inner) must Ask with BuiltinSubstitutionFloor; got %+v", got)
	}

	// ModeYolo: evalLoose evaluator + allowAllCLIRules → Allow.
	if got := child.Evaluate(context.Background(), sid, session.ModeYolo, badCall, nil); got.Decision.Effect != governance.Allow {
		t.Fatalf("child ModeYolo substitution: expected Allow (loose evaluator), got %v (%s)", got.Decision.Effect, got.Decision.Reason)
	}

	goodArgs, _ := json.Marshal(map[string]string{"command": "go test $(git rev-parse HEAD)"}) // read-only inner
	goodCall := session.NewToolCall("c2", "Shell", goodArgs)
	// ModeDefault: read-only inner, but the only matching Allow is the floor (ScopeBuiltinDefault)
	// → flooredBuiltin → AskProvenanceBuiltinSubstitutionFloor.
	if got := child.Evaluate(context.Background(), sid, session.ModeDefault, goodCall, nil); got.Decision.Effect != governance.Ask || got.Decision.AskProvenance != governance.AskProvenanceBuiltinSubstitutionFloor {
		t.Fatalf("child ModeDefault substitution (read-only inner) must Ask with BuiltinSubstitutionFloor; got %+v", got)
	}
}

// TestYoloLoosensChildSubstitutionStrictDoesNot is the governance/permpolicy DECISION
// proof for the mode-driven substitution behavior: a child policy evaluated in
// ModeYolo ALLOWS a substitution/heredoc Shell command that ModeDefault and ModeAuto
// ASKS — AND the locked-at-every-tier invariants still hold: a ScopeManaged Deny
// still wins, and a configured (above-floor) Ask still asks. Uses the innocuous
// non-read-only inner `$(zap)`; nothing executes.
func TestYoloLoosensChildSubstitutionStrictDoesNot(t *testing.T) {
	const sid = session.SessionID("s1")
	subArgs, _ := json.Marshal(map[string]string{"command": "cat $(zap)"}) // non-read-only inner
	subCall := session.NewToolCall("c1", "Shell", subArgs)
	heredocArgs, _ := json.Marshal(map[string]string{"command": "python3 - <<'PY'\nprint('x')\nPY"})
	heredocCall := session.NewToolCall("c2", "Shell", heredocArgs)

	child := childPermPolicy(Config{})

	// ModeYolo: evalLoose + allowAllCLIRules → Allow for substitution and heredoc.
	for _, c := range []session.ToolCall{subCall, heredocCall} {
		if got := child.Evaluate(context.Background(), sid, session.ModeYolo, c, nil); got.Decision.Effect != governance.Allow {
			t.Fatalf("ModeYolo child substitution must Allow; got %v (%s)", got.Decision.Effect, got.Decision.Reason)
		}
	}

	// ModeAuto: standard evaluator + allowAllCLIRules → substitution still floors at Ask.
	for _, c := range []session.ToolCall{subCall, heredocCall} {
		if got := child.Evaluate(context.Background(), sid, session.ModeAuto, c, nil); got.Decision.Effect != governance.Allow && got.Decision.Effect != governance.Ask {
			t.Fatalf("ModeAuto child substitution must not auto-allow a non-read-only inner; got %v (%s)", got.Decision.Effect, got.Decision.Reason)
		}
	}
	// Be exact: bad-inner substitution ASKS under ModeAuto (the floored child path).
	if got := child.Evaluate(context.Background(), sid, session.ModeAuto, subCall, nil); got.Decision.Effect != governance.Ask {
		t.Fatalf("ModeAuto child substitution (bad inner) must Ask; got %v (%s)", got.Decision.Effect, got.Decision.Reason)
	}

	// ModeDefault: no extra rules → same Ask behavior as ModeAuto for substitutions.
	if got := child.Evaluate(context.Background(), sid, session.ModeDefault, subCall, nil); got.Decision.Effect != governance.Ask {
		t.Fatalf("ModeDefault child substitution (bad inner) must Ask; got %v (%s)", got.Decision.Effect, got.Decision.Reason)
	}

	// Locked-at-every-tier: a ScopeManaged Deny still wins under ModeYolo (deny-dominance).
	denyRules := append([]governance.Rule{
		{Scope: governance.ScopeManaged, Tool: "Shell", Effect: governance.Deny, Audience: governance.AudienceSubagent},
	}, childRules(Config{})...)
	denyPolicy := permpolicy.NewPolicy(denyRules, nil, childEvaluatorOptions(Config{})...)
	if got := denyPolicy.Evaluate(context.Background(), sid, session.ModeYolo, subCall, nil); got.Decision.Effect != governance.Deny {
		t.Fatalf("ModeYolo must NOT override a ScopeManaged Deny; got %v (%s)", got.Decision.Effect, got.Decision.Reason)
	}

	// Locked-at-every-tier: a CONFIGURED (above-floor) Ask still asks under ModeYolo.
	askRules := append([]governance.Rule{
		{Scope: governance.ScopeUser, Tool: "Shell", Effect: governance.Ask, Audience: governance.AudienceSubagent},
	}, childRules(Config{})...)
	askPolicy := permpolicy.NewPolicy(askRules, nil, childEvaluatorOptions(Config{})...)
	plainArgs, _ := json.Marshal(map[string]string{"command": "zap -rf build"})
	plainCall := session.NewToolCall("c3", "Shell", plainArgs)
	if got := askPolicy.Evaluate(context.Background(), sid, session.ModeYolo, plainCall, nil); got.Decision.Effect != governance.Ask {
		t.Fatalf("ModeYolo must NOT suppress a configured Ask; got %v (%s)", got.Decision.Effect, got.Decision.Reason)
	}

	// Locked-at-every-tier: PLAN MODE hard-denies a mutate even under ModeYolo.
	if got := child.Evaluate(context.Background(), sid, session.ModePlan, plainCall, nil); got.Decision.Effect != governance.Deny {
		t.Fatalf("plan mode must hard-deny a mutate even under ModeYolo; got %v (%s)", got.Decision.Effect, got.Decision.Reason)
	}
}

// TestAutoAndYoloModesInjectAllowAllInEvaluate is the KILL-SWITCH: proves that
// Policy.Evaluate with ModeAuto/ModeYolo returns Allow for mutating tools on the
// MAIN policy (which has defaultRules() mutate-ask floor). ModeDefault must Ask.
func TestAutoAndYoloModesInjectAllowAllInEvaluate(t *testing.T) {
	const sid = session.SessionID("s1")
	args, _ := json.Marshal(map[string]string{"command": "zap -rf build"}) // unknown-verb mutate stand-in
	call := session.NewToolCall("c1", "Shell", args)

	p := permpolicy.NewPolicy(mainRules(Config{}), nil, mainEvaluatorOptions(Config{})...)

	if got := p.Evaluate(context.Background(), sid, session.ModeAuto, call, nil); got.Decision.Effect != governance.Allow {
		t.Fatalf("main ModeAuto must Allow a plain mutate; got %v (%s)", got.Decision.Effect, got.Decision.Reason)
	}
	if got := p.Evaluate(context.Background(), sid, session.ModeYolo, call, nil); got.Decision.Effect != governance.Allow {
		t.Fatalf("main ModeYolo must Allow a plain mutate; got %v (%s)", got.Decision.Effect, got.Decision.Reason)
	}
	if got := p.Evaluate(context.Background(), sid, session.ModeDefault, call, nil); got.Decision.Effect != governance.Ask {
		t.Fatalf("main ModeDefault must Ask a plain mutate; got %v (%s)", got.Decision.Effect, got.Decision.Reason)
	}
}

// TestSubstitutionLooseningIsModeDriven codifies the mode-ladder rule for
// substitution loosening:
//   - Main policy in ModeAuto: substitution floors at Ask (standard evaluator + allowAll rules)
//   - Main policy in ModeYolo: substitution resolves Allow (evalLoose evaluator)
//   - Child policy in ModeAuto: substitution floors at Ask
//   - Child policy in ModeYolo: substitution resolves Allow
//
// A regression that loosened at ModeAuto (or stopped loosening at ModeYolo) flips
// one of these and fails here. Uses the innocuous non-read-only inner `$(zap)`.
func TestSubstitutionLooseningIsModeDriven(t *testing.T) {
	const sid = session.SessionID("s1")
	args, _ := json.Marshal(map[string]string{"command": "cat $(zap)"})
	call := session.NewToolCall("c1", "Shell", args)

	mainPolicy := permpolicy.NewPolicy(mainRules(Config{}), nil, mainEvaluatorOptions(Config{})...)
	childPolicy := childPermPolicy(Config{})

	type want struct {
		main  governance.Effect
		child governance.Effect
	}
	cases := []struct {
		mode session.PermissionMode
		want want
	}{
		{session.ModeDefault, want{governance.Ask, governance.Ask}},
		{session.ModeAuto, want{governance.Ask, governance.Ask}},     // auto: standard evaluator, substitution floor stands
		{session.ModeYolo, want{governance.Allow, governance.Allow}}, // yolo: evalLoose, substitution allowed
	}
	for _, tc := range cases {
		if got := mainPolicy.Evaluate(context.Background(), sid, tc.mode, call, nil).Decision.Effect; got != tc.want.main {
			t.Errorf("main %s substitution: got %v, want %v", tc.mode, got, tc.want.main)
		}
		if got := childPolicy.Evaluate(context.Background(), sid, tc.mode, call, nil).Decision.Effect; got != tc.want.child {
			t.Errorf("child %s substitution: got %v, want %v", tc.mode, got, tc.want.child)
		}
	}
}
