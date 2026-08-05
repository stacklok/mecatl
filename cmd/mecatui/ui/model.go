// Package ui is the Bubble Tea (Elm) layer of mecatui: the root Model state
// machine, its Update reducer, the View assembly, and the conversation/block
// renderers. It imports only the client and theme packages (plus charm libs and
// stdlib) — never contracts/gen or any internal/... package — so it renders
// purely from the plain msg structs the client layer translates proto Events
// into. All glamour rendering happens here on the single update goroutine.
package ui

import (
	"context"

	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/theme"
)

// SessionCreator creates a server-side session and returns its id together with
// the server's advertised capabilities. *client.Client satisfies it (via the
// sessionAdapter); tests supply a fake. Keeping it an interface lets the ui be
// driven entirely offline. Capabilities is the proto-free relayed truth the ui
// stores for its honest discoverability affordances (Phase B); an older server
// yields the all-false zero value. ResolvedModel is the EFFECTIVE provider+model
// the server resolved the session to (echoed verbatim); the ui shows it in the
// header from turn zero, and an older server yields the zero value (no model
// segment).
type SessionCreator interface {
	CreateSession(ctx context.Context, sel client.ModelSelection, mode string) (string, client.Capabilities, client.ResolvedModel, error)
	// CreateSessionInWorkspace creates a session bound to an explicit workspace
	// root (the /worktrees switch path, issue #102). CreateSession (above)
	// delegates to this with the launch workspace, so the /models restart + the
	// connect paths are byte-identical and only the /worktrees switch passes a
	// different root. The workspace becomes the session's tool root (Read/Edit/
	// Write/Grep/Glob/Bash cwd all resolve there); osfs confinement is unchanged.
	CreateSessionInWorkspace(ctx context.Context, workspace string, sel client.ModelSelection, mode string) (string, client.Capabilities, client.ResolvedModel, error)
	// CreateSessionWithCarryover is CreateSession seeded with sourceSessionID's
	// conversation history (issue #20, model-switch carryover): it restarts on a
	// picked model AND carries the current session's transcript onto the new
	// session. The server is the authority on same-vs-cross (a same-provider
	// carryover replays verbatim; a cross-provider carryover strips the prior
	// provider's replay blobs) and a turn-boundary source; the ui offers the
	// switch unconditionally when a live session exists. The caller owns closing
	// the source session AFTER the new one is ready (the server snapshotted it
	// at create time). Same return shape as CreateSession so the
	// footer/effective-model heal path is shared.
	CreateSessionWithCarryover(ctx context.Context, sourceSessionID string, sel client.ModelSelection, mode string) (string, client.Capabilities, client.ResolvedModel, error)
	// CloseSession ends a server-side session by id. The /models restart-now handoff
	// closes the OLD session before creating the new one so a model switch leaves no
	// orphaned server-side session. Best-effort: the caller proceeds with the new
	// create even if the close errors.
	CloseSession(ctx context.Context, id string) error
	// GetSession re-reads the EFFECTIVE resolved model for an existing session. The
	// footer context-meter heal (issue #66) fires it on a turn boundary while the
	// meter's denominator is still unknown — the create-time echo can carry a 0 /
	// curated-floor window for a session on a LIVE-ONLY model whose async live
	// model-list swap had not yet landed, and the server's ResolvedModel resolves to
	// the real live window once it has. SessionCreator is the canonical ui-injection
	// seam for it (the method also satisfies the narrower client.SessionGetter that
	// RefreshResolvedModelCmd consumes); *client.Client (via the sessionAdapter) and
	// the test fakes satisfy both.
	GetSession(ctx context.Context, id string) (client.SessionSnapshot, error)
	// SetMode asks the server to change the current session's permission mode and
	// returns the server-confirmed mode. Mid-turn attempts may be rejected; the ui
	// defers and retries at the next prompt boundary.
	SetMode(ctx context.Context, id, mode string) (string, error)
	// ForkSession creates a peer session from srcID's conversation-history snapshot
	// (ADR 0065) with an OPTIONAL reasoning-effort override (ADR 0068; empty
	// inherits the source's) and returns the new session id. The /effort fork-resume
	// handoff uses it: the transcript SURVIVES the effort switch because the fork
	// carries it (title is omitted — the fork inherits the source's title; provider
	// and model ALWAYS inherit). The caller owns closing the source session and the
	// GetSession refetch for the fork's resolved-model echo.
	ForkSession(ctx context.Context, srcID, reasoningEffort string) (string, error)
}

// SelectionStore persists + loads the client-side model selection (last-used). It
// is satisfied by a main-owned concrete type backed by an XDG state file; nil
// cleanly disables persistence (the active selection then lives only for the run).
// The ui touches no os/xdg itself — persistence is composition-side, like
// SessionCreator. Save is given the workspace so the store can key per-workspace;
// SaveGlobalDefault writes the workspace-agnostic global `default:` block (the
// model new/unseen workspaces inherit) — the picker's ctrl+g affordance.
type SelectionStore interface {
	Save(workspace string, sel client.ModelSelection) error
	SaveGlobalDefault(sel client.ModelSelection) error
}

// Converser opens one Converse run as a *client.Stream. *client.Client satisfies
// it (its OpenConverse, wrapped to fix the mode/ctx); tests supply a fake that
// returns a Stream over a scripted Recver.
type Converser interface {
	OpenConverse(ctx context.Context) (*client.Stream, error)
}

// LearningSettings atomically advances the operator's completed-trajectory learning
// mode and returns display-ready labels plus restart guidance. Policy ordering stays outside ui.
type LearningSettings interface {
	Advance() (fromLabel, toLabel, restart string, err error)
}

