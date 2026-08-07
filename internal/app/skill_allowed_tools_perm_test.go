package app

// skill_allowed_tools_perm_test.go pins the SECURITY invariant for the advisory
// `allowed-tools` frontmatter (agentskills.io, Experimental; issue #419): a
// skill declaring `allowed-tools: "Bash"` MUST NOT pre-approve, loosen, or grant
// a Bash call. The permission evaluator (governance/port.PermissionPolicy/
// engine/agent dispatch) NEVER reads AllowedTools — it is surfaced as an
// advisory note on activation only. mecatl's permission model is deny-dominant
// at every posture, so a Bash call still resolves through the normal policy to
// Ask in the DEFAULT posture (NOT auto-approved) even when a skill names it.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/skills"
)

// staticSkillsSource is a path-free Source over a fixed slice, the in-memory
// peer of the skillfs/staticSource test helper, so the security assertion can
// exercise the REAL NewFSSource seam without touching the filesystem.
type staticSkillsSource []skills.Skill

func (s staticSkillsSource) Skills(context.Context) ([]skills.Skill, []skills.SkipError, error) {
	return s, nil, nil
}

// TestAllowedToolsDoesNotBypassPolicy proves a skill's `allowed-tools` field is
// ADVISORY ONLY: it renders the activation note that EXPLICITLY says calls
// still follow normal permission rules, AND it does NOT change the resolved
// permission of a named tool — a Bash call still resolves to Ask under the
// DEFAULT posture (built-in defaultRules() AND the PRODUCTION mainRules
// assembly — the live ruleset buildEngine feeds the policy).
func TestAllowedToolsDoesNotBypassPolicy(t *testing.T) {
	ctx := context.Background()

	// Build the Skill tool over a skill declaring `allowed-tools: "Bash"`,
	// through the REAL seam (NewFSSource snapshot + NewSnapshotActivator).
	src, skips, err := skills.NewFSSource(ctx, staticSkillsSource{
		{
			Name:         "tooling",
			Description:  "a skill declaring allowed-tools: Bash",
			Body:         "Use Bash to run git.",
			AllowedTools: []string{"Bash"},
		},
	})
	if err != nil {
		t.Fatalf("NewFSSource: %v", err)
	}
	if len(skips) != 0 {
		t.Fatalf("unexpected skips: %v", skips)
	}
	metas, _ := src.ListSkills(ctx)
	tl := skills.NewTool(metas, skills.NewSnapshotActivator(src))

	// Precondition A: the skill carries the advisory allowed-tools field on
	// the port-shaped SkillMeta (the layer the catalog and policy see).
	if len(metas) != 1 || len(metas[0].AllowedTools) != 1 || metas[0].AllowedTools[0] != "Bash" {
		t.Fatalf("SkillMeta.AllowedTools = %v, want [Bash] (the field must reach the port)", metas)
	}

	// Precondition B: activation surfaces the advisory note — and it EXPLICITLY
	// states calls still follow normal permission rules, so the model does NOT
	// infer pre-approval from the field.
	res, err := tl.Execute(ctx, session.NewToolCall("id", skills.ToolName, mustMarshalArgs(t, map[string]any{"name": "tooling"})), nil)
	if err != nil || res.IsError {
		t.Fatalf("skill activation failed (res=%+v err=%v): the advisory note must render", res, err)
	}
	wantNote := "This skill declares allowed-tools: Bash. These are the tools the skill expects to use; each call still follows normal permission rules."
	if !strings.Contains(res.Content, wantNote) {
		t.Fatalf("activation must surface the advisory note stating calls still follow permission rules; got %q", res.Content)
	}

	// INVARIANT: a Bash tool call resolves to Ask in the DEFAULT posture under
	// BOTH the built-in defaultRules() AND the PRODUCTION mainRules(Config{})
	// assembly (the live ruleset the engine is built from). The skill's
	// allowed-tools declaration does NOT loosen, pre-approve, or grant it.
	for name, rules := range map[string][]governance.Rule{
		"defaultRules()":      defaultRules(),
		"mainRules(Config{})": mainRules(Config{}),
	} {
		policy := permpolicy.NewPolicy(rules, nil)
		call := session.NewToolCall("id", "Bash", json.RawMessage(`{"command":"echo hi"}`))
		got := policy.Evaluate(ctx, "s1", session.ModeDefault, call, nil).Effect
		if got != governance.Ask {
			t.Errorf("[%s] Bash call should resolve to Ask (allowed-tools must NOT pre-approve), got %v", name, got)
		}
	}
}

// mustMarshalArgs marshals m or fails the test.
func mustMarshalArgs(t *testing.T, m map[string]any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}
	return raw
}
