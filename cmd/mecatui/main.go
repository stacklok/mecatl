// Command mecatui is a flashy, themeable terminal UI for the mecatl headless
// agentic coding harness. It is a gRPC CLIENT of a mecated server: it creates a
// session, opens the bidi Converse stream, renders the streamed Events (glamour
// markdown for assistant text, themed lipgloss for user/tool blocks), and resolves
// permission asks inline by sending ResumeApproval back on the same stream.
//
// The server it talks to may be EXTERNAL (a separately-run mecated, via --server)
// or, by default, one this process HOSTS in-process over a UNIX socket (see
// cmd/mecatui/embed) — so a single `mecatui` binary "just works" with no daemon to
// start and no TCP port. In AUTO mode (no --server) it first probes the loopback
// default and reuses a server already running there; only if none answers does it
// embed.
//
// Architectural boundary: the render packages (ui, theme) and the client package
// import no engine/... or internal/... package and no proto directly — they render purely from
// proto Events. Hosting the embedded server makes the cmd/mecatui MAIN (and its
// embed subpackage) a second composition root, alongside cmd/mecated; that import
// of internal/app + the server adapter is confined HERE and to cmd/mecatui/embed.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/adrg/xdg"
	"golang.org/x/term"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/embed"
	"github.com/stacklok/mecatl/cmd/mecatui/theme"
	"github.com/stacklok/mecatl/cmd/mecatui/ui"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/internal/adapter/slogdiag"
	"github.com/stacklok/mecatl/internal/adapter/xdgconfig"
	"github.com/stacklok/mecatl/internal/app"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "mecatui:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	cfg, err := parseFlags(args)
	if err != nil {
		return err
	}
	if err := cfg.validate(); err != nil {
		return err
	}

	// UNIVERSAL global-slog floor: redirect the stdlib default to io.Discard (or, under
	// --quiet, still discard) BEFORE any transport resolution or the Bubble Tea program.
	// This covers EVERY transport path — external --server and reuse-an-already-running
	// mecated both return early from resolveTransport and would otherwise leave the
	// default at stderr, which the alt-screen (started below for ALL paths) would let a
	// stray ambient/third-party slog line corrupt. The host-embedded branch later refines
	// this floor to the mecatui.log file writer. See docs/adr/0020-diagnostics.md.
	installBaselineSlog(cfg.quiet)

	// Operator-posture WARN: mecatui has no slog and runs on the alt screen, so emit a
	// single pre-TUI stderr line (it lands in scrollback before the alt screen takes
	// over). Only meaningful for the embedded server (an external --server owns its own
	// posture). Refusal already handled in validate(). The line is tier-specific:
	// strict/trusted are silent, auto/yolo each warn (yolo names the child-defense-OFF
	// behaviour change).
	if cfg.server == "" {
		switch embeddedAuthoritativePosture(cfg) {
		case app.PostureAuto:
			fmt.Fprintln(os.Stderr, "mecatui: WARNING: posture auto is active; allow-all is ON for the embedded server (the built-in mutate-ask floor + the MAIN agent's substitution floor are waived). A Deny in any scope and any configured Ask still apply. The CHILD prompt-injection defense stays ON. For unattended single-tenant use.")
		case app.PostureYolo:
			fmt.Fprintln(os.Stderr, "mecatui: WARNING: posture yolo is active; allow-all is ON AND the CHILD prompt-injection defense is OFF — $()/backtick/heredoc commands AUTO-RUN in subagents/branches. A Deny in any scope and any configured Ask still apply. ISOLATED, SINGLE-TENANT use ONLY. NOTE: --yolo now ALSO loosens the child substitution floor.")
		}
	}

	reg := buildRegistry(cfg.workspace, cfg.themeDir)
	if cfg.listThemes {
		for _, name := range reg.List() {
			fmt.Println(name)
		}
		return nil
	}
	th, ok := reg.Resolve(cfg.theme)
	if !ok {
		fmt.Fprintf(os.Stderr, "mecatui: unknown theme %q, using %q\n", cfg.theme, th.Name)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Resolve where to connect: an explicit external server, a server already
	// running on the loopback default, or an embedded server we host in-process.
	target, dial, cleanup, err := resolveTransport(ctx, cfg)
	if err != nil {
		return err
	}
	defer cleanup()

	if cfg.insecure {
		fmt.Fprintln(os.Stderr, "mecatui: WARNING: --insecure skips TLS certificate verification (testing only)")
	}

	cl, err := client.Dial(dial)
	if err != nil {
		return err
	}
	defer func() { _ = cl.Close() }()

	// Client-side model-selection persistence (the /models picker): the store reads
	// the last-used selection at launch and persists a pick. Lives in main (the
	// composition root) so the client stays proto-only and the ui never touches
	// os/xdg. The connect-time ListModels reconcile clears a now-unavailable provider
	// before the create carries it (see ui.Init / updateModelsMsg).
	store := newSelectionStore(xdgconfig.OSEnv)
	initialSel := store.Load(cfg.workspace)
	// Provenance inputs for the /models picker (display-only): the un-collapsed
	// per-workspace entry and the global default, kept separate so the picker can tell
	// "workspace default" from "global default" without a server round-trip.
	wsDefault, wsDefaultSet := store.LoadWorkspace(cfg.workspace)
	globalDefault := store.LoadGlobalDefault()

	deps := ui.Deps{
		Session:             &sessionAdapter{cl: cl, workspace: cfg.workspace, mode: cfg.mode},
		Conv:                cl,
		MCP:                 cl,
		Cmds:                cl,
		Skills:              cl,
		Agents:              cl,
		Soul:                cl,
		UserModel:           cl,
		Models:              cl,
		Worktrees:           cl,
		Sched:               cl,
		Sessions:            cl,
		Replayer:            cl,
		SelectionStore:      store,
		InitialModel:        initialSel,
		WorkspaceDefault:    wsDefault,
		WorkspaceDefaultSet: wsDefaultSet,
		GlobalDefault:       globalDefault,
		Clipboard:           client.NewClipboard(),
		Theme:               th,
		Server:              target,
		// Model is best-effort display only. For an EXTERNAL --server it reflects
		// the locally-configured --model flag and may NOT match the server's actual
		// model (the server owns provider config); for an embedded server it is
		// authoritative. The footer context-meter denominator is the SERVER-resolved
		// per-model window echoed on session create (and refreshed on GetSession), now
		// live-first server-side — there is no client-side override (the operator
		// escape-hatch is mecated's -context-window-override, which moves both the
		// engine trigger and this echoed denominator).
		Model:     cfg.model,
		Workspace: cfg.workspace,
		Mode:      cfg.mode,
		Ctx:       ctx,
		// Build version for the welcome splash (ldflags-set; "dev" by default).
		Version: version,
		// Suppress the rich welcome splash under --no-banner, --quiet, or a
		// non-interactive stdin (the OR lives here so config.go stays pure — it owns
		// only the flag). The plain prompt hint is still shown in all three cases.
		NoBanner: cfg.noBanner || cfg.quiet || !term.IsTerminal(int(os.Stdin.Fd())),
		// First-class opt-out: render inline in the normal buffer (preserving
		// native scrollback) instead of the alternate screen. Default false.
		NoAltScreen: cfg.noAltScreen,
		// Escape hatch: disable mouse capture so the terminal's native selection
		// works (trades away in-app wheel scroll + drag-select). Default false.
		NoMouse: cfg.noMouse,
		// Diagnostic: MECATUI_DEBUG_MOUSE=1 shows raw mouse coords + content mapping in
		// the footer (for diagnosing selection/coordinate issues). Default off.
		DebugMouse: os.Getenv("MECATUI_DEBUG_MOUSE") != "",
	}

	// Apply keymap overrides (CLI for now).
	if err := applyKeyOverridesToDeps(cfg, &deps); err != nil {
		return err
	}

	prog := tea.NewProgram(ui.New(deps), tea.WithContext(ctx))
	_, err = prog.Run()
	return err
}

