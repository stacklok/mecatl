package app

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/agents"
	"github.com/stacklok/mecatl/internal/adapter/osfs"
)

// skillreadroots_test.go covers the composition half of the activated-skill
// read-root carve-out: skillReadRoots derives the per-skill allowed dirs from
// resolveSkills' output (so the workspace-trust project-tier gate is inherited
// by construction), and the ONE computed list reaches both the per-session
// workspace factory and the fork-workspace constructor every delegation family
// shares.

// TestSkillReadRootsTrustGated proves the allowlist is trust-gated BY
// CONSTRUCTION: an untrusted workspace's project-tier skill dir never enters the
// computed roots (its skill is never discovered), while the user-tier skill dir
// always does; a trusted workspace admits both. It also pins the PER-SKILL
// granularity — the source dir itself (whose random siblings must never become
// readable) is not a root, only the individual skill dirs are.
func TestSkillReadRootsTrustGated(t *testing.T) {
	ws := t.TempDir()
	xdg := t.TempDir()
	home := t.TempDir()
	fakeUserConfigEnv(t, xdg, home)

	writeSkill(t, filepath.Join(ws, ".claude", "skills"), "proj-skill", "from the repo", "REPO BODY")
	writeSkill(t, filepath.Join(xdg, "mecatl", "skills"), "user-skill", "my own", "USER BODY")

	projDir := resolveRootForTest(t, filepath.Join(ws, ".claude", "skills", "proj-skill"))
	userDir := resolveRootForTest(t, filepath.Join(xdg, "mecatl", "skills", "user-skill"))
	userSourceDir := resolveRootForTest(t, filepath.Join(xdg, "mecatl", "skills"))

	untrusted := assetDirsForTest(t, Config{
		Workspace:          ws,
		SkillsConventional: true,
		TrustProject:       false,
	})
	if dirsListContains(untrusted, projDir) {
		t.Errorf("untrusted: the project-tier skill dir must be ABSENT from the read roots; got %v", untrusted)
	}
	if !dirsListContains(untrusted, userDir) {
		t.Errorf("untrusted: the user-tier skill dir must be present; got %v", untrusted)
	}

	// The SkillDraft QUARANTINE dir can never enter the allowlist: it is never a
	// skills Source, so even a planted SKILL.md inside it is never discovered.
	draftDir := filepath.Join(t.TempDir(), "drafts")
	writeSkill(t, draftDir, "sneaky-draft", "model-authored", "DRAFT BODY")

	trusted := assetDirsForTest(t, Config{
		Workspace:          ws,
		SkillsConventional: true,
		TrustProject:       true,
		SkillsDraftDir:     draftDir,
	})
	if !dirsListContains(trusted, projDir) || !dirsListContains(trusted, userDir) {
		t.Errorf("trusted: both skill dirs (%q, %q) must be present; got %v", projDir, userDir, trusted)
	}
	// PER-SKILL dirs only — never the whole source dir.
	if dirsListContains(trusted, userSourceDir) {
		t.Errorf("the skills SOURCE dir %q must never be a read root (per-skill granularity); got %v", userSourceDir, trusted)
	}
	if dirsListContains(trusted, resolveRootForTest(t, filepath.Join(draftDir, "sneaky-draft"))) ||
		dirsListContains(trusted, resolveRootForTest(t, draftDir)) {
		t.Errorf("the SkillDraft quarantine dir must never enter the read roots; got %v", trusted)
	}
}

// TestSkillReadRootsResolveSymlinkAlias is the composition twin of the Skill
// tool's alias test: the seam's read roots (FSSource.AssetDirs) must emit the
// RESOLVED (osfs.ResolveRoot) per-skill dir, never the raw symlink-alias form
// of the discovery path — the exact divergence a symlinked path prefix
// (/home → /var/home) produces, which t.TempDir alone cannot manufacture on a
// canonical-path host. A raw filepath.Dir/Clean root would never match the
// canonical key the osfs allowlist stores, silently breaking absolute reads
// for symlinked homes.
func TestSkillReadRootsResolveSymlinkAlias(t *testing.T) {
	realDir := t.TempDir()
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(realDir, alias); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	// The skill's discovery path crosses the alias, as it would when a
	// conventional skills location lives behind a symlinked prefix: the alias
	// dir is the EXPLICIT skills dir the seam resolves.
	writeSkill(t, alias, "aliased", "via alias", "BODY")

	resolved := resolveRootForTest(t, filepath.Join(realDir, "aliased"))
	rawDir := filepath.Clean(filepath.Join(alias, "aliased"))
	if rawDir == resolved {
		t.Fatalf("test setup did not produce a divergent alias: raw %q == resolved %q", rawDir, resolved)
	}

	roots := assetDirsForTest(t, Config{SkillsDirs: []string{alias}})
	if !dirsListContains(roots, resolved) {
		t.Errorf("roots must contain the RESOLVED dir %q; got %v", resolved, roots)
	}
	if dirsListContains(roots, rawDir) {
		t.Errorf("roots must not carry the raw symlink-alias dir %q; got %v", rawDir, roots)
	}
}

