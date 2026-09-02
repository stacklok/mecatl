// Package agent — export_env_test.go bridges test helpers from the internal
// agent test files to the external agent_test package (issue #462). The helpers
// below re-export the Environment-building helpers so the external e2e tests can
// wrap a memfs workspace without duplicating the ref/panic dance. This file is
// TEST-ONLY and lives in package agent so it can see the unexported helpers.

package agent

import (
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

// EnvForWS wraps a Workspace into a tool.Environment with an optional bound
// runner (exported for the external agent_test package).
func EnvForWS(ws tool.Workspace, runner tool.CommandRunner) tool.Environment {
	return tool.MustEnvironment(session.EnvironmentRef{Kind: session.EnvKindLocal, ID: ws.Root(), Revision: "in-tree-v1"}, ws, runner)
}
