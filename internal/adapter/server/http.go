package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"google.golang.org/protobuf/encoding/protojson"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/learning"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

// HTTPHandler is the HTTP/SSE adapter over the shared Service. It serves the
// thin REST surface from ARCHITECTURE §7.2:
//
//	POST   /v1/sessions               -> CreateSession (JSON)
//	GET    /v1/sessions/{id}          -> GetSession (JSON snapshot)
//	DELETE /v1/sessions/{id}          -> CloseSession (release session resources; 204)
//	POST   /v1/sessions/{id}/rename   -> RenameSession (persist an explicit title)
//	POST   /v1/sessions/{id}/delete   -> DeleteSession (physical snapshot + sidecars)
//	POST   /v1/sessions/{id}/compact  -> CompactSession (bodyless manual compaction)
//	POST   /v1/sessions/{id}/prompt   -> start a run; text/event-stream of Events
//	POST   /v1/sessions/{id}/approve  -> resolve the paused ask on the run
//	POST   /v1/sessions/{id}/cancel   -> cancel the in-flight run
//	POST   /v1/sessions/{id}/cancel-child -> cancel ONE child (subagent) of the run
//	POST   /v1/sessions/{id}/fork     -> ForkSession (peer session from a history snapshot; 201)
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
	h.mux.HandleFunc("GET /v1/info", h.getServerInfo)
	h.mux.HandleFunc("POST /v1/sessions", h.createSession)
	h.mux.HandleFunc("GET /v1/sessions/{id}", h.getSession)
	h.mux.HandleFunc("GET /v1/sessions/{id}/transcript", h.getSessionTranscript)
	h.mux.HandleFunc("POST /v1/sessions/{id}/mode", h.setMode)
	h.mux.HandleFunc("DELETE /v1/sessions/{id}", h.closeSession)
	h.mux.HandleFunc("POST /v1/sessions/{id}/rename", h.renameSession)
	h.mux.HandleFunc("POST /v1/sessions/{id}/delete", h.deleteSession)
	h.mux.HandleFunc("POST /v1/sessions/{id}/compact", h.compactSession)
	h.mux.HandleFunc("POST /v1/sessions/{id}/prompt", h.prompt)
	h.mux.HandleFunc("POST /v1/sessions/{id}/retry", h.retry)
	h.mux.HandleFunc("POST /v1/sessions/{id}/approve", h.approve)
	h.mux.HandleFunc("POST /v1/sessions/{id}/plan:approve", h.approvePlan)
	h.mux.HandleFunc("POST /v1/sessions/{id}/cancel", h.cancel)
	h.mux.HandleFunc("POST /v1/sessions/{id}/cancel-child", h.cancelChild)
	h.mux.HandleFunc("POST /v1/sessions/{id}/fork", h.forkSession)
	h.mux.HandleFunc("POST /v1/sessions/{id}/adoption:preflight", h.preflightSessionAdoption)
	h.mux.HandleFunc("POST /v1/sessions/{id}/adopt", h.adoptSession)
	h.mux.HandleFunc("POST /v1/sessions/{id}/reflect", h.reflectSession)
	h.mux.HandleFunc("POST /v1/dream/plans", h.generateDreamPlan)
	h.mux.HandleFunc("POST /v1/dream/plans/{plan_id}/decision", h.decideDreamPlan)
	h.mux.HandleFunc("GET /v1/learning/proposals", h.listLearningProposals)
	h.mux.HandleFunc("GET /v1/learning/proposals/{id}", h.getLearningProposal)
	h.mux.HandleFunc("POST /v1/learning/proposals/{id}/decision", h.decideLearningProposal)
	h.mux.HandleFunc("POST /v1/learning/proposals/{id}/undo", h.undoLearningPromotion)
	h.mux.HandleFunc("GET /v1/mcp/resources", h.listMcpResources)
	h.mux.HandleFunc("GET /v1/mcp/resources/read", h.readMcpResource)
	h.mux.HandleFunc("GET /v1/mcp/prompts", h.listMcpPrompts)
	h.mux.HandleFunc("POST /v1/mcp/prompts/get", h.getMcpPrompt)
	h.mux.HandleFunc("GET /v1/mcp/sources", h.listMcpSources)
	h.mux.HandleFunc("GET /v1/mcp/toolhive/groups", h.listToolHiveGroups)
	h.mux.HandleFunc("GET /v1/agents", h.listAgents)
	h.mux.HandleFunc("GET /v1/skills", h.listSkills)
	h.mux.HandleFunc("GET /v1/skills/learned", h.listLearnedSkills)
	h.mux.HandleFunc("GET /v1/skills/learned/changes", h.listSkillChanges)
	h.mux.HandleFunc("GET /v1/skills/learned/{id}", h.getLearnedSkill)
	h.mux.HandleFunc("GET /v1/skills/learned/{id}/diff", h.diffLearnedSkill)
	h.mux.HandleFunc("POST /v1/skills/learned/{id}/activate", h.activateLearnedSkill)
	h.mux.HandleFunc("POST /v1/skills/learned/{id}/reject", h.rejectLearnedSkill)
	h.mux.HandleFunc("POST /v1/skills/learned/{id}/archive", h.archiveLearnedSkill)
	h.mux.HandleFunc("POST /v1/skills/learned/{id}/rollback", h.rollbackLearnedSkill)
	h.mux.HandleFunc("GET /v1/models", h.listModels)
	h.mux.HandleFunc("GET /v1/soul", h.getSoul)
	h.mux.HandleFunc("GET /v1/usermodel", h.getUserModel)
	h.mux.HandleFunc("GET /v1/commands", h.listCommands)
	h.mux.HandleFunc("GET /v1/worktrees", h.listWorktrees)
	h.mux.HandleFunc("GET /v1/sessions", h.listSessions)
	h.mux.HandleFunc("GET /v1/storage/health", h.getStorageHealth)
	h.mux.HandleFunc("POST /v1/storage/migrations/plan", h.planSessionMigration)
	h.mux.HandleFunc("POST /v1/storage/migrations/apply", h.applySessionMigration)
	h.mux.HandleFunc("GET /v1/storage/migrations/{id}", h.getSessionMigrationJob)
	h.mux.HandleFunc("POST /v1/storage/migrations/{id}/resume", h.resumeSessionMigration)
	h.mux.HandleFunc("POST /v1/storage/migrations/{id}/cancel", h.cancelSessionMigration)
	h.mux.HandleFunc("POST /v1/storage/cleanup:plan", h.planSessionCleanup)
	h.mux.HandleFunc("POST /v1/storage/cleanup:apply", h.applySessionCleanup)
	h.mux.HandleFunc("POST /v1/storage/cleanup/jobs/{id}/cancel", h.cancelSessionCleanup)
	h.mux.HandleFunc("GET /v1/storage/cleanup/jobs/{id}", h.getSessionCleanupJob)
	h.mux.HandleFunc("GET /v1/sessions/{id}/events", h.streamSessionEvents)
	h.mux.HandleFunc("POST /v1/teams", h.createTeam)
	h.mux.HandleFunc("POST /v1/teams/{id}/members", h.spawnTeammate)
	h.mux.HandleFunc("POST /v1/teams/{id}/messages", h.sendTeammateMessage)
	h.mux.HandleFunc("POST /v1/teams/{id}/members/cancel", h.cancelTeammate)
	h.mux.HandleFunc("POST /v1/teams/{id}/run", h.runTeam)
	h.mux.HandleFunc("GET /v1/teams/{id}", h.listTeam)
	h.mux.HandleFunc("DELETE /v1/teams/{id}", h.cleanupTeam)
	// Schedule routes (issue #232, Phase 2a): a peer REST surface over the same
	// Service.CreateSchedule/... methods the gRPC ScheduleService delegates to.
	h.mux.HandleFunc("POST /v1/schedules", h.createSchedule)
	h.mux.HandleFunc("GET /v1/schedules", h.listSchedules)
	h.mux.HandleFunc("GET /v1/schedules/{name}", h.getSchedule)
	h.mux.HandleFunc("PUT /v1/schedules/{name}", h.updateSchedule)
	h.mux.HandleFunc("DELETE /v1/schedules/{name}", h.deleteSchedule)
	h.mux.HandleFunc("POST /v1/schedules/{name}/fire", h.fireNowSchedule)
	h.mux.HandleFunc("POST /v1/schedules/{name}/pause", h.pauseSchedule)
	h.mux.HandleFunc("POST /v1/schedules/{name}/resume", h.resumeSchedule)
	h.mux.HandleFunc("GET /v1/schedules/{name}/fires", h.listFires)
	h.mux.HandleFunc("GET /v1/schedules/{name}/fires/{id}", h.getFire)
	return h
}

