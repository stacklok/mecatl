package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/internal/adapter/slogdiag"
	"github.com/stacklok/mecatl/internal/app"
	"github.com/stacklok/mecatl/internal/cliconfig"
)

type stringList []string

func (s *stringList) String() string { return strings.Join(*s, ",") }
func (s *stringList) Set(value string) error {
	*s = append(*s, value)
	return nil
}

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
	// summaryCompact selects the stdout-compact summary mode (issue #341): an
	// EXPLICIT --out-summary=- (the flag given, value "-") emits the Summary as a
	// SINGLE compact JSON line as the FINAL stdout line, so a scheduler tailing
	// pod logs can parse "the last line". The unset default — which also resolves
	// to "-" — keeps the indented JSON (default behavior unchanged), as does an
	// explicit file path. Distinguished via fs.Visit in parseFlags.
	summaryCompact bool

	// timeout is a wall-clock bound on the whole run (defense-in-depth for CI). 0
	// (default) disables it; a positive value wraps the run ctx in
	// context.WithTimeout. A timeout-cancelled run exits 1 with a "timed out" message.
	timeout time.Duration

	// Engine-build knobs mapped onto app.Config (see appConfig).
	workspace              string
	model                  string
	defaultProvider        string
	defaultModel           string
	defaultProviderFlagSet bool
	useOpenAI              bool
	// providerFlags holds shared provider flag bindings; providerCredentials is
	// the once-resolved snapshot projected by appConfig without further I/O.
	providerFlags       *cliconfig.ProviderFlags
	providerCredentials cliconfig.ResolvedCredentials
	// toolhiveLLMFlags holds --toolhive-llm / --toolhive-llm-base-url (issue
	// #262). A CI runner pod naturally has no ToolHive config file, so this is
	// inert by default (register-on-intent finds nothing to register) —
	// --toolhive-llm=false is still recommended on a SHARED host.
	toolhiveLLMFlags *cliconfig.ToolhiveLLMFlags
	// mcpServers holds the repeatable --mcp-server name=URL entries (issue #341,
	// the factory MCP wiring), via the SAME cliconfig.MCPServerList helper as
	// mecated/mecak8s: a per-server bearer rides the MCP_<NAME>_TOKEN env (a
	// scheduler like titlani injects a short-lived per-run identity there), token
	// optional. Threaded onto app.Config.MCPServers in appConfig.
	mcpServers        *cliconfig.MCPServerList
	permissionConfigs stringList
	useMock           bool
	storeDir          string
	shell             string
	noBash            bool
	maxRunTokens      int
	maxTeamTokens     int
	// maxTurns caps the session's model calls (the StopMaxTurns terminal). 0
	// (default/unset) inherits the composition default (internal/app build.go), so
	// it is NOT mapped onto app.Config — it is a per-SESSION limit threaded to
	// CreateSession via run(), not an engine-build knob. A positive value tightens
	// (or raises) the turn cap for this single-shot run.
	maxTurns int

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

	// Subagent model router (ADR 0031; enable model per ADR 0042): the router is ENABLED
	// by the operator-tier models.router: taxonomy (the guardrails-parity enable model).
	// The --subagent-model-router flag is a KILL-SWITCH: subagentModelRouter holds its
	// value, subagentModelRouterSet records whether it was given. =false sets
	// RouterDisabled; a bare flag / =true is a harmless no-op (the router stays governed
	// by the taxonomy); unset leaves routing governed by taxonomy presence.
	subagentModelRouter    bool
	subagentModelRouterSet bool

	// Posture ladder (strict < trusted < auto < yolo). postureFlagSet records an
	// explicit --posture so composition lets CLI out-rank the settings.yaml key.
	posture        string
	postureFlagSet bool
	// Reasoning-effort tier (ADR 0055). reasoningEffortFlagSet records an explicit
	// --reasoning-effort so composition lets CLI out-rank the settings.yaml key.
	reasoningEffort        string
	reasoningEffortFlagSet bool
	// trustProject opts INTO project-tier ingestion (the repo's AGENTS.md/CLAUDE.md,
	// .mecatl/.claude rules, agents, skills, soul, slash commands, ALLOW rules, git
	// snapshot) on a HEADLESS root where the posture ladder does NOT grant it. DEFAULT
	// OFF (the fail-safe default: a CI run over a freshly-cloned untrusted repo
	// ingests NONE of the repo's steering). Matching the other three roots.
	trustProject bool

	// Headless telemetry (issue #343): OPT-IN OTLP trace + metrics push. A
	// single-shot CI run is too short-lived for a Prometheus scrape, so mecatequi
	// PUSHES metrics (and traces) to an OTLP collector and flushes before exit
	// via --otlp-shutdown-timeout. Both endpoints empty (the default) leaves the
	// pipeline off — the byte-identical no-telemetry posture.
	otlpEndpoint        string
	otlpProtocol        string
	otlpInsecure        bool
	otlpMetricsEndpoint string
	otlpMetricsProtocol string
	otlpShutdownTimeout time.Duration
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

	fs.StringVar(&f.outSummary, "out-summary", "-", "where to write the run-summary JSON (\"-\" = stdout, the default). The summary is the machine-readable result Pipeline 2 consumes; pipe it to jq. Passing --out-summary=- EXPLICITLY selects the stdout-COMPACT mode: the Summary is emitted as a SINGLE compact JSON line as the FINAL stdout line (nothing follows it), so a scheduler tailing logs can parse the last line; the unset default keeps the indented JSON")
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
	// ToolHive LLM gateway (issue #262): a CI runner pod naturally has no
	// ToolHive config file, so this is inert unless the operator explicitly
	// points --toolhive-llm-base-url at a reachable proxy.
	f.toolhiveLLMFlags = cliconfig.RegisterToolhiveLLMFlags(fs, cliconfig.DefaultToolhiveLLMFlagHelp)
	// Remote MCP servers (issue #341): the shared repeatable name=URL flag +
	// MCP_<NAME>_TOKEN bearer convention, identical to mecated/mecak8s.
	f.mcpServers = cliconfig.RegisterMCPServerFlag(fs, "")
	fs.Var(&f.permissionConfigs, "permission-config", "explicit operator settings YAML (repeatable); uses the same precedence and strict parser as conventional settings")
	fs.BoolVar(&f.useMock, "mock", false, "use a canned offline mock provider (no network; smoke tests only)")
	fs.StringVar(&f.storeDir, "store-dir", "", "directory for the JSONL session store (empty -> in-memory store)")
	fs.StringVar(&f.shell, "shell", "/bin/sh", "shell used to execute Bash-tool commands; empty disables Bash")
	fs.BoolVar(&f.noBash, "no-bash", false, "disable the Bash tool entirely (shell-less mode); overrides --shell")
	fs.IntVar(&f.maxRunTokens, "max-run-tokens", 0, "max cumulative input+output tokens per run; a run that crosses it ends cleanly with stop=budget. 0 = unlimited")
	fs.IntVar(&f.maxTeamTokens, "max-team-tokens", 0, "max cumulative input+output tokens per team run; 0 = unlimited")
	fs.IntVar(&f.maxTurns, "max-turns", 0, "max model calls (turns) for the run; a run that crosses it ends cleanly with stop=max_turns. 0 (default) uses the deployment default; a positive value caps this single-shot run. Orthogonal to --max-run-tokens (turns vs tokens; both compose)")

	fs.BoolVar(&f.headless, "headless", true, "run NON-interactive (DEFAULT on, inverted from mecated): a single-shot CI run has no human approver, so a child subagent/member/branch permission ask is auto-denied / routed to the opt-in --subagent-ask-reviewer rather than parked until run-end. Pass --headless=false only when driving from something that can answer asks")

	fs.StringVar(&f.guardrailsModel, "guardrails-model", "", "GUARDRAILS (issue #27): tool-less checker model id / alias inspecting outbound args + inbound results. Configuring a model here OR via a bound `guardrail` model slot (models.slots.guardrail) ENABLES guardrails (configure = enable, ADR 0046); empty + no slot disables them. A bound `guardrail` slot SUPERSEDES this flag's model. The rule list lives in the operator-tier settings.yaml guardrails: subtree")
	fs.StringVar(&f.guardrailsMode, "guardrails", "", "GUARDRAILS KILL-SWITCH only: pass --guardrails=off to force the checker OFF regardless of --guardrails-model / the `guardrail` slot / the YAML config. The positive enable path is configuring a checker model (--guardrails-model OR the `guardrail` slot), NOT this flag")

	fs.StringVar(&f.subagentAskReviewer, "subagent-ask-reviewer", "", "OPT-IN headless ask reviewer (issue #31): model id / alias of a tool-less one-turn reviewer adjudicating a child permission ask the headless auto-deny would otherwise reject. Empty disables it")
	fs.BoolVar(&f.subagentModelRouter, "subagent-model-router", false, "Semantic model router KILL-SWITCH (ADR 0042, superseding 0031's enable model): the router is ENABLED by an operator-tier models.router: category taxonomy (configure = enable, guardrails-parity), NOT by this flag. Pass --subagent-model-router=false to force it OFF despite a taxonomy (also models.router.disabled: true in YAML). When enabled, a tiny classifier on the `router` slot picks the child model per plain Subagent delegation before the child is minted (decide-once, same-provider); fail-soft to the inherited model on any miss")
	fs.IntVar(&f.subagentAskReviewerMaxDenies, "subagent-ask-reviewer-max-denies", agent.DefaultAskReviewMaxDenies, "circuit breaker for --subagent-ask-reviewer: consecutive non-allow outcomes that disable the reviewer for the rest of the run; <=0 uses the default (3)")
	fs.StringVar(&f.subagentAskReviewerPolicyFile, "subagent-ask-reviewer-policy", "", "path to a TRUSTED policy rubric file for --subagent-ask-reviewer; its CONTENT replaces the built-in rubric. Read once at startup; an unreadable file fails startup")

	fs.StringVar(&f.posture, "posture", "", "OPERATOR POSTURE LADDER (strict < trusted < auto < yolo): strict (default) prompts every mutate — and a headless single-shot run has NO approver, so a main-agent ask CANCELS the run (exit 1). For an autonomous CI run use --posture auto (allow-all, child injection-defense ON) or trusted/yolo. trusted honours a project's ALLOW rules; auto adds allow-all + main substitution loosening; yolo additionally auto-runs $()/backtick/heredoc in children. An unknown value fails closed to strict")
	fs.StringVar(&f.reasoningEffort, "reasoning-effort", "", "OPERATOR REASONING-EFFORT TIER (ADR 0055): auto (default — unset, the provider default applies) or low/medium/high/xhigh/max. OpenAI supports low/medium/high only (xhigh/max clamp to high); Anthropic maps all five. Empty = unset (honours the operator-global settings.yaml reasoning-effort: key). Operator-tier only; a project-tier key is ignored with a WARN. An unknown value fail-softs to unset with a WARN")
	fs.BoolVar(&f.trustProject, "trust-project", false, "trust the workspace for this run: admit BOTH project steering (AGENTS.md/CLAUDE.md, project rules/agents/skills/soul/commands/git snapshot) and the read-only child worktree shell. On this HEADLESS root posture never grants trust. DEFAULT OFF: without explicit, declared, or remembered trust a cloned repo gets neither steering nor child shell. TRUST BOUNDARY: only pass it for a repo whose content and .git you trust")

	// Headless telemetry (issue #343, ADR 0098): OPT-IN OTLP trace + metrics push.
	// Both endpoints empty (the default) leaves the pipeline off — no metrics, no
	// tracing, byte-identical to the pre-telemetry posture. A metrics endpoint
	// installs a PeriodicReader (push) alongside the always-on prometheus reader.
	fs.StringVar(&f.otlpEndpoint, "otlp-endpoint", "", "OTLP trace collector endpoint (empty disables tracing). e.g. \"localhost:4317\" for gRPC or a host for HTTP. OPT-IN: mecatequi PUSHES a single run's spans here")
	fs.StringVar(&f.otlpProtocol, "otlp-protocol", "grpc", "OTLP transport for traces: \"grpc\" (default) or \"http\"")
	fs.BoolVar(&f.otlpInsecure, "otlp-insecure", false, "skip TLS when dialing the OTLP collector (development only)")
	fs.StringVar(&f.otlpMetricsEndpoint, "otlp-metrics-endpoint", "", "OTLP METRICS collector endpoint (empty disables metrics push). A single-shot run is too short for a Prometheus scrape, so mecatequi PUSHES the run's counters/histograms here and flushes before exit. OPT-IN")
	fs.StringVar(&f.otlpMetricsProtocol, "otlp-metrics-protocol", "grpc", "OTLP transport for metrics: \"grpc\" (default) or \"http\"")
	fs.DurationVar(&f.otlpShutdownTimeout, "otlp-shutdown-timeout", 5*time.Second, "bound on the telemetry flush at exit (so a dead collector cannot hang the run). 0 disables the bound (flush until it completes); the flush runs BEFORE the diff/summary emit defer unwinds")

	fs.Usage = usageEpilogue(fs)

	if err := fs.Parse(argv); err != nil {
		return flags{}, err
	}

	// Post-parse MCP finalize (issue #358): resolve the --mcp-server-insecure-http
	// relaxations against the collected --mcp-server entries and run the deferred
	// token-bearing scheme gate. Deferring it here (instead of inside Set) is what
	// makes the opt-in order-independent on argv.
	if err := f.mcpServers.Finalize(); err != nil {
		return flags{}, err
	}

	// Record whether --posture was set EXPLICITLY (vs the empty default) so
	// composition lets CLI out-rank the operator-global settings.yaml posture: key.
	fs.Visit(func(fl *flag.Flag) {
		switch fl.Name {
		case "posture":
			f.postureFlagSet = true
		case "subagent-model-router":
			// Tri-state kill-switch (ADR 0042): record that the flag was given so
			// appConfig can distinguish unset / =false (kill-switch) / =true (inert).
			f.subagentModelRouterSet = true
		case "out-summary":
			// Stdout-compact summary mode (issue #341): ONLY an EXPLICIT
			// --out-summary=- selects it. The unset default also resolves to "-"
			// but keeps the indented JSON — default behavior unchanged.
			f.summaryCompact = f.outSummary == "-"
		}
		if fl.Name == "reasoning-effort" {
			f.reasoningEffortFlagSet = true
		}
		if fl.Name == "default-provider" {
			f.defaultProviderFlagSet = true
		}
	})

	// Provider credentials are resolved once after the remaining input checks and
	// cached on flags for I/O-free projection by appConfig, mirroring mecated/mecatui.

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

	f.providerCredentials = f.providerFlags.Resolve()
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

