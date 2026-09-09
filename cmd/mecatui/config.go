package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/internal/app"
	"github.com/stacklok/mecatl/internal/cliconfig"
)

// config is the resolved CLI/env configuration for mecatui.
type config struct {
	// transportMode is the resolved canonical transport mode (local/connect)
	// threaded explicitly from resolveInvocation through parse and validate.
	// It drives the transport path (no-probe/no-embed) and the
	// trust/provider/posture validation gating (ADR 0087).
	transportMode transportMode
	// connectAddress is the dial target for `mecatui connect ADDRESS` ("" for the
	// bare/local mode). Set by resolveInvocation; consumed by resolveTransport.
	connectAddress string
	// browseSessions selects the startup session-browser launch intent. Transport
	// remains independent: both embedded and connect modes can browse first.
	browseSessions bool
	// debugTarget binds a dedicated no-filesystem analysis session to one stored
	// target. It comes only from the command grammar, never from a flag.
	debugTarget string
	// debugMCP selects configured server-global MCP servers by name for a debug session.
	debugMCP []string
	// helpAll is true when --help-all was passed; it requests the exhaustive
	// flag listing and exits 0 before transport resolution.
	helpAll   bool
	helpFlags bool
	keymap    *cliconfig.KeyValueList
	workspace string
	// workspaceExplicit distinguishes an operator-supplied --workspace from the
	// empty default. Remote connect rejects the former without resolving it.
	workspaceExplicit bool
	mode              string
	theme             string
	themeDir          string
	authToken         string
	anonymous         bool
	useTLS            bool
	tlsExplicit       bool
	tlsCA             string
	insecure          bool
	listThemes        bool
	// debug enables mecatui's client-side diagnostic surfaces. An explicit
	// --debug value outranks MECATUI_DEBUG and the legacy per-surface aliases.
	debug        bool
	debugFlagSet bool
	debugMouse   bool
	debugSteer   bool
	debugAsk     bool
	debugKeymap  bool

	// noAltScreen renders mecatui INLINE in the terminal's normal buffer instead
	// of the alternate screen. Off by default (full-screen TUI on the alt screen);
	// the first-class opt-out for users who want the session streamed into native
	// scrollback so it stays searchable/scrollable after exit. Wired to
	// ui.Deps.NoAltScreen.
	noAltScreen bool

	// noBanner disables the rich first-run welcome SPLASH (mascot + gradient
	// wordmark): the zero-state then shows the plain card (title + prompt hint +
	// affordances). Off by default (full splash). main also forces it on under
	// --quiet or a non-interactive stdin. Wired to ui.Deps.NoBanner.
	noBanner bool

	// noMouse disables mouse capture on the alt screen, so the terminal's OWN
	// click-drag selection works again — at the cost of in-app mouse-wheel scroll
	// and the in-app drag-select/copy layer (keyboard scroll stays). The escape
	// hatch for terminals/multiplexers (tmux, zellij, some web terminals) that
	// strip OSC52 AND where the user prefers native selection. Off by default
	// (mouse captured: wheel scroll + in-app selection). Honoured from --no-mouse
	// or MECATUI_NO_MOUSE=1. Inert under --no-alt-screen (mouse is already off
	// inline). Wired to ui.Deps.NoMouse.
	noMouse bool

	// terminalTitleOff suppresses the dynamic terminal window/tab title (leaving
	// it at the bare "mecatui"). Off by default (the title is dynamic: "<title> —
	// <status word> mecatui"). Honoured from --terminal-title=off/false/0 or
	// MECATUI_NO_TERMINAL_TITLE=1/true. The escape hatch for terminals/
	// multiplexers where a set title does more harm than good. Wired to
	// ui.Deps.NoWindowTitle.
	terminalTitle        string
	terminalTitleOff     bool
	terminalTitleFlagSet bool

	// Embedded-server provider config (used only when no external server is
	// dialled). The OpenAI key is read from OPENAI_API_KEY; --mock selects the
	// canned offline provider instead (useful for a no-network smoke run).
	// subagentModel is the def-less child-default model (--subagent-model),
	// mapped onto app.Config.SubagentModel exactly like mecated's flag.
	// defaultProvider/defaultModel are the server-configured deployment-wide
	// default (--default-provider/--default-model), mapped onto
	// app.Config.DefaultProvider/DefaultModel exactly like mecated's flags.
	// defaultProviderFlagSet records an explicit --default-provider so CLI out-ranks
	// the operator-global settings.yaml models.default_provider: key.
	model                  string
	subagentModel          string
	defaultProvider        string
	defaultModel           string
	defaultProviderFlagSet bool
	// modelAliases / modelSlots mirror the mecated flags for the embedded server
	// (ADR 0030): modelAliases maps a short alias to a concrete id; modelSlots binds
	// an internal lightweight call (compaction/guardrail; the ask-reviewer slot is
	// inert here — mecatui runs interactive, so the headless child-ask reviewer never
	// engages) or a tier (cheap/fast/reasoning) to a selector resolved THROUGH
	// modelAliases. INERT when dialling an external server. The *cliconfig.KeyValueList
	// pointers are the flag bindings returned by cliconfig.RegisterModelFlags (issue
	// #93: the type lives in cliconfig so the two mains cannot drift).
	modelAliases *cliconfig.KeyValueList
	modelSlots   *cliconfig.KeyValueList
	// Subagent model router (ADR 0031; enable model per ADR 0042, embedded server
	// only): the router is ENABLED by an operator-tier models.router: taxonomy in the
	// user-global settings.yaml (the guardrails-parity enable model). The
	// --subagent-model-router flag is a KILL-SWITCH: subagentModelRouter holds its value
	// and subagentModelRouterSet records whether it was given. =false sets
	// app.Config.RouterDisabled (forces the router OFF despite a taxonomy); a bare flag /
	// =true is a harmless no-op (the router stays governed by the taxonomy); unset leaves
	// routing governed by taxonomy presence. The router IS meaningful under mecatui —
	// it picks the child's model before it runs, in both interactive and headless modes.
	subagentModelRouter    bool
	subagentModelRouterSet bool
	// providerFlags holds the shared provider base-URL flags (cliconfig), applied onto
	// app.Config when the embedded server is built. The resolved credential keys below
	// are read via cliconfig too (one definition of the env-var names across the mains).
	providerFlags *cliconfig.ProviderFlags
	// toolhiveLLMFlags holds --toolhive-llm / --toolhive-llm-base-url (issue
	// #262), applied onto app.Config alongside providerFlags when the
	// embedded server is built.
	toolhiveLLMFlags *cliconfig.ToolhiveLLMFlags
	// providerKeys is resolved once at parse time. Keeping the result on the config
	// lets startup validation see auth.yaml without reading it again when the
	// embedded server is assembled.
	providerKeys         cliconfig.ResolvedKeys
	providerKeysResolved bool
	openAIKey            string
	openRouterKey        string
	anthropicKey         string
	openCodeKey          string
	mock                 bool
	noBash               bool
	// noSteer disables the mid-run steer inbox (steer-while-running, issue #512) on
	// the embedded server — the opt-OUT of a DEFAULT-ON knob. noSteerFlagSet records
	// an explicit --no-steer so CLI out-ranks the settings.yaml steer: key.
	noSteer        bool
	noSteerFlagSet bool

	// productMetrics reports anonymous product-adoption metrics to Stacklok
	// for the embedded server only (ignored under `mecatui connect`, which
	// hosts no local engine). OPT-OUT: ON by default. See the
	// --product-metrics flag help text. productMetricsFlagSet records an
	// explicit --product-metrics so CLI out-ranks the operator-global
	// settings.yaml telemetry.productMetrics.enabled: key (mirrors
	// noSteerFlagSet).
	productMetrics        bool
	productMetricsFlagSet bool

	// resumeID and resumeLatest select an existing owned main chat for static
	// startup adoption. They are shared by embedded and connect modes and mutually
	// exclusive; the first prompt still owns all run-entry attachment/revalidation.
	resumeID     string
	resumeLatest bool

	// prompt is the literal seed-prompt text supplied via -p/--prompt.
	// Empty = no seed. Joined ahead of --prompt-file when both are given.
	prompt string
	// promptFile is the path supplied via --prompt-file. The file body is read
	// at parse time into promptFileBody; this field holds the raw flag value.
	promptFile string
	// promptFileBody is the content of --prompt-file read at parse time (fail-fast
	// on unreadable). Joined after the --prompt literal.
	promptFileBody string

	// Embedded-server LLM resilience timeouts (used only when hosting an
	// in-process server; ignored under `mecatui connect`). They mirror
	// mecated's --llm-per-attempt-timeout / --llm-stream-idle-timeout and are
	// mapped onto app.Config.LLMPerAttemptTimeout / app.Config.LLMStreamIdleTimeout
	// in main.go. llmPerAttemptTimeout bounds ESTABLISHMENT (connect + first chunk)
	// only — it never cuts an actively-streaming turn; llmStreamIdleTimeout bounds
	// the idle gap between chunks after the first.
	llmPerAttemptTimeout time.Duration
	llmStreamIdleTimeout time.Duration
	// contextWindowOverride mirrors mecated's embedded-server-only escape hatch.
	contextWindowOverride int

	// Provider-side prompt caching (ADR 0100), embedded server only. Mirrors
	// mecated's --no-prompt-cache / --anthropic-cache-ttl, mapped onto
	// app.Config.PromptCacheDisabled / app.Config.AnthropicCacheTTL in main.go.
	noPromptCache     bool
	anthropicCacheTTL string

	// trustProject controls whether a discovered PROJECT's permission ALLOW rules
	// and its project-scoped soul (.mecatl/soul.md) are honoured for the EMBEDDED
	// server only (ignored under `mecatui connect`). DEFAULT FALSE — the
	// safe stance, unified with mecated's --trust-project. A project's deny/ask rules
	// are ALWAYS honoured regardless; only its ALLOW grants and project soul are
	// gated. Pass --trust-project for a repo you trust. Mapped onto
	// app.Config.TrustProject in embeddedConfig.
	trustProject bool

	// allowAllTools is the operator allow-all posture for the EMBEDDED server only
	// (ignored under `mecatui connect`). When set it injects a single
	// ScopeCLI allow-all rule that suppresses the built-in mutate-ask floor; a Deny
	// in any scope and any deliberately configured Ask still apply. Refused as root
	// outside a declared sandbox (see validate). See docs/adr/0022-allow-all-posture.md.
	allowAllTools bool

	// posture is the graduated operator posture ladder for the EMBEDDED server only
	// (strict < trusted < auto < yolo). --posture sets it; --yolo and --trust-project
	// are ALIASES composition folds MAX-tier. Empty = unset (composition default
	// PostureStrict unless an alias/settings.yaml raises it). postureFlagSet records an
	// explicit --posture so CLI out-ranks the operator-global settings.yaml posture:
	// key. Mapped onto app.Config.Posture/PostureFlagSet in embeddedConfig.
	posture        string
	postureFlagSet bool
	// reasoningEffort is the operator-tier reasoning-effort default (ADR 0055) for
	// the EMBEDDED server. reasoningEffortFlagSet records an explicit
	// --reasoning-effort so CLI out-ranks the operator-global settings.yaml
	// reasoning-effort: key. Mapped onto app.Config.ReasoningEffort/
	// ReasoningEffortFlagSet in embeddedConfig.
	reasoningEffort        string
	reasoningEffortFlagSet bool

	// quiet routes the embedded server's operational diagnostics (and the perf
	// surface's startup/teardown lines) to io.Discard instead of the per-user state
	// log file. Default OFF: diagnostics land in $XDG_STATE_HOME/mecatl/mecatui.log
	// (never stderr — stderr corrupts the Bubble Tea alt-screen). --quiet drops them
	// entirely for an operator who wants zero on-disk diagnostics.
	quiet bool

	// diagnosticsLog overrides the embedded server's diagnostics log path. Empty
	// (the default) means the shared per-user $XDG_STATE_HOME/mecatl/mecatui.log;
	// a non-empty value is the exact file to open (created mode 0600, parent dir
	// 0700). Used to give a specific mecatui instance its own diagnostics file
	// (e.g. ad-hoc multi-instance testing) instead of sharing the per-user log.
	diagnosticsLog string

	// Embedded-server memory config (used only when hosting an in-process
	// server). An empty memoryDir means "compute the per-project default under
	// $XDG_DATA_HOME/mecatui/memory"; an explicit path overrides it. noMemory
	// disables cross-session memory (Remember/Recall) entirely and wins over
	// both (the resolved MemoryDir becomes ""). Precedence is applied in
	// embeddedConfig (resolveMemoryDir), not here.
	memoryDir string
	noMemory  bool

	// Embedded-server session-store config (issue #79; used only when hosting an
	// in-process server). The durable JSONL session/event store. ON by default at
	// a per-workspace dir under $XDG_STATE_HOME/mecatui/sessions (see
	// resolveStoreDir/defaultStoreDir), so a session survives restart and can be
	// inspected after the fact. PRIVACY: the store holds the RAW conversation
	// (prompts, model output, tool args/results) in PLAINTEXT on disk; the dir is
	// created mode 0700 (owner-only). storeDir overrides the path; noStore opts
	// out entirely and wins (the engine then falls back to the in-memory store and
	// nothing persists). Precedence is applied in embeddedConfig (resolveStoreDir),
	// not here.
	storeDir string
	noStore  bool

	// Embedded-only automatic retention policy. Connected mode rejects these flags
	// because only an advertised remote management API can configure the server.
	childRetention, mainRetention, scheduledRetention                time.Duration
	childRetentionCount, mainRetentionCount, scheduledRetentionCount int
	retentionSweepCadence                                            time.Duration
	retentionCLISet                                                  app.RetentionCLISet
	acknowledgeMainRetention                                         bool

	// Embedded-server soul config (issue #14, Phase 1; used only when hosting an
	// in-process server). A user-scoped, agent-READ-ONLY persona fragment injected
	// as turn-0 context. ON by default reading the conventional
	// $XDG_CONFIG_HOME/mecatl/soul.md (fallback ~/.config/mecatl/soul.md) — a
	// missing file is fail-soft, so it costs nothing. soulFile overrides the path;
	// noSoul disables it entirely and wins (the resolved SoulPath/NoSoul map onto
	// app.Config in embeddedConfig). No tool can write the soul.
	//
	// Drift baseline (issue #14, Phase 3, Item 1): the harness records the soul's
	// content hash in a sidecar (<soulPath>.sha256) trust-on-first-use; a later run
	// whose hash differs logs a drift WARN and still loads. approveSoul (re)writes the
	// baseline to the current hash (accept the edit); soulStrict makes a DRIFTED soul
	// contribute no fragment.
	soulFile    string
	noSoul      bool
	approveSoul bool
	soulStrict  bool

	// Embedded-server user-model config (issue #14, Phase 2; used only when hosting
	// an in-process server). A user-scoped, CROSS-PROJECT memory of durable FACTS
	// about the operator (explicit user-memory tools plus a live bounded operator
	// profile in the volatile system suffix). ON by default at the conventional
	// $XDG_CONFIG_HOME/mecatl/usermodel (fallback ~/.config/mecatl/usermodel).
	// userModelDir overrides the dir; noUserModel disables it. userModelReview
	// enables the OPT-IN (off by default) Stop-triggered background reviewer;
	// userModelReviewInterval is its session-count debounce. Map onto app.Config in
	// embeddedConfig. The user model holds FACTS about the operator, never rules.
	userModelDir            string
	noUserModel             bool
	userModelReview         bool
	userModelReviewInterval int

	// Embedded-server slash-command config (used only when hosting an in-process
	// server). Command expansion is ON by default, expanding "/<name>" inputs from
	// the conventional workspace dirs (.mecatl/commands, .claude/commands). An
	// explicit commandsDir overrides the directory; noCommands disables expansion
	// entirely and wins. Precedence is applied in embeddedConfig (resolveCommands),
	// not here.
	commandsDir string
	noCommands  bool

	// Embedded-server skills config (used only when hosting an in-process server).
	// Conventional skill discovery is ON by default: the progressive-disclosure
	// Skill tool activates SKILL.md units from the conventional dirs (e.g.
	// .claude/skills) when present — consistent with AgentsConventional. An explicit
	// skillsDir overrides with a single vetted directory; noSkills disables skill
	// discovery entirely and wins. Only read-only discovery is wired here, never the
	// writable SkillDraft quarantine. Precedence is applied in embeddedConfig
	// (resolveSkills), not here.
	skillsDir string
	noSkills  bool

	// Embedded-server perf observability (decision 7 in
	// docs/adr/0018-perf-observability.md; used only when hosting an in-process
	// server). OFF by default. perf arms the loopback runtime-introspection admin
	// surface (pprof/expvar/RSS/goroutines/flightrecorder + /metrics) plus the
	// domain-metrics EventSink. Empty perfAddr uses a private UNIX socket, except
	// perfMCP uses ephemeral loopback TCP for its streaming-HTTP transport.
	// perfGoroutineWarnThreshold arms the live goroutine-leak watchdog (0 = off).
	perf                       bool
	perfAddr                   string
	perfGoroutineWarnThreshold int
	// perfMCP mounts the read-only perf MCP server at /mcp on the embedded admin
	// surface (only meaningful with --perf). The admin listener is loopback by
	// construction; embed FAILS CLOSED if --perf-addr is non-loopback with this set.
	perfMCP bool
}

