package agent

import (
	"context"
	"testing"

	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/team"
	"github.com/stacklok/mecatl/engine/tool"
)

func TestWithRootAuthorityStampsOnlyDirectTeamMembers(t *testing.T) {
	root := session.Authority{
		CapabilitySet: governance.CapabilitySet{Tools: []string{"Read"}, RemainingDelegationDepth: 1, FileSystem: true},
		Provenance:    "composed_root",
	}
	makeSupervisor := func(options ...SupervisorOption) *Supervisor {
		tm := team.New("direct")
		factory := func(_ MemberSpec, _ string) MemberBuild {
			return MemberBuild{Engine: NewEngine(Deps{Catalog: tool.NewCatalog()})}
		}
		return NewSupervisor(tm, memEnv("/workspace"), factory, options...)
	}

	t.Run("direct team", func(t *testing.T) {
		sup := makeSupervisor(WithRootAuthority(root))
		if err := sup.AddMember(context.Background(), MemberSpec{Name: "worker"}); err != nil {
			t.Fatalf("AddMember: %v", err)
		}
		got, bound := sup.members["worker"].sess.BoundAuthority()
		if !bound || !got.CapabilitySet.Contains(root.CapabilitySet) || !root.CapabilitySet.Contains(got.CapabilitySet) {
			t.Fatalf("member authority = %+v bound=%t, want root %+v", got, bound, root)
		}
	})

	t.Run("parent team remains for child derivation", func(t *testing.T) {
		sup := makeSupervisor(WithRootAuthority(root), withParentCaps(parentCaps{parentSessionID: "parent"}))
		if err := sup.AddMember(context.Background(), MemberSpec{Name: "worker"}); err != nil {
			t.Fatalf("AddMember: %v", err)
		}
		if _, bound := sup.members["worker"].sess.BoundAuthority(); bound {
			t.Fatal("parent-driven team member received a root authority instead of later child derivation")
		}
	})
}
