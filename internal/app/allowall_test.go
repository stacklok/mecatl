package app

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/session"
)

// TestMainRulesInjectsAllowAllWhenSet proves the AllowAllTools posture injects a
// single ScopeCLI allow-all rule into the main engine's static ruleset, and is a
// pure no-op (identical to defaultRules) when unset. See
// docs/adr/0022-allow-all-posture.md.
func TestMainRulesInjectsAllowAllWhenSet(t *testing.T) {
	withAllowAll := mainRules(Config{AllowAllTools: true})
	var found bool
	for _, r := range withAllowAll {
		if r.Scope == governance.ScopeCLI && r.Tool == "" && r.Pattern == "" && r.Effect == governance.Allow {
			found = true
			if r.Audience != governance.AudienceMain {
				t.Fatalf("the main allow-all rule must carry AudienceMain; got %v", r.Audience)
			}
			break
		}
	}
	if !found {
		t.Fatalf("AllowAllTools=true should inject a ScopeCLI empty-Tool/empty-Pattern Allow rule; rules=%+v", withAllowAll)
	}
	if len(withAllowAll) != len(defaultRules())+1 {
		t.Fatalf("AllowAllTools=true should add exactly one rule: got %d, want %d", len(withAllowAll), len(defaultRules())+1)
	}

	without := mainRules(Config{AllowAllTools: false})
	if len(without) != len(defaultRules()) {
		t.Fatalf("AllowAllTools=false should equal defaultRules(): got %d, want %d", len(without), len(defaultRules()))
	}
	for _, r := range without {
		if r.Scope == governance.ScopeCLI && r.Tool == "" && r.Pattern == "" && r.Effect == governance.Allow {
			t.Fatalf("AllowAllTools=false must NOT inject the allow-all rule; rules=%+v", without)
		}
	}
}

// TestMainRulesPolicyAutoAllowsAskFloor closes the loop on the structural check
// above: it drives mainRules through the REAL permpolicy evaluator and asserts the
// injected rule actually flips the built-in mutate-ask floor (Shell/Edit) from Ask
// to Allow when AllowAllTools is set, and leaves it at Ask when unset. This would
// catch a wrong scope (one that cannot loosen the ScopeBuiltinDefault floor) that
// the shape-only assertions miss. Benign commands only; nothing executes (the test
// stops at Evaluate).
func TestMainRulesPolicyAutoAllowsAskFloor(t *testing.T) {
	const sid = session.SessionID("s1")
	bashArgs, _ := json.Marshal(map[string]string{"command": "touch x"})
	bashCall := session.NewToolCall("c1", "Shell", bashArgs)
	editArgs, _ := json.Marshal(map[string]string{"file_path": "/x"})
	editCall := session.NewToolCall("c2", "Edit", editArgs)

	on := permpolicy.NewPolicy(mainRules(Config{AllowAllTools: true}), nil)
	if got := on.Evaluate(context.Background(), sid, session.ModeDefault, bashCall, nil); got.Effect != governance.Allow {
		t.Fatalf("AllowAllTools=true Shell: expected Allow, got %v", got.Effect)
	}
	if got := on.Evaluate(context.Background(), sid, session.ModeDefault, editCall, nil); got.Effect != governance.Allow {
		t.Fatalf("AllowAllTools=true Edit: expected Allow, got %v", got.Effect)
	}

	off := permpolicy.NewPolicy(mainRules(Config{AllowAllTools: false}), nil)
	if got := off.Evaluate(context.Background(), sid, session.ModeDefault, bashCall, nil); got.Effect != governance.Ask {
		t.Fatalf("AllowAllTools=false Shell: expected Ask, got %v", got.Effect)
	}
	if got := off.Evaluate(context.Background(), sid, session.ModeDefault, editCall, nil); got.Effect != governance.Ask {
		t.Fatalf("AllowAllTools=false Edit: expected Ask, got %v", got.Effect)
	}
}