// Deps are the ui's injected collaborators and presentation config. The ui
// imports client + theme only — never contracts/gen or any internal/... package;
// all proto contact happens behind Converser/SessionCreator.
type Deps struct {
	Session   SessionCreator
	Conv      Converser
	MCP       client.MCP             // MCP/ToolHive inventory + resources/prompts; nil disables the overlay
	Cmds      client.Commander       // slash-command discovery for the input palette; nil disables it
	Skills    client.SkillLister     // skills-inventory discovery for the /skills panel; nil disables it
	Agents    client.AgentLister     // agent-definition discovery for the /agents panel; nil disables it
	Soul      client.SoulFetcher     // soul (persona) inspection for the /soul panel; nil disables it
	UserModel client.UserModelLister // user-model inspection for the /usermodel panel; nil disables it
	Models    client.ModelLister     // selectable-model discovery for the /models picker; nil disables it
	Worktrees client.WorktreeLister  // worktree discovery for the /worktrees overlay (issue #102); nil disables it
	// Sched is the schedule discovery + management surface for the /schedule overlay
	// (issue #234); nil disables it (the overlay is honestly absent). The overlay can
	// create/inspect/pause/resume/fire-now on any store-backed server; auto-firing on
	// a cadence is the server's tick loop (ON by default on a schedule-capable store,
	// ADR 0073 — `mecated --no-scheduler` opts out).
	Sched client.ScheduleLister
	// Sessions is the stored-session inventory surface for the /sessions picker
	// (issue #245 Phase 2); nil disables it (the overlay is honestly absent). It is
	// the lister the picker calls to enumerate stored sessions. Unlike the
	// caps-gated overlays it is NOT gated on a ServerCapabilities bit — the picker
	// is available whenever a lister + replayer are wired (a no-FS/cloud server
	// with a durable SessionStore still has stored sessions to list).
	Sessions client.SessionLister
	// Replayer is the durable-event-log replay surface for the /sessions transcript
	// viewer (issue #245 Phase 2/3, cloud-native Phase 3a read-back); nil disables
	// the /sessions overlay (the picker needs BOTH a lister AND a replayer — gating
	// on both keeps the overlay honest: a lister without a replayer could list
	// sessions it cannot open). The ui holds the interface (not a *Client) so it is
	// injectable with a fake for offline tests.
	Replayer client.SessionReplayer
	// LiveStream is the LIVE per-session event feed (ADR 0075 Scenario 5): the server
	// pushes fire-result delivery notes for the active session as they occur. The ui
	// holds the interface (not a *Client) so it is injectable with a fake for offline
	// tests. nil disables the live bridge (the ui still renders deliveries via the
	// replay on a session switch/reload, just not live). *Client satisfies it.
	LiveStream client.LiveStreamer
	// SelectionStore persists the picked model (last-used). nil disables persistence
	// (the pick still applies to the next create this run, just isn't remembered).
	SelectionStore SelectionStore
	Learning       LearningSettings // operator mecatl settings.yaml; nil disables /learning
	// InitialModel is the persisted selection loaded at launch (composition-side,
	// from the state file). The picker seeds its active selection from it (the ●
	// marker) and the startup CreateSession carries it — AFTER the connect-time
	// ListModels reconcile clears it if its PROVIDER is no longer available. The
	// reconcile is provider-level only (issue #41): a model absent from the (possibly
	// embedded-floor) snapshot is still carried verbatim — the server validates it,
	// and a rejection falls back to the default loudly (connectFallbackMsg).
	InitialModel client.ModelSelection
	// WorkspaceDefault / GlobalDefault are the SEPARATE raw state-file values loaded at
	// launch (composition-side): the per-workspace entry (zero when none — see
	// WorkspaceDefaultSet) and the global `default:` block. They are display-only
	// provenance inputs for the /models picker's "current: <model> (<provenance>)"
	// line and the ★ global-default row marker — the ui derives a best-effort label
	// from client-held state, never a server round-trip. (InitialModel is the RESOLVED
	// Load result = workspace-or-global; these are the un-collapsed pieces.)
	WorkspaceDefault    client.ModelSelection
	WorkspaceDefaultSet bool
	GlobalDefault       client.ModelSelection
	// Clipboard reads the OS clipboard for ctrl+v paste (image-first, text-fallback).
	// nil cleanly disables ctrl+v image paste (same convention as nil MCP/Cmds);
	// main.go populates it with client.NewClipboard().
	Clipboard client.Clipboard
	Theme     theme.Theme

	// Display-only context for the header bar.
	Server    string
	Workspace string
	Mode      string
	Model     string
	// InitialPrompt is a CLI-supplied seed prompt auto-submitted once the first
	// session is ready (the equivalent of typing the prompt and pressing enter).
	// Empty = today's behavior (no seed). Cleared after the first use so a
	// /models restart or /clear never re-submits it. Populated by main.go from
	// -p/--prompt + --prompt-file.
	InitialPrompt string

	// Version is the mecatui build version, shown on the first-run welcome splash
	// (e.g. "v0.3.1" or "dev"). Threaded from the shared
	// internal/buildinfo.Version (ldflags-set); "" omits the version line.
	// Display-only.
	Version string

	// NoBanner suppresses the rich first-run welcome splash (mascot + gradient
	// wordmark): the zero-state then renders the LEGACY plain card (title + prompt
	// hint + affordances). Set by --no-banner, --quiet, or a non-interactive stdin
	// (composed in main). Default false (full splash).
	NoBanner bool

	// Ctx is the program-level context; per-run stream contexts derive from it.
	Ctx context.Context //nolint:containedctx // stored to parent per-run stream cancels

	// NoAltScreen disables the alternate screen buffer, rendering inline in the
	// terminal's normal buffer. Default false (full-screen TUI on the alt screen).
	// Set true by the --no-alt-screen/--inline flag — a first-class user opt-out
	// for streaming the session into native scrollback (so it stays
	// searchable/scrollable after exit) — and by golden tests so the final frame
	// persists in the captured output instead of being cleared on exit.
	NoAltScreen bool

	// NoMouse disables mouse capture on the alt screen (View leaves MouseMode at
	// MouseModeNone), so the terminal's OWN click-drag selection works again — at
	// the cost of in-app mouse-wheel scroll and the in-app drag-select/copy layer
	// (selectable() returns false). Keyboard scroll is unaffected. Default false
	// (mouse captured). Set true by --no-mouse / MECATUI_NO_MOUSE=1. Inert under
	// NoAltScreen (mouse is already off inline). The escape hatch for terminals
	// that strip OSC52 or users who prefer native selection.
	NoMouse bool

	// NoWindowTitle suppresses the dynamic terminal window/tab title, leaving the
	// title at the bare "mecatui" (no phase word, no session title). Default
	// false (the title is dynamic: "<title> — <status word> mecatui"). Set true by
	// --terminal-title=off / MECATUI_NO_TERMINAL_TITLE=1 — the escape hatch for
	// terminals/multiplexers where a set title does more harm than good (or where
	// the per-phase churn is unwanted).
	NoWindowTitle bool

	// DebugMouse turns on a footer diagnostic overlay (env MECATUI_DEBUG_MOUSE=1):
	// on every mouse press/motion the footer-left is overridden with the raw mouse
	// coordinates and their content mapping (top=convTopRow, yoff, viewport height,
	// and the screenToContent result) — the durable instrument for diagnosing
	// selection/coordinate issues (it is what surfaced the highlight-on-wrong-line
	// bug). Default OFF (zero cost when unset); main.go reads the env var.
	DebugMouse bool

	// KeyOverrides maps a keyMap field name (e.g. "Agents") to its replacement chord(s).
	// nil = no overrides, byte-identical to today. Resolved+validated in composition.
	KeyOverrides map[string][]string

	// onPhase is a test-only observer (nil in production, unexported so no external
	// caller can set it) invoked by Update on the SINGLE update goroutine after each
	// reduced message, with the model's current phase. The teatest cases use it to
	// sequence input on the program's ACTUAL reducer progress instead of on rendered
	// output: under `task test`'s parallel `go test -race ./...` the Bubble Tea 60fps
	// flush ticker is CPU-starved and the captured output stalls for seconds, so a
	// WaitFor(tm.Output()) deadline fires before any frame is flushed (the historical
	// "~1/3 -race flake", in truth far worse under load). The reducer goroutine keeps
	// getting scheduled, so observing it directly is starvation-robust.
	//
	// Why the cleaner alternatives don't cover what this serves — a future maintainer
	// may want to drop it, so the tradeoff is recorded honestly:
	//   - tm.FinalModel() exposes only the FINAL model after the program exits. It
	//     cannot gate an INTERMEDIATE step mid-run — e.g. "the reducer has reached
	//     phaseAwaitingApproval, now send the approval keypress". The approval
	//     round-trip must be driven WHILE the program runs, before quit.
	//   - The fake-side reachedGate signal (fakeRecver) fires when the permission.ask
	//     is YIELDED to the ReadLoop — the input side of the gate. It does NOT observe
	//     the reducer actually entering phaseAwaitingApproval, nor the reducer
	//     RETURNING to idle after the terminal result (waitRunComplete's
	//     running→idle transition) — both reducer-side facts the fake cannot see
	//     because the fake has no handle on the model. onPhase is the minimal seam
	//     that surfaces exactly those reducer transitions.
	// (An all-fake-side scheme that also signals run-completion would remove this
	// field; that rework is deferred. For now: nil ⇒ zero cost, zero behaviour change.)
	onPhase func(phase)
}

