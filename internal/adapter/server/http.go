package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/session"
)

// HTTPHandler is the HTTP/SSE adapter over the shared Service. It serves the
// thin REST surface from ARCHITECTURE §7.2:
//
//	POST   /v1/sessions               -> CreateSession (JSON)
//	GET    /v1/sessions/{id}          -> GetSession (JSON snapshot)
//	DELETE /v1/sessions/{id}          -> CloseSession (release session resources; 204)
//	POST   /v1/sessions/{id}/prompt   -> start a run; text/event-stream of Events
//	POST   /v1/sessions/{id}/approve  -> resolve the paused ask on the run
//	POST   /v1/sessions/{id}/cancel   -> cancel the in-flight run
//	POST   /v1/sessions/{id}/cancel-child -> cancel ONE child (subagent) of the run
//
// Every Event is emitted as one SSE `data:` line carrying the proto Event
// marshalled to JSON, so the HTTP and gRPC surfaces share one event shape.
type HTTPHandler struct {
	svc *Service
	mux *http.ServeMux
}

// NewHTTPHandler constructs an HTTPHandler over svc. The returned value is an
// http.Handler ready to mount.
func NewHTTPHandler(svc *Service) *HTTPHandler {
	h := &HTTPHandler{svc: svc, mux: http.NewServeMux()}
	h.mux.HandleFunc("POST /v1/sessions", h.createSession)
	h.mux.HandleFunc("GET /v1/sessions/{id}", h.getSession)
	h.mux.HandleFunc("POST /v1/sessions/{id}/mode", h.setMode)
	h.mux.HandleFunc("DELETE /v1/sessions/{id}", h.closeSession)
	h.mux.HandleFunc("POST /v1/sessions/{id}/prompt", h.prompt)
	h.mux.HandleFunc("POST /v1/sessions/{id}/approve", h.approve)
	h.mux.HandleFunc("POST /v1/sessions/{id}/cancel", h.cancel)
	h.mux.HandleFunc("POST /v1/sessions/{id}/cancel-child", h.cancelChild)
	h.mux.HandleFunc("GET /v1/mcp/resources", h.listMcpResources)
	h.mux.HandleFunc("GET /v1/mcp/resources/read", h.readMcpResource)
	h.mux.HandleFunc("GET /v1/mcp/prompts", h.listMcpPrompts)
	h.mux.HandleFunc("POST /v1/mcp/prompts/get", h.getMcpPrompt)
	h.mux.HandleFunc("GET /v1/mcp/sources", h.listMcpSources)
	h.mux.HandleFunc("GET /v1/mcp/toolhive/groups", h.listToolHiveGroups)
	h.mux.HandleFunc("GET /v1/agents", h.listAgents)
	h.mux.HandleFunc("GET /v1/skills", h.listSkills)
	h.mux.HandleFunc("GET /v1/models", h.listModels)
	h.mux.HandleFunc("GET /v1/soul", h.getSoul)
	h.mux.HandleFunc("GET /v1/usermodel", h.getUserModel)
	h.mux.HandleFunc("GET /v1/commands", h.listCommands)
	h.mux.HandleFunc("GET /v1/worktrees", h.listWorktrees)
	h.mux.HandleFunc("POST /v1/teams", h.createTeam)
	h.mux.HandleFunc("POST /v1/teams/{id}/members", h.spawnTeammate)
	h.mux.HandleFunc("POST /v1/teams/{id}/messages", h.sendTeammateMessage)
	h.mux.HandleFunc("POST /v1/teams/{id}/members/cancel", h.cancelTeammate)
	h.mux.HandleFunc("POST /v1/teams/{id}/run", h.runTeam)
	h.mux.HandleFunc("GET /v1/teams/{id}", h.listTeam)
	h.mux.HandleFunc("DELETE /v1/teams/{id}", h.cleanupTeam)
	return h
}

// ServeHTTP routes to the registered handlers.
func (h *HTTPHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.mux.ServeHTTP(w, r)
}

// --- request/response bodies ------------------------------------------------

type createSessionBody struct {
	Workspace string    `json:"workspace"`
	Mode      string    `json:"mode,omitempty"`
	Limits    *limitsIn `json:"limits,omitempty"`
	// ProviderID / ModelID select a per-session provider+model (multi-provider
	// Phase 0, S3). Empty both => the server default provider. ProviderID without
	// ModelID => the provider's default model; ModelID without ProviderID is a
	// client error (a bare model on the default provider is ambiguous).
	ProviderID string `json:"provider_id,omitempty"`
	ModelID    string `json:"model_id,omitempty"`
	// Profile selects the session's tool-surface profile (issue #55), mirroring
	// the proto field: "" = default (full filesystem, REQUIRES workspace),
	// "no-fs" = the no-filesystem profile (REQUIRES an EMPTY workspace). Any
	// other value is a 400.
	Profile string `json:"profile,omitempty"`
}

