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
	"github.com/stacklok/mecatl/internal/adapter/mcpauthority"
	"github.com/stacklok/mecatl/internal/adapter/slogdiag"
	"github.com/stacklok/mecatl/internal/app"
	"github.com/stacklok/mecatl/internal/cliconfig"
	"github.com/stacklok/mecatl/internal/flaghelp"
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
	// untrustedPrompt fences the prompt body via governance.FenceUntrusted
	// (cmd-side only). Default false: a normal CI prompt is the operator's own
	// trusted task.
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
	// scheduler injects a short-lived per-run identity there), token
	// optional. Threaded onto app.Config.MCPServers in appConfig.
	mcpServers        *cliconfig.MCPServerList
	permissionConfigs stringList
	useMock           bool
	storeDir          string
	shell             string
	shellFlagSet      bool
	noShell           bool
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

	// productMetrics reports anonymous product-adoption metrics to Stacklok.
	// OPT-OUT: ON by default. See the --product-metrics flag help text.
	productMetrics bool
	// productMetricsSet records whether --product-metrics was explicitly passed,
	// so ResolveProductMetricsEnabled can let CLI out-rank DO_NOT_TRACK/settings.
	productMetricsSet bool
	// productMetricsDryRun logs every would-be product-metrics observation
	// via diag instead of exporting it over OTLP — an audit mode to verify
	// the no-PII claim before trusting --product-metrics for real.
	productMetricsDryRun bool
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
	configureFlags(fs, &f)

	if err := fs.Parse(cliconfig.NormalizeLegacyNoBash(argv)); err != nil {
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
		case "shell":
			f.shellFlagSet = true
		case "subagent-model-router":
			// Tri-state kill-switch (ADR 0042): record that the flag was given so
			// appConfig can distinguish unset / =false (kill-switch) / =true (inert).
			f.subagentModelRouterSet = true
		case "out-summary":
			// Stdout-compact summary mode (issue #341): ONLY an EXPLICIT
			// --out-summary=- selects it. The unset default also resolves to "-"
			// but keeps the indented JSON — default behavior unchanged.
			f.summaryCompact = f.outSummary == "-"
		case "product-metrics":
			f.productMetricsSet = true
		}
		if fl.Name == "reasoning-effort" {
			f.reasoningEffortFlagSet = true
		}
		if fl.Name == "default-provider" {
			f.defaultProviderFlagSet = true
		}
	})
	resolvedShell, err := cliconfig.ResolveCommandRunnerConfig(f.shell, f.shellFlagSet, true, f.permissionConfigs)
	if err != nil {
		return flags{}, fmt.Errorf("command runner configuration: %w", err)
	}
	f.shell = resolvedShell

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

