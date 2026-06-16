package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/internal/adapter/slogdiag"
	"github.com/stacklok/mecatl/internal/app"
	"github.com/stacklok/mecatl/internal/cliconfig"
)

// flags is the parsed command-line configuration for mecatequi. It is a small,
// single-shot subset of mecated's config: the engine-build knobs (provider, model,
// workspace, posture, budgets, guardrails, ask-reviewer) plus the cmd-local I/O knobs
// (prompt inputs and the three output paths). It deliberately does NOT carry mecated's
// serve-time state (listeners, TLS, auth, rate-limit, telemetry) — mecatequi runs ONE
// prompt in-process and exits.
type flags struct {
	// Prompt inputs. At least one of prompt / promptFile is required; both may be
	// supplied (the literal is joined ahead of the file body). promptFileBody holds
	// the content read from promptFile at parse time (cmd mains may use os).
	prompt         string
	promptFile     string
	promptFileBody string
	// untrustedPrompt fences the prompt body via agent.FenceUntrusted (cmd-side
	// only). Default false: a normal CI prompt is the operator's own trusted task.
	untrustedPrompt bool
	// instructions is the TRUSTED operator-framing channel (--instructions). When
	// non-empty buildPrompt emits it OUTSIDE the untrusted fence (never fenced), ahead
	// of untrustedPromptInstruction — high-level framing such as how to format the final
	// message or to self-verify before finishing. It is a cmd-local prompt-assembly knob,
	// DELIBERATELY not mapped onto app.Config. Distinct from prompt/promptFile, which
	// carry the task and ARE fenced under untrustedPrompt.
	instructions string

	// Output sinks. "-" means stdout. The SUMMARY is the stdout default (so a bare
	// `mecatequi ... | jq` works); the diff and the event log are opt-in (empty
	// disables them) so the two stdout streams cannot collide. A setup-time guard
	// (validateOutputs) rejects two outputs resolving to the same non-empty sink.
	outDiff    string
	outSummary string
	outEvents  string

	// timeout is a wall-clock bound on the whole run (defense-in-depth for CI). 0
	// (default) disables it; a positive value wraps the run ctx in
	// context.WithTimeout. A timeout-cancelled run exits 1 with a "timed out" message.
	timeout time.Duration

	// Engine-build knobs mapped onto app.Config (see appConfig).
	workspace       string
	model           string
	defaultProvider string
	defaultModel    string
	useOpenAI       bool
	// providerFlags holds the shared provider base-URL flags + credential reads
	// (cliconfig) — the SAME helper mecated/mecatui use, so mecatequi reads ALL three
	// keys (OPENAI/OPENROUTER/ANTHROPIC_API_KEY) and registers all three base-URL flags
	// rather than the OpenAI-only subset it had. Applied onto app.Config in appConfig.
	providerFlags *cliconfig.ProviderFlags
	useMock       bool
	storeDir      string
	shell         string
	noBash        bool
	maxRunTokens  int
	maxTeamTokens int

	// headless declares NO human approver is attached. DEFAULT true (inverted from
	// mecated ON PURPOSE): a single-shot CI tool has nobody to answer a permission
	// ask, so a child's unresolved ask is auto-denied / routed to the opt-in
	// ask-reviewer rather than parked until run-end. Pass --headless=false only if
	// driving mecatequi from something that can answer asks (unusual for batch use).
	headless bool

	// Guardrails (issue #27): the checker model + the master kill-switch
	// (--guardrails=off).
	guardrailsModel string
	guardrailsMode  string
	guardrailsOff   bool

	// Headless ask reviewer (issue #31): model id, per-run consecutive-deny breaker,
	// and a TRUSTED policy rubric file whose CONTENT is read at parse time.
	subagentAskReviewer           string
	subagentAskReviewerMaxDenies  int
	subagentAskReviewerPolicyFile string
	subagentAskReviewerPolicy     string

	// Posture ladder (strict < trusted < auto < yolo). postureFlagSet records an
	// explicit --posture so composition lets CLI out-rank the settings.yaml key.
	posture        string
	postureFlagSet bool
}

