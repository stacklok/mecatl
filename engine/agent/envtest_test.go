// Package agent — test helpers for the Environment seam (issue #462).
//
// This file is TEST-ONLY and provides a tiny constructor the agent package's
// tests use to wrap a Workspace (and optional runner) into a tool.Environment
// without each test repeating the ref/panic dance. It is not imported by
// production code.

package agent

import (
	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/memledger"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

func testReadLedger() tool.ReadLedger { return memledger.New() }

// testEnvironment wraps a Workspace into a tool.Environment for tests, with a
// "test" backend ref and an optional bound runner. It panics on a nil workspace
// (which would be a test-setup bug, not a runtime condition).
func testEnvironment(ws tool.Workspace, runner tool.CommandRunner) tool.Environment {
	return tool.MustEnvironment(session.EnvironmentRef{Kind: session.EnvKindLocal, ID: ws.Root(), Revision: "in-tree-v1"}, ws, testReadLedger(), runner)
}

// memEnv builds a shell-less in-memory Environment rooted at root for the many
// agent tests that drive an engine against a throwaway memfs workspace without
// a command runner (issue #462).
func memEnv(root string) tool.Environment {
	return tool.MustEnvironment(session.EnvironmentRef{Kind: session.EnvKindLocal, ID: root, Revision: "in-tree-v1"}, memfs.NewWorkspace(root), testReadLedger(), nil)
}

// MemEnv is the exported form of memEnv for the external agent_test package.
func MemEnv(root string) tool.Environment { return memEnv(root) }

// ForkEnv wraps a forked child Workspace into a shell-less Environment tagged
// with the fork root (exported for the external agent_test package, issue #462).
func ForkEnv(ws tool.Workspace) tool.Environment {
	return forkEnv(ws)
}

// forkEnv wraps a forked child Workspace into a shell-less Environment tagged
// with the fork root, for test forkers (issue #462: Fork returns an Environment).
func forkEnv(ws tool.Workspace) tool.Environment {
	return tool.MustEnvironment(session.EnvironmentRef{Kind: session.EnvKindMem, ID: ws.Root(), Revision: "test-v1"}, ws, testReadLedger(), nil)
}

// memEnvRunner builds an in-memory Environment rooted at root with a bound runner.
func memEnvRunner(root string, runner tool.CommandRunner) tool.Environment {
	return tool.MustEnvironment(session.EnvironmentRef{Kind: session.EnvKindLocal, ID: root, Revision: "in-tree-v1"}, memfs.NewWorkspace(root), testReadLedger(), runner)
}

// MemEnvRunner is the exported form of memEnvRunner for the external agent_test
// package (issue #462): an in-memory Environment rooted at root with a bound
// command runner, so a test's Shell observes the runner the way a real session's
// Shell reads it off the Environment.
func MemEnvRunner(root string, runner tool.CommandRunner) tool.Environment {
	return memEnvRunner(root, runner)
}