// resolveTransport decides how mecatui reaches a server and returns the dial
// target (for display + connection), the client.DialConfig to dial it with, and a
// cleanup func to defer (a no-op unless an embedded server was started).
//
//   - --server set:     dial that external address with the TLS/auth flags.
//   - --server empty:   AUTO — if a server already answers on the loopback default,
//     reuse it (plaintext); otherwise host an embedded server over a UNIX socket.

// keyOverridesFromConfig merges CLI --keymap entries into a map[string][]string.
// YAML wiring will be added in a later step; for now only CLI is consulted.
func keyOverridesFromConfig(cfg config) map[string][]string {
	if cfg.keymap == nil || len(*cfg.keymap) == 0 {
		return nil
	}
	out := make(map[string][]string, len(*cfg.keymap))
	for action, val := range map[string]string(*cfg.keymap) {
		// val is comma-separated chords; trim spaces
		parts := make([]string, 0, 1)
		for _, p := range strings.Split(val, ",") {
			p = strings.TrimSpace(p)
			if p != "" {
				parts = append(parts, p)
			}
		}
		if len(parts) > 0 {
			out[action] = parts
		}
	}
	return out
}

func resolveTransport(ctx context.Context, cfg config) (target string, dial client.DialConfig, cleanup func(), err error) {
	noop := func() {}

	if cfg.server != "" {
		return cfg.server, client.DialConfig{
			Server:    cfg.server,
			AuthToken: cfg.authToken,
			UseTLS:    cfg.useTLS,
			TLSCAFile: cfg.tlsCA,
			Insecure:  cfg.insecure,
		}, noop, nil
	}

	if client.IsReachable(ctx, defaultProbeAddr) {
		fmt.Fprintf(os.Stderr, "mecatui: using mecated already running at %s\n", defaultProbeAddr)
		return defaultProbeAddr, client.DialConfig{Server: defaultProbeAddr}, noop, nil
	}

	// We are about to HOST an embedded server (the auto-reuse path above returned).
	// This is the pre-TUI window (before the Bubble Tea alt screen starts) where the
	// first-encounter workspace-trust prompt belongs (Workspace-Trust Phase 2c): if
	// the workspace is not already trusted but carries a project authority set (or a
	// remembered entry that DRIFTED), prompt the operator. The outcome feeds
	// cfg.trustProject so embeddedConfig → app.Build honours it WITHOUT re-resolving
	// or re-prompting. Only the embedded server is gated; an external --server (above)
	// owns its own declarative trust. See cmd/mecatui/trust.go.
	// Open the embedded server's diagnostics sink ONCE, here in the host-an-embedded
	// branch. It is a file under $XDG_STATE_HOME/mecatl/mecatui.log (fallback
	// ~/.local/state/...), or io.Discard under --quiet / on any open failure — NEVER
	// stderr, which would corrupt the Bubble Tea alt-screen. The same writer backs
	// BOTH the app.Diagnostics sink and the perf surface's slog.Logger, so neither
	// path leaks a line to the terminal. The file handle (when one was opened) is
	// closed by the returned cleanup alongside the server.
	diagW, diagCloser, toFile := openDiagLogWriter(xdgconfig.OSEnv, cfg.quiet)
	diag := slogdiag.New(diagW, false, port.LevelInfo)
	// A dedicated slog.Logger over the SAME writer for the perf surface's Logger field.
	// Explicit injection (rather than relying on the redirected default below) keeps the
	// perf surface's sink unambiguous even if a caller ever reuses perfConfig elsewhere.
	perfLogger := slog.New(slog.NewTextHandler(diagW, &slog.HandlerOptions{Level: slog.LevelInfo}))
	// REFINE the universal baseline (installBaselineSlog at the top of run() already
	// floored the global default to io.Discard for every transport path): in the
	// host-embedded path, redirect the GLOBAL slog default onto the same FILE writer the
	// Diagnostics sink uses (io.Discard under --quiet) BEFORE the embedded server /
	// Bubble Tea program starts. Any ambient slog.Default() use — a transitive
	// dependency that logs, or embed.setupPerf's nil-Logger fallback — now writes to
	// $XDG_STATE_HOME/mecatl/mecatui.log, NEVER to stderr/the alt-screen, AND is
	// operator-recoverable rather than discarded. This second SetDefault wins over the
	// baseline for the embedded path. cmd/ mains are the only layer allowed to call
	// slog.SetDefault (internal/ flows through the injected port.Diagnostics, ban-
	// guarded). See docs/adr/0020-diagnostics.md.
	slog.SetDefault(slog.New(slog.NewTextHandler(diagW, &slog.HandlerOptions{Level: slog.LevelInfo})))

	cfg = applyTrustPrompt(cfg, diag)

	srv, err := embed.Start(ctx, embeddedConfig(cfg, diag), perfConfig(cfg, perfLogger))
	if err != nil {
		_ = diagCloser.Close()
		return "", client.DialConfig{}, noop, fmt.Errorf("start embedded server: %w", err)
	}
	if toFile {
		// One line, written to the FILE sink (never the TUI), so an operator can find
		// where the embedded server's diagnostics went.
		diag.Log(ctx, port.LevelInfo, "mecatui: embedded server diagnostics log opened",
			"path", resolveDiagLogPath(xdgconfig.OSEnv))
	}
	fmt.Fprintf(os.Stderr, "mecatui: no server found; hosting an embedded mecated at %s\n", srv.Target())
	if addr := srv.AdminAddr(); addr != "" {
		// Mirror mecated's loopback/unauth note: the perf surface can leak prompt
		// text/file paths/goroutine stacks, so it is loopback-bound only.
		paths := "/metrics /debug/pprof /debug/vars /debug/flightrecorder"
		if cfg.perfMCP {
			paths += " /mcp"
		}
		fmt.Fprintf(os.Stderr, "mecatui: perf admin surface (loopback, UNAUTHENTICATED) at http://%s — %s\n", addr, paths)
		if cfg.perfMCP {
			fmt.Fprintf(os.Stderr, "mecatui: perf MCP ready at http://%s/mcp — point an MCP client here\n", addr)
		}
	}
	// The embedded server has no auth/TLS — it is a private UNIX socket dialled
	// plaintext, the same single-user loopback trust model mecated uses. Cleanup
	// closes the server AND the diagnostics log file (a no-op closer for the
	// discard/quiet paths), so a clean exit leaks no fd.
	return srv.Target(), client.DialConfig{Server: srv.Target()}, func() {
		_ = srv.Close()
		_ = diagCloser.Close()
	}, nil
}

