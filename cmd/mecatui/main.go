// Command mecatui is a flashy, themeable terminal UI for the mecatl headless
// agentic coding harness. It is a gRPC CLIENT of a mecated server: it creates a
// session, opens the bidi Converse stream, renders the streamed Events (glamour
// markdown for assistant text, themed lipgloss for user/tool blocks), and resolves
// permission asks inline by sending ResumeApproval back on the same stream.
//
// The server it talks to may be EXTERNAL (a separately-run mecated, via
// `mecatui connect ADDRESS`) or, by default, one this process HOSTS in-process
// over a UNIX socket (see cmd/mecatui/embed) — so a single `mecatui` binary
// "just works" with no daemon to start and no TCP port. The bare invocation
// ALWAYS embeds (it never probes loopback); `connect ADDRESS` always dials.
//
// Architectural boundary: the render packages (ui, theme) and the client package
// import no engine/... or internal/... package and no proto directly — they render purely from
// proto Events. Hosting the embedded server makes the cmd/mecatui MAIN (and its
// embed subpackage) a second composition root, alongside cmd/mecated; that import
// of internal/app + the server adapter is confined HERE and to cmd/mecatui/embed.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/adrg/xdg"
	"golang.org/x/term"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/embed"
	"github.com/stacklok/mecatl/cmd/mecatui/statusline"
	"github.com/stacklok/mecatl/cmd/mecatui/theme"
	"github.com/stacklok/mecatl/cmd/mecatui/ui"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/internal/adapter/clientauth"
	"github.com/stacklok/mecatl/internal/adapter/credentialstore"
	"github.com/stacklok/mecatl/internal/adapter/mcpauthority"
	"github.com/stacklok/mecatl/internal/adapter/slogdiag"
	"github.com/stacklok/mecatl/internal/adapter/xdgconfig"
	"github.com/stacklok/mecatl/internal/app"
	"github.com/stacklok/mecatl/internal/buildinfo"
	"github.com/stacklok/mecatl/internal/cliconfig"
)

// usageErrorTrailer wraps a resolveInvocation usage error so main's error
// printer appends the top-level command summary (writeTopLevelHelp) beneath the
// error line — the operator who typo'd a command needs the grammar, not just the
// error. It rides run()'s ordinary error return (the resolver is pure and prints
// nothing itself).
type usageErrorTrailer struct{ err error }

func (e *usageErrorTrailer) Error() string { return e.err.Error() }
func (e *usageErrorTrailer) Unwrap() error { return e.err }

func main() {
	if buildinfo.IsVersion(os.Args) {
		buildinfo.PrintVersion(os.Stdout, "mecatui")
		return
	}
	if err := run(os.Args); err != nil {
		// --help / --help-all is a successful action: the Usage hook (or the
		// --help-all renderer) already printed help; mirror mecated's
		// errors.Is(err, flag.ErrHelp) handling and exit 0 without printing
		// "mecatui: flag: help requested".
		if errors.Is(err, flag.ErrHelp) {
			return
		}
		fmt.Fprintln(os.Stderr, "mecatui:", err)
		var trailer *usageErrorTrailer
		if errors.As(err, &trailer) {
			fmt.Fprintln(os.Stderr)
			writeTopLevelHelp(os.Stderr)
		}
		os.Exit(1)
	}
}

// prepareRun performs the side-effecting run preparation that follows pure
// invocation resolution: it renders top-level help and wraps leading-word usage
// errors for main's printer. The resolver itself performs neither action.
func prepareRun(argv []string) (invocationResolution, error) {
	res := resolveInvocation(argv)
	if res.err != nil {
		// A leading-word usage error (unknown command, connect missing/flag-first
		// ADDRESS): print the error AND the top-level command summary beneath it
		// (mirroring mecated's errBareInvocation arm) — the operator needs the
		// grammar, not just the error line. run() owns no streams, so main's error
		// printer writes both to stderr; the trailer marks the error so main can
		// recognize it without a string match.
		return invocationResolution{}, &usageErrorTrailer{err: res.err}
	}
	if res.helpIndex {
		writeTopLevelHelp(os.Stderr)
		return invocationResolution{}, flag.ErrHelp
	}
	if res.debugHelp {
		writeDebugHelp(os.Stderr, res.mode == modeConnect)
		return invocationResolution{}, flag.ErrHelp
	}
	return res, nil
}

func resolveConnectionMode(cfg config) string {
	if cfg.connectAddress != "" {
		return "connect"
	}
	return "embedded"
}

func validateRunConfig(cfg config) error {
	return cfg.validate()
}

// buildStatusSource constructs a source from already-validated customization.
func buildStatusSource(customization statusCustomization) statusline.Source {
	return newSource(customization)
}

func prepareStatusSource(cfg config) (statusline.Source, error) {
	if err := validateRunConfig(cfg); err != nil {
		return nil, err
	}
	customization, err := readStatusCustomization()
	if err != nil {
		return nil, err
	}
	return buildStatusSource(customization), nil
}

func run(argv []string) error {
	return runWithOptions(argv, runOptions{})
}

type restartTransport struct {
	Target    string
	TLSCAFile string
}

type runOptions struct {
	connectOpen            bool
	connectError           string
	connectReason          client.AuthReason
	connectTarget          string
	connectResumeSessionID string
	connectTransport       restartTransport
	recoveryOnly           bool
}

