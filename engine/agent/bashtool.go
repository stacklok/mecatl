package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

// bashToolMaxOutputBytes caps the byte length of one Bash tool result. It
// mirrors engine/adapter/fstools.MaxOutputBytes EXACTLY (both 25,000 bytes):
// engine/agent is core production code and must not import the fstools
// adapter, so the constant is redefined here — keep the two byte-identical.
const bashToolMaxOutputBytes = 25_000

// bashToolTruncationMarker is the suffix bashTruncate appends when it trims a
// body to the byte cap — mirrors fstools.TruncationMarker byte-identically.
const bashToolTruncationMarker = "\n... [output truncated: exceeded 25000 bytes]"

// maxBackgroundBashJobs bounds the live background-Bash jobs one parent run
// holds. It matches the Subagent background gate's scale (8): each live job is
// an OS process the run-end drain must cancel+join plus a 64 KiB tail buffer.
const maxBackgroundBashJobs = 8

// bashToolDescription is the model-facing documentation for the Bash tool. The
// foreground half is byte-identical to the fstools Bash description (the
// composition swap must not change the contract a foreground call sees); the
// trailing paragraph documents the background flag this variant adds.
const bashToolDescription = `Run a shell command in the workspace root and return its combined output and exit code.

When to use:
- To run builds, tests, linters, git, and other CLI tooling.
- For operations no dedicated tool covers.

When NOT to use:
- To read a file (use Read), search contents (use Grep), or find files by name
  (use Glob). Those tools give cleaner, line-numbered, capped output.

Behavior:
- The command runs with the workspace root as its working directory.
- Despite its name, Bash does not necessarily run Bash: it invokes "shell -c command"
  with the shell reported by "shell:" in the system prompt's <env> block (for
  example, "/bin/sh").
- The shell is non-interactive: it has no terminal or user input. Do not run
  interactive commands such as "git rebase -i", editors, pagers, or REPLs.
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

Background:
- Set background to true for a long-running command (a dev server, a watch loop,
  a slow build). The call returns immediately with a job id and the command
  keeps running while you continue.
- BashStatus is the SOLE channel to a background job: poll it for the retained
  output tail, collect the finished job's result, or cancel the job. Output is
  NOT delivered back to you automatically.
- A background job keeps only the most recent output (a bounded tail, older
  lines are dropped) and is cancelled if it is still running when this run
  ends.
- A background command runs in the REAL workspace root with no isolation: its
  effects may interleave with your own file changes and the tree is not stable
  while it runs. Prefer the foreground for anything whose result you need
  before acting.

Arguments:
- command    (required): the shell command line to run.
- timeout_ms (optional): cancel the command after this many milliseconds.
- background (optional): run detached and return a job id instead of waiting
  for the command to finish.

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

// BashTool is the agent-loop Bash tool: the foreground path is byte-identical
// to the fstools Bash body's orchestration (same arg validation, timeout ctx,
// runner.Run, combined-output shaping, exit-code error), and background:true
// detaches the command as a run-scoped background job on the parent run's
// child registry (the childCapableTool seam). It lives in engine/agent — not
// the fstools adapter — because the background half needs the child registry,
// which is an agent-package type.
//
// The tool registers under tool.BashToolName ("Bash"): the permission
// evaluator special-cases that literal name (resolveBash / planModeDecision /
// LearnableRule), so a second tool name would silently bypass the bash gate.
// Statically non-read-only: whether a specific command is read-only is
// governance's job, not this tool's.
//
// The runner is read off the tool.Environment at Execute time (issue #462),
// NOT captured at construction: a bound runner is part of the per-namespace
// Environment (main session, or a forked child whose runner is bound to the
// child namespace), so the command's cwd always matches the workspace the tool
// executes against, never a stale shared parent base. A namespace with no
// shell (env.CommandRunner == nil) surfaces ErrNoShell honestly. The
// composition root decides whether to REGISTER a Bash tool at all based on
// runner availability; a shell-less catalog simply omits Bash.
type BashTool struct{}

// NewBashTool constructs the Bash tool. The runner is NOT captured here — it
// is read off the tool.Environment at Execute time (issue #462). The
// composition root registers the returned tool ONLY when a runner is available
// for the namespace; without one, the catalog has no Bash and the agent runs
// shell-less. Background calls additionally require the bound runner to
// implement tool.CommandStreamer (they decline honestly when it does not).
func NewBashTool() tool.Tool {
	return BashTool{}
}

// Compile-time assertions: BashTool is a tool.Tool with the childCapableTool
// seam the dispatcher drives background calls through.
var (
	_ tool.Tool        = BashTool{}
	_ childCapableTool = BashTool{}
)

// bashArgs is the JSON argument shape for the Bash tool.
type bashArgs struct {
	Command    string `json:"command"`
	TimeoutMS  int    `json:"timeout_ms"`
	Background bool   `json:"background"`
}

// Spec returns the model-facing specification of the Bash tool.
func (BashTool) Spec() tool.ToolSpec {
	return tool.ToolSpec{
		Name:        tool.BashToolName,
		Description: bashToolDescription,
		Schema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "command": {"type": "string", "description": "Shell command line to run in the workspace root."},
    "timeout_ms": {"type": "integer", "description": "Optional timeout in milliseconds."},
    "background": {"type": "boolean", "description": "Optional: run detached and return a job id instead of waiting for the command to finish."}
  },
  "required": ["command"]
}`),
		// timeout_ms and background are intentionally OPTIONAL and absent from
		// "required" — the openai adapter sends tools NON-STRICT, and arg
		// validation happens at the execution edge (same discipline as the
		// fstools Bash spec; do NOT "fix" required).
	}
}