// applyTrustPrompt runs the pre-TUI first-encounter workspace-trust gate
// (Workspace-Trust Phase 2c) and returns cfg with trustProject set when the
// operator (or an already-existing trust grant) trusts the run. It builds the
// production trust seam over embeddedConfig(cfg) — so the prompt folds the EXACT
// same app.Config app.Build will fold — and reads stdin / writes stderr / detects
// a TTY on os.Stdin's fd. On a positive outcome it sets trustProject=true so
// app.Build short-circuits to TrustFlag (the prompt outcome wins, no
// re-resolution). On a non-TTY or a declined prompt, trustProject is left as-is and
// app.Build re-resolves to the same (untrusted, or declaratively-trusted) answer.
//
// --trust-project already trusting the project short-circuits the whole gate (the
// flag is an explicit operator grant; ResolveTrust returns Trusted, so no prompt).
func applyTrustPrompt(cfg config, diag port.Diagnostics) config {
	seam := prodTrustSeam(embeddedConfig(cfg, diag))
	isTTY := term.IsTerminal(int(os.Stdin.Fd()))
	out := resolveTrustForRun(seam, cfg.workspace, os.Stdin, os.Stderr, isTTY)
	// Monotonic-positive: only a positive outcome grants; never flip an existing
	// grant off (a declared/remembered trust already made ResolveTrust return
	// Trusted, so out.trusted is true and this is a no-op for those paths).
	if out.trusted {
		cfg.trustProject = true
	}
	return cfg
}

