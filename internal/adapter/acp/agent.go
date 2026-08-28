package acp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/mcp"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

// Agent is the ACP adapter's request handler: it bridges the JSON-RPC Conn to the
// surface-agnostic *server.Service (the SAME service the gRPC/HTTP adapters
// consume). It owns the one-prompt-per-session serialization and the outbound
// request_permission round-trip. It is the ACP equivalent of server.HarnessServer.
type Agent struct {
	svc  *server.Service
	conn *Conn

	// inFlight guards the "one in-flight prompt per session" rule: a session id is
	// present while its session/prompt is running, so a second prompt for the same
	// session is rejected.
	//
	// mcpSessions records the session ids created with a PER-SESSION engine (i.e.
	// the client supplied streaming-HTTP MCP servers). Mid-session teardown is
	// handled by session/close (handleSessionClose untracks the id, then runs the
	// same EndSession teardown). When the Serve loop ends (editor disconnect) the
	// Agent calls svc.CloseSession for each STILL-tracked id as a BACKSTOP, tearing
	// down any session's client MCP manager the client never explicitly closed so
	// it does not outlive the connection. Both maps are guarded by mu.
	mu          sync.Mutex
	inFlight    map[string]struct{}
	mcpSessions map[string]struct{}

	// info is the agent identity returned on initialize.
	info implementation

	// caps is the DEFAULT provider+model's intersected multimodal input support
	// (catalog ∩ adapter, computed in composition), read ONCE from the Service at
	// construction. handleInitialize advertises it as the promptCapabilities
	// (image/audio/embeddedContext), and handleSessionPrompt consults it to
	// loud-reject prompt content the provider cannot consume — so a non-conformant
	// client that ignores the advertised caps still gets a clear error instead of a
	// silent drop.
	//
	// CAPTURE-ONCE IS CORRECT IN P0: ACP carries NO per-session provider/model
	// selector (session/new passes only mcpServers, never a selector), so every ACP
	// session rides the DEFAULT engine and a.caps is correct for every one of them.
	// svc.ProviderCapabilities() returns the composition-intersected DEFAULT caps —
	// the SAME value the gRPC/HTTP CreateSessionResponse echoes for a default-engine
	// session — so the ACP gate and the wire echo cannot disagree. A per-session ACP
	// capability gate (capture-once → per-session lookup) lands only when an ACP
	// selector lands (P1+); see docs/adr/0016-multi-provider.md.
	caps port.ProviderCapabilities

	// resume reports whether session/load is supported (a session store is
	// configured). When false, loadSession is advertised false in initialize and
	// session/load returns an error. The composition root sets it via WithResume.
	resume bool

	// fsDelegation records whether the CLIENT advertised BOTH fs.readTextFile and
	// fs.writeTextFile at initialize. When true, session/new registers a per-session
	// workspace that routes file Read/Write through the editor's buffers (fs/* calls)
	// instead of touching disk; when false (or initialize omitted), sessions use the
	// shared osfs workspace (the pre-delegation behavior). It is set once in
	// handleInitialize, which the ACP handshake guarantees precedes any session/new,
	// and only read thereafter on the same dispatch path — no lock needed.
	fsDelegation bool

	// diag is the operational-logging sink for the adapter's best-effort Debug lines
	// (notify/cancel/close/request_permission failures). Never nil after NewAgent
	// (defaulted to port.NopDiagnostics), so a caller that injects none stays silent
	// rather than nil-panicking. The composition root sets it via WithDiagnostics.
	diag port.Diagnostics
}

// AgentOption configures an Agent at construction.
type AgentOption func(*Agent)

// WithResume advertises session/load support (loadSession:true) and enables the
// session/load handler. The composition root passes true only when a durable
// session store is configured (mecated --store-dir), so resume is offered only
// when it can actually work.
func WithResume(enabled bool) AgentOption {
	return func(a *Agent) { a.resume = enabled }
}

// WithDiagnostics injects the operational-logging sink the adapter writes its
// best-effort Debug lines through. A nil sink is ignored (NewAgent's
// NopDiagnostics default stands), so the adapter never nil-panics.
func WithDiagnostics(d port.Diagnostics) AgentOption {
	return func(a *Agent) {
		if d != nil {
			a.diag = d
		}
	}
}

// NewAgent constructs an Agent over svc. The Conn is set by Serve so the Agent
// can issue outbound request_permission calls and session/update notifications.
func NewAgent(svc *server.Service, opts ...AgentOption) *Agent {
	a := &Agent{
		svc:         svc,
		inFlight:    make(map[string]struct{}),
		mcpSessions: make(map[string]struct{}),
		info:        implementation{Name: "mecatl", Version: "acp-phase3"},
		caps:        svc.ProviderCapabilities(),
		diag:        port.NopDiagnostics{},
	}
	for _, opt := range opts {
		opt(a)
	}
	return a
}

// Serve runs the ACP stdio session over conn and blocks until the input stream
// ends or ctx is cancelled. It is the entry the composition root calls under
// `mecated acp` (passing a Conn over os.Stdin/os.Stdout).
//
// ORDERING (load-bearing): Serve records conn as a.conn BEFORE conn.Serve starts
// dispatching inbound frames. a.conn is the outbound channel the handlers
// dereference — handleSessionPrompt's requestPermission issues an outbound Call,
// and notifyUpdate sends notifications, both through a.conn. Because the same
// conn handles inbound dispatch and a.conn is set first on this goroutine before
// any inbound frame is read, a handler can never observe a nil a.conn. Do not
// move the assignment after conn.Serve, and call Serve exactly once per Agent.
func (a *Agent) Serve(ctx context.Context, conn *Conn) error {
	a.conn = conn
	// On disconnect (the input stream ends or ctx is cancelled), tear down every
	// STILL-tracked per-session engine this connection created so a session's
	// client-provided MCP manager does not outlive the editor. This is the BACKSTOP
	// for sessions the client never explicitly ended: mid-session teardown is
	// handled by session/close (handleSessionClose, which untracks the id), and
	// process exit by Service.Close — this defer catches the rest, since Serve is
	// the connection's lifetime.
	defer a.closeTrackedSessions()
	return conn.Serve(ctx)
}