// parseFlags turns argv into a flags value, resolving env-derived defaults and
// reading any file-backed inputs (the prompt file and the ask-reviewer policy). It
// mirrors cmd/mecated/main.go's parseFlags shape (a ContinueOnError FlagSet, env keys
// for secrets, an fs.Visit pass for the posture-set bit) but for mecatequi's small
// single-shot surface. Validation errors (missing prompt, unreadable file) are
// returned for main to map to the SETUP-failure exit code.
func parseFlags(argv []string) (flags, error) {
	fs := flag.NewFlagSet("mecatequi", flag.ContinueOnError)
	var f flags

	cwd, _ := os.Getwd()

	fs.StringVar(&f.prompt, "prompt", "", "the prompt to run (the agent's task). At least one of --prompt / --prompt-file is required; both may be given (literal first)")
	fs.StringVar(&f.promptFile, "prompt-file", "", "path to a file whose contents are the prompt body. At least one of --prompt / --prompt-file is required")
	fs.BoolVar(&f.untrustedPrompt, "untrusted-prompt", false, "treat the prompt body as UNTRUSTED data (e.g. a task description fetched from an external source): wrap it in the harness untrusted-data fence so the model treats it as data to act on, not instructions to obey. Default off (the prompt is the operator's own trusted task)")
	fs.StringVar(&f.instructions, "instructions", "", "TRUSTED operator framing emitted OUTSIDE the untrusted-prompt fence (never fenced): high-level instructions such as how to format the final message or to self-verify before finishing. Empty (default) omits it; NOTE the mecatequi GitHub Action sets a NON-EMPTY default (PR-description + self-verify framing — see its `instructions` input), so a CI run injects framing even though this binary's default is empty. Distinct from --prompt/--prompt-file, which carry the task and ARE fenced under --untrusted-prompt")

	fs.StringVar(&f.outSummary, "out-summary", "-", "where to write the run-summary JSON (\"-\" = stdout, the default). The summary is the machine-readable result Pipeline 2 consumes; pipe it to jq")
	fs.StringVar(&f.outDiff, "out-diff", "", "where to write the working-tree git diff the run produced (\"-\" = stdout). EMPTY (default) disables it — the summary already carries non_empty_diff and diff_bytes; opt in with a path when you want the patch. Cannot share a sink with --out-summary/--out-events")
	fs.StringVar(&f.outEvents, "out-events", "", "path for the durable event log (JSONL, one redacted session.Event per line). EMPTY (default) disables it. Cannot share a sink with --out-diff/--out-summary")
	fs.DurationVar(&f.timeout, "timeout", 0, "wall-clock bound on the whole run (e.g. 5m); a run that exceeds it is cancelled and exits 1 with a \"timed out\" message. 0 (default) = no timeout. Defense-in-depth for CI — orthogonal to --max-run-tokens")

	fs.StringVar(&f.workspace, "workspace", cwd, "session workspace root (must be a git repository so the diff can be computed)")
	fs.StringVar(&f.model, "model", "", "model identifier sent to the provider (empty: provider-appropriate default). PER-SESSION PASSTHROUGH: accepts any model the provider serves, including ids newer than the embedded catalog. Prefer this over --default-model for a newer/uncatalogued model")
	fs.StringVar(&f.defaultProvider, "default-provider", "", "deployment default provider id (e.g. openai, openrouter, anthropic); validated at startup")
	fs.StringVar(&f.defaultModel, "default-model", "", "deployment default model id for the default provider; validated against the embedded model catalog at startup and REJECTED if not in the snapshot — for a newer/uncatalogued model use --model instead, which passes through")
	fs.BoolVar(&f.useOpenAI, "openai", false, "use the OpenAI Responses provider (key from OPENAI_API_KEY)")
	// Shared provider base-URL flags + credential reads (cliconfig). Registers
	// --openai-base-url / --openrouter-base-url / --anthropic-base-url and reads
	// OPENAI/OPENROUTER/ANTHROPIC_API_KEY — so mecatequi can run Anthropic
	// (ANTHROPIC_API_KEY + --default-provider anthropic) and OpenRouter explicitly,
	// like its siblings. Default (mecated) help wording.
	f.providerFlags = cliconfig.RegisterProviderFlags(fs, cliconfig.ProviderFlagHelp{})
	fs.BoolVar(&f.useMock, "mock", false, "use a canned offline mock provider (no network; smoke tests only)")
	fs.StringVar(&f.storeDir, "store-dir", "", "directory for the JSONL session store (empty -> in-memory store)")
	fs.StringVar(&f.shell, "shell", "/bin/sh", "shell used to execute Bash-tool commands; empty disables Bash")
	fs.BoolVar(&f.noBash, "no-bash", false, "disable the Bash tool entirely (shell-less mode); overrides --shell")
	fs.IntVar(&f.maxRunTokens, "max-run-tokens", 0, "max cumulative input+output tokens per run; a run that crosses it ends cleanly with stop=budget. 0 = unlimited")
	fs.IntVar(&f.maxTeamTokens, "max-team-tokens", 0, "max cumulative input+output tokens per team run; 0 = unlimited")

	fs.BoolVar(&f.headless, "headless", true, "run NON-interactive (DEFAULT on, inverted from mecated): a single-shot CI run has no human approver, so a child subagent/member/branch permission ask is auto-denied / routed to the opt-in --subagent-ask-reviewer rather than parked until run-end. Pass --headless=false only when driving from something that can answer asks")

	fs.StringVar(&f.guardrailsModel, "guardrails-model", "", "GUARDRAILS (issue #27): tool-less checker model id / alias inspecting outbound args + inbound results. Empty disables guardrails. The rule list lives in the operator-tier settings.yaml guardrails: subtree")
	fs.StringVar(&f.guardrailsMode, "guardrails", "", "GUARDRAILS master switch: pass --guardrails=off to force the checker OFF regardless of --guardrails-model / the YAML config")

	fs.StringVar(&f.subagentAskReviewer, "subagent-ask-reviewer", "", "OPT-IN headless ask reviewer (issue #31): model id / alias of a tool-less one-turn reviewer adjudicating a child permission ask the headless auto-deny would otherwise reject. Empty disables it")
	fs.IntVar(&f.subagentAskReviewerMaxDenies, "subagent-ask-reviewer-max-denies", 3, "circuit breaker for --subagent-ask-reviewer: consecutive non-allow outcomes that disable the reviewer for the rest of the run; <=0 uses the default (3)")
	fs.StringVar(&f.subagentAskReviewerPolicyFile, "subagent-ask-reviewer-policy", "", "path to a TRUSTED policy rubric file for --subagent-ask-reviewer; its CONTENT replaces the built-in rubric. Read once at startup; an unreadable file fails startup")

	fs.StringVar(&f.posture, "posture", "", "OPERATOR POSTURE LADDER (strict < trusted < auto < yolo): strict (default) prompts every mutate — and a headless single-shot run has NO approver, so a main-agent ask CANCELS the run (exit 1). For an autonomous CI run use --posture auto (allow-all, child injection-defense ON) or trusted/yolo. trusted honours a project's ALLOW rules; auto adds allow-all + main substitution loosening; yolo additionally auto-runs $()/backtick/heredoc in children. An unknown value fails closed to strict")

	fs.Usage = usageEpilogue(fs)

	if err := fs.Parse(argv); err != nil {
		return flags{}, err
	}

	// Record whether --posture was set EXPLICITLY (vs the empty default) so
	// composition lets CLI out-rank the operator-global settings.yaml posture: key.
	fs.Visit(func(fl *flag.Flag) {
		if fl.Name == "posture" {
			f.postureFlagSet = true
		}
	})

	// Provider credentials are read from the environment by providerFlags.Apply
	// (called in appConfig), the shared cliconfig seam — never flag values, mirroring
	// mecated/mecatui.

	// Validate the prompt inputs: at least one source is required.
	if f.prompt == "" && f.promptFile == "" {
		return flags{}, errors.New("a prompt is required: pass --prompt <text> and/or --prompt-file <path>")
	}
	// Read the prompt file body (cmd mains may use os). An unreadable file is a
	// SETUP failure surfaced to the caller.
	if f.promptFile != "" {
		body, err := os.ReadFile(f.promptFile)
		if err != nil {
			return flags{}, fmt.Errorf("read --prompt-file %q: %w", f.promptFile, err)
		}
		f.promptFileBody = string(body)
	}

	// Read the trusted ask-reviewer policy rubric, if any (an unreadable file fails
	// startup, mirroring mecated).
	if f.subagentAskReviewerPolicyFile != "" {
		body, err := os.ReadFile(f.subagentAskReviewerPolicyFile)
		if err != nil {
			return flags{}, fmt.Errorf("read --subagent-ask-reviewer-policy %q: %w", f.subagentAskReviewerPolicyFile, err)
		}
		f.subagentAskReviewerPolicy = string(body)
	}

	// --guardrails=off is the master kill-switch (any other value leaves guardrails
	// governed by the model config), mirroring mecated.
	f.guardrailsOff = f.guardrailsMode == "off"

	// Reject two outputs resolving to the SAME non-empty sink (both "-" stdout, or
	// equal file paths) — they would interleave and corrupt each other.
	if err := validateOutputs(f); err != nil {
		return flags{}, err
	}

	return f, nil
}