// parseFlags is the bare-mode test seam: it parses argv (excluding the program
// name) as a bare (embedded/local) invocation. Existing tests that exercise the
// flag-parsing logic (not the mode-specific transport/help behaviour) use this
// entry point. Production goes through parseTransportFlags via
// resolveInvocation.
func parseFlags(args []string) (config, error) {
	_, cfg, err := parseTransportFlags(modeLocal, os.Stderr, args)
	return cfg, err
}

// parseTransportFlags is parseFlags with an explicit transport mode and an
// injected output writer, selecting which help renderer the --help hook invokes
// and which applicability policy governs explicit flags. It returns the built
// *flag.FlagSet alongside the config so progressive-help tests can run the
// validateFlagApplicability completeness invariant over the FULL real FlagSet
// (every flag parseTransportFlags registers) instead of a synthetic subset. It
// is the small test seam: tests capture the REAL Usage / --help / --help-all
// render output into a strings.Builder and inspect the FlagSet, without copying
// the registration block. Production calls it with os.Stderr and discards the
// returned FlagSet. mode is the resolved canonical transport mode; out is where
// --help / parse errors are written; args excludes the program name (and, for
// local/connect, the command word / ADDRESS — resolveInvocation strips them).
func parseTransportFlags(mode transportMode, out io.Writer, args []string, browseSessions ...bool) (*flag.FlagSet, config, error) {
	var cfg config
	cfg.transportMode = mode
	cfg.browseSessions = len(browseSessions) > 0 && browseSessions[0]
	fs := flag.NewFlagSet("mecatui", flag.ContinueOnError)
	fs.SetOutput(out)
	fs.StringVar(&cfg.workspace, "workspace", "", "embedded server only: absolute deployment workspace root (default: cwd); not accepted by connect")
	fs.StringVar(&cfg.mode, "mode", "default", "permission mode: default | plan | accept-edits")
	fs.Func("debug-mcp", "debug sessions only: select one already-configured server-global streaming-HTTP MCP server by name (repeatable)", func(value string) error {
		cfg.debugMCP = append(cfg.debugMCP, value)
		return nil
	})
	fs.StringVar(&cfg.resumeID, "resume", "", "start by continuing the owned main chat with this exact opaque session ID; loads its authoritative transcript without creating a throwaway session (mutually exclusive with --resume-latest)")
	fs.BoolVar(&cfg.resumeLatest, "resume-latest", false, "start by continuing the newest eligible owned main chat with an available authoritative transcript; excludes active, awaiting, scheduled, child, and unknown sessions (mutually exclusive with --resume); when none exists, start a new chat instead of failing")
	fs.StringVar(&cfg.prompt, "prompt", "", "seed prompt auto-submitted once the first session is ready (the CLI task to launch with). The TUI stays interactive for follow-ups; this is NOT a one-shot. Both --prompt and --prompt-file may be given (literal first)")
	fs.StringVar(&cfg.prompt, "p", "", "short form of --prompt")
	fs.StringVar(&cfg.promptFile, "prompt-file", "", "path to a file whose contents are the seed prompt body. Read at startup (fail-fast on unreadable). Joined after --prompt when both are given")
	fs.StringVar(&cfg.theme, "theme", "", "theme name (default: aztec)")
	fs.StringVar(&cfg.themeDir, "theme-dir", "", "extra directory of *.json themes to load")
	fs.StringVar(&cfg.authToken, "auth-token", "", "bearer token for an external server (or MECATL_AUTH_TOKEN)")
	fs.BoolVar(&cfg.anonymous, "anonymous", false, "bypass saved OIDC enrollment and send no bearer unless --auth-token or MECATL_AUTH_TOKEN supplies one")
	fs.BoolVar(&cfg.useTLS, "tls", false, "use verified TLS for an external server (default for non-loopback targets; --tls=false explicitly permits plaintext)")
	fs.StringVar(&cfg.tlsCA, "tls-ca", "", "path to a PEM CA bundle for external-server verification")
	fs.BoolVar(&cfg.insecure, "insecure", false, "skip TLS verification (testing only)")
	fs.BoolVar(&cfg.listThemes, "list-themes", false, "list available themes and exit")
	fs.BoolVar(&cfg.debug, "debug", false, "enable client-side diagnostic surfaces: mouse mapping, steer correlation, and debug-only built-ins")
	fs.BoolVar(&cfg.noAltScreen, "no-alt-screen", false, "render inline in the terminal's normal buffer instead of the alternate screen, preserving native scrollback/search")
	fs.BoolVar(&cfg.noAltScreen, "inline", false, "alias for --no-alt-screen: render inline in the normal buffer, preserving native scrollback/search")
	fs.BoolVar(&cfg.noMouse, "no-mouse", false, "disable mouse capture on the alt screen so the terminal's NATIVE click-drag selection works (for tmux/zellij/web terminals that strip OSC52, or when you prefer native select); trades away in-app mouse-wheel scroll and the in-app drag-select/copy layer. Keyboard scroll (pgup/pgdn/home/end) is unaffected. Or set MECATUI_NO_MOUSE=1")
	fs.BoolVar(&cfg.noBanner, "no-banner", false, "disable the welcome splash (mascot + gradient wordmark); the plain prompt hint and affordance list are still shown. Also forced on under --quiet or a non-interactive stdin")
	fs.StringVar(&cfg.terminalTitle, "terminal-title", "on", "dynamic terminal window/tab title: on (default — shows \"<session title> — <status word> mecatui\") or off (bare \"mecatui\", the escape hatch for terminals/multiplexers where a set title does more harm than good). Accepts on/off/true/false/1/0. Or set MECATUI_NO_TERMINAL_TITLE=1")

	// Keymap overrides: action=chords (comma-separated), repeatable.
	cfg.keymap = new(cliconfig.KeyValueList)
	fs.Var(cfg.keymap, "keymap", "rebind a key: Action=chord[,chord2] (repeatable). Actions: Agents, ScrollU, ScrollD, ScrollTop, ScrollBottom, ModeSwitch, MCPPanel, Resources, Prompts, Up, Down, Choose, Close, Refresh, Tasks, Findings, JumpTop, JumpEnd, NextTab, CancelChild, ExpandTools, Help, Effort, Submit, Newline, Cancel, EditBack, Paste, Quit, Allow, AllowAlways, Deny, SetGlobalDefault, RawArgs")

	fs.StringVar(&cfg.model, "model", "", "model identifier for the embedded server (empty: use the provider-appropriate default; ignored when dialling an external server)")
	fs.StringVar(&cfg.defaultProvider, "default-provider", "", "embedded server only: deployment-wide default provider id shared by every client (e.g. openai, openrouter, anthropic); overrides the built-in provider preference for zero-selector sessions while a client-side selection still wins. Validated FAIL-FAST at startup: an unknown or unavailable provider refuses to start")
	fs.StringVar(&cfg.defaultModel, "default-model", "", "embedded server only: deployment-wide default model id for the default provider; sits BELOW client-side defaults and ABOVE the per-provider built-in default. Validated FAIL-FAST at startup: a model not catalogued for the default provider refuses to start")
	fs.StringVar(&cfg.subagentModel, "subagent-model", "", "embedded server only: global default model for every Subagent / Parallel-branch / team-member child that does not pin its own model (the analogue of CLAUDE_CODE_SUBAGENT_MODEL); the Parallel judge stays on the session model. Same provider as the session. Empty inherits --model; a value that does not resolve to a usable model id FAILS STARTUP. settings.yaml home: `models.subagent:` (operator-tier); this flag wins when both are set")
	// Shared model alias/slot flags (cliconfig); mecatui keeps its own help wording.
	cfg.modelAliases, cfg.modelSlots = cliconfig.RegisterModelFlags(fs, cliconfig.ModelFlagHelp{
		ModelAlias: "embedded server only: model alias mapping as name=model-id (repeatable), e.g. --model-alias cheap=gpt-4o-mini. Aliases are resolved in the composition layer; a --model-slot selector and an agent def's `model: <alias>` resolve through this map",
		ModelSlot:  "embedded server only: per-slot model binding as slot=selector (repeatable), e.g. --model-slot compaction=cheap (ADR 0030). A SLOT routes an internal lightweight LLM call to its own model: under mecatui the wired slots are `compaction` (the compaction summary call), `guardrail` (the content checker), and `title` (automatic session-title generation). `title` is explicit opt-in and has NO default-tier or session-model fallback: without a compatible binding it makes no title-model call. The `ask-reviewer` slot is INERT here (mecatui runs INTERACTIVE, so the headless child-ask reviewer never engages — that slot only routes on a headless `mecated --headless`). A TIER key (`cheap`/`fast`/`reasoning`) gives a default a slot falls through to (each routed slot except `title` defaults to `cheap`). The selector is an alias (--model-alias / built-ins) or a concrete id. Empty keeps every call on the session model. FAIL-SOFT on a typo/inherit. Operator-tier only",
	})
	fs.BoolVar(&cfg.subagentModelRouter, "subagent-model-router", false, "embedded server only: Semantic model router KILL-SWITCH (ADR 0042, superseding 0031's enable model): the router is ENABLED by an operator-tier models.router: category taxonomy in the user-global settings.yaml (configure = enable, guardrails-parity), NOT by this flag. Pass --subagent-model-router=false to force it OFF despite a taxonomy (also models.router.disabled: true in YAML). When enabled, a tiny classifier on the `router` slot picks the child model per plain Subagent delegation before the child is minted (decide-once, same-provider); fail-soft to the inherited model on any miss. The router IS meaningful under mecatui — it picks a child's model before the child runs, in both interactive and headless modes")
	// Shared provider base-URL flags (cliconfig); mecatui keeps its own help wording.
	cfg.providerFlags = cliconfig.RegisterProviderFlags(fs, cliconfig.ProviderFlagHelp{
		OpenAIBaseURL:     "override the OpenAI API base URL for the embedded server (compatible endpoints)",
		OpenRouterBaseURL: "embedded server only: override the OpenRouter API base URL (default https://openrouter.ai/api/v1; key from OPENROUTER_API_KEY)",
		AnthropicBaseURL:  "embedded server only: override the native Anthropic API base URL (compatible/proxy endpoints; key from ANTHROPIC_API_KEY)",
		OpenCodeBaseURL:   "embedded server only: override the OpenCode Go API base URL (default https://opencode.ai/zen/go/v1; key from OPENCODE_API_KEY)",
	})
	// ToolHive LLM gateway (issue #262): embedded-server-only, like every other
	// provider knob on this main.
	cfg.toolhiveLLMFlags = cliconfig.RegisterToolhiveLLMFlags(fs, cliconfig.ToolhiveLLMFlagHelp{
		Enable:  "embedded server only: " + cliconfig.DefaultToolhiveLLMFlagHelp.Enable,
		BaseURL: "embedded server only: " + cliconfig.DefaultToolhiveLLMFlagHelp.BaseURL,
		Mode:    "embedded server only: " + cliconfig.DefaultToolhiveLLMFlagHelp.Mode,
	})
	fs.BoolVar(&cfg.mock, "mock", false, "embedded server only: use the canned offline mock provider instead of OpenAI (no network)")
	fs.BoolVar(&cfg.noBash, "no-bash", false, "embedded server only: disable the Bash tool (shell-less mode)")
	fs.BoolVar(&cfg.noSteer, "no-steer", false, "embedded server only: disable the mid-run steer inbox (steer-while-running, issue #512): `enter` mid-run then falls back to the client-side terminal merge-queue and ServerCapabilities.steer reads false. Steer is ON by default; this is the opt-OUT. The operator-tier settings.yaml `steer: false` scalar is the YAML twin (CLI out-ranks YAML; a project-tier steer: key is ignored)")
	fs.DurationVar(&cfg.llmPerAttemptTimeout, "llm-per-attempt-timeout", 300*time.Second, "embedded server only: per-attempt timeout for ESTABLISHING an LLM stream (connect + first chunk only; never cuts an actively-streaming turn). 0 disables; large-context reasoning models can take a long time to first token")
	fs.DurationVar(&cfg.llmStreamIdleTimeout, "llm-stream-idle-timeout", 180*time.Second, "embedded server only: max idle gap between LLM stream chunks after the first chunk; a longer stall terminates the turn (0 disables)")
	fs.IntVar(&cfg.contextWindowOverride, "context-window-override", 0, "embedded server only: override the model's context window in tokens for BOTH the compaction trigger (compaction fires at 80% of it) AND the footer context-meter denominator echoed to clients. Set this to the model's ACTUAL window when a model under-reports its window or sits behind a proxy that does. 0 (default) keeps the configured/live/catalogued/128k resolution unchanged. A small value (below a few thousand tokens) forces the agent to compact on nearly every turn — degraded, only useful for stress-testing compaction.")
	fs.BoolVar(&cfg.noPromptCache, "no-prompt-cache", false, "embedded server only: disable provider-side prompt caching (ADR 0100): every adapter's cache dialect degrades to None, reproducing the pre-caching wire exactly. Caching is ON by default")
	fs.StringVar(&cfg.anthropicCacheTTL, "anthropic-cache-ttl", "", "embedded server only: TTL stamped on every Anthropic ephemeral cache_control breakpoint: \"5m\" or \"1h\". Empty (default) omits the ttl field — the API's own 5m default applies. Any other value is ignored with a WARN")
	fs.BoolVar(&cfg.trustProject, "trust-project", false, "embedded server only: honour a discovered PROJECT's ALLOW rules AND its project-scoped soul (.mecatl/soul.md) (its deny/ask rules are always honoured regardless). Default OFF (the safe stance, unified with mecated): an untrusted repo's permission grants and project soul are ignored. TRUST BOUNDARY: enabling this lets a checked-in .mecatl/settings.yaml auto-approve tool calls and a checked-in project soul steer the model — only pass it for a repo you trust")
	fs.BoolVar(&cfg.allowAllTools, "yolo", false,
		"embedded server only; ALIAS for --posture yolo (dangerous): allow-all AND loosen the CHILD substitution floor (a subagent's $()/backtick/heredoc AUTO-RUNS — prompt-injection defense OFF). Deny in any scope and configured Ask still apply. Isolated/single-tenant ONLY. Refused as root unless MECATL_SANDBOX=1 (or IS_SANDBOX=1).")
	fs.StringVar(&cfg.posture, "posture", "",
		"embedded server only: OPERATOR POSTURE LADDER (strict < trusted < auto < yolo): strict (default) prompts every mutate; trusted = --trust-project; auto adds allow-all + main substitution loosening (child injection-defense ON); yolo additionally auto-runs $()/backtick/heredoc in CHILDREN (injection-defense OFF). --yolo/--trust-project are aliases. auto/yolo refused as root outside MECATL_SANDBOX. Unknown value fails closed to strict.")
	fs.StringVar(&cfg.reasoningEffort, "reasoning-effort", "",
		"embedded server only: OPERATOR REASONING-EFFORT TIER (ADR 0055): auto (default — unset, the provider default applies) or low/medium/high/xhigh/max. OpenAI supports low/medium/high only (xhigh/max clamp to high); Anthropic maps all five. Empty = unset (honours the operator-global settings.yaml reasoning-effort: key). A per-session /effort out-ranks it. Operator-tier only; a project-tier key is ignored with a WARN. An unknown value fail-softs to unset with a WARN.")
	fs.BoolVar(&cfg.quiet, "quiet", false,
		"discard the embedded server's operational diagnostics instead of writing them to $XDG_STATE_HOME/mecatl/mecatui.log (fallback ~/.local/state/mecatl/mecatui.log). Diagnostics NEVER go to stderr (that corrupts the TUI alt-screen); --quiet drops them entirely")
	fs.StringVar(&cfg.diagnosticsLog, "diagnostics-log", "",
		"embedded server only: override the diagnostics log file. Empty (default) writes to the shared per-user $XDG_STATE_HOME/mecatl/mecatui.log; a non-empty value is the exact file to open (created mode 0600, parent dir 0700). Use to give one mecatui instance its own diagnostics file (e.g. multi-instance testing) instead of sharing the per-user log. Ignored under --quiet")
	fs.StringVar(&cfg.memoryDir, "memory-dir", "", "embedded server only: per-project memory store directory (empty = a per-project default under $XDG_DATA_HOME/mecatui/memory)")
	fs.BoolVar(&cfg.noMemory, "no-memory", false, "embedded server only: disable cross-session memory (Remember/Recall) entirely")
	fs.StringVar(&cfg.storeDir, "store-dir", "", "embedded server only: durable JSONL session/event store directory (empty = a per-workspace default under $XDG_STATE_HOME/mecatui/sessions, so sessions survive restart and can be inspected after the fact). PRIVACY: stores the RAW conversation (prompts, model output, tool args/results) in PLAINTEXT; the dir is created mode 0700 (owner-only). Tool args/results include file contents and command output the agent read, so secrets it touched (e.g. a .env it opened) are persisted too")
	fs.BoolVar(&cfg.noStore, "no-store", false, "embedded server only: disable the durable session store (use an in-memory store instead, so nothing is persisted to disk)")
	fs.DurationVar(&cfg.childRetention, "child-retention", 168*time.Hour, "embedded server only: child-session maximum age; 0 disables the age limit")
	fs.IntVar(&cfg.childRetentionCount, "child-retention-max-per-family", 500, "embedded server only: child-session count cap per family; 0 disables the cap")
	fs.DurationVar(&cfg.mainRetention, "main-retention", 0, "embedded server only: main-session maximum age; 0 disables destructive main age cleanup")
	fs.IntVar(&cfg.mainRetentionCount, "main-retention-max-total", 0, "embedded server only: main-session store-wide count cap; 0 disables destructive main count cleanup")
	fs.DurationVar(&cfg.scheduledRetention, "schedule-fire-retention", 7*24*time.Hour, "embedded server only: scheduled-fire session maximum age; 0 disables the age limit")
	fs.IntVar(&cfg.scheduledRetentionCount, "schedule-fire-retention-max-total", 0, "embedded server only: scheduled-fire session count cap; 0 disables the cap")
	fs.DurationVar(&cfg.retentionSweepCadence, "retention-sweep-cadence", time.Hour, "embedded server only: automatic retention sweep cadence; 0 disables repeat cadence (startup sweep remains for flag compatibility)")
	fs.BoolVar(&cfg.acknowledgeMainRetention, "acknowledge-main-retention", false, "embedded server only: explicitly acknowledge destructive main-session cleanup after reviewing the policy summary")
	fs.StringVar(&cfg.soulFile, "soul-file", "", "embedded server only: path to a user-scoped, agent-READ-ONLY persona/\"soul\" file injected as turn-0 context (empty = the conventional $XDG_CONFIG_HOME/mecatl/soul.md, fallback ~/.config/mecatl/soul.md; fail-soft if absent)")
	fs.BoolVar(&cfg.noSoul, "no-soul", false, "embedded server only: disable the user-scoped persona/soul fragment entirely")
	fs.BoolVar(&cfg.approveSoul, "approve-soul", false, "embedded server only: (re)write the soul DRIFT BASELINE to the current soul's content hash, accepting the file as-is. The baseline is a harness-owned sidecar next to the soul (<soul-path>.sha256); a later run whose hash differs logs a drift WARN")
	fs.BoolVar(&cfg.soulStrict, "soul-strict", false, "embedded server only: refuse a DRIFTED soul — if its content hash differs from the recorded baseline, contribute NO soul fragment this run (instead of the default warn-and-load). Pair with --approve-soul to accept an edit")
	fs.StringVar(&cfg.userModelDir, "user-model-dir", "", "embedded server only: directory for the user-scoped, CROSS-PROJECT user-model store of durable FACTS about the operator (empty = the conventional $XDG_CONFIG_HOME/mecatl/usermodel, fallback ~/.config/mecatl/usermodel). Exposes explicit user-memory lifecycle tools and a live bounded operator profile")
	fs.BoolVar(&cfg.noUserModel, "no-user-model", false, "embedded server only: disable the user model entirely (explicit tools and live operator profile)")
	fs.BoolVar(&cfg.userModelReview, "user-model-review", false, "embedded server only: DEPRECATED compatibility alias for operator settings learning.mode: auto (one release window); conflicts with an explicit non-auto mode. Eligible completions use the bounded staged-reflection coordinator; no direct reviewer writes")
	fs.IntVar(&cfg.userModelReviewInterval, "user-model-review-interval", 1, "embedded server only: process-wide completion debounce for learning.mode: auto and the deprecated --user-model-review alias (1 = every eligible completion)")
	fs.StringVar(&cfg.commandsDir, "commands-dir", "", "embedded server only: directory of slash-command templates (<name>.md); empty = the conventional dirs (.mecatl/commands, .claude/commands)")
	fs.BoolVar(&cfg.noCommands, "no-commands", false, "embedded server only: disable slash-command expansion entirely")
	fs.StringVar(&cfg.skillsDir, "skills-dir", "", "embedded server only: directory of skill units (<name>/SKILL.md); empty = the conventional dirs (e.g. .claude/skills)")
	fs.BoolVar(&cfg.noSkills, "no-skills", false, "embedded server only: disable skill discovery (the Skill tool) entirely")
	fs.BoolVar(&cfg.productMetrics, "product-metrics", true,
		"report anonymous product-adoption metrics to Stacklok (version, OS/arch, enabled features, coarse session/run/tool-call counts — never a prompt, file path, tool name, or model id). ON by default; opt out with --product-metrics=false, DO_NOT_TRACK=1, or telemetry.productMetrics.enabled: false in settings.yaml")

	fs.BoolVar(&cfg.perf, "perf", false, "embedded server only: expose the private perf-observability admin surface (/metrics, /debug/pprof, /debug/vars, /debug/flightrecorder) and wire domain metrics into the engine. OFF by default. Empty --perf-addr uses a per-instance UNIX socket. SECURITY: UNAUTHENTICATED — its output can embed prompt text/file paths/goroutine stacks")
	fs.StringVar(&cfg.perfAddr, "perf-addr", "", "embedded server only: explicit loopback host:port for the --perf admin surface (empty = private per-instance UNIX socket, or ephemeral 127.0.0.1 TCP with --perf-mcp). Use 127.0.0.1:0 for explicit ephemeral TCP. Non-loopback addresses are refused. Only consulted with --perf")
	fs.IntVar(&cfg.perfGoroutineWarnThreshold, "perf-goroutine-warn-threshold", 0, "embedded server only: arm the live goroutine-leak watchdog — log a Warn whenever runtime.NumGoroutine() exceeds this count (decision 10). 0 (default) disables the alarm; the /metrics goroutine-count series is exported regardless. Only consulted with --perf")
	fs.BoolVar(&cfg.perfMCP, "perf-mcp", false, "embedded server only: mount the read-only streaming-HTTP perf MCP server at /mcp. With empty --perf-addr this selects ephemeral 127.0.0.1 TCP and logs the resolved URL; stdio is never used. Only meaningful with --perf. SECURITY: loopback-bound, UNAUTHENTICATED")
	fs.BoolVar(&cfg.helpAll, "help-all", false, "print the exhaustive flag reference for this command and exit")
	fs.BoolVar(&cfg.helpFlags, "help-flags", false, "print the common embedded-mode flag reference and exit (bare invocation only)")

	fs.Usage = transportUsage(fs, mode, cfg.browseSessions)

	if err := fs.Parse(args); err != nil {
		// Return the fully-registered FlagSet even on a parse/help error so the
		// progressive-help completeness invariant (validateFlagApplicability) can
		// run over the full real registration path via the --help-triggered ErrHelp
		// path.
		return fs, config{}, err
	}

	// --help-flags is the bare/local common flag reference.
	if cfg.helpFlags {
		if mode != modeLocal || cfg.browseSessions {
			return fs, config{}, errors.New("--help-flags is available only as bare 'mecatui --help-flags'")
		}
		writeBareCommonHelp(fs.Output(), fs)
		return nil, config{}, flag.ErrHelp
	}

	// --help-all was parsed as a normal flag; render and return ErrHelp (exit 0).
	if cfg.helpAll {
		out := fs.Output()
		if cfg.browseSessions {
			writeSessionsHelpAll(out, fs, mode)
		} else {
			switch mode {
			case modeConnect:
				writeConnectHelpAll(out, fs)
			default:
				writeBareHelpAll(out, fs)
			}
		}
		return nil, config{}, flag.ErrHelp
	}

	// By-name applicability rejection (ADR 0087): connect rejects embedded-only
	// flags; the bare/local mode rejects remote-only flags.
	if err := rejectInapplicableFlags(fs, mode); err != nil {
		return fs, config{}, err
	}
	if fs.NArg() != 0 {
		return fs, config{}, fmt.Errorf("unexpected operand %q", fs.Arg(0))
	}

	if err := finalizeParsedConfig(fs, &cfg); err != nil {
		return fs, config{}, err
	}
	if err := validateResumeSelectors(cfg); err != nil {
		return fs, config{}, err
	}
	if err := validateSessionsLaunch(cfg); err != nil {
		return fs, config{}, err
	}
	if cfg.promptFile != "" {
		body, err := os.ReadFile(cfg.promptFile)
		if err != nil {
			return fs, config{}, fmt.Errorf("reading --prompt-file %q: %w", cfg.promptFile, err)
		}
		cfg.promptFileBody = string(body)
	}
	return fs, cfg, nil
}