//nolint:gocyclo // composition root sequences transport, safe auth recovery, and Bubble Tea lifecycle.
func runWithOptions(argv []string, options runOptions) error {
	if err := runTestSignalHandler(); err != nil {
		return err
	}

	// Resolve the full CLI invocation through the PURE resolveInvocation seam
	// (ADR 0087). prepareRun owns only the help/error side effects; an executable
	// invocation then threads its mode and remaining flag tail into
	// parseTransportFlags. Neither step reads or mutates os.Args.
	res, err := prepareRun(argv)
	if err != nil {
		return err
	}

	if handled, err := runSpecialMode(res); handled {
		return err
	}

	cfg, err := parseRunConfig(res)
	if err != nil {
		return err
	}
	if cfg.providerKeys.AuthFileWarning != "" {
		fmt.Fprintln(os.Stderr, "mecatui: WARNING: "+wrapAuthFileWarning(cfg.providerKeys.AuthFileWarning))
	}
	statusSource, err := prepareStatusSource(cfg)
	if err != nil {
		return err
	}
	emitDebugPrivacyWarning(os.Stderr, cfg.debugTarget, cfg.debugMCP...)

	// UNIVERSAL global-slog floor: redirect the stdlib default to io.Discard (or, under
	// --quiet, still discard) BEFORE transport setup or the Bubble Tea program.
	// This covers EVERY transport path — the connect (client-only) mode returns early
	// from resolveTransport and would otherwise leave the default at stderr, which the
	// alt-screen (started below for ALL paths) would let a stray ambient/third-party
	// slog line corrupt. The host-embedded branch later refines this floor to the
	// mecatui.log file writer. See docs/adr/0020-diagnostics.md.
	installBaselineSlog(cfg.quiet)

	warnEmbeddedPosture(cfg)

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
	themeAutoDetect := resolveThemeAutoDetect(cfg, term.IsTerminal(int(os.Stdout.Fd())))
	if options.recoveryOnly {
		return runDisconnectedRecovery(context.Background(), argv, th, themeAutoDetect, options)
	}

	// Manual two-signal handler: first signal = graceful shutdown (cancels ctx →
	// Bubble Tea quits); second signal during cleanup = immediate hard os.Exit(130).
	ctx, forceExit := setupSignalHandler()

	// Resolve where to connect: an explicit external server, a server already
	// running on the loopback default, or an embedded server we host in-process.
	target, dial, transCleanup, err := resolveTransport(ctx, cfg)
	if err != nil {
		if reason, ok := client.AuthFailure(err, cfg.authToken != ""); ok {
			recoveryTarget := target
			if recoveryTarget == "" {
				recoveryTarget = cfg.connectAddress
			}
			return runWithOptions([]string{argv[0]}, authRecoveryOptions(reason, recoveryTarget, "Authentication needs attention.", options.connectResumeSessionID, restartTransport{Target: recoveryTarget, TLSCAFile: cfg.tlsCA}))
		}
		return err
	}

	if cfg.insecure {
		fmt.Fprintln(os.Stderr, "mecatui: WARNING: --insecure skips TLS certificate verification (testing only)")
	}

	cl, err := client.Dial(dial)
	if err != nil {
		transCleanup()
		return err
	}

	resumeCfg := cfg
	if options.connectResumeSessionID != "" {
		// This candidate originated from an interrupted auth stream, not an
		// explicit --resume. GetSession/transcript remain the server's ownership
		// proof; only a terminal turn boundary can be adopted automatically.
		resumeCfg.resumeID = options.connectResumeSessionID
		resumeCfg.resumeLatest = false
	}
	resume, uiWorkspace, err := startupResumeConfig(ctx, cl, resumeCfg)
	if options.connectResumeSessionID != "" {
		// An auth-recovery candidate is opportunistic. Only a verified terminal
		// boundary is adopted; every other state and every ambiguous verification
		// failure is discarded. The authenticated connection then proceeds to its
		// ordinary fresh CreateSession, whose own error remains authoritative.
		if !shouldAdoptAuthRecoveryCandidate(resume, err) {
			resume = nil
			uiWorkspace = cfg.workspace
			err = nil
		}
	}
	if err != nil {
		if reason, ok := client.AuthFailure(err, dial.AuthToken != "" || dial.TokenSource != nil); ok {
			_ = cl.Close()
			transCleanup()
			return runWithOptions([]string{argv[0]}, authRecoveryOptions(reason, target, "Authentication needs attention.", options.connectResumeSessionID, restartTransport{Target: target, TLSCAFile: cfg.tlsCA}))
		}
		_ = cl.Close()
		transCleanup()
		return err
	}

	// Client-side model-selection persistence (the /models picker): the store reads
	// the last-used selection at launch and persists a pick. Lives in main (the
	// composition root) so the client stays proto-only and the ui never touches
	// os/xdg. The connect-time ListModels reconcile clears a now-unavailable provider
	// before the create carries it (see ui.Init / updateModelsMsg).
	store := newSelectionStore(xdgconfig.OSEnv)
	initialSel, wsDefault, wsDefaultSet, globalDefault := launchSelections(store, uiWorkspace, cfg.debugTarget)
	defer func() { _ = statusSource.Close(context.Background()) }()

	connectionMode := resolveConnectionMode(cfg)
	deps := applyLaunchIntent(cfg, ui.Deps{
		Session:                &sessionAdapter{cl: cl, mode: cfg.mode, debugTarget: cfg.debugTarget, debugMCP: cfg.debugMCP},
		Conv:                   cl,
		MCP:                    cl,
		Cmds:                   cl,
		Skills:                 cl,
		Agents:                 cl,
		Soul:                   cl,
		UserModel:              cl,
		Reflections:            cl,
		Dream:                  cl,
		Models:                 cl,
		Worktrees:              cl,
		Sched:                  cl,
		Sessions:               cl,
		StorageHealth:          cl,
		Migration:              cl,
		Cleanup:                cl,
		SessionManagement:      cl,
		Transcript:             cl,
		Replayer:               cl,
		LiveStream:             cl,
		SelectionStore:         store,
		Learning:               learningSettingsForConfig(cfg),
		Connect:                savedConnectController{},
		ConnectOpen:            options.connectOpen,
		ConnectError:           options.connectError,
		ConnectReason:          options.connectReason,
		ConnectTarget:          options.connectTarget,
		ConnectResumeSessionID: options.connectResumeSessionID,
		BearerBacked:           dial.AuthToken != "" || dial.TokenSource != nil,
		InitialModel:           initialSel,
		WorkspaceDefault:       wsDefault,
		WorkspaceDefaultSet:    wsDefaultSet,
		GlobalDefault:          globalDefault,
		Clipboard:              client.NewClipboard(),
		Theme:                  th,
		ThemeAutoDetect:        themeAutoDetect,
		StatusSource:           statusSource,
		LocalSessionContext:    cl,
		Server:                 target,
		ConnectionMode:         connectionMode,
		ClientBuild:            buildinfo.BuildID,
		Embedded:               cfg.transportMode == modeLocal,
		// Model is best-effort display only. For an EXTERNAL --server it reflects
		// the locally-configured --model flag and may NOT match the server's actual
		// model (the server owns provider config); for an embedded server it is
		// authoritative. The footer context-meter denominator is the SERVER-resolved
		// per-model window echoed on session create (and refreshed on GetSession), now
		// live/config-first server-side — never recomputed by the client. Embedded mode's
		// --context-window-override and an external mecated's flag both move the engine
		// trigger and this echoed denominator.
		Model:     cfg.model,
		Workspace: uiWorkspace,
		Mode:      cfg.mode,
		Resume:    resume,
		Ctx:       ctx,
		// Build identity for the welcome splash (explicit linker stamp, or a
		// VCS-derived source-build ID when embedded metadata is available).
		Version: buildinfo.BuildID,
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
		// Dynamic terminal window/tab title: off collapses to bare "mecatui".
		// Default false (dynamic: "<title> — <status word> mecatui").
		NoWindowTitle: cfg.terminalTitleOff,
		// Seed prompt from -p/--prompt + --prompt-file: joined at startup and
		// auto-submitted once the first session is ready (interactive-seed, NOT a
		// one-shot — the TUI stays open for follow-ups). Empty = no seed.
		InitialPrompt: initialPromptForConfig(cfg),
		DebugTarget:   cfg.debugTarget,
	})
	applyDebugConfig(cfg, &deps)
	deps.ServerImpl = mecatuiServerImplementation
	wireManualCompaction(&deps, cl)
	deps.MCPAuthorization = cl
	deps.WorkspaceEnrollment = cl
	deps.OpenURL = openBrowserURL

	// Apply keymap overrides (CLI for now).
	if err := applyKeyOverridesToDeps(cfg, &deps); err != nil {
		_ = cl.Close()
		transCleanup()
		return err
	}

	prog := tea.NewProgram(ui.New(deps), tea.WithContext(ctx))
	finalModel, runErr := prog.Run()
	interrupted := ctx.Err() != nil

	runCleanup(forceExit, func() {
		_ = cl.Close()
		transCleanup()
	})
	if intent, ok := connectRestartIntent(finalModel); ok {
		return restartFromConnectIntent(argv, intent, restartTransport{Target: target, TLSCAFile: cfg.tlsCA})
	}
	if shouldWriteFinalSessionHandoff(finalModel, runErr, interrupted) {
		writeFinalSessionHandoff(os.Stderr, finalModel)
	}
	return runErr
}