// ServeHTTP routes to the registered handlers.
func (h *HTTPHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.mux.ServeHTTP(w, r)
}

// getServerInfo returns safe build, composition, and the caller-selected provider endpoint projection.
func (h *HTTPHandler) getServerInfo(w http.ResponseWriter, r *http.Request) {
	providerIDs := r.URL.Query()["provider_id"]
	providerID := ""
	if len(providerIDs) == 1 {
		providerID = providerIDs[0]
	}
	writeJSON(w, http.StatusOK, h.svc.serverInfoResponse(providerID))
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
	// ReasoningEffort sets the session's reasoning-effort tier (ADR 0055),
	// mirroring the proto field: "" / "auto" = unset (operator/provider default),
	// else low/medium/high/xhigh/max. The server normalises + per-provider-clamps +
	// capability-gates it; an unknown value falls back to the operator default with
	// a WARN. The effective value is echoed on resolved_model.reasoning_effort.
	ReasoningEffort string `json:"reasoning_effort,omitempty"`
	// SourceSessionID, when non-empty, seeds the new session's conversation history
	// from the named source session (issue #20: model-switch context carryover).
	// Always allowed across providers: same-provider replays verbatim,
	// cross-provider seeds a provider-neutral copy (blobs stripped). A
	// running/awaiting source is a 4xx (FailedPrecondition). Empty means no
	// carryover.
	SourceSessionID string `json:"source_session_id,omitempty"`
	// DebugTargetSessionID creates a separate no-fs diagnostic session bound to
	// one authorized target; it never copies target conversation state.
	DebugTargetSessionID string `json:"debug_target_session_id,omitempty"`
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
	ProviderID      string `json:"provider_id"`
	ModelID         string `json:"model_id"`
	ContextWindow   int64  `json:"context_window"`
	ReasoningEffort string `json:"reasoning_effort,omitempty"`
}

// resolvedModelToJSON maps the server-side ResolvedModel to its JSON form, nil for
// the zero value (round-trips to "absent" so the client falls back to today's
// behavior). Mirrors resolvedModelToProto.
func resolvedModelToJSON(rm ResolvedModel) *resolvedModelJSON {
	if rm.ProviderID == "" && rm.ModelID == "" && rm.ContextWindow == 0 && rm.ReasoningEffort == "" {
		return nil
	}
	return &resolvedModelJSON{ProviderID: rm.ProviderID, ModelID: rm.ModelID, ContextWindow: rm.ContextWindow, ReasoningEffort: rm.ReasoningEffort}
}

// serverCapabilitiesJSON mirrors mecatlv1.ServerCapabilities for the JSON
// surface, so an HTTP client receives the same honest feature flags the gRPC
// client gets. Populated from the shared Service.capabilities() so the two
// surfaces cannot drift.
type serverCapabilitiesJSON struct {
	MCP               bool                              `json:"mcp"`
	SlashCommands     bool                              `json:"slash_commands"`
	Memory            bool                              `json:"memory"`
	Skills            bool                              `json:"skills"`
	Teams             bool                              `json:"teams"`
	Bash              bool                              `json:"bash"`
	Image             bool                              `json:"image"`
	Audio             bool                              `json:"audio"`
	ModelSelection    bool                              `json:"model_selection"`
	Reflection        bool                              `json:"reflection"`
	LearningProposals bool                              `json:"learning_proposals"`
	LearnedSkills     bool                              `json:"learned_skills"`
	StorageHealth     bool                              `json:"storage_health"`
	StorageMigration  bool                              `json:"storage_migration"`
	StorageCleanup    bool                              `json:"storage_cleanup"`
	LegacyAdoption    bool                              `json:"legacy_adoption"`
	SessionDebug      bool                              `json:"session_debug"`
	ManualDream       *mecatlv1.ManualDreamCapabilities `json:"manual_dream,omitempty"`
	ManualCompaction  bool                              `json:"manual_compaction"`
	Posture           string                            `json:"posture,omitempty"`
}

// capabilitiesJSON projects the shared proto capabilities onto the JSON shape.
func capabilitiesJSON(c *mecatlv1.ServerCapabilities) *serverCapabilitiesJSON {
	if c == nil {
		return nil
	}
	return &serverCapabilitiesJSON{
		MCP:               c.GetMcp(),
		SlashCommands:     c.GetSlashCommands(),
		Memory:            c.GetMemory(),
		Skills:            c.GetSkills(),
		Teams:             c.GetTeams(),
		Bash:              c.GetBash(),
		Image:             c.GetImage(),
		Audio:             c.GetAudio(),
		ModelSelection:    c.GetModelSelection(),
		Reflection:        c.GetReflection(),
		LearningProposals: c.GetLearningProposals(),
		LearnedSkills:     c.GetLearnedSkills(),
		StorageHealth:     c.GetStorageHealth(),
		StorageMigration:  c.GetStorageMigration(),
		StorageCleanup:    c.GetStorageCleanup(),
		LegacyAdoption:    c.GetLegacyAdoption(),
		SessionDebug:      c.GetSessionDebug(),
		ManualDream:       c.GetManualDream(),
		ManualCompaction:  c.GetManualCompaction(),
		Posture:           c.GetPosture(),
	}
}

