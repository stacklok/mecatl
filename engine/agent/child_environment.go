package agent

import (
	"time"

	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

func newChildSessionInEnvironment(id session.SessionID, mode session.PermissionMode, env tool.Environment, limits session.Limits, createdAt time.Time) *session.Session {
	child := session.New(id, mode, env.Workspace().Root(), limits, createdAt)
	child.EnvironmentRef = env.Ref()
	return child
}

func newSubagentSessionInEnvironment(id session.SessionID, mode session.PermissionMode, env tool.Environment, limits session.Limits, createdAt time.Time, parent session.SessionID, parentIncarnation session.IncarnationID, call session.ToolCallID) (*session.Session, error) {
	child, err := session.NewSubagent(id, mode, env.Workspace().Root(), limits, createdAt, parent, parentIncarnation, call)
	if err != nil {
		return nil, err
	}
	child.EnvironmentRef = env.Ref()
	return child, nil
}

func newParallelBranchSessionInEnvironment(id session.SessionID, mode session.PermissionMode, env tool.Environment, limits session.Limits, createdAt time.Time, parent session.SessionID, parentIncarnation session.IncarnationID, call session.ToolCallID, branchIndex int) (*session.Session, error) {
	child, err := session.NewParallelBranch(id, mode, env.Workspace().Root(), limits, createdAt, parent, parentIncarnation, call, branchIndex)
	if err != nil {
		return nil, err
	}
	child.EnvironmentRef = env.Ref()
	return child, nil
}

func newTeamMemberSessionInEnvironment(id session.SessionID, mode session.PermissionMode, env tool.Environment, limits session.Limits, createdAt time.Time, teamID, member string, parent session.SessionID, parentIncarnation session.IncarnationID) (*session.Session, error) {
	child, err := session.NewTeamMember(id, mode, env.Workspace().Root(), limits, createdAt, teamID, member, parent, parentIncarnation)
	if err != nil {
		return nil, err
	}
	child.EnvironmentRef = env.Ref()
	return child, nil
}

func rehomeSessionInEnvironment(child *session.Session, env tool.Environment) error {
	if err := child.Rehome(env.Workspace().Root()); err != nil {
		return err
	}
	child.EnvironmentRef = env.Ref()
	return nil
}
