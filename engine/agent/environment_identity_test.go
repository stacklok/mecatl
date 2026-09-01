package agent

import (
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

func TestChildSessionsUseEnvironmentIdentity(t *testing.T) {
	t.Parallel()

	ref := session.EnvironmentRef{Kind: "remote", ID: "opaque-child", Revision: "inventory-v3"}
	env := tool.MustEnvironment(ref, memfs.NewWorkspace("/private/physical/root"), nil)
	parent := session.SessionID("parent")
	parentIncarnation := session.NewIncarnationID()
	createdAt := time.Unix(1, 0)

	subagent, err := newSubagentSessionInEnvironment("subagent-1", session.ModeDefault, env, session.Limits{}, createdAt, parent, parentIncarnation, "call-1")
	if err != nil {
		t.Fatalf("newSubagentSessionInEnvironment: %v", err)
	}
	parallel, err := newParallelBranchSessionInEnvironment("parallel-1", session.ModeDefault, env, session.Limits{}, createdAt, parent, parentIncarnation, "call-1", 0)
	if err != nil {
		t.Fatalf("newParallelBranchSessionInEnvironment: %v", err)
	}
	member, err := newTeamMemberSessionInEnvironment("team-1-member", session.ModeDefault, env, session.Limits{}, createdAt, "team-1", "member", parent, parentIncarnation)
	if err != nil {
		t.Fatalf("newTeamMemberSessionInEnvironment: %v", err)
	}

	for name, child := range map[string]*session.Session{
		"subagent": subagent,
		"parallel": parallel,
		"member":   member,
	} {
		if child.EnvironmentRef != env.Ref() {
			t.Errorf("%s EnvironmentRef = %+v, want environment ref %+v", name, child.EnvironmentRef, env.Ref())
		}
		if child.Workspace != env.Workspace().Root() {
			t.Errorf("%s Workspace = %q, want transitional root %q", name, child.Workspace, env.Workspace().Root())
		}
	}
}

func TestRehomeSessionUsesEnvironmentIdentity(t *testing.T) {
	t.Parallel()

	child := session.New("child", session.ModeDefault, "/old", session.Limits{}, time.Unix(1, 0))
	child.EnvironmentRef = session.EnvironmentRef{Kind: "remote", ID: "opaque", Revision: "v1"}
	env := tool.MustEnvironment(
		session.EnvironmentRef{Kind: "remote", ID: "opaque", Revision: "v2"},
		memfs.NewWorkspace("/new/private/root"), nil,
	)

	if err := rehomeSessionInEnvironment(child, env); err != nil {
		t.Fatalf("rehomeSessionInEnvironment: %v", err)
	}
	if child.EnvironmentRef != env.Ref() {
		t.Fatalf("EnvironmentRef = %+v, want %+v", child.EnvironmentRef, env.Ref())
	}
	if child.Workspace != env.Workspace().Root() {
		t.Fatalf("Workspace = %q, want %q", child.Workspace, env.Workspace().Root())
	}
}