type limitsIn struct {
	MaxTurns               int `json:"max_turns,omitempty"`
	MaxToolCalls           int `json:"max_tool_calls,omitempty"`
	MaxConsecutiveFailures int `json:"max_consecutive_failures,omitempty"`
}

type createSessionResp struct {
	SessionID    string                  `json:"session_id"`
	Capabilities *serverCapabilitiesJSON `json:"capabilities,omitempty"`
	// SessionCapabilities echoes the per-session resolved input capability (catalog
	// ∩ adapter for THIS session's provider+model), so an HTTP client gates
	// per-session @-attach UX on the same intersected value the gRPC client gets.
	SessionCapabilities *sessionCapabilitiesJSON `json:"session_capabilities,omitempty"`
	// ResolvedModel echoes the EFFECTIVE provider+model THIS session resolved to
	// (the composition single source via Service.ResolvedModel), so an HTTP client
	// shows the same effective model the gRPC client gets. Omitted (nil) when no
	// model resolved (older-server-equivalent fallback).
	ResolvedModel *resolvedModelJSON `json:"resolved_model,omitempty"`
}

// sessionCapabilitiesJSON mirrors mecatlv1.SessionCapabilities for the JSON
// surface. Bools-only by design (the per-session surface is ONLY the model-varying
// image/audio input axis); it structurally cannot leak a secret.
type sessionCapabilitiesJSON struct {
	Image bool `json:"image"`
	Audio bool `json:"audio"`
}

// resolvedModelJSON mirrors mecatlv1.ResolvedModel for the JSON surface. It carries
// no secret material (provider id, model id, context window only).
type resolvedModelJSON struct {
	ProviderID    string `json:"provider_id"`
	ModelID       string `json:"model_id"`
	ContextWindow int64  `json:"context_window"`
}

// resolvedModelToJSON maps the server-side ResolvedModel to its JSON form, nil for
// the zero value (round-trips to "absent" so the client falls back to today's
// behavior). Mirrors resolvedModelToProto.
func resolvedModelToJSON(rm ResolvedModel) *resolvedModelJSON {
	if rm.ProviderID == "" && rm.ModelID == "" && rm.ContextWindow == 0 {
		return nil
	}
	return &resolvedModelJSON{ProviderID: rm.ProviderID, ModelID: rm.ModelID, ContextWindow: rm.ContextWindow}
}

// serverCapabilitiesJSON mirrors mecatlv1.ServerCapabilities for the JSON
// surface, so an HTTP client receives the same honest feature flags the gRPC
// client gets. Populated from the shared Service.capabilities() so the two
// surfaces cannot drift.
type serverCapabilitiesJSON struct {
	MCP            bool   `json:"mcp"`
	SlashCommands  bool   `json:"slash_commands"`
	Memory         bool   `json:"memory"`
	Skills         bool   `json:"skills"`
	Teams          bool   `json:"teams"`
	Bash           bool   `json:"bash"`
	Image          bool   `json:"image"`
	Audio          bool   `json:"audio"`
	ModelSelection bool   `json:"model_selection"`
	Posture        string `json:"posture,omitempty"`
}

// capabilitiesJSON projects the shared proto capabilities onto the JSON shape.
func capabilitiesJSON(c *mecatlv1.ServerCapabilities) *serverCapabilitiesJSON {
	if c == nil {
		return nil
	}
	return &serverCapabilitiesJSON{
		MCP:            c.GetMcp(),
		SlashCommands:  c.GetSlashCommands(),
		Memory:         c.GetMemory(),
		Skills:         c.GetSkills(),
		Teams:          c.GetTeams(),
		Bash:           c.GetBash(),
		Image:          c.GetImage(),
		Audio:          c.GetAudio(),
		ModelSelection: c.GetModelSelection(),
		Posture:        c.GetPosture(),
	}
}

type sessionResp struct {
	SessionID string `json:"session_id"`
	State     string `json:"state"`
	Mode      string `json:"mode"`
	Workspace string `json:"workspace"`
	Turns     int    `json:"turns"`
	ToolCalls int    `json:"tool_calls"`
	// ResolvedModel mirrors the gRPC Session snapshot's resolved_model so the HTTP
	// read surface is consistent with gRPC GetSession: the EFFECTIVE provider+model
	// this session resolved to (from Service.ResolvedModel, the composition single
	// source). Omitted (nil) when no model resolved (older-server-equivalent).
	ResolvedModel *resolvedModelJSON `json:"resolved_model,omitempty"`
}

type modeBody struct {
	Mode string `json:"mode"`
}

type promptBody struct {
	Text string `json:"text"`
	// Parts carries non-text media (image/audio) alongside the text. Each part
	// names its kind ("image"/"audio"), mime type, and EITHER base64 data OR a url.
	Parts []promptContentBody `json:"parts,omitempty"`
}

// promptContentBody is the JSON form of one multimodal prompt part. data is
// standard base64 (Go's encoding/json decodes a JSON string into []byte as
// base64 automatically); exactly one of data/url is set.
type promptContentBody struct {
	Kind     string `json:"kind"`
	MimeType string `json:"mime_type,omitempty"`
	Data     []byte `json:"data,omitempty"`
	URL      string `json:"url,omitempty"`
}

