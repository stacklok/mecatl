package app

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
)

// TestHeadlessAutoWithoutTrustProjectSuppressesProjectAgentDefs is the
// LOAD-BEARING composition e2e test for the issue-#359 redesign. It drives the
// REAL app.Build and proves a HEADLESS --posture auto run WITHOUT --trust-project
// suppresses project-tier agent defs at the resolveAgentRegistry consumer
// (agentdefs.go), not just at the projectIngestionAdmitted() helper in isolation.
// A test that only covered the helper truth-table would stay green if the
// consumer silently reverted to cfg.TrustProject — this test fails that revert.
//
// It contrasts a PINNED build (Headless: true, Posture: auto, NO --trust-project)
// against a CONTROL build (same + TrustProject: true) over the SAME seeded
// workspace:
//  1. Seeds .claude/agents/scout.md (a minimal valid agent def with YAML
//     frontmatter).
//  2. PINNED: asserts scout is ABSENT from ListAgents AND the WITHHELD WARN
//     fired in diagnostics (the fail-safe default).
//  3. CONTROL: asserts scout IS present in ListAgents — this proves the def is
//     discoverable when ingested, so the pinned absence is caused by the
//     withheld ingestion grant, not a broken fixture.
func TestHeadlessAutoWithoutTrustProjectSuppressesProjectAgentDefs(t *testing.T) {
	t.Run("pinned absent", func(t *testing.T) {
		ws := seedProjectAgentDef(t, "scout")
		diag := slogdiagBuffer(t)

		built, err := Build(context.Background(), Config{
			Workspace:          ws,
			Model:              "mock",
			UseMock:            true,
			NoSoul:             true,
			AgentsConventional: true,
			Posture:            PostureAuto,
			Headless:           true, // a headless root does NOT grant ingestion via the ladder
			// TrustProject stays FALSE — the operator did not pass --trust-project.
			Diagnostics: diag.diag,
		})
		if err != nil {
			t.Fatalf("Build: %v", err)
		}
		defer built.Close()

		ensureAbsent(t, built.Service, "scout")

		// The WITHHELD WARN text as emitted at agentdefs.go.
		if diag.lineContaining("project-tier defs WITHHELD") == "" {
			t.Fatalf("expected WITHHELD WARN in diagnostics; log:\n%s", diag.String())
		}
	})

	t.Run("control present", func(t *testing.T) {
		// Re-seed the SAME workspace shape so a control build discovers the
		// def — this proves the pinned assertion is non-vacuous.
		ws := seedProjectAgentDef(t, "scout")

		built, err := Build(context.Background(), Config{
			Workspace:          ws,
			Model:              "mock",
			UseMock:            true,
			NoSoul:             true,
			AgentsConventional: true,
			Posture:            PostureAuto,
			Headless:           true,
			TrustProject:       true, // the explicit opt-in admits ingestion on the headless root
			Diagnostics:        nil,  // port.NopDiagnostics via nil
		})
		if err != nil {
			t.Fatalf("Build: %v", err)
		}
		defer built.Close()

		ensurePresent(t, built.Service, "scout")
	})
}

// seedProjectAgentDef creates a workspace under t.TempDir(), writes a minimal
// valid .claude/agents/<name>.md agent-definition file, and returns the
// workspace path.
func seedProjectAgentDef(t *testing.T, name string) string {
	t.Helper()
	ws := t.TempDir()
	dir := filepath.Join(ws, ".claude", "agents")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir .claude/agents: %v", err)
	}
	// Minimal valid agent def: YAML frontmatter with the two required fields
	// (name + description), followed by a markdown body.
	def := "---\nname: " + name + "\ndescription: quickly explores a codebase\n---\nAn expert scout that maps the repository structure.\n"
	path := filepath.Join(dir, name+".md")
	if err := os.WriteFile(path, []byte(def), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return ws
}

// ensurePresent fails if no agent named want is in the ListAgents snapshot.
func ensurePresent(t *testing.T, svc interface {
	ListAgents(context.Context) []*mecatlv1.AgentInfo
}, want string) {
	t.Helper()
	for _, a := range svc.ListAgents(context.Background()) {
		if a.GetName() == want {
			return
		}
	}
	t.Fatalf("agent %q not found in ListAgents — fixture is broken or ingestion was withheld", want)
}

// ensureAbsent fails if an agent named want IS in the ListAgents snapshot.
func ensureAbsent(t *testing.T, svc interface {
	ListAgents(context.Context) []*mecatlv1.AgentInfo
}, want string) {
	t.Helper()
	for _, a := range svc.ListAgents(context.Background()) {
		if a.GetName() == want {
			t.Fatalf("agent %q found in ListAgents but should be ABSENT (headless auto without --trust-project withholds project ingestion)", want)
		}
	}
}