// ReadOnly reports that Bash is statically treated as mutating.
func (BashTool) ReadOnly() bool { return false }

// bashNoShellResult is the SINGLE composer for a nil-runner (shell-less
// namespace) site, so the no-shell message can never drift from
// bashErrorTrailer's ErrNoShell wording. It mirrors the fstools Bash tool's
// nil-runner path: an empty body + tool.ErrNoShell yields the standalone
// "[command failed to run: no shell available]" byte-identically.
func bashNoShellResult(callID session.ToolCallID) session.ToolResult {
	return session.NewToolError(callID, bashErrorMessage("", tool.ErrNoShell, 0))
}

// Execute runs a FOREGROUND Bash call. A background:true call must arrive via
// the childCapableTool seam (ExecuteWithParent), which owns the child
// registry; on the plain Execute path it gets an honest error, never a silent
// foreground fallback (the model was promised detached delivery).
func (t BashTool) Execute(ctx context.Context, in session.ToolCall, env tool.Environment) (session.ToolResult, error) {
	args, msg, ok := parseBashArgs(in)
	if !ok {
		return session.NewToolError(in.ID, msg), nil
	}
	if args.Background {
		return session.NewToolError(in.ID,
			"Bash: `background` is not supported on this run (no child registry); omit it to run in the foreground"), nil
	}
	runner := env.CommandRunner()
	if runner == nil {
		return bashNoShellResult(in.ID), nil
	}
	return t.runForeground(ctx, in.ID, args, runner), nil
}

// ExecuteWithParent is the childCapableTool seam. The foreground half is the
// same as Execute; the background half registers a run-scoped job on the
// parent run's child registry (cancelled at run end by its drain), detaches
// the drive, and returns the started-result immediately. It emits NO events —
// a background Bash job is not a delegation family; the started-result, the
// registry (notice/status/collect), and the stored result are the only
// channels.
func (t BashTool) ExecuteWithParent(ctx context.Context, in session.ToolCall, env tool.Environment, _ func(session.Event), caps parentCaps) (session.ToolResult, error) {
	args, msg, ok := parseBashArgs(in)
	if !ok {
		return session.NewToolError(in.ID, msg), nil
	}
	runner := env.CommandRunner()
	if !args.Background {
		if runner == nil {
			return bashNoShellResult(in.ID), nil
		}
		return t.runForeground(ctx, in.ID, args, runner), nil
	}
	if caps.children == nil {
		return session.NewToolError(in.ID,
			"Bash: `background` is not supported on this run (no child registry); omit it to run in the foreground"), nil
	}
	if runner == nil {
		return bashNoShellResult(in.ID), nil
	}
	streamer, ok := runner.(tool.CommandStreamer)
	if !ok {
		return session.NewToolError(in.ID,
			"Bash: `background` is not supported by this command runner (it cannot stream output); omit it to run in the foreground"), nil
	}

	// Job-count gate: FAIL-FAST, mirroring the Subagent background gate — a
	// background job holds its registry slot ACROSS turns, so blocking could
	// deadlock the model against itself. The read happens BEFORE the job's own
	// registration, so the error lists only genuinely-live jobs (the failing
	// call's own id can never appear in it).
	if ids := caps.liveBackgroundChildIDs(); len(ids) >= maxBackgroundBashJobs {
		return session.NewToolError(in.ID, bashJobGateFullError(ids)), nil
	}

	jobID := session.SessionID(BashCmdJobPrefix + string(in.ID))
	jobCtx, cancelJob := context.WithCancel(ctx)
	// Per-call wall-clock deadline: a hard ceiling on the job, independent of a
	// run-end cancel. timeoutCtx lets the terminal classification tell a
	// deadline-kill (DeadlineExceeded) apart from a parent cancellation.
	jobCtx, timeoutCtx, cancelTimeout := applyCallTimeout(jobCtx, &args.TimeoutMS)

	_ = caps.registerChildRun(jobCtx, jobID, childFamilyBashCmd, bashCommandLabel(args.Command), cancelJob, true)
	tail := newTailBuffer(maxBashJobTailBytes)
	caps.attachChildOutputTail(jobID, tail)
	caps.startChildRun(jobID)

	go t.driveBackground(jobCtx, timeoutCtx, jobID, args.Command, streamer, tail, cancelJob, cancelTimeout, caps)

	return session.NewToolResult(in.ID, fmt.Sprintf("background bash job started.\n\njob id: %s\n\n"+
		"It keeps running while you continue; a note will tell you when it finishes. "+
		"Poll its output or collect its result with BashStatus; it will be "+
		"cancelled if it is still running when this run ends.", jobID)), nil
}