// trackSession records a session id created with a per-session engine, so the
// Serve loop tears it down on disconnect.
func (a *Agent) trackSession(sessionID string) {
	a.mu.Lock()
	a.mcpSessions[sessionID] = struct{}{}
	a.mu.Unlock()
}

// untrackSession removes a session id from the tracked set, symmetric to
// trackSession. It is called by handleSessionClose after a mid-session
// teardown so the eventual closeTrackedSessions on disconnect does NOT
// double-close that session's per-session engine. delete on an absent key is a
// no-op, so untracking a never-tracked (shared-engine) session is harmless.
func (a *Agent) untrackSession(sessionID string) {
	a.mu.Lock()
	delete(a.mcpSessions, sessionID)
	a.mu.Unlock()
}

// closeTrackedSessions closes every per-session engine this connection created,
// draining the tracking set so a second call is a no-op.
func (a *Agent) closeTrackedSessions() {
	a.mu.Lock()
	ids := make([]string, 0, len(a.mcpSessions))
	for id := range a.mcpSessions {
		ids = append(ids, id)
	}
	a.mcpSessions = make(map[string]struct{})
	a.mu.Unlock()
	for _, id := range ids {
		a.svc.CloseSession(session.SessionID(id))
	}
}

// Handle is the JSON-RPC Handler: it routes inbound ACP methods. A request
// (isRequest true) returns a result/error; a notification (session/cancel) acts
// and returns nil. An unknown method returns codeMethodNotFound.
func (a *Agent) Handle(ctx context.Context, method string, params json.RawMessage, isRequest bool) (any, error) {
	switch method {
	case methodInitialize:
		return a.handleInitialize(params)
	case methodSessionNew:
		return a.handleSessionNew(ctx, params)
	case methodSessionPrompt:
		return a.handleSessionPrompt(ctx, params)
	case methodSessionLoad:
		return a.handleSessionLoad(ctx, params)
	case methodSessionSetMode:
		return a.handleSetMode(ctx, params)
	case methodSessionClose:
		return a.handleSessionClose(ctx, params)
	case methodSessionCancel:
		a.handleSessionCancel(ctx, params)
		return nil, nil
	default:
		if !isRequest {
			return nil, nil // ignore unknown notifications
		}
		return nil, newMethodErr(codeMethodNotFound, "acp: unknown method "+method)
	}
}

// handleInitialize negotiates capabilities. The promptCapabilities now reflect
// the CONFIGURED PROVIDER (via the ProviderCapabilities seam read once at
// construction): image/audio mirror what the provider can consume, and
// embeddedContext mirrors EmbeddedContext (the adapter accepts inline-text
// resource blocks by flattening them into the prompt text). The OpenAI Responses
// provider declares image:true, embeddedContext:true, audio:false (its input
// content union has no audio member), so a typical mecated advertises image but
// not audio; a text-only provider advertises all three false. It also advertises
// the streaming-HTTP MCP transport (http:true) — a client may supply http MCP
// servers on session/new, which are mounted per-session; sse:false and stdio is
// hard-rejected (mecatl connects only streaming-HTTP MCP, never spawns a server
// process) — loadSession reflecting whether a session store is configured
// (WithResume), and sessionCapabilities.close:true UNCONDITIONALLY (mecatl can
// always end a session via session/close — see handleSessionClose), and echoes
// the protocol version it implements.
func (a *Agent) handleInitialize(params json.RawMessage) (any, error) {
	var req initializeRequest
	if len(params) > 0 {
		if err := json.Unmarshal(params, &req); err != nil {
			return nil, newMethodErr(codeInvalidParams, "acp: initialize: "+err.Error())
		}
	}
	// Decide file-I/O delegation from the CLIENT's advertised capabilities. Require
	// BOTH read and write: a read-only-delegating workspace would read the editor's
	// buffers but still write to disk, re-introducing exactly the buffer/disk
	// divergence delegation exists to remove. Absent caps leave this false (osfs).
	// This is a property of the request, NOT something the agent advertises back —
	// so it does not appear in the initialize response's agentCapabilities.
	a.fsDelegation = req.ClientCapabilities.FS.ReadTextFile && req.ClientCapabilities.FS.WriteTextFile
	return initializeResponse{
		ProtocolVersion: protocolVersion,
		AgentCapabilities: agentCapabilities{
			LoadSession:     a.resume,
			McpCapabilities: mcpCapabilities{HTTP: true, SSE: false},
			PromptCapabilities: promptCapabilities{
				Audio:           a.caps.Audio,
				EmbeddedContext: a.caps.EmbeddedContext,
				Image:           a.caps.Image,
			},
			// close is advertised UNCONDITIONALLY: mecatl can always end a session
			// (cancel any in-flight run + free per-session resources) — unlike
			// loadSession, which is store-gated, session/close needs no backing
			// resource. It is the mid-session teardown hook (see handleSessionClose).
			SessionCapabilities: sessionCapabilities{Close: true},
		},
		AuthMethods: []any{},
		AgentInfo:   &a.info,
	}, nil
}

