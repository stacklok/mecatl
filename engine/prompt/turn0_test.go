package prompt_test

import (
	"context"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/prompt"
	"github.com/stacklok/mecatl/engine/tool"
)

// scriptedSoul / scriptedIndex / scriptedRules are minimal sources so the test drives
// the REAL render path of each assembler — IsInjectedTurn0Fragment is asserted against
// the actual rendered bytes, not a hand-copied header literal, so it cannot drift from
// what the assemblers emit.
type scriptedSoul struct{ body string }

func (s scriptedSoul) Load(context.Context) (string, error) { return s.body, nil }

type scriptedIndex struct{ entries []tool.MemoryEntry }

func (s scriptedIndex) Index(context.Context) ([]tool.MemoryEntry, error) { return s.entries, nil }

type scriptedRules struct{ rules []prompt.Rule }

func (s scriptedRules) ListRules(context.Context) ([]prompt.Rule, error) { return s.rules, nil }

// TestIsInjectedTurn0FragmentRecognisesRealAssemblerOutput drives each of the five
// turn-0 assemblers (project instructions / rules / soul / memory index / user model)
// and asserts IsInjectedTurn0Fragment recognises the message each ACTUALLY renders,
// while rejecting a genuine user instruction. Because the input is the real render
// output, a header reword in an assembler that also missed turn0.go's
// source-of-truth const would surface here.
func TestIsInjectedTurn0FragmentRecognisesRealAssemblerOutput(t *testing.T) {
	ctx := context.Background()

	render := func(a prompt.InstructionAssembler, ws tool.Workspace) string {
		t.Helper()
		msgs, err := a.Assemble(ctx, ws)
		if err != nil {
			t.Fatalf("Assemble: %v", err)
		}
		if len(msgs) != 1 {
			t.Fatalf("expected exactly one assembled message, got %d", len(msgs))
		}
		return msgs[0].Text
	}

	agentsWS := memfs.NewWorkspace("/proj")
	mustWrite(t, agentsWS, "AGENTS.md", "follow the house style")
	claudeWS := memfs.NewWorkspace("/proj2")
	mustWrite(t, claudeWS, "CLAUDE.md", "fallback project rule")

	entries := []tool.MemoryEntry{{Key: "pref/runner", Description: "preferred test runner"}}
	cases := []struct {
		name string
		text string
	}{
		{"project-instructions (AGENTS.md)", render(prompt.RootAssembler{}, agentsWS)},
		{"project-instructions (CLAUDE.md)", render(prompt.RootAssembler{}, claudeWS)},
		{"rules", render(prompt.RulesAssembler{Src: scriptedRules{rules: []prompt.Rule{{Name: "r1", Body: "body\n", Origin: prompt.RuleOriginProject}}}}, nil)},
		{"soul", render(prompt.SoulAssembler{Src: scriptedSoul{body: "terse engineer"}}, agentsWS)},
		{"memory-index", render(prompt.MemoryIndexAssembler{Src: scriptedIndex{entries: entries}}, agentsWS)},
		{"user-model", render(prompt.UserModelAssembler{Src: scriptedIndex{entries: entries}}, agentsWS)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if !prompt.IsInjectedTurn0Fragment(tc.text) {
				t.Fatalf("IsInjectedTurn0Fragment did not recognise the %s fragment:\n%s", tc.name, tc.text)
			}
		})
	}

	// Negatives: a genuine user instruction (and the empty string) must NOT be
	// recognised. The third deliberately starts with the words "Project
	// instructions" but is a genuine question — it does NOT match the marker prefix
	// "Project instructions (" (note the open paren), so it must read as a real turn.
	for _, neg := range []string{
		"rename Foo to Bar across the package",
		"",
		"Project instructions are unclear, please clarify.",
	} {
		if prompt.IsInjectedTurn0Fragment(neg) {
			t.Fatalf("IsInjectedTurn0Fragment falsely flagged a genuine user turn: %q", neg)
		}
	}
}
