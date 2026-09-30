package session

import (
	"reflect"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/governance"
)

// TestSessionScopedAgentIdentity_Scenario3_AuthorityCeilingBoundOnce pins AC3.1:
// an agent-bound session's bound Authority carries a write-once Ceiling set
// once at bind time (BindAuthority/RestoreLabels), never re-derived; an
// ordinary (non-agent-bound) session's Authority carries no Ceiling.
func TestSessionScopedAgentIdentity_Scenario3_AuthorityCeilingBoundOnce(t *testing.T) {
	t.Run("bind time sets the ceiling once", func(t *testing.T) {
		s := New("ceiling-bind", ModeDefault, EnvironmentRef{Kind: EnvKindLocal, ID: ".", Revision: "in-tree-v1"}, Limits{}, time.Unix(1, 0).UTC())
		ceiling := &governance.CapabilitySet{Tools: []string{"Read", "Edit"}}
		if err := s.BindAuthority(Authority{
			CapabilitySet:      governance.CapabilitySet{Tools: []string{"Read"}},
			Provenance:         "agent-def:test",
			DefinitionIdentity: "agent:test",
			Ceiling:            ceiling,
		}); err != nil {
			t.Fatalf("BindAuthority: %v", err)
		}
		got, bound := s.BoundAuthority()
		if !bound {
			t.Fatal("authority not bound")
		}
		if got.Ceiling == nil || !reflect.DeepEqual(got.Ceiling.Tools, []string{"Read", "Edit"}) {
			t.Fatalf("Ceiling = %+v, want {Tools: [Read Edit]}", got.Ceiling)
		}

		// The returned Ceiling is an independent copy: mutating it must not
		// reach back into the aggregate (write-once, never re-derived).
		got.Ceiling.Tools[0] = "mutated"
		again, _ := s.BoundAuthority()
		if again.Ceiling.Tools[0] != "Read" {
			t.Fatalf("mutating a returned Ceiling changed the aggregate: %+v", again.Ceiling)
		}

		// A second bind attempt (write-once) must be rejected and must not
		// replace the already-bound Ceiling.
		if err := s.BindAuthority(Authority{
			CapabilitySet: governance.CapabilitySet{Tools: []string{"Write"}},
			Provenance:    "agent-def:other",
			Ceiling:       &governance.CapabilitySet{Tools: []string{"Write"}},
		}); err == nil {
			t.Fatal("second BindAuthority accepted, ceiling is not write-once")
		}
		final, _ := s.BoundAuthority()
		if !reflect.DeepEqual(final.Ceiling.Tools, []string{"Read", "Edit"}) {
			t.Fatalf("Ceiling re-derived by rejected rebind: %+v", final.Ceiling)
		}
	})

	t.Run("restore labels round-trips the ceiling through bind authority", func(t *testing.T) {
		s := New("ceiling-restore", ModeDefault, EnvironmentRef{Kind: EnvKindLocal, ID: ".", Revision: "in-tree-v1"}, Limits{}, time.Unix(1, 0).UTC())
		owner := &Principal{Issuer: "issuer", Subject: "subject", GrantType: GrantTypeUser}
		ceiling := &governance.CapabilitySet{Tools: []string{"Read"}}
		if err := s.RestoreLabels(owner, Authority{
			CapabilitySet:      governance.CapabilitySet{Tools: []string{"Read"}},
			Provenance:         "agent-def:test",
			DefinitionIdentity: "agent:test",
			Ceiling:            ceiling,
		}); err != nil {
			t.Fatalf("RestoreLabels: %v", err)
		}
		got, bound := s.BoundAuthority()
		if !bound {
			t.Fatal("authority not bound after RestoreLabels")
		}
		if got.Ceiling == nil || !reflect.DeepEqual(got.Ceiling.Tools, []string{"Read"}) {
			t.Fatalf("Ceiling = %+v after RestoreLabels, want {Tools: [Read]}", got.Ceiling)
		}
	})

	t.Run("ordinary session carries no ceiling", func(t *testing.T) {
		s := New("no-ceiling", ModeDefault, EnvironmentRef{Kind: EnvKindLocal, ID: ".", Revision: "in-tree-v1"}, Limits{}, time.Unix(1, 0).UTC())
		if err := s.BindAuthority(Authority{
			CapabilitySet: governance.CapabilitySet{Tools: []string{"Read"}},
			Provenance:    "configured:test",
		}); err != nil {
			t.Fatalf("BindAuthority: %v", err)
		}
		got, bound := s.BoundAuthority()
		if !bound {
			t.Fatal("authority not bound")
		}
		if got.Ceiling != nil {
			t.Fatalf("Ceiling = %+v, want nil for an ordinary session", got.Ceiling)
		}
	})

	t.Run("rejects a bind whose CapabilitySet already exceeds its own Ceiling", func(t *testing.T) {
		// Hardening beyond the original AC3.1/AC3.2 scope: GrantToolAuthority and
		// CompleteWorkspaceEnrollment reject a POST-bind widening past Ceiling, but
		// nothing previously stopped BindAuthority itself from accepting an
		// already-inconsistent payload — leaving an agent-bound session outside
		// its own stated non-widenable ceiling from the moment it's created, with
		// neither guard able to repair it after the fact. BindAuthority now
		// refuses that payload outright (mutation-verified: this subtest failed
		// with a nil error before the Ceiling.Contains check was added).
		s := New("ceiling-inconsistent", ModeDefault, EnvironmentRef{Kind: EnvKindLocal, ID: ".", Revision: "in-tree-v1"}, Limits{}, time.Unix(1, 0).UTC())
		err := s.BindAuthority(Authority{
			CapabilitySet:      governance.CapabilitySet{Tools: []string{"Read", "Shell"}},
			Provenance:         "agent-def:test",
			DefinitionIdentity: "agent:test",
			Ceiling:            &governance.CapabilitySet{Tools: []string{"Read"}},
		})
		if err == nil {
			t.Fatal("BindAuthority accepted a CapabilitySet exceeding its own Ceiling")
		}
		if _, bound := s.BoundAuthority(); bound {
			t.Fatal("session left in a bound state after a rejected BindAuthority call")
		}
	})
}