// TestYoloLoosensSubstitutionFloorViaComposition (T3) proves the COMPOSITION wiring that
// connects Config.AllowAllTools → governance.WithLooseSubstitution is real, not silently
// disconnected. It builds the main policy the EXACT way buildEngine does (mainRules +
// mainEvaluatorOptions), then drives a substitution command through the real evaluator:
// with --yolo a non-read-only substitution resolves Allow; without it floors at Ask. A
// silent break in mainEvaluatorOptions would flip the yolo case back to Ask and fail
// here. Uses the innocuous stand-in `zap` (a non-read-only inner); nothing executes.
func TestYoloLoosensSubstitutionFloorViaComposition(t *testing.T) {
	const sid = session.SessionID("s1")
	args, _ := json.Marshal(map[string]string{"command": "cat $(zap)"})
	call := session.NewToolCall("c1", "Shell", args)

	// Built the SAME way buildEngine builds the main policy.
	yoloCfg := Config{AllowAllTools: true}
	yolo := permpolicy.NewPolicy(mainRules(yoloCfg), nil, mainEvaluatorOptions(yoloCfg)...)
	if got := yolo.Evaluate(context.Background(), sid, session.ModeDefault, call, nil); got.Effect != governance.Allow {
		t.Fatalf("--yolo substitution: expected Allow (the loose-substitution option must be wired), got %v (%s)", got.Effect, got.Reason)
	}

	plainCfg := Config{AllowAllTools: false}
	plain := permpolicy.NewPolicy(mainRules(plainCfg), nil, mainEvaluatorOptions(plainCfg)...)
	if got := plain.Evaluate(context.Background(), sid, session.ModeDefault, call, nil); got.Effect != governance.Ask {
		t.Fatalf("no-yolo substitution: expected Ask (floor stands), got %v (%s)", got.Effect, got.Reason)
	}
}

// TestChildRulesInjectsAllowAllWhenSet mirrors TestMainRulesInjectsAllowAllWhenSet
// for the CHILD ruleset: under --yolo childRules prepends exactly one
// ScopeCLI/empty-Tool/empty-Pattern/Allow/AudienceSubagent rule to the allow-all
// floor; with yolo off it equals AllowAllFloorRules() with no injected rule. This
// is the structural half of the team-member/subagent auto-approve fix.
func TestChildRulesInjectsAllowAllWhenSet(t *testing.T) {
	floor := permpolicy.AllowAllFloorRules()

	withAllowAll := childRules(Config{AllowAllTools: true})
	var found int
	for _, r := range withAllowAll {
		if r.Scope == governance.ScopeCLI && r.Tool == "" && r.Pattern == "" && r.Effect == governance.Allow {
			found++
			if r.Audience != governance.AudienceSubagent {
				t.Fatalf("the child allow-all rule must carry AudienceSubagent; got %v", r.Audience)
			}
		}
	}
	if found != 1 {
		t.Fatalf("AllowAllTools=true should inject exactly one ScopeCLI AudienceSubagent Allow rule; found %d in %+v", found, withAllowAll)
	}
	if len(withAllowAll) != len(floor)+1 {
		t.Fatalf("AllowAllTools=true should add exactly one rule: got %d, want %d", len(withAllowAll), len(floor)+1)
	}

	without := childRules(Config{AllowAllTools: false})
	if len(without) != len(floor) {
		t.Fatalf("AllowAllTools=false should equal AllowAllFloorRules(): got %d, want %d", len(without), len(floor))
	}
	for _, r := range without {
		if r.Scope == governance.ScopeCLI && r.Tool == "" && r.Pattern == "" && r.Effect == governance.Allow {
			t.Fatalf("AllowAllTools=false must NOT inject the ScopeCLI allow-all rule; rules=%+v", without)
		}
	}
}

// TestChildPolicyAutoApprovesNonSubstitution proves the child policy
// auto-approves a plain (non-read-only, non-substitution) mutate Shell command —
// and that this holds REGARDLESS of --yolo, because the child floor is a blanket
// allow-all (children have no mutate-ask floor to loosen, unlike the main
// engine's defaultRules()). This pins the empirical reality the
// team-member/subagent "bug" was misdiagnosed against: a child's plain mutate
// never prompted; only its substitution/heredoc commands floor at Ask (kept by
// TestYoloChildSubstitutionStillFailsSafe). Innocuous non-read-only stand-in;
// nothing executes.
func TestChildPolicyAutoApprovesNonSubstitution(t *testing.T) {
	const sid = session.SessionID("s1")
	args, _ := json.Marshal(map[string]string{"command": "python3 script.py"})
	call := session.NewToolCall("c1", "Shell", args)

	for _, yolo := range []bool{true, false} {
		p := childPermPolicy(Config{AllowAllTools: yolo})
		if got := p.Evaluate(context.Background(), sid, session.ModeDefault, call, nil); got.Effect != governance.Allow {
			t.Fatalf("child plain mutate (yolo=%v): expected Allow (blanket floor), got %v (%s)", yolo, got.Effect, got.Reason)
		}
	}
}