// maxQueued caps the number of follow-up prompts that may be staged while a run
// streams. A further enqueue over the cap is rejected with a muted "queue full"
// status and the input is kept, so a typo'd burst can't grow the queue without
// bound. The drain MERGES the whole queue into one prompt (see drainQueue), so the
// cap is the only backpressure the queue needs.
const maxQueued = 16

// queueMergeSep joins multiple staged follow-ups into ONE prompt when the queue
// drains (or is pulled back for editing). The blank-line separator keeps each
// staged instruction as its own paragraph in the merged prompt (and in the
// textarea on an edit-back), which reads naturally as a multi-part request.
const queueMergeSep = "\n\n"

// phase is the model's coarse state machine.
type phase int

const (
	phaseConnecting       phase = iota // awaiting CreateSession
	phaseIdle                          // ready for a prompt
	phaseRunning                       // a Converse run is streaming
	phaseAwaitingApproval              // a permission modal is open
	phaseFatal                         // connect/fatal error; input disabled
	phaseReplay                        // a stored-session transcript replay is open (read-only; issue #245)
)

// spinnerVisible reports whether the footer renders the animated spinner in the
// current phase (renderFooter's phaseRunning/phaseConnecting arms — keep in sync).
// It gates the spinner.TickMsg handler: a tick in any other phase is dropped,
// which terminates the self-perpetuating tick chain; every transition INTO a
// visible phase must re-arm m.sp.Tick.
func (m Model) spinnerVisible() bool {
	if m.phase == phaseRunning || m.phase == phaseConnecting {
		return true
	}
	// In phaseReplay the spinner shows while the replay is still loading (the first
	// replay msg is pending or the stream has not yet closed). Once the transcript
	// is loaded the spinner idles to zero. Slice 3a's loading card is the visible
	// affordance; Slice 3b's transcript render will key off the same loading flag.
	if m.phase == phaseReplay {
		return m.sessions.loading || (!m.sessions.replayClosed && m.sessions.receivedMsgs == 0)
	}
	return false
}