// appConfig constructs the complete declarative app.Config for the command root.
// app.Build loads the injected provider credential after resolving operator definitions.
func appConfig(f flags, diag port.Diagnostics, obs observability) app.Config {
	out := app.Config{
		Workspace:       f.workspace,
		Model:           f.model,
		DefaultProvider: f.defaultProvider,
		DefaultModel:    f.defaultModel,
		// defaultProviderFlagSet lets CLI out-rank the operator-global settings.yaml
		// models.default_provider: key (folded by foldOperatorDefaultProvider in app.Build).
		DefaultProviderFlagSet: f.defaultProviderFlagSet,
		UseOpenAI:              f.useOpenAI,
		UseMock:                f.useMock,
		StoreDir:               f.storeDir,
		Shell:                  f.shell,
		NoBash:                 f.noBash,
		MaxRunTokens:           f.maxRunTokens,
		MaxTeamTokens:          f.maxTeamTokens,
		// Remote MCP servers (issue #341): the static name=URL entries (with any
		// MCP_<NAME>_TOKEN bearer already resolved into Headers at parse time),
		// consumed by app.Build's static MCP source. Nil-safe when the flag was
		// never registered (a hand-built test config).
		MCPServers:               f.mcpServers.Servers(),
		MCPProfileLoader:         cliconfig.NewMCPProfileResolver(f.mcpServers, os.LookupEnv),
		ProviderCredentialLoader: cliconfig.NewProviderCredentialResolver(f.providerFlags, f.providerCredentials),
		ProviderOverrides:        f.providerFlags.EndpointOverrides(),
		PermissionsConventional:  true,
		PermissionConfigs:        f.permissionConfigs,

		GuardrailsModel:    f.guardrailsModel,
		GuardrailsDisabled: f.guardrailsOff,

		SubagentAskReviewerModel: f.subagentAskReviewer,
		// Subagent model router (ADR 0042): kill-switch. =false forces the router OFF
		// (RouterDisabled); a bare flag / =true is a harmless no-op (the router stays
		// governed by the taxonomy); unset leaves routing governed by the taxonomy.
		RouterDisabled:               f.subagentModelRouterSet && !f.subagentModelRouter,
		SubagentAskReviewerMaxDenies: f.subagentAskReviewerMaxDenies,
		SubagentAskReviewerPolicy:    f.subagentAskReviewerPolicy,

		Posture:        app.ParsePosture(f.posture),
		PostureFlagSet: f.postureFlagSet,
		// Explicit workspace trust: on this HEADLESS root the posture ladder never
		// raises TrustProject, so --trust-project is the one-shot opt-in that admits
		// both project steering and the read-only worktree shell.
		TrustProject: f.trustProject,
		// Reasoning-effort tier (ADR 0055): operator-tier only; reasoningEffortFlagSet
		// lets CLI out-rank the operator-global settings.yaml reasoning-effort: key.
		ReasoningEffort:        f.reasoningEffort,
		ReasoningEffortFlagSet: f.reasoningEffortFlagSet,
		Privileged:             privilegedProcess(),

		// Headless is explicit deployment identity. DEFAULT true; the posture
		// ladder never raises workspace trust on a headless root.
		Headless: f.headless,

		// Interactive = !headless: the deliberate inversion. mecatequi defaults
		// headless=true (no approver), so a child's unresolved ask is auto-denied /
		// routed to the opt-in ask-reviewer rather than parked until run-end.
		Interactive: !f.headless,

		Diagnostics: diag,
		// Observability (issue #343, ADR 0098): OPT-IN OTLP push. With no --otlp-*
		// flags the handles are zero-valued (nil Sink/ToolCallRecorder/
		// MetricsRoleScoper) — the byte-identical no-telemetry posture.
		Sink:              obs.Sink,
		ToolCallRecorder:  obs.ToolCallRecorder,
		MetricsRoleScoper: obs.MetricsRoleScoper,
	}
	keys := f.providerCredentials
	f.providerFlags.ApplyResolved(&out, keys)
	if keys.OpenAI != "" {
		out.UseOpenAI = true
	}
	f.toolhiveLLMFlags.Apply(&out)
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
