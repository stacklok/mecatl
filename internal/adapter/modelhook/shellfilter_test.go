package modelhook_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/internal/adapter/modelhook"
)

// countingChecker records how many times Check was called and returns a fixed verdict.
// It is the oracle for "did the read-only pre-filter skip the checker?" — calls==0
// proves the skip path fired (zero LLM calls), calls>=1 proves inspection.
type countingChecker struct {
	verdict modelhook.Verdict
	calls   int
}

func (c *countingChecker) Check(_ context.Context, _ modelhook.CheckRequest) (modelhook.CheckResult, error) {
	c.calls++
	return modelhook.CheckResult{Verdict: c.verdict}, nil
}

// shellSkipRule is the default-shaped Shell rule: pre/block with the read-only pre-filter on.
func shellSkipRule(t *testing.T) modelhook.CompiledRule {
	t.Helper()
	r, ok := modelhook.CompileRule(modelhook.RuleSpec{
		Match: "Shell", Phases: []string{"pre"}, Mode: string(modelhook.ModeBlock), SkipReadOnlyShell: true,
	})
	if !ok {
		t.Fatal("bash skip rule must compile")
	}
	return r
}

// shellArgs builds a Shell tool-call args JSON with a "command" field.
func shellArgs(cmd string) json.RawMessage {
	b, _ := json.Marshal(map[string]string{"command": cmd})
	return b
}

// runOne drives one Runner.Run for a Shell event in the given phase with the given raw
// args, returning the checker call count and the outcome.
func runOne(t *testing.T, rule modelhook.CompiledRule, phase governance.HookPhase, tool string, input json.RawMessage) (int, governance.HookOutcome) {
	t.Helper()
	chk := &countingChecker{verdict: modelhook.Verdict{Safe: boolp(true)}}
	r := modelhook.New(noopHooks{}, modelhook.Options{Rules: []modelhook.CompiledRule{rule}, Checker: chk})
	out, err := r.Run(context.Background(), governance.HookEvent{Phase: phase, Tool: tool, Input: input})
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}
	return chk.calls, out
}

// noopHooks is an inert inner HookRunner (allows every phase, mutates nothing).
type noopHooks struct{}

func (noopHooks) Run(context.Context, governance.HookEvent) (governance.HookOutcome, error) {
	return governance.HookOutcome{}, nil
}

// TestShellReadOnlySkipsChecker: a CONFIDENTLY read-only Pre Shell command skips the
// checker entirely (calls==0) and returns the zero (pass-through) outcome.
func TestShellReadOnlySkipsChecker(t *testing.T) {
	rule := shellSkipRule(t)
	readOnly := []string{
		"ls",
		"cat f",
		"grep x f",
		"git status",
		"git log",
		"timeout 5 ls",
		"cat $(ls)", // a fully-read-only substitution: SubstitutionReadOnly clears it.
	}
	for _, cmd := range readOnly {
		t.Run(cmd, func(t *testing.T) {
			calls, out := runOne(t, rule, governance.PhasePreToolUse, "Shell", shellArgs(cmd))
			if calls != 0 {
				t.Fatalf("read-only %q must skip the checker; calls=%d", cmd, calls)
			}
			if out.Block || len(out.Mutated) != 0 || out.Message != "" {
				t.Fatalf("read-only skip must return the zero outcome; got %+v", out)
			}
		})
	}
}

// TestShellMutatingIsInspected: a mutating command is NOT skipped — the checker runs
// once.
func TestShellMutatingIsInspected(t *testing.T) {
	rule := shellSkipRule(t)
	mutating := []string{
		"mkdir build",
		"touch out",
		"git commit -m x",
		"cp a b",
		"echo hi > f",
	}
	for _, cmd := range mutating {
		t.Run(cmd, func(t *testing.T) {
			calls, _ := runOne(t, rule, governance.PhasePreToolUse, "Shell", shellArgs(cmd))
			if calls != 1 {
				t.Fatalf("mutating %q must be inspected; calls=%d", cmd, calls)
			}
		})
	}
}

// TestShellAmbiguousIsInspected: a substitution-as-verb / not-provably-read-only command
// is inspected (fail-safe direction — never skipped on ambiguity).
func TestShellAmbiguousIsInspected(t *testing.T) {
	rule := shellSkipRule(t)
	ambiguous := []string{
		"$(printf ls)",     // command substitution in command position: executes its output.
		"`printf ls`",      // backtick in command position: same.
		"some-unknown-bin", // unknown verb: not provably read-only.
	}
	for _, cmd := range ambiguous {
		t.Run(cmd, func(t *testing.T) {
			calls, _ := runOne(t, rule, governance.PhasePreToolUse, "Shell", shellArgs(cmd))
			if calls != 1 {
				t.Fatalf("ambiguous %q must be inspected (fail-safe); calls=%d", cmd, calls)
			}
		})
	}
}

