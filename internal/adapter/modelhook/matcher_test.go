package modelhook

import "testing"

func TestResolveRuleSpecificityAndOrder(t *testing.T) {
	wildcard, ok := CompileRule(RuleSpec{Match: "*", Phases: []string{"pre"}, Mode: "advisory"})
	if !ok {
		t.Fatal("compile wildcard")
	}
	prefix, ok := CompileRule(RuleSpec{Match: "mcp__*", Phases: []string{"pre"}, Mode: "block"})
	if !ok {
		t.Fatal("compile prefix")
	}
	exact, ok := CompileRule(RuleSpec{Match: "mcp__github", Phases: []string{"pre"}, Mode: "advisory"})
	if !ok {
		t.Fatal("compile exact")
	}
	if got, found := ResolveRule([]CompiledRule{wildcard, exact, prefix}, "mcp__github", PhasePre); !found || got.Match() != "mcp__github" {
		t.Fatalf("exact resolution = (%q, %v)", got.Match(), found)
	}
	if got, found := ResolveRule([]CompiledRule{wildcard, exact, prefix}, "mcp__other", PhasePre); !found || got.Match() != "mcp__*" {
		t.Fatalf("prefix resolution = (%q, %v)", got.Match(), found)
	}
}

func TestCompiledRuleContextualShellSkip(t *testing.T) {
	rule, ok := CompileRule(RuleSpec{Match: "Shell", Phases: []string{"pre"}, Mode: "block", SkipReadOnlyShell: true})
	if !ok {
		t.Fatal("compile shell rule")
	}
	if !rule.SkipAction("Shell", `{"command":"git status"}`) {
		t.Fatal("provably read-only shell action was not skipped")
	}
	for _, input := range []string{`{"command":"rm a"}`, `{"command":"cat $(zap)"}`, `{}`} {
		if rule.SkipAction("Shell", input) {
			t.Fatalf("ambiguous or mutating shell action skipped: %s", input)
		}
	}
}

func TestCompileRuleRejectsInvalidContextualConfiguration(t *testing.T) {
	for _, spec := range []RuleSpec{
		{Match: "", Phases: []string{"pre"}, Mode: "block"},
		{Match: "Shell", Phases: []string{"unknown"}, Mode: "block"},
		{Match: "Shell", Phases: []string{"pre"}, Mode: "sanitize"},
	} {
		if _, ok := CompileRule(spec); ok {
			t.Fatalf("invalid rule compiled: %+v", spec)
		}
	}
}