// embeddedConfig maps the TUI config onto the shared app.Config build contract for
// the in-process server. It enables the standard default toolset (Bash unless
// --no-bash, Fork), the agent-teams capability (inert until a client
// drives a team), conventional agent-definition discovery (AgentsConventional:
// true, also inert until a <name>.md exists under a conventional dir), and
// cross-session memory (Remember/Recall) scoped per-project (see resolveMemoryDir;
// disable with --no-memory or relocate with --memory-dir), slash-command expansion
// from the conventional dirs (.mecatl/commands, .claude/commands; disable with
// --no-commands or relocate with --commands-dir), conventional skill discovery
// (the read-only Skill tool over .claude/skills etc.; disable with --no-skills or
// scope with --skills-dir), and ToolHive MCP server discovery (ToolHiveEnabled:
// true, fail-soft when no runtime is reachable, so inert on a laptop without
// Podman/Docker). It leaves the heavier opt-ins (static --mcp-server, telemetry,
// the writable SkillDraft quarantine) off — a focused single-user default. The
// provider is OpenAI when OPENAI_API_KEY is set, else the offline mock (--mock).
func embeddedConfig(cfg config, diag port.Diagnostics) app.Config {
	cmdDir, enableCmds := resolveCommands(cfg)
	skillDirs, skillsConv := resolveSkills(cfg)
	out := app.Config{
		Workspace:       cfg.workspace,
		Model:           cfg.model,
		DefaultProvider: cfg.defaultProvider,
		DefaultModel:    cfg.defaultModel,
		// defaultProviderFlagSet lets CLI out-rank the operator-global settings.yaml
		// models.default_provider: key (folded by foldOperatorDefaultProvider in app.Build).
		DefaultProviderFlagSet: cfg.defaultProviderFlagSet,
		SubagentModel:          cfg.subagentModel,
		// Per-slot models (ADR 0030): the mecated flags mirror, mapped verbatim.
		// The *cliconfig.KeyValueList flag bindings are converted to the plain
		// map[string]string app.Config expects (nil for an unset flag).
		ModelAliases: cfg.modelAliases.AsMap(),
		ModelSlots:   cfg.modelSlots.AsMap(),
		// Headless ask reviewer (issue #31): the mecated flag mirrors, mapped
		// verbatim. Empty model = off (the zero-cost default).
		SubagentAskReviewerModel:     cfg.subagentAskReviewer,
		SubagentAskReviewerMaxDenies: cfg.subagentAskReviewerMaxDenies,
		SubagentAskReviewerPolicy:    cfg.subagentAskReviewerPolicy,
		// Subagent model router (ADR 0042): kill-switch. =false forces the router OFF
		// (RouterDisabled); a bare flag / =true is a harmless no-op (the router stays
		// governed by the taxonomy); unset leaves routing governed by the operator-tier
		// models.router: taxonomy. Idempotent: safe to compute on both calls.
		RouterDisabled:       cfg.subagentModelRouterSet && !cfg.subagentModelRouter,
		UseMock:              cfg.mock,
		Shell:                "/bin/sh",
		NoBash:               cfg.noBash,
		Compaction:           "heuristic",
		Tokenizer:            "heuristic",
		LLMMaxAttempts:       3,
		LLMPerAttemptTimeout: cfg.llmPerAttemptTimeout,
		LLMStreamIdleTimeout: cfg.llmStreamIdleTimeout,
		LLMBreakerThreshold:  5,
		LLMBreakerCooldown:   30 * time.Second,
		EnableParallel:       true,
		EnableTeams:          true,
		AgentsConventional:   true,
		// Memory is ON by default, per-project. MemoryConsolidateInterval is left
		// at 0 (off) deliberately: the "dream" distiller spawns a goroutine that
		// calls the real provider on a timer, so a default-on interval would
		// silently spend tokens on an idle TUI. mecated defaults it to 0 too.
		MemoryDir: resolveMemoryDir(cfg),
		// Durable session/event store ON by default (issue #79): a per-workspace dir
		// under $XDG_STATE_HOME/mecatui/sessions, so a session survives restart and
		// can be inspected after the fact. --no-store opts out (in-memory store);
		// --store-dir relocates it. See resolveStoreDir.
		StoreDir: resolveStoreDir(cfg),
		// Session retention GC (issues #38 + #79). With a DURABLE default store the
		// child snapshots (subagent/parallel/team) AND the top-level main-session
		// snapshots accumulate on disk, so a LONG-LIVED TUI process otherwise grows
		// without bound. Free and local, so on by default. The child knobs are
		// mecated's defaults; the main knobs (issue #79) bound the durable store the
		// TUI now defaults on — a 30-day age horizon plus a 200-session global cap, so
		// recent history is recoverable but stale sessions are reaped. A live run is
		// always skipped.
		ChildRetention:             168 * time.Hour,
		ChildRetentionMaxPerFamily: 500,
		MainRetention:              720 * time.Hour,
		MainRetentionMaxTotal:      200,
		ChildGCInterval:            time.Hour,
		// Soul ON by default (issue #14, Phase 1): a user-scoped, agent-READ-ONLY
		// persona fragment read from the conventional ~/.config/mecatl/soul.md
		// (fail-soft if absent), consistent with the "enable every free+local feature
		// by default" posture. --soul-file overrides the path; --no-soul disables it.
		SoulPath:    cfg.soulFile,
		NoSoul:      cfg.noSoul,
		ApproveSoul: cfg.approveSoul,
		SoulStrict:  cfg.soulStrict,
		// User model ON by default (issue #14, Phase 2): cross-project operator FACTS.
		// The background reviewer (UserModelReview) and consolidation stay OFF by
		// default — both spend tokens on the real provider, so an idle TUI never does.
		UserModelDir:            cfg.userModelDir,
		NoUserModel:             cfg.noUserModel,
		UserModelReview:         cfg.userModelReview,
		UserModelReviewInterval: cfg.userModelReviewInterval,
		// Slash commands ON by default (the .mecatl/commands + .claude/commands
		// convention); --no-commands disables, --commands-dir overrides. File-backed
		// commands are local, user-authored prompt templates — no network/trust cost,
		// unlike MCP prompts (which stay off with MCP).
		CommandsDir:    cmdDir,
		EnableCommands: enableCmds,
		// Skills ON by default via conventional discovery (.claude/skills etc.),
		// consistent with AgentsConventional. Only the read-only Skill tool — the
		// writable SkillDraft quarantine stays off (SkillsDraftDir unset). Opt out
		// with --no-skills; scope to one vetted dir with --skills-dir.
		SkillsDirs:         skillDirs,
		SkillsConventional: skillsConv,
		// ToolHive MCP discovery ON by default (the "default" group). Fail-soft when
		// no container runtime is reachable (degrades to zero servers + diagnostic),
		// so it's inert on a laptop without Podman/Docker. When workloads ARE running,
		// their tools register automatically — the same single-user convenience as
		// agents/skills/commands. Static --mcp-server stays off (heavier opt-in).
		ToolHiveEnabled: true,
		ToolHiveGroup:   "", // empty -> "default"
		// MCP resource/prompt tools: ON when a connected server exposes them (no-op
		// when none do or when ToolHive discovers zero servers).
		MCPResourceTools: true,
		MCPPrompts:       true,
		// File-based permission config (issue #13): discover the conventional
		// per-project config and import Claude-Code settings.json — re-resolved per
		// session against the session workspace root, inert until a
		// .mecatl/settings.yaml (or .claude/settings.json) exists. TrustProject is
		// DEFAULT FALSE (unified with mecated, WORKSPACE-TRUST Phase 0): a project's
		// ALLOW rules and its project soul are honoured ONLY with --trust-project; its
		// deny/ask rules are always honoured regardless.
		PermissionsConventional: true,
		ImportClaudePermissions: true,
		TrustProject:            cfg.trustProject,
		AllowAllTools:           cfg.allowAllTools,
		// Posture ladder: --posture sets the tier; --yolo/--trust-project are aliases
		// composition folds MAX-tier. postureFlagSet lets CLI out-rank the operator-global
		// settings.yaml posture: key. Privileged is the "root && !sandbox" predicate fed
		// to Build's AUTHORITATIVE root-refusal, so a YAML-only allow-all tier cannot
		// escape it.
		Posture:        app.ParsePosture(cfg.posture),
		PostureFlagSet: cfg.postureFlagSet,
		// Output-economy tier (ADR 0041): operator-tier only; outputEconomyFlagSet
		// lets CLI out-rank the operator-global settings.yaml output-economy: key.
		OutputEconomy:        cfg.outputEconomy,
		OutputEconomyFlagSet: cfg.outputEconomyFlagSet,
		// Reasoning-effort tier (ADR 0055): operator-tier only; reasoningEffortFlagSet
		// lets CLI out-rank the operator-global settings.yaml reasoning-effort: key.
		ReasoningEffort:        cfg.reasoningEffort,
		ReasoningEffortFlagSet: cfg.reasoningEffortFlagSet,
		Privileged:             embeddedPrivileged(),
		// INTERACTIVE: mecatui IS the interactive client — a human sits at the
		// approval modal. So the embedded server runs interactive (Interactive=true),
		// and a subagent/team-member/branch child's unresolved permission ask is
		// SURFACED to that modal (the #32 re-framed parent EvPermissionAsk; the
		// client routes the verdict back to the child via ResumeApproval →
		// Run.Approve → childAskRouter). It must NOT default headless (which would
		// auto-deny — or LLM-adjudicate — a child ask the human is right there to
		// answer). This is why --subagent-ask-reviewer is INERT under mecatui (the
		// modal always sees the ask): the embedded reviewer flag exists only for
		// symmetry with mecated and is documented as such.
		Interactive: true,
		// Diagnostics is the injected file-backed (or, under --quiet, discarding) sink.
		// It is NEVER stderr: an operational line on stderr corrupts the Bubble Tea
		// alt-screen. The caller (resolveTransport) opens the sink once over
		// $XDG_STATE_HOME/mecatl/mecatui.log and threads it here AND into the perf
		// Logger, so both land in the same file rather than the terminal. A nil diag
		// (e.g. the pre-embed trust-prompt fold) is tolerated — app.Build defaults it to
		// NopDiagnostics.
		Diagnostics: diag,
	}
	// Apply the shared provider credentials + base URLs (cliconfig). mecatui's
	// UseOpenAI is "an OpenAI key is present" (it has no --openai flag), preserved here
	// off the resolved key.
	keys := cfg.providerFlags.Apply(&out)
	out.UseOpenAI = keys.OpenAI != ""
	cfg.toolhiveLLMFlags.Apply(&out)
	return out
}