// driveBackground is the detached goroutine owning one background-Bash job's
// remaining lifecycle: stream → classify the terminal → store the result. It
// is run-scoped: jobCtx derives from the parent run's, the run-end drain
// cancels and joins it (doneCh closes in the deferred finishChildRunResult),
// and it emits NOTHING — no events cross its goroutine boundary.
func (BashTool) driveBackground(jobCtx, timeoutCtx context.Context, jobID session.SessionID, command string, streamer tool.CommandStreamer, tail *tailBuffer, cancelJob, cancelTimeout context.CancelFunc, caps parentCaps) {
	exitCode, err := streamer.RunStreaming(jobCtx, command, tail)
	caps.setChildExitCode(jobID, exitCode)

	// Terminal classification, mirroring the Subagent background terminal
	// taxonomy: a clean exit is StopEndTurn; a non-zero exit or an execution
	// fault is StopError; a cancellation is StopCancelled UNLESS the deadline
	// fired (a timeout is a StopError with the partial tail, like the
	// foreground timeout path).
	stop := session.StopEndTurn
	switch {
	case err == nil && exitCode != 0:
		stop = session.StopError
	case errors.Is(err, context.DeadlineExceeded) || (timeoutCtx != nil && timeoutCtx.Err() != nil):
		stop = session.StopError
	case errors.Is(err, context.Canceled):
		stop = session.StopCancelled
	case err != nil:
		stop = session.StopError
	}
	res := bashJobTerminalResult(string(jobID), tail, exitCode, err, stop)

	// Ownership releases FIRST, the done signal LAST — the Subagent background
	// ordering: finishChildRunResult closes the registry doneCh the run-end
	// drain JOINS on, so the cancels must run happens-before that close.
	cancelJob()
	cancelTimeout()
	caps.finishChildRunResult(jobID, stop, &res)
}

// runForeground is the fstools Bash body's Execute orchestration, kept
// byte-identical: timeout ctx → runner.Run → combined output → exit-code
// error. It is re-implemented here (not imported) because engine/agent must
// not import the fstools adapter.
func (BashTool) runForeground(ctx context.Context, callID session.ToolCallID, args bashArgs, runner tool.CommandRunner) session.ToolResult {
	if args.TimeoutMS > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(args.TimeoutMS)*time.Millisecond)
		defer cancel()
	}

	res, err := runner.Run(ctx, args.Command)
	if err != nil {
		// Surface command-execution failures (no shell, timeout, cancellation)
		// to the model so it can adapt, preserving the runner's partial output
		// rather than collapsing to a bare "command failed".
		body := bashCombinedOutput(res, false) // includeExit=false: the ctx-error
		// path leaves ExitCode as a 0 placeholder; printing "[exit code: 0]"
		// would read as success, which is misleading on a failure.
		return session.NewToolError(callID, bashErrorMessage(body, err, args.TimeoutMS))
	}

	out := bashTruncate(bashCombinedOutput(res, true))
	if res.ExitCode != 0 {
		return session.NewToolError(callID, out)
	}
	return session.NewToolResult(callID, out)
}

// parseBashArgs unmarshals and validates a Bash call's JSON arguments, the
// fstools validation verbatim (plus the background flag's type, which
// session.ParseArgs checks).
func parseBashArgs(in session.ToolCall) (args bashArgs, msg string, ok bool) {
	if msg, ok := session.ParseArgs(in, &args); !ok {
		return bashArgs{}, msg, false
	}
	if strings.TrimSpace(args.Command) == "" {
		return bashArgs{}, "the \"command\" argument is required", false
	}
	if args.TimeoutMS < 0 {
		return bashArgs{}, "\"timeout_ms\" must be non-negative", false
	}
	return args, "", true
}