func applyDebugConfig(cfg config, deps *ui.Deps) {
	deps.Debug = cfg.debug
	deps.DebugMouse = cfg.debugMouse
	deps.DebugSteer = cfg.debugSteer
	deps.DebugAsk = cfg.debugAsk
}

// parseRunConfig resolves the transport-independent flags, then applies the
// target-aware workspace authority rule before any transport is dialed.
func parseRunConfig(res invocationResolution) (config, error) {
	_, cfg, err := parseTransportFlags(res.mode, os.Stderr, res.remaining, res.browseSessions)
	if err != nil {
		return config{}, err
	}
	cfg.connectAddress = res.address
	cfg.debugTarget = res.debugTarget
	if err := resolveRemoteTLSPolicy(&cfg); err != nil {
		return config{}, err
	}
	if err := configureWorkspaceForTransport(&cfg); err != nil {
		return config{}, err
	}
	return cfg, nil
}

func runTestSignalHandler() error {
	if v := os.Getenv("MECATUI_TEST_SIGNAL_HANDLER"); v != "" {
		return testSignalHandler(v)
	}
	return nil
}

func runSpecialMode(res invocationResolution) (bool, error) {
	// Login routes are intentionally separate from transport setup.
	switch res.mode {
	case modeProviderStatus:
		return true, runProviderStatusCommand(res, os.Stdout, os.Stderr)
	case modeLogin:
		return true, runLLMCommand(res)
	case modeLLMConfig:
		return true, runLLMConfigCommand(res, os.Stdout, os.Stderr)
	case modeRemoteLogin:
		return true, runRemoteLogin(res.address, res.remaining)
	case modeRemoteLogout:
		return true, runRemoteLogout(res.address, res.remaining)
	default:
		return false, nil
	}
}

func warnEmbeddedPosture(cfg config) {
	if !cfg.mayEmbed() {
		return
	}
	switch embeddedAuthoritativePosture(cfg) {
	case app.PostureAuto:
		fmt.Fprintln(os.Stderr, "mecatui: WARNING: posture auto is active; allow-all is ON for the embedded server (the built-in mutate-ask floor + the MAIN agent's substitution floor are waived). A Deny in any scope and any configured Ask still apply. The CHILD prompt-injection defense stays ON. For unattended single-tenant use.")
	case app.PostureYolo:
		fmt.Fprintln(os.Stderr, "mecatui: WARNING: posture yolo is active; allow-all is ON AND the CHILD prompt-injection defense is OFF — $()/backtick/heredoc commands AUTO-RUN in subagents/branches. A Deny in any scope and any configured Ask still apply. ISOLATED, SINGLE-TENANT use ONLY. NOTE: --yolo now ALSO loosens the child substitution floor.")
	}
}

func shouldWriteFinalSessionHandoff(final tea.Model, runErr error, interrupted bool) bool {
	if _, restarting := connectRestartIntent(final); restarting {
		return false
	}
	return runErr == nil && !interrupted
}

func connectRestartIntent(final tea.Model) (ui.ConnectRestartIntent, bool) {
	reporter, ok := final.(interface {
		ConnectRestartIntent() (ui.ConnectRestartIntent, bool)
	})
	if !ok {
		return ui.ConnectRestartIntent{}, false
	}
	return reporter.ConnectRestartIntent()
}

// resolveThemeAutoDetect decides whether the light/dark terminal-background
// auto-detect (ADR 0280) should be armed for this launch: only when no
// explicit --theme/MECATUI_THEME was given (finalizeParsedConfig resolves both
// into cfg.theme, so an empty value means neither was given) AND stdout is a
// real terminal — never on redirected/piped output, which must never see the
// OSC background-colour query escape. Extracted as a pure function (stdout's
// TTY-ness passed in, not read here) so the gate's logic is unit-testable
// without a real terminal.
func resolveThemeAutoDetect(cfg config, stdoutIsTTY bool) bool {
	return cfg.theme == "" && stdoutIsTTY
}