// toContentParts maps the HTTP prompt parts into the domain []session.Content.
// It is the HTTP wire→domain choke point: each part is built through
// session.NewContent (structural invariants + https/non-internal URL SSRF
// backstop) and the slice is size-capped via session.ValidateMediaParts — the
// SAME validation the gRPC mapper applies. An empty input yields nil.
func toContentParts(parts []promptContentBody) ([]session.Content, error) {
	if len(parts) == 0 {
		return nil, nil
	}
	out := make([]session.Content, 0, len(parts))
	for i, p := range parts {
		var kind session.MediaKind
		switch p.Kind {
		case string(session.MediaImage):
			kind = session.MediaImage
		case string(session.MediaAudio):
			kind = session.MediaAudio
		case "":
			return nil, fmt.Errorf("parts[%d]: kind is required", i)
		default:
			return nil, fmt.Errorf("parts[%d]: unknown kind %q", i, p.Kind)
		}
		c, err := session.NewContent(kind, p.MimeType, p.Data, p.URL)
		if err != nil {
			return nil, fmt.Errorf("parts[%d]: %w", i, err)
		}
		out = append(out, c)
	}
	if err := session.ValidateMediaParts(out); err != nil {
		return nil, err
	}
	return out, nil
}

type approveBody struct {
	AskID string `json:"ask_id"`
	// Allow is the LEGACY boolean (back-compat): true -> allow once, false -> deny.
	// It is used only when Verdict is empty/unspecified.
	Allow bool `json:"allow"`
	// Verdict is the preferred three-way resolution: "deny", "allow_once", or
	// "allow_always" (allow_always additionally learns a per-session rule). An
	// empty/unknown value falls back to Allow.
	Verdict string `json:"verdict,omitempty"`
}

// --- handlers ---------------------------------------------------------------

// createSession handles POST /v1/sessions.
func (h *HTTPHandler) createSession(w http.ResponseWriter, r *http.Request) {
	var body createSessionBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	// Session profile (issue #55): the workspace requirement is PROFILE-AWARE and
	// enforced in the service (default requires one; no-fs requires an EMPTY one),
	// so there is deliberately NO unconditional empty-workspace guard here.
	profile, err := ParseSessionProfile(body.Profile)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	var limits session.Limits
	if body.Limits != nil {
		limits = session.Limits{
			MaxTurns:               body.Limits.MaxTurns,
			MaxToolCalls:           body.Limits.MaxToolCalls,
			MaxConsecutiveFailures: body.Limits.MaxConsecutiveFailures,
		}
	}
	sel := ProviderSelector{ProviderID: body.ProviderID, ModelID: body.ModelID}
	sess, err := h.svc.CreateSessionWithProfile(r.Context(), body.Workspace, modeFromString(body.Mode), limits, sel, profile)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	scaps := h.svc.SessionCapabilities(sess.ID)
	writeJSON(w, http.StatusCreated, createSessionResp{
		SessionID:           string(sess.ID),
		Capabilities:        capabilitiesJSON(h.svc.capabilities()),
		SessionCapabilities: &sessionCapabilitiesJSON{Image: scaps.Image, Audio: scaps.Audio},
		ResolvedModel:       resolvedModelToJSON(h.svc.ResolvedModel(sess.ID)),
	})
}

// getSession handles GET /v1/sessions/{id}.
func (h *HTTPHandler) getSession(w http.ResponseWriter, r *http.Request) {
	id := session.SessionID(r.PathValue("id"))
	sess, err := h.svc.GetSession(r.Context(), id)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	h.writeSession(w, http.StatusOK, sess)
}

// setMode handles POST /v1/sessions/{id}/mode.
func (h *HTTPHandler) setMode(w http.ResponseWriter, r *http.Request) {
	id := session.SessionID(r.PathValue("id"))
	var body modeBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	sess, err := h.svc.SetMode(r.Context(), id, modeFromString(body.Mode))
	if err != nil {
		writeServiceError(w, err)
		return
	}
	h.writeSession(w, http.StatusOK, sess)
}

func (h *HTTPHandler) writeSession(w http.ResponseWriter, status int, sess *session.Session) {
	writeJSON(w, status, sessionResp{
		SessionID:     string(sess.ID),
		State:         string(sess.State),
		Mode:          string(sess.Mode),
		Workspace:     sess.Workspace,
		Turns:         sess.Counters.Turns,
		ToolCalls:     sess.Counters.ToolCalls,
		ResolvedModel: resolvedModelToJSON(h.svc.ResolvedModel(sess.ID)),
	})
}

// maxPromptBodyBytes bounds the POST /prompt request body so an oversized
// payload is rejected before it is buffered into memory (CWE-770), rather than
// relying on the post-decode size cap alone. It is sized above the decoded
// inline-media cap (session.MaxPromptMediaBytes) to allow a legitimate maximal
// prompt: media arrives base64-encoded (~4/3 expansion) plus the JSON envelope
// and prompt text, so the wire body can exceed the decoded byte budget. 32 MiB
// comfortably covers the 20 MiB decoded cap (~27 MiB base64) with headroom while
// still bounding the read.
const maxPromptBodyBytes = 32 << 20 // 32 MiB