// bashCombinedOutput renders a CommandResult as the model-facing combined
// output: stdout then stderr (each newline-normalized), and — only when
// includeExit — a trailing "[exit code: N]" line. Byte-identical to the
// fstools helper of the same name.
func bashCombinedOutput(res tool.CommandResult, includeExit bool) string {
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

// bashErrorMessage composes the model-facing message for a runner error,
// keeping any partial output (body) and appending a trailer that explains why
// the command stopped — the fstools helper's exact contract: the trailer
// ALWAYS survives the output cap (the BODY is truncated first, reserving room
// for the trailer and the truncation marker), so a timed-out/runaway command
// that flooded its output can never lose the "timed out"/"canceled" signal.
func bashErrorMessage(body string, err error, timeoutMS int) string {
	noBodyMsg, trailer := bashErrorTrailer(err, timeoutMS)
	if body == "" {
		return bashTruncate(noBodyMsg)
	}
	suffix := "\n" + trailer
	budget := bashToolMaxOutputBytes - len(suffix) - len(bashToolTruncationMarker)
	if budget < 0 {
		budget = 0
	}
	return bashTruncateBody(body, budget) + suffix
}

// bashErrorTrailer returns the model-facing wording for a runner error,
// byte-identical to the fstools classification: deadline / cancellation first,
// then no-shell, then a generic default.
func bashErrorTrailer(err error, timeoutMS int) (noBodyMsg, trailer string) {
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

// bashJobTerminalResult renders the stored result a finished background-Bash
// job leaves for BashStatus collection: the retained output tail (capped like
// any Bash result) followed by a one-line terminal summary — the exit code, or
// the reason the job stopped, plus whether the tail was truncated. The error
// bit is set for a non-zero exit, a timeout, and an execution fault; a clean
// exit and a cancellation (matching the foreground ctx-error contract, which
// also reports cancellation as a tool error) follow their stop classification.
func bashJobTerminalResult(jobID string, tail *tailBuffer, exitCode int, err error, stop session.StopReason) session.ToolResult {
	body := bashTruncate(strings.TrimRight(tail.Snapshot(), "\n"))
	var summary string
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		summary = "[command timed out; output above is partial]"
	case errors.Is(err, context.Canceled):
		summary = "[command was canceled; output above is partial]"
	case err != nil:
		summary = fmt.Sprintf("[command failed to run: %v]", err)
	default:
		summary = fmt.Sprintf("[exit code: %d]", exitCode)
	}
	if tail.Truncated() {
		summary += " [older output dropped: retained the last 65536 bytes]"
	}
	content := summary
	if body != "" {
		content = body + "\n" + summary
	}
	res := session.NewToolResult(session.ToolCallID(jobID), content)
	if stop != session.StopEndTurn && stop != session.StopCancelled {
		res.IsError = true
	}
	return res
}

// bashJobGateFullError renders the background-Bash gate's fail-fast error:
// model-addressable, listing the currently-live background ids (ids ONLY —
// nothing model-authored) and the recoverable actions. It mirrors the Subagent
// gate's wording and names the jobs' own channel (BashStatus).
func bashJobGateFullError(ids []string) string {
	msg := "Bash: background job concurrency limit reached"
	if len(ids) > 0 {
		msg += "; currently running in the background: " + strings.Join(ids, ", ")
	}
	return msg + ". Wait for one to finish with BashStatus (use wait_ms), or run this command in the foreground."
}

// bashCommandLabel is the registry label for a background job: the command
// line normalized through the delegation families' own goal-label clamp
// (truncateGoal — single bounded line, ellipsis on overflow). It backs only
// the started-result echo and internal overlays, never roster or notice
// output (ids and enum labels only there).
func bashCommandLabel(command string) string {
	return truncateGoal(strings.TrimSpace(command))
}

// bashTruncate trims s to at most bashToolMaxOutputBytes on a rune boundary,
// appending bashToolTruncationMarker when it does — the fstools truncateBytes.
func bashTruncate(s string) string {
	return bashTruncateBody(s, bashToolMaxOutputBytes)
}

// bashTruncateBody is bashTruncate's bounded core (the fstools truncate): a
// rune-boundary cut so the result is never invalid UTF-8.
func bashTruncateBody(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	cut := maxBytes
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + bashToolTruncationMarker
}