// resolveRemoteTLSPolicy applies the connect transport policy only after the
// command grammar has supplied its target. tlsExplicit preserves the distinction
// between an omitted --tls and an explicit --tls=false. It classifies targets
// with client.IsLocalTarget, the SAME predicate client.Dial gates its plaintext
// guards on, so the policy layer can never default a target to TLS that the
// transport layer would then dial in plaintext (or vice versa) — notably a
// "unix://" socket, which is local but not a loopback host:port.
func resolveRemoteTLSPolicy(cfg *config) error {
	if cfg.transportMode != modeConnect {
		return nil
	}
	if cfg.tlsCA != "" && cfg.insecure {
		return errors.New("--tls-ca and --insecure are mutually exclusive")
	}
	if cfg.tlsExplicit && !cfg.useTLS {
		if cfg.tlsCA != "" || cfg.insecure {
			return errors.New("--tls=false conflicts with --tls-ca or --insecure")
		}
		if cfg.authToken != "" && !client.IsLocalTarget(cfg.connectAddress) {
			return errors.New("refusing static bearer over explicit plaintext to non-loopback target; remove --tls=false")
		}
		return nil
	}
	if cfg.tlsExplicit || cfg.tlsCA != "" || cfg.insecure || !client.IsLocalTarget(cfg.connectAddress) {
		cfg.useTLS = true
	}
	return nil
}

