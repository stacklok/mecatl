package app

import (
	"path/filepath"
	"strings"
	"testing"

	agents "github.com/stacklok/mecatl/engine/adapter/agentfs"
)

// TestSessionScopedAgentIdentity_Scenario2_ProjectMemoryScopedToOwnPlacement
// pins AC2.5: `memory: project` for an agent-bound session resolves against
// THIS session's own bound placement root, never the deployment's single
// process-wide workspace — two agent-bound sessions on the SAME def but
// DIFFERENT placements (e.g. one forked onto an alternate worktree per ADR
// 0291) must never share or leak the same project-memory file, and a
// placement the operator never specifically vetted must not read project
// memory there merely because the deployment's global trust flag happens to
// be true (the root-aware projectIngestionAdmittedForRoot gate, not the bare
// global-flag projectIngestionAdmitted).
func TestSessionScopedAgentIdentity_Scenario2_ProjectMemoryScopedToOwnPlacement(t *testing.T) {
	def := agents.AgentDef{Name: "release-reviewer", Description: "d", Memory: "project"}

	t.Run("two placements on the same def resolve distinct memory files", func(t *testing.T) {
		rootA := t.TempDir()
		rootB := t.TempDir()
		writeAgentMemory(t, filepath.Join(rootA, ".mecatl"), "release-reviewer", "ROOT-A-MEMORY\n")
		writeAgentMemory(t, filepath.Join(rootB, ".mecatl"), "release-reviewer", "ROOT-B-MEMORY\n")

		// Each session's own placement root is its OWN vetted default — the
		// realistic shape of two independently-vetted agent-bound sessions (e.g.
		// two different deployments, or one forked onto an alternate worktree
		// whose root the operator separately trusts).
		headA, okA := resolveAgentMemoryHead(Config{Workspace: rootA, TrustProject: true}, def, rootA)
		if !okA || !strings.Contains(headA, "ROOT-A-MEMORY") {
			t.Fatalf("session bound to rootA: ok=%v head=%q, want ROOT-A-MEMORY", okA, headA)
		}
		if strings.Contains(headA, "ROOT-B-MEMORY") {
			t.Fatalf("session bound to rootA leaked rootB's memory: %q", headA)
		}

		headB, okB := resolveAgentMemoryHead(Config{Workspace: rootB, TrustProject: true}, def, rootB)
		if !okB || !strings.Contains(headB, "ROOT-B-MEMORY") {
			t.Fatalf("session bound to rootB: ok=%v head=%q, want ROOT-B-MEMORY", okB, headB)
		}
		if strings.Contains(headB, "ROOT-A-MEMORY") {
			t.Fatalf("session bound to rootB leaked rootA's memory: %q", headB)
		}
	})

	t.Run("a placement other than cfg.Workspace is never admitted despite global trust", func(t *testing.T) {
		vetted := t.TempDir()
		unvetted := t.TempDir()
		writeAgentMemory(t, filepath.Join(vetted, ".mecatl"), "release-reviewer", "VETTED-DEFAULT-MEMORY\n")
		writeAgentMemory(t, filepath.Join(unvetted, ".mecatl"), "release-reviewer", "UNVETTED-MEMORY\n")

		// cfg.Workspace (the operator's one specifically-vetted root) is "vetted",
		// but THIS session is bound to a DIFFERENT placement ("unvetted") — e.g. a
		// fork onto a worktree the operator never separately reviewed. Even though
		// cfg.TrustProject is globally true, projectIngestionAdmittedForRoot must
		// reject a root that isn't cfg.Workspace: the global flag must not vouch
		// for an arbitrary placement, and the bare cfg.Workspace must never be
		// substituted for the session's own bound root either (that would silently
		// serve the WRONG session's memory file instead of correctly refusing).
		head, ok := resolveAgentMemoryHead(Config{Workspace: vetted, TrustProject: true}, def, unvetted)
		if ok || head != "" {
			t.Fatalf("an unvetted placement must not resolve project memory (neither its own file nor cfg.Workspace's), got ok=%v head=%q", ok, head)
		}
	})
}

// TestSessionScopedAgentIdentity_Scenario2_ProjectMemoryInertUnderNoFS pins
// AC2.6: under profile: "no-fs" there is no workspace to bind a project-memory
// file to, so an agent-bound session's memory: project resolution is a silent
// no-op (ok=false, no error/panic) — never an error, regardless of the
// deployment's cfg.Workspace/TrustProject.
func TestSessionScopedAgentIdentity_Scenario2_ProjectMemoryInertUnderNoFS(t *testing.T) {
	ws := t.TempDir()
	writeAgentMemory(t, filepath.Join(ws, ".mecatl"), "release-reviewer", "SHOULD-NOT-BE-READ\n")
	def := agents.AgentDef{Name: "release-reviewer", Description: "d", Memory: "project"}

	// cfg.Workspace/TrustProject reflect a normal trusted deployment default, but
	// THIS call's sessionRoot is "" — the no-fs shape (no placement to bind to).
	// The function must key off sessionRoot, not cfg.Workspace: resolving via
	// cfg.Workspace here would be exactly the leak this task closes.
	head, ok := resolveAgentMemoryHead(Config{Workspace: ws, TrustProject: true}, def, "")
	if ok || head != "" {
		t.Fatalf("profile: no-fs (empty sessionRoot) must be inert, got ok=%v head=%q", ok, head)
	}
}