// perfConfig maps the TUI config onto the embedded server's perf-observability
// options (decision 7). It is OFF unless --perf is passed; when on, it carries the
// loopback admin address (empty → an ephemeral port chosen and logged by embed)
// and the optional goroutine-leak watchdog threshold. The Logger is set to the
// SAME file-backed (or, under --quiet, discarding) writer the Diagnostics sink uses
// — so the perf surface's startup/teardown/watchdog lines land in
// $XDG_STATE_HOME/mecatl/mecatui.log, NEVER on stderr where they would corrupt the
// Bubble Tea alt-screen. (Belt-and-suspenders: resolveTransport also redirects the
// global slog default onto the same writer, so even embed's nil-Logger fallback
// would land in the file rather than the terminal.)
func perfConfig(cfg config, logger *slog.Logger) embed.PerfConfig {
	if !cfg.perf {
		return embed.PerfConfig{}
	}
	return embed.PerfConfig{
		Enabled:                true,
		Addr:                   cfg.perfAddr,
		GoroutineWarnThreshold: cfg.perfGoroutineWarnThreshold,
		MCP:                    cfg.perfMCP,
		Logger:                 logger,
	}
}

// resolveSkills applies the embedded-server skill-discovery precedence: --no-skills
// disables it (nil dirs, no conventional discovery → the Skill tool isn't
// registered); an explicit --skills-dir scopes discovery to that single vetted
// directory; otherwise conventional discovery is ON by default (nil dirs, true).
// The returned (dirs, conventional) pair feeds app.Config.{SkillsDirs,
// SkillsConventional}; registerSkills registers the Skill tool only when the
// resolved sources contain at least one SKILL.md (opt-in by presence).
func resolveSkills(cfg config) (dirs []string, conventional bool) {
	if cfg.noSkills {
		return nil, false
	}
	if cfg.skillsDir != "" {
		return []string{cfg.skillsDir}, false
	}
	return nil, true
}