// applySavedRemoteTLSPolicy gives managed OIDC credentials their stronger
// transport guarantee. TLSCAFile deliberately remains untouched: an issuer CA
// is not gRPC server trust.
func applySavedRemoteTLSPolicy(cfg config, dial *client.DialConfig) error {
	if (cfg.tlsExplicit && !cfg.useTLS) || cfg.insecure {
		return errors.New("saved remote authentication requires verified TLS; remove --tls=false and --insecure")
	}
	dial.UseTLS = true
	dial.Insecure = false
	dial.RemotePlaintextAllowed = false
	return nil
}

// validateResumeSelectors enforces that at most ONE startup resume intent is chosen:
// --resume and --resume-latest are mutually exclusive. It is shared by the
// parse-time check and the client-side validate() so both surfaces agree.
func validateResumeSelectors(cfg config) error {
	n := 0
	if cfg.resumeID != "" {
		n++
	}
	if cfg.resumeLatest {
		n++
	}
	if n > 1 {
		return errors.New("--resume and --resume-latest are mutually exclusive")
	}
	return nil
}

func validateLaunchSelectors(cfg config) error {
	if err := validateResumeSelectors(cfg); err != nil {
		return err
	}
	if cfg.debugTarget == "" && len(cfg.debugMCP) > 0 {
		return errors.New("--debug-mcp is allowed only with the debug command")
	}
	return nil
}

