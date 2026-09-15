package fstools

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/stacklok/mecatl/engine/internal/shellcompat"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

// ShellToolName aliases tool.ShellToolName, the single authority for the name the
// Shell tool registers under (the permission evaluator special-cases the literal,
// so the constant lives in the port package where every implementation — this
// adapter's AND engine/agent's background-capable ShellTool — can import it).
// Callers probe the catalog for Shell enablement by referencing the constant
// rather than a local literal that could drift on a rename (see
// internal/adapter/server.Service.capabilities).
const ShellToolName = tool.ShellToolName

// shellDescription is the model-facing documentation for the Shell tool.
const shellDescription = `Run a shell command in the workspace root and return its combined output and exit code.

When to use:
- To run builds, tests, linters, git, and other CLI tooling.
- For operations no dedicated tool covers.

When NOT to use:
- To read a file (use Read), search contents (use Grep), or find files by name
  (use Glob). Those tools give cleaner, line-numbered, capped output.

Behavior:
- The command runs with the workspace root as its working directory.
- Despite its name, Shell does not necessarily run Shell: it invokes "shell -c command"
  with the shell reported by "shell:" in the system prompt's <env> block (for
  example, "/bin/sh").
- When the configured shell path basename is sh or dash, Shell provides a limited
  compatibility diagnostic for [[ ... ]], process substitution, array expressions,
  and ANSI-C quotes before execution. This is feedback only: permission, guardrail,
  trust, and secret-scrubbing controls remain independent.
- The shell is non-interactive: it has no terminal or user input. Do not run
  interactive commands such as "git rebase -i", editors, pagers, or REPLs.
- Credential-shaped environment variables are scrubbed by default. An
  authentication failure does not prove that the operator is unauthenticated.
  Never inspect, echo, copy, write, or commit credentials available to a command.
- Under a subagent (a forked branch or an isolated team member) the working
  directory is a throwaway, isolated workspace (a git worktree or a copy), not the
  shared base — so commands you run there do not affect the parent's tree.
- Standard output and standard error are captured together and returned along
  with the process exit code. A non-zero exit code is reported, not hidden.
- If the command times out (exceeds timeout_ms) or is canceled, whatever output
  it produced before stopping is still returned, followed by a short trailer
  saying why it stopped. The result is marked an error, so on a timeout you can
  re-run with a larger timeout_ms after reading the partial output above. A
  cancellation or a "no shell available" failure is NOT retryable — do not re-run
  those.

Arguments:
- command    (required): the shell command line to run.
- timeout_ms (optional): cancel the command after this many milliseconds.

Example:
  {"command": "go test ./...", "timeout_ms": 120000}

Limits:
- Output is truncated to ~25000 bytes; redirect to a file and Read it in pages
  if you need more.
- Whether a given command is permitted is decided by the harness, not this tool.
- Command/process substitution or subshell grouping ($(...), backticks, <(...),
  (...)) may require approval and, in a non-interactive subagent shell, may be
  denied unless every part is read-only or a worktree-safe go test/build/vet/list.
  Prefer a direct command for inspection (run the inner command first, then use its
  output) when a substitution is not essential.`

// ShellTool runs a shell command via the CommandRunner bound to the
// tool.Environment it executes against (issue #462). It is statically
// classified as non-read-only: deciding whether a specific command is
// read-only is governance's job, not this tool's.
//
// The runner is read off the Environment at Execute time, NOT captured at
// construction: a bound runner is part of the per-namespace Environment (main
// session, or a forked child whose runner is bound to the child namespace), so
// the command's cwd always matches the workspace the tool executes against,
// never a stale shared parent base. A namespace with no shell (env.CommandRunner
// == nil) surfaces ErrNoShell honestly rather than aborting. The composition
// root decides whether to REGISTER a Shell tool at all based on runner
// availability; a shell-less catalog simply omits Shell.
//
// Residual: this fixes the runner's working DIRECTORY, not Shell's trust model.
// Unlike path-scoped Edit/Write (confined by os.Root), Shell can still escape its
// cwd via absolute paths or `cd` — that is inherent to running a shell, the same
// as in the main session. The fix removes the ACCIDENTAL shared-base mutation
// (a fork branch's relative-path Shell landing in the parent base), which is what
// ParallelTool.ReadOnly() / the read-only-share / mutating-fork isolation needs.
type ShellTool struct{}