// validateOutputs rejects a configuration where two of {out-summary, out-diff,
// out-events} resolve to the SAME non-empty sink. Two writers on one stream
// (stdout, or one file) interleave and corrupt each other (a patch + JSON on stdout
// breaks `| jq` and makes the patch unusable). An empty path means "disabled" and never
// collides. Stdout ("-") is compared as a distinct sink; file paths are compared after
// cleaning, best-effort (a symlink/relative alias slipping through is bounded — the
// O_NOFOLLOW|O_TRUNC writes still won't corrupt the wrong file silently).
func validateOutputs(f flags) error {
	type out struct{ flag, sink string }
	outs := []out{
		{"--out-summary", f.outSummary},
		{"--out-diff", f.outDiff},
		{"--out-events", f.outEvents},
	}
	seen := map[string]string{} // resolved sink -> the flag that claimed it
	for _, o := range outs {
		if o.sink == "" {
			continue // disabled
		}
		key := o.sink
		if key != "-" {
			key = filepath.Clean(key)
		}
		if prev, ok := seen[key]; ok {
			where := "stdout"
			if key != "-" {
				where = fmt.Sprintf("the same file %q", key)
			}
			return fmt.Errorf("%s and %s both write to %s — give them distinct paths (or set one to a path and leave the other on stdout/disabled)", prev, o.flag, where)
		}
		seen[key] = o.flag
	}
	return nil
}