// handleSessionNew creates a mecatl session rooted at the client's cwd via
// Service.CreateSessionWithMCP, and returns its id. It ACCEPTS client-provided
// streaming-HTTP MCP servers — they are validated and mounted PER-SESSION (so
// their tools and auth never leak into other sessions). A stdio (command-shaped)
// entry and an sse entry are hard-rejected (CLAUDE.md: no stdio MCP, ever; mecatl
// never spawns a server process). The session mode is always default this phase;
// the available modes are reflected so the editor can show them.
func (a *Agent) handleSessionNew(ctx context.Context, params json.RawMessage) (any, error) {
	var req newSessionRequest
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, newMethodErr(codeInvalidParams, "acp: session/new: "+err.Error())
	}
	if err := validateCwd(req.Cwd, "session/new"); err != nil {
		return nil, err
	}
	specs, err := partitionClientMCP(req.McpServers, "session/new")
	if err != nil {
		return nil, err
	}

	sess, err := a.svc.CreateSessionWithMCP(ctx, req.Cwd, session.ModeDefault, session.Limits{}, specs)
	if err != nil {
		return nil, newMethodErr(codeInvalidParams, "acp: session/new: "+err.Error())
	}
	// Track sessions created with a per-session engine so the Serve loop can tear
	// them down (and their client MCP managers) on editor disconnect. A session
	// with no client MCP uses the shared engine and is not tracked.
	if len(specs) > 0 {
		a.trackSession(string(sess.ID))
	}
	// When the client advertised fs.readTextFile && fs.writeTextFile, register a
	// per-session workspace that routes file Read/Write through the editor's
	// buffers (fs/* calls) for THIS session. It composes an osfs workspace rooted
	// at the same cwd for Root/Stat/Glob/Grep and overrides Read/Write plus the
	// read-ledger to delegate. The override is keyed by session id on the shared
	// Service (mirroring the per-session engine registry) and torn down on
	// disconnect alongside the engines. Registration is independent of client MCP:
	// it applies to every session/new while delegating, so we also track the
	// session for teardown even when it carries no MCP servers.
	if a.fsDelegation {
		ws, werr := newFSWorkspace(a.conn, string(sess.ID), sess.Workspace)
		if werr != nil {
			// A bad cwd (osfs could not root there) is a session-creation failure: the
			// session exists but its workspace cannot be built, so fail loudly rather
			// than silently falling back to disk under a client that asked for buffers.
			return nil, newMethodErr(codeInvalidParams, "acp: session/new: "+werr.Error())
		}
		// Register a COMPLETE shell-less Environment: the ACP fsWorkspace is a
		// real-filesystem workspace rooted at the session cwd (a local-kind
		// namespace), but the editor provides NO shell, so the CommandRunner is
		// nil (Bash surfaces ErrNoShell honestly). The ref ID is the session root
		// so the parent can identify the namespace (issue #462 phase-2 finding #2).
		env := tool.MustEnvironment(session.EnvironmentRef{Kind: session.EnvKindLocal, ID: ws.Root()}, ws, nil)
		a.svc.SetSessionEnvironment(sess.ID, env)
		if len(specs) == 0 {
			// Not already tracked via the MCP path; track now so the override is
			// evicted on disconnect.
			a.trackSession(string(sess.ID))
		}
	}
	// Advertise the slash commands for this workspace as an available_commands_update
	// so the editor can offer them in its input palette. Best-effort: a discovery
	// fault or an empty list simply means no (or an empty) update — it must not fail
	// session creation. Sent AFTER the session exists so the notification's
	// sessionId is valid.
	a.notifyAvailableCommands(ctx, string(sess.ID), sess.Workspace)

	return newSessionResponse{
		SessionID: string(sess.ID),
		Modes:     modeStateFor(sess.Mode),
	}, nil
}

// modeStateFor builds the ACP sessionModeState advertising mecatl's three
// permission modes with current as the session's CURRENT mode. It is shared by
// session/new and session/load so both seed the editor's mode picker identically.
func modeStateFor(current session.PermissionMode) *sessionModeState {
	return &sessionModeState{
		CurrentModeID: string(current),
		AvailableModes: []sessionMode{
			{ID: string(session.ModeDefault), Name: "Default", Description: "Standard deny/ask/allow permissions"},
			{ID: string(session.ModePlan), Name: "Plan", Description: "Read-only planning (no mutations)"},
			{ID: string(session.ModeAccept), Name: "Accept Edits", Description: "Auto-accept edits"},
		},
	}
}

// notifyAvailableCommands lists the slash commands for the workspace via the
// Service seam and, when any are found, pushes an available_commands_update. A
// nil lister, an empty result, or a discovery fault yields NO update (the palette
// stays empty) — command discovery must never break the session.
func (a *Agent) notifyAvailableCommands(ctx context.Context, sessionID, workspace string) {
	cmds, err := a.svc.ListCommands(ctx, workspace)
	if err != nil {
		a.diag.Log(ctx, port.LevelDebug, "acp: list commands failed", "session", sessionID, "err", err)
		return
	}
	if len(cmds) == 0 {
		return
	}
	out := make([]availableCommand, 0, len(cmds))
	for _, c := range cmds {
		out = append(out, availableCommand{Name: c.Name, Description: c.Description})
	}
	a.notifyUpdate(ctx, sessionID, availableCommandsUpdate{
		SessionUpdate:     updateAvailableCommands,
		AvailableCommands: out,
	})
}