// NewShellTool constructs the Shell tool. The runner is NOT captured here — it is
// read off the tool.Environment at Execute time (issue #462). The composition
// root registers the returned tool ONLY when a runner is available for the
// namespace; without one, the catalog has no Shell and the agent runs shell-less.
func NewShellTool() tool.Tool {
	return ShellTool{}
}

// Compile-time assertion that ShellTool implements tool.Tool.
var _ tool.Tool = ShellTool{}

// shellArgs is the JSON argument shape for the Shell tool.
type shellArgs struct {
	Command   string `json:"command"`
	TimeoutMS int    `json:"timeout_ms"`
	TempScope string `json:"temp_scope"`
}

// Spec returns the model-facing specification of the Shell tool.
func (ShellTool) Spec() tool.ToolSpec {
	return tool.ToolSpec{
		Name:        ShellToolName,
		Description: shellDescription,
		Schema: schema(`{
  "type": "object",
  "properties": {
    "command": {"type": "string", "description": "Shell command line to run in the workspace root."},
    "timeout_ms": {"type": "integer", "description": "Optional timeout in milliseconds."},
    "temp_scope": {"type": "string", "enum": ["managed", "system"], "description": "Optional temporary-storage scope; managed is disposable, system requests host-shared temporary storage."}
  },
  "required": ["command"]
}`),
		// timeout_ms is intentionally OPTIONAL and absent from "required". The openai
		// adapter sends tools NON-STRICT (see openai.buildTools), so a `required` that
		// omits an optional property is fine — do NOT "fix" this by adding timeout_ms
		// to required; strict mode is deliberately off and arg validation happens at
		// the execution edge.
	}
}

// ReadOnly reports that Shell is statically treated as mutating.
func (ShellTool) ReadOnly() bool { return false }

// Execute runs the command, honoring an optional timeout, and returns combined
// output with the exit code. The runner is read off env; a shell-less namespace
// (nil runner) surfaces ErrNoShell.
func (ShellTool) Execute(ctx context.Context, in session.ToolCall, env tool.Environment) (session.ToolResult, error) {
	var args shellArgs
	if msg, ok := parseArgs(in, &args); !ok {
		return session.NewToolError(in.ID, msg), nil
	}
	if strings.TrimSpace(args.Command) == "" {
		return session.NewToolError(in.ID, "the \"command\" argument is required"), nil
	}
	if args.TimeoutMS < 0 {
		return session.NewToolError(in.ID, "\"timeout_ms\" must be non-negative"), nil
	}
	if args.TempScope != "" && args.TempScope != string(tool.TemporaryScopeManaged) && args.TempScope != string(tool.TemporaryScopeSystem) {
		return session.NewToolError(in.ID, "\"temp_scope\" must be managed or system"), nil
	}
	runner := env.CommandRunner()
	if runner == nil {
		// Route through the SAME composer every runner-error path uses, so the
		// no-shell message can never drift from bashErrorTrailer's ErrNoShell
		// wording. An empty body + tool.ErrNoShell yields the standalone
		// "[command failed to run: no shell available]" byte-identically.
		return session.NewToolError(in.ID, shellErrorMessage("", tool.ErrNoShell, 0)), nil
	}

	if err := shellCompatibilityDiagnostic(runner, args.Command); err != nil {
		return session.NewToolError(in.ID, err.Error()), nil
	}

	if args.TimeoutMS > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(args.TimeoutMS)*time.Millisecond)
		defer cancel()
	}

	res, err := runWithTemporaryScope(ctx, runner, args.Command, fstoolsTemporaryScope(args.TempScope), args.TempScope != "")
	if err != nil {
		// Surface command-execution failures (no shell, timeout, cancellation)
		// to the model so it can adapt, rather than aborting the harness. The
		// runner returns whatever output it captured before the error alongside
		// the ctx error (see osfs.CommandRunner.Run), so preserve that partial
		// output here rather than discarding it: a timed-out build that printed
		// a useful failure should not collapse to a bare "command failed".
		body := shellCombinedOutput(res, false) // includeExit=false: the ctx-error
		// path leaves ExitCode as a 0 placeholder; printing "[exit code: 0]"
		// would read as success, which is misleading on a failure.
		return session.NewToolError(in.ID, shellErrorMessage(body, err, args.TimeoutMS)), nil
	}

	out := truncateBytes(shellCombinedOutput(res, true))
	if res.ExitCode != 0 {
		return session.NewToolError(in.ID, out), nil
	}
	return session.NewToolResult(in.ID, out), nil
}