// usageEpilogue returns a fs.Usage func that prints the default flag listing PLUS an
// exit-code / outcome-class section, so `--help` explains what an exit code MEANS — the
// no_progress/budget "exit 0 ≠ task accomplished" trap especially. It writes to the
// FlagSet's configured output.
func usageEpilogue(fs *flag.FlagSet) func() {
	return func() {
		out := fs.Output()
		_, _ = fmt.Fprintf(out, "mecatequi — single-shot, headless mecatl runner for CI / batch use.\n\n")
		_, _ = fmt.Fprintf(out, "Usage: mecatequi --prompt <text> [flags]\n\n")
		_, _ = fmt.Fprintf(out, "Flags:\n")
		fs.PrintDefaults()
		_, _ = fmt.Fprintf(out, `
Output routing:
  --out-summary defaults to stdout ("-"); --out-diff and --out-events are opt-in
  (empty = disabled). No two outputs may share a sink. Operational logs and the
  per-event human trace go to stderr, so a piped summary stays clean.

Exit codes (read stop_reason in the summary — the code alone is coarse):
  0  CLEAN terminal. Includes end_turn AND the "model did not finish" terminals
     no_progress / budget / max_turns / max_tool_calls / max_consecutive_failures /
     structured_output. EXIT 0 IS NOT "task accomplished" — check stop_reason and
     non_empty_diff to judge whether real work landed.
  1  RUN failure: a model error, a cancelled run, the no-approver cancel-on-ask
     (posture=strict + headless), or a --timeout. stop_reason tells error vs cancelled.
  2  SETUP failure: bad flags, missing prompt, build error, a non-git --workspace,
     a colliding output sink, or a write failure.
`)
	}
}