// TestShellFilterDoesNotApplyToPost: the same rule, but on the POST phase, the filter
// does NOT apply (a post-phase Shell rule would inspect; but this rule is pre-only, so a
// post event does not match and the checker is never called). To prove the filter is
// pre-only we use a rule that covers BOTH phases and confirm a read-only command is
// inspected on post.
func TestShellFilterDoesNotApplyToPost(t *testing.T) {
	r, ok := modelhook.CompileRule(modelhook.RuleSpec{
		Match: "Shell", Phases: []string{"pre", "post"}, Mode: string(modelhook.ModeBlock), SkipReadOnlyShell: true,
	})
	if !ok {
		t.Fatal("rule must compile")
	}
	// Post phase: the loop packs {args, content, is_error} — but the filter only ever
	// fires on Pre, so a read-only command on Post is still inspected.
	calls, _ := runOne(t, r, governance.PhasePostToolUse, "Shell", json.RawMessage(`{"content":"ls"}`))
	if calls != 1 {
		t.Fatalf("the read-only filter must NOT apply on the post phase; calls=%d", calls)
	}
}

// TestShellFilterDoesNotApplyToNonShell: a non-Shell tool with a skipReadOnlyShell rule is
// inspected normally (the filter is tool=="Shell" only).
func TestShellFilterDoesNotApplyToNonShell(t *testing.T) {
	// A "*" rule with the flag set, matching WebFetch on pre. The flag is inert for a
	// non-Shell tool, so the checker runs.
	r, ok := modelhook.CompileRule(modelhook.RuleSpec{
		Match: "*", Phases: []string{"pre"}, Mode: string(modelhook.ModeBlock), SkipReadOnlyShell: true,
	})
	if !ok {
		t.Fatal("rule must compile")
	}
	calls, _ := runOne(t, r, governance.PhasePreToolUse, "WebFetch", json.RawMessage(`{"url":"x"}`))
	if calls != 1 {
		t.Fatalf("the read-only filter must NOT apply to a non-Shell tool; calls=%d", calls)
	}
}

// TestShellMalformedArgsAreInspected: an unparseable args object OR a missing command
// field falls through to inspection (fail-safe — never presumed read-only).
func TestShellMalformedArgsAreInspected(t *testing.T) {
	rule := shellSkipRule(t)
	cases := []struct {
		name  string
		input json.RawMessage
	}{
		{"not json", json.RawMessage(`{not json`)},
		{"no command field", json.RawMessage(`{"foo":"ls"}`)},
		{"empty command", json.RawMessage(`{"command":"   "}`)},
		{"command not a string", json.RawMessage(`{"command":123}`)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			calls, _ := runOne(t, rule, governance.PhasePreToolUse, "Shell", c.input)
			if calls != 1 {
				t.Fatalf("malformed args (%s) must be inspected; calls=%d", c.name, calls)
			}
		})
	}
}

// TestShellCmdFallbackField: the "cmd" alias field is read when "command" is absent. A
// read-only "cmd" still skips.
func TestShellCmdFallbackField(t *testing.T) {
	rule := shellSkipRule(t)
	calls, _ := runOne(t, rule, governance.PhasePreToolUse, "Shell", json.RawMessage(`{"cmd":"ls"}`))
	if calls != 0 {
		t.Fatalf("a read-only command in the cmd field must skip; calls=%d", calls)
	}
}

// TestShellFilterWithoutFlagInspects: WITHOUT SkipReadOnlyShell a Shell pre rule inspects
// every command, read-only or not — proving the flag is the sole opt-in.
func TestShellFilterWithoutFlagInspects(t *testing.T) {
	r, ok := modelhook.CompileRule(modelhook.RuleSpec{
		Match: "Shell", Phases: []string{"pre"}, Mode: string(modelhook.ModeBlock),
	})
	if !ok {
		t.Fatal("rule must compile")
	}
	calls, _ := runOne(t, r, governance.PhasePreToolUse, "Shell", shellArgs("ls"))
	if calls != 1 {
		t.Fatalf("without the flag, a read-only Shell command must still be inspected; calls=%d", calls)
	}
}

// TestCompileRuleCarriesSkipReadOnlyShell: CompileRule threads the flag from RuleSpec
// into the CompiledRule (observable via the runtime skip behaviour, since the field is
// unexported). The behavioural check above (TestShellReadOnlySkipsChecker vs
// TestShellFilterWithoutFlagInspects) is the proof; this test pins the COMPILE step
// directly by toggling the flag and asserting the divergent outcome.
func TestCompileRuleCarriesSkipReadOnlyShell(t *testing.T) {
	with, ok1 := modelhook.CompileRule(modelhook.RuleSpec{Match: "Shell", Phases: []string{"pre"}, Mode: string(modelhook.ModeBlock), SkipReadOnlyShell: true})
	without, ok2 := modelhook.CompileRule(modelhook.RuleSpec{Match: "Shell", Phases: []string{"pre"}, Mode: string(modelhook.ModeBlock), SkipReadOnlyShell: false})
	if !ok1 || !ok2 {
		t.Fatal("both rules must compile")
	}
	if c, _ := runOne(t, with, governance.PhasePreToolUse, "Shell", shellArgs("ls")); c != 0 {
		t.Fatalf("SkipReadOnlyShell=true must skip a read-only command; calls=%d", c)
	}
	if c, _ := runOne(t, without, governance.PhasePreToolUse, "Shell", shellArgs("ls")); c != 1 {
		t.Fatalf("SkipReadOnlyShell=false must inspect a read-only command; calls=%d", c)
	}
}