// prompt handles POST /v1/sessions/{id}/prompt, streaming the run's events as
// Server-Sent Events. It starts a run on the shared engine and relays each
// session.Event (mapped to the proto Event, JSON-encoded) as one SSE frame.
func (h *HTTPHandler) prompt(w http.ResponseWriter, r *http.Request) {
	id := session.SessionID(r.PathValue("id"))
	// Bound the body read so an oversized payload cannot exhaust memory; a body
	// that exceeds the limit surfaces as a decode error → 400 (not an OOM).
	r.Body = http.MaxBytesReader(w, r.Body, maxPromptBodyBytes)
	var body promptBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			writeError(w, http.StatusRequestEntityTooLarge, "request body too large")
			return
		}
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if body.Text == "" && len(body.Parts) == 0 {
		writeError(w, http.StatusBadRequest, "text or parts is required")
		return
	}
	parts, perr := toContentParts(body.Parts)
	if perr != nil {
		writeError(w, http.StatusBadRequest, perr.Error())
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}

	run, err := h.svc.StartRunContent(r.Context(), id, body.Text, parts)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	h.relayRunSSE(w, r, id, run, flusher)
}

// relayRunSSE streams run's Events to w as Server-Sent Events until the channel
// closes (the run ended). It is the SHARED relay used by both the prompt run-entry
// and the awaiting-approval re-entry (the approve handler's rehydrate path), so the
// SSE framing, the dead-client drain-to-discard, the disconnect-cancels-run hook,
// the EvPermissionAsk persist, and the deregister-on-end discipline cannot drift
// between the two entry points. The caller must already have validated the Flusher
// and written nothing to w yet (this sets the headers + 200 itself).
func (h *HTTPHandler) relayRunSSE(w http.ResponseWriter, r *http.Request, id session.SessionID, run *agent.Run, flusher http.Flusher) {
	defer h.svc.deregister(id, run)

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	// If the client disconnects, cancel the run.
	go func() {
		<-r.Context().Done()
		run.Cancel()
	}()

	// Relay events; the channel closes when the run ends. On the FIRST write
	// error (the client is gone) cancel the run but KEEP RANGING, discarding
	// events until the channel closes: a run that keeps emitting must never
	// wedge in its own sends behind a dead relay. The failure flag is sticky —
	// no further write happens after the first error.
	//
	// The durable event-log Append (cloud-native Phase 3a) is DECOUPLED from the
	// client write: it runs for EVERY observed event, BEFORE and independent of the
	// drain-to-discard guard, so a disconnected client never stops the log (the
	// whole point of a server-side durable log is to survive the client — it must
	// record the post-disconnect tail, including the terminal EvResult). It uses a
	// cancel-detached context so a cancelled request ctx (client gone) cannot abort
	// the durable write. This is DISTINCT from the EvPermissionAsk Persist below,
	// which is snapshot semantics gated to the healthy path: the log is append-only
	// history and must record what happened regardless of client liveness.
	logCtx := context.WithoutCancel(r.Context())
	enc := json.NewEncoder(w)
	failed := false
	fail := func() {
		failed = true
		run.Cancel()
	}
	for ev := range run.Events() {
		h.svc.appendEvent(logCtx, id, ev)
		if failed {
			continue // drain-to-discard: keep the run unwedged after a dead client
		}
		// EvApproval (3a), EvCompactionArchive (3b), and EvUserPrompt (ADR 0038) are
		// consumed by the durable log ONLY — appended above but NOT relayed to the client
		// wire (the verdict record, the pre-compaction archive, and the user-prompt record
		// are log/audit history, not client events; the client already holds its own
		// prompt). Skip the client write AFTER the Append.
		if ev.Type == session.EvApproval || ev.Type == session.EvCompactionArchive || ev.Type == session.EvUserPrompt {
			continue
		}
		// Persist when the run pauses awaiting approval so a restart leaves a
		// loadable awaiting session a client can re-attach to.
		if ev.Type == session.EvPermissionAsk {
			h.svc.Persist(r.Context(), id)
		}
		if _, err := w.Write([]byte("data: ")); err != nil {
			fail()
			continue
		}
		if err := enc.Encode(toProto(ev)); err != nil { // Encode appends a newline
			fail()
			continue
		}
		if _, err := w.Write([]byte("\n")); err != nil {
			fail()
			continue
		}
		flusher.Flush()
	}
}