// TestYoloChildSubstitutionStillFailsSafe is the SAFETY kill test: under --yolo a
// child's substitution command with a NON-read-only inner ($(zap)) must STILL
// floor at Ask, with neither FlooredConfiguredAllow nor ConfiguredAsk set (the
// loosening is main-only; the child resolves through the child-ask model). The
// paired positive: a substitution with a READ-ONLY inner clears the
// flooredAllowSafe inner contract → FlooredConfiguredAllow, still surfaced as Ask.
func TestYoloChildSubstitutionStillFailsSafe(t *testing.T) {
	const sid = session.SessionID("s1")
	child := childPermPolicy(Config{AllowAllTools: true})

	badArgs, _ := json.Marshal(map[string]string{"command": "cat $(zap)"}) // non-read-only inner
	badCall := session.NewToolCall("c1", "Shell", badArgs)
	if got := child.Evaluate(context.Background(), sid, session.ModeDefault, badCall, nil); got.Effect != governance.Ask || got.FlooredConfiguredAllow || got.ConfiguredAsk {
		t.Fatalf("--yolo child substitution (bad inner) must Ask with no Floored/Configured bits; got %+v", got)
	}

	goodArgs, _ := json.Marshal(map[string]string{"command": "go test $(git rev-parse HEAD)"}) // read-only inner
	goodCall := session.NewToolCall("c2", "Shell", goodArgs)
	if got := child.Evaluate(context.Background(), sid, session.ModeDefault, goodCall, nil); got.Effect != governance.Ask || !got.FlooredConfiguredAllow {
		t.Fatalf("--yolo child substitution (read-only inner) must Ask with FlooredConfiguredAllow; got %+v", got)
	}
}

