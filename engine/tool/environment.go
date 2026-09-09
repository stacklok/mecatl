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
//   - a NON-NULL Workspace (the rooted, path-scoped content/search/versioned-
//     mutation seam);
//   - a NON-NULL ReadLedger holding this session's read-before-write evidence,
//     independently selected from the Workspace content backend;
//   - an OPTIONAL bound CommandRunner (present when the host has a shell for
//     this namespace — the main session, a worktree or force-copy fork child;
//     absent for a file-less / in-memory / shell-less namespace). The Shell
//     tool reads it off the Environment; a nil runner surfaces ErrNoShell.
//
// It is NOT a service locator: it carries no policy, hooks, MCP, memory,
// forker, or merger. Those stay on the engine's Deps / the composition root.
// The Environment is the per-namespace CAPABILITY bundle threaded through the
// loop and handed to each Tool.Execute (as the env parameter), binding a
// Workspace + ReadLedger + optional CommandRunner into one per-namespace seam.
//
// It is immutable: construct once with NewEnvironment, read via the
// accessors. A fork constructs a fresh child Environment bound to the child
// namespace (EnvironmentForker); the parent Environment is never mutated.
//
// The ZERO VALUE Environment{} has nil mandatory capabilities and is INVALID
// for execution: NewEnvironment/MustEnvironment enforce a non-nil Workspace and
// ReadLedger, so every valid Environment carries both. Callers at trust boundaries
// MUST use NewEnvironment (handling its error) rather than assuming a zero value
// is usable; MustEnvironment is for construction sites where a nil mandatory
// capability is a programmer error.
type Environment struct {
	ref        session.EnvironmentRef
	workspace  Workspace
	readLedger ReadLedger
	runner     CommandRunner
}

// ErrEnvironmentNoWorkspace is the sentinel NewEnvironment returns when a
// caller tries to build an Environment with a nil Workspace. A Workspace is
// mandatory for a coherent environment, but not every tool consumes it; FS
// tools do, remote/self-contained tools may ignore the environment's
// capabilities.
var ErrEnvironmentNoWorkspace = errors.New("tool: Environment requires a non-nil Workspace")

// ErrEnvironmentNoReadLedger is returned when an Environment is constructed
// without its mandatory session-scoped read evidence capability.
var ErrEnvironmentNoReadLedger = errors.New("tool: Environment requires a non-nil ReadLedger")

// NewEnvironment constructs an immutable Environment from a ref, a NON-NULL
// workspace, a NON-NULL read ledger, and an OPTIONAL bound command runner.
func NewEnvironment(ref session.EnvironmentRef, ws Workspace, ledger ReadLedger, runner CommandRunner) (Environment, error) {
	if ws == nil {
		return Environment{}, ErrEnvironmentNoWorkspace
	}
	if ledger == nil {
		return Environment{}, ErrEnvironmentNoReadLedger
	}
	return Environment{ref: ref, workspace: ws, readLedger: ledger, runner: runner}, nil
}

// MustEnvironment constructs an Environment like NewEnvironment but panics on
// a nil mandatory capability. Use it only at construction sites where such a
// nil is a programmer error (composition roots, test fixtures); prefer NewEnvironment
// at boundaries that read a config value.
func MustEnvironment(ref session.EnvironmentRef, ws Workspace, ledger ReadLedger, runner CommandRunner) Environment {
	env, err := NewEnvironment(ref, ws, ledger, runner)
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

// ReadLedger returns the session-scoped read-before-write evidence capability.
// It is non-nil for every Environment built through the constructors.
func (e Environment) ReadLedger() ReadLedger { return e.readLedger }

// CommandRunner returns the bound command runner, or nil when the namespace
// has no shell. The Shell tool surfaces nil as ErrNoShell.
func (e Environment) CommandRunner() CommandRunner { return e.runner }