// configureFlags registers mecatequi's flags on fs. Keeping registration separate
// lets help-output tests exercise the same flag descriptions that users see.
func configureFlags(fs *flag.FlagSet, f *flags) {

	cwd, _ := os.Getwd()

	fs.StringVar(&f.prompt, "prompt", "", "Task text. Provide --prompt, --prompt-file, or both; --prompt is placed first.")
	fs.StringVar(&f.promptFile, "prompt-file", "", "File containing task text. Provide --prompt, --prompt-file, or both.")
	fs.BoolVar(&f.untrustedPrompt, "untrusted-prompt", false, "Treat task text as untrusted external data and fence it before the model receives it. Default: false.")
	fs.StringVar(&f.instructions, "instructions", "", "Trusted operator instructions prepended to the task. With --untrusted-prompt, they remain outside the data fence. Default: empty.")

	fs.StringVar(&f.outSummary, "out-summary", "-", "Destination for run-summary JSON. \"-\" writes to stdout and is the default. An explicit --out-summary=- writes one compact JSON line as the final stdout line; the default stdout summary is indented.")
	fs.StringVar(&f.outDiff, "out-diff", "", "Destination for the working-tree Git diff. \"-\" writes to stdout. Default: empty, which disables diff output. Choose a sink that differs from the summary and event outputs.")
	fs.StringVar(&f.outEvents, "out-events", "", "Destination for the durable JSONL event log. Each line is a redacted session event. Default: empty, which disables event output. Choose a sink that differs from the summary and diff outputs.")
	fs.DurationVar(&f.timeout, "timeout", 0, "Maximum wall-clock duration for the run, for example 5m. A timeout cancels the run and exits 1. Default: 0, no timeout.")

	fs.StringVar(&f.workspace, "workspace", cwd, "Git repository root used for the run and generated diff. Default: the current directory.")
	fs.StringVar(&f.model, "model", "", "Model identifier for this run. Empty uses the provider default. Accepts any model identifier supported by the provider.")
	fs.StringVar(&f.defaultProvider, "default-provider", "", "Default provider identifier, for example openai, openrouter, or anthropic. Validated at startup.")
	fs.StringVar(&f.defaultModel, "default-model", "", "Default model identifier for the default provider. It must be in the embedded model catalog. Use --model for an identifier outside that catalog.")
	fs.BoolVar(&f.useOpenAI, "openai", false, "Use the OpenAI Responses provider. Reads OPENAI_API_KEY.")
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
	fs.Var(&f.permissionConfigs, "permission-config", "Operator settings YAML file. Repeat the flag for multiple files. Uses standard settings precedence and strict validation.")
	fs.BoolVar(&f.useMock, "mock", false, "Use the offline mock provider. It does not make network requests.")
	fs.StringVar(&f.storeDir, "store-dir", "", "Directory for the JSONL session store. Default: in-memory store.")
	fs.StringVar(&f.shell, "shell", "/bin/sh", "Shell for Shell tool commands. An empty value disables the Shell tool.")
	fs.BoolVar(&f.noShell, "no-shell", false, "Disable the Shell tool. Overrides --shell.")
	fs.IntVar(&f.maxRunTokens, "max-run-tokens", 0, "Maximum cumulative input and output tokens for the run. Crossing the limit ends with stop_reason=budget. Default: 0, unlimited.")
	fs.IntVar(&f.maxTeamTokens, "max-team-tokens", 0, "Maximum cumulative input and output tokens for an agent team run. Default: 0, unlimited.")
	fs.IntVar(&f.maxTurns, "max-turns", 0, "Maximum model calls for the run. Crossing the limit ends with stop_reason=max_turns. Default: 0, use the deployment default.")

	fs.BoolVar(&f.headless, "headless", true, "Run without a human approver. Unresolved child subagent, team member, and branch permission requests are denied or sent to --subagent-ask-reviewer. Default: true. Set false only when an external approver can respond.")

	fs.StringVar(&f.guardrailsModel, "guardrails-model", "", "Model identifier or alias for the tool-free guardrails checker. Setting this flag enables guardrails unless --guardrails=off. A guardrail model slot takes precedence. Default: empty.")
	fs.StringVar(&f.guardrailsMode, "guardrails", "", "Set to off to disable guardrails, including configured checker models. Other values leave guardrails controlled by the configured checker model.")

	fs.StringVar(&f.subagentAskReviewer, "subagent-ask-reviewer", "", "Model identifier or alias for the tool-free reviewer of child permission requests in headless runs. Default: empty, which disables the reviewer.")
	fs.BoolVar(&f.subagentModelRouter, "subagent-model-router", false, "Set false to disable configured subagent model routing. A configured models.router taxonomy enables routing; this flag does not enable it.")
	fs.IntVar(&f.subagentAskReviewerMaxDenies, "subagent-ask-reviewer-max-denies", agent.DefaultAskReviewMaxDenies, "Consecutive non-allow reviewer outcomes before the reviewer is disabled for the rest of the run. Values less than or equal to 0 use the default: 3.")
	fs.StringVar(&f.subagentAskReviewerPolicyFile, "subagent-ask-reviewer-policy", "", "Trusted policy rubric file for --subagent-ask-reviewer. Its contents replace the built-in rubric. An unreadable file fails startup.")

	fs.StringVar(&f.posture, "posture", "", "Permission posture: strict (default), trusted, auto, or yolo. In a headless strict run, a main-agent permission request cancels the run with exit 1. trusted honors project allow rules; auto allows tools by default and relaxes main command substitutions while retaining child injection defenses; yolo also automatically runs $(), backticks, and here-documents in children. Deny rules and configured ask rules still apply. Unknown values use strict.")
	fs.StringVar(&f.reasoningEffort, "reasoning-effort", "", "Reasoning effort: auto, low, medium, high, xhigh, or max. Empty uses the provider or operator setting. OpenAI maps xhigh and max to high. Unknown values use the provider or operator setting.")
	fs.BoolVar(&f.trustProject, "trust-project", false, "Allow workspace content to provide project instructions, rules, agents, skills, souls, commands, Git snapshots, and the read-only child worktree shell. Default: false. Enable only for a repository and Git metadata you trust.")

	// Headless telemetry (issue #343, ADR 0098): OPT-IN OTLP trace + metrics push.
	// Both endpoints empty (the default) leaves the pipeline off — no metrics, no
	// tracing, byte-identical to the pre-telemetry posture. A metrics endpoint
	// installs a PeriodicReader (push) alongside the always-on prometheus reader.
	fs.StringVar(&f.otlpEndpoint, "otlp-endpoint", "", "OTLP trace collector endpoint. Empty disables trace export. For example, localhost:4317 for gRPC.")
	fs.StringVar(&f.otlpProtocol, "otlp-protocol", "grpc", "OTLP trace transport: grpc (default) or http.")
	fs.BoolVar(&f.otlpInsecure, "otlp-insecure", false, "Disable TLS for the OTLP collector connection. Use only for development.")
	fs.StringVar(&f.otlpMetricsEndpoint, "otlp-metrics-endpoint", "", "OTLP metrics collector endpoint. Empty disables metrics export. Metrics are flushed before exit.")
	fs.StringVar(&f.otlpMetricsProtocol, "otlp-metrics-protocol", "grpc", "OTLP metrics transport: grpc (default) or http.")
	fs.DurationVar(&f.otlpShutdownTimeout, "otlp-shutdown-timeout", 5*time.Second, "Maximum telemetry flush duration at exit. Default: 5s. Set to 0 to wait until flushing completes.")

	fs.BoolVar(&f.productMetrics, "product-metrics", true,
		"Report anonymous product-adoption metrics to Stacklok: version, OS and architecture, enabled features, and coarse session, run, and tool-call counts. Excludes prompts, file paths, tool names, and model identifiers. Default: true. Disable with --product-metrics=false, MECATL_PRODUCT_METRICS=false, DO_NOT_TRACK=1, or telemetry.productMetrics.enabled: false in settings.yaml.")
	fs.BoolVar(&f.productMetricsDryRun, "product-metrics-dry-run", false,
		"Write each product-metrics observation to stderr instead of sending it.")

	fs.Usage = usageEpilogue(fs)
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

// usageEpilogue returns a fs.Usage func that adds output-routing and exit-status
// reference to the default flag listing. It writes to the FlagSet's configured output.
func usageEpilogue(fs *flag.FlagSet) func() {
	return func() {
		out := fs.Output()
		_, _ = fmt.Fprintf(out, "mecatequi: single-shot, headless Mecatl runner for CI and batch use.\n\n")
		_, _ = fmt.Fprintf(out, "Usage: mecatequi --prompt <text> [flags]\n\n")
		_, _ = fmt.Fprintf(out, "Flags:\n")
		flaghelp.PrintDefaults(out, fs)
		_, _ = fmt.Fprintln(out, "\nVersion: mecatequi --version prints the build version and exits.")
		_, _ = fmt.Fprintf(out, `
Output routing:
  --out-summary writes JSON to stdout by default. --out-diff and --out-events are
  disabled by default. Each enabled output must use a distinct sink. Operational
  diagnostics, the event trace, and the final verdict are written to stderr. An
  explicit --out-summary=- writes one compact JSON line last; the default is indented.

Exit status:
  0  A clean terminal: end_turn, no_progress, budget, max_turns, max_tool_calls,
     max_consecutive_failures, structured_output, plan_approved, or plan_iterate.
     Exit 0 does not confirm that the requested task was accomplished. Read
     stop_reason and inspect non_empty_diff and the emitted artifacts.
  1  Run failure, cancellation, or an absent or unknown terminal, including a timeout
     or an unanswered main-agent permission request in a headless strict run.
  2  Setup or output failure, including invalid flags, a missing prompt, a build
     failure, an invalid workspace, colliding output sinks, or a write failure.
`)
	}
}

// appConfig constructs the complete declarative app.Config for the command root.
// app.Build loads the injected provider credential after resolving operator definitions.
func appConfig(f flags, diag port.Diagnostics, obs observability) app.Config {
	nativeEndpointLoader := &cliconfig.NativeEndpointLoader{}
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
		NoShell:                f.noShell,
		MaxRunTokens:           f.maxRunTokens,
		MaxTeamTokens:          f.maxTeamTokens,
		// Remote MCP servers (issue #341): the static name=URL entries (with any
		// MCP_<NAME>_TOKEN bearer already resolved into Headers at parse time),
		// consumed by app.Build's static MCP source. Nil-safe when the flag was
		// never registered (a hand-built test config).
		MCPServers:                        f.mcpServers.Servers(),
		MCPProfileLoader:                  cliconfig.NewMCPProfileResolver(f.mcpServers, os.LookupEnv),
		MCPAuthorityLoader:                cliconfig.NewMCPProfileResolver(f.mcpServers, os.LookupEnv),
		MCPAuthorityDefault:               mcpauthority.Global,
		MCPBrokerSupported:                false,
		ProviderCredentialLoader:          cliconfig.NewProviderCredentialResolver(f.providerFlags, f.providerCredentials),
		NativeEndpointCredentialLoader:    nativeEndpointLoader,
		NativeEndpointCredentialLifecycle: nativeEndpointLoader,
		ProviderOverrides:                 f.providerFlags.EndpointOverrides(),
		PermissionsConventional:           true,
		PermissionConfigs:                 f.permissionConfigs,

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
		// MetricsRoleScoper) — the byte-identical no-telemetry posture. The
		// opt-out product-metrics Sink/ToolCallRecorder are folded in alongside
		// (nil-guarded fan-out): both nil reproduces the byte-identical
		// no-telemetry posture exactly.
		Sink:                             productMetricsSink(obs),
		ToolCallRecorder:                 productMetricsRecorder(obs),
		MetricsRoleScoper:                obs.MetricsRoleScoper,
		SessionLoadFailureMetricsEmitter: obs.SessionLoadFailureMetricsEmitter,
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