func runDisconnectedRecovery(ctx context.Context, argv []string, th theme.Theme, themeAutoDetect bool, options runOptions) error {
	deps := ui.Deps{Ctx: ctx, Theme: th, ThemeAutoDetect: themeAutoDetect, Connect: savedConnectController{}, ConnectOpen: true, ConnectError: options.connectError, ConnectReason: options.connectReason, ConnectTarget: options.connectTarget, ConnectResumeSessionID: options.connectResumeSessionID}
	prog := tea.NewProgram(ui.New(deps), tea.WithContext(ctx))
	finalModel, runErr := prog.Run()
	if intent, ok := connectRestartIntent(finalModel); ok {
		return restartFromConnectIntent(argv, intent, options.connectTransport)
	}
	return runErr
}

func shouldAdoptAuthRecoveryCandidate(resume *client.ResumeSelection, err error) bool {
	return err == nil && resume != nil && safeAuthRecoveryState(resume.Snapshot.State)
}

func safeAuthRecoveryState(state string) bool {
	switch state {
	case "completed", "cancelled", "failed":
		return true
	default:
		return false
	}
}

func authRecoveryOptions(reason client.AuthReason, target, message, candidate string, transport restartTransport) runOptions {
	return runOptions{connectOpen: true, connectError: message, connectReason: reason, connectTarget: target, connectResumeSessionID: candidate, connectTransport: transport, recoveryOnly: true}
}

type connectRestartOps struct {
	run          func([]string, runOptions) error
	connection   func(string) (clientauth.Connection, error)
	login        func(context.Context, clientauth.Connection, bool) error
	loginContext func(time.Duration) (context.Context, context.CancelFunc)
}

func restartFromConnectIntent(argv []string, intent ui.ConnectRestartIntent, transport restartTransport) error {
	return restartFromConnectIntentWith(argv, intent, transport, defaultConnectRestartOps())
}

// defaultConnectRestartOps is the production connectRestartOps wiring,
// factored out so a test can assert exactly which functions it wires (e.g.
// that Reauthenticate uses the existing-only login, never the creating one)
// without invoking the real run/login side effects.
func defaultConnectRestartOps() connectRestartOps {
	return connectRestartOps{
		run:          runWithOptions,
		connection:   savedConnection,
		login:        runExistingSavedRemoteLogin,
		loginContext: newSavedLoginContext,
	}
}

func connectRecoveryReason(action ui.ConnectAction) client.AuthReason {
	switch action {
	case ui.RetryAfterCleanup:
		return client.AuthCredentialCleanup
	case ui.Reauthenticate:
		return client.AuthSessionExpired
	default:
		return ""
	}
}

func restartFromConnectIntentWith(argv []string, intent ui.ConnectRestartIntent, transport restartTransport, ops connectRestartOps) error {
	resumeSessionID := ""
	recoveryReason := connectRecoveryReason(intent.Action)
	sameTarget := transport.Target != "" && transport.Target == intent.Target
	selectedTransport := restartTransport{Target: intent.Target}
	if sameTarget {
		selectedTransport.TLSCAFile = transport.TLSCAFile
	}
	// Recovery authority is target-bound at composition, independently of the UI.
	// A malformed reporter cannot carry recovery semantics or a session candidate
	// onto a different target; treat it as an ordinary saved connection instead.
	if !sameTarget && (intent.Action == ui.RetryAfterCleanup || intent.Action == ui.Reauthenticate) {
		intent.Action = ui.ConnectSaved
		recoveryReason = ""
	}
	switch intent.Action {
	case ui.AddTarget:
		return ops.run([]string{argv[0]}, runOptions{connectOpen: true, connectError: "Add a target with mecatui login ADDRESS, then select it here.", recoveryOnly: true})
	case ui.ConnectSaved:
		// Ordinary saved connects are intentionally browser-free and never carry a
		// recovery candidate, even if a malformed reporter supplied one.
	case ui.RetryAfterCleanup:
		// Cleanup retries are browser-free but retain the same-target candidate.
		resumeSessionID = intent.ResumeSessionID
	case ui.Reauthenticate:
		resumeSessionID = intent.ResumeSessionID
		conn, err := ops.connection(intent.Target)
		if err == nil {
			ctx, cancel := ops.loginContext(savedLoginCallbackTimeout)
			// ADR 0271: the recovery overlay never opens a browser. This is
			// unconditional -- not read from the intent -- so no producer of
			// ConnectRestartIntent can put the process back in the browser
			// path for a reauthentication restart.
			err = ops.login(ctx, conn, true)
			cancel()
		}
		if err != nil {
			if reason, ok := client.AuthFailure(err, false); ok {
				recoveryReason = reason
			}
			return ops.run([]string{argv[0]}, runOptions{connectOpen: true, connectError: "Sign in failed; check the saved target and try again.", connectReason: recoveryReason, connectTarget: intent.Target, connectResumeSessionID: resumeSessionID, connectTransport: selectedTransport, recoveryOnly: true})
		}
	default:
		return fmt.Errorf("invalid connect action %d", intent.Action)
	}

	connectArgv := []string{argv[0], "connect", intent.Target, "--tls"}
	if selectedTransport.TLSCAFile != "" {
		connectArgv = append(connectArgv, "--tls-ca", selectedTransport.TLSCAFile)
	}
	err := ops.run(connectArgv, runOptions{connectResumeSessionID: resumeSessionID})
	if err != nil {
		return ops.run([]string{argv[0]}, runOptions{connectOpen: true, connectError: "Connection failed; check the saved target and try again.", connectReason: recoveryReason, connectTarget: intent.Target, connectResumeSessionID: resumeSessionID, connectTransport: selectedTransport, recoveryOnly: true})
	}
	return nil
}

// applyLaunchIntent threads command-derived launch state into the ui at the
// composition boundary. Keeping this projection separate makes the command path
// testable without starting a transport or a Bubble Tea program.
func applyLaunchIntent(cfg config, deps ui.Deps) ui.Deps {
	deps.BrowseSessions = cfg.browseSessions
	return deps
}

const defaultDebugPrompt = "Diagnose the bound target session and explain the most likely cause of its reported behavior."