// handleSessionLoad resumes a previously-persisted session so the next
// session/prompt continues it. It validates cwd exactly like session/new and —
// like session/new — ACCEPTS client-provided streaming-HTTP MCP servers: they are
// validated (via the SAME partitionClientMCP helper, so stdio/sse reject, the SSRF
// allowlist, the server cap, and the bounded connect timeout all apply identically)
// and RE-MOUNTED PER-SESSION via Service.LoadSessionWithMCP. It then loads (and, if
// the session had cleanly completed, reopens) the session and REPLAYS the persisted
// conversation as session/update notifications (see
// replayHistory): a re-attaching editor would otherwise see an empty transcript,
// so we re-project the stored Conversation through the same projectUpdate path the
// live loop uses, rebuilding the message chunks, tool_call cards, and their
// updates. The replay runs synchronously here, so the notifications are flushed
// BEFORE this load response returns. It is idempotent — a repeated load simply
// re-streams the same transcript, keyed by tool-call id, so each card is reopened
// and re-settled identically (no dedupe guard needed). The user's own prompts, the
// opaque reasoning replay blob, and any historical permission.ask are deliberately
// NOT replayed (see replay.go). It returns the resumed mode state so the editor
// seeds its mode picker.
//
// When resume is disabled (no session store — WithResume(false)), the handler is
// effectively unreachable because loadSession is advertised false; we still guard
// it so a client that calls it anyway gets a clean method error rather than a
// surprising load against an in-memory store.
func (a *Agent) handleSessionLoad(ctx context.Context, params json.RawMessage) (any, error) {
	if !a.resume {
		return nil, newMethodErr(codeMethodNotFound, "acp: session/load: not supported (no session store configured)")
	}
	var req loadSessionRequest
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, newMethodErr(codeInvalidParams, "acp: session/load: "+err.Error())
	}
	if req.SessionID == "" {
		return nil, newMethodErr(codeInvalidParams, "acp: session/load: sessionId is required")
	}
	if err := validateCwd(req.Cwd, "session/load"); err != nil {
		return nil, err
	}
	// Re-mount any client-provided streaming-HTTP MCP servers PER-SESSION, exactly as
	// session/new does — the SAME partitionClientMCP helper enforces stdio/sse reject,
	// the SSRF allowlist, the server cap, and the bounded connect timeout (its method
	// param drives the error prefix).
	specs, err := partitionClientMCP(req.McpServers, "session/load")
	if err != nil {
		return nil, err
	}
	// NOTE: session/load deliberately does NOT register an fs/* workspace override; a
	// resumed session stays on osfs (fs/* delegation on load is a separate deferred
	// item — see ADR 0001 "Deferred").

	sess, err := a.svc.LoadSessionWithMCP(ctx, session.SessionID(req.SessionID), specs)
	if err != nil {
		// An unknown/never-persisted session (incl. the in-memory store after a
		// restart) is a client error: the id does not resolve.
		return nil, newMethodErr(codeInvalidParams, "acp: session/load: "+err.Error())
	}
	// Track sessions resumed with a per-session engine so the Serve loop tears them
	// down (and their re-mounted client MCP managers) on editor disconnect — identical
	// to session/new. A session resumed with no client MCP uses the shared engine and
	// is not tracked.
	if len(specs) > 0 {
		a.trackSession(string(sess.ID))
	}
	// Rebuild the editor's transcript from the persisted history BEFORE returning,
	// so a re-attaching editor sees the prior turns rather than an empty session.
	a.replayHistory(ctx, req.SessionID, sess.Conversation)
	return loadSessionResponse{Modes: modeStateFor(sess.Mode)}, nil
}

// handleSetMode applies a session/set_mode by mapping the ACP modeId to a mecatl
// PermissionMode and calling Service.SetMode. On a successful change it emits a
// current_mode_update so the editor's picker reflects the new selection. An
// unknown modeId is rejected; a mid-turn change is rejected by the aggregate
// (Service.SetMode wraps the illegal transition) — the client must defer it to
// the next prompt. The ACP result is the empty SetSessionModeResponse.
func (a *Agent) handleSetMode(ctx context.Context, params json.RawMessage) (any, error) {
	var req setModeRequest
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, newMethodErr(codeInvalidParams, "acp: session/set_mode: "+err.Error())
	}
	if req.SessionID == "" {
		return nil, newMethodErr(codeInvalidParams, "acp: session/set_mode: sessionId is required")
	}
	mode, ok := modeFromACP(req.ModeID)
	if !ok {
		return nil, newMethodErr(codeInvalidParams,
			fmt.Sprintf("acp: session/set_mode: unknown modeId %q", req.ModeID))
	}
	sess, err := a.svc.SetMode(ctx, session.SessionID(req.SessionID), mode)
	if err != nil {
		return nil, newMethodErr(codeInvalidParams, "acp: session/set_mode: "+err.Error())
	}
	// Confirm the change to the editor. SetMode is a no-op when the mode is
	// unchanged, but emitting the update unconditionally keeps the picker
	// authoritative and is harmless (the editor sets the value it already has).
	a.notifyUpdate(ctx, req.SessionID, currentModeUpdate{
		SessionUpdate: updateCurrentMode,
		CurrentModeID: string(sess.Mode),
	})
	return setModeResponse{}, nil
}

// modeFromACP maps an ACP modeId to a mecatl PermissionMode. The ids are mecatl's
// own mode strings (advertised verbatim as the availableModes ids on
// session/new), so this is an identity-with-validation map: an unrecognized id
// yields ok=false so the handler can reject it.
func modeFromACP(modeID string) (session.PermissionMode, bool) {
	switch session.PermissionMode(modeID) {
	case session.ModeDefault:
		return session.ModeDefault, true
	case session.ModePlan:
		return session.ModePlan, true
	case session.ModeAccept:
		return session.ModeAccept, true
	default:
		return "", false
	}
}

