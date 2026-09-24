// Package hookexec implements port.HookRunner by running a configured shell
// command per lifecycle HookPhase. The HookEvent is serialized to JSON and
// written to the hook process's stdin; the process's exit code carries the
// outcome:
//
//	0          → allow (HookOutcome.Block == false)
//	2          → block (HookOutcome.Block == true); the message is read from
//	             stdout (preferred) or stderr
//	other != 0 → error (the hook itself failed)
//
// Mutation: on an ALLOW (exit 0), if the hook's stdout is a JSON OBJECT it is
// parsed as a control envelope: a "mutated" field (raw JSON) becomes
// HookOutcome.Mutated — the rewritten action payload the loop applies (a
// {"prompt": ...} object for UserPromptSubmit, the rewritten tool-args object for
// PreToolUse) — and an optional "message" string becomes HookOutcome.Message.
// Plain (non-JSON-object) stdout is treated as a message string exactly as
// before, so existing hooks are unaffected. (A JSON object is detected only when
// stdout begins with '{', so a hook printing arbitrary prose never trips it.)
//
// A nil or empty hook map means "no hooks configured": every event is allowed.
// Execution honours the caller's context and a per-run timeout.
package hookexec

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/internal/adapter/procgroup"
)

// DefaultTimeout bounds a single hook invocation when no timeout is supplied.
const DefaultTimeout = 30 * time.Second

// killGrace bounds how long Run waits for a killed hook's stdout/stderr pipes to
// drain after the timeout fires, before exec force-closes them and returns. It
// is a portable backstop: on POSIX systems the hook runs in its own process
// group that is killed as a unit (see procgroup.Configure), so the pipes
// normally close at once and this delay is never reached.
const killGrace = time.Second

const (
	exitAllow = 0
	exitBlock = 2
)

// Runner implements port.HookRunner over OS process execution. Construct it with
// New. The zero value runs no hooks (allows everything).
type Runner struct {
	hooks   map[governance.HookPhase]string
	timeout time.Duration
	shell   string
}

// Option configures a Runner.
type Option func(*Runner)

// WithTimeout sets the per-invocation timeout. A non-positive value resets to
// DefaultTimeout.
func WithTimeout(d time.Duration) Option {
	return func(r *Runner) {
		if d <= 0 {
			d = DefaultTimeout
		}
		r.timeout = d
	}
}

// WithShell overrides the shell used to interpret hook commands (default
// "/bin/sh"). Each command is run as `<shell> -c <command>`.
func WithShell(shell string) Option {
	return func(r *Runner) {
		if shell != "" {
			r.shell = shell
		}
	}
}

// New constructs a Runner that runs the given phase → shell-command map. A nil or
// empty map yields a Runner that allows every event.
func New(hooks map[governance.HookPhase]string, opts ...Option) *Runner {
	r := &Runner{
		hooks:   hooks,
		timeout: DefaultTimeout,
		shell:   "/bin/sh",
	}
	for _, o := range opts {
		o(r)
	}
	return r
}

// Run executes the hook registered for ev.Phase. With no hook for the phase the
// event is allowed. The HookEvent is delivered as JSON on the hook's stdin.
func (r *Runner) Run(ctx context.Context, ev governance.HookEvent) (port.HookResult, error) {
	cmd, ok := r.hooks[ev.Phase]
	if !ok || strings.TrimSpace(cmd) == "" {
		// No hook configured for this phase: allow.
		return port.HookResult{}, nil
	}

	payload, err := json.Marshal(ev)
	if err != nil {
		return port.HookResult{}, fmt.Errorf("hookexec: marshal event: %w", err)
	}

	runCtx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()

	var stdout, stderr bytes.Buffer
	c := exec.CommandContext(runCtx, r.shell, "-c", cmd)
	c.Stdin = bytes.NewReader(payload)
	c.Stdout = &stdout
	c.Stderr = &stderr

	// On timeout/cancel, exec kills only the direct child (the shell). A hook
	// command can spawn grandchildren (e.g. `sh -c "sleep 5"`) that inherit the
	// stdout/stderr pipes; with those pipes still open, (*Cmd).Wait blocks until
	// they exit — so the run would ignore the timeout and hang for the child's
	// full lifetime. procgroup.Configure kills the whole group on POSIX so the
	// pipes close promptly; WaitDelay bounds the wait everywhere as a backstop.
	c.WaitDelay = killGrace
	procgroup.Configure(c)

	runErr := c.Run()

	// Surface context cancellation / timeout explicitly rather than as a plain
	// exit error, so callers can distinguish an aborted run from a hook verdict.
	if ctxErr := runCtx.Err(); ctxErr != nil {
		return port.HookResult{}, fmt.Errorf("hookexec: %s hook: %w", ev.Phase, ctxErr)
	}

	if runErr == nil {
		// Exit 0 → allow (possibly carrying a mutation envelope on stdout).
		return port.HookResult{Outcome: allowOutcome(stdout.String())}, nil
	}

	var exitErr *exec.ExitError
	if errors.As(runErr, &exitErr) {
		switch exitErr.ExitCode() {
		case exitAllow:
			return port.HookResult{Outcome: allowOutcome(stdout.String())}, nil
		case exitBlock:
			return port.HookResult{Outcome: governance.HookOutcome{
				Block:   true,
				Message: blockMessage(&stdout, &stderr),
			}}, nil
		default:
			return port.HookResult{}, fmt.Errorf(
				"hookexec: %s hook exited %d: %s",
				ev.Phase, exitErr.ExitCode(), blockMessage(&stdout, &stderr))
		}
	}

	// Failure to start the process (e.g. bad shell): treat as an error.
	return port.HookResult{}, fmt.Errorf("hookexec: %s hook: %w", ev.Phase, runErr)
}

// allowOutcome builds the HookOutcome for an allowing hook (exit 0). When stdout
// is a JSON object it is parsed as a control envelope ({"mutated": ..., "message":
// ...}); otherwise the trimmed stdout is the message, preserving the original
// plain-text contract.
func allowOutcome(stdout string) governance.HookOutcome {
	s := trimmed(stdout)
	if !strings.HasPrefix(s, "{") {
		return governance.HookOutcome{Message: s}
	}
	var env struct {
		Mutated json.RawMessage `json:"mutated"`
		Message string          `json:"message"`
	}
	if err := json.Unmarshal([]byte(s), &env); err != nil {
		// Looked like JSON but did not parse: fall back to treating it as a message
		// so a malformed envelope is still surfaced rather than swallowed.
		return governance.HookOutcome{Message: s}
	}
	return governance.HookOutcome{Message: trimmed(env.Message), Mutated: env.Mutated}
}

// blockMessage prefers stdout, falling back to stderr, for the human-readable
// reason a hook surfaces.
func blockMessage(stdout, stderr *bytes.Buffer) string {
	if m := trimmed(stdout.String()); m != "" {
		return m
	}
	return trimmed(stderr.String())
}

func trimmed(s string) string { return strings.TrimSpace(s) }
