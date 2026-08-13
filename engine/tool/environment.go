package tool

import (
	"errors"

	"github.com/stacklok/mecatl/engine/session"
)

// Environment is the concrete, immutable execution environment a Tool.Execute
// runs against. It bundles the three things a tool needs from the host to run
// in one session/fork namespace:
//
//   - an EnvironmentRef naming the backend family + opaque identity;
//   - a NON-NULL Workspace (the rooted, path-scoped filesystem seam — every
//     tool reads through it, and the agent-facing Edit/Write enforce their
//     read-before-edit / conditional-mutation invariants through it);
//   - an OPTIONAL bound CommandRunner (present when the host has a shell for
//     this namespace — the main session, a worktree or force-copy fork child;
//     absent for a file-less / in-memory / shell-less namespace). The Bash
//     tool reads it off the Environment; a nil runner surfaces ErrNoShell.
//
// It is NOT a service locator: it carries no policy, hooks, MCP, memory,
// forker, or merger. Those stay on the engine's Deps / the composition root.
// The Environment is the per-namespace CAPABILITY bundle threaded through the
// loop and handed to each Tool.Execute (as the env parameter), binding a
// Workspace + optional CommandRunner into one per-namespace seam.
//
// It is immutable: construct once with NewEnvironment, read via the
// accessors. A fork constructs a fresh child Environment bound to the child
// namespace (EnvironmentForker); the parent Environment is never mutated.
//
// The ZERO VALUE Environment{} has a nil Workspace and a nil CommandRunner
// and is INVALID for execution: NewEnvironment/MustEnvironment enforce a
// non-nil Workspace (the one mandatory capability), so every valid
// Environment carries a non-nil Workspace. Callers at trust boundaries MUST
// use NewEnvironment (handling its error) rather than assuming a zero value
// is usable; MustEnvironment is for construction sites where a nil Workspace
// is a programmer error.
type Environment struct {
	ref       session.EnvironmentRef
	workspace Workspace
	runner    CommandRunner
}

// ErrEnvironmentNoWorkspace is the sentinel NewEnvironment returns when a
// caller tries to build an Environment with a nil Workspace. A Workspace is
// mandatory for a coherent environment, but not every tool consumes it; FS
// tools do, remote/self-contained tools may ignore the environment's
// capabilities.
var ErrEnvironmentNoWorkspace = errors.New("tool: Environment requires a non-nil Workspace")

// NewEnvironment constructs an immutable Environment from a ref, a NON-NULL
// workspace, and an OPTIONAL bound command runner. A nil workspace is
// rejected (it is the one mandatory capability); a nil runner is allowed and
// means "no shell in this namespace" (the Bash tool surfaces ErrNoShell).
func NewEnvironment(ref session.EnvironmentRef, ws Workspace, runner CommandRunner) (Environment, error) {
	if ws == nil {
		return Environment{}, ErrEnvironmentNoWorkspace
	}
	return Environment{ref: ref, workspace: ws, runner: runner}, nil
}

// MustEnvironment constructs an Environment like NewEnvironment but panics on
// a nil workspace. Use it only at construction sites where a nil workspace is
// a programmer error (composition roots, test fixtures); prefer NewEnvironment
// at boundaries that read a config value.
func MustEnvironment(ref session.EnvironmentRef, ws Workspace, runner CommandRunner) Environment {
	env, err := NewEnvironment(ref, ws, runner)
	if err != nil {
		panic(err)
	}
	return env
}

// Ref returns the Environment's backend identity.
func (e Environment) Ref() session.EnvironmentRef { return e.ref }

// Workspace returns the rooted, path-scoped filesystem seam. It is non-nil
// for an Environment built through NewEnvironment/MustEnvironment (which
// reject a nil workspace); the zero-value Environment{} has a nil Workspace
// and is invalid for execution. Most tools consume it through
// Tool.Execute's env parameter rather than reading it from the
// Environment directly.
func (e Environment) Workspace() Workspace { return e.workspace }

// CommandRunner returns the bound command runner, or nil when the namespace
// has no shell. The Bash tool surfaces nil as ErrNoShell.
func (e Environment) CommandRunner() CommandRunner { return e.runner }