// approve handles POST /v1/sessions/{id}/approve, resolving the paused ask on
// the session's in-flight run.
func (h *HTTPHandler) approve(w http.ResponseWriter, r *http.Request) {
	id := session.SessionID(r.PathValue("id"))
	var body approveBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if body.AskID == "" {
		writeError(w, http.StatusBadRequest, "ask_id is required")
		return
	}
	run, err := h.svc.ApproveRun(r.Context(), id, body.AskID, verdictFromHTTP(body.Verdict, body.Allow))
	if err != nil {
		writeServiceError(w, err)
		return
	}
	if run == nil {
		// SAME-PROCESS path: a live run resolved the ask over its channel and the
		// existing stream (the prompt SSE / the bidi relay) delivers the effects.
		// Ack only.
		w.WriteHeader(http.StatusNoContent)
		return
	}
	// REHYDRATE path (cloud-native Phase 2): the process that parked the ask died
	// and we re-entered the loop AT the ask in this process. The resumed run has no
	// existing stream to ride, so relay its events as SSE on this approve response —
	// the standard reconnect-and-relay shape, no streaming-approve proto change. A
	// client that does not consume the body still gets a correct run: the drain-to-
	// discard keeps it unwedged and the terminal state persists.
	flusher, ok := w.(http.Flusher)
	if !ok {
		// No streaming: the resumed run is registered and will drive to completion;
		// fall back to an ack so the verdict is not lost. Drain in the background so
		// the run never wedges behind an unread buffer — but STILL append every
		// event to the durable log (cloud-native Phase 3a): the log records what
		// happened regardless of whether a client consumes the stream. Use a
		// cancel-detached context so the request returning does not abort the writes.
		logCtx := context.WithoutCancel(r.Context())
		go func() {
			for ev := range run.Events() {
				h.svc.appendEvent(logCtx, id, ev)
			}
			h.svc.deregister(id, run)
		}()
		w.WriteHeader(http.StatusNoContent)
		return
	}
	h.relayRunSSE(w, r, id, run, flusher)
}

// verdictFromHTTP maps the HTTP approve body's string verdict to the domain
// ApprovalVerdict, preferring an explicit verdict and falling back to the legacy
// allow bool. It is fail-safe: an empty verdict with allow=false, and any
// unrecognized string, resolve to VerdictDeny.
func verdictFromHTTP(verdict string, allow bool) session.ApprovalVerdict {
	switch verdict {
	case "allow_always":
		return session.VerdictAllowAlways
	case "allow_once":
		return session.VerdictAllowOnce
	case "deny":
		return session.VerdictDeny
	case "":
		if allow {
			return session.VerdictAllowOnce
		}
		return session.VerdictDeny
	default:
		return session.VerdictDeny
	}
}