func shellCompatibilityDiagnostic(runner tool.CommandRunner, command string) error {
	provider, ok := runner.(interface{ ShellPath() string })
	if !ok {
		return nil
	}
	return shellcompat.Check(provider.ShellPath(), command)
}

func fstoolsTemporaryScope(scope string) tool.TemporaryScope {
	if scope == string(tool.TemporaryScopeSystem) {
		return tool.TemporaryScopeSystem
	}
	return tool.TemporaryScopeManaged
}

func runWithTemporaryScope(ctx context.Context, runner tool.CommandRunner, command string, scope tool.TemporaryScope, requested bool) (tool.CommandResult, error) {
	if scoped, ok := runner.(tool.CommandTemporaryScopeRunner); ok {
		return scoped.RunWithTemporaryScope(ctx, command, scope)
	}
	if requested {
		return tool.CommandResult{}, errors.New("command runner does not support temporary scope selection")
	}
	return runner.Run(ctx, command)
}

// shellCombinedOutput renders a CommandResult as the model-facing combined output:
// stdout then stderr (each newline-normalized), and — only when includeExit — a
// trailing "[exit code: N]" line. The success path includes the exit line; the
// ctx-error path omits it (ExitCode is an unset 0 placeholder there).
func shellCombinedOutput(res tool.CommandResult, includeExit bool) string {
	var b strings.Builder
	if res.Stdout != "" {
		b.WriteString(res.Stdout)
		if !strings.HasSuffix(res.Stdout, "\n") {
			b.WriteByte('\n')
		}
	}
	if res.Stderr != "" {
		b.WriteString(res.Stderr)
		if !strings.HasSuffix(res.Stderr, "\n") {
			b.WriteByte('\n')
		}
	}
	if includeExit {
		fmt.Fprintf(&b, "[exit code: %d]", res.ExitCode)
	}
	return b.String()
}

// shellErrorMessage composes the model-facing message for a runner error, keeping
// any partial output (body) and appending a trailer that explains why the command
// stopped. timeoutMS is the caller-requested timeout (0 if unset) — used only to
// name the configured limit on a deadline; the runner's own default timeout is
// private to it and is never fabricated here.
//
// The trailer ALWAYS survives the output cap: a timed-out/runaway command commonly
// produces output far larger than MaxOutputBytes, so this truncates the
// BODY first (reserving room for the trailer) and then appends the trailer, rather
// than truncating the joined string — which would land the cut inside the body and
// drop the "timed out"/"canceled" signal entirely, leaving the model to read a
// truncated result as an ordinary too-long one.
func shellErrorMessage(body string, err error, timeoutMS int) string {
	noBodyMsg, trailer := shellErrorTrailer(err, timeoutMS)
	if body == "" {
		// No partial output: the standalone phrasing IS the whole message.
		return truncateBytes(noBodyMsg)
	}
	// Reserve room for the trailer (plus its leading newline) AND for the
	// truncation marker truncate appends when it trims the body — so the final
	// string fits the cap with the timeout/cancel reason intact.
	suffix := "\n" + trailer
	budget := MaxOutputBytes - len(suffix) - len(TruncationMarker)
	if budget < 0 {
		budget = 0
	}
	return truncate(body, budget) + suffix
}

// shellErrorTrailer returns the model-facing wording for a runner error: noBodyMsg
// is the self-contained message when the command produced no output; trailer is
// the line appended after any partial output. Classification order: deadline /
// cancellation first, then no-shell, then a generic default.
func shellErrorTrailer(err error, timeoutMS int) (noBodyMsg, trailer string) {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		if timeoutMS > 0 {
			return fmt.Sprintf("[command produced no output and timed out after %dms]", timeoutMS),
				fmt.Sprintf("[command timed out after %dms; output above is partial]", timeoutMS)
		}
		return "[command produced no output and timed out]",
			"[command timed out; output above is partial]"
	case errors.Is(err, context.Canceled):
		return "[command was canceled before producing output]",
			"[command was canceled; output above is partial]"
	case errors.Is(err, tool.ErrNoShell):
		return "[command failed to run: no shell available]",
			"[command failed to run: no shell available]"
	default:
		return fmt.Sprintf("command failed to run: %v", err),
			fmt.Sprintf("[command failed to run: %v]", err)
	}
}
