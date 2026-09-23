package app

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stacklok/mecatl/engine/prompt"
	"github.com/stacklok/mecatl/internal/adapter/osfs"
	"github.com/stacklok/mecatl/internal/adapter/skills"
)

// trust_gate_test.go covers the Workspace-Trust Phase-2a composition gate
// (MUST-FIX 1 / R2.4): when a workspace is UNTRUSTED, ONLY the PROJECT TIER of
// agents / commands / skills is withheld, while the operator's USER-TIER config
// stays fully active and the loop still works ("ask the human" mode, not "do
// nothing"). The positive counterpart asserts a trusted workspace admits the
// project tier.
//
// cfg.TrustProject below stands in for the folded TrustDecision: Build collapses
// resolveTrust onto cfg.TrustProject BEFORE buildEngine runs, so the build
// functions read only the effective bool. The tests set it directly, exactly as
// Build would after the fold.

// writeAgentDef writes a <dir>/<name>.md agent definition.
func writeAgentDef(t *testing.T, dir, name, desc, body string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir agent dir: %v", err)
	}
	content := "---\nname: " + name + "\ndescription: " + desc + "\n---\n" + body
	if err := os.WriteFile(filepath.Join(dir, name+".md"), []byte(content), 0o644); err != nil {
		t.Fatalf("write agent def: %v", err)
	}
}

// writeCommand writes a <ws>/<reldir>/<name>.md slash-command template.
func writeCommand(t *testing.T, ws, reldir, name, body string) {
	t.Helper()
	dir := filepath.Join(ws, reldir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir command dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, name+".md"), []byte(body), 0o644); err != nil {
		t.Fatalf("write command: %v", err)
	}
}

// fakeUserConfigEnv points the real-env user-tier resolution (xdgconfig.OSEnv,
// used by agents.ResolveSources / skills.ResolveSources in internal/app) at an
// offline temp dir, so the "user tier stays active" assertions never read the
// developer's real ~/.config or ~/.claude.
func fakeUserConfigEnv(t *testing.T, xdg, home string) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	t.Setenv("HOME", home)
}

// TestUntrustedWorkspaceWithholdsProjectAgentsKeepsUser proves an untrusted
// workspace drops the PROJECT-tier agent def but keeps the USER-tier one
// (R2.4 — both sides). The trusted counterpart admits both.
func TestUntrustedWorkspaceWithholdsProjectAgentsKeepsUser(t *testing.T) {
	ws := t.TempDir()
	xdg := t.TempDir()
	home := t.TempDir()
	fakeUserConfigEnv(t, xdg, home)

	// Project-tier def (repo-injected) and user-tier def (operator's own).
	writeAgentDef(t, filepath.Join(ws, ".claude", "agents"), "evil", "repo persona", "STEER")
	writeAgentDef(t, filepath.Join(xdg, "mecatl", "agents"), "mine", "my own persona", "OK")

	// Untrusted: project def withheld, user def active.
	regUntrusted := resolveAgentRegistry(context.Background(), Config{
		Workspace:          ws,
		AgentsConventional: true,
		TrustProject:       false,
	})
	if _, ok := regUntrusted.Get("evil"); ok {
		t.Error("untrusted workspace admitted a PROJECT-tier agent def (security gap)")
	}
	if _, ok := regUntrusted.Get("mine"); !ok {
		t.Error("untrusted workspace dropped the USER-tier agent def (over-gating; must stay active)")
	}

	// Trusted + ingestion granted: both admitted.
	regTrusted := resolveAgentRegistry(context.Background(), Config{
		Workspace:          ws,
		AgentsConventional: true,
		TrustProject:       true,
	})
	if _, ok := regTrusted.Get("evil"); !ok {
		t.Error("trusted workspace must admit the project-tier agent def")
	}
	if _, ok := regTrusted.Get("mine"); !ok {
		t.Error("trusted workspace must still admit the user-tier agent def")
	}
}

// TestUntrustedWorkspaceWithholdsProjectSkillsKeepsUser proves an untrusted
// workspace drops the PROJECT-tier skill but keeps the USER-tier one (R2.4).
func TestUntrustedWorkspaceWithholdsProjectSkillsKeepsUser(t *testing.T) {
	ws := t.TempDir()
	xdg := t.TempDir()
	home := t.TempDir()
	fakeUserConfigEnv(t, xdg, home)

	writeSkill(t, filepath.Join(ws, ".claude", "skills"), "repo-skill", "from the repo", "REPO BODY")
	writeSkill(t, filepath.Join(xdg, "mecatl", "skills"), "user-skill", "my own", "USER BODY")

	discUntrusted := resolveSkillsForTest(t, Config{
		Workspace:          ws,
		SkillsConventional: true,
		TrustProject:       false,
	})
	if skillsContain(discUntrusted, "repo-skill") {
		t.Error("untrusted workspace admitted a PROJECT-tier skill (security gap)")
	}
	if !skillsContain(discUntrusted, "user-skill") {
		t.Error("untrusted workspace dropped the USER-tier skill (over-gating; must stay active)")
	}

	discTrusted := resolveSkillsForTest(t, Config{
		Workspace:          ws,
		SkillsConventional: true,
		TrustProject:       true,
	})
	if !skillsContain(discTrusted, "repo-skill") {
		t.Error("trusted workspace must admit the project-tier skill")
	}
	if !skillsContain(discTrusted, "user-skill") {
		t.Error("trusted workspace must still admit the user-tier skill")
	}
}