// closeSession handles DELETE /v1/sessions/{id}, ending the session and releasing
// its server-side resources. Unknown (never-created) id -> 404; an already-released
// session succeeds (204, idempotent). It does NOT delete the persisted snapshot or
// cancel an in-flight run.
func (h *HTTPHandler) closeSession(w http.ResponseWriter, r *http.Request) {
	id := session.SessionID(r.PathValue("id"))
	if err := h.svc.EndSession(r.Context(), id); err != nil {
		writeServiceError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// cancel handles POST /v1/sessions/{id}/cancel, cancelling the in-flight run.
func (h *HTTPHandler) cancel(w http.ResponseWriter, r *http.Request) {
	id := session.SessionID(r.PathValue("id"))
	if err := h.svc.Cancel(r.Context(), id); err != nil {
		writeServiceError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// cancelChildBody is the JSON body of POST /v1/sessions/{id}/cancel-child.
type cancelChildBody struct {
	// ChildID is the child session id, verbatim (the Subagent result's `agentId:`
	// line / the subagent.start child_id).
	ChildID string `json:"child_id"`
}

// cancelChild handles POST /v1/sessions/{id}/cancel-child, cancelling ONE child
// (a subagent) of the session's in-flight run while the run itself keeps
// streaming. Mirrors /approve: 204 on success, 404 for an unknown session OR an
// unknown/already-finished child, 409 for a known-but-runless session.
func (h *HTTPHandler) cancelChild(w http.ResponseWriter, r *http.Request) {
	id := session.SessionID(r.PathValue("id"))
	var body cancelChildBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if body.ChildID == "" {
		writeError(w, http.StatusBadRequest, "child_id is required")
		return
	}
	if err := h.svc.CancelChild(r.Context(), id, body.ChildID); err != nil {
		writeServiceError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- team request bodies -----------------------------------------------------

// teammateSpecBody is one member's enrolment fields, shared by createTeam's
// roster and spawnTeammate. It mirrors the proto TeammateSpec (minus team_id)
// and maps to agent.MemberSpec the same way fromProtoTeammateSpecs does.
type teammateSpecBody struct {
	Name          string `json:"name"`
	AgentType     string `json:"agent_type,omitempty"`
	Lead          bool   `json:"lead,omitempty"`
	Mutating      bool   `json:"mutating,omitempty"`
	InitialPrompt string `json:"initial_prompt,omitempty"`
}

// toMemberSpec maps a JSON spec body to the agent.MemberSpec the Service takes.
func (b teammateSpecBody) toMemberSpec() agent.MemberSpec {
	return agent.MemberSpec{
		Name:          b.Name,
		AgentType:     b.AgentType,
		Lead:          b.Lead,
		Mutating:      b.Mutating,
		InitialPrompt: b.InitialPrompt,
	}
}

type createTeamBody struct {
	Workspace string             `json:"workspace"`
	Name      string             `json:"name,omitempty"`
	Goal      string             `json:"goal,omitempty"`
	Members   []teammateSpecBody `json:"members,omitempty"`
	// MaxTeamTokens mirrors CreateTeamRequest.max_team_tokens: a per-request
	// team-wide token budget, tighten-only against the server's --max-team-tokens.
	MaxTeamTokens int32 `json:"max_team_tokens,omitempty"`
}

type sendTeammateMessageBody struct {
	From string `json:"from,omitempty"`
	To   string `json:"to"`
	Body string `json:"body"`
}

// cancelTeammateBody mirrors CancelTeammateRequest (minus the path-borne team id).
// The member rides the JSON body — matching sendTeammateMessage's idiom — rather
// than a path segment, so member names need no URL-escaping discipline.
type cancelTeammateBody struct {
	Member string `json:"member"`
}

// --- team handlers -----------------------------------------------------------

// createTeam handles POST /v1/teams, enrolling the optional initial roster
// atomically. The proto CreateTeamResponse is JSON-encoded so the HTTP and gRPC
// surfaces share one shape.
func (h *HTTPHandler) createTeam(w http.ResponseWriter, r *http.Request) {
	var body createTeamBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if body.Workspace == "" {
		writeError(w, http.StatusBadRequest, "workspace is required")
		return
	}
	var specs []agent.MemberSpec
	if len(body.Members) > 0 {
		specs = make([]agent.MemberSpec, 0, len(body.Members))
		for _, m := range body.Members {
			specs = append(specs, m.toMemberSpec())
		}
	}
	id, enrolled, err := h.svc.CreateTeam(r.Context(), body.Workspace, body.Name, body.Goal, int(body.MaxTeamTokens), specs)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, &mecatlv1.CreateTeamResponse{
		TeamId:  id,
		Members: toProtoTeamMembers(enrolled),
	})
}

// spawnTeammate handles POST /v1/teams/{id}/members.
func (h *HTTPHandler) spawnTeammate(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var body teammateSpecBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if body.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	m, err := h.svc.SpawnTeammate(r.Context(), id, body.toMemberSpec())
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, &mecatlv1.SpawnTeammateResponse{Member: toProtoTeamMember(m)})
}

// sendTeammateMessage handles POST /v1/teams/{id}/messages.
func (h *HTTPHandler) sendTeammateMessage(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var body sendTeammateMessageBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if body.To == "" || body.Body == "" {
		writeError(w, http.StatusBadRequest, "to and body are required")
		return
	}
	if err := h.svc.SendTeammateMessage(r.Context(), id, body.From, body.To, body.Body); err != nil {
		writeServiceError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// cancelTeammate handles POST /v1/teams/{id}/members/cancel, cancelling one
// member of a running team (issue #29; the HTTP mirror of the CancelTeammate
// unary). 204 on success — including the cancel of an already-stopped-but-present
// member, an honest no-op.
func (h *HTTPHandler) cancelTeammate(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var body cancelTeammateBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if body.Member == "" {
		writeError(w, http.StatusBadRequest, "member is required")
		return
	}
	if err := h.svc.CancelTeammate(r.Context(), id, body.Member); err != nil {
		writeServiceError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// runTeam handles POST /v1/teams/{id}/run, driving the team to quiescence and
// streaming every member event (mapped to the proto TeamEvent, JSON-encoded) as
// one SSE frame — mirroring the prompt handler — then closing with the single
// terminal frame carrying TeamEvent.outcome (issue #36; SSE parity with the gRPC
// RunTeam handler). RunTeam serialises sink calls through a single forwarder, so
// writing from the sink is safe. A write failure flags and cancels the run so the
// team stops promptly, and suppresses the outcome frame (no further writes to a
// dead client).
func (h *HTTPHandler) runTeam(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}

	// Deriving from r.Context() means a client disconnect already cancels the
	// run; the explicit cancel lets a write failure stop the team promptly too.
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	headersWritten := false
	enc := json.NewEncoder(w)
	var writeErr bool
	// writeFrame writes one SSE data frame (headers lazily first), flagging
	// writeErr and cancelling the run on any failure. Both the per-event sink and
	// the terminal outcome frame go through it — one encode+flush shape.
	writeFrame := func(te *mecatlv1.TeamEvent) {
		if writeErr {
			return
		}
		// Write the stream headers lazily on the first frame so that an early
		// lookup failure (returned below) can still surface as a JSON error.
		if !headersWritten {
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("Cache-Control", "no-cache")
			w.Header().Set("Connection", "keep-alive")
			w.WriteHeader(http.StatusOK)
			flusher.Flush()
			headersWritten = true
		}
		if _, e := w.Write([]byte("data: ")); e != nil {
			writeErr = true
			cancel()
			return
		}
		if e := enc.Encode(te); e != nil { // Encode appends a newline
			writeErr = true
			cancel()
			return
		}
		if _, e := w.Write([]byte("\n")); e != nil {
			writeErr = true
			cancel()
			return
		}
		flusher.Flush()
	}
	out, err := h.svc.RunTeam(ctx, id, func(te agent.TeamEvent) {
		writeFrame(&mecatlv1.TeamEvent{Member: te.Member, Event: toProto(te.Event)})
	})
	if err != nil {
		// A lookup/precondition failure before any event: nothing has been
		// written yet, so a JSON error is still well-formed.
		if !headersWritten {
			writeServiceError(w, err)
		}
		return
	}
	// Terminal outcome frame. writeFrame writes the headers first if no member
	// event streamed (an empty team still delivers its outcome) and is a no-op
	// after a write failure.
	writeFrame(&mecatlv1.TeamEvent{Outcome: toProtoTeamOutcome(out)})
}

// listTeam handles GET /v1/teams/{id}. The proto ListTeamResponse is
// JSON-encoded so the HTTP and gRPC surfaces share one shape.
func (h *HTTPHandler) listTeam(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	members, tasks, quiescent, err := h.svc.ListTeam(r.Context(), id)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, &mecatlv1.ListTeamResponse{
		Members:   toProtoTeamMembers(members),
		Tasks:     toProtoTeamTasks(tasks),
		Quiescent: quiescent,
	})
}

// cleanupTeam handles DELETE /v1/teams/{id}.
func (h *HTTPHandler) cleanupTeam(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := h.svc.CleanupTeam(r.Context(), id); err != nil {
		writeServiceError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- MCP inspection handlers -------------------------------------------------

// getMcpPromptBody is the POST body for /v1/mcp/prompts/get.
type getMcpPromptBody struct {
	Server    string            `json:"server"`
	Name      string            `json:"name"`
	Arguments map[string]string `json:"arguments,omitempty"`
}

// listMcpResources handles GET /v1/mcp/resources?server=. The proto response is
// JSON-encoded so the HTTP and gRPC surfaces share one shape.
func (h *HTTPHandler) listMcpResources(w http.ResponseWriter, r *http.Request) {
	res, err := h.svc.ListMcpResources(r.Context(), r.URL.Query().Get("server"))
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, &mecatlv1.ListMcpResourcesResponse{Resources: toProtoMcpResources(res)})
}

// readMcpResource handles GET /v1/mcp/resources/read?server=&uri=.
func (h *HTTPHandler) readMcpResource(w http.ResponseWriter, r *http.Request) {
	server := r.URL.Query().Get("server")
	uri := r.URL.Query().Get("uri")
	if server == "" || uri == "" {
		writeError(w, http.StatusBadRequest, "server and uri are required")
		return
	}
	c, err := h.svc.ReadMcpResource(r.Context(), server, uri)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, &mecatlv1.ReadMcpResourceResponse{
		Contents: []*mecatlv1.McpResourceContents{toProtoMcpResourceContents(c)},
	})
}

// listMcpPrompts handles GET /v1/mcp/prompts?server=.
func (h *HTTPHandler) listMcpPrompts(w http.ResponseWriter, r *http.Request) {
	ps, err := h.svc.ListMcpPrompts(r.Context(), r.URL.Query().Get("server"))
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, &mecatlv1.ListMcpPromptsResponse{Prompts: toProtoMcpPrompts(ps)})
}

// getMcpPrompt handles POST /v1/mcp/prompts/get.
func (h *HTTPHandler) getMcpPrompt(w http.ResponseWriter, r *http.Request) {
	var body getMcpPromptBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if body.Server == "" || body.Name == "" {
		writeError(w, http.StatusBadRequest, "server and name are required")
		return
	}
	res, err := h.svc.GetMcpPrompt(r.Context(), body.Server, body.Name, body.Arguments)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	msgs := make([]*mecatlv1.McpPromptMessage, 0, len(res.Messages))
	for _, m := range res.Messages {
		msgs = append(msgs, toProtoMcpPromptMessage(m))
	}
	writeJSON(w, http.StatusOK, &mecatlv1.GetMcpPromptResponse{Description: res.Description, Messages: msgs})
}

// listMcpSources handles GET /v1/mcp/sources.
func (h *HTTPHandler) listMcpSources(w http.ResponseWriter, r *http.Request) {
	infos := h.svc.ListMcpSources(r.Context())
	out := make([]*mecatlv1.McpSource, 0, len(infos))
	for _, s := range infos {
		out = append(out, toProtoMcpSource(s))
	}
	writeJSON(w, http.StatusOK, &mecatlv1.ListMcpSourcesResponse{Sources: out})
}

// listToolHiveGroups handles GET /v1/mcp/toolhive/groups.
func (h *HTTPHandler) listToolHiveGroups(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, &mecatlv1.ListToolHiveGroupsResponse{Groups: h.svc.ListToolHiveGroups(r.Context())})
}

// listAgents handles GET /v1/agents.
func (h *HTTPHandler) listAgents(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, &mecatlv1.ListAgentsResponse{Agents: h.svc.ListAgents(r.Context())})
}

// listSkills handles GET /v1/skills.
func (h *HTTPHandler) listSkills(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, &mecatlv1.ListSkillsResponse{Skills: h.svc.ListSkills(r.Context())})
}

// listModels handles GET /v1/models.
func (h *HTTPHandler) listModels(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, &mecatlv1.ListModelsResponse{Models: h.svc.ListModels(r.Context())})
}

// getSoul handles GET /v1/soul.
func (h *HTTPHandler) getSoul(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, &mecatlv1.GetSoulResponse{Soul: h.svc.GetSoul(r.Context())})
}