func validateSessionsLaunch(cfg config) error {
	if !cfg.browseSessions {
		return nil
	}
	switch {
	case cfg.prompt != "":
		return errors.New("mecatui sessions conflicts with -p/--prompt")
	case cfg.promptFile != "":
		return errors.New("mecatui sessions conflicts with --prompt-file")
	case cfg.resumeID != "":
		return errors.New("mecatui sessions conflicts with --resume")
	case cfg.resumeLatest:
		return errors.New("mecatui sessions conflicts with --resume-latest")
	default:
		return nil
	}
}

// finalizeParsedConfig applies the post-parse env fallbacks, records which flags
// were set explicitly (so composition can let CLI out-rank operator-YAML keys),
// validates --terminal-title, reads provider credentials, and resolves the
// workspace to an absolute path. It is extracted from parseTransportFlags to
// keep parseTransportFlags' cyclomatic complexity under the lint gate; the
// helper owns the post-parse branches.
// recordExplicitFlag records ONE explicitly-passed flag's "set" marker onto cfg, so
// composition lets CLI out-rank the operator-global settings.yaml keys (mirrors
// mecated's recordExplicitFlags). Split out of finalizeParsedConfig's fs.Visit to keep
// that function under the cyclomatic-complexity bound.
func recordExplicitFlag(f *flag.Flag, cfg *config) {
	switch f.Name {
	case "tls":
		cfg.tlsExplicit = true
	case "debug":
		cfg.debugFlagSet = true
	case "posture":
		cfg.postureFlagSet = true
	case "subagent-model-router":
		// Kill-switch (ADR 0042): record that the flag was given so embeddedConfig can
		// distinguish unset (router governed by the taxonomy) from =false (kill-switch)
		// and =true/bare (a harmless no-op, the router stays governed by the taxonomy).
		cfg.subagentModelRouterSet = true
	case "no-steer":
		// Record an explicit --no-steer so CLI out-ranks the settings.yaml steer: key.
		cfg.noSteerFlagSet = true
	case "product-metrics":
		// Record an explicit --product-metrics so CLI out-ranks the settings.yaml
		// telemetry.productMetrics.enabled: key (ResolveProductMetricsEnabled's
		// highest-precedence input).
		cfg.productMetricsFlagSet = true
	case "reasoning-effort":
		cfg.reasoningEffortFlagSet = true
	case "default-provider":
		cfg.defaultProviderFlagSet = true
	case "terminal-title":
		cfg.terminalTitleFlagSet = true
	case "workspace":
		cfg.workspaceExplicit = true
	}
	markRetentionCLIFlag(&cfg.retentionCLISet, f.Name)
}

