// Command mecatequi is a single-shot, headless mecatl runner for CI / batch use: it
// runs ONE prompt against an in-process engine assembled by internal/app, drives the
// run to a terminal state, and emits three artifacts — a working-tree git diff, a
// machine-readable run-summary JSON, and an optional durable JSONL event log — then
// maps the terminal stop reason to a process exit code.
//
// It is deliberately small. Unlike mecated (a network daemon) it owns no listeners,
// TLS, auth, rate-limiting, or telemetry pipeline; unlike mecatui it has no UI. It
// shares the SAME engine/service assembly (app.Build) so its behaviour matches the
// daemon's.
//
// Pipeline 1 scope: the untrusted-prompt fence is cmd-side ONLY — mecatequi builds the
// fenced prompt string with the existing agent.FenceUntrusted helper and passes it as
// ordinary prompt text. Nothing in engine/agent, internal/app, or
// internal/adapter/server is modified for it.
//
// DEVIATION FROM mecated (on purpose): --headless defaults to true. A single-shot CI
// tool has no human to answer a permission ask, so a CHILD subagent/member/branch
// permission ask is auto-denied / routed to the opt-in --subagent-ask-reviewer rather
// than parked until run-end. See flags.headless.
//
// HEADLESS × POSTURE (the main-agent ask): the child auto-deny above covers CHILD asks
// only. A MAIN-engine ask still has nobody to answer it. With the default posture
// (strict), the main engine asks on every mutate — so a strict + headless run that
// reaches a mutate is CANCELLED on that ask (run.Cancel, drain-to-close) and exits 1
// with an actionable "no approver" message naming the fix. The intended CI posture is
// therefore --posture auto (allow-all, child injection-defense ON) or trusted/yolo,
// where the main engine does not ask. --timeout is the orthogonal wall-clock backstop.
// See run()'s cancel-on-ask path and flags.posture.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"syscall"

	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/app"
)

func main() {
	os.Exit(realMain(os.Args[1:], os.Stdout, os.Stderr))
}

// realMain is the testable entry point: it returns the process exit code rather than
// calling os.Exit, so a test can drive the whole flag->build->run->emit path and
// assert the code. stdout carries the "-" outputs (the summary by default); stderr
// carries operational diagnostics, the per-event human trace, the final verdict line,
// and setup-failure messages — so a piped summary stays clean.
//
// Exit codes: 0 = a clean terminal (end_turn or a clean non-completion); 1 = a run
// terminal of error/cancelled (incl. the no-approver cancel-on-ask and --timeout); 2 =
// a SETUP failure (bad flags, missing prompt, Build/CreateSession error, a non-git or
// non-top-level --workspace, a colliding output sink, write failure). A best-effort
// summary is written before a setup-failure exit where one is available.
func realMain(argv []string, stdout, stderr io.Writer) int {
	f, err := parseFlags(argv)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "mecatequi: %v\n", err)
		return 2
	}

	diag := newDiagnostics()

	// Validate the workspace is a git repository AND its top level BEFORE building —
	// a non-repo or a subdir would silently diff an enclosing repo (the wrong forge
	// artifact). This is a setup failure (exit 2).
	if werr := validateWorkspaceRepo(context.Background(), f.workspace); werr != nil {
		_, _ = fmt.Fprintf(stderr, "mecatequi: %v\n", werr)
		return 2
	}

	built, err := app.Build(context.Background(), appConfig(f, diag))
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "mecatequi: build: %v\n", err)
		return 2
	}
	defer built.Close()

	prompt := buildPrompt(f.prompt, f.promptFileBody, f.instructions, f.untrustedPrompt)

	// Wall-clock bound (defense-in-depth for CI): wrap the run ctx when --timeout > 0.
	ctx := context.Background()
	var cancel context.CancelFunc
	if f.timeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, f.timeout)
		defer cancel()
	}

	// Drive the run. The per-event human render goes to stderr. --max-turns rides as
	// the session's MaxTurns; a zero value inherits the deployment default in
	// CreateSession (per-field, so the tool-call / failure caps survive regardless).
	outcome, err := run(ctx, built.Service, f.workspace, session.Limits{MaxTurns: f.maxTurns}, prompt, stderr)
	if err != nil {
		// A setup failure inside run (e.g. CreateSession). Emit whatever summary we
		// have (it carries at least the session id when known) before exiting 2.
		_, _ = fmt.Fprintf(stderr, "mecatequi: run: %v\n", err)
		_ = emitSummary(f, stdout, outcome.Summary)
		return 2
	}
	sum := outcome.Summary

	// Compute the actual working-tree diff (the HONESTY invariant: NonEmptyDiff
	// reflects reality). The repo was already validated above, so a diff error here is
	// unexpected but still a setup-class failure.
	patch, nonEmpty, derr := gitDiffPatch(context.Background(), f.workspace)
	if derr != nil {
		_, _ = fmt.Fprintf(stderr, "mecatequi: diff: %v\n", derr)
		_ = emitSummary(f, stdout, sum)
		return 2
	}
	sum.NonEmptyDiff = nonEmpty
	sum.DiffBytes = len(patch)

	// Emit the three artifacts. A write failure on any of them is a setup-class
	// failure (exit 2) — the run itself succeeded, but the deliverable could not be
	// persisted, which the caller must learn about.
	if werr := emitDiff(f, stdout, patch); werr != nil {
		_, _ = fmt.Fprintf(stderr, "mecatequi: write diff: %v\n", werr)
		return 2
	}
	if werr := emitEvents(f, outcome.Events); werr != nil {
		_, _ = fmt.Fprintf(stderr, "mecatequi: write events: %v\n", werr)
		return 2
	}
	if werr := emitSummary(f, stdout, sum); werr != nil {
		_, _ = fmt.Fprintf(stderr, "mecatequi: write summary: %v\n", werr)
		return 2
	}

	// Final operator-facing verdict on stderr (honest: reflects the real stop reason +
	// diff), so a human watching the run sees the outcome without parsing JSON.
	_, _ = fmt.Fprintln(stderr, verdictLine(sum, outcome.NoApprover, ctx.Err() == context.DeadlineExceeded))

	return exitCode(sum)
}