// getUserModel handles GET /v1/usermodel.
func (h *HTTPHandler) getUserModel(w http.ResponseWriter, r *http.Request) {
	resp, err := h.svc.GetUserModel(r.Context())
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// listCommands handles GET /v1/commands?workspace=.
func (h *HTTPHandler) listCommands(w http.ResponseWriter, r *http.Request) {
	cmds, err := h.svc.ListCommands(r.Context(), r.URL.Query().Get("workspace"))
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, &mecatlv1.ListCommandsResponse{Commands: toProtoCommands(cmds)})
}

// listWorktrees handles GET /v1/worktrees?workspace= (issue #102).
func (h *HTTPHandler) listWorktrees(w http.ResponseWriter, r *http.Request) {
	wts, err := h.svc.ListWorktrees(r.Context(), r.URL.Query().Get("workspace"))
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, &mecatlv1.ListWorktreesResponse{Worktrees: toProtoWorktrees(wts)})
}

// --- helpers ----------------------------------------------------------------

// modeFromString maps a JSON mode string to a session.PermissionMode. Unknown
// or empty values fall through to the empty mode (Service applies its default).
func modeFromString(s string) session.PermissionMode {
	switch strings.ToLower(s) {
	case "plan":
		return session.ModePlan
	case "acceptedits", "accept-edits", "accept_edits", "accept":
		return session.ModeAccept
	case "default":
		return session.ModeDefault
	default:
		return ""
	}
}