// resolveCommands applies the embedded-server slash-command precedence: --no-commands
// disables expansion entirely (returns "", false → the NoopExpander); an explicit
// --commands-dir overrides the directory; otherwise commands are ON by default using
// the conventional workspace dirs (.mecatl/commands, .claude/commands → "", true).
// The returned (dir, enable) pair feeds app.Config.{CommandsDir,EnableCommands};
// buildCommandExpander turns expansion on when either is set.
func resolveCommands(cfg config) (dir string, enable bool) {
	if cfg.noCommands {
		return "", false
	}
	if cfg.commandsDir != "" {
		return cfg.commandsDir, true
	}
	return "", true
}

// resolveMemoryDir applies the embedded-server memory precedence: --no-memory
// disables it (""), an explicit --memory-dir overrides, otherwise a per-project
// default under XDG data (see defaultMemoryDir). The store owns creating the dir;
// main only computes a path string and treats the location as opaque.
func resolveMemoryDir(cfg config) string {
	if cfg.noMemory {
		return ""
	}
	if cfg.memoryDir != "" {
		return cfg.memoryDir
	}
	// The single place that reads the xdg.DataHome global — main is the
	// composition root, so the one global read lives here, and defaultMemoryDir
	// stays a pure function of its arguments (cheap to table-test).
	return defaultMemoryDir(xdg.DataHome, cfg.workspace)
}