func ceilingBoundSession(t *testing.T, ceilingTools, boundTools []string) *Session {
	t.Helper()
	s := New("ceiling-session", ModeDefault, EnvironmentRef{Kind: EnvKindLocal, ID: ".", Revision: "in-tree-v1"}, Limits{}, time.Unix(1, 0).UTC())
	s.Owner = &Principal{Issuer: "issuer", Subject: "owner", GrantType: GrantTypeUser}
	if err := s.BindAuthority(Authority{
		CapabilitySet: governance.CapabilitySet{
			Tools:                    append([]string(nil), boundTools...),
			RemainingDelegationDepth: 2,
			FileSystem:               true,
			DirectWrite:              true,
		},
		Provenance:         "agent-def:test",
		DefinitionIdentity: "agent:test",
		Ceiling:            &governance.CapabilitySet{Tools: append([]string(nil), ceilingTools...)},
	}); err != nil {
		t.Fatalf("BindAuthority: %v", err)
	}
	return s
}

// TestSessionScopedAgentIdentity_Scenario3_GrantToolAuthorityRespectsCeiling
// pins AC3.2's GrantToolAuthority half: a grant naming a direct-MCP tool
// outside an agent-bound session's Ceiling must fail without widening
// CapabilitySet.Tools.
func TestSessionScopedAgentIdentity_Scenario3_GrantToolAuthorityRespectsCeiling(t *testing.T) {
	t.Run("grant within ceiling succeeds", func(t *testing.T) {
		s := ceilingBoundSession(t, []string{"Read", "mcp__allowed__tool"}, []string{"Read"})
		if err := s.GrantToolAuthority([]string{"mcp__allowed__tool"}); err != nil {
			t.Fatalf("GrantToolAuthority within ceiling: %v", err)
		}
		got, _ := s.BoundAuthority()
		if !got.CapabilitySet.AllowsTool("mcp__allowed__tool") {
			t.Fatal("in-ceiling tool was not granted")
		}
	})

	t.Run("grant outside ceiling is rejected and does not widen tools", func(t *testing.T) {
		s := ceilingBoundSession(t, []string{"Read"}, []string{"Read"})
		before, _ := s.BoundAuthority()
		if err := s.GrantToolAuthority([]string{"mcp__outside__tool"}); err == nil {
			t.Fatal("GrantToolAuthority accepted a tool outside the ceiling")
		}
		after, _ := s.BoundAuthority()
		if !reflect.DeepEqual(after, before) {
			t.Fatalf("rejected grant mutated authority: before=%+v after=%+v", before, after)
		}
		if after.CapabilitySet.AllowsTool("mcp__outside__tool") {
			t.Fatal("outside-ceiling tool leaked into CapabilitySet.Tools")
		}
	})

	t.Run("mixed batch containing an outside-ceiling tool is rejected atomically", func(t *testing.T) {
		s := ceilingBoundSession(t, []string{"Read", "mcp__allowed__tool"}, []string{"Read"})
		before, _ := s.BoundAuthority()
		if err := s.GrantToolAuthority([]string{"mcp__allowed__tool", "mcp__outside__tool"}); err == nil {
			t.Fatal("GrantToolAuthority accepted a mixed batch with an outside-ceiling tool")
		}
		after, _ := s.BoundAuthority()
		if !reflect.DeepEqual(after, before) {
			t.Fatalf("rejected mixed batch mutated authority: before=%+v after=%+v", before, after)
		}
	})

	t.Run("no ceiling remains unrestricted", func(t *testing.T) {
		s := grantAuthoritySession(t)
		if err := s.GrantToolAuthority([]string{"anything__unbounded"}); err != nil {
			t.Fatalf("GrantToolAuthority without a ceiling: %v", err)
		}
	})
}