// handleSessionPrompt runs one prompt to completion. It enforces one in-flight
// prompt per session, translates the content blocks into the prompt's flattened
// text PLUS media parts (buildPromptContent — text/inline-text-resource flatten,
// image/audio become Parts, resource_link/unknown reject loudly), CAPABILITY-GATES
// the media (an image/audio Part the configured provider cannot consume is
// rejected loudly BEFORE the run starts, so StartRunContent is never reached for
// unsupported content), starts a run via Service.StartRunContent, drains the run's
// Events projecting each to a session/update notification (and handling
// permission.ask out of band as an outbound request_permission), and returns
// {stopReason} only when the terminal EvResult arrives. It BLOCKS for the whole
// turn, exactly as ACP requires.
func (a *Agent) handleSessionPrompt(ctx context.Context, params json.RawMessage) (any, error) {
	var req promptRequest
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, newMethodErr(codeInvalidParams, "acp: session/prompt: "+err.Error())
	}
	if req.SessionID == "" {
		return nil, newMethodErr(codeInvalidParams, "acp: session/prompt: sessionId is required")
	}
	text, parts, err := buildPromptContent(req.Prompt)
	if err != nil {
		return nil, err
	}
	// Capability-gate the media BEFORE starting the run: reject loudly any part the
	// configured provider cannot consume, so a non-conformant client that ignores
	// the advertised promptCapabilities gets a clear error rather than a silent
	// drop, and StartRunContent is never reached for unsupported content.
	// (resource_link is already rejected in buildPromptContent.)
	if cerr := a.rejectUnsupportedMedia(parts); cerr != nil {
		return nil, cerr
	}
	if strings.TrimSpace(text) == "" && len(parts) == 0 {
		return nil, newMethodErr(codeInvalidParams, "acp: session/prompt: prompt has no content")
	}

	if !a.acquire(req.SessionID) {
		return nil, newMethodErr(codeInvalidParams, "acp: session/prompt: a prompt is already in flight for this session")
	}
	defer a.release(req.SessionID)

	run, err := a.svc.StartRunContent(ctx, session.SessionID(req.SessionID), text, parts)
	if err != nil {
		return nil, newMethodErr(codeInvalidParams, "acp: session/prompt: "+err.Error())
	}
	// Remove the run from the Service registry once we finish draining its events,
	// mirroring the gRPC/HTTP adapters' `defer ... FinishRun(...)`. Without this a
	// long-lived editor session leaks a dead run per prompt.
	defer a.svc.FinishRun(session.SessionID(req.SessionID), run)

	stop := stopEndTurn
	for ev := range run.Events() {
		switch ev.Type {
		case session.EvPermissionAsk:
			if ev.Ask != nil {
				// Persist the awaiting snapshot so a session/load after a restart can
				// re-attach to a paused session (mirrors the gRPC/HTTP adapters). With
				// session/load now landed this is no longer a dead snapshot.
				a.svc.Persist(ctx, session.SessionID(req.SessionID))
				// Out-of-band: ask the editor, then resolve the run. Run on its own
				// goroutine so draining the event channel never blocks behind the
				// editor's reply (the loop is paused awaiting Approve anyway, but
				// concurrent tool results from a parallel read still flow).
				a.requestPermission(ctx, req.SessionID, run, *ev.Ask)
			}
		case session.EvResult:
			if ev.Result != nil {
				stop = stopReasonFor(ev.Result.Stop)
			}
		default:
			if update, ok := projectUpdate(ev); ok {
				a.notifyUpdate(ctx, req.SessionID, update)
			}
		}
	}
	// The event channel has closed, so the run reached a terminal state. Persist
	// the final snapshot (conversation + counters + terminal state) BEFORE the
	// deferred FinishRun deregisters the run — Persist is a no-op once the run is
	// gone. This is what makes session/load resume a continuable session: the
	// engine mutates the session in place, and only this Save captures the
	// completed turn's history durably.
	a.svc.Persist(ctx, session.SessionID(req.SessionID))
	return promptResponse{StopReason: stop}, nil
}

// handleSessionCancel cancels the in-flight run for the session. The blocked
// session/prompt then resolves with stopReason:"cancelled".
func (a *Agent) handleSessionCancel(ctx context.Context, params json.RawMessage) {
	var n cancelNotification
	if err := json.Unmarshal(params, &n); err != nil || n.SessionID == "" {
		return
	}
	if err := a.svc.Cancel(ctx, session.SessionID(n.SessionID), ""); err != nil {
		a.diag.Log(ctx, port.LevelDebug, "acp: session/cancel", "session", n.SessionID, "err", err)
	}
}