func initialPromptForConfig(cfg config) string {
	if prompt := cliconfig.JoinPromptBody(cfg.prompt, cfg.promptFileBody); strings.TrimSpace(prompt) != "" {
		return prompt
	}
	if cfg.debugTarget != "" {
		if len(cfg.debugMCP) > 0 {
			return defaultDebugPrompt + " Selected reporting servers are available: " + strings.Join(cfg.debugMCP, ", ") + ". Their availability does not authorize publication or sending."
		}
		return defaultDebugPrompt
	}
	return ""
}

func launchSelections(store *selectionStore, workspace, debugTarget string) (client.ModelSelection, client.ModelSelection, bool, client.ModelSelection) {
	if debugTarget != "" {
		return client.ModelSelection{}, client.ModelSelection{}, false, client.ModelSelection{}
	}
	initial := store.Load(workspace)
	workspaceDefault, set := store.LoadWorkspace(workspace)
	return initial, workspaceDefault, set, store.LoadGlobalDefault()
}

func emitDebugPrivacyWarning(w io.Writer, target string, servers ...string) {
	if target != "" {
		_, _ = fmt.Fprintln(w, "mecatui: PRIVACY: bounded evidence from the target session sent to the configured model may include prompts, assistant output, tool arguments and results, file paths, and secrets")
		if len(servers) > 0 {
			_, _ = fmt.Fprintf(w, "mecatui: PRIVACY: selected reporting servers available to this debug session: %s\n", strings.Join(servers, ", "))
		}
	}
}

// openBrowserURL opens only a presentation URL obtained from the authenticated
// server control surface. The URL is an argument, never a shell fragment.
func openBrowserURL(_ context.Context, url string) error {
	var name string
	switch runtime.GOOS {
	case "darwin":
		name = "open"
	case "windows":
		name = "rundll32"
		return exec.Command(name, "url.dll,FileProtocolHandler", url).Start()
	default:
		name = "xdg-open"
	}
	return exec.Command(name, url).Start()
}

// emitAuthFileWarning is the command-root's single warning emission seam.
// Credential resolution may aggregate multiple safe findings into one string;
// the TUI still emits at most one pre-alt-screen line and embeddedConfig remains
// a pure projection with no logging side effect.
func emitAuthFileWarning(writer io.Writer, warning string) {
	if warning != "" {
		_, _ = fmt.Fprintln(writer, "mecatui: WARNING:", warning)
	}
}

// setupSignalHandler installs the manual two-signal handler: first signal =
// graceful shutdown (cancels the returned ctx → Bubble Tea quits); a second
// signal before cleanup completes = immediate hard os.Exit(130). It returns the
// ctx plus a forceExit channel the caller closes ONLY after the post-Run cleanup
// has finished (see runCleanup) to retire the handler on the normal path.
//
// The signal goroutine is the SOLE owner of sigCh (signal.Notify's channel): no
// other code reads it, and NOTHING calls signal.Stop on the force-exit path, so
// a second SIGINT/SIGTERM delivered while the post-Run cleanup is still running
// is ALWAYS received and drives the immediate os.Exit(130). That is the
// criterion-6 contract — a second signal during graceful shutdown forces a hard
// exit. It must never be swallowed: a signal.Stop, or a second consumer draining
// the channel, would silently revert that second Ctrl+C to the OS default
// disposition and wedge the shutdown it exists to interrupt. To keep that
// deterministic, forceExit is closed only AFTER cleanup finishes — never while
// the goroutine is still parked listening for the second signal — so there is no
// select race between "second signal" and "cleanup done" mid-cleanup.
func setupSignalHandler() (context.Context, chan struct{}) {
	sigCh := make(chan os.Signal, 2)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	ctx, cancel := context.WithCancel(context.Background())

	forceExit := make(chan struct{})
	go func() {
		defer signal.Stop(sigCh)
		defer cancel()
		// First: graceful signal, or clean quit before any signal.
		select {
		case sig := <-sigCh:
			fmt.Fprintf(os.Stderr, "mecatui: received %s, shutting down gracefully (press again to force exit)...\n", sig)
			cancel()
		case <-forceExit:
			return
		}
		// Past the first signal: keep listening for the SECOND signal through the
		// whole cleanup. forceExit closes only after cleanup completes, so while
		// cleanup is still running a second signal deterministically wins (it is
		// the only ready case) and hard-exits; once cleanup is done, forceExit
		// retires the handler. A signal buffered before forceExit closes is still
		// honoured — the select prefers neither, but forceExit is not closed until
		// cleanup returns, so a signal sitting in sigCh during cleanup is read
		// first.
		select {
		case <-sigCh:
			fmt.Fprintln(os.Stderr, "mecatui: forcing immediate exit")
			os.Exit(130)
		case <-forceExit:
		}
	}()
	return ctx, forceExit
}

// runCleanup runs the post-Run cleanup under a 45s hard deadline, retiring the
// signal handler (close(forceExit)) only AFTER cleanup completes — never during
// it — so a second signal delivered mid-cleanup still reaches the parked signal
// goroutine and hard-exits (criterion 6). The embed Close → GracefulStop path is
// already bounded to ~40s by tasks #1+#2, so 45s normally lets it complete; on
// timeout the process exits 1 rather than hang (the signal goroutine stays armed
// the whole time, so an operator Ctrl+C also force-exits a wedged cleanup).
func runCleanup(forceExit chan struct{}, cleanup func()) {
	cleanupDone := make(chan struct{})
	go func() {
		defer close(cleanupDone)
		cleanup()
	}()

	select {
	case <-cleanupDone:
	case <-time.After(45 * time.Second):
		fmt.Fprintln(os.Stderr, "mecatui: cleanup timeout; exiting")
		os.Exit(1)
	}
	close(forceExit) // retire the signal handler AFTER cleanup, not during it
}

func wireManualCompaction(deps *ui.Deps, compactor client.SessionCompactor) {
	deps.Compactor = compactor
}