// TestYoloLoosensChildSubstitutionStrictDoesNot is the governance/permpolicy DECISION
// proof for the posture ladder's headline behaviour change: a CHILD policy built with
// LooseChildSubstitution=true (yolo) ALLOWS a substitution/heredoc Shell command that
// the same policy with it false (strict/trusted/auto) ASKS — AND the locked-at-every-
// tier invariants still hold under yolo: a ScopeManaged Deny still wins, and a
// configured (above-floor) Ask still asks. Innocuous non-read-only stand-ins only;
// nothing executes (the test stops at Evaluate).
func TestYoloLoosensChildSubstitutionStrictDoesNot(t *testing.T) {
	const sid = session.SessionID("s1")
	subArgs, _ := json.Marshal(map[string]string{"command": "cat $(zap)"}) // non-read-only inner
	subCall := session.NewToolCall("c1", "Shell", subArgs)
	heredocArgs, _ := json.Marshal(map[string]string{"command": "python3 - <<'PY'\nprint('x')\nPY"})
	heredocCall := session.NewToolCall("c2", "Shell", heredocArgs)

	yolo := childPermPolicy(Config{LooseChildSubstitution: true})
	for _, c := range []session.ToolCall{subCall, heredocCall} {
		if got := yolo.Evaluate(context.Background(), sid, session.ModeDefault, c, nil); got.Effect != governance.Allow {
			t.Fatalf("yolo child substitution must Allow; got %v (%s)", got.Effect, got.Reason)
		}
	}

	notYolo := childPermPolicy(Config{LooseChildSubstitution: false})
	for _, c := range []session.ToolCall{subCall, heredocCall} {
		if got := notYolo.Evaluate(context.Background(), sid, session.ModeDefault, c, nil); got.Effect != governance.Allow && got.Effect != governance.Ask {
			t.Fatalf("non-yolo child substitution must not auto-allow a non-read-only inner; got %v (%s)", got.Effect, got.Reason)
		}
	}
	// Be exact: the bad-inner substitution ASKS under non-yolo (the floored child path).
	if got := notYolo.Evaluate(context.Background(), sid, session.ModeDefault, subCall, nil); got.Effect != governance.Ask {
		t.Fatalf("non-yolo child substitution (bad inner) must Ask; got %v (%s)", got.Effect, got.Reason)
	}

	// Locked-at-every-tier: a ScopeManaged Deny still wins under yolo (deny-dominance).
	denyRules := append([]governance.Rule{
		{Scope: governance.ScopeManaged, Tool: "Shell", Effect: governance.Deny, Audience: governance.AudienceSubagent},
	}, childRules(Config{LooseChildSubstitution: true})...)
	denyPolicy := permpolicy.NewPolicy(denyRules, nil, childEvaluatorOptions(Config{LooseChildSubstitution: true})...)
	if got := denyPolicy.Evaluate(context.Background(), sid, session.ModeDefault, subCall, nil); got.Effect != governance.Deny {
		t.Fatalf("yolo must NOT override a ScopeManaged Deny; got %v (%s)", got.Effect, got.Reason)
	}

	// Locked-at-every-tier: a CONFIGURED (above-floor) Ask still asks under yolo.
	askRules := append([]governance.Rule{
		{Scope: governance.ScopeUser, Tool: "Shell", Effect: governance.Ask, Audience: governance.AudienceSubagent},
	}, childRules(Config{LooseChildSubstitution: true})...)
	askPolicy := permpolicy.NewPolicy(askRules, nil, childEvaluatorOptions(Config{LooseChildSubstitution: true})...)
	plainArgs, _ := json.Marshal(map[string]string{"command": "zap -rf build"})
	plainCall := session.NewToolCall("c3", "Shell", plainArgs)
	if got := askPolicy.Evaluate(context.Background(), sid, session.ModeDefault, plainCall, nil); got.Effect != governance.Ask {
		t.Fatalf("yolo must NOT suppress a configured Ask; got %v (%s)", got.Effect, got.Reason)
	}

	// Locked-at-every-tier: PLAN MODE hard-denies a mutate even under yolo (plan-mode
	// hard-deny runs FIRST, before any loosening). A plain mutate that yolo would
	// otherwise allow must Deny under ModePlan.
	if got := yolo.Evaluate(context.Background(), sid, session.ModePlan, plainCall, nil); got.Effect != governance.Deny {
		t.Fatalf("plan mode must hard-deny a mutate even under yolo; got %v (%s)", got.Effect, got.Reason)
	}
}

// TestAllowAllToolsBindsMainAndChildren is the KILL-SWITCH for the regression
// class the user explicitly wants blocked: a future knob that touches only
// mainRules (and not the shared yoloAllowAllRule) would let the main and child
// allow-all rulesets diverge again. It pins that under --yolo the SAME ScopeCLI
// empty-Tool/empty-Pattern Allow rule is present in BOTH rulesets (mainRules
// carries AudienceMain, childRules AudienceSubagent) and ABSENT from both with
// yolo off. It does NOT assert a child Ask→Allow flip for a plain mutate: the
// child floor is a blanket allow-all, so a plain child mutate is Allow either
// way (TestChildPolicyAutoApprovesNonSubstitution) — only the MAIN engine has a
// mutate-ask floor for the rule to flip, asserted here via the real evaluator.
// The substitution floor stays for children regardless (pinned by
// TestSubstitutionLooseningStaysMainOnly). Mirrors the #42 catalog-drift
// kill-switch discipline in catalog_drift_test.go.
func TestAllowAllToolsBindsMainAndChildren(t *testing.T) {
	hasAllowAll := func(rules []governance.Rule, want governance.Audience) bool {
		for _, r := range rules {
			if r.Scope == governance.ScopeCLI && r.Tool == "" && r.Pattern == "" && r.Effect == governance.Allow && r.Audience == want {
				return true
			}
		}
		return false
	}

	rulesets := []struct {
		name     string
		rules    func(Config) []governance.Rule
		audience governance.Audience
	}{
		{"main", mainRules, governance.AudienceMain},
		{"child", childRules, governance.AudienceSubagent},
	}
	for _, rs := range rulesets {
		if !hasAllowAll(rs.rules(Config{AllowAllTools: true}), rs.audience) {
			t.Fatalf("%s ruleset under --yolo must contain the ScopeCLI %v allow-all rule", rs.name, rs.audience)
		}
		if hasAllowAll(rs.rules(Config{AllowAllTools: false}), rs.audience) {
			t.Fatalf("%s ruleset without --yolo must NOT contain the ScopeCLI allow-all rule", rs.name)
		}
	}

	// The MAIN engine has a mutate-ask floor for the rule to flip; drive it
	// through the real evaluator to prove the rule is wired, not inert.
	const sid = session.SessionID("s1")
	args, _ := json.Marshal(map[string]string{"command": "zap -rf build"}) // unknown-verb mutate stand-in
	call := session.NewToolCall("c1", "Shell", args)
	on := permpolicy.NewPolicy(mainRules(Config{AllowAllTools: true}), nil, mainEvaluatorOptions(Config{AllowAllTools: true})...)
	if got := on.Evaluate(context.Background(), sid, session.ModeDefault, call, nil); got.Effect != governance.Allow {
		t.Fatalf("main posture under --yolo must Allow a plain mutate; got %v (%s)", got.Effect, got.Reason)
	}
	off := permpolicy.NewPolicy(mainRules(Config{AllowAllTools: false}), nil, mainEvaluatorOptions(Config{AllowAllTools: false})...)
	if got := off.Evaluate(context.Background(), sid, session.ModeDefault, call, nil); got.Effect != governance.Ask {
		t.Fatalf("main posture without --yolo must Ask a plain mutate; got %v (%s)", got.Effect, got.Reason)
	}
}