// handleSessionClose implements the ACP session/close request: the first-class
// mid-session-end hook. Per spec it MUST cancel ongoing work, THEN free the
// session's resources, so the adapter COMPOSES svc.Cancel + svc.EndSession.
//
// This is NOT behaviorally identical to the gRPC CloseSession RPC / HTTP DELETE:
// those call svc.EndSession ONLY and deliberately do NOT cancel an in-flight run
// (cancel is a separate endpoint there — see service.go EndSession + http.go).
// The three surfaces share the EndSession TEARDOWN STEP, not the full behaviour.
// Composing Cancel + EndSession at the ADAPTER (rather than baking cancel into
// EndSession) is deliberate: it keeps the shared core minimal and leaves the
// gRPC/HTTP contract (close ≠ cancel) unchanged.
//
//  1. svc.Cancel drives any in-flight run to its terminal EvResult (the blocked
//     handleSessionPrompt then returns stopReason "cancelled"). It is
//     best-effort: ErrNoActiveRun/ErrNotFound (no run in flight — the common
//     case) are ignored. Cancelling FIRST (rather than yanking the engine from
//     under a live run) lets the run unwind so its deferred FinishRun
//     deregisters it before teardown closes the engine — the same documented
//     concurrency-safe race profile as the disconnect path.
//  2. untrackSession removes the id from the tracked set so the eventual
//     closeTrackedSessions on disconnect does NOT double-close this session's
//     per-session engine (the load-bearing guard — see TestSessionClose_*).
//  3. svc.EndSession runs the precondition-checked teardown (the step shared with
//     the gRPC/HTTP surfaces): ErrNotFound for a never-created id maps to
//     codeInvalidParams — mirroring this adapter's OWN session/load unknown-id
//     mapping (ACP JSON-RPC has no not-found code), NOT the gRPC/HTTP close path
//     (which map ErrNotFound to codes.NotFound / HTTP 404). A created session
//     tears down idempotently (a repeated session/close succeeds because the
//     persisted snapshot still resolves).
func (a *Agent) handleSessionClose(ctx context.Context, params json.RawMessage) (any, error) {
	var req closeSessionRequest
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, newMethodErr(codeInvalidParams, "acp: session/close: "+err.Error())
	}
	if req.SessionID == "" {
		return nil, newMethodErr(codeInvalidParams, "acp: session/close: sessionId is required")
	}
	id := session.SessionID(req.SessionID)
	// Cancel ongoing work first (best-effort: no live run is the common case).
	if err := a.svc.Cancel(ctx, id, ""); err != nil &&
		!errors.Is(err, server.ErrNoActiveRun) && !errors.Is(err, server.ErrNotFound) {
		a.diag.Log(ctx, port.LevelDebug, "acp: session/close: cancel", "session", req.SessionID, "err", err)
	}
	// Untrack BEFORE teardown so a later closeTrackedSessions on disconnect skips
	// this id (no double-close). delete on an absent key is a no-op, so untracking
	// a never-tracked (shared-engine) session is harmless.
	a.untrackSession(req.SessionID)
	if err := a.svc.EndSession(ctx, id); err != nil {
		return nil, newMethodErr(codeInvalidParams, "acp: session/close: "+err.Error())
	}
	return closeSessionResponse{}, nil
}

// requestPermission issues the outbound session/request_permission, awaits the
// editor's reply, and resolves the paused run with the mapped verdict. A
// transport error or a cancelled context denies the ask (fail-safe), so the run
// never hangs waiting on an approval that will not come.
func (a *Agent) requestPermission(ctx context.Context, sessionID string, run runApprover, ask session.PendingAsk) {
	go func() {
		var resp requestPermissionResponse
		err := a.conn.Call(ctx, methodRequestPermission, permissionRequestFor(sessionID, ask), &resp)
		if err != nil {
			a.diag.Log(ctx, port.LevelDebug, "acp: request_permission failed; denying", "session", sessionID, "ask", ask.AskID, "err", err)
			run.Approve(ask.AskID, session.VerdictDeny)
			return
		}
		run.Approve(ask.AskID, approvalFor(resp.Outcome))
	}()
}

// notifyUpdate pushes one session/update notification, logging (not failing) a
// write error: a dropped notification must not abort the in-flight prompt. ctx is
// the dispatch context, forwarded to diag.Log only as a trace/baggage carrier (the
// Notify itself is fire-and-forget — the adapter derives no cancellation from it).
func (a *Agent) notifyUpdate(ctx context.Context, sessionID string, update any) {
	if err := a.conn.Notify(methodSessionUpdate, sessionNotification{SessionID: sessionID, Update: update}); err != nil {
		a.diag.Log(ctx, port.LevelDebug, "acp: session/update notify failed", "session", sessionID, "err", err)
	}
}

// acquire marks a session's prompt in flight, returning false if one already is.
func (a *Agent) acquire(sessionID string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if _, busy := a.inFlight[sessionID]; busy {
		return false
	}
	a.inFlight[sessionID] = struct{}{}
	return true
}

func (a *Agent) release(sessionID string) {
	a.mu.Lock()
	delete(a.inFlight, sessionID)
	a.mu.Unlock()
}

// runApprover is the subset of *agent.Run the permission round-trip needs,
// narrowed so requestPermission is unit-testable with a fake.
type runApprover interface {
	Approve(askID string, verdict session.ApprovalVerdict)
}

// validateCwd enforces ACP's absolute-existing-directory cwd contract, shared by
// session/new and session/load. ACP contracts cwd as an ABSOLUTE path, and
// CreateSession -> osfs would silently CREATE a missing dir; we fail loudly
// instead, so a non-absolute or typo'd/nonexistent cwd is rejected rather than
// spawning a session rooted at an accidentally-created directory. (os.Root
// confines tool I/O, so this is input validation, CWE-20, not an escape.) method
// is the ACP method name for the error prefix.
func validateCwd(cwd, method string) error {
	if strings.TrimSpace(cwd) == "" {
		return newMethodErr(codeInvalidParams, "acp: "+method+": cwd is required (absolute path)")
	}
	if !filepath.IsAbs(cwd) {
		return newMethodErr(codeInvalidParams,
			fmt.Sprintf("acp: %s: cwd %q must be an absolute path", method, cwd))
	}
	if info, statErr := os.Stat(cwd); statErr != nil || !info.IsDir() {
		return newMethodErr(codeInvalidParams,
			fmt.Sprintf("acp: %s: cwd %q must be an existing directory", method, cwd))
	}
	return nil
}

// maxClientMCPServers caps how many MCP servers one client may declare on
// session/new. The factory connects them SERIALLY, each bounded by
// clientMCPConnectTimeout, so an uncapped count would let a client stall a single
// session/new for count × timeout (CWE-400, resource exhaustion). 8 is generous for
// a real editor while bounding the worst-case connect wall-clock.
const maxClientMCPServers = 8

