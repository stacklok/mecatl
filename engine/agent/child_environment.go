package agent

import (
	"time"

	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

const inTreeEnvironmentRevision = "in-tree-v1"

func newChildSessionInEnvironment(id session.SessionID, mode session.PermissionMode, env tool.Environment, limits session.Limits, createdAt time.Time) *session.Session {
	child := session.New(id, mode, env.Ref(), limits, createdAt)
	return child
}

func newSubagentSessionInEnvironment(id session.SessionID, mode session.PermissionMode, env tool.Environment, limits session.Limits, createdAt time.Time, parent session.SessionID, parentIncarnation session.IncarnationID, call session.ToolCallID) (*session.Session, error) {
	return session.NewSubagent(id, mode, env.Ref(), limits, createdAt, parent, parentIncarnation, call)
}

func newParallelBranchSessionInEnvironment(id session.SessionID, mode session.PermissionMode, env tool.Environment, limits session.Limits, createdAt time.Time, parent session.SessionID, parentIncarnation session.IncarnationID, call session.ToolCallID, branchIndex int) (*session.Session, error) {
	return session.NewParallelBranch(id, mode, env.Ref(), limits, createdAt, parent, parentIncarnation, call, branchIndex)
}

func newTeamMemberSessionInEnvironment(id session.SessionID, mode session.PermissionMode, env tool.Environment, limits session.Limits, createdAt time.Time, teamID, member string, parent session.SessionID, parentIncarnation session.IncarnationID) (*session.Session, error) {
	return session.NewTeamMember(id, mode, env.Ref(), limits, createdAt, teamID, member, parent, parentIncarnation)
}

func rehomeSessionInEnvironment(child *session.Session, env tool.Environment) error {
	return child.Rehome(env.Ref())
}