// testSignalHandler is the MECATUI_TEST_SIGNAL_HANDLER test seam: a minimal
// signal-handler path with no TUI or server. Valid modes:
//
//	"first"  — block until one signal, print the graceful-shutdown line, return nil.
//	"second" — spawn a goroutine that fires os.Exit(130) on the second signal;
//	           the caller blocks forever so the goroutine's os.Exit terminates the process.
func testSignalHandler(mode string) error {
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sigCh)

	// Readiness handshake: print AFTER signal.Notify so the parent test knows the
	// handler is installed before it signals (a blind sleep races -race startup).
	fmt.Fprintln(os.Stderr, "mecatui: signal-handler ready")

	switch mode {
	case "first":
		sig := <-sigCh
		fmt.Fprintf(os.Stderr, "mecatui: received %s, shutting down gracefully (press again to force exit)...\n", sig)
		return nil
	case "second":
		go func() {
			sig := <-sigCh
			fmt.Fprintf(os.Stderr, "mecatui: received %s, shutting down gracefully (press again to force exit)...\n", sig)
			<-sigCh
			fmt.Fprintln(os.Stderr, "mecatui: forcing immediate exit")
			os.Exit(130)
		}()
		select {} // block forever; the goroutine's os.Exit terminates the process
	default:
		return fmt.Errorf("mecatui: unknown MECATUI_TEST_SIGNAL_HANDLER value: %q", mode)
	}
}

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

