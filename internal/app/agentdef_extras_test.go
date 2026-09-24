package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	agents "github.com/stacklok/mecatl/engine/adapter/agentfs"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/internal/adapter/hookexec"
)

// writeSkill writes a <dir>/<name>/SKILL.md with the given description and body.
func writeSkill(t *testing.T, dir, name, desc, body string) {
	t.Helper()
	skillDir := filepath.Join(dir, name)
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	content := "---\nname: " + name + "\ndescription: " + desc + "\n---\n" + body
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(content), 0o644); err != nil {
		t.Fatalf("write skill: %v", err)
	}
}

// TestResolveSkillIndexAndPreload asserts a def's `skills:` preloads the matched
// skill bodies (and only those) and that an unknown name is reported as missing.
func TestResolveSkillIndexAndPreload(t *testing.T) {
	dir := t.TempDir()
	writeSkill(t, dir, "refactoring", "how to refactor", "REFACTOR PLAYBOOK")
	writeSkill(t, dir, "testing", "how to test", "TEST PLAYBOOK")

	idx := resolveSkillIndexForTest(t, Config{SkillsDirs: []string{dir}})
	if idx == nil || len(idx) != 2 {
		t.Fatalf("index = %v, want 2 skills", idx)
	}

	def := agents.AgentDef{Name: "spec", Skills: []string{"refactoring", "ghost"}}
	bodies, missing := preloadedSkillBodies(def, idx)
	if len(bodies) != 1 || !strings.Contains(bodies[0], "REFACTOR PLAYBOOK") {
		t.Fatalf("bodies = %v, want the refactoring body", bodies)
	}
	if !strings.Contains(bodies[0], "Skill (refactoring)") {
		t.Fatalf("body missing skill header: %q", bodies[0])
	}
	if len(missing) != 1 || missing[0] != "ghost" {
		t.Fatalf("missing = %v, want [ghost]", missing)
	}
}

// TestResolveSkillIndexDisabled asserts no skills dirs => a nil index, and a nil
// index makes every preload name "missing" (no panic).
func TestResolveSkillIndexDisabled(t *testing.T) {
	if idx := resolveSkillIndexForTest(t, Config{}); idx != nil {
		t.Fatalf("no skills dirs should yield a nil index, got %v", idx)
	}
	def := agents.AgentDef{Name: "x", Skills: []string{"a"}}
	bodies, missing := preloadedSkillBodies(def, nil)
	if len(bodies) != 0 || len(missing) != 1 {
		t.Fatalf("nil index: bodies=%v missing=%v", bodies, missing)
	}
}

// TestAgentPromptConfigPreloadsSkills asserts skill bodies are composed into the
// prompt Role alongside the def body.
func TestAgentPromptConfigPreloadsSkills(t *testing.T) {
	cfg := Config{Model: "m"}
	def := agents.AgentDef{Name: "spec", Body: "DEF BODY"}
	pc := agentPromptConfig(cfg, def, "m", "", "Skill (s):\n\nSKILL BODY")
	if !strings.Contains(pc.Role, "DEF BODY") {
		t.Fatalf("role missing def body:\n%s", pc.Role)
	}
	if !strings.Contains(pc.Role, "SKILL BODY") {
		t.Fatalf("role missing preloaded skill body:\n%s", pc.Role)
	}
}

// TestDefHookRunnerBuildsScopedRunner asserts a def's `hooks:` map produces a real
// runner that fires for the configured phase, and that an unknown phase is dropped.
func TestDefHookRunnerBuildsScopedRunner(t *testing.T) {
	cfg := Config{Shell: "/bin/sh"}
	def := agents.AgentDef{
		Name: "gated",
		Hooks: map[string]string{
			"PreToolUse": "exit 2", // a blocking PreToolUse hook
			"BogusPhase": "echo nope",
		},
	}
	fallback := hookexec.New(nil)
	r := defHookRunner(cfg, def, fallback)
	if r == fallback {
		t.Fatalf("def with a valid hook should NOT return the fallback runner")
	}

	// The PreToolUse hook (exit 2) blocks; the bogus phase was dropped, so a known
	// phase with no hook (PostToolUse) allows.
	out, err := r.Run(context.Background(), governance.HookEvent{Phase: governance.PhasePreToolUse})
	if err != nil {
		t.Fatalf("PreToolUse run: %v", err)
	}
	if !out.Outcome.Block {
		t.Fatalf("PreToolUse should block (exit 2), got allow")
	}
	post, err := r.Run(context.Background(), governance.HookEvent{Phase: governance.PhasePostToolUse})
	if err != nil {
		t.Fatalf("PostToolUse run: %v", err)
	}
	if post.Outcome.Block {
		t.Fatalf("PostToolUse should allow (no hook configured), got block")
	}
}

// TestDefHookRunnerNoHooksReturnsFallback asserts a def with no (valid) hooks
// adopts the fallback runner unchanged (no behaviour change for hook-less defs).
func TestDefHookRunnerNoHooksReturnsFallback(t *testing.T) {
	fallback := hookexec.New(nil)
	// No hooks at all.
	if r := defHookRunner(Config{}, agents.AgentDef{Name: "x"}, fallback); r != fallback {
		t.Fatalf("hook-less def should return the fallback runner")
	}
	// Only unknown phases => all dropped => fallback.
	def := agents.AgentDef{Name: "x", Hooks: map[string]string{"Nope": "echo x"}}
	if r := defHookRunner(Config{}, def, fallback); r != fallback {
		t.Fatalf("def with only unknown phases should return the fallback runner")
	}
}

// TestBuildAgentSubagentEnginesWithSkillsAndHooks is an end-to-end wiring smoke test:
// a def carrying both skills and hooks builds an engine without error and produces
// one meta entry (the per-def routing metadata) — exercising the full
// buildAgentSubagentEngines path with the new params populated.
func TestBuildAgentSubagentEnginesWithSkillsAndHooks(t *testing.T) {
	dir := t.TempDir()
	writeSkill(t, dir, "playbook", "a playbook", "PLAYBOOK BODY")
	idx := resolveSkillIndexForTest(t, Config{SkillsDirs: []string{dir}})

	reg := agents.NewRegistry([]agents.AgentDef{{
		Name:        "spec",
		Description: "a specialist",
		Skills:      []string{"playbook"},
		Hooks:       map[string]string{"PreToolUse": "exit 0"},
	}})
	prov := mockllm.New()
	engines, meta, _ := buildAgentSubagentEngines(context.Background(), Config{Model: "m", Shell: "/bin/sh"}, prov, regForTest(prov, providerMock, "m"), providerMock, "m", reg, idx, hookexec.New(nil), nil, nil)
	if len(engines) != 1 || engines["spec"] == nil {
		t.Fatalf("want 1 engine for 'spec', got %d", len(engines))
	}
	if len(meta) != 1 || meta[0].Name != "spec" {
		t.Fatalf("meta = %+v, want one 'spec' entry", meta)
	}
}