type sessionResp struct {
	SessionID string `json:"session_id"`
	State     string `json:"state"`
	Mode      string `json:"mode"`
	Workspace string `json:"workspace"`
	Turns     int    `json:"turns"`
	ToolCalls int    `json:"tool_calls"`
	// Title is the human-readable session label (snapshot Title, or the lazy
	// deriveTitle fallback when the snapshot Title is empty). Omitted via
	// omitempty only when both are empty (no genuine prompt).
	Title string `json:"title,omitempty"`
	// TitleProvenance records whether Title is prompt-derived, operator-authored,
	// or legacy/unknown.
	TitleProvenance string `json:"title_provenance,omitempty"`
	// ResolvedModel mirrors the gRPC Session snapshot's resolved_model so the HTTP
	// read surface is consistent with gRPC GetSession: the EFFECTIVE provider+model
	// this session resolved to (from Service.ResolvedModel, the composition single
	// source). Omitted (nil) when no model resolved (older-server-equivalent).
	ResolvedModel *resolvedModelJSON            `json:"resolved_model,omitempty"`
	Kind          string                        `json:"kind,omitempty"`
	Relationship  *mecatlv1.SessionRelationship `json:"relationship,omitempty"`
}

type dreamGenerateBody struct {
	Target string `json:"target"`
}

type dreamDecisionBody struct {
	Decision string `json:"decision"`
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
	sel := ProviderSelector{ProviderID: body.ProviderID, ModelID: body.ModelID, ReasoningEffort: body.ReasoningEffort}
	var opts []CreateSessionOption
	if body.SourceSessionID != "" {
		opts = append(opts, WithSourceSession(session.SessionID(body.SourceSessionID)))
	}
	if body.DebugTargetSessionID != "" {
		opts = append(opts, WithDebugTarget(session.SessionID(body.DebugTargetSessionID)))
	}
	sess, err := h.svc.CreateSessionWithProfile(r.Context(), body.Workspace, modeFromString(body.Mode), limits, sel, profile, opts...)
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

// getSessionTranscript handles GET /v1/sessions/{id}/transcript.
func (h *HTTPHandler) getSessionTranscript(w http.ResponseWriter, r *http.Request) {
	transcript, err := h.svc.GetTranscript(r.Context(), session.SessionID(r.PathValue("id")))
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toProtoTranscript(transcript))
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

// forkSession handles POST /v1/sessions/{id}/fork, creating a peer session whose
// conversation history is a snapshot of {id}'s (ADR 0065). The new session
// inherits the source's mode, workspace, limits, and provider/model/profile
// labels; same provider and model only. The source must be at a turn boundary
// (idle/terminal); a running/awaiting source is rejected with 412. An OPTIONAL
// JSON body `{"title": "...", "reasoning_effort": "..."}` overrides the forked
// session's title and/or reasoning-effort tier (ADR 0068; empty/absent inherits
// the source's — provider and model always inherit). Returns 201 + the new
// session id.
func (h *HTTPHandler) forkSession(w http.ResponseWriter, r *http.Request) {
	id := session.SessionID(r.PathValue("id"))
	var body struct {
		Title           string `json:"title"`
		ReasoningEffort string `json:"reasoning_effort"`
	}
	// An empty body is valid (title/effort inherit the source's); only a malformed
	// non-empty body is an error.
	if r.ContentLength != 0 {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON body")
			return
		}
	}
	newID, err := h.svc.ForkSession(r.Context(), id, body.Title, body.ReasoningEffort)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, struct {
		SessionID string `json:"session_id"`
	}{SessionID: string(newID)})
}

type adoptionBindingsJSON struct {
	Workspace       string `json:"workspace"`
	EnvironmentKind string `json:"environment_kind"`
	EnvironmentID   string `json:"environment_id"`
	ProviderID      string `json:"provider_id"`
	ModelID         string `json:"model_id"`
	Profile         string `json:"profile"`
	IdempotencyKey  string `json:"idempotency_key,omitempty"`
}

func (b adoptionBindingsJSON) bindings() (AdoptionBindings, error) {
	profile, err := ParseSessionProfile(b.Profile)
	if err != nil {
		return AdoptionBindings{}, err
	}
	return AdoptionBindings{Workspace: b.Workspace, EnvironmentRef: session.EnvironmentRef{Kind: session.EnvironmentKind(b.EnvironmentKind), ID: b.EnvironmentID}, ProviderID: b.ProviderID, ModelID: b.ModelID, Profile: profile}, nil
}

func adoptionBindingsJSONFrom(binding AdoptionBindings) adoptionBindingsJSON {
	return adoptionBindingsJSON{Workspace: binding.Workspace, EnvironmentKind: string(binding.EnvironmentRef.Kind), EnvironmentID: binding.EnvironmentRef.ID, ProviderID: binding.ProviderID, ModelID: binding.ModelID, Profile: string(binding.Profile)}
}

const maxAdoptionBodyBytes = 1 << 20

func (h *HTTPHandler) preflightSessionAdoption(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxAdoptionBodyBytes)
	var body adoptionBindingsJSON
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	bindings, err := body.bindings()
	if err != nil {
		writeServiceError(w, err)
		return
	}
	result, err := h.svc.PreflightSessionAdoption(r.Context(), session.SessionID(r.PathValue("id")), bindings)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Eligible bool                 `json:"eligible"`
		Reason   string               `json:"reason_code,omitempty"`
		Bindings adoptionBindingsJSON `json:"bindings"`
	}{result.Eligible, string(result.Reason), adoptionBindingsJSONFrom(result.Bindings)})
}

func (h *HTTPHandler) adoptSession(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxAdoptionBodyBytes)
	var body adoptionBindingsJSON
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	bindings, err := body.bindings()
	if err != nil {
		writeServiceError(w, err)
		return
	}
	sess, err := h.svc.AdoptSession(r.Context(), session.SessionID(r.PathValue("id")), body.IdempotencyKey, bindings)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, struct {
		SessionID       string                  `json:"session_id"`
		SourceSessionID string                  `json:"source_session_id"`
		Capabilities    *serverCapabilitiesJSON `json:"capabilities"`
		ResolvedModel   *resolvedModelJSON      `json:"resolved_model"`
	}{string(sess.ID), string(adoptionSourceID(sess)), capabilitiesJSON(h.svc.capabilities()), resolvedModelToJSON(h.svc.ResolvedModel(sess.ID))})
}