// TestChildSubstitutionLooseningIsTierDependent (evolved from the historical
// TestSubstitutionLooseningStaysMainOnly) codifies the posture-ladder rule that the
// CHILD substitution-floor loosening is YOLO-ONLY, while the MAIN loosening fires at
// auto+yolo. It drives the EVALUATOR-OPTION seam directly (ruleset held constant at
// the floor allow-all) so only the option set under test varies:
//
//   - mainEvaluatorOptions includes WithLooseSubstitution for auto+yolo, omits it for
//     strict/trusted (a substitution then ALLOWS vs ASKS).
//   - childEvaluatorOptions includes WithLooseSubstitution ONLY for yolo
//     (LooseChildSubstitution); it OMITS it for strict/trusted/auto (the child
//     substitution floor stands — the prompt-injection defense is ON).
//
// A regression that loosened children at auto (or stopped loosening them at yolo)
// flips one of these and fails here. Uses the innocuous non-read-only inner `$(zap)`.
func TestChildSubstitutionLooseningIsTierDependent(t *testing.T) {
	const sid = session.SessionID("s1")
	args, _ := json.Marshal(map[string]string{"command": "cat $(zap)"})
	call := session.NewToolCall("c1", "Shell", args)

	eval := func(opts ...governance.EvaluatorOption) governance.Effect {
		p := permpolicy.NewPolicy(permpolicy.AllowAllFloorRules(), nil, opts...)
		return p.Evaluate(context.Background(), sid, session.ModeDefault, call, nil).Effect
	}

	// applyPosture maps each tier to (AllowAllTools, LooseChildSubstitution); we drive
	// the option builders with the derived cfg so the test exercises the SAME mapping
	// composition uses.
	type want struct {
		main  governance.Effect
		child governance.Effect
	}
	cases := []struct {
		posture Posture
		want    want
	}{
		{PostureStrict, want{governance.Ask, governance.Ask}},
		{PostureTrusted, want{governance.Ask, governance.Ask}},
		{PostureAuto, want{governance.Allow, governance.Ask}},   // child defense ON
		{PostureYolo, want{governance.Allow, governance.Allow}}, // child defense OFF
	}
	for _, tc := range cases {
		cfg := applyPosture(Config{Posture: tc.posture})
		if got := eval(mainEvaluatorOptions(cfg)...); got != tc.want.main {
			t.Fatalf("%s main substitution: got %v, want %v", tc.posture, got, tc.want.main)
		}
		if got := eval(childEvaluatorOptions(cfg)...); got != tc.want.child {
			t.Fatalf("%s child substitution: got %v, want %v", tc.posture, got, tc.want.child)
		}
	}
}