// defaultMemoryDir derives a stable, per-project memory directory under the XDG
// data base dataHome ($XDG_DATA_HOME, else ~/.local/share — resolved by the caller
// via xdg.DataHome). The leaf is the resolved absolute workspace path with the OS
// separator replaced by '-' (preserving the leading separator as a leading '-'),
// e.g. "/var/home/ozz/dev/mecatl" → "-var-home-ozz-dev-mecatl". Encoding the FULL
// path keeps it deterministic, human-legible, and collision-free across same-named
// projects. Returns "" when dataHome or workspace is empty — in that degraded case
// memory stays off rather than anchoring a store at a bogus path. Pure in its
// arguments: it reads no globals.
func defaultMemoryDir(dataHome, workspace string) string {
	if dataHome == "" || workspace == "" {
		return ""
	}
	leaf := strings.ReplaceAll(workspace, string(filepath.Separator), "-")
	return filepath.Join(dataHome, "mecatui", "memory", leaf)
}

// resolveStoreDir applies the embedded-server session-store precedence (issue
// #79): --no-store disables it (""), an explicit --store-dir overrides,
// otherwise a per-workspace default under the XDG STATE base (see
// defaultStoreDir). An empty result makes the engine fall back to the in-memory
// store. The store owns creating the dir (mode 0700); main only computes a path
// string and treats the location as opaque.
//
// CONCURRENCY CAVEAT: the per-workspace default isolates the common case (one
// mecatui per workspace). There is NO file lock — a SECOND mecatui hosting an
// embedded server on the SAME workspace shares this dir, and its retention GC
// may prune the other instance's idle (non-live) main sessions early. Point a
// second instance at its own --store-dir, or --no-store, to avoid that.
func resolveStoreDir(cfg config) string {
	if cfg.noStore {
		return ""
	}
	if cfg.storeDir != "" {
		return cfg.storeDir
	}
	// The single place that reads the XDG STATE base — main is the composition
	// root, so the one global read lives here (via xdgconfig.UserStateDir), and
	// defaultStoreDir stays a pure function of its arguments (cheap to table-test).
	return defaultStoreDir(xdgconfig.UserStateDir(xdgconfig.OSEnv), cfg.workspace)
}