// Model is the root Elm model. It owns the conversation, the bubbles widgets, the
// renderer (glamour cache), the active run stream, and the per-run cancel func.
type Model struct {
	deps Deps
	keys keyMap
	rend *renderer

	phase     phase
	sessionID string
	// sessionTitle is the session's human label for the terminal window/tab title
	// (the "<title> — …" head of windowTitle). Set-once from the first genuine
	// user prompt (submitPrompt), adopted on a session switch (switchToSession
	// reads the picker's stored title), and self-healed via a GetSession refetch
	// (onResolvedModelMsg) on the carryover/fork/adopt paths where the server
	// already set a title this client never saw. Cleared by resetSession (a
	// /clear wipes the session-derived state, including the label). The render
	// path clamps + sanitizes it; this field holds the raw adopted title.
	sessionTitle string
	statusMsg    string
	fatalErr     string

	width  int
	height int

	conv   conversation
	vp     viewport.Model
	planVP viewport.Model
	// planVPReady is true once the plan-review viewport has been populated for the
	// current plan ask (openPlanReviewView). It gates both the render path (so a
	// half-initialized planVP never renders) and the scroll-key routing (so a scroll
	// key before population does not no-op into an empty viewport). Reset to false
	// whenever the plan ask resolves/retracts/endRun/resetSession clears planVP.
	planVPReady bool
	// planVPWidth/planVPHeight record the geometry planVP was LAST populated at, so
	// relayout can skip a no-op re-population (and the expensive glamour re-wrap +
	// SetContent it triggers) when the body region did not actually change. A
	// width/height change re-populates so the plan re-wraps at the new size; the
	// scroll offset is preserved (clamped) across the re-wrap. Zero before the
	// first population.
	planVPWidth, planVPHeight int
	// planVPFingerprint is the ask fingerprint (Tool + Args + queued + model)
	// planVP was LAST populated for, so a no-op re-population (same ask, same
	// geometry) short-circuits in openPlanReviewView — a plan-review keypress
	// does not re-render the plan. Cleared alongside planVPReady.
	planVPFingerprint string
	ta                textarea.Model
	sp                spinner.Model
	// stuck is true while the viewport auto-follows the bottom (tails streaming
	// output). It is no longer hardcoded: syncStuck re-derives it from
	// m.vp.AtBottom() after every scroll/wheel/nav so a scroll-up unsticks (and
	// survives streaming — refreshView only re-pins to bottom when stuck) and
	// scrolling/jumping back to the bottom re-sticks (auto-follow resumes). The
	// initial value is true because an empty conversation is already at-bottom.
	stuck bool

	// viewDirty is set when a streamed delta mutated the conversation but
	// refreshView has not yet re-rendered it into the viewport. A one-shot
	// frame-cadence tick (renderTickCmd) flushes it at most once per frame,
	// coalescing the per-token re-renders into one render per visible frame.
	// refreshView clears it, so "rendered ⟺ not dirty" is invariant.
	viewDirty bool

	// tickArmed is true while a renderTickMsg is in flight (a flush tick has been
	// scheduled but not yet handled). A delta arms the tick ONLY when none is
	// pending, and the tick disarms itself when handled — so the ticker is a single
	// self-disarming one-shot driven by delta activity, not a free-running 60fps
	// loop. That bounds the extra render churn to actual streaming bursts (idle gaps
	// and the post-run tail cost no ticks), keeping the teatest frame cadence — and
	// thus its known ~1/3 -race flake — no worse than before.
	tickArmed bool

	activeTool   string     // tool name in flight, shown beside the spinner
	toolProgress string     // transient progress line for the in-flight tool (cleared on result/turn boundary)
	ask          pendingAsk // current permission modal (when phaseAwaitingApproval)
	// askQueue is the FIFO of surfaced permission asks waiting BEHIND the visible
	// modal — the head is always m.ask (invariant: phase==phaseAwaitingApproval ⟺
	// m.ask.AskID != ""). Concurrent subagents (team members, parallel Subagent
	// calls) can surface asks while one is already open; each parks its child
	// server-side until answered, so a second ask must queue, never clobber the
	// first. Bounded in practice by the server's child-concurrency gate — no
	// client-side cap needed. Head-advancement funnels through advanceAsk.
	askQueue []pendingAsk
	// resolvedAsks is the set of askIDs answered/retracted THIS run — a defensive
	// same-stream dedupe for re-delivered PermissionAskMsgs (the streamGen guard
	// already kills stale-reader duplicates; this kills same-stream ones). Lazily
	// initialised (markAskResolved); a reference type mutable through the
	// value-receiver Model, same pattern as filesSeen below.
	resolvedAsks map[string]struct{}
	mcp          mcpState       // MCP overlay state (view==mcpNone when closed)
	skills       skillsState    // skills-inventory overlay state (view==skillsNone when closed)
	palette      paletteState   // slash-command palette (open when the input starts with "/")
	mention      mentionState   // @-file-mention completion menu (open when the trailing word is an "@token"); mutually exclusive with palette
	queued       []string       // follow-up prompts staged while a run streams; MERGED into one prompt and drained on a clean/transient stop (see drainQueue)
	queuePaused  string         // non-empty when a run ended on a non-clean stop with a non-empty queue: the stop reason holding the queue (see drainQueue/renderQueue)
	team         teamState      // unified ctrl+a agents overlay: container open flag + Teams-tab state (view==teamNone when closed)
	agentsTab    agentsTab      // active tab in the unified agents overlay (Subagents | Parallel | Teams)
	subagents    subagentState  // Subagents-tab state of the unified agents overlay (roster | focus)
	parallel     parallelState  // Parallel-tab state of the unified agents overlay (roster | group focus)
	agentsInv    agentsInvState // agent-definition inventory overlay state (view==agentsInvNone when closed)
	soul         soulState      // soul (persona) inspection overlay state (view==soulNone when closed)
	userModel    userModelState // user-model inspection overlay state (view==userModelNone when closed)
	userModelGen uint64         // monotonic request generation; invalidates delayed detail/index responses
	models       modelsState    // /models picker overlay state (view==modelsNone when closed)
	effort       effortState    // /effort picker overlay state (view==effortNone when closed) — ADR 0055
	worktrees    worktreesState // /worktrees overlay state (view==worktreesNone when closed) — issue #102
	schedule     scheduleState  // /schedule overlay state (view==scheduleNone when closed) — issue #234
	sessions     sessionsState  // /sessions overlay state (view==sessionsNone when closed) — issue #245
	// activeModel is the currently-selected (provider, model) the NEXT CreateSession
	// will carry (apply-on-next-create). Seeded from Deps.InitialModel, updated by the
	// picker, and reconciled-to-default at connect when its provider is unavailable. It
	// is the SOURCE of truth for the create selection; m.models.active mirrors it for
	// the picker's ● marker. The header model display reads from it once non-zero.
	activeModel client.ModelSelection
	// effectiveModel is the EFFECTIVE provider+model the SERVER resolved THIS session
	// to, echoed verbatim on SessionReadyMsg (the create response). The header shows
	// its id from turn zero. DISTINCT from activeModel: activeModel is what the NEXT
	// create will REQUEST (and may be the zero/empty default selection), whereas
	// effectiveModel is what the CURRENT session actually RESOLVED to (server-owned).
	// The header reads effectiveModel for the live model display, never resolving a
	// default itself. Zero value (empty ids) until SessionReadyMsg and for an older
	// server → the header shows no model segment. The model is FIXED per session.
	effectiveModel client.ResolvedModel
	// providerRoute is the DOWNSTREAM provider the serving provider routed the LATEST
	// turn to (issue #480; today only the openrouter entry produces it, as a display
	// name like "Google"). Updated on each ProviderRouteMsg; the header appends it to
	// the model segment as "/ <name>" so the operator sees the routed downstream next
	// to the model id. Empty until the first routed turn, on a cache hit (OpenRouter
	// strips the metadata), and for any non-routed provider — the header then shows
	// the bare model segment, never a stale or fabricated suffix.
	providerRoute string
	// activeWorkspace is the workspace root the CURRENT session is bound to. Seeded
	// from Deps.Workspace at construction (the launch root) and updated by
	// switchToWorktree (issue #102) to the chosen worktree path. Shown in the header
	// when it differs from the launch workspace (Deps.Workspace), so the user can
	// tell at a glance that the session is rooted at a sibling worktree rather than
	// the launch directory. Empty = connecting (not yet bound).
	activeWorkspace string
	// pickedThisSession is the (provider, model) the user EXPLICITLY chose via the
	// /models picker's restart-now confirm during THIS process — set when a restart-now
	// handoff rebinds the session to a picked model. It is the provenance signal that
	// lets the picker label the current model "picked this session" (vs a launch-time
	// workspace/global default). Zero until a restart-now pick. Display-only.
	pickedThisSession client.ModelSelection
	showHelp          bool           // the "?" keys-&-features overlay is open (caps-driven; see help.go)
	stream            *client.Stream // current run's stream
	cancelRun         context.CancelFunc

	// quitArmed is true after a first ctrl+c on an empty prompt: a second ctrl+c
	// within quitArmWindow then quits (Claude Code's "press again to exit"
	// convention). Any other key disarms it, and a timed quitDisarmMsg disarms it
	// when the window lapses. quitArmGen is the monotonic arm generation: the
	// disarm tick carries the gen it was armed with, so a stale tick (the guard was
	// disarmed and re-armed in between) is ignored. NOT reset in resetSession — the
	// quit guard is transport/compose state, not session-derived transcript state.
	quitArmed  bool
	quitArmGen int

	// clickCount tracks the multi-click sequence for word/line select, mirroring the
	// quitArmed machinery: 0 = no sequence (disarmed), 1 = single click (today's
	// zero-width anchor), 2 = double-click (word select), 3 = triple-click (whole-line
	// select); a 4th press at the same spot WRAPS 3→1 (re-anchoring a zero-width
	// selection). A press at a DIFFERENT logical (line,col) resets the count to 1.
	// clickGen is the monotonic arm generation: the disarm tick (clickDisarmMsg)
	// carries the gen it was armed with, so a stale tick (the count was reset and
	// re-armed in between) is ignored — exactly like quitArmGen. clickL/clickC record
	// the LOGICAL (line,col) of the last counted press (from screenToContent, NOT raw
	// x/y) so same-position equality is by content position and survives layout. A
	// drag that extends the head invalidates the sequence (clickCount=0). Zeroed in
	// resetSession alongside m.sel — a /clear rebuilds the transcript, so a pending
	// multi-click anchored into the old content is stale.
	clickCount int
	clickGen   int
	clickL     int
	clickC     int

	// caps is the connected server's advertised capabilities, delivered once on
	// SessionReadyMsg. It drives the honest discoverability affordances (which
	// chords the help overlay annotates as available, and whether an empty
	// MCP/commands box reads "not enabled" vs "none configured"). Zero value
	// (all-false) until connect and for an older server. STORED, UNRENDERED in
	// Phase A — Phase B consumes it.
	caps client.Capabilities

	// activeMode is the server-confirmed permission mode for THIS session. It is
	// initialized from the launch mode and updated only from SessionReady/GetSession/
	// SetMode responses, so the server remains the authority. pendingMode is a
	// next-prompt retry requested while the aggregate was mid-turn and refused the
	// immediate switch.
	activeMode  string
	pendingMode string

	// fullColor is true on a truecolor terminal, derived from the tea.ColorProfileMsg
	// (msg.Profile == colorprofile.TrueColor) in the reducer. It gates the welcome
	// wordmark's jade→gold gradient: on a non-truecolor profile the wordmark collapses
	// to a single accent colour rather than banding the blend into mush. The ui passes
	// a bool to the welcome package so welcome never imports colorprofile. Default false
	// until the first ColorProfileMsg.
	fullColor bool

	// emojiOK is the PROCESS-STABLE emoji-presentation capability, seeded ONCE at New
	// from emojiCapable() (conservative, env-based — see emoji.go). It is read on the
	// header hot path by postureBadge to pick the yolo badge's glyph variant (emoji
	// "⚡️" with VS16 vs width-stable text "⚡"), so it must NOT call os.Environ() per
	// render — seeding it here keeps the detection off the hot path. A bool field (not a
	// sync.Once over a process global) so a test can override it on the Model after New
	// and so t.Setenv-driven tests still drive the pure detectEmoji directly.
	emojiOK bool

	// kittyActive is true once the terminal is detected Kitty-graphics-capable AND the
	// mascot image has been transmitted (the transmit tea.Cmd has fired): the welcome
	// splash then emits the Kitty Unicode-placeholder grid for the mascot instead of the
	// half-block fallback. kittyTier is the cols the mascot was last transmitted at, so a
	// size-tier change can re-transmit at the new footprint (re-fire guard). Default
	// false (half-block path). The kitty escapes go out via tea.Raw, never View content.
	// Reset to false (and kittyTier to 0) on the empty→non-empty transition so a later
	// /clear back to the zero-state re-transmits.
	kittyActive bool
	// kittyTier is the mascot column count last transmitted. cols FULLY determines the
	// cols×rows placement footprint (rows = cols/2), so it is the whole tier key: a
	// resize that keeps the same cols needs no re-transmit, a tier-crossing resize does.
	kittyTier int

	// restartedThisRun is set once a /models restart-now handoff has rebound the app
	// to a NEW session. It SUPPRESSES the first-run welcome splash (+ the Kitty mascot
	// transmit) for the rest of the process: the splash is a genuine first-run
	// affordance, and resetSession empties the conversation, so without this guard the
	// restart's empty-conversation idle frame would re-show the splash on every model
	// switch. The genuine first session AND /clear keep the splash unchanged (only a
	// restart sets this); it is never cleared (a restart is one-way for the run).
	restartedThisRun bool

	// pendingModelSwitchNote is the transient status note armed by chooseModel when the
	// user picks a model — surfaced on the SessionReadyMsg rebind as "switched to
	// <model> — conversation kept" (or, for a cross-provider switch, the honest caveat
	// that the prior model's reasoning cache was stripped). It is a one-shot: a single
	// non-empty value is consumed by applySessionReady and cleared, so a later
	// connect/reconnect that happens to pass through applySessionReady (e.g. the
	// startup create) never echoes a stale model-switch note. Empty ⇒ the rebind falls
	// back to the plain "connected" status.
	pendingModelSwitchNote string

	// pendingInitialPrompt is the CLI-supplied seed prompt (-p/--prompt +
	// --prompt-file) awaiting its first session ready. Seeded from
	// Deps.InitialPrompt at construction and consumed ONCE by applySessionReady:
	// it is set into the textarea, the pending field is cleared BEFORE the
	// submit (defense-in-depth against re-fire), and the identical typed-prompt
	// path runs. A /models restart or /clear funnels back through
	// applySessionReady but the field is already empty, so the seed never
	// re-fires. Empty = no seed (the default; today's behavior).
	pendingInitialPrompt string

	// restartFailed is true while a /models restart-now handoff's re-create FAILED and
	// the app is in the RECOVERABLE no-session state (phaseIdle, sessionID==""). It is
	// NOT phaseFatal: a transient blip on a deliberate model switch must leave a usable
	// app. While set, enter on an empty prompt re-fires restartOnModelCmd (NEVER
	// createSessionCmd — its issue-#41 fallback leg would clear the user's EXPLICIT
	// pick on a rejection; see onIdleSubmit) with the selection still in
	// m.activeModel. Cleared the moment a session is (re)established
	// (SessionReadyMsg) or a retry is fired.
	restartFailed bool

	// restartFailedForkID is the retry origin for the /effort FORK failure: the
	// SOURCE session id the failed fork was attempted from (the fork failure leaves
	// the source OPEN). While it is non-empty the armed enter-retry re-fires
	// switchEffortCmd over THIS id — re-forking PRESERVES the transcript where the
	// default restartOnModelCmd retry (create-fresh + resetSession) would wipe it,
	// the exact thing the fork-resume switch exists to prevent. Set by the
	// restartFailedMsg reducer from the msg's viaFork bit (m.sessionID is "" by
	// then, so the source id must ride its own field); cleared by the same
	// SessionReadyMsg/attempt-start paths that own restartFailed. Empty for the
	// /models + /worktrees failures (their retry re-creates fresh — their old
	// session is already gone).
	restartFailedForkID string

	// usage accumulates across the session for the footer.
	usage client.Usage

	// contextTokens is the CURRENT context occupancy, the numerator of the
	// footer context meter: fed from each TurnEndMsg.Usage.InputTokens (the
	// latest turn's prompt size, which already includes cache-served tokens) —
	// never the cumulative ResultMsg total. Distinct from usage, which is the
	// cumulative session total.
	contextTokens int64

	// expandTools toggles all tool-result bodies (and Edit/Write diffs) between
	// the line-capped view and the full view. Flipped by ctrl+t.
	expandTools bool

	// filesChanged is the de-duplicated, insertion-ordered set of workspace paths
	// touched by file-MUTATING tool calls (Edit/Write) this session, derived
	// purely from observed tool.call events (no proto/server change). filesSeen is
	// the membership set guarding the order-preserving slice against duplicates.
	// Surfaced as a muted "Δ N files" header indicator, with the list folded into
	// the ctrl+t details expansion.
	filesChanged []string
	filesSeen    map[string]struct{}

	// streamCh is the current run's reader channel; WaitForMsg drains it.
	streamCh chan tea.Msg

	// streamGen is the monotonic generation of the CURRENT run's stream. Every
	// reader command (waitCmd) tags the message it delivers with the generation that
	// was current when it was armed; the reducer drops any stream message whose
	// generation no longer matches (see Update). submitPrompt bumps it when it opens a
	// new run and endRun bumps it when it tears one down, so a reader left bound to an
	// ABANDONED stream channel (e.g. one leaked across a queue-drain) can never route
	// its messages — a stale StreamClosed/StreamErr can't cancel the new run, and a
	// stale event can't re-arm a reader on the new channel. This is the structural
	// backstop for the hand-maintained "exactly one reader per run" fan-in invariant:
	// even if a future handler leaks an extra reader, its messages become inert at the
	// next run boundary regardless of how many readers leaked.
	streamGen uint64

	// liveCh is the live session event feed's reader channel (LiveStreamCmd /
	// LiveReplayStreamCmd); WaitForMsg drains it. Armed when the active session
	// settles (session create / run end) and torn down on session switch / reset.
	liveCh    chan tea.Msg
	liveStop  func() // idempotent teardown (context.CancelFunc via sync.Once)
	liveGen   uint64 // generation guard (parallel to streamGen): drops stale-reader msgs
	liveArmed string // the session id liveCh is armed for ("" = not armed); avoids re-arm on same id

	// Live-feed reconnect + catch-up state (issue #387). When the live feed drops
	// (StreamClosedMsg/StreamErrMsg on the live reader), the ui drives the client's
	// ReconnectLiveCmd: a bounded-backoff loop that drains the durable catch-up
	// (recovering delivery notes from the gap) then re-opens StreamSessionLive.
	// liveReconCh/liveReconStop are the reconnect loop's channel + teardown;
	// liveReconGen is its generation guard (parallel to liveGen), bumped in
	// disarmLiveFeed so a stale reconnect reader after a session switch is dropped
	// WITHOUT triggering a reconnect for the old session. liveReconnecting/
	// liveReconnectAttempt/liveReconnectErr drive the degraded footer state. The
	// catch-up event msgs flow through the SAME updateStreamEvent path a live event
	// takes, so a DeliveryNoteMsg caught up here renders via addDelivery exactly as
	// a live one does.
	liveReconCh          chan tea.Msg
	liveReconStop        func()
	liveReconGen         uint64
	liveReconnecting     bool
	liveReconnectAttempt int
	liveReconnectErr     string

	// seenFireIDs is the per-session delivery-note dedup set (issue #387): a
	// fire-result delivery note that arrives BOTH via the durable catch-up AND the
	// reopened live feed must render EXACTLY ONCE. Keyed by DeliveryNoteMsg.FireID
	// (the scheduler's fire id, stable across replay and live). Seeded empty on
	// session create/switch (resetSession) and cleared alongside the live feed; a
	// delivery with an empty FireID (a malformed note deliverNoteFrom could not
	// parse) is NOT deduped (it renders once per arrival — the rare malformed case).
	seenFireIDs map[string]struct{}

	// stagedMedia holds clipboard/pasted-path image attachments not yet sent,
	// keyed by their literal "[Image #N]" marker (which also sits in the textarea
	// text). nextMediaN is the monotonic marker counter. The design is
	// no-live-renumber + reconcile-at-submit: a marker's N is assigned once and
	// never reused (deleting a marker leaves a numbering gap — fine, documented),
	// and submitPrompt reconciles by which markers still survive in the sent text
	// (survivingMarkers), ordering the parts ascending by N. This keeps the paste
	// handler O(1) and avoids renumbering every staged marker on each edit.
	stagedMedia map[string]stagedAttachment
	nextMediaN  int

	// stagedPastes holds LARGE text pastes not yet sent, keyed by their literal
	// "[Pasted text #N]" marker (which also sits in the textarea text) — the text
	// twin of stagedMedia, same design: no-live-renumber + expand-at-submit. A
	// bracketed paste over the staging thresholds (pasteNeedsStaging) inserts only
	// the marker, so the textarea buffer — which the bubbles textarea re-wraps per
	// rendered frame — never holds the huge payload (issue #45). submitPrompt and
	// enqueuePrompt expand surviving markers in place (expandPastePlaceholders);
	// a deleted marker's content is silently dropped at submit. nextPasteN is its
	// own monotonic counter (separate numbering from nextMediaN, never reused).
	stagedPastes map[string]string
	nextPasteN   int

	// sel is the in-app text-selection state (mouse-drag select + copy over the
	// conversation viewport). Zero value = inactive. Its coordinates are LOGICAL
	// content positions (line index + grapheme column into the ansi-stripped line),
	// so the highlight survives scrolling and a streaming re-render — the selection
	// style is spliced into the CURRENT content each frame (see styleSelection /
	// refreshView). Active only on the alt screen; an overlay/modal/help blocks a new
	// selection and clears an active one.
	sel selection

	// mouseDebug is the last formatted mouse-diagnostic line (see mouseDebugLine),
	// rendered in the footer only when Deps.DebugMouse is set. Set at the top of
	// onMousePress/onMouseMotion when the diagnostic is enabled; empty otherwise.
	mouseDebug string

	// selBase is the UNSTYLED content the active selection's highlight is spliced
	// onto (styleSelection) — the conversation render WITHOUT any selection styling.
	// It is captured the moment a selection becomes active (a press / word / line
	// gesture) and refreshed by refreshView (a full conversation re-render). A pure
	// GEOMETRY change (drag / edge-autoscroll, via snapshotSelection) re-splices THIS
	// base in place rather than re-rendering the whole conversation — so the highlight
	// follows the new span without a content rebuild and the viewport's scroll/line
	// geometry is undisturbed (matching the old in-place highlight). Empty when no
	// selection is active.
	selBase string

	// gatewayNotice is the rendered idle footer-left notice fired ONCE per process
	// when an intent-driven provider (the ToolHive LLM gateway) is detected-and-
	// reachable but NOT the active default. Empty = not shown. Dismissed by any
	// keypress at idle or by opening /models. Rendered via the "muted" theme slot so
	// it reads as a notice, not an error. See updateModelsMsg (the latch site).
	gatewayNotice string
	// gatewayNoticeShown latches true once the notice has fired, so it fires at most
	// ONCE per process even across repeated ModelsMsg landings (a re-open, a live
	// refresh). Survives the dismissal of gatewayNotice (which only clears the text).
	gatewayNoticeShown bool
}

