package sourceconformance

import (
	"context"
	"reflect"
	"testing"

	"github.com/stacklok/mecatl/engine/prompt"
)

// RuleFixture is the canonical rule set RunRulesSource asserts against, sorted
// by Name. It covers the three shapes the suite needs: an unconditional rule
// (no paths), a single-glob path-scoped rule, and a two-glob path-scoped rule.
//
// The fixture is authored to ROUND-TRIP the filesystem frontmatter parser:
// bodies are trimmed and globs are written as a YAML sequence — so the FS
// backend writing these out as <name>.md and re-discovering them answers
// byte-identically. Origin is deliberately UNSET here: each backend stamps its
// own admission tier, and the suite asserts only that it is non-empty.
var RuleFixture = []prompt.Rule{
	{
		Name: "api",
		Body: "Follow the API style guide for every endpoint.",
	},
	{
		Name:  "go-style",
		Body:  "Run gofmt and go vet before committing.",
		Paths: []string{"**/*.go", "go.mod"},
	},
	{
		Name:  "testing",
		Body:  "Write table-driven tests with t.Run subtests.",
		Paths: []string{"**/*_test.go"},
	},
}

// fixtureRule returns the fixture entry for name.
func fixtureRule(t *testing.T, name string) prompt.Rule {
	t.Helper()
	for _, r := range RuleFixture {
		if r.Name == name {
			return r
		}
	}
	t.Fatalf("no fixture rule %q", name)
	return prompt.Rule{}
}

// RunRulesSource executes the shared RulesSource conformance table against the
// source produced by newSource. newSource must return a fresh source serving
// EXACTLY the canonical RuleFixture each call.
func RunRulesSource(t *testing.T, newSource func(t *testing.T) prompt.RulesSource) {
	t.Helper()
	ctx := context.Background()

	t.Run("list matches fixture", func(t *testing.T) {
		src := newSource(t)
		rules, err := src.ListRules(ctx)
		if err != nil {
			t.Fatalf("ListRules: %v", err)
		}
		if len(rules) != len(RuleFixture) {
			t.Fatalf("ListRules returned %d rules, want %d: %+v", len(rules), len(RuleFixture), rules)
		}
		seen := map[string]bool{}
		for i, got := range rules {
			if seen[got.Name] {
				t.Errorf("duplicate rule name %q (names must be unique)", got.Name)
			}
			seen[got.Name] = true
			if i > 0 && rules[i-1].Name >= got.Name {
				t.Errorf("ListRules not sorted by name: %q before %q", rules[i-1].Name, got.Name)
			}
			if got.Origin == "" {
				t.Errorf("rule %q Origin is empty (a tier label is required for observability)", got.Name)
			}
			// Deep-equal EVERY field except Origin (each backend stamps its own
			// admission tier).
			want := fixtureRule(t, got.Name)
			want.Origin = got.Origin
			if !reflect.DeepEqual(got, want) {
				t.Errorf("rule %q mismatch:\n got %+v\nwant %+v", got.Name, got, want)
			}
		}
	})

	t.Run("list deterministic", func(t *testing.T) {
		src := newSource(t)
		first, err := src.ListRules(ctx)
		if err != nil {
			t.Fatalf("ListRules #1: %v", err)
		}
		second, err := src.ListRules(ctx)
		if err != nil {
			t.Fatalf("ListRules #2: %v", err)
		}
		if !reflect.DeepEqual(first, second) {
			t.Errorf("ListRules is not stable across calls (snapshot semantics):\n#1 %+v\n#2 %+v", first, second)
		}
	})

	t.Run("paths round trip", func(t *testing.T) {
		src := newSource(t)
		rules, err := src.ListRules(ctx)
		if err != nil {
			t.Fatalf("ListRules: %v", err)
		}
		for _, got := range rules {
			want := fixtureRule(t, got.Name)
			if !reflect.DeepEqual(got.Paths, want.Paths) {
				t.Errorf("rule %q Paths = %v, want %v", got.Name, got.Paths, want.Paths)
			}
		}
	})

	t.Run("caps respected", func(t *testing.T) {
		// Every listed rule must respect the canonical port-side cap
		// (prompt.MaxRuleBytes): the FS parser truncates on discovery, a driver
		// client re-truncates wire data, and the fixture itself is authored under
		// it — so an over-cap body escaping ANY backend is a normalization
		// regression.
		src := newSource(t)
		rules, err := src.ListRules(ctx)
		if err != nil {
			t.Fatalf("ListRules: %v", err)
		}
		for _, r := range rules {
			if len(r.Body) > prompt.MaxRuleBytes {
				t.Errorf("rule %q Body is %d bytes, over prompt.MaxRuleBytes (%d)",
					r.Name, len(r.Body), prompt.MaxRuleBytes)
			}
		}
	})
}

// NewRuleFixtureSource returns the in-memory REFERENCE prompt.RulesSource
// serving exactly the canonical RuleFixture, with the user tier stamped. It
// lives here (not in a _test.go file) because a future driver conformance
// fixture mounts it behind a wire server; it is also the suite's self-test
// subject, so the suite cannot smuggle filesystem-shaped assumptions.
func NewRuleFixtureSource() prompt.RulesSource {
	return ruleFixtureSource{}
}

type ruleFixtureSource struct{}

func (ruleFixtureSource) ListRules(_ context.Context) ([]prompt.Rule, error) {
	out := make([]prompt.Rule, len(RuleFixture))
	copy(out, RuleFixture)
	for i := range out {
		out[i].Origin = prompt.RuleOriginUser
	}
	return out, nil
}
