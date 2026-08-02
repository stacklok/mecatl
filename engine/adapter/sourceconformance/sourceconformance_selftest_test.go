package sourceconformance

import (
	"testing"

	"github.com/stacklok/mecatl/engine/prompt"
	"github.com/stacklok/mecatl/engine/tool"
)

// TestFixtureSourceConformance self-tests the suite against the in-memory
// reference source, so the suite cannot smuggle filesystem- or wire-shaped
// assumptions: anything RunSkillSource asserts must be satisfiable by a plain
// map over the canonical Fixture.
func TestFixtureSourceConformance(t *testing.T) {
	RunSkillSource(t, func(_ *testing.T) tool.SkillSource {
		return NewFixtureSource()
	})
}

// TestAgentFixtureSourceConformance self-tests the agent suite against the
// in-memory reference source, so anything RunAgentSource asserts must be
// satisfiable by a plain slice over the canonical AgentFixture.
func TestAgentFixtureSourceConformance(t *testing.T) {
	RunAgentSource(t, func(_ *testing.T) tool.AgentDefSource {
		return NewAgentFixtureSource()
	})
}

// TestCommandFixtureSourceConformance self-tests the command suite against the
// in-memory reference source — same rationale as the agent self-test.
func TestCommandFixtureSourceConformance(t *testing.T) {
	RunCommandSource(t, func(_ *testing.T) prompt.CommandSource {
		return NewCommandFixtureSource()
	})
}

// TestRuleFixtureSourceConformance self-tests the rules suite against the
// in-memory reference source — same rationale as the agent self-test.
func TestRuleFixtureSourceConformance(t *testing.T) {
	RunRulesSource(t, func(_ *testing.T) prompt.RulesSource {
		return NewRuleFixtureSource()
	})
}

// TestFixtureCoversTheThreeShapes pins the canonical fixture's shape contract
// (the suite's docs promise it): at least one skill with both a text and an
// executable asset, one asset-less skill, and one multi-segment logical name —
// so a backend passing the suite has demonstrably handled all three.
func TestFixtureCoversTheThreeShapes(t *testing.T) {
	var hasExec, hasText, hasAssetless, hasMultiSegment bool
	for _, f := range Fixture {
		if len(f.Assets) == 0 {
			hasAssetless = true
		}
		for _, a := range f.Assets {
			if a.Executable {
				hasExec = true
			} else {
				hasText = true
			}
			if countSlashes(a.Name) > 1 {
				hasMultiSegment = true
			}
			if !tool.ValidSkillAssetName(a.Name) {
				t.Errorf("fixture asset %q has an invalid logical name", a.Name)
			}
		}
	}
	if !hasExec || !hasText || !hasAssetless || !hasMultiSegment {
		t.Errorf("fixture must cover exec=%v text=%v asset-less=%v multi-segment=%v (all true)",
			hasExec, hasText, hasAssetless, hasMultiSegment)
	}
}

func countSlashes(s string) int {
	n := 0
	for _, r := range s {
		if r == '/' {
			n++
		}
	}
	return n
}