// New builds the root model from deps. It wires the widgets but does not connect;
// Init kicks off CreateSession.
func New(deps Deps) Model {
	if deps.Ctx == nil {
		deps.Ctx = context.Background()
	}
	th := deps.Theme
	keys := applyKeyOverrides(defaultKeys(), deps.KeyOverrides)
	hk := keyMarkings(keys)

	ta := textarea.New()
	// The mode-coloured rail border (renderInputRail) is the SINGLE vertical accent cue,
	// so suppress the textarea's own inner prompt bar (U+2503) and line-number gutter to
	// avoid a redundant second bar (issue #161). Both MUST be set before any SetWidth —
	// bubbles' textarea computes its inner gutter width in SetWidth from Prompt +
	// ShowLineNumbers — which covers both New()'s internal SetWidth and the later onResize.
	ta.Prompt = ""
	ta.ShowLineNumbers = false
	ta.Placeholder = "Ask mecatl to do something…  (" + hk.submit + " to send · " + hk.newlineFirst + " for newline · " + hk.help + " for help)"
	ta.SetHeight(3)
	ta.Focus()

	sp := spinner.New(spinner.WithSpinner(spinner.Dot), spinner.WithStyle(th.Style("spinner")))

	vp := viewport.New()
	// In-app text-selection highlight is rendered by the APP (styleSelection splices
	// the "selection" theme style into the content lines inside refreshView), NOT the
	// viewport's native SetHighlights/HighlightStyle. The native highlighter mis-placed
	// the block on ANSI-styled (glamour) content — its parseMatches detects newlines at
	// stripped offsets indexed into the original ANSI bytes, so the highlight landed on
	// the wrong line (see styleSelection in selection.go). Owning the splice ourselves
	// is the durable fix and removes the HighlightStyle/SelectedHighlightStyle wiring
	// (and its EnsureVisible scroll-jump) entirely. The "selection" style is a solid
	// high-contrast block (luminance-derived foreground), legible on dark and light
	// themes alike — styleSelection reads it via m.deps.Theme.Style("selection").

	return Model{
		deps:  deps,
		keys:  keys,
		rend:  newRenderer(th, keyMarkings(keys)),
		phase: phaseConnecting,
		ta:    ta,
		sp:    sp,
		vp:    vp,
		stuck: true,
		// Seed the active selection from the persisted last-used (composition loads it
		// from the state file). The connect-time ListModels reconcile clears it to the
		// server default if its PROVIDER is no longer available, BEFORE the create that
		// carries it (so a removed key never hard-fails the connect with InvalidArgument).
		// A model merely absent from the snapshot is kept (issue #41); if the server then
		// rejects the create, createSessionCmd's fallback leg retries on the default and
		// surfaces a loud warning (connectFallbackMsg) — connect still completes.
		activeModel:     deps.InitialModel,
		activeMode:      client.ModeString(client.ModeFromString(deps.Mode)),
		models:          modelsState{active: deps.InitialModel, globalDefault: deps.GlobalDefault},
		activeWorkspace: deps.Workspace,
		// Seed the CLI-supplied seed prompt (-p/--prompt + --prompt-file) for
		// one-shot auto-submit on the FIRST session ready.
		pendingInitialPrompt: deps.InitialPrompt,
		// Detect emoji-presentation capability ONCE at construction (conservative,
		// env-based) so the header hot path reads a bool, never os.Environ().
		emojiOK: emojiCapable(),
	}
}