// clientMCPConnectTimeout bounds the connect handshake + tool listing for ONE
// client-provided MCP server, deliberately shorter than the operator-path
// defaultConnectTimeout (30s): a slow client server must not hold session/new open
// for the full operator budget. It rides on each spec's ServerConfig.Timeout seam.
const clientMCPConnectTimeout = 10 * time.Second

// partitionClientMCP classifies client-provided MCP server entries and returns
// the streaming-HTTP ones as mcp.ServerConfig specs to mount per-session. It is
// FAIL-LOUD: the first bad entry rejects the whole request, so a session never
// silently drops a server the client asked for.
//
// Classification per entry:
//
//   - STDIO — type=="stdio", or type=="" with a non-empty Command: hard-rejected
//     with a "stdio MCP" message (mecatl never spawns an MCP server process).
//   - SSE — type=="sse": rejected ("sse transport not supported").
//   - HTTP — type=="http", or type=="" with a non-empty URL: validated via
//     mcp.ValidateClientURL (SSRF scheme allowlist) and, on success, appended as a
//     mcp.ServerConfig carrying the entry's Name, URL, mapped Headers, and a
//     bounded per-server connect Timeout (clientMCPConnectTimeout).
//
// It rejects a request declaring more than maxClientMCPServers (CWE-400: the
// servers connect serially, so an unbounded count could stall session/new). It
// returns nil specs (no error) for an empty server list, so a session/new with no
// mcpServers takes the shared-engine path. Header VALUES are never logged.
func partitionClientMCP(servers []mcpServer, method string) ([]mcp.ServerConfig, error) {
	if len(servers) == 0 {
		return nil, nil
	}
	if len(servers) > maxClientMCPServers {
		return nil, newMethodErr(codeInvalidParams,
			fmt.Sprintf("acp: %s: too many MCP servers (%d > %d max)", method, len(servers), maxClientMCPServers))
	}
	specs := make([]mcp.ServerConfig, 0, len(servers))
	for _, m := range servers {
		switch {
		case m.Type == "stdio" || (m.Type == "" && m.Command != ""):
			return nil, newMethodErr(codeInvalidParams,
				fmt.Sprintf("acp: %s: stdio MCP server %q rejected (mecatl is streaming-HTTP MCP only)", method, m.Name))
		case m.Type == "sse":
			return nil, newMethodErr(codeInvalidParams,
				fmt.Sprintf("acp: %s: sse transport not supported for MCP server %q (streaming-HTTP only)", method, m.Name))
		case m.Type == "http" || (m.Type == "" && m.URL != ""):
			if verr := mcp.ValidateClientURL(m.URL); verr != nil {
				return nil, newMethodErr(codeInvalidParams,
					fmt.Sprintf("acp: %s: MCP server %q rejected: %v", method, m.Name, verr))
			}
			specs = append(specs, mcp.ServerConfig{
				Name:    m.Name,
				URL:     m.URL,
				Headers: headerMap(m.Headers),
				Timeout: clientMCPConnectTimeout,
			})
		default:
			return nil, newMethodErr(codeInvalidParams,
				fmt.Sprintf("acp: %s: MCP server %q has no recognized transport (need http url)", method, m.Name))
		}
	}
	return specs, nil
}