// TestUntrustedWorkspaceWithholdsProjectCommands proves an untrusted workspace
// withholds the project-tier (workspace-relative) slash-command dirs: a "/x"
// invocation passes through UNEXPANDED (raw text → the agent runs in ask-the-human
// mode), while a trusted workspace expands it. An explicit --commands-dir is
// operator-supplied and trusted regardless (asserted last).
func TestUntrustedWorkspaceWithholdsProjectCommands(t *testing.T) {
	ws := t.TempDir()
	writeCommand(t, ws, ".mecatl/commands", "greet", "Hello from the repo command")

	expand := func(cfg Config) (string, bool) {
		exp := buildCommandExpander(cfg, nil)
		out, ok, err := exp.Expand(context.Background(), "/greet")
		if err != nil {
			t.Fatalf("expand: %v", err)
		}
		return out, ok
	}

	// Untrusted + EnableCommands (default project dirs): command WITHHELD, raw passes.
	if _, ok := expand(Config{Workspace: ws, EnableCommands: true, TrustProject: false}); ok {
		t.Error("untrusted workspace expanded a PROJECT-tier slash command (security gap)")
	}

	// Trusted + ingestion granted + EnableCommands: command expands.
	if out, ok := expand(Config{Workspace: ws, EnableCommands: true, TrustProject: true}); !ok || out != "Hello from the repo command" {
		t.Errorf("trusted workspace must expand the project command; got %q ok=%v", out, ok)
	}

	// Explicit --commands-dir is operator-supplied: trusted even when untrusted. The
	// DirCommandExpander resolves dirs workspace-relative, so the explicit dir is a
	// subdir of the workspace (an operator pointing at a path they chose).
	const explicitRel = "ops/commands"
	if err := os.MkdirAll(filepath.Join(ws, explicitRel), 0o755); err != nil {
		t.Fatalf("mkdir explicit dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(ws, explicitRel, "op.md"), []byte("operator command"), 0o644); err != nil {
		t.Fatalf("write explicit command: %v", err)
	}
	exp := buildCommandExpander(Config{Workspace: ws, CommandsDir: explicitRel, TrustProject: false}, nil)
	out, ok, err := exp.Expand(context.Background(), "/op")
	if err != nil {
		t.Fatalf("expand explicit: %v", err)
	}
	if !ok || out != "operator command" {
		t.Errorf("an explicit --commands-dir must work regardless of trust; got %q ok=%v", out, ok)
	}
}

// TestUntrustedWorkspaceStillUsable is the consolidated R2.4 "STILL USABLE"
// assertion: an untrusted workspace withholds the project authority set yet the
// agent is NOT disabled. It confirms the user-tier capability survives across all
// three surfaces in ONE untrusted build, and that the command expander degrades to
// the NoopExpander (raw text passes through — the loop still runs) rather than
// erroring or being removed.
func TestUntrustedWorkspaceStillUsable(t *testing.T) {
	ws := t.TempDir()
	xdg := t.TempDir()
	home := t.TempDir()
	fakeUserConfigEnv(t, xdg, home)

	// Project-tier (repo-injected) members.
	writeAgentDef(t, filepath.Join(ws, ".mecatl", "agents"), "proj-agent", "repo", "X")
	writeSkill(t, filepath.Join(ws, ".mecatl", "skills"), "proj-skill", "repo", "X")
	writeCommand(t, ws, ".mecatl/commands", "proj-cmd", "repo body")
	// User-tier (operator's own) members.
	writeAgentDef(t, filepath.Join(xdg, "mecatl", "agents"), "user-agent", "mine", "Y")
	writeSkill(t, filepath.Join(xdg, "mecatl", "skills"), "user-skill", "mine", "Y")

	cfg := Config{
		Workspace:          ws,
		AgentsConventional: true,
		SkillsConventional: true,
		EnableCommands:     true,
		TrustProject:       false, // untrusted
	}

	// Agents: project withheld, user active.
	reg := resolveAgentRegistry(context.Background(), cfg)
	if _, ok := reg.Get("proj-agent"); ok {
		t.Error("untrusted: project agent leaked")
	}
	if _, ok := reg.Get("user-agent"); !ok {
		t.Error("untrusted: user agent must stay active (still usable)")
	}

	// Skills: project withheld, user active.
	disc := resolveSkillsForTest(t, cfg)
	if skillsContain(disc, "proj-skill") {
		t.Error("untrusted: project skill leaked")
	}
	if !skillsContain(disc, "user-skill") {
		t.Error("untrusted: user skill must stay active (still usable)")
	}

	// Commands: project-tier dirs withheld → the expander degrades to NoopExpander,
	// which leaves raw user text UNTOUCHED so the loop still runs.
	exp := buildCommandExpander(cfg, nil)
	if _, isNoop := exp.(prompt.NoopExpander); !isNoop {
		t.Errorf("untrusted: command expander should degrade to NoopExpander, got %T", exp)
	}
	out, expanded, err := exp.Expand(context.Background(), "just a normal prompt")
	if err != nil {
		t.Fatalf("noop expand errored (loop would be broken): %v", err)
	}
	if expanded || out != "just a normal prompt" {
		t.Errorf("untrusted: raw user text must pass through untouched (ask-the-human mode); got %q expanded=%v", out, expanded)
	}
}

// skillsContain reports whether the discovered skills include the named one.
func skillsContain(disc []skills.Skill, name string) bool {
	for _, s := range disc {
		if s.Name == name {
			return true
		}
	}
	return false
}

// TestResolveSkillIndexUntrustedDropsProjectSkill pins the agent-def skill-PRELOAD
// leak path (FIX 3): resolveSkillIndex feeds a def's `skills:` preload. An untrusted
// workspace must drop the PROJECT-tier skill from the index (so it can't be preloaded
// into a specialist's prompt) while keeping the USER-tier skill; a trusted workspace
// keeps both.
func TestResolveSkillIndexUntrustedDropsProjectSkill(t *testing.T) {
	ws := t.TempDir()
	xdg := t.TempDir()
	home := t.TempDir()
	fakeUserConfigEnv(t, xdg, home)

	writeSkill(t, filepath.Join(ws, ".mecatl", "skills"), "proj", "from the repo", "PROJ BODY")
	writeSkill(t, filepath.Join(xdg, "mecatl", "skills"), "user", "my own", "USER BODY")

	// Untrusted: project skill absent from the preload index, user skill present.
	idxUntrusted := resolveSkillIndexForTest(t, Config{
		Workspace:          ws,
		SkillsConventional: true,
		TrustProject:       false,
	})
	if _, ok := idxUntrusted["proj"]; ok {
		t.Error("untrusted: a PROJECT-tier skill leaked into the agent-def preload index (security gap)")
	}
	if _, ok := idxUntrusted["user"]; !ok {
		t.Error("untrusted: the USER-tier skill must stay in the preload index (over-gating)")
	}

	// Trusted + ingestion granted: both present.
	idxTrusted := resolveSkillIndexForTest(t, Config{
		Workspace:          ws,
		SkillsConventional: true,
		TrustProject:       true,
	})
	if _, ok := idxTrusted["proj"]; !ok {
		t.Error("trusted: the project-tier skill must be available for preload")
	}
	if _, ok := idxTrusted["user"]; !ok {
		t.Error("trusted: the user-tier skill must still be available for preload")
	}
}

// TestActiveSkillDirsUntrustedExcludesProjectTier pins the SkillDraft draft-overlap
// leak path (FIX 4): activeSkillDirs feeds the draft novelty/overlap check. An
// untrusted workspace must NOT list the project-tier skill dirs (else the overlap
// check would cover trees the catalog no longer serves), but MUST list them when
// trusted.
func TestActiveSkillDirsUntrustedExcludesProjectTier(t *testing.T) {
	ws := t.TempDir()
	xdg := t.TempDir()
	home := t.TempDir()
	fakeUserConfigEnv(t, xdg, home)
	// The dirs must EXIST so osfs.ResolveRoot resolves them to their realpath (the
	// same canonical form activeSkillDirs returns), making the assertions exact.
	mkdirAll(t, filepath.Join(ws, ".mecatl", "skills"))
	mkdirAll(t, filepath.Join(ws, ".claude", "skills"))

	projMecatl := resolveRootForTest(t, filepath.Join(ws, ".mecatl", "skills"))
	projClaude := resolveRootForTest(t, filepath.Join(ws, ".claude", "skills"))

	untrusted := activeSkillDirs(Config{Workspace: ws, SkillsConventional: true, TrustProject: false})
	if dirsListContains(untrusted, projMecatl) || dirsListContains(untrusted, projClaude) {
		t.Errorf("untrusted: project-tier skill dirs must be excluded; got %v", untrusted)
	}

	trusted := activeSkillDirs(Config{Workspace: ws, SkillsConventional: true, TrustProject: true})
	if !dirsListContains(trusted, projMecatl) || !dirsListContains(trusted, projClaude) {
		t.Errorf("trusted: project-tier skill dirs (%q, %q) must be present; got %v", projMecatl, projClaude, trusted)
	}
}

// mkdirAll is a tiny test helper that creates a directory tree, failing the test on
// error.
func mkdirAll(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %q: %v", dir, err)
	}
}

// resolveRootForTest returns the canonical (abs + EvalSymlinks) form of dir, matching
// what activeSkillDirs emits via osfs.ResolveRoot.
func resolveRootForTest(t *testing.T, dir string) string {
	t.Helper()
	resolved, err := osfs.ResolveRoot(dir)
	if err != nil {
		t.Fatalf("resolve %q: %v", dir, err)
	}
	return resolved
}

// dirsListContains reports whether dirs includes target.
func dirsListContains(dirs []string, target string) bool {
	for _, d := range dirs {
		if d == target {
			return true
		}
	}
	return false
}