// TestSkillReadRootsWiring proves the computed roots actually reach the two
// workspace constructor families: the per-session server workspace factory
// (osfsWorkspaceFactory) AND the shared fork-workspace constructor every
// delegation forker uses (newForkWorkspace — Subagent worktree, team members,
// Parallel branches), so an isolated child can read activated-skill files by
// absolute path exactly like the main session.
func TestSkillReadRootsWiring(t *testing.T) {
	skillDir := filepath.Join(t.TempDir(), "skills", "demo")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatalf("mkdir skill dir: %v", err)
	}
	skillFile := filepath.Join(skillDir, "SKILL.md")
	if err := os.WriteFile(skillFile, []byte("BODY"), 0o644); err != nil {
		t.Fatalf("write skill file: %v", err)
	}
	absFile := filepath.Join(resolveRootForTest(t, skillDir), "SKILL.md")
	roots := []string{skillDir}
	ctx := context.Background()

	// The per-session factory (server.Config.Workspaces).
	factory := osfsWorkspaceFactory(Config{}.diag(), roots)
	mainWS := factory(t.TempDir())
	if mainWS == nil {
		t.Fatal("workspace factory returned nil")
	}
	if got, err := mainWS.Read(ctx, absFile); err != nil || string(got) != "BODY" {
		t.Errorf("factory workspace Read(skill abs path) = %q, %v; want BODY", got, err)
	}

	// The fork constructor (worktree-shaped: a fresh root elsewhere on disk).
	forkWS, err := newForkWorkspace(roots)(t.TempDir())
	if err != nil {
		t.Fatalf("newForkWorkspace: %v", err)
	}
	if got, err := forkWS.Read(ctx, absFile); err != nil || string(got) != "BODY" {
		t.Errorf("forked workspace Read(skill abs path) = %q, %v; want BODY", got, err)
	}
}

// TestSkillReadRootsThreadedThroughTeamWiring pins the CALL-SITE threading, not
// just the shared helper: buildTeamWiring's two real forkers (fk force-copy for
// mutating members, roFk worktree-default for read-only members) must produce
// fork workspaces that honor the skill read roots — a refactor reverting either
// constructor to an inline rootless closure breaks this test, where the
// helper-level TestSkillReadRootsWiring alone would stay green. The base is a
// plain (non-git) dir, so both forkers take the offline recursive-copy path.
//
// RESIDUAL (documented, not silently assumed): the OTHER two newForkWorkspace
// call sites — buildSubagentTool's worktree forker and registerParallelTool's
// branch forker — are constructed behind their tools' options and are only
// reachable by driving a child run, so their threading is pinned by shared
// construction (the same one-line newForkWorkspace(a.skillReadRoots) idiom this
// test locks for the team site), not by a per-site fork here.
func TestSkillReadRootsThreadedThroughTeamWiring(t *testing.T) {
	skillDir := filepath.Join(t.TempDir(), "skills", "demo")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatalf("mkdir skill dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("BODY"), 0o644); err != nil {
		t.Fatalf("write skill file: %v", err)
	}
	absFile := filepath.Join(resolveRootForTest(t, skillDir), "SKILL.md")
	ctx := context.Background()

	provider := mockllm.New(mockllm.TextTurn("ok"))
	cfg := Config{Model: "m"}
	_, fk, roFk, _, _ := buildTeamWiring(ctx, cfg, regForTest(provider, providerMock, cfg.Model),
		provider, providerMock, cfg.Model, nil, agents.NewRegistry(nil), []string{skillDir}, nil, catalogAssets{}, false)

	base, err := osfs.NewWorkspace(t.TempDir())
	if err != nil {
		t.Fatalf("open base workspace: %v", err)
	}
	for _, tc := range []struct {
		name string
		fk   tool.WorkspaceForker
	}{
		{"mutating force-copy forker", fk},
		{"read-only forker", roFk},
	} {
		child, cleanup, _, err := tc.fk.Fork(ctx, base, "readroots-pin")
		if err != nil {
			t.Fatalf("%s: Fork: %v", tc.name, err)
		}
		t.Cleanup(func() { _ = cleanup() })
		if got, err := child.Read(ctx, absFile); err != nil || string(got) != "BODY" {
			t.Errorf("%s: forked member Read(skill abs path) = %q, %v; want BODY (roots not threaded at the call site)", tc.name, got, err)
		}
	}
}