// writeJSON writes v as a JSON response with the given status code.
func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// writeError writes a JSON {"error": msg} body with the given status code.
func writeError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

// writeServiceError maps a service sentinel error to an HTTP status.
func writeServiceError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrInvalidArgument):
		writeError(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, ErrNotFound):
		writeError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, ErrTeamNotFound):
		writeError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, ErrChildNotFound):
		writeError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, ErrFailedPrecondition):
		writeError(w, http.StatusPreconditionFailed, err.Error())
	case errors.Is(err, ErrTeamsDisabled):
		// Teams are not enabled (no MemberEngine wired): a precondition for any
		// team RPC is unmet. The gRPC side maps it to FailedPrecondition.
		writeError(w, http.StatusPreconditionFailed, err.Error())
	case errors.Is(err, ErrTeamRunning):
		// The team is already running: a second run, a late spawn, or a cleanup
		// of a live team. FailedPrecondition, like the gRPC side.
		writeError(w, http.StatusPreconditionFailed, err.Error())
	case errors.Is(err, ErrTeamNotRunning):
		// The team is NOT running (created-but-never-run, or already done): a
		// CancelTeammate has no in-flight run to reach into. FailedPrecondition,
		// like the gRPC side.
		writeError(w, http.StatusPreconditionFailed, err.Error())
	case errors.Is(err, ErrTooManyTeams):
		// The live-team registry is at MaxTeams (gRPC: ResourceExhausted).
		writeError(w, http.StatusTooManyRequests, err.Error())
	case errors.Is(err, ErrTooManySessionEngines):
		// The per-session engine registry is at MaxSessionEngines (gRPC:
		// ResourceExhausted): the client must release a session before opening another.
		writeError(w, http.StatusTooManyRequests, err.Error())
	case errors.Is(err, ErrNoActiveRun):
		// Known session, but its run is not live in this process (e.g. the
		// stream was lost across a restart): nothing to deliver the control to.
		writeError(w, http.StatusConflict, err.Error())
	case errors.Is(err, ErrSessionLeasedElsewhere):
		// Cloud-native Phase 4: another replica holds the session's single-writer
		// lease. 409 Conflict — the session exists and is well-formed, it is just
		// owned by another process right now (a later retry can succeed).
		writeError(w, http.StatusConflict, err.Error())
	case errors.Is(err, ErrNoMCPProvider):
		// No MCP provider is wired: the precondition for read/get is unmet.
		writeError(w, http.StatusPreconditionFailed, err.Error())
	case errors.Is(err, ErrInternal):
		// A downstream/transport fault on a connected MCP server — not the
		// client's fault, so 500 rather than 400.
		writeError(w, http.StatusInternalServerError, err.Error())
	default:
		writeError(w, http.StatusInternalServerError, err.Error())
	}
}