// defaultStoreDir derives a stable, per-workspace session-store directory under
// the XDG state base stateBase ($XDG_STATE_HOME, else ~/.local/state — resolved
// by the caller via xdgconfig.UserStateDir). The leaf is the resolved absolute
// workspace path with the OS separator replaced by '-' (preserving the leading
// separator as a leading '-'), e.g. "/var/home/ozz/dev/mecatl" →
// "-var-home-ozz-dev-mecatl" — the SAME slug scheme as defaultMemoryDir, so the
// two stores sit side by side per workspace. Encoding the FULL path keeps it
// deterministic, human-legible, and collision-free across same-named projects.
// Returns "" when stateBase or workspace is empty — in that degraded case the
// store stays in-memory rather than anchoring at a bogus path. Pure in its
// arguments: it reads no globals.
func defaultStoreDir(stateBase, workspace string) string {
	if stateBase == "" || workspace == "" {
		return ""
	}
	leaf := strings.ReplaceAll(workspace, string(filepath.Separator), "-")
	return filepath.Join(stateBase, "mecatui", "sessions", leaf)
}

// buildRegistry seeds the theme registry with built-ins and loads user theme
// dirs in increasing precedence: XDG config → workspace .mecatui → cwd .mecatui
// → an explicit --theme-dir. Load errors are warnings, not fatal — a bad theme
// file should never stop the UI from launching.
func buildRegistry(workspace, extraDir string) *theme.Registry {
	reg := theme.NewRegistry()
	for _, dir := range themeDirs(workspace, extraDir) {
		if err := reg.LoadDir(dir); err != nil {
			fmt.Fprintln(os.Stderr, "mecatui: theme load:", err)
		}
	}
	return reg
}

// themeDirs returns the theme directory search path, lowest precedence first:
// XDG/home config, the workspace's .mecatui/themes, the cwd's .mecatui/themes,
// then any explicit --theme-dir (highest).
func themeDirs(workspace, extraDir string) []string {
	var dirs []string
	if xdg.ConfigHome != "" {
		dirs = append(dirs, filepath.Join(xdg.ConfigHome, "mecatui", "themes"))
	}
	if workspace != "" {
		dirs = append(dirs, filepath.Join(workspace, ".mecatui", "themes"))
	}
	if wd, err := os.Getwd(); err == nil && wd != workspace {
		dirs = append(dirs, filepath.Join(wd, ".mecatui", "themes"))
	}
	if extraDir != "" {
		dirs = append(dirs, extraDir)
	}
	return dirs
}

// sessionAdapter bridges the ui's SessionCreator to the client's
// CreateSession(ctx, workspace, mode, sel). The workspace is fixed at startup;
// the mode and model selection are per-call so in-TUI mode switches and /models
// restarts carry the current desired posture through the same proto-build point.
// The ui never sees the proto request. CreateSessionInWorkspace (issue #102) is
// the /worktrees switch path: it passes an explicit workspace (a sibling git
// worktree); CreateSession delegates to it with the launch workspace so the
// existing restart + connect paths are byte-identical.
type sessionAdapter struct {
	cl        *client.Client
	workspace string
	mode      string
}

func (s *sessionAdapter) CreateSession(ctx context.Context, sel client.ModelSelection, mode string) (string, client.Capabilities, client.ResolvedModel, error) {
	return s.CreateSessionInWorkspace(ctx, s.workspace, sel, mode)
}

func (s *sessionAdapter) CreateSessionInWorkspace(ctx context.Context, workspace string, sel client.ModelSelection, mode string) (string, client.Capabilities, client.ResolvedModel, error) {
	if mode == "" {
		mode = s.mode
	}
	return s.cl.CreateSession(ctx, workspace, client.ModeFromString(mode), sel)
}

func (s *sessionAdapter) CloseSession(ctx context.Context, id string) error {
	return s.cl.CloseSession(ctx, id)
}

func (s *sessionAdapter) GetSession(ctx context.Context, id string) (client.SessionSnapshot, error) {
	return s.cl.GetSession(ctx, id)
}

func (s *sessionAdapter) SetMode(ctx context.Context, id, mode string) (string, error) {
	return s.cl.SetMode(ctx, id, mode)
}