// TestSessionScopedAgentIdentity_Scenario3_CompleteWorkspaceEnrollmentRespectsCeiling
// pins AC3.2's CompleteWorkspaceEnrollment half: a completion computing a
// broker tool outside an agent-bound session's Ceiling must fail without
// widening CapabilitySet.Tools, regardless of the method's internal
// ledger/delta mechanics (out of this task's scope).
func TestSessionScopedAgentIdentity_Scenario3_CompleteWorkspaceEnrollmentRespectsCeiling(t *testing.T) {
	t.Run("completion within ceiling succeeds", func(t *testing.T) {
		s := ceilingBoundSession(t, []string{"Read", "mcp__broker__tool"}, []string{"Read"})
		pending := testWorkspaceEnrollment()
		if err := s.BeginWorkspaceEnrollment(pending); err != nil {
			t.Fatalf("BeginWorkspaceEnrollment: %v", err)
		}
		if err := s.CompleteWorkspaceEnrollment(pending, []string{"Read", "mcp__broker__tool"}); err != nil {
			t.Fatalf("CompleteWorkspaceEnrollment within ceiling: %v", err)
		}
		got, _ := s.BoundAuthority()
		if !got.CapabilitySet.AllowsTool("mcp__broker__tool") {
			t.Fatal("in-ceiling broker tool was not granted")
		}
	})

	t.Run("completion outside ceiling is rejected and does not widen tools", func(t *testing.T) {
		s := ceilingBoundSession(t, []string{"Read"}, []string{"Read"})
		pending := testWorkspaceEnrollment()
		if err := s.BeginWorkspaceEnrollment(pending); err != nil {
			t.Fatalf("BeginWorkspaceEnrollment: %v", err)
		}
		before, _ := s.BoundAuthority()
		if err := s.CompleteWorkspaceEnrollment(pending, []string{"Read", "mcp__broker__outside"}); err == nil {
			t.Fatal("CompleteWorkspaceEnrollment accepted a broker tool outside the ceiling")
		}
		after, _ := s.BoundAuthority()
		if !reflect.DeepEqual(after, before) {
			t.Fatalf("rejected completion mutated authority: before=%+v after=%+v", before, after)
		}
		if after.CapabilitySet.AllowsTool("mcp__broker__outside") {
			t.Fatal("outside-ceiling broker tool leaked into CapabilitySet.Tools")
		}
		if _, ok := s.PendingWorkspaceEnrollment(); !ok {
			t.Fatal("rejected completion cleared the pending enrollment")
		}
	})

	t.Run("no ceiling remains unrestricted", func(t *testing.T) {
		s, pending := sessionWithEnrollmentAuthority(t)
		if err := s.CompleteWorkspaceEnrollment(pending, []string{"anything__unbounded"}); err != nil {
			t.Fatalf("CompleteWorkspaceEnrollment without a ceiling: %v", err)
		}
	})
}
