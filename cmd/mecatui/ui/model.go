// Package ui is the Bubble Tea (Elm) layer of mecatui: the root Model state
// machine, its Update reducer, the View assembly, and the conversation/block
// renderers. It imports only the client and theme packages (plus charm libs and
// stdlib) — never contracts/gen or any internal/... package — so it renders
// purely from the plain msg structs the client layer translates proto Events
// into. All glamour rendering happens here on the single update goroutine.
package ui

import (
	"context"
	"errors"

	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	statusline "github.com/stacklok/mecatl/cmd/mecatui/statusline"
	"github.com/stacklok/mecatl/cmd/mecatui/theme"
	"github.com/stacklok/mecatl/cmd/mecatui/ui/platform"
	"github.com/stacklok/mecatl/cmd/mecatui/ui/prompttextarea"
	"github.com/stacklok/mecatl/cmd/mecatui/ui/welcome"
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
	AdvanceSensitivity() (fromLabel, toLabel, restart string, err error)
}

// Deps are the ui's injected collaborators and presentation config. The ui
// imports client + theme only — never contracts/gen or any internal/... package;
// all proto contact happens behind Converser/SessionCreator.
type Deps struct {
	Session SessionCreator
	Conv    Converser
	MCP     client.MCP       // MCP/ToolHive inventory + resources/prompts; nil disables the overlay
	Cmds    client.Commander // slash-command discovery for the input palette; nil disables it
	// ServerInfo reads the safe build and composition identities when /diagnostics is invoked against a remote server.
	ServerInfo ServerInfoGetter
	// ServerImpl is the locally-known embedded server family. It is used without
	// an RPC when Embedded is true.
	ServerImpl  string
	Skills      client.SkillLister      // skills-inventory discovery for the /skills panel; nil disables it
	Agents      client.AgentLister      // agent-definition discovery for the /agents panel; nil disables it
	Soul        client.SoulFetcher      // soul (persona) inspection for the /soul panel; nil disables it
	UserModel   client.UserModelLister  // user-model inspection for the /usermodel panel; nil disables it
	Reflections client.ReflectionClient // proposal review and explicit reflection; nil disables it
	Dream       client.DreamClient      // manual memory consolidation review; nil disables /dream
	Compactor   client.SessionCompactor // out-of-band session compaction; nil disables /compact
	Models      client.ModelLister      // selectable-model discovery for the /models picker; nil disables it
	Worktrees   client.WorktreeLister   // worktree discovery for the /worktrees overlay (issue #102); nil disables it
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
	// is available whenever a lister + authoritative transcript loader are wired
	// (a no-FS/cloud server with a durable SessionStore still has stored sessions).
	Sessions client.SessionPager
	// StorageHealth is the authenticated aggregate health surface. The capability
	// bit controls whether the Sessions panel advertises its maintenance tab.
	StorageHealth client.StorageHealthFetcher
	// Migration and Cleanup are deliberately distinct management seams. Their
	// server capability bits independently gate the semantics-preserving and
	// destructive workflows.
	Migration client.SessionMigrator
	Cleanup   client.SessionCleaner
	// SessionManagement mutates stored main-chat metadata. nil leaves rename/delete
	// undiscoverable even if a custom lister advertises those capabilities.
	SessionManagement client.SessionManager
	// Adoption is the authenticated legacy-copy surface. Eligibility is always
	// taken from its source-correlated preflight, never inferred from row IDs.
	Adoption client.SessionAdopter
	// Transcript is the authoritative snapshot-derived conversation surface used
	// by /sessions for both continuation and read-only inspection. Event replay is
	// optional activity and never substitutes for this seam.
	Transcript client.SessionTranscripter
	// Replayer is the optional durable-event-log activity surface used by live
	// delivery catch-up. It never attests conversation completeness.
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
	// Connect lists public saved remote-target metadata. It cannot authenticate,
	// launch a browser, or expose credentials; selection exits via ConnectRestartIntent.
	Connect ConnectController
	// ConnectOpen reopens /connect after a host-side login/dial failure. ConnectError
	// is host-sanitized display text only.
	ConnectOpen  bool
	ConnectError string
	// ConnectReason controls the auth-recovery affordance without exposing a
	// transport error or credential to the renderer.
	ConnectReason          client.AuthReason
	ConnectTarget          string
	ConnectResumeSessionID string
	// BearerBacked records credential provenance for classifying server auth
	// responses. It contains no credential material.
	BearerBacked bool
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
	// StatusSource is composed outside ui. The UI only submits display facts and
	// consumes semantic snapshots through one Bubble Tea listener.
	StatusSource statusline.Source

	// Presentation capability probes are package-private test seams. New replaces
	// nil values with the production environment detectors.
	emojiCapable      func() bool
	kittyCapable      func() bool
	scrollKeysMarking func() string

	// ClientBuild is the local mecatui build identity and Embedded selects the
	// local server identity path for /diagnostics.
	ClientBuild string
	Embedded    bool

	// Display-only context for the header bar.
	Server         string
	ConnectionMode string
	Workspace      string
	Mode           string
	Model          string
	// Resume is a statically validated existing chat selected before Bubble Tea
	// starts. Its authoritative transcript is adopted without CreateSession; nil
	// preserves the new-session default.
	Resume *client.ResumeSelection
	// BrowseSessions launches into the same inventory used by /sessions without
	// creating a throwaway session. New-chat creation remains gated on the startup
	// model-list reconcile.
	BrowseSessions bool
	// InitialPrompt is a CLI-supplied seed prompt auto-submitted once the first
	// session is ready (the equivalent of typing the prompt and pressing enter).
	// Empty = today's behavior (no seed). Cleared after the first use so a
	// /models restart or /clear never re-submits it. Populated by main.go from
	// -p/--prompt + --prompt-file.
	InitialPrompt string

	// Version is the mecatui build version, shown on the first-run welcome splash
	// (e.g. "v0.3.1" or "dev"). Threaded from the shared
	// internal/buildinfo.BuildID (ldflags-set); "" omits the version line.
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

	// DebugSteer turns on a steer correlation trace in the status line (env
	// MECATUI_DEBUG_STEER=1): each steer ack/echo logs the incoming message_id,
	// the live bundle's id, and the match/burn/drop decision, so a stuck or
	// mis-correlated steer lifecycle is visible in the TUI rather than opaque.
	// Default OFF (zero cost when unset); main.go reads the env var.
	DebugSteer bool

	// DebugAsk registers the /debug-ask built-in (env MECATUI_DEBUG_ASK=1): it
	// injects a fake permission ask with long Bash args through the REAL ask
	// reducer, so the modal's wrap/scroll/full-screen-args behaviour (issue #488)
	// can be exercised by hand without driving a live run. Default OFF (the
	// built-in is absent); main.go reads the env var — deliberately never a flag,
	// so it stays out of --help.
	DebugAsk bool

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

// steerPhase is the AUTHORITATIVE lifecycle of a steer-mode (Capabilities.Steer)
// mid-run operator steer. The server is the sole authority on what happened to a
// steer (the client cannot observe the exact drain moment across stream latency),
// so these phases are driven ONLY by the server's steer.outcome acks and the
// steer drain echo — never assumed client-side.
type steerPhase int

const (
	// steerPending: the merged text was sent on the Converse stream but the
	// server's steer.outcome ack has not arrived yet (send in flight).
	steerPending steerPhase = iota
	// steerSent: the server acked accepted/appended — the steer is parked in the
	// run's single-slot inbox awaiting the next turn-boundary drain.
	steerSent
	// steerPromoted: the server acked too_late+promoted — the run had already gone
	// terminal, so the text was auto-promoted to a fresh follow-up run.
	steerPromoted
	// steerFailed: the server acked too_late WITHOUT promoted — the promotion
	// failed (routing / run-entry / lease / funnel error) and the text was NOT
	// delivered. Distinct from promoted so the ui does NOT report a successful
	// follow-up. The text is preserved (it may be re-sent or dropped explicitly).
	steerFailed
	// steerRetracted: a steer_cancel won — the pending steer was retracted before
	// it drained (the run drains nothing for it).
	steerRetracted
)

// steerQueuedSend is ONE send in the ordered steer queue: the fresh client-minted
// message_id of THIS send and the (fragment) text it carried. The ordered queue
// (steerState.sends) lets the TUI split "drained up to the watermark id" from
// "still pending after it" on each drain echo, instead of collapsing everything
// into one re-minted bundle (the duplication bug the append model exposed).
type steerQueuedSend struct {
	ID   string
	Text string
}

// steerState is the ONE-ELEMENT steer-mode mid-run state: the pending bundle —
// an ORDERED queue of sends (steerQueuedSend), its merged display text, and its
// authoritative lifecycle phase. The ordered queue is the SINGLE correlation
// source (the watermark id is the tail's message_id — derived, never stored);
// text-display is the blank-line join of sends (cached to avoid a per-render
// re-join).
type steerState struct {
	// Text is the merged display text (the blank-line join of sends) shown on the
	// card; the wire sends each send's OWN fragment (never the whole merged
	// text — that would re-append drained text, the duplication bug).
	Text string
	// Phase is the authoritative lifecycle (see steerPhase).
	Phase steerPhase
	// Sends is the ordered queue of sends currently in this bundle (ID+fragment).
	// On a drain echo the prefix up to and incl. the watermark id is dropped
	// (drained); the suffix stays pending and Text re-joins from the remainder.
	Sends []steerQueuedSend
}

// watermarkID derives the bundle's current watermark id — the tail send's
// message_id — or "" for an empty queue (the steerCancel frame's scoping hint).
func (s *steerState) watermarkID() string {
	if s == nil || len(s.Sends) == 0 {
		return ""
	}
	return s.Sends[len(s.Sends)-1].ID
}

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
// visible phase must re-arm m.sp.Tick. Session transcript loading is owned and
// rendered by sessionsState; it does not depend on the Model spinner.
func (m Model) spinnerVisible() bool {
	return m.phase == phaseRunning || m.phase == phaseConnecting
}

// Model is the root Elm model. It owns the conversation, the bubbles widgets, the
// renderer (glamour cache), the active run stream, and the per-run cancel func.
type Model struct {
	deps    Deps
	keys    keyMap
	rend    *renderer
	hits    *hitRegions // reference state shared with value-receiver View copies
	metrics *renderedSurfaceMetrics

	phase     phase
	sessionID string
	// browsingStartupSessions keeps the launch picker lifecycle distinct from the
	// ordinary in-chat /sessions overlay. modelsReconciled gates n so a persisted
	// selection cannot race the startup ListModels result.
	browsingStartupSessions bool
	modelsReconciled        bool
	// sessionsTranscriptRequestToken identifies a transcript request across the entire
	// modal lifecycle. Unlike the surface-local request token, it never resets when
	// a fresh sessionsState is created after closing the modal.
	sessionsTranscriptRequestToken uint64
	sessionsPageRequestToken       uint64
	sessionsActionRequestToken     uint64
	// Maintenance job handles outlive the Sessions overlay. Reopening uses them
	// only to refetch server-owned durable progress; the UI owns no job state.
	maintenanceMigrationJobID string
	maintenanceCleanupJobID   string
	// sessionDetailsOpen is the read-only /session surface. The metadata fields
	// below are refreshed from the current session snapshot; zero timestamps are
	// rendered as unknown rather than guessed.
	sessionDetailsOpen bool
	sessionState       string
	sessionCreatedAt   int64
	sessionModifiedAt  int64
	// sessionTitle is the session's human label for the terminal window/tab title
	// (the "<title> — …" head of windowTitle). Set-once from the first genuine
	// user prompt (submitPrompt), adopted on a session switch (switchToSession
	// reads the picker's stored title), and self-healed via a GetSession refetch
	// (onResolvedModelMsg) on the carryover/fork/adopt paths where the server
	// already set a title this client never saw. Cleared by resetSession (a
	// /clear wipes the session-derived state, including the label). The render
	// path clamps + sanitizes it; this field holds the raw adopted title.
	sessionTitle        string
	statusMsg           string
	generatedStatusLine statusline.Result
	fatalErr            string

	compactPending      bool
	compactRequestToken uint64

	width  int
	height int

	conv conversation
	vp   viewport.Model

	// approval state is dynamic: the surface owns it only while an ask is open.
	// debugAskCycle rotates the /debug-ask built-in (Deps.DebugAsk) through its
	// canned long-args payloads so repeated invocations exercise the different
	// wrap shapes (one long line, a compound pipeline, a heredoc).
	debugAskCycle int

	prompt prompttextarea.Editor
	sp     spinner.Model
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

	activeTool                   string         // tool name in flight, shown beside the spinner
	toolProgress                 string         // transient progress line for the in-flight tool (cleared on result/turn boundary)
	skillsEpoch                  uint64         // model-lifetime monotonic /skills request epoch; never reset on close (the surface mints via its nextEpoch closure)
	skillChangeLast              string         // newest bounded lifecycle receipt already announced
	palette                      paletteState   // slash-command palette (open when the input starts with "/")
	mention                      mentionState   // @-file-mention completion menu (open when the trailing word is an "@token"); mutually exclusive with palette
	queued                       []string       // follow-up prompts staged while a run streams; MERGED into one prompt and drained on a healthy stop (see drainQueue)
	queuePaused                  string         // non-empty when a run ended on a non-clean stop with a non-empty queue: the stop reason holding the queue (see drainQueue/renderQueue)
	failedStepRetryTried         bool           // one-shot guard for automatic typed precommit retry; reset by a genuine prompt or session replacement
	failedStepRetryRun           bool           // current Converse stream was opened with RetryStart
	failedStepRetryAuthoritative bool           // current retry emitted turn.start and therefore called the model
	team                         teamState      // unified ctrl+a agents overlay: container open flag + Teams-tab state (view==teamNone when closed)
	agentsTab                    agentsTab      // active tab in the unified agents overlay (Subagents | Parallel | Teams)
	subagents                    subagentState  // Subagents-tab state of the unified agents overlay (roster | focus)
	parallel                     parallelState  // Parallel-tab state of the unified agents overlay (roster | group focus)
	agentsInv                    agentsInvState // agent-definition inventory overlay state (view==agentsInvNone when closed)
	userModel                    userModelState // user-model inspection overlay state (view==userModelNone when closed)
	userModelGen                 uint64         // monotonic request generation; invalidates delayed detail/index responses
	reflections                  reflectionsState
	reflectionsGen               uint64
	dream                        dreamState
	dreamGen                     uint64
	dreamRequest                 uint64
	// steer is the steer-mode (Capabilities.Steer) mid-run state: ONE bundle (the
	// merged operator steer text + its client-minted message_id) with its
	// AUTHORITATIVE lifecycle — idle → pending (sent, un-acked) → sent (acked,
	// awaiting drain) → promoted (too_late; the server auto-started a follow-up
	// run) → failed (too_late without promoted) → retracted (steer_cancel won). nil
	// when no steer is in flight (the common case) OR steer is disabled (the #228
	// local merge-queue then owns mid-run input, byte-identical). A ONE-BUNDLE
	// state — the client-side merge collapses staged lines into ONE text BEFORE
	// send, so the engine's single-slot inbox only ever has one bundle outstanding.
	steer    *steerState
	steerSeq int // session-scoped message-id serial (1-based; "steer-%04d")
	// modelCatalogRequestToken is the Model-lifetime sequence for durable catalog
	// requests. It never resets when the picker closes, so an older result cannot
	// overwrite root state or a reopened picker.
	modelCatalogRequestToken uint64
	modelCatalog             modelCatalog          // root-owned inventory, statuses, defaults, and selection reconciliation
	effort                   effortState           // /effort picker overlay state (view==effortNone when closed) — ADR 0055
	worktrees                worktreesState        // /worktrees overlay state (view==worktreesNone when closed) — issue #102
	schedule                 scheduleState         // /schedule overlay state (view==scheduleNone when closed) — issue #234
	connect                  connectState          // /connect saved-target picker (tombstone overlay)
	connectLoadGeneration    uint64                // monotonic across overlay close/reopen; stale async loads are ignored
	connectIntent            *ConnectRestartIntent // set only immediately before tea.Quit
	// modal is the ONE open modal overlay (nil = none). Stack/tiling/focus-tree
	// is later; the field carries the one migrated surface. A surface's state is
	// created at Open and lives ONLY inside this interface field — never a
	// pre-declared tombstone field (surface.go).
	modal surface
	// modelSwitchRequestToken correlates the asynchronous create-and-hydrate handoff.
	// A stale result must not replace a session selected by a later lifecycle action.
	modelSwitchRequestToken uint64
	// createModelSelection is the client selection for future CreateSession calls. Zero uses the server default.
	createModelSelection client.ModelSelection
	// resolvedSessionModel is the server-resolved model for the bound session.
	resolvedSessionModel client.ResolvedModel
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

	// suspendedFrom records the phase the model was in when a ctrl+z suspend fired
	// (issue #504) plus the session id captured at that instant (the session could
	// roll over while suspended). Set in onSuspend, read by the ResumeMsg reducer to
	// explain what the suspension left running, then cleared. suspendedAtSet is the
	// "did a ctrl+z suspend just resume" flag.
	suspendedFrom phase
	suspendedAtID string

	// quitDArmed / quitDArmGen are the ctrl+d double-press guard — the unix
	// EOF-habit quit. They are INDEPENDENT of quitArmed/quitArmGen (a ctrl+c arms
	// only the Quit guard, a ctrl+d only the QuitD guard; validator rule 6 forbids
	// the two sharing a chord, so neither can confirm the other). Same arm-window +
	// timed-disarm + generation machinery as the Quit guard. NOT reset in
	// resetSession for the same reason.
	quitDArmed  bool
	quitDArmGen int

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
	// sync.Once over a process global) keeps each Model independently injectable while
	// pure detectEmoji tests continue to cover the production environment contract.
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
	// <model> — conversation kept". It is a one-shot: a single
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

	// Startup-adopted chats remain protected until their first prompt reaches the
	// server stream. A pre-SessionInit failure restores the authoritative transcript
	// as a read-only retry/back view; no fallback session is ever created.
	startupAdopted            bool
	startupFirstPromptPending bool
	startupRunEntryFailed     bool
	startupRetryPrompt        string

	// restartFailed is true while a /models restart-now handoff's re-create FAILED and
	// the app is in the RECOVERABLE no-session state (phaseIdle, sessionID==""). It is
	// NOT phaseFatal: a transient blip on a deliberate model switch must leave a usable
	// app. While set, enter on an empty prompt re-fires restartOnModelCmd (NEVER
	// createSessionCmd — its issue-#41 fallback leg would clear the user's EXPLICIT
	// pick on a rejection; see onIdleSubmit) with the selection still in
	// m.createModelSelection. Cleared the moment a session is (re)established
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

	// liveCh is the live session event feed's reader channel (LiveStreamCmd);
	// WaitForMsg drains it. Armed when the active session
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
	// liveContinuityAttempt is the per-session reader-failure sequence. It is
	// independent of the current reconnect footer attempt: probes and catch-up
	// do not reset it; only a real event from the current live reader does.
	liveContinuityAttempt int
	liveReconnectErr      string

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
	// gesture) and refreshed by refreshView (a full conversation re-render) — and by
	// snapshotSelection itself when a gesture races a pending delta (the viewDirty
	// guard, so a stale base is never spliced). A pure GEOMETRY change (drag /
	// edge-autoscroll, via snapshotSelection) re-splices THIS base in place rather
	// than re-rendering the whole conversation — so the highlight follows the new
	// span without a content rebuild and the viewport's scroll/line geometry is
	// undisturbed (matching the old in-place highlight). Empty when no selection is
	// active.
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
	if deps.emojiCapable == nil {
		deps.emojiCapable = emojiCapable
	}
	if deps.kittyCapable == nil {
		deps.kittyCapable = welcome.KittyCapable
	}
	if deps.scrollKeysMarking == nil {
		deps.scrollKeysMarking = platform.ScrollKeysMarking
	}
	th := deps.Theme
	keys := applyKeyOverrides(defaultKeys(), deps.KeyOverrides)
	hk := keyMarkingsWithScroll(keys, deps.scrollKeysMarking())

	prompt := prompttextarea.New(prompttextarea.Config{
		Placeholder: "Ask mecatl to do something…  (" + hk.submit + " to send · " + hk.newlineFirst + " for newline · " + hk.help + " for help)",
		SelectAll:   keys.SelectAll,
	})

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

	m := Model{
		deps:    deps,
		keys:    keys,
		rend:    newRenderer(th, hk),
		hits:    &hitRegions{},
		metrics: &renderedSurfaceMetrics{},
		phase:   phaseConnecting,
		prompt:  prompt,
		sp:      sp,
		vp:      vp,
		stuck:   true,
		// Seed the active selection from the persisted last-used (composition loads it
		// from the state file). The connect-time ListModels reconcile clears it to the
		// server default if its PROVIDER is no longer available, BEFORE the create that
		// carries it (so a removed key never hard-fails the connect with InvalidArgument).
		// A model merely absent from the snapshot is kept (issue #41); if the server then
		// rejects the create, createSessionCmd's fallback leg retries on the default and
		// surfaces a loud warning (connectFallbackMsg) — connect still completes.
		createModelSelection: deps.InitialModel,
		activeMode:           client.ModeString(client.ModeFromString(deps.Mode)),
		modelCatalog:         modelCatalog{active: deps.InitialModel, globalDefault: deps.GlobalDefault},
		activeWorkspace:      deps.Workspace,
		// Seed the CLI-supplied seed prompt (-p/--prompt + --prompt-file) for
		// one-shot auto-submit on the FIRST session ready.
		pendingInitialPrompt: deps.InitialPrompt,
		// Detect emoji-presentation capability ONCE at construction (conservative,
		// env-based) so the header hot path reads a bool, never os.Environ().
		emojiOK: deps.emojiCapable(),
	}
	// Recovery-only startup has no session creator by design. Start directly in the
	// connect surface so Init cannot fall through to createSessionCmd.
	if deps.ConnectOpen && deps.Session == nil {
		m.phase = phaseIdle
		m.connectLoadGeneration = 1
		m.connect = connectState{open: true, loading: true, err: deps.ConnectError, reason: deps.ConnectReason, failedTarget: deps.ConnectTarget, resumeSessionID: deps.ConnectResumeSessionID}
		m.prompt.Blur()
	}
	// Init issues token 1 for the startup catalog request. Resume skips that
	// request, so its first picker request starts at 1 instead.
	if deps.Models != nil && deps.Resume == nil {
		m.modelCatalogRequestToken = 1
	}
	if deps.BrowseSessions {
		m.phase = phaseIdle
		m.browsingStartupSessions = true
		m.modelsReconciled = deps.Models == nil
		state := m.newSessionsSurface(true)
		_ = state.beginPage("")
		m.prompt.Blur()
		if deps.Sessions == nil {
			state.loading = false
			state.loadState = sessionsInitialPageError
			state.err = errors.New("session inventory unavailable")
		}
	}
	if resume := deps.Resume; resume != nil {
		m.phase = phaseIdle
		m.sessionID = resume.Row.ID
		m.sessionTitle = resume.Row.Title
		m.sessionState = resume.Snapshot.State
		m.sessionCreatedAt = resume.Snapshot.CreatedAt
		m.sessionModifiedAt = resume.Row.ModifiedAt
		m.activeWorkspace = resume.Snapshot.Workspace
		m.activeMode = client.ModeString(client.ModeFromString(resume.Snapshot.Mode))
		(&m).setResolvedSessionModel(resume.Snapshot.ResolvedModel)
		m.caps = resume.Snapshot.Capabilities
		m.conv = conversationFromTranscript(resume.Transcript.Messages)
		m.startupAdopted = true
		m.restartedThisRun = true
		m.statusMsg = "continuing chat " + sanitizeTerminal(resume.Row.Title) + " — type to add a turn"
		m.refreshView()
	}
	return m
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
func (m Model) resetSession() Model {
	m.conv = conversation{}
	return m.resetSessionDerived()
}

func (m Model) resetSessionDerived() Model {
	// Drop renderer caches before installing the target's authoritative transcript.
	m.rend.resetBlockCaches()
	// Reset auto-follow for the next session's transcript.
	m.stuck = true
	m.filesChanged = nil
	m.filesSeen = nil
	m.usage = client.Usage{}
	m.contextTokens = 0
	m.activeTool = ""
	m.toolProgress = ""
	m.providerRoute = ""
	// Drop the session title: it is session-derived (seeded from the first prompt
	// / adopted from the stored session), so a /clear or fresh /models restart
	// must not leave a stale label on its new session.
	m.sessionTitle = ""
	// Drop any pending permission modal — and the FIFO queue behind it plus the
	// answered-set dedupe: an ask is session-derived in-flight state (its AskID
	// correlates to a run on the OLD session), so a reset must not leave a stale
	// modal, stale queued asks, or a stale dedupe set dangling. Latent today (the
	// picker/clear paths are idle-only, so no ask is open), but keeps this seam's
	// "owns all session-derived state" invariant honest — and the restart-now
	// handoff goes through here. The plan-review viewport is cleared alongside (a
	// plan ask may have been open), as is the full-screen ask-args view.
	m.closeModal()
	m.queued = nil
	m.queuePaused = ""
	m.failedStepRetryTried = false
	m.failedStepRetryRun = false
	m.failedStepRetryAuthoritative = false
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
	m.liveContinuityAttempt = 0
	m.liveReconnectErr = ""
	return m
}

// startupResumeReadyMsg starts post-adoption work only after Bubble Tea owns the
// model, preserving transcript-before-seed ordering.
type startupResumeReadyMsg struct{}

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
	if m.deps.ConnectOpen && m.deps.Session == nil {
		if m.deps.Connect == nil {
			return nil
		}
		deps := m.deps
		return func() tea.Msg {
			targets, err := deps.Connect.ListConnectTargets(deps.Ctx)
			return connectTargetsMsg{generation: m.connectLoadGeneration, targets: targets, err: err}
		}
	}
	if m.deps.BrowseSessions {
		cmds := []tea.Cmd{m.sp.Tick, m.statusLineWaitCmd()}
		if m.deps.Models != nil {
			cmds = append(cmds, client.ListModelsCmd(m.deps.Ctx, m.deps.Models, m.modelCatalogRequestToken))
		}
		if m.deps.Sessions != nil {
			cmds = append(cmds, sessionsSurface(&m).pageCmd(), textinput.Blink)
		}
		return tea.Batch(cmds...)
	}
	if m.deps.Resume != nil {
		return tea.Batch(m.sp.Tick, func() tea.Msg { return startupResumeReadyMsg{} }, m.statusLineWaitCmd())
	}
	if m.deps.Models != nil {
		return tea.Batch(m.sp.Tick, client.ListModelsCmd(m.deps.Ctx, m.deps.Models, m.modelCatalogRequestToken), m.statusLineWaitCmd())
	}
	// No-lister / old-server path: with no model lister wired there is nothing to
	// reconcile, so fire CreateSession directly (with the empty selection) — do NOT
	// wait on a ListModels that will never arrive, which would strand at "connecting…".
	return tea.Batch(m.sp.Tick, m.createSessionCmd(), m.statusLineWaitCmd())
}