func (h *HTTPHandler) writeSession(w http.ResponseWriter, status int, sess *session.Session) {
	title := sess.Title
	if title == "" {
		// Lazy display-time fallback (no write-on-read: sess.Title is not mutated).
		title = DeriveTitle(sess)
	}
	writeJSON(w, status, sessionResp{
		SessionID:       string(sess.ID),
		State:           string(sess.State),
		Mode:            string(sess.Mode),
		Workspace:       sess.Workspace,
		Turns:           sess.Counters.Turns,
		ToolCalls:       sess.Counters.ToolCalls,
		Title:           title,
		TitleProvenance: string(sess.TitleProvenance),
		ResolvedModel:   resolvedModelToJSON(h.svc.ResolvedModel(sess.ID)),
		Kind:            string(sess.Kind),
		Relationship:    toProtoSessionRelationship(sess.Relationship),
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
	h.relayRunSSE(w, r, id, run, flusher, h.svc.RecoverNotice(id), false)
}

// retry handles POST /v1/sessions/{id}/retry. It has no request body and streams
// the failed-step retry through the same SSE relay and terminal cleanup as /prompt.
func (h *HTTPHandler) retry(w http.ResponseWriter, r *http.Request) {
	id := session.SessionID(r.PathValue("id"))
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}
	run, err := h.svc.RetryFailedRun(r.Context(), id)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	h.relayRunSSE(w, r, id, run, flusher, "", true)
}

// relayRunSSE streams run's Events to w as Server-Sent Events until the channel
// closes (the run ended). It is the SHARED relay used by both the prompt run-entry
// and the awaiting-approval re-entry (the approve handler's rehydrate path), so the
// SSE framing, the dead-client drain-to-discard, the disconnect-cancels-run hook,
// the EvPermissionAsk persist, and the deregister-on-end discipline cannot drift
// between the two entry points. The caller must already have validated the Flusher
// and written nothing to w yet (this sets the headers + 200 itself).
//
// notice, when non-empty, is a pre-flight EvRecoverNotice message emitted BEFORE
// the main event loop — the prompt run-entry path passes it; the approve handler
// path passes "".
func (h *HTTPHandler) relayRunSSE(w http.ResponseWriter, r *http.Request, id session.SessionID, run *agent.Run, flusher http.Flusher, notice string, persistAtEnd bool) {
	defer func() {
		if persistAtEnd {
			h.svc.Persist(context.WithoutCancel(r.Context()), id)
		}
		h.svc.deregister(id, run)
	}()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()
	logCtx := context.WithoutCancel(r.Context())
	recorder := NewRunEventRecorder(logCtx, h.svc, id)
	defer recorder.Close()
	enc := json.NewEncoder(w)

	// Inject a pre-flight EvRecoverNotice when the session just recovered from a
	// PERMANENT failure — surface the advisory BEFORE the main event loop burns a
	// provider call. Emitted ONCE per recovery.
	if notice != "" {
		ev := session.Event{Type: session.EvRecoverNotice, Text: notice}
		recorder.Observe(ev)
		if _, err := w.Write([]byte("data: ")); err != nil {
			return
		}
		if err := enc.Encode(toProto(ev)); err != nil {
			return
		}
		if _, err := w.Write([]byte("\n")); err != nil {
			return
		}
		flusher.Flush()
	}

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
	failed := false
	fail := func() {
		failed = true
		run.Cancel()
	}
	for ev := range run.Events() {
		if failed {
			// drain-to-discard: the client is gone. Still append to the durable
			// log (it must record the post-disconnect tail), but skip Persist /
			// auto-approve / the client write.
			recorder.Observe(ev)
			continue
		}
		if !h.svc.relayEvent(r.Context(), id, ev, true, recorder) {
			continue // log-only event: consumed by the durable log, not relayed to the client wire
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
		recorder := NewRunEventRecorder(logCtx, h.svc, id)
		go func() {
			defer recorder.Close()
			for ev := range run.Events() {
				recorder.Observe(ev)
			}
			h.svc.deregister(id, run)
		}()
		w.WriteHeader(http.StatusNoContent)
		return
	}
	h.relayRunSSE(w, r, id, run, flusher, "", false)
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

// planApproveBody is the JSON body for POST /v1/sessions/{id}/plan:approve. The
// target_mode string mirrors the proto enum names (case-insensitive): "default"
// → allow-once (flip to default), "accept_edits" → allow-always (flip to
// accept-edits), "plan"/"" → deny (iterate, no continuation run).
type planApproveBody struct {
	TargetMode string `json:"target_mode"`
	Note       string `json:"note"`
}

// approvePlan handles POST /v1/sessions/{id}/plan:approve, atomically resolving
// a parked plan-approval ask and streaming BOTH the resumed run's and (on an
// allow) the continuation run's events as SSE on this response. See
// Service.ApprovePlan for the contract. 409 on a precondition failure (live run /
// not awaiting / not a plan ask), 404 on an unknown session.
func (h *HTTPHandler) approvePlan(w http.ResponseWriter, r *http.Request) {
	id := session.SessionID(r.PathValue("id"))
	var body planApproveBody
	// An empty body is valid (target_mode "" → deny, no note); only a malformed
	// non-empty body is an error.
	if r.ContentLength != 0 {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON body")
			return
		}
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}
	// The Service's ctx-watcher cancels the live run when this ctx is cancelled,
	// so derive a cancellable child we can also trigger on a Send error (a dead
	// client): cancelling here propagates to the Service, which cancels the run,
	// ending the stream promptly instead of running on to completion unseen.
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	events, err := h.svc.ApprovePlan(ctx, id, planModeFromString(body.TargetMode), body.Note)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	h.relayEventsSSE(w, r, id, events, flusher, cancel)
}

// relayEventsSSE streams a Service-returned event channel to w as Server-Sent
// Events until the channel closes. It is the SHARED relay for the ApprovePlan
// path (a merged event stream the Service owns — distinct from relayRunSSE,
// which drains a single *agent.Run and owns run.Cancel/deregister). It mirrors
// relayRunSSE's discipline exactly: SSE framing, dead-client drain-to-discard,
// the durable event-log Append (decoupled from the client write, cancel-detached
// so a dead client never stops the log), the EvPermissionAsk Persist, the
// log-only-kind client-wire skip, and cancel-on-send-error (via the passed
// cancel, which the Service's ctx-watcher turns into a run cancel).
func (h *HTTPHandler) relayEventsSSE(w http.ResponseWriter, r *http.Request, id session.SessionID, events <-chan session.Event, flusher http.Flusher, cancel context.CancelFunc) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	// appendEvent uses a cancel-detached ctx so a dead client never stops the
	// durable log (it must record the post-disconnect tail, including the terminal
	// EvResult) — the same discipline as relayRunSSE.
	logCtx := context.WithoutCancel(r.Context())
	recorder := NewRunEventRecorder(logCtx, h.svc, id)
	defer recorder.Close()
	enc := json.NewEncoder(w)
	failed := false
	fail := func() {
		failed = true
		cancel()
	}
	for ev := range events {
		if failed {
			// drain-to-discard: the client is gone. Still append to the durable
			// log, but skip Persist / the client write.
			recorder.Observe(ev)
			continue
		}
		// autoApprove=false: this path IS the plan-approval resolution — running
		// the auto-approve observer inside it would recurse.
		if !h.svc.relayEvent(r.Context(), id, ev, false, recorder) {
			continue // log-only event: consumed by the durable log, not relayed to the client wire
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

// planModeFromString maps the HTTP plan:approve body's target_mode string to a
// session.PermissionMode. It accepts the proto enum names (case-insensitive,
// with or without the PERMISSION_MODE_ prefix) and the bare mode names
// ("default"/"plan"/"accept_edits"); an empty or unrecognized value defaults to
// ModePlan (deny / iterate) — the fail-safe posture that does NOT flip the mode
// or start a continuation run, mirroring verdictFromHTTP's fail-safe-to-deny.
func planModeFromString(s string) session.PermissionMode {
	switch strings.TrimPrefix(strings.ToLower(s), "permission_mode_") {
	case "default":
		return session.ModeDefault
	case "accept_edits", "accept", "acceptedits":
		return session.ModeAccept
	default:
		// "plan", "", or unrecognized → deny / iterate (no flip, no continuation).
		return session.ModePlan
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

// renameSession handles POST /v1/sessions/{id}/rename.
func (h *HTTPHandler) renameSession(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Title string `json:"title"`
	}
	if err := decodeLearningJSON(r, 2<<10, &body, false); err != nil || strings.TrimSpace(body.Title) == "" {
		writeError(w, http.StatusBadRequest, "a non-blank title is required")
		return
	}
	sess, err := h.svc.RenameSession(r.Context(), session.SessionID(r.PathValue("id")), body.Title)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	h.writeSession(w, http.StatusOK, sess)
}

// deleteSession handles POST /v1/sessions/{id}/delete. DELETE on the base path
// intentionally retains CloseSession's resource-release-only semantics.
func (h *HTTPHandler) deleteSession(w http.ResponseWriter, r *http.Request) {
	if err := h.svc.DeleteSession(r.Context(), session.SessionID(r.PathValue("id"))); err != nil {
		writeServiceError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// compactSession handles the bodyless POST /v1/sessions/{id}/compact action.
func (h *HTTPHandler) compactSession(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "session_id is required")
		return
	}
	result, err := h.svc.CompactSession(r.Context(), session.SessionID(id), session.PrincipalFromContext(r.Context()))
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, &mecatlv1.CompactSessionResponse{Compacted: result.Changed})
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

// --- schedule request bodies + handlers --------------------------------------

// decodeScheduleSpec reads r's body and unmarshals it into a *mecatlv1.ScheduleSpec
// via protojson — NOT stdlib encoding/json — so the proto well-known types
// embedded in ScheduleSpec (the one_shot google.protobuf.Timestamp, the Content
// oneof, the PermissionMode/MisfirePolicy enums) decode correctly from their
// wire JSON forms (RFC3339 string, oneof field, enum name-or-number). It is run
// through protoToScheduleSpec, the SAME mapping path the gRPC handler uses —
// one validation/mapping chokepoint, not two. DiscardUnknown preserves the
// previous lenient (ignore-unknown-field) decode behavior.
func decodeScheduleSpec(r *http.Request) (*mecatlv1.ScheduleSpec, error) {
	data, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, err
	}
	spec := &mecatlv1.ScheduleSpec{}
	unmarshaler := protojson.UnmarshalOptions{DiscardUnknown: true}
	if err := unmarshaler.Unmarshal(data, spec); err != nil {
		return nil, err
	}
	return spec, nil
}

// createSchedule handles POST /v1/schedules. The proto CreateScheduleResponse
// is JSON-encoded so the HTTP and gRPC surfaces share one shape.
func (h *HTTPHandler) createSchedule(w http.ResponseWriter, r *http.Request) {
	body, err := decodeScheduleSpec(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if body.GetName() == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	spec, err := protoToScheduleSpec(body)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	sched, err := h.svc.CreateSchedule(r.Context(), spec)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, &mecatlv1.CreateScheduleResponse{Schedule: scheduleToProto(sched)})
}

// listSchedules handles GET /v1/schedules.
func (h *HTTPHandler) listSchedules(w http.ResponseWriter, r *http.Request) {
	schedules, err := h.svc.ListSchedules(r.Context())
	if err != nil {
		writeServiceError(w, err)
		return
	}
	out := make([]*mecatlv1.Schedule, 0, len(schedules))
	for _, s := range schedules {
		out = append(out, scheduleToProto(s))
	}
	writeJSON(w, http.StatusOK, &mecatlv1.ListSchedulesResponse{Schedules: out})
}

// getSchedule handles GET /v1/schedules/{name}.
func (h *HTTPHandler) getSchedule(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	sched, err := h.svc.GetSchedule(r.Context(), name)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, &mecatlv1.GetScheduleResponse{Schedule: scheduleToProto(sched)})
}

// updateSchedule handles PUT /v1/schedules/{name}. The name rides the URL; the
// body's spec (if any name field) is overridden to the path-borne name.
func (h *HTTPHandler) updateSchedule(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	body, err := decodeScheduleSpec(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	body.Name = name
	spec, err := protoToScheduleSpec(body)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	sched, err := h.svc.UpdateSchedule(r.Context(), spec)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, &mecatlv1.UpdateScheduleResponse{Schedule: scheduleToProto(sched)})
}

// deleteSchedule handles DELETE /v1/schedules/{name}. Idempotent.
func (h *HTTPHandler) deleteSchedule(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if err := h.svc.DeleteSchedule(r.Context(), name); err != nil {
		writeServiceError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// fireNowSchedule handles POST /v1/schedules/{name}/fire, returning the per-fire
// session id (fire_id == session_id on the wire).
func (h *HTTPHandler) fireNowSchedule(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	fire, err := h.svc.FireNow(r.Context(), name)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, &mecatlv1.FireNowResponse{
		FireId:    fire.ID,
		SessionId: string(fire.SessionID),
	})
}

// pauseSchedule handles POST /v1/schedules/{name}/pause.
func (h *HTTPHandler) pauseSchedule(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if err := h.svc.PauseSchedule(r.Context(), name); err != nil {
		writeServiceError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// resumeSchedule handles POST /v1/schedules/{name}/resume.
func (h *HTTPHandler) resumeSchedule(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if err := h.svc.ResumeSchedule(r.Context(), name); err != nil {
		writeServiceError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// listFires handles GET /v1/schedules/{name}/fires.
func (h *HTTPHandler) listFires(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	fires, err := h.svc.ListFires(r.Context(), name)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	out := make([]*mecatlv1.ScheduleFire, 0, len(fires))
	for _, f := range fires {
		out = append(out, scheduleFireToProto(f))
	}
	writeJSON(w, http.StatusOK, &mecatlv1.ListFiresResponse{Fires: out})
}

// getFire handles GET /v1/schedules/{name}/fires/{id}.
func (h *HTTPHandler) getFire(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	fire, err := h.svc.GetFire(r.Context(), id)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, &mecatlv1.GetFireResponse{Fire: scheduleFireToProto(fire)})
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

func (h *HTTPHandler) listLearnedSkills(w http.ResponseWriter, r *http.Request) {
	limit, err := strconv.Atoi(defaultString(r.URL.Query().Get("limit"), "0"))
	if err != nil || limit < 0 || limit > learning.MaxSkillPageSize {
		http.Error(w, "invalid limit", http.StatusBadRequest)
		return
	}
	resp, err := h.svc.ListLearnedSkills(r.Context(), &mecatlv1.ListLearnedSkillsRequest{Project: r.URL.Query().Get("project"), Cursor: r.URL.Query().Get("cursor"), Limit: int32(limit), State: r.URL.Query().Get("state"), OwnerAgent: r.URL.Query().Get("owner_agent")}) //nolint:gosec // bounded above
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}
func (h *HTTPHandler) getLearnedSkill(w http.ResponseWriter, r *http.Request) {
	resp, err := h.svc.GetLearnedSkill(r.Context(), &mecatlv1.GetLearnedSkillRequest{Project: r.URL.Query().Get("project"), OwnerAgent: r.URL.Query().Get("owner_agent"), Id: r.PathValue("id"), Version: r.URL.Query().Get("version")})
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}
func (h *HTTPHandler) diffLearnedSkill(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	resp, err := h.svc.DiffLearnedSkillVersions(r.Context(), &mecatlv1.DiffLearnedSkillVersionsRequest{Project: q.Get("project"), OwnerAgent: q.Get("owner_agent"), Id: r.PathValue("id"), FromVersion: q.Get("from"), ToVersion: q.Get("to")})
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

type learnedSkillMutationBody struct {
	Project          string `json:"project"`
	OwnerAgent       string `json:"owner_agent"`
	Version          string `json:"version"`
	ExpectedRevision string `json:"expected_revision"`
	TargetVersion    string `json:"target_version"`
}

func (*HTTPHandler) decodeLearnedSkillMutation(w http.ResponseWriter, r *http.Request) (learnedSkillMutationBody, bool) {
	var body learnedSkillMutationBody
	if err := decodeLearningJSON(r, 16<<10, &body, false); err != nil || len(body.Project) > 4096 || len(body.OwnerAgent) > learning.MaxSkillOwnerBytes || len(body.Version) > 256 || len(body.ExpectedRevision) > 256 || len(body.TargetVersion) > 256 {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return body, false
	}
	return body, true
}
func (h *HTTPHandler) learnedSkillMutation(w http.ResponseWriter, r *http.Request, operation string) {
	body, ok := h.decodeLearnedSkillMutation(w, r)
	if !ok {
		return
	}
	request := &mecatlv1.MutateLearnedSkillRequest{Project: body.Project, OwnerAgent: body.OwnerAgent, Id: r.PathValue("id"), Version: body.Version, ExpectedRevision: body.ExpectedRevision}
	var resp *mecatlv1.MutateLearnedSkillResponse
	var err error
	switch operation {
	case "activate":
		resp, err = h.svc.ActivateLearnedSkill(r.Context(), request)
	case "reject":
		resp, err = h.svc.RejectLearnedSkill(r.Context(), request)
	case "archive":
		resp, err = h.svc.ArchiveLearnedSkill(r.Context(), request)
	}
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}
func (h *HTTPHandler) activateLearnedSkill(w http.ResponseWriter, r *http.Request) {
	h.learnedSkillMutation(w, r, "activate")
}
func (h *HTTPHandler) rejectLearnedSkill(w http.ResponseWriter, r *http.Request) {
	h.learnedSkillMutation(w, r, "reject")
}
func (h *HTTPHandler) archiveLearnedSkill(w http.ResponseWriter, r *http.Request) {
	h.learnedSkillMutation(w, r, "archive")
}
func (h *HTTPHandler) rollbackLearnedSkill(w http.ResponseWriter, r *http.Request) {
	body, ok := h.decodeLearnedSkillMutation(w, r)
	if !ok {
		return
	}
	resp, err := h.svc.RollbackLearnedSkill(r.Context(), &mecatlv1.RollbackLearnedSkillRequest{Project: body.Project, OwnerAgent: body.OwnerAgent, Id: r.PathValue("id"), TargetVersion: body.TargetVersion, ExpectedRevision: body.ExpectedRevision})
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}
func (h *HTTPHandler) listSkillChanges(w http.ResponseWriter, r *http.Request) {
	limit, err := strconv.Atoi(defaultString(r.URL.Query().Get("limit"), "0"))
	if err != nil || limit < 0 || limit > learning.MaxSkillPageSize {
		http.Error(w, "invalid limit", http.StatusBadRequest)
		return
	}
	resp, err := h.svc.ListSkillChanges(r.Context(), &mecatlv1.ListSkillChangesRequest{Project: r.URL.Query().Get("project"), Cursor: r.URL.Query().Get("cursor"), Limit: int32(limit)}) //nolint:gosec // bounded above
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func defaultString(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

// listModels handles GET /v1/models. Threads (issue #262) the per-provider
// live-listing status alongside the model list; ListModels itself triggers
// the on-demand refresh (when installed), so ProviderStatuses is read AFTER
// it to reflect the just-completed refresh.
func (h *HTTPHandler) listModels(w http.ResponseWriter, r *http.Request) {
	models := h.svc.ListModels(r.Context())
	writeJSON(w, http.StatusOK, &mecatlv1.ListModelsResponse{Models: models, ProviderStatus: h.svc.ProviderStatuses()})
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
	resp.Detail, err = h.svc.GetUserModelDetail(r.Context(), r.URL.Query().Get("key"))
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func rejectDuplicateJSONKeys(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	var walk func() error
	walk = func() error {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		delim, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		switch delim {
		case '{':
			seen := make(map[string]struct{})
			for decoder.More() {
				keyToken, keyErr := decoder.Token()
				if keyErr != nil {
					return keyErr
				}
				key, ok := keyToken.(string)
				if !ok {
					return errors.New("invalid JSON object key")
				}
				if _, duplicate := seen[key]; duplicate {
					return fmt.Errorf("duplicate JSON key %q", key)
				}
				seen[key] = struct{}{}
				if err := walk(); err != nil {
					return err
				}
			}
		case '[':
			for decoder.More() {
				if err := walk(); err != nil {
					return err
				}
			}
		default:
			return errors.New("invalid JSON delimiter")
		}
		_, err = decoder.Token()
		return err
	}
	return walk()
}

func decodeLearningJSON(r *http.Request, limit int64, dst any, allowEmpty bool) error {
	raw, err := io.ReadAll(io.LimitReader(r.Body, limit+1))
	if err != nil {
		return err
	}
	if int64(len(raw)) > limit {
		return errors.New("request body too large")
	}
	if len(strings.TrimSpace(string(raw))) == 0 {
		if allowEmpty {
			return nil
		}
		return io.ErrUnexpectedEOF
	}
	if err := rejectDuplicateJSONKeys(raw); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	return nil
}

func (h *HTTPHandler) reflectSession(w http.ResponseWriter, r *http.Request) {
	if err := decodeLearningJSON(r, 1<<10, &struct{}{}, true); err != nil {
		writeError(w, http.StatusBadRequest, "invalid reflection request")
		return
	}
	receipt, err := h.svc.ReflectSession(r.Context(), session.SessionID(r.PathValue("id")))
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, &mecatlv1.ReflectSessionResponse{Receipt: receipt})
}

func (h *HTTPHandler) generateDreamPlan(w http.ResponseWriter, r *http.Request) {
	var body dreamGenerateBody
	if err := decodeLearningJSON(r, 1<<10, &body, false); err != nil {
		writeError(w, http.StatusBadRequest, "invalid dream generation request")
		return
	}
	review, err := h.svc.GenerateDream(r.Context(), DreamTarget(body.Target))
	if err != nil {
		writeServiceError(w, normalizeDreamError(err))
		return
	}
	writeJSON(w, http.StatusOK, &mecatlv1.GenerateDreamPlanResponse{Plan: toProtoDreamReview(review)})
}

func (h *HTTPHandler) decideDreamPlan(w http.ResponseWriter, r *http.Request) {
	var body dreamDecisionBody
	if err := decodeLearningJSON(r, 1<<10, &body, false); err != nil {
		writeError(w, http.StatusBadRequest, "invalid dream decision request")
		return
	}
	receipt, err := h.svc.DecideDream(r.Context(), r.PathValue("plan_id"), DreamDecision(body.Decision))
	if err != nil && (!errors.Is(err, ErrDreamApplyFailed) || receipt.ID == "") {
		writeServiceError(w, normalizeDreamError(err))
		return
	}
	writeJSON(w, http.StatusOK, &mecatlv1.DecideDreamPlanResponse{Receipt: toProtoDreamReceipt(receipt)})
}

func (h *HTTPHandler) listLearningProposals(w http.ResponseWriter, r *http.Request) {
	limit, err := strconv.Atoi(r.URL.Query().Get("limit"))
	if err != nil && r.URL.Query().Get("limit") != "" {
		writeError(w, http.StatusBadRequest, "invalid proposal limit")
		return
	}
	resp, err := h.svc.ListLearningProposals(r.Context(), r.URL.Query().Get("status"), r.URL.Query().Get("cursor"), limit, r.URL.Query().Get("project"))
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *HTTPHandler) getLearningProposal(w http.ResponseWriter, r *http.Request) {
	proposal, err := h.svc.GetLearningProposal(r.Context(), r.PathValue("id"), r.URL.Query().Get("project"))
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, &mecatlv1.GetLearningProposalResponse{Proposal: proposal})
}

func (h *HTTPHandler) decideLearningProposal(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ExpectedVersion string `json:"expected_version"`
		Decision        string `json:"decision"`
		Reason          string `json:"reason"`
		Project         string `json:"project"`
	}
	if err := decodeLearningJSON(r, 4<<10, &body, false); err != nil {
		writeError(w, http.StatusBadRequest, "invalid proposal decision")
		return
	}
	proposal, err := h.svc.DecideLearningProposal(r.Context(), r.PathValue("id"), body.ExpectedVersion, body.Decision, body.Reason, body.Project)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, &mecatlv1.DecideLearningProposalResponse{Proposal: proposal})
}

func (h *HTTPHandler) undoLearningPromotion(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ExpectedVersion string `json:"expected_version"`
		Project         string `json:"project"`
	}
	if err := decodeLearningJSON(r, 2<<10, &body, false); err != nil {
		writeError(w, http.StatusBadRequest, "invalid proposal undo")
		return
	}
	proposal, err := h.svc.UndoLearningPromotion(r.Context(), r.PathValue("id"), body.ExpectedVersion, body.Project)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, &mecatlv1.UndoLearningPromotionResponse{Proposal: proposal})
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

// listSessions handles GET /v1/sessions — the stored-session inventory picker
// (issue #245 Phase 1). Read-only; loads no conversation content.
func (h *HTTPHandler) listSessions(w http.ResponseWriter, r *http.Request) {
	pageSize := 0
	if raw := r.URL.Query().Get("page_size"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 0 {
			writeServiceError(w, fmt.Errorf("%w: page_size must be a non-negative integer", ErrInvalidArgument))
			return
		}
		pageSize = parsed
	}
	page, err := h.svc.ListSessionPage(r.Context(), ListSessionsPageRequest{
		PageSize: pageSize, Cursor: r.URL.Query().Get("cursor"),
	})
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, &mecatlv1.ListSessionsResponse{
		Sessions: toProtoSessionSummaries(page.Sessions), NextCursor: page.NextCursor,
		TotalCount: ClampInt32(page.TotalCount),
	})
}

func (h *HTTPHandler) getStorageHealth(w http.ResponseWriter, r *http.Request) {
	health, err := h.svc.StorageHealth(r.Context())
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toProtoStorageHealth(health))
}

func (h *HTTPHandler) planSessionMigration(w http.ResponseWriter, r *http.Request) {
	plan, err := h.svc.PlanSessionMigration(r.Context())
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toProtoMigrationPlan(plan))
}

func (h *HTTPHandler) applySessionMigration(w http.ResponseWriter, r *http.Request) {
	var body struct {
		PlanID    string `json:"plan_id"`
		BatchSize int    `json:"batch_size"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeServiceError(w, fmt.Errorf("%w: invalid migration request", ErrInvalidArgument))
		return
	}
	job, err := h.svc.ApplySessionMigration(r.Context(), body.PlanID, body.BatchSize)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toProtoMigrationJob(job))
}

func (h *HTTPHandler) resumeSessionMigration(w http.ResponseWriter, r *http.Request) {
	var body struct {
		BatchSize int `json:"batch_size"`
	}
	if r.ContentLength != 0 {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeServiceError(w, fmt.Errorf("%w: invalid migration request", ErrInvalidArgument))
			return
		}
	}
	job, err := h.svc.ResumeSessionMigration(r.Context(), r.PathValue("id"), body.BatchSize)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toProtoMigrationJob(job))
}

func (h *HTTPHandler) cancelSessionMigration(w http.ResponseWriter, r *http.Request) {
	job, err := h.svc.CancelSessionMigration(r.Context(), r.PathValue("id"))
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toProtoMigrationJob(job))
}

func (h *HTTPHandler) getSessionMigrationJob(w http.ResponseWriter, r *http.Request) {
	job, err := h.svc.SessionMigrationJob(r.Context(), r.PathValue("id"))
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toProtoMigrationJob(job))
}

func (h *HTTPHandler) planSessionCleanup(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Kinds []string `json:"kinds"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeServiceError(w, fmt.Errorf("%w: invalid cleanup plan body", ErrInvalidArgument))
		return
	}
	scope := CleanupScope{}
	for _, kind := range body.Kinds {
		scope.Kinds = append(scope.Kinds, session.SessionKind(kind))
	}
	plan, err := h.svc.PlanSessionCleanup(r.Context(), scope)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toProtoCleanupPlan(plan))
}

func (h *HTTPHandler) applySessionCleanup(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Token string `json:"confirmation_token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeServiceError(w, fmt.Errorf("%w: invalid cleanup apply body", ErrInvalidArgument))
		return
	}
	job, err := h.svc.ApplySessionCleanup(r.Context(), body.Token)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toProtoCleanupJob(job))
}

func (h *HTTPHandler) cancelSessionCleanup(w http.ResponseWriter, r *http.Request) {
	job, err := h.svc.CancelSessionCleanup(r.Context(), r.PathValue("id"))
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toProtoCleanupJob(job))
}

func (h *HTTPHandler) getSessionCleanupJob(w http.ResponseWriter, r *http.Request) {
	job, err := h.svc.SessionCleanupJob(r.Context(), r.PathValue("id"))
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toProtoCleanupJob(job))
}

// streamSessionEvents handles GET /v1/sessions/{id}/events — replays a session's
// durable event log as a Server-Sent Events stream (issue #245 Phase 1; cloud-
// native Phase 3a read-back). This is the READ path: it never calls appendEvent
// and never starts a run.
func (h *HTTPHandler) streamSessionEvents(w http.ResponseWriter, r *http.Request) {
	id := session.SessionID(r.PathValue("id"))
	flusher, _ := w.(http.Flusher)
	if flusher == nil {
		writeError(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}
	events, err := h.svc.StreamSessionEvents(r.Context(), id)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	// CRITICAL (replay-vs-live): the LIVE relayRunSSE SKIPS the three log-only
	// event kinds (EvApproval/EvCompactionArchive/EvUserPrompt) on the client wire
	// because they are persistence-only. This is the READ-BACK of the durable log
	// itself — a client opening a PAST session WANTS the verdicts and user prompts
	// (they ARE the transcript). So relay ALL events through toProto, including the
	// three log-only kinds. They are already metadata-only/redacted by construction
	// (gauntlet #7). Do NOT copy the live-relay filter here.
	enc := json.NewEncoder(w)
	for ev, iterErr := range events {
		if iterErr != nil {
			// Mid-stream fault: emit an SSE error frame and stop. The iter.Seq2
			// releases its file handle on early break per port.EventLog.Read's
			// contract.
			_ = enc.Encode(map[string]string{"error": iterErr.Error()})
			_, _ = w.Write([]byte("\n"))
			flusher.Flush()
			return
		}
		if _, err := w.Write([]byte("data: ")); err != nil {
			return // client disconnected; the iter releases its file handle on break
		}
		if err := enc.Encode(toProto(ev)); err != nil { // Encode appends a newline
			return
		}
		if _, err := w.Write([]byte("\n")); err != nil {
			return
		}
		flusher.Flush()
	}
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
//
//nolint:gocyclo // a flat error→code classifier; a switch is the correct shape.
func writeServiceError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrManagementUnauthorized):
		writeError(w, http.StatusForbidden, err.Error())
	case errors.Is(err, ErrStorageHealthBackend):
		writeError(w, http.StatusInternalServerError, err.Error())
	case errors.Is(err, ErrMigrationUnsupported):
		writeError(w, http.StatusNotImplemented, err.Error())
	case errors.Is(err, ErrMigrationConflict):
		writeError(w, http.StatusConflict, err.Error())
	case errors.Is(err, ErrMigrationBackend):
		writeError(w, http.StatusInternalServerError, err.Error())
	case errors.Is(err, ErrCleanupPlanStale):
		writeError(w, http.StatusConflict, err.Error())
	case errors.Is(err, ErrCleanupUnsupported):
		writeError(w, http.StatusNotImplemented, err.Error())
	case errors.Is(err, ErrCleanupBackend):
		writeError(w, http.StatusInternalServerError, err.Error())
	case errors.Is(err, ErrInvalidArgument):
		writeError(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, ErrNotFound):
		writeError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, ErrTeamNotFound):
		writeError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, ErrChildNotFound):
		writeError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, ErrLearningUnavailable):
		writeError(w, http.StatusNotImplemented, err.Error())
	case errors.Is(err, ErrProposalConflict):
		writeError(w, http.StatusConflict, err.Error())
	case errors.Is(err, ErrDreamUnavailable):
		writeError(w, http.StatusNotImplemented, ErrDreamUnavailable.Error())
	case errors.Is(err, ErrDreamNotFound):
		writeError(w, http.StatusNotFound, ErrDreamNotFound.Error())
	case errors.Is(err, ErrDreamInProgress):
		writeError(w, http.StatusConflict, ErrDreamInProgress.Error())
	case errors.Is(err, ErrDreamConflict):
		writeError(w, http.StatusPreconditionFailed, ErrDreamConflict.Error())
	case errors.Is(err, ErrDreamTerminalConflict):
		writeError(w, http.StatusGone, ErrDreamTerminalConflict.Error())
	case errors.Is(err, ErrDreamCapacity):
		writeError(w, http.StatusTooManyRequests, ErrDreamCapacity.Error())
	case errors.Is(err, ErrDreamGenerateFailed):
		writeError(w, http.StatusInternalServerError, ErrDreamGenerateFailed.Error())
	case errors.Is(err, ErrDreamApplyFailed):
		writeError(w, http.StatusInternalServerError, ErrDreamApplyFailed.Error())
	case errors.Is(err, ErrDreamDeadline):
		writeError(w, http.StatusGatewayTimeout, ErrDreamDeadline.Error())
	case errors.Is(err, ErrDreamRequestFailed):
		writeError(w, http.StatusInternalServerError, ErrDreamRequestFailed.Error())
	case errors.Is(err, ErrFailedStepRetryIneligible):
		writeError(w, http.StatusConflict, err.Error())
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
	case errors.Is(err, ErrNoScheduleStore):
		// The configured store backend does not implement ScheduleStore: the
		// schedule RPCs are not available on this deployment. 501.
		writeError(w, http.StatusNotImplemented, err.Error())
	case errors.Is(err, ErrNoEventLog):
		// No durable EventLog (cloud-native Phase 3a) is configured: the
		// StreamSessionEvents read-back surface is not available on this
		// deployment. 501 (gRPC Unimplemented).
		writeError(w, http.StatusNotImplemented, err.Error())
	case errors.Is(err, ErrSessionDeleteUnsupported):
		writeError(w, http.StatusNotImplemented, err.Error())
	case errors.Is(err, port.ErrSessionMetadataCursorRestart):
		writeError(w, http.StatusConflict, err.Error())
	case errors.Is(err, port.ErrSessionMetadataPagingUnsupported):
		writeError(w, http.StatusNotImplemented, err.Error())
	case errors.Is(err, ErrSchedulerNotRunning):
		// A ScheduleStore is available but no in-process scheduler is wired to
		// drive a manual FireNow. 412 (gRPC FailedPrecondition), distinct from
		// ErrNoScheduleStore's 501 (the store itself works fine).
		writeError(w, http.StatusPreconditionFailed, err.Error())
	case errors.Is(err, ErrScheduleDisabled):
		// FireNow on a paused/done schedule. 412 (gRPC FailedPrecondition).
		writeError(w, http.StatusPreconditionFailed, err.Error())
	case errors.Is(err, ErrScheduleExhausted):
		// FireNow on an already-fired one-shot. 412 (gRPC FailedPrecondition).
		writeError(w, http.StatusPreconditionFailed, err.Error())
	case errors.Is(err, ErrFireNowOverlap):
		// FireNow singleton-overlap skip. 412 (gRPC FailedPrecondition) — the
		// schedule exists and is well-formed, it is just running.
		writeError(w, http.StatusPreconditionFailed, err.Error())
	case errors.Is(err, ErrScheduleNotLeader):
		// FireNow on a standby (non-leader) replica. 412 (gRPC
		// FailedPrecondition); the message names the leader to redirect to.
		writeError(w, http.StatusPreconditionFailed, err.Error())
	case errors.Is(err, port.ErrScheduleNotFound):
		// A schedule/fire not found from the store. 404.
		writeError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, port.ErrScheduleUnsupported):
		// The backend can never store schedules (a sticky-disable case). 501.
		writeError(w, http.StatusNotImplemented, err.Error())
	case errors.Is(err, ErrNoActiveRun):
		// Known session, but its run is not live in this process (e.g. the
		// stream was lost across a restart): nothing to deliver the control to.
		writeError(w, http.StatusConflict, err.Error())
	case errors.Is(err, ErrNotAwaitingPlan):
		// ApprovePlan precondition (issue #206, Wave 4): the session is not parked
		// awaiting a plan-originated ask (it is live, not awaiting, or awaiting a
		// generic tool ask). 409 Conflict — the session exists and is well-formed,
		// it is just not in the state this atomic RPC requires.
		writeError(w, http.StatusConflict, err.Error())
	case errors.Is(err, ErrSessionLeasedElsewhere):
		// Cloud-native Phase 4: another replica holds the session's single-writer
		// lease. 409 Conflict — the session exists and is well-formed, it is just
		// owned by another process right now (a later retry can succeed).
		writeError(w, http.StatusConflict, err.Error())
	case errors.Is(err, ErrUnavailable):
		// ADR 0048 drain gate: this replica is draining (graceful shutdown) and
		// refuses new run-entries. 503 so the client retries a survivor.
		writeError(w, http.StatusServiceUnavailable, err.Error())
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