// resolveDebugConfig applies the canonical debug switch and its legacy env aliases.
func resolveDebugConfig(cfg *config) {
	// Explicit --debug=false suppresses all env fallbacks; without an explicit flag,
	// the legacy variables remain narrow aliases for their original surfaces.
	if !cfg.debugFlagSet {
		cfg.debug = os.Getenv("MECATUI_DEBUG") == "1"
		cfg.debugMouse = cfg.debug || os.Getenv("MECATUI_DEBUG_MOUSE") != ""
		cfg.debugSteer = cfg.debug || os.Getenv("MECATUI_DEBUG_STEER") != ""
		cfg.debugAsk = cfg.debug || os.Getenv("MECATUI_DEBUG_ASK") != ""
		cfg.debugKeymap = cfg.debug || os.Getenv("MECATUI_DEBUG_KEYMAP") == "1"
		return
	}
	cfg.debugMouse = cfg.debug
	cfg.debugSteer = cfg.debug
	cfg.debugAsk = cfg.debug
	cfg.debugKeymap = cfg.debug
}

func finalizeParsedConfig(fs *flag.FlagSet, cfg *config) error {
	// Record explicit flags so composition lets CLI out-rank the operator-global
	// settings.yaml keys (mirrors mecated). Extracted to recordExplicitFlag to keep
	// this function under the cyclomatic-complexity bound.
	fs.Visit(func(f *flag.Flag) { recordExplicitFlag(f, cfg) })

	if cfg.authToken == "" {
		cfg.authToken = os.Getenv("MECATL_AUTH_TOKEN")
	}
	if cfg.authToken != "" {
		// Static bearer credentials are the highest-priority credential source.
		// --anonymous only overrides saved OIDC state when no static token was
		// supplied explicitly or through MECATL_AUTH_TOKEN.
		cfg.anonymous = false
	}
	if cfg.theme == "" {
		cfg.theme = os.Getenv("MECATUI_THEME")
	}
	resolveDebugConfig(cfg)
	// Env fallback: --no-mouse wins if passed; otherwise MECATUI_NO_MOUSE=1/true
	// enables it (set-and-forget in a shell rc for a multiplexer that strips OSC52).
	if !cfg.noMouse {
		switch os.Getenv("MECATUI_NO_MOUSE") {
		case "1", "true":
			cfg.noMouse = true
		}
	}
	// Validate --terminal-title and resolve it onto terminalTitleOff. Accepted
	// values: on/true/1/"" → on (the default); off/false/0 → off; anything else
	// fails fast (unlike posture's fail-soft behavior, a title toggle is binary, so
	// an unknown value is a genuine config error, not a soft-degrade case).
	switch cfg.terminalTitle {
	case "on", "true", "1", "":
		cfg.terminalTitleOff = false
	case "off", "false", "0":
		cfg.terminalTitleOff = true
	default:
		return fmt.Errorf("invalid --terminal-title %q (want on|off|true|false|1|0)", cfg.terminalTitle)
	}
	// Env fallback: --terminal-title wins if passed; otherwise
	// MECATUI_NO_TERMINAL_TITLE=1/true turns the dynamic title off (set-and-forget
	// in a shell rc for a terminal/multiplexer where a set title misbehaves).
	if !cfg.terminalTitleFlagSet && !cfg.terminalTitleOff {
		switch os.Getenv("MECATUI_NO_TERMINAL_TITLE") {
		case "1", "true":
			cfg.terminalTitleOff = true
		}
	}
	// Provider credentials are not needed for connect or --list-themes. Avoiding
	// Resolve there also avoids touching the conventional auth file on paths that
	// cannot host an embedded provider. Embedded runs retain one resolution result.
	var keys cliconfig.ResolvedKeys
	if cfg.transportMode != modeConnect && !cfg.listThemes {
		keys = cfg.providerFlags.Resolve()
		if warning := conventionalAuthFileWarning(*cfg, keys); warning != "" {
			keys.AuthFileWarning = warning
		}
	}
	cfg.providerKeys = keys
	cfg.providerKeysResolved = true
	cfg.openAIKey = keys.OpenAI
	cfg.openRouterKey = keys.OpenRouter
	cfg.anthropicKey = keys.Anthropic
	cfg.openCodeKey = keys.OpenCode

	if !cfg.listThemes && cfg.transportMode != modeConnect {
		ws, err := resolveWorkspace(cfg.workspace)
		if err != nil {
			return err
		}
		cfg.workspace = ws
	}
	return nil
}