// headerMap collapses the ACP []mcpHeader list into the map[string]string shape
// mcp.ServerConfig.Headers expects. An empty/absent list yields nil so a server
// with no headers carries no transport headers. Empty header names are dropped.
func headerMap(headers []mcpHeader) map[string]string {
	if len(headers) == 0 {
		return nil
	}
	out := make(map[string]string, len(headers))
	for _, h := range headers {
		if h.Name == "" {
			continue
		}
		out[h.Name] = h.Value
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// buildPromptContent translates an inbound ACP prompt — a list of ContentBlocks —
// into mecatl's multimodal prompt shape: the flattened text PLUS the non-text
// media parts. It is the wire→domain choke point for ACP-supplied content, so it
// reuses the SAME validating constructors (session.NewImageContent / NewImageURLContent
// / NewAudioContent / NewAudioURLContent) and the SAME per-prompt size caps
// (session.ValidateMediaParts) as the gRPC/HTTP surfaces — the SSRF (CWE-918),
// mime-consistency, exactly-one-of(data,url), and size (CWE-770) guarantees hold
// at the ACP boundary exactly as elsewhere; there is no second validation path.
//
// It is FAIL-LOUD by design (the honesty fix this issue exists for): NO block is
// ever silently dropped. Each block becomes prompt text, a media Part, or a loud
// error:
//
//   - text                          → appended to the flattened text.
//   - resource (inline TEXT)        → flattened into the text (an embedded-text
//     resource; no Part).
//   - resource (blob+image/audio mime) → a media Part via the constructors.
//   - resource_link                 → REJECTED (a URI mecatl cannot fetch).
//   - image / audio                 → a media Part (inline base64 Data, or a URL).
//   - any other / unsupported type  → REJECTED.
//
// Errors are returned as codeInvalidParams MethodErrors so the caller surfaces
// them verbatim to the editor.
func buildPromptContent(blocks []contentBlock) (text string, parts []session.Content, err error) {
	var textParts []string
	for i, b := range blocks {
		switch b.Type {
		case "text":
			if b.Text != "" {
				textParts = append(textParts, b.Text)
			}
		case "resource":
			rtext, part, rerr := resourceToContent(i, b.Resource)
			if rerr != nil {
				return "", nil, rerr
			}
			if rtext != "" {
				textParts = append(textParts, rtext)
			}
			if part != nil {
				parts = append(parts, *part)
			}
		case "resource_link":
			return "", nil, newMethodErr(codeInvalidParams,
				fmt.Sprintf("acp: session/prompt: prompt[%d] is a resource_link (%q) — a URI mecatl cannot fetch", i, b.URI))
		case "image":
			c, cerr := mediaContent(i, session.MediaImage, b.MimeType, b.Data, b.URI)
			if cerr != nil {
				return "", nil, cerr
			}
			parts = append(parts, c)
		case "audio":
			c, cerr := mediaContent(i, session.MediaAudio, b.MimeType, b.Data, b.URI)
			if cerr != nil {
				return "", nil, cerr
			}
			parts = append(parts, c)
		default:
			return "", nil, newMethodErr(codeInvalidParams,
				fmt.Sprintf("acp: session/prompt: prompt[%d] has unsupported content type %q", i, b.Type))
		}
	}
	if verr := session.ValidateMediaParts(parts); verr != nil {
		return "", nil, newMethodErr(codeInvalidParams, "acp: session/prompt: "+verr.Error())
	}
	return strings.Join(textParts, "\n"), parts, nil
}

// resourceToContent handles a "resource" ContentBlock. An inline-TEXT resource is
// flattened into the prompt text (returned as rtext, no Part — the EmbeddedContext
// path). A binary (blob) resource whose mime names image/* or audio/* becomes that
// media Part. A resource with neither text nor blob, or a blob with a non-media
// mime, is a loud error (never silent-dropped).
func resourceToContent(idx int, r *resourceContents) (rtext string, part *session.Content, err error) {
	if r == nil {
		return "", nil, newMethodErr(codeInvalidParams,
			fmt.Sprintf("acp: session/prompt: prompt[%d] resource has no contents", idx))
	}
	switch {
	case r.Text != "" && r.Blob != "":
		// Ambiguous: ACP resource contents are text XOR blob. Reject loudly rather
		// than silently picking one (this issue's whole point is no silent drop).
		return "", nil, newMethodErr(codeInvalidParams,
			fmt.Sprintf("acp: session/prompt: prompt[%d] resource has both text and blob contents (expected one)", idx))
	case r.Text != "":
		// Embedded-text resource: flatten into the prompt text, no media Part.
		return r.Text, nil, nil
	case r.Blob != "":
		kind, kerr := mediaKindForMIME(r.MimeType)
		if kerr != nil {
			return "", nil, newMethodErr(codeInvalidParams,
				fmt.Sprintf("acp: session/prompt: prompt[%d] resource blob mime %q is not image/* or audio/*", idx, r.MimeType))
		}
		c, cerr := mediaContent(idx, kind, r.MimeType, r.Blob, "")
		if cerr != nil {
			return "", nil, cerr
		}
		return "", &c, nil
	default:
		return "", nil, newMethodErr(codeInvalidParams,
			fmt.Sprintf("acp: session/prompt: prompt[%d] resource has neither text nor blob contents", idx))
	}
}

// mediaContent builds one validated media Content from an ACP image/audio block:
// inline base64 data XOR a URL, with mime. It base64-decodes Data (a decode
// failure is a loud error) and routes through the session.NewContent validating
// constructors so the exactly-one-of, mime, and URL-SSRF invariants are enforced
// once, identically to the gRPC/HTTP mappers. A block carrying BOTH data and uri,
// or NEITHER, is rejected by the constructor's exactly-one-of check.
func mediaContent(idx int, kind session.MediaKind, mime, b64Data, rawURL string) (session.Content, error) {
	if b64Data != "" {
		raw, derr := base64.StdEncoding.DecodeString(b64Data)
		if derr != nil {
			return session.Content{}, newMethodErr(codeInvalidParams,
				fmt.Sprintf("acp: session/prompt: prompt[%d] %s data is not valid base64: %v", idx, kind, derr))
		}
		c, cerr := session.NewContent(kind, mime, raw, "")
		if cerr != nil {
			return session.Content{}, newMethodErr(codeInvalidParams,
				fmt.Sprintf("acp: session/prompt: prompt[%d]: %v", idx, cerr))
		}
		return c, nil
	}
	c, cerr := session.NewContent(kind, mime, nil, rawURL)
	if cerr != nil {
		return session.Content{}, newMethodErr(codeInvalidParams,
			fmt.Sprintf("acp: session/prompt: prompt[%d]: %v", idx, cerr))
	}
	return c, nil
}

// mediaKindForMIME maps an IANA mime prefix to the media kind for a blob resource.
// Only image/* and audio/* are recognized; anything else is an error so a blob
// resource of an unsupported type is rejected loudly rather than dropped.
func mediaKindForMIME(mime string) (session.MediaKind, error) {
	switch {
	case strings.HasPrefix(strings.ToLower(mime), "image/"):
		return session.MediaImage, nil
	case strings.HasPrefix(strings.ToLower(mime), "audio/"):
		return session.MediaAudio, nil
	default:
		return "", errors.New("unsupported media mime")
	}
}

// rejectUnsupportedMedia loud-rejects any media Part the configured provider
// cannot consume, consulting the capabilities read once at construction. It is
// defense-in-depth: handleInitialize already advertised the provider's caps, but a
// non-conformant client may send an image/audio block anyway — it gets a clear
// codeInvalidParams error here, never a silent drop. resource_link is already
// rejected upstream in buildPromptContent.
func (a *Agent) rejectUnsupportedMedia(parts []session.Content) error {
	for _, p := range parts {
		switch p.Kind {
		case session.MediaImage:
			if !a.caps.Image {
				return newMethodErr(codeInvalidParams,
					"acp: session/prompt: image content not supported by the configured provider")
			}
		case session.MediaAudio:
			if !a.caps.Audio {
				return newMethodErr(codeInvalidParams,
					"acp: session/prompt: audio content not supported by the configured provider")
			}
		}
	}
	return nil
}
