package governance

import (
	"encoding/json"
	"testing"
)

// editArgs builds Edit-shaped args (path field) for the learn tests.
func editArgs(p string) json.RawMessage {
	b, _ := json.Marshal(map[string]string{"path": p})
	return b
}

// --- EvaluateWith: learned allow interacts correctly with the precedence rules ---

// (a) A learned Allow makes a matching call resolve Allow when no static rule
// matches (the default would otherwise be Ask).
func TestEvaluateWithLearnedAllowMatches(t *testing.T) {
	e := NewEvaluator(nil) // no static rules → bare call is Ask
	learned, ok := e.LearnableRule("Shell", shellArgs("git status"))
	if !ok {
		t.Fatalf("expected git status to be learnable")
	}
	got := e.EvaluateWith("Shell", shellArgs("git status"), false, []Rule{learned})
	if got.Effect != Allow {
		t.Fatalf("expected Allow from learned rule, got %v (%s)", got.Effect, got.Reason)
	}
	// And without the extra it stays Ask — the learned rule never mutates the Evaluator.
	if got := e.Evaluate("Shell", shellArgs("git status"), false); got.Effect != Ask {
		t.Fatalf("expected Ask without learned rule (immutability), got %v", got.Effect)
	}
}

// (b) A static Deny beats a learned Allow for the same tool+pattern.
func TestEvaluateWithStaticDenyBeatsLearnedAllow(t *testing.T) {
	e := NewEvaluator([]Rule{
		{Scope: ScopeManaged, Tool: "Shell", Pattern: "git status", Effect: Deny},
	})
	learned := Rule{Scope: ScopeUser, Tool: "Shell", Pattern: "git status", Effect: Allow, Exact: true}
	if got := e.EvaluateWith("Shell", shellArgs("git status"), false, []Rule{learned}); got.Effect != Deny {
		t.Fatalf("expected Deny to beat learned Allow, got %v", got.Effect)
	}
}

// (c) A static Ask beats a learned Allow for the same tool+pattern.
func TestEvaluateWithStaticAskBeatsLearnedAllow(t *testing.T) {
	e := NewEvaluator([]Rule{
		{Scope: ScopeManaged, Tool: "Shell", Pattern: "git status", Effect: Ask},
	})
	learned := Rule{Scope: ScopeUser, Tool: "Shell", Pattern: "git status", Effect: Allow, Exact: true}
	if got := e.EvaluateWith("Shell", shellArgs("git status"), false, []Rule{learned}); got.Effect != Ask {
		t.Fatalf("expected Ask to beat learned Allow, got %v", got.Effect)
	}
}

// (d) Plan mode hard-denies a mutating tool even with a learned Allow present.
func TestEvaluateWithPlanModeDeniesMutatingDespiteLearnedAllow(t *testing.T) {
	e := NewEvaluator(nil)
	learned := Rule{Scope: ScopeUser, Tool: "Write", Pattern: "/tmp/x", Effect: Allow, Exact: true}
	got := e.EvaluateWith("Write", editArgs("/tmp/x"), true, []Rule{learned})
	if got.Effect != Deny {
		t.Fatalf("expected plan-mode Deny to beat learned Allow for a mutating tool, got %v", got.Effect)
	}
}

// (e) A learned EXACT pattern matches ONLY its literal — never via glob — so a
// learned `git*` (were it ever stored) does not green-light `git push`.
func TestEvaluateWithLearnedExactNoGlob(t *testing.T) {
	e := NewEvaluator(nil)
	// A pattern containing a glob metachar, stored Exact: it must match literally.
	learned := Rule{Scope: ScopeUser, Tool: "Shell", Pattern: "git*", Effect: Allow, Exact: true}
	// The literal "git*" command matches.
	if got := e.EvaluateWith("Shell", shellArgs("git*"), false, []Rule{learned}); got.Effect != Allow {
		t.Fatalf("expected Allow for the literal learned pattern, got %v", got.Effect)
	}
	// A DIFFERENT command must NOT be approved by glob expansion of "git*".
	if got := e.EvaluateWith("Shell", shellArgs("git push"), false, []Rule{learned}); got.Effect != Ask {
		t.Fatalf("expected Ask (no glob escalation) for a different command, got %v", got.Effect)
	}
}

// --- LearnableRule: which calls are learnable, and the pattern derived ---

func TestLearnableRuleSingleShellCommand(t *testing.T) {
	e := NewEvaluator(nil)
	r, ok := e.LearnableRule("Shell", shellArgs("git   status"))
	if !ok {
		t.Fatalf("expected single bash command to be learnable")
	}
	if r.Tool != "Shell" {
		t.Fatalf("expected Tool=Shell, got %q", r.Tool)
	}
	if r.Effect != Allow || !r.Exact || r.Scope != ScopeUser {
		t.Fatalf("expected {Allow, Exact, ScopeUser}, got %+v", r)
	}
	// The pattern is the CANONICAL form (collapsed whitespace), so it matches the
	// canonicalized call the evaluator builds.
	if r.Pattern != Canonicalize("git   status") {
		t.Fatalf("expected canonical pattern %q, got %q", Canonicalize("git   status"), r.Pattern)
	}
}

func TestLearnableRuleRefusesCompoundAndSubstituted(t *testing.T) {
	e := NewEvaluator(nil)
	for _, cmd := range []string{
		"git status && rm -rf /", // &&
		"git status; rm -rf /",   // ;
		"echo $(rm -rf /)",       // command substitution
		"echo `rm -rf /`",        // backticks
		"(rm -rf /)",             // subshell grouping
		"",                       // empty
	} {
		if _, ok := e.LearnableRule("Shell", shellArgs(cmd)); ok {
			t.Fatalf("expected %q NOT to be learnable", cmd)
		}
	}
}

func TestLearnableRuleEditWithPathExact(t *testing.T) {
	e := NewEvaluator(nil)
	r, ok := e.LearnableRule("Edit", editArgs("/repo/main.go"))
	if !ok {
		t.Fatalf("expected Edit with a path to be learnable")
	}
	if r.Tool != "Edit" || r.Pattern != "/repo/main.go" || r.Effect != Allow || !r.Exact {
		t.Fatalf("expected exact Edit allow for the path, got %+v", r)
	}
}

func TestLearnableRuleRefusesEmptyPattern(t *testing.T) {
	e := NewEvaluator(nil)
	// A tool call with no targetable field derives an empty pattern → never learn a
	// tool-wide allow.
	if _, ok := e.LearnableRule("Read", json.RawMessage(`{"unrelated":"x"}`)); ok {
		t.Fatalf("expected no-targetable-field call NOT to be learnable (no tool-wide allow)")
	}
	if _, ok := e.LearnableRule("Read", nil); ok {
		t.Fatalf("expected nil-args call NOT to be learnable")
	}
}