// conventionalAuthFileWarning is deliberately a mecatui policy, not part of the
// shared credential resolver. Other command mains must not turn an ordinary absent
// conventional file into a warning, and mecatui must not warn when another usable
// startup path (mock or ToolHive) exists.
func conventionalAuthFileWarning(c config, keys cliconfig.ResolvedKeys) string {
	if !c.mayEmbed() || c.listThemes || c.mock || keys.Any() {
		return ""
	}
	var probe app.Config
	c.toolhiveLLMFlags.Apply(&probe)
	if app.ToolhiveAvailable(probe) {
		return ""
	}
	path, explicit := c.providerFlags.AuthFilePath()
	if explicit || path == "" {
		return ""
	}
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return fmt.Sprintf("auth file %s: file not found and no provider credentials were resolved", path)
	}
	return ""
}

// wrapAuthFileWarning keeps startup diagnostics readable without changing the
// value-free warning returned by the auth-file adapter. The rendered prefix and
// continuation indentation are included in the width budget.
func wrapAuthFileWarning(warning string) string {
	const wrapWidth = 80
	wrapped := ansi.Wrap(warning, wrapWidth, "")
	lines := strings.Split(wrapped, "\n")
	for i := 1; i < len(lines); i++ {
		lines[i] = "  " + lines[i]
	}
	return strings.Join(lines, "\n")
}