// recordFileChange folds a workspace path touched by a file-mutating tool into
// the session's changed-files set, preserving first-seen order and ignoring
// duplicates. Non-mutating / unrecognised tools never reach here (the caller
// gates on mutatedPath). Lazily initialises the membership set so a zero Model
// needs no constructor wiring.
func (m *Model) recordFileChange(path string) {
	if path == "" {
		return
	}
	if m.filesSeen == nil {
		m.filesSeen = make(map[string]struct{})
	}
	if _, ok := m.filesSeen[path]; ok {
		return
	}
	m.filesSeen[path] = struct{}{}
	m.filesChanged = append(m.filesChanged, path)
}

// resetSession is the single seam that owns "the session-derived state of the
// Model": the conversation transcript plus everything accumulated FROM the stream
// over a session (changed-files set, cumulative usage, current context size, and
// the in-flight tool affordances). It is co-located with the field declarations
// it zeroes (see the Model struct above) so that ANY future session-derived field
// added there has an obvious, single place to be reset — keeping /clear honest
// without each call site re-listing fields.
//
// It deliberately does NOT touch the per-RUN transport teardown (stream /
// streamCh / cancelRun / phase / textarea focus) — that is endRun's concern and
// its semantics are relied on by the idle-guard. The only overlap is the in-flight
// tool affordances (activeTool/toolProgress), which are genuinely both
// "session-derived display state" and "cleared at run end"; resetSession owns
// them here, endRun continues to clear activeTool on its own teardown path. The
// caller is responsible for re-rendering (refreshView) after calling this.
//
// It also drops any staged follow-up prompts (queued): /clear wipes the
// session-derived state, and a queue of as-yet-unsent follow-ups is part of that
// state — leaving them to drain into a freshly-cleared transcript would surprise.
//
// It deliberately does NOT clear the picker/inventory overlay state (models/
// worktrees/schedule/sessions) — those are transport/compose state like
// activeModel/caps, NOT session-derived transcript state, so a /clear or a
// session switch must not dismiss an open picker. The /sessions replay-derived
// fields (replayCh/replayStop/transcript) are cleared by closeSessionsTranscript
// on the esc-teardown path from phaseReplay, NOT here — resetSession is called on
// the switchToSession handoff BEFORE those are set, and closeSessionsTranscript
// owns their teardown. Only the transcript conversation (m.conv) is session-
// derived; the sessionsState's replay-transcript field (sessions.transcript) is a
// SEPARATE conversation the replay projects into, cleared by closeSessionsTranscript.
func (m Model) resetSession() Model {
	m.conv = conversation{}
	// Drop the renderer's per-block caches (blockCache AND blockMD) alongside the
	// conversation: both key on the block's conversation INDEX, and the rebuilt
	// conversation reuses indices 0..n for entirely different blocks whose
	// rev/src could coincidentally match a stale entry — which would alias an old
	// block's render onto the new transcript. Covers /clear and the /models
	// restart-now handoff (both funnel through here).
	m.rend.resetBlockCaches()
	// An empty conversation is at-bottom by definition, so auto-follow must be
	// re-armed: without this a /clear issued while scrolled up (stuck=false) would
	// strand stuck false, and refreshView (re-pins only if stuck) would silently
	// fail to tail the NEXT run's streaming deltas until the user manually hit End.
	m.stuck = true
	m.filesChanged = nil
	m.filesSeen = nil
	m.usage = client.Usage{}
	m.contextTokens = 0
	m.activeTool = ""
	m.toolProgress = ""
	m.providerRoute = ""
	// Drop the session title: it is session-derived (seeded from the first prompt
	// / adopted from the stored session on a switch), so a /clear or a /models
	// restart-now must not leave a stale label on the freshly-cleared session.
	m.sessionTitle = ""
	// Drop any pending permission modal — and the FIFO queue behind it plus the
	// answered-set dedupe: an ask is session-derived in-flight state (its AskID
	// correlates to a run on the OLD session), so a reset must not leave a stale
	// modal, stale queued asks, or a stale dedupe set dangling. Latent today (the
	// picker/clear paths are idle-only, so no ask is open), but keeps this seam's
	// "owns all session-derived state" invariant honest — and the restart-now
	// handoff goes through here. The plan-review viewport is cleared alongside (a
	// plan ask may have been open).
	(&m).clearPlanReview()
	m.ask = pendingAsk{}
	m.askQueue = nil
	m.resolvedAsks = nil
	m.queued = nil
	m.queuePaused = ""
	// Drop staged-but-unsent media attachments: /clear wipes the session-derived
	// state, and pasted-but-unsent images are part of that compose state.
	m.stagedMedia = nil
	m.nextMediaN = 0
	// Same for staged-but-unsent large text pastes (their markers lived in the
	// textarea this reset wipes via the caller's input handling — a dangling store
	// would silently re-attach old pastes to a future marker collision).
	m.stagedPastes = nil
	m.nextPasteN = 0
	// Drop any active text selection: /clear rebuilds the transcript, so a selection
	// anchored into the old content is stale. The caller's refreshView re-renders
	// without re-applying it (sel is now inactive), clearing the highlight too.
	m.sel = selection{}
	// Drop any pending multi-click sequence: it is anchored into the old content.
	m.clickCount = 0
	// Disarm the live feed: a /clear or session switch rebuilds the session, so
	// the old live subscription (bound to the old/cleared session id or opened
	// while the stale session was active) must not route delivery events into the
	// fresh conversation.
	m.disarmLiveFeed()
	// Disarm the reconnect loop too (issue #387): a /clear or session switch
	// abandons the old session's live recovery, and the gen bump invalidates any
	// stale reconnect reader so it cannot route a late LiveReconnectingMsg (or a
	// catch-up event) into the fresh session.
	m.disarmReconnect()
	// Drop the per-session delivery-note dedup set: the fire ids belong to the
	// old session, so a fresh session must not suppress a coincidentally-reused
	// fire id. (Empty FireID deliveries are never deduped, so an empty map is the
	// honest "no dedup yet" state.)
	m.seenFireIDs = nil
	// Clear any live-feed degraded state: a /clear or session switch abandons
	// the old session's live recovery, so the footer must not keep showing
	// "reconnecting" for a session that no longer exists.
	m.liveReconnecting = false
	m.liveReconnectAttempt = 0
	m.liveReconnectErr = ""
	return m
}

// Init starts the spinner and kicks off connect.
//
// Connect SEQUENCING (§4 key-removed safety): when a model lister is wired, it
// fetches ListModels FIRST and lets the connecting-phase ModelsMsg reconcile the
// persisted selection against PROVIDER availability BEFORE firing CreateSession —
// so a removed provider key can never hard-fail the connect with InvalidArgument.
// The reconcile is provider-level only (issue #41): the model string rides through
// verbatim (the boot snapshot may be the embedded floor), the server validates it,
// and a server rejection degrades to the default loudly via createSessionCmd's
// fallback leg. With no lister wired (old server / persistence off) it fires
// CreateSession directly (the historical path, with an empty selection).
func (m Model) Init() tea.Cmd {
	if m.deps.Models != nil {
		return tea.Batch(m.sp.Tick, client.ListModelsCmd(m.deps.Ctx, m.deps.Models))
	}
	// No-lister / old-server path: with no model lister wired there is nothing to
	// reconcile, so fire CreateSession directly (with the empty selection) — do NOT
	// wait on a ListModels that will never arrive, which would strand at "connecting…".
	return tea.Batch(m.sp.Tick, m.createSessionCmd())
}