// appConfig maps the parsed flags onto the shared app.Config build contract. It
// threads a Diagnostics sink (stderr, the mecated pattern) and leaves Sink /
// ToolCallRecorder / MetricsRoleScoper nil — a single-shot CLI has no metrics
// pipeline. The Interactive inversion is the deliberate headless default: a CI run
// has no approver, so Interactive = !headless.
func appConfig(f flags, diag port.Diagnostics) app.Config {
	out := app.Config{
		Workspace:       f.workspace,
		Model:           f.model,
		DefaultProvider: f.defaultProvider,
		DefaultModel:    f.defaultModel,
		UseOpenAI:       f.useOpenAI,
		UseMock:         f.useMock,
		StoreDir:        f.storeDir,
		Shell:           f.shell,
		NoBash:          f.noBash,
		MaxRunTokens:    f.maxRunTokens,
		MaxTeamTokens:   f.maxTeamTokens,

		GuardrailsModel:    f.guardrailsModel,
		GuardrailsDisabled: f.guardrailsOff,

		SubagentAskReviewerModel:     f.subagentAskReviewer,
		SubagentAskReviewerMaxDenies: f.subagentAskReviewerMaxDenies,
		SubagentAskReviewerPolicy:    f.subagentAskReviewerPolicy,

		Posture:        app.ParsePosture(f.posture),
		PostureFlagSet: f.postureFlagSet,
		Privileged:     privilegedProcess(),

		// Interactive = !headless: the deliberate inversion. mecatequi defaults
		// headless=true (no approver), so a child's unresolved ask is auto-denied /
		// routed to the opt-in ask-reviewer rather than parked until run-end.
		Interactive: !f.headless,

		Diagnostics: diag,
		// Sink / ToolCallRecorder / MetricsRoleScoper deliberately nil: a single-shot
		// run carries no metrics pipeline.
	}
	// Apply the shared provider credentials + base URLs (env reads happen here, once).
	// An OPENAI_API_KEY in the environment implies the real provider — the same flip
	// mecated does — keyed off the resolved key.
	keys := f.providerFlags.Apply(&out)
	if keys.OpenAI != "" {
		out.UseOpenAI = true
	}
	return out
}

// privilegedProcess mirrors cmd/mecated/main.go's privilegedProcess: it reports
// "running as root WITHOUT a declared sandbox" (euid 0 && MECATL_SANDBOX/IS_SANDBOX
// unset), the SAME value fed to app.Config.Privileged so the posture root-refusal
// agrees with mecated. The cmd owns the os/env reads; internal/app takes the bool.
func privilegedProcess() bool {
	return os.Geteuid() == 0 && !sandboxDeclared()
}

func sandboxDeclared() bool {
	return os.Getenv("MECATL_SANDBOX") == "1" || os.Getenv("IS_SANDBOX") == "1"
}

// newDiagnostics builds the stderr slog Diagnostics sink (the mecated pattern):
// operational logging goes to stderr so stdout stays reserved for the summary/diff
// when those are written to "-".
func newDiagnostics() port.Diagnostics {
	return slogdiag.NewText(os.Stderr)
}