// verdictLine is the one-line human summary printed to stderr after the run. It is
// honest: it names the real stop reason and the actual file-change signal, and it adds
// the actionable cause for the two non-obvious exit-1 paths (no approver, timeout).
func verdictLine(sum Summary, noApprover, timedOut bool) string {
	files := "0 files changed"
	if sum.NonEmptyDiff {
		files = "files changed"
	}
	stop := sum.StopReason
	if stop == "" {
		stop = "(no terminal)"
	}
	switch {
	case timedOut:
		return fmt.Sprintf("mecatequi: TIMED OUT — stop=%s, %s; the run exceeded --timeout and was cancelled", stop, files)
	case noApprover:
		return "mecatequi: NO APPROVER — a permission approval was requested but none is attached (--headless); " +
			"re-run with --posture auto|trusted|yolo or add allow rules. stop=" + stop
	default:
		return fmt.Sprintf("mecatequi: done — stop=%s, %s", stop, files)
	}
}

// emitDiff writes the git patch to --out-diff ("-" = stdout).
func emitDiff(f flags, stdout io.Writer, patch []byte) error {
	return writeTo(f.outDiff, stdout, func(w io.Writer) error {
		_, err := w.Write(patch)
		return err
	})
}

// emitSummary writes the Summary JSON (indented, trailing newline) to --out-summary
// ("-" = stdout). Under the stdout-compact mode (an EXPLICIT --out-summary=-, issue
// #341) it instead emits the Summary as ONE compact JSON line — realMain emits the
// summary LAST on stdout, so that line is the FINAL stdout line a log-tailing
// scheduler parses; nothing may be written to stdout after it.
func emitSummary(f flags, stdout io.Writer, sum Summary) error {
	return writeTo(f.outSummary, stdout, func(w io.Writer) error {
		enc := json.NewEncoder(w)
		if !f.summaryCompact {
			enc.SetIndent("", "  ")
		}
		// json.Encoder.Encode without SetIndent emits compact JSON + exactly one
		// trailing newline — the single-line contract.
		return enc.Encode(sum)
	})
}

// emitEvents writes the durable JSONL event log to --out-events. An empty path
// disables the log (no-op, no file created).
func emitEvents(f flags, events []session.Event) error {
	if f.outEvents == "" {
		return nil
	}
	return writeTo(f.outEvents, nil, func(w io.Writer) error {
		return writeDurableLog(w, events)
	})
}

// writeTo routes an output to stdout (path == "-") or a file, invoking write with the
// resolved writer. A file is opened O_WRONLY|O_CREATE|O_TRUNC|O_NOFOLLOW at 0o600: it
// refuses to follow a symlink (so a planted symlink at the output path cannot redirect
// the write to an attacker-chosen file) and creates the artifact owner-only. A file is
// closed after write.
func writeTo(path string, stdout io.Writer, write func(io.Writer) error) error {
	if path == "" || path == "-" {
		return write(stdout)
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return err
	}
	if werr := write(file); werr != nil {
		_ = file.Close()
		return werr
	}
	return file.Close()
}