// resolveTransport decides how mecatui reaches a server and returns the dial
// target (for display + connection), the client.DialConfig to dial it with, and a
// cleanup func to defer (a no-op unless an embedded server was started).
//
//   - `mecatui connect ADDRESS`: dial ADDRESS with the TLS/auth flags (never
//     probes, never embeds).
//   - bare `mecatui` (modeLocal): host an embedded server over a UNIX socket
//     (never probes loopback).
//
//nolint:gocyclo // composition root resolves the mutually exclusive remote and embedded transports.
func resolveTransport(ctx context.Context, cfg config) (target string, dial client.DialConfig, cleanup func(), err error) {
	noop := func() {}

	// The two modes are PURE (ADR 0087): `mecatui connect ADDRESS` ALWAYS dials
	// ADDRESS and NEVER probes/embeds; the bare invocation ALWAYS embeds and
	// NEVER probes loopback.
	if cfg.transportMode == modeConnect {
		dial := client.DialConfig{Server: cfg.connectAddress, AuthToken: cfg.authToken, ExplicitAnonymous: cfg.anonymous, UseTLS: cfg.useTLS, TLSCAFile: cfg.tlsCA, Insecure: cfg.insecure, RemotePlaintextAllowed: cfg.tlsExplicit && !cfg.useTLS}
		if cfg.authToken == "" && !cfg.anonymous {
			root := filepath.Join(xdg.ConfigHome, "mecatl")
			registry, regErr := clientauth.OpenExistingRegistry(root)
			if regErr != nil {
				return target, client.DialConfig{}, noop, &client.AuthError{Reason: client.AuthStorageUnavailable}
			}
			conn, findErr := registry.Find(cfg.connectAddress)
			if findErr == nil {
				target = conn.Identity.Target
				dial.Server = target
				if err := applySavedRemoteTLSPolicy(cfg, &dial); err != nil {
					return target, client.DialConfig{}, noop, err
				}
				var ca []byte
				var readErr error
				if conn.IssuerCAFile != "" {
					ca, readErr = os.ReadFile(conn.IssuerCAFile)
				}
				if readErr != nil {
					return target, client.DialConfig{}, noop, &client.AuthError{Reason: client.AuthStorageUnavailable}
				}
				store, _, storeErr := clientauth.OpenExistingCredentialStore(ctx, root)
				if storeErr != nil {
					return target, client.DialConfig{}, noop, &client.AuthError{Reason: client.AuthStorageUnavailable}
				}
				creds, credsErr := clientauth.NewCredentials(store)
				if credsErr != nil {
					_ = store.Close()
					return target, client.DialConfig{}, noop, &client.AuthError{Reason: client.AuthStorageUnavailable}
				}
				if _, loadErr := creds.Load(ctx, conn.Identity); loadErr != nil {
					_ = store.Close()
					if errors.Is(loadErr, credentialstore.ErrNotFound) {
						// The registry entry above proves this target IS enrolled, so an
						// absent credential means the stored one is gone -- typically
						// deleted after a provider rejected its refresh. NotEnrolled
						// belongs to the FindTarget miss below, not here. clientauth
						// remaps this within one process lifetime; across a restart that
						// memory is gone and only the registry can tell them apart.
						return target, client.DialConfig{}, noop, &client.AuthError{Reason: client.AuthSessionExpired}
					}
					if errors.Is(loadErr, clientauth.ErrCorrupt) {
						return target, client.DialConfig{}, noop, &client.AuthError{Reason: client.AuthCredentialUnusable}
					}
					return target, client.DialConfig{}, noop, &client.AuthError{Reason: client.AuthStorageUnavailable}
				}
				source, sourceErr := clientauth.NewRefreshSource(ctx, creds, clientauth.LoginConfig{Identity: conn.Identity, IssuerAddressPolicy: conn.IssuerAddressPolicy, TrustedCAPEM: ca, Registry: registry})
				if sourceErr != nil {
					_ = store.Close()
					// NewRefreshSource fails with ErrDiscovery when the issuer is
					// unreachable, its TLS is untrusted, or JWKS will not load --
					// an infrastructure/network problem, not evidence the local
					// keyring/registry/store is broken. Return it unwrapped so it
					// falls through AuthFailure's deliberate unclassified case
					// instead of steering the user toward local-storage recovery.
					if errors.Is(sourceErr, clientauth.ErrDiscovery) {
						return target, client.DialConfig{}, noop, sourceErr
					}
					return target, client.DialConfig{}, noop, &client.AuthError{Reason: client.AuthStorageUnavailable}
				}
				dial.TokenSource = mapAuthTokenSource(source)
				return target, dial, func() { _ = source.Close(); _ = store.Close() }, nil
			}
			if errors.Is(findErr, credentialstore.ErrNotFound) {
				// A clean registry miss is not an authentication decision. The server
				// remains authoritative: dial without a bearer and recover only if it
				// actually returns Unauthenticated. Registry/storage errors still fail
				// closed.
				return cfg.connectAddress, dial, noop, nil
			}
			return target, client.DialConfig{}, noop, &client.AuthError{Reason: client.AuthStorageUnavailable}
		}
		return cfg.connectAddress, dial, noop, nil
	}

	// We are about to HOST an embedded server (the bare/local mode).
	// This is the pre-TUI window (before the Bubble Tea alt screen starts) where the
	// first-encounter workspace-trust prompt belongs (Workspace-Trust Phase 2c): if
	// the workspace is not already trusted but carries a project authority set (or a
	// remembered entry that DRIFTED), prompt the operator. The outcome feeds
	// cfg.trustProject so embeddedConfig → app.Build honours it WITHOUT re-resolving
	// or re-prompting. Only the embedded server is gated; a connect-mode server
	// (handled above) owns its own declarative trust. See cmd/mecatui/trust.go.
	// Open the embedded server's diagnostics sink ONCE, here in the host-an-embedded
	// branch. It is a file under $XDG_STATE_HOME/mecatl/mecatui.log (fallback
	// ~/.local/state/...), or io.Discard under --quiet / on any open failure — NEVER
	// stderr, which would corrupt the Bubble Tea alt-screen. The same writer backs
	// BOTH the app.Diagnostics sink and the perf surface's slog.Logger, so neither
	// path leaks a line to the terminal. The file handle (when one was opened) is
	// closed by the returned cleanup alongside the server.
	diagW, diagCloser, toFile := openDiagLogWriter(xdgconfig.OSEnv, cfg.quiet, cfg.diagnosticsLog)
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

	composition := embeddedConfig(cfg, diag)
	srv, err := embed.Start(ctx, composition, perfConfig(cfg, perfLogger))
	if err != nil {
		_ = diagCloser.Close()
		return target, client.DialConfig{}, noop, fmt.Errorf("start embedded server: %w", err)
	}
	if toFile {
		// One line, written to the FILE sink (never the TUI), so an operator can find
		// where the embedded server's diagnostics went.
		diag.Log(ctx, port.LevelInfo, "mecatui: embedded server diagnostics log opened",
			"path", resolveDiagLogPath(xdgconfig.OSEnv))
	}
	fmt.Fprintf(os.Stderr, "mecatui: hosting an embedded mecated at %s\n", srv.Target())
	if addr := srv.AdminAddr(); addr != "" {
		paths := "/metrics /debug/pprof /debug/vars /debug/flightrecorder"
		if cfg.perfMCP {
			paths += " /mcp"
		}
		if srv.AdminNetwork() == "unix" {
			fmt.Fprintf(os.Stderr, "mecatui: perf admin surface (private UNIX socket, UNAUTHENTICATED) at %s — %s\n", addr, paths)
		} else {
			fmt.Fprintf(os.Stderr, "mecatui: perf admin surface (loopback, UNAUTHENTICATED) at http://%s — %s\n", addr, paths)
			if cfg.perfMCP {
				fmt.Fprintf(os.Stderr, "mecatui: perf MCP ready at http://%s/mcp — point an MCP client here\n", addr)
			}
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

// mecatuiServerImplementation is the stable family of the embedded server.
const mecatuiServerImplementation = "mecatui"

// embeddedConfig constructs the embedded server's declarative app.Config. app.Build
// loads the injected provider credential; connect mode never calls this function.
func embeddedConfig(cfg config, diag port.Diagnostics) app.Config {
	cmdDir, enableCmds := resolveCommands(cfg)
	skillDirs, skillsConv := resolveSkills(cfg)
	nativeEndpointLoader := &cliconfig.NativeEndpointLoader{}
	out := app.Config{
		Workspace:            cfg.workspace,
		ServerImplementation: mecatuiServerImplementation,
		Model:                cfg.model,
		DefaultProvider:      cfg.defaultProvider,
		DefaultModel:         cfg.defaultModel,
		// defaultProviderFlagSet lets CLI out-rank the operator-global settings.yaml
		// models.default_provider: key (folded by foldOperatorDefaultProvider in app.Build).
		DefaultProviderFlagSet: cfg.defaultProviderFlagSet,
		SubagentModel:          cfg.subagentModel,
		// Per-slot models (ADR 0030): the mecated flags mirror, mapped verbatim.
		// The *cliconfig.KeyValueList flag bindings are converted to the plain
		// map[string]string app.Config expects (nil for an unset flag).
		ModelAliases: cfg.modelAliases.AsMap(),
		ModelSlots:   cfg.modelSlots.AsMap(),
		// Subagent model router (ADR 0042): kill-switch. =false forces the router OFF
		// (RouterDisabled); a bare flag / =true is a harmless no-op (the router stays
		// governed by the taxonomy); unset leaves routing governed by the operator-tier
		// models.router: taxonomy. Idempotent: safe to compute on both calls.
		RouterDisabled:        cfg.subagentModelRouterSet && !cfg.subagentModelRouter,
		UseMock:               cfg.mock,
		Shell:                 "/bin/sh",
		NoShell:               cfg.noShell,
		Compaction:            "heuristic",
		Tokenizer:             "heuristic",
		LLMMaxAttempts:        3,
		LLMPerAttemptTimeout:  cfg.llmPerAttemptTimeout,
		LLMStreamIdleTimeout:  cfg.llmStreamIdleTimeout,
		ContextWindowOverride: cfg.contextWindowOverride,
		PromptCacheDisabled:   cfg.noPromptCache,
		AnthropicCacheTTL:     cfg.anthropicCacheTTL,
		LLMBreakerThreshold:   5,
		LLMBreakerCooldown:    30 * time.Second,
		EnableParallel:        true,
		EnableTeams:           true,
		AgentsConventional:    true,
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
		// Scheduled tasks ON by default (ADR 0073 decision 2, AC2.4): the TUI
		// inherits the on-by-default scheduler (the same !--no-scheduler fold the
		// mecated cmd feeds), so the embedded server ticks and a due schedule
		// auto-fires with no flag — the /schedule overlay's in-chat and manual
		// fires work out of the box. On the default per-workspace jsonlstore the
		// store backs a ScheduleStore, so the scheduler engages; on --no-store
		// (the in-memory store) buildScheduler reconciles to the byte-identical
		// inert path (AC2.3). The 30s tick is the scheduler's own default
		// (SchedulerTickInterval left 0).
		SchedulerEnabled: true,
		// Session retention GC (issues #38 + #79). With a DURABLE default store the
		// child snapshots (subagent/parallel/team) AND the top-level main-session
		// snapshots accumulate on disk, so a LONG-LIVED TUI process otherwise grows
		// without bound. Free and local, so on by default. The child knobs are
		// mecated's defaults; the main knobs (issue #79) bound the durable store the
		// TUI now defaults on — a 30-day age horizon plus a 200-session global cap, so
		// recent history is recoverable but stale sessions are reaped. A live run is
		// always skipped.
		// The effective local policy is explicit in flags/operator settings; main
		// deletion defaults off and requires acknowledgement when enabled.
		ChildRetention:                cfg.childRetention,
		ChildRetentionMaxPerFamily:    cfg.childRetentionCount,
		MainRetention:                 cfg.mainRetention,
		MainRetentionMaxTotal:         cfg.mainRetentionCount,
		ScheduleFireRetention:         cfg.scheduledRetention,
		ScheduleFireRetentionMaxTotal: cfg.scheduledRetentionCount,
		ChildGCInterval:               cfg.retentionSweepCadence,
		RetentionCLISet:               cfg.retentionCLISet,
		AcknowledgeMainRetention:      cfg.acknowledgeMainRetention,
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
		// answer). This is why the headless ask reviewer is a mecated-only flag
		// (ADR 0089 removed the inert --subagent-ask-reviewer* flags from mecatui:
		// the modal always sees the ask, so the reviewer never engages here — run
		// a headless `mecated --headless --subagent-ask-reviewer …` and point
		// `mecatui connect` at it to use the reviewer).
		Interactive: true,
		// mecatui is interactive, so trusted/auto/yolo retain the developer
		// workspace-trust floor.
		Headless: false,
		// Steer (steer-while-running, issue #512): the opt-OUT of the default-ON
		// mid-run inbox on the embedded server. noSteerFlagSet lets CLI out-rank the
		// settings.yaml steer: key.
		DisableSteer:        cfg.noSteer,
		DisableSteerFlagSet: cfg.noSteerFlagSet,
		// Diagnostics is the injected file-backed (or, under --quiet, discarding) sink.
		// It is NEVER stderr: an operational line on stderr corrupts the Bubble Tea
		// alt-screen. The caller (resolveTransport) opens the sink once over
		// $XDG_STATE_HOME/mecatl/mecatui.log and threads it here AND into the perf
		// Logger, so both land in the same file rather than the terminal. A nil diag
		// (e.g. the pre-embed trust-prompt fold) is tolerated — app.Build defaults it to
		// NopDiagnostics.
		Diagnostics: diag,
	}
	// Apply the credentials resolved at parse time. Reusing the result keeps
	// startup validation and embedded composition on one auth-file read.
	keys := cfg.providerKeys
	if !cfg.providerKeysResolved {
		keys = cfg.providerFlags.Resolve()
	}
	cfg.providerFlags.ApplyResolved(&out, keys)
	out.UseOpenAI = keys.OpenAI != ""
	cfg.toolhiveLLMFlags.Apply(&out)
	// Operator-tier MCP profiles use the canonical authority resolver (global vs
	// broker) rather than MCPProfileLoader.Load's global-mode-only path. The
	// embedded server supports broker construction while retaining the existing
	// profile loader for compatibility with consumers that use it directly.
	out.MCPProfileLoader = cliconfig.NewMCPProfileResolver(nil, os.LookupEnv)
	out.MCPAuthorityLoader = cliconfig.NewMCPProfileResolver(nil, os.LookupEnv)
	out.MCPAuthorityDefault = mcpauthority.Global
	out.MCPBrokerSupported = true
	out.ProviderCredentialLoader = cliconfig.NewProviderCredentialResolver(cfg.providerFlags, keys)
	out.NativeEndpointCredentialLoader = nativeEndpointLoader
	out.NativeEndpointCredentialLifecycle = nativeEndpointLoader
	out.ProviderOverrides = cfg.providerFlags.EndpointOverrides()
	return out
}

// perfConfig maps the TUI config onto the embedded server's perf-observability
// options (decision 7). It is OFF unless --perf is passed. Plain perf defaults
// to a private per-instance UNIX socket; --perf-mcp defaults to resolved ephemeral
// loopback TCP, and an explicit address selects loopback TCP for either mode.
// Logger uses the same file-backed (or, under --quiet, discarding) Diagnostics sink
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

// sessionAdapter bridges the ui's SessionCreator to the path-free client API.
// Embedded workspace configuration is consumed by app.Build; no session request
// carries it. Mode and model selection remain per-call.
type sessionAdapter struct {
	cl          *client.Client
	mode        string
	debugTarget string
	debugMCP    []string
}

func (s *sessionAdapter) CreateSession(ctx context.Context, sel client.ModelSelection, mode string) (string, client.Capabilities, client.ResolvedModel, error) {
	if s.debugTarget != "" {
		if mode == "" {
			mode = s.mode
		}
		id, target, caps, resolved, err := s.cl.CreateDebugSession(ctx, s.debugTarget, client.ModeFromString(mode), sel, s.debugMCP...)
		if target != "" {
			s.debugTarget = target
		}
		return id, caps, resolved, err
	}
	return s.cl.CreateSession(ctx, client.ModeFromString(mode), sel)
}

func (s *sessionAdapter) DebugTargetID() string { return s.debugTarget }

// CreateSessionWithCarryover implements the ui SessionCreator's carryover seam
// (issue #20): like CreateSession it carries the pick + mode, but it ALSO sets
// source_session_id so the server seeds the new session's history from the source.
// The ui offers the switch unconditionally when a live session exists; the server
// is the authority on same-vs-cross (same-provider replays verbatim, cross-provider
// strips the prior provider's replay blobs via StripProviderState). Best-effort
// CloseSession of the source is the CALLER's job (after the new session is ready).
func (s *sessionAdapter) CreateSessionWithCarryover(ctx context.Context, sourceSessionID string, sel client.ModelSelection) (string, client.Capabilities, client.ResolvedModel, error) {
	return s.cl.CreateSessionWithCarryover(ctx, sel, sourceSessionID)
}

func (s *sessionAdapter) ClearSession(ctx context.Context, sourceID string, selector *client.WorktreeSelector) (string, client.SessionSnapshot, error) {
	return s.cl.ClearSession(ctx, sourceID, selector)
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

// ForkSession implements the ui SessionCreator's fork seam (ADR 0068): the title
// stays inherited ("") — the /effort fork-resume switches effort ONLY, so the fork
// keeps the source's title, provider, and model.
func (s *sessionAdapter) ForkSession(ctx context.Context, srcID, reasoningEffort string) (string, error) {
	return s.cl.ForkSession(ctx, srcID, "", reasoningEffort)
}