// transportUsage returns the fs.Usage closure for the resolved transport mode:
// the bare form prints the bare-mode common help; `connect` prints its
// mode-specific common help. Callers that want to assert the banner is the real
// one (not a dead copy) wire this helper rather than duplicating the closure.
func transportUsage(fs *flag.FlagSet, mode transportMode, browseSessions ...bool) func() {
	return func() {
		out := fs.Output()
		if len(browseSessions) > 0 && browseSessions[0] {
			writeSessionsCommonHelp(out, fs, mode)
			return
		}
		switch mode {
		case modeConnect:
			writeConnectCommonHelp(out, fs)
		default:
			writeBareCommonHelp(out, fs)
		}
	}
}

// configureWorkspaceForTransport applies the workspace authority rule after the
// connect target is known. A remote client never resolves its cwd: the empty wire
// field asks the server to select its authoritative root. An explicit path is
// rejected before a dial or CreateSession call. Embedded and loopback workflows
// retain the local cwd/worktree default.
func configureWorkspaceForTransport(cfg *config) error {
	if cfg.transportMode == modeConnect {
		if cfg.workspaceExplicit {
			return errors.New("--workspace configures only the embedded server and is not allowed with connect")
		}
		cfg.workspace = ""
		return nil
	}
	ws, err := resolveWorkspace(cfg.workspace)
	if err != nil {
		return err
	}
	cfg.workspace = ws
	return nil
}

// resolveWorkspace defaults an empty workspace to the cwd and makes it absolute.
func resolveWorkspace(ws string) (string, error) {
	if ws == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("resolve cwd as workspace: %w", err)
		}
		return cwd, nil
	}
	if !filepath.IsAbs(ws) {
		abs, err := filepath.Abs(ws)
		if err != nil {
			return "", fmt.Errorf("resolve workspace %q: %w", ws, err)
		}
		return abs, nil
	}
	return ws, nil
}

// validate checks invariants the server also enforces, failing fast client-side.
func (c config) validate() error {
	if c.debugTarget != "" && c.listThemes {
		return errors.New("debug conflicts with --list-themes")
	}
	if c.listThemes {
		return nil
	}
	if err := validateLaunchSelectors(c); err != nil {
		return err
	}
	if c.debugTarget != "" {
		switch {
		case c.resumeID != "" || c.resumeLatest:
			return errors.New("debug conflicts with resume launch actions")
		case c.browseSessions:
			return errors.New("debug conflicts with sessions launch")
		}
	}
	if c.workspace == "" && c.debugTarget == "" && c.transportMode != modeConnect {
		return errors.New("workspace is required")
	}
	if c.workspace != "" && !filepath.IsAbs(c.workspace) {
		return fmt.Errorf("workspace must be absolute: %q", c.workspace)
	}
	switch c.mode {
	case "default", "plan", "accept-edits":
	default:
		return fmt.Errorf("invalid --mode %q (want default|plan|accept-edits)", c.mode)
	}
	// Provider/posture checks apply ONLY to paths that may embed (ADR 0087 Phase
	// 1); the predicate + its rationale live once on config.mayEmbed.
	if c.mayEmbed() {
		if err := validateEmbeddedProvider(c); err != nil {
			return err
		}
		// Operator posture: refuse an allow-all tier (auto or yolo) when running
		// privileged outside a declared sandbox. The tier is the AUTHORITATIVE one
		// (incl. the operator-global settings.yaml posture: key), so a YAML-only
		// allow-all tier cannot escape the refusal — and app.Build re-checks it as
		// the fail-closed backstop.
		if err := app.PostureRefusalReason(embeddedAuthoritativePosture(c), embeddedPrivileged()); err != nil {
			return err
		}
	}
	return nil
}

// validateEmbeddedProvider ensures an embedded server has a provider path before the TUI
// takes over the terminal. It mirrors app.Build's provider availability rules.
func validateEmbeddedProvider(c config) error {
	if c.providerKeys.Any() || c.openAIKey != "" || c.openRouterKey != "" || c.anthropicKey != "" || c.openCodeKey != "" || c.mock {
		return nil
	}
	hasCustom, err := cliconfig.HasOperatorProviderDefinitions(true, true, nil)
	if err != nil {
		return fmt.Errorf("resolve operator provider configuration: %w", err)
	}
	if hasCustom {
		return nil
	}
	var probe app.Config
	c.toolhiveLLMFlags.Apply(&probe)
	if app.ToolhiveAvailable(probe) {
		return nil
	}
	return errors.New("no LLM provider configured: set a provider credential, use --auth-file, enable a ToolHive gateway, pass --mock, or connect to mecated; see docs/usage.md")
}

// mayEmbed reports whether this run may host an embedded server, and so is
// subject to the provider/posture checks in validate() and the pre-TUI posture
// WARN in run(). The bare/local mode always embeds; `connect` never embeds, so
// it skips those checks (ADR 0087). It is the single predicate both guards key
// on, so the gating rationale lives in one place.
func (c config) mayEmbed() bool {
	return c.transportMode == modeLocal
}

// embeddedAuthoritativePosture resolves the SAME posture tier app.Build resolves for
// the embedded server: the --posture flag + --yolo/--trust-project aliases + the
// operator-global settings.yaml posture: key. It mirrors mecated's posturePreCheckConfig
// — the conventional discovery is ON for the embedded server (PermissionsConventional /
// ImportClaudePermissions true, see embeddedConfig).
func embeddedAuthoritativePosture(c config) app.Posture {
	return app.ResolveAuthoritativePosture(app.Config{
		Workspace:               c.workspace,
		PermissionsConventional: true,
		ImportClaudePermissions: true,
		Posture:                 app.ParsePosture(c.posture),
		PostureFlagSet:          c.postureFlagSet,
		AllowAllTools:           c.allowAllTools,
		TrustProject:            c.trustProject,
	})
}

// embeddedPrivileged is the "root && !sandbox" predicate fed to the posture refusal
// (the cmd owns the os/env reads; internal/app takes the bool). It is the SAME value
// embeddedConfig threads onto app.Config.Privileged so the fast-path and Build agree.
func embeddedPrivileged() bool {
	sandbox := os.Getenv("MECATL_SANDBOX") == "1" || os.Getenv("IS_SANDBOX") == "1"
	return os.Geteuid() == 0 && !sandbox
}
