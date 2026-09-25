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
	"github.com/stacklok/mecatl/contracts/sessionaffinity"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/learning"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/mcp"
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
//	GET    /v1/sessions/{id}/mcp-authorizations/{authorization_id}/presentation -> live browser URL
//	POST   /v1/sessions/{id}/mcp-authorizations/{authorization_id}/recheck -> status/continuation SSE
//	POST   /v1/sessions/{id}/mcp-authorizations/{authorization_id}/cancel  -> cancellation/continuation SSE
//	POST   /v1/sessions/{id}/workspace-enrollment/connect -> begin or observe workspace enrollment
//	POST   /v1/sessions/{id}/workspace-enrollment/{enrollment_id}/retry -> replace one exact enrollment
//	POST   /v1/sessions/{id}/workspace-enrollment/{enrollment_id}/cancel -> cancel one exact enrollment
//	POST   /v1/sessions/{id}/prompt   -> start a run; text/event-stream of Events
//	POST   /v1/sessions/{id}/controls/resolve-ask -> resolve an ask on one exact run
//	POST   /v1/sessions/{id}/controls/cancel -> cancel one exact run
//	POST   /v1/sessions/{id}/cancel-child -> cancel ONE child (subagent) of the run
//	POST   /v1/sessions/{id}/controls/steer -> steer one exact run
//	POST   /v1/sessions/{id}/controls/cancel-steer -> retract a steer on one exact run
//	POST   /v1/sessions/{id}/fork     -> ForkSession (peer session from a history snapshot; 201)
//	GET    /v1/sessions/{id}/events   -> replay the durable event log; the stream ENDS
//	GET    /v1/sessions/{id}/watch    -> durable replay-then-follow; the stream STAYS OPEN
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
	h.mux.HandleFunc("GET /v1/compatibility", h.getCompatibilityInfo)
	h.mux.HandleFunc("POST /v1/sessions", h.createSession)
	for _, route := range []struct {
		pattern string
		handler http.HandlerFunc
	}{
		{"GET /v1/sessions/{id}", h.getSession},
		{"GET /v1/sessions/{id}/mcp/connectors", h.listSessionMcpConnectors},
		{"GET /v1/sessions/{id}/transcript", h.getSessionTranscript},
		{"POST /v1/sessions/{id}/mode", h.setMode},
		{"DELETE /v1/sessions/{id}", h.closeSession},
		{"POST /v1/sessions/{id}/rename", h.renameSession},
		{"POST /v1/sessions/{id}/delete", h.deleteSession},
		{"POST /v1/sessions/{id}/compact", h.compactSession},
		{"POST /v1/sessions/{id}/mcp-refresh", h.refreshMcpSources},
		{"GET /v1/sessions/{id}/mcp-authorizations/{authorization_id}/presentation", h.mcpAuthorizationPresentation},
		{"POST /v1/sessions/{id}/mcp-authorizations/{authorization_id}/recheck", h.recheckMCPAuthorization},
		{"POST /v1/sessions/{id}/mcp-authorizations/{authorization_id}/cancel", h.cancelMCPAuthorization},
		{"POST /v1/sessions/{id}/workspace-enrollment/connect", h.connectWorkspaceServices},
		{"POST /v1/sessions/{id}/workspace-enrollment/{enrollment_id}/retry", h.retryWorkspaceEnrollment},
		{"POST /v1/sessions/{id}/workspace-enrollment/{enrollment_id}/cancel", h.cancelWorkspaceEnrollment},
		{"POST /v1/sessions/{id}/prompt", h.prompt},
		{"POST /v1/sessions/{id}/retry", h.retry},
		{"POST /v1/sessions/{id}/plan:approve", h.approvePlan},
		{"POST /v1/sessions/{id}/cancel-child", h.cancelChild},
		{"POST /v1/sessions/{id}/controls/resolve-ask", h.resolveRunAsk},
		{"POST /v1/sessions/{id}/controls/resolve-plan-ask", h.resolvePlanAsk},
		{"POST /v1/sessions/{id}/controls/cancel", h.cancelRun},
		{"POST /v1/sessions/{id}/controls/steer", h.steerRun},
		{"POST /v1/sessions/{id}/controls/cancel-steer", h.cancelRunSteer},
		{"POST /v1/sessions/{id}/fork", h.forkSession},
		{"POST /v1/sessions/{id}/clear", h.clearSession},
		{"POST /v1/sessions/{id}/reflect", h.reflectSession},
		{"GET /v1/sessions/{id}/events", h.streamSessionEvents},
		{"GET /v1/sessions/{id}/watch", h.watchSessionEvents},
	} {
		h.mux.HandleFunc(route.pattern, requireSessionAffinity(route.handler))
	}
	h.mux.HandleFunc("POST /v1/dream/plans", h.generateDreamPlan)
	h.mux.HandleFunc("POST /v1/dream/plans/{plan_id}/decision", h.decideDreamPlan)
	h.mux.HandleFunc("GET /v1/learning/attempts", h.listLearningAttempts)
	h.mux.HandleFunc("GET /v1/learning/attempts/{id}", h.getLearningAttempt)
	h.mux.HandleFunc("POST /v1/learning/attempts/{id}/retry", h.retryLearningAttempt)
	h.mux.HandleFunc("POST /v1/learning/attempts/{id}/abandon", h.abandonLearningAttempt)
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
	h.mux.HandleFunc("POST /v1/storage/cleanup:plan", h.planSessionCleanup)
	h.mux.HandleFunc("POST /v1/storage/cleanup:apply", h.applySessionCleanup)
	h.mux.HandleFunc("POST /v1/storage/cleanup/jobs/{id}/cancel", h.cancelSessionCleanup)
	h.mux.HandleFunc("GET /v1/storage/cleanup/jobs/{id}", h.getSessionCleanupJob)
	// Session event routes are registered with the session route inventory above.
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
	// ServeMux canonicalizes the doubled separator in this one session-scoped
	// route before matching, which would turn an empty id into a redirect. Keep
	// the REST contract aligned with gRPC: it is an invalid request, never a
	// cacheable redirect.
	if r.Method == http.MethodGet && r.URL.Path == "/v1/sessions//mcp/connectors" {
		requireSessionAffinity(h.listSessionMcpConnectors)(w, r)
		return
	}
	h.mux.ServeHTTP(w, r)
}

func requireCreateSessionAffinity(w http.ResponseWriter, r *http.Request, debugTargetID string) bool {
	values := r.Header.Values(sessionaffinity.HeaderName)
	if len(values) == 0 {
		return true
	}
	if debugTargetID == "" || len(values) != 1 || !sessionaffinity.ValidValue(values[0]) || values[0] != debugTargetID {
		writeProblem(w, entryForHTTPStatus(http.StatusBadRequest), "invalid session affinity header")
		return false
	}
	return true
}

func requireSessionAffinityValue(w http.ResponseWriter, r *http.Request, authoritativeID string) bool {
	values := r.Header.Values(sessionaffinity.HeaderName)
	if len(values) == 0 {
		return true
	}
	if len(values) != 1 || !sessionaffinity.ValidValue(values[0]) || values[0] != authoritativeID {
		writeProblem(w, entryForHTTPStatus(http.StatusBadRequest), "invalid session affinity header")
		return false
	}
	return true
}

// requireSessionAffinity admits an optional, exact session-affinity header only
// after the mux has decoded the authoritative path value. It is transport
// routing metadata, not an authority grant; the wrapped handler still performs
// its ordinary authentication, ownership, and management checks.
func requireSessionAffinity(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireSessionAffinityValue(w, r, r.PathValue("id")) {
			return
		}
		next(w, r)
	}
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
	Mode   string    `json:"mode,omitempty"`
	Limits *limitsIn `json:"limits,omitempty"`
	// ProviderID / ModelID select a per-session provider+model (multi-provider
	// Phase 0, S3). Empty both => the server default provider. ProviderID without
	// ModelID => the provider's default model; ModelID without ProviderID is a
	// client error (a bare model on the default provider is ambiguous).
	ProviderID string `json:"provider_id,omitempty"`
	ModelID    string `json:"model_id,omitempty"`
	// Profile selects server-owned placement: "" binds the deployment default and
	// "no-fs" explicitly attenuates filesystem access. The public request carries
	// no workspace, cwd, placement ID, or selector. Any other value is a 400.
	Profile string `json:"profile,omitempty"`
	// ReasoningEffort sets the session's reasoning-effort tier (ADR 0055),
	// mirroring the proto field: "" / "auto" = unset (operator/provider default),
	// else low/medium/high/xhigh/max. The server normalises + per-provider-clamps +
	// capability-gates it; an unknown value falls back to the operator default with
	// a WARN. The effective value is echoed on resolved_model.reasoning_effort.
	ReasoningEffort string `json:"reasoning_effort,omitempty"`
	// DebugTargetSessionID creates a separate no-fs diagnostic session bound to
	// one authorized target; it never copies target conversation state.
	DebugTargetSessionID string   `json:"debug_target_session_id,omitempty"`
	DebugMCPServers      []string `json:"debug_mcp_servers,omitempty"`
	// MCPServers are CLIENT-PROVIDED streaming-HTTP MCP servers mounted for this
	// session's lifetime, mirroring the proto field (issue #821, ADR 0237). Empty
	// is byte-identical to today. Whether the field is accepted at all is a
	// DEPLOYMENT policy: a deployment with any network-facing API listener refuses
	// every non-empty value with a 501 "client_mcp_unsupported" problem. A stdio or
	// sse entry is a 400 on every deployment.
	MCPServers []mcpServerIn `json:"mcp_servers,omitempty"`
}

// mcpServerIn is one client-provided MCP server on the HTTP create body. It
// mirrors the proto McpServerSpec field-for-field so the two transports accept
// the same request, and carries Command ONLY so a command-shaped entry is
// classified as stdio and rejected AS stdio — it is never executed.
//
// Headers values are secret-shaped: never logged, never echoed in the response,
// never included in an error.
type mcpServerIn struct {
	Name    string            `json:"name,omitempty"`
	URL     string            `json:"url,omitempty"`
	Type    string            `json:"type,omitempty"`
	Command string            `json:"command,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
}

// clientMCPFromJSON maps the HTTP create body's MCP entries onto the
// transport-neutral shape the shared classifier consumes. Like its gRPC peer it
// is a pure field mapping and makes no decisions: classification and the
// deployment policy both live behind Service.ClientMCPFromWire.
func clientMCPFromJSON(in []mcpServerIn) []mcp.ClientServer {
	if len(in) == 0 {
		return nil
	}
	out := make([]mcp.ClientServer, 0, len(in))
	for _, m := range in {
		out = append(out, mcp.ClientServer{
			Name:    m.Name,
			Command: m.Command,
			URL:     m.URL,
			Type:    m.Type,
			Headers: m.Headers,
		})
	}
	return out
}

type limitsIn struct {
	MaxTurns               int `json:"max_turns,omitempty"`
	MaxToolCalls           int `json:"max_tool_calls,omitempty"`
	MaxConsecutiveFailures int `json:"max_consecutive_failures,omitempty"`
}

type createSessionResp struct {
	SessionID string `json:"session_id"`
	// SessionCapabilities echoes the per-session resolved input capability (catalog
	// ∩ adapter for THIS session's provider+model), so an HTTP client gates
	// per-session @-attach UX on the same intersected value the gRPC client gets.
	SessionCapabilities *sessionCapabilitiesJSON `json:"session_capabilities,omitempty"`
	// ResolvedModel echoes the EFFECTIVE provider+model THIS session resolved to
	// (the composition single source via Service.ResolvedModel), so an HTTP client
	// shows the same effective model the gRPC client gets. Omitted (nil) when no
	// model resolved (older-server-equivalent fallback).
	ResolvedModel *resolvedModelJSON     `json:"resolved_model,omitempty"`
	Placement     *placementMetadataJSON `json:"placement,omitempty"`
}

type placementMetadataJSON struct {
	Kind     string `json:"kind,omitempty"`
	Label    string `json:"label,omitempty"`
	Branch   string `json:"branch,omitempty"`
	Revision string `json:"revision,omitempty"`
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

func placementMetadataToJSON(meta session.PlacementMetadata) *placementMetadataJSON {
	if meta.Kind == "" {
		return nil
	}
	return &placementMetadataJSON{Kind: meta.Kind, Label: meta.Label, Branch: meta.Branch, Revision: meta.Revision}
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

type sessionResp struct {
	SessionID string                 `json:"session_id"`
	State     string                 `json:"state"`
	Mode      string                 `json:"mode"`
	Placement *placementMetadataJSON `json:"placement,omitempty"`
	Turns     int                    `json:"turns"`
	ToolCalls int                    `json:"tool_calls"`
	// SessionCapabilities mirrors the per-session media capability carried by the
	// create and gRPC snapshot surfaces. Global feature bits remain on capabilities.
	SessionCapabilities *sessionCapabilitiesJSON `json:"session_capabilities,omitempty"`
	// TitleMetadata is the bounded source-free title lifecycle projection.
	TitleMetadata *sessionTitleJSON `json:"title_metadata,omitempty"`
	// TokenUsage is the canonical durable accounting projection.
	TokenUsage map[string]tokenUsageJSON `json:"token_usage,omitempty"`
	// ResolvedModel mirrors the gRPC Session snapshot's resolved_model so the HTTP
	// read surface is consistent with gRPC GetSession: the EFFECTIVE provider+model
	// this session resolved to (from Service.ResolvedModel, the composition single
	// source). Omitted (nil) when no model resolved (older-server-equivalent).
	ResolvedModel *resolvedModelJSON            `json:"resolved_model,omitempty"`
	Kind          string                        `json:"kind,omitempty"`
	Relationship  *mecatlv1.SessionRelationship `json:"relationship,omitempty"`
}

type sessionTitleJSON struct {
	Title           string                   `json:"title"`
	Provenance      string                   `json:"provenance"`
	GenerationState string                   `json:"generation_state"`
	LatestAttempt   *titleAttemptSummaryJSON `json:"latest_attempt,omitempty"`
	Revision        uint64                   `json:"revision"`
}

type titleAttemptSummaryJSON struct {
	ID      string `json:"id"`
	Outcome string `json:"outcome"`
}

type tokenUsageJSON struct {
	Total  usageJSON            `json:"total"`
	Models map[string]usageJSON `json:"models"`
}

type usageJSON struct {
	InputTokens      int `json:"input_tokens"`
	OutputTokens     int `json:"output_tokens"`
	CacheReadTokens  int `json:"cache_read_tokens"`
	CacheWriteTokens int `json:"cache_write_tokens"`
	ReasoningTokens  int `json:"reasoning_tokens"`
}

func sessionTitleToJSON(p session.TitlePayload) *sessionTitleJSON {
	out := &sessionTitleJSON{Title: valid(p.Title), Provenance: valid(string(p.Provenance)), GenerationState: valid(string(p.GenerationState)), Revision: p.Revision}
	if p.LatestAttempt != nil {
		out.LatestAttempt = &titleAttemptSummaryJSON{ID: valid(p.LatestAttempt.ID), Outcome: valid(string(p.LatestAttempt.Outcome))}
	}
	return out
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
	Text                        string `json:"text"`
	ServerOwnedPlanContinuation bool   `json:"server_owned_plan_continuation,omitempty"`
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

// --- handlers ---------------------------------------------------------------

// createSession handles POST /v1/sessions.
func (h *HTTPHandler) createSession(w http.ResponseWriter, r *http.Request) {
	var body createSessionBody
	// STRICT decode. An unknown field is a 400, not a silent drop.
	//
	// This body is where leniency stopped being harmless: a client coming from the
	// gRPC surface (or using a generated client) naturally writes the protojson
	// spelling {"mcpServers": [...]}, which a lenient decoder discards — returning
	// 201 with a session that has none of the MCP servers the caller asked for, and
	// no signal anywhere that it dropped them. That is the same silent-degradation
	// class as a partial mount, on the transport where it is easiest to hit.
	//
	// The error detail is surfaced because encoding/json names the offending field
	// ("unknown field \"mcpServers\""), which turns an otherwise baffling 400 into a
	// self-diagnosing one. It describes the caller's own input, so it leaks nothing.
	//
	// It is a deliberate behaviour CHANGE: a request carrying a stray field used to
	// succeed. The strictness matches decodeLearningJSON's existing posture on this
	// same handler set.
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "invalid JSON body: multiple JSON values")
		return
	}
	if !requireCreateSessionAffinity(w, r, body.DebugTargetSessionID) {
		return
	}
	// Parse only the public profile attenuation. The service binds either the
	// deployment default or no-FS placement and validates the exact EnvironmentRef;
	// this transport has no workspace-derived fallback.
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
	if body.DebugTargetSessionID != "" {
		opts = append(opts, WithDebugTarget(session.SessionID(body.DebugTargetSessionID)))
	}
	if len(body.DebugMCPServers) > 0 {
		opts = append(opts, WithDebugMCP(body.DebugMCPServers))
	}
	// Client-provided MCP servers (issue #821, ADR 0237): the SAME Service seam the
	// gRPC handler calls, so both transports classify through one validator and
	// read one deployment policy. No filtering or classification happens here.
	grant, err := h.svc.ClientMCPFromWire(clientMCPFromJSON(body.MCPServers))
	if err != nil {
		writeServiceError(w, err)
		return
	}
	if !grant.IsEmpty() {
		opts = append(opts, WithClientMCP(grant))
	}
	sess, err := h.svc.CreateSessionWithProfile(r.Context(), modeFromString(body.Mode), limits, sel, profile, opts...)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	scaps := h.svc.sessionCapabilitiesFor(sess)
	writeJSON(w, http.StatusCreated, createSessionResp{
		SessionID:           string(sess.ID),
		SessionCapabilities: &sessionCapabilitiesJSON{Image: scaps.Image, Audio: scaps.Audio},
		ResolvedModel:       resolvedModelToJSON(h.svc.resolvedModelFor(sess)),
		Placement:           placementMetadataToJSON(sess.Placement),
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

type successorBody struct {
	WorktreeSelector *string `json:"worktree_selector,omitempty"`
	Title            string  `json:"title,omitempty"`
	ProviderID       string  `json:"provider_id,omitempty"`
	ModelID          string  `json:"model_id,omitempty"`
	ReasoningEffort  string  `json:"reasoning_effort,omitempty"`
}

func decodeOptionalStrictJSON(r *http.Request, dst any) error {
	if r.ContentLength == 0 {
		return nil
	}
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	return nil
}

func (h *HTTPHandler) clearSession(w http.ResponseWriter, r *http.Request) {
	var body successorBody
	if err := decodeOptionalStrictJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	selector := ""
	if body.WorktreeSelector != nil {
		selector = *body.WorktreeSelector
	}
	newID, err := h.svc.ClearSessionSuccessor(r.Context(), session.SessionID(r.PathValue("id")), SuccessorPlacement{Selector: selector, SelectorPresent: body.WorktreeSelector != nil})
	if err != nil {
		writeServiceError(w, err)
		return
	}
	h.writeSuccessor(r.Context(), w, newID)
}

func (h *HTTPHandler) forkSession(w http.ResponseWriter, r *http.Request) {
	var body successorBody
	if err := decodeOptionalStrictJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	selector := ""
	if body.WorktreeSelector != nil {
		selector = *body.WorktreeSelector
	}
	newID, err := h.svc.ForkSessionSuccessor(r.Context(), ForkSuccessorRequest{
		Source: session.SessionID(r.PathValue("id")), Placement: SuccessorPlacement{Selector: selector, SelectorPresent: body.WorktreeSelector != nil},
		Title: body.Title, ProviderID: body.ProviderID, ModelID: body.ModelID, ReasoningEffort: body.ReasoningEffort,
	})
	if err != nil {
		writeServiceError(w, err)
		return
	}
	h.writeSuccessor(r.Context(), w, newID)
}

func (h *HTTPHandler) writeSuccessor(ctx context.Context, w http.ResponseWriter, id session.SessionID) {
	created, err := h.svc.GetSession(ctx, id)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, struct {
		SessionID string                 `json:"session_id"`
		Placement *placementMetadataJSON `json:"placement,omitempty"`
	}{string(id), placementMetadataToJSON(created.Placement)})
}

func (h *HTTPHandler) writeSession(w http.ResponseWriter, status int, sess *session.Session) {
	scaps := h.svc.sessionCapabilitiesFor(sess)
	writeJSON(w, status, sessionResp{
		SessionID:           string(sess.ID),
		State:               string(sess.State),
		Mode:                string(sess.Mode),
		Placement:           placementMetadataToJSON(sess.Placement),
		Turns:               sess.Counters.Turns,
		ToolCalls:           sess.Counters.ToolCalls,
		SessionCapabilities: &sessionCapabilitiesJSON{Image: scaps.Image, Audio: scaps.Audio},
		TitleMetadata:       sessionTitleToJSON(titlePayload(sess)),
		TokenUsage:          tokenUsageToJSON(sess.TokenUsageSnapshot()),
		ResolvedModel:       resolvedModelToJSON(h.svc.resolvedModelFor(sess)),
		Kind:                string(sess.Kind),
		Relationship:        toProtoSessionRelationship(sess.Relationship),
	})
}

func tokenUsageToJSON(in map[session.UsageKind]session.TokenUsage) map[string]tokenUsageJSON {
	out := make(map[string]tokenUsageJSON, len(in))
	for kind, bucket := range in {
		models := make(map[string]usageJSON, len(bucket.Models))
		for model, usage := range bucket.Models {
			models[valid(model)] = usageToJSON(usage)
		}
		out[string(kind)] = tokenUsageJSON{Total: usageToJSON(bucket.Total), Models: models}
	}
	return out
}

func usageToJSON(usage session.Usage) usageJSON {
	return usageJSON{InputTokens: usage.InputTokens, OutputTokens: usage.OutputTokens, CacheReadTokens: usage.CacheReadTokens, CacheWriteTokens: usage.CacheWriteTokens, ReasoningTokens: usage.ReasoningTokens}
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

func (h *HTTPHandler) connectWorkspaceServices(w http.ResponseWriter, r *http.Request) {
	if !controlRequestBodyEmpty(r) {
		writeError(w, http.StatusBadRequest, "workspace enrollment controls do not accept a request body")
		return
	}
	id := session.SessionID(r.PathValue("id"))
	if id == "" {
		writeError(w, http.StatusBadRequest, "session ID is required")
		return
	}
	result, err := h.svc.ConnectWorkspaceServices(r.Context(), id)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toProtoWorkspaceEnrollment(result))
}

func (h *HTTPHandler) retryWorkspaceEnrollment(w http.ResponseWriter, r *http.Request) {
	h.workspaceEnrollmentControl(w, r, true)
}

func (h *HTTPHandler) cancelWorkspaceEnrollment(w http.ResponseWriter, r *http.Request) {
	h.workspaceEnrollmentControl(w, r, false)
}

func (h *HTTPHandler) workspaceEnrollmentControl(w http.ResponseWriter, r *http.Request, retry bool) {
	if !controlRequestBodyEmpty(r) {
		writeError(w, http.StatusBadRequest, "workspace enrollment controls do not accept a request body")
		return
	}
	id, enrollmentID := session.SessionID(r.PathValue("id")), session.WorkspaceEnrollmentID(r.PathValue("enrollment_id"))
	if id == "" || !enrollmentID.Valid() {
		writeError(w, http.StatusBadRequest, "valid session and enrollment IDs are required")
		return
	}
	var (
		result WorkspaceEnrollmentProjection
		err    error
	)
	if retry {
		result, err = h.svc.RetryWorkspaceEnrollment(r.Context(), id, enrollmentID)
	} else {
		result, err = h.svc.CancelWorkspaceEnrollment(r.Context(), id, enrollmentID)
	}
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toProtoWorkspaceEnrollment(result))
}

func (h *HTTPHandler) mcpAuthorizationPresentation(w http.ResponseWriter, r *http.Request) {
	if !controlRequestBodyEmpty(r) {
		writeError(w, http.StatusBadRequest, "MCP authorization controls do not accept a request body")
		return
	}
	id, authorizationID := session.SessionID(r.PathValue("id")), r.PathValue("authorization_id")
	if id == "" || !session.ValidAuthorizationID(authorizationID) {
		writeError(w, http.StatusBadRequest, "invalid session or authorization ID")
		return
	}
	url, err := h.svc.MCPAuthorizationPresentation(r.Context(), id, MCPAuthorizationControl{SessionID: id, AuthorizationID: authorizationID})
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, &mecatlv1.GetMcpAuthorizationPresentationResponse{Url: valid(url)})
}

func (h *HTTPHandler) recheckMCPAuthorization(w http.ResponseWriter, r *http.Request) {
	h.relayMCPAuthorizationControlSSE(w, r, false)
}

func (h *HTTPHandler) cancelMCPAuthorization(w http.ResponseWriter, r *http.Request) {
	h.relayMCPAuthorizationControlSSE(w, r, true)
}

// controlRequestBodyEmpty enforces the correlation-only HTTP shape.
// Reading at most one byte rejects JSON success/status/code/token assertions
// without buffering attacker-controlled bodies.
func controlRequestBodyEmpty(r *http.Request) bool {
	if r.Body == nil || r.Body == http.NoBody {
		return true
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1))
	return err == nil && len(body) == 0
}

//nolint:gocyclo // relays a live continuation over SSE while racing client disconnect and cancellation; inherent.
func (h *HTTPHandler) relayMCPAuthorizationControlSSE(w http.ResponseWriter, r *http.Request, cancel bool) {
	if !controlRequestBodyEmpty(r) {
		writeError(w, http.StatusBadRequest, "MCP authorization controls do not accept a request body")
		return
	}
	id, authorizationID := session.SessionID(r.PathValue("id")), r.PathValue("authorization_id")
	if id == "" || !session.ValidAuthorizationID(authorizationID) {
		writeError(w, http.StatusBadRequest, "invalid session or authorization ID")
		return
	}
	// Recheck and cancel may register a continuation run. Prove this response can
	// drain that run before invoking either mutating control, so an incapable
	// ResponseWriter cannot leave a live continuation orphaned.
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}
	control := MCPAuthorizationControl{SessionID: id, AuthorizationID: authorizationID}
	var result MCPAuthorizationResult
	var err error
	if cancel {
		result, err = h.svc.CancelMCPAuthorization(r.Context(), id, control)
	} else {
		result, err = h.svc.RecheckMCPAuthorization(r.Context(), id, control)
	}
	if err != nil {
		writeServiceError(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()
	enc := json.NewEncoder(w)
	if result.Run == nil {
		if _, err := w.Write([]byte("data: ")); err != nil {
			return
		}
		if err := enc.Encode(toProto(result.Event)); err != nil {
			return
		}
		_, _ = w.Write([]byte("\n"))
		flusher.Flush()
		return
	}
	defer h.svc.FinishRun(id, result.Run)
	logCtx := context.WithoutCancel(r.Context())
	recorder := NewRunEventRecorder(logCtx, h.svc, id)
	defer recorder.Close()
	failed := false
	fail := func() {
		if failed {
			return
		}
		failed = true
		h.svc.cancelRegisteredRun(id, result.Run)
	}
	writeEvent := func(event session.Event) bool {
		if _, err := w.Write([]byte("data: ")); err != nil {
			return false
		}
		if err := enc.Encode(toProto(event)); err != nil {
			return false
		}
		if _, err := w.Write([]byte("\n")); err != nil {
			return false
		}
		flusher.Flush()
		return true
	}

	// The status precedes the continuation. If it cannot be delivered, retain
	// ownership of the registered run: cancel it, drain its events into the log,
	// then finish it below.
	if r.Context().Err() != nil || !writeEvent(result.Event) {
		fail()
	}
	requestDone := r.Context().Done()
	for events := result.Run.Events(); events != nil; {
		if !failed && r.Context().Err() != nil {
			fail()
			requestDone = nil
		}
		select {
		case <-requestDone:
			// A lost HTTP request has no control channel to recover through. Cancel
			// the continuation but keep draining so it can emit its terminal event.
			fail()
			requestDone = nil
		case event, ok := <-events:
			if !ok {
				events = nil
				continue
			}
			if failed {
				recorder.Observe(event)
				continue
			}
			if r.Context().Err() != nil {
				fail()
				requestDone = nil
				recorder.Observe(event)
				continue
			}
			if !h.svc.relayEvent(r.Context(), id, event, false, recorder) {
				continue
			}
			if r.Context().Err() != nil {
				fail()
				requestDone = nil
				continue
			}
			if !writeEvent(event) {
				fail()
			}
		}
	}
}

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

	var run *agent.Run
	var err error
	if body.ServerOwnedPlanContinuation {
		run, err = h.svc.StartInteractiveRunContentWithPlanContinuation(r.Context(), id, body.Text, parts)
	} else {
		run, err = h.svc.StartInteractiveRunContent(r.Context(), id, body.Text, parts)
	}
	if err != nil {
		writeServiceError(w, err)
		return
	}
	h.relayRunSSE(w, r, id, run, flusher, h.svc.RecoverNotice(id))
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
	h.relayRunSSE(w, r, id, run, flusher, "")
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
func (h *HTTPHandler) relayRunSSE(w http.ResponseWriter, r *http.Request, id session.SessionID, run *agent.Run, flusher http.Flusher, notice string) {
	defer func() {
		h.svc.finishRelayRun(context.WithoutCancel(r.Context()), id, run)
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
		h.svc.cancelRegisteredRun(id, run)
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
		h.svc.cancelRegisteredRun(id, run)
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
		proto := toProto(ev)
		if err := enc.Encode(proto); err != nil { // Encode appends a newline
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

// refreshMcpSources handles bodyless POST /v1/sessions/{id}/mcp-refresh.
func (h *HTTPHandler) refreshMcpSources(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "session_id is required")
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 2))
	if err != nil || len(body) != 0 {
		writeError(w, http.StatusBadRequest, "request body must be empty")
		return
	}
	result, err := h.svc.RefreshMcpSources(r.Context(), session.SessionID(id))
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, &mecatlv1.RefreshMcpSourcesResponse{Revision: result.Revision, Changed: result.Changed})
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

// decodeSteerControlJSON applies one strict, bounded request contract to both
// steer controls. Unknown fields and trailing JSON are rejected: silently
// dropping a misspelled expected_run_id would turn a safe strict control into
// an unqualified operation.
func decodeSteerControlJSON(w http.ResponseWriter, r *http.Request, dst any, allowEmpty bool) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxPromptBodyBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		if allowEmpty && errors.Is(err, io.EOF) {
			return true
		}
		writeSteerControlDecodeError(w, err)
		return false
	}
	var trailing any
	if err := dec.Decode(&trailing); !errors.Is(err, io.EOF) {
		writeSteerControlDecodeError(w, err)
		return false
	}
	return true
}

func writeSteerControlDecodeError(w http.ResponseWriter, err error) {
	var maxErr *http.MaxBytesError
	if errors.As(err, &maxErr) {
		writeError(w, http.StatusRequestEntityTooLarge, "request body too large")
		return
	}
	writeError(w, http.StatusBadRequest, "invalid JSON body")
}

type resolveRunAskBody struct {
	ExpectedRunID string `json:"expected_run_id"`
	AskID         string `json:"ask_id"`
	Verdict       string `json:"verdict"`
	ReviewID      string `json:"review_id,omitempty"`
	GuardrailKind string `json:"guardrail_kind,omitempty"`
}

type cancelRunBody struct {
	ExpectedRunID string `json:"expected_run_id"`
}

type steerRunBody struct {
	ExpectedRunID string              `json:"expected_run_id"`
	Text          string              `json:"text"`
	Parts         []promptContentBody `json:"parts"`
	MessageID     string              `json:"message_id"`
}

type cancelRunSteerBody struct {
	ExpectedRunID string `json:"expected_run_id"`
	MessageID     string `json:"message_id"`
}

type resolveRunAskResponse struct {
	RunID string `json:"run_id"`
	AskID string `json:"ask_id"`
}

type cancelRunResponse struct {
	RunID string `json:"run_id"`
}

type steerRunResponse struct {
	Outcome   string `json:"outcome"`
	RunID     string `json:"run_id"`
	MessageID string `json:"message_id"`
}

func strictRunAskVerdict(value string) (session.ApprovalVerdict, bool) {
	switch value {
	case "deny":
		return session.VerdictDeny, true
	case "allow_once":
		return session.VerdictAllowOnce, true
	case "allow_always":
		return session.VerdictAllowAlways, true
	default:
		return session.VerdictDeny, false
	}
}

func decodeStrictRunControlJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	return decodeSteerControlJSON(w, r, dst, false)
}

// resolveRunAsk is the strict acknowledgement-only HTTP mirror of ResolveRunAsk.
func (h *HTTPHandler) resolveRunAsk(w http.ResponseWriter, r *http.Request) {
	var body resolveRunAskBody
	if !decodeStrictRunControlJSON(w, r, &body) {
		return
	}
	verdict, ok := strictRunAskVerdict(body.Verdict)
	if body.ExpectedRunID == "" || body.AskID == "" || !ok {
		writeError(w, http.StatusBadRequest, "expected_run_id, ask_id, and a valid verdict are required")
		return
	}
	var ack RunAskAcknowledgement
	var err error
	if body.ReviewID != "" || body.GuardrailKind != "" {
		ack, err = h.svc.ResolveScopedRunAsk(r.Context(), session.SessionID(r.PathValue("id")), body.ExpectedRunID, agent.ApprovalResolution{
			AskID: body.AskID, ReviewID: body.ReviewID, Kind: session.GuardrailApprovalKind(body.GuardrailKind), Verdict: verdict,
		})
	} else {
		ack, err = h.svc.ResolveRunAsk(r.Context(), session.SessionID(r.PathValue("id")), body.ExpectedRunID, body.AskID, verdict)
	}
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resolveRunAskResponse(ack))
}

// resolvePlanAsk is the strict acknowledgement-only HTTP mirror of ResolvePlanAsk.
func (h *HTTPHandler) resolvePlanAsk(w http.ResponseWriter, r *http.Request) {
	var body resolveRunAskBody
	if !decodeStrictRunControlJSON(w, r, &body) {
		return
	}
	verdict, ok := strictRunAskVerdict(body.Verdict)
	if body.ExpectedRunID == "" || body.AskID == "" || !ok {
		writeError(w, http.StatusBadRequest, "expected_run_id, ask_id, and a valid verdict are required")
		return
	}
	ack, err := h.svc.ResolvePlanAsk(r.Context(), session.SessionID(r.PathValue("id")), body.ExpectedRunID, body.AskID, verdict)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resolveRunAskResponse(ack))
}

// cancelRun is the strict exact-run HTTP mirror of CancelRun.
func (h *HTTPHandler) cancelRun(w http.ResponseWriter, r *http.Request) {
	var body cancelRunBody
	if !decodeStrictRunControlJSON(w, r, &body) {
		return
	}
	if body.ExpectedRunID == "" {
		writeError(w, http.StatusBadRequest, "expected_run_id is required")
		return
	}
	ack, err := h.svc.CancelRun(r.Context(), session.SessionID(r.PathValue("id")), body.ExpectedRunID)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, cancelRunResponse(ack))
}

// steerRun is the strict exact-run HTTP mirror of SteerRun and never promotes.
func (h *HTTPHandler) steerRun(w http.ResponseWriter, r *http.Request) {
	var body steerRunBody
	if !decodeStrictRunControlJSON(w, r, &body) {
		return
	}
	if body.ExpectedRunID == "" || body.Text == "" && len(body.Parts) == 0 {
		writeError(w, http.StatusBadRequest, "expected_run_id and text or parts are required")
		return
	}
	parts, err := toContentParts(body.Parts)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := validateSteerMessageID(body.MessageID); err != nil {
		writeServiceError(w, err)
		return
	}
	ack, err := h.svc.SteerRun(r.Context(), session.SessionID(r.PathValue("id")), body.ExpectedRunID, body.Text, parts, body.MessageID)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, steerRunResponse{Outcome: string(ack.Outcome), RunID: ack.RunID, MessageID: ack.MessageID})
}

// cancelRunSteer is the strict exact-run HTTP mirror of CancelRunSteer.
func (h *HTTPHandler) cancelRunSteer(w http.ResponseWriter, r *http.Request) {
	var body cancelRunSteerBody
	if !decodeStrictRunControlJSON(w, r, &body) {
		return
	}
	if body.ExpectedRunID == "" {
		writeError(w, http.StatusBadRequest, "expected_run_id is required")
		return
	}
	if err := validateSteerMessageID(body.MessageID); err != nil {
		writeServiceError(w, err)
		return
	}
	ack, err := h.svc.CancelRunSteer(r.Context(), session.SessionID(r.PathValue("id")), body.ExpectedRunID, body.MessageID)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, steerRunResponse{Outcome: string(ack.Outcome), RunID: ack.RunID, MessageID: ack.MessageID})
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
	SessionID string             `json:"session_id"`
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
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "invalid JSON body: multiple JSON values")
		return
	}
	if !requireSessionAffinityValue(w, r, body.SessionID) {
		return
	}
	var specs []agent.MemberSpec
	if len(body.Members) > 0 {
		specs = make([]agent.MemberSpec, 0, len(body.Members))
		for _, m := range body.Members {
			specs = append(specs, m.toMemberSpec())
		}
	}
	id, enrolled, err := h.svc.CreateTeamForSession(r.Context(), session.SessionID(body.SessionID), body.Name, body.Goal, int(body.MaxTeamTokens), specs)
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
		if !isPublicEvent(te.Event) {
			return
		}
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

// getCompatibilityInfo handles GET /v1/compatibility.
//
// It reads the SAME Service.CompatibilityInfo projection the gRPC handler does,
// so the two transports cannot disagree about what this server permits — the
// transport parity the SDK's normalized surface depends on. See ADR 0248.
//
// GET /v1/info is the sibling route for build identity (ADR 0245); the two are
// deliberately separate resources rather than one overloaded document.
func (h *HTTPHandler) getCompatibilityInfo(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, h.svc.CompatibilityInfo(r.Context()))
}

func (h *HTTPHandler) listSessionMcpConnectors(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	result, err := h.svc.ListSessionMcpConnectors(r.Context(), session.SessionID(r.PathValue("id")))
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toProtoConnectorInventory(result))
}

// listMcpSources handles GET /v1/mcp/sources.
func (h *HTTPHandler) listMcpSources(w http.ResponseWriter, r *http.Request) {
	cached := h.svc.ListMcpSources(r.Context())
	out := make([]*mecatlv1.McpSource, 0, len(cached.Sources))
	for _, s := range cached.Sources {
		out = append(out, toProtoMcpSource(s))
	}
	writeJSON(w, http.StatusOK, &mecatlv1.ListMcpSourcesResponse{Sources: out, Revision: cached.Revision, Stale: cached.Stale, Reconciling: cached.Reconciling})
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

// listModels handles GET /v1/models from one captured model/status publication.
func (h *HTTPHandler) listModels(w http.ResponseWriter, r *http.Request) {
	view := h.svc.ListModelSnapshot(r.Context())
	writeJSON(w, http.StatusOK, &mecatlv1.ListModelsResponse{Models: view.Models, ProviderStatus: view.ProviderStatus})
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

func (h *HTTPHandler) listLearningAttempts(w http.ResponseWriter, r *http.Request) {
	limit, err := strconv.Atoi(r.URL.Query().Get("limit"))
	if err != nil && r.URL.Query().Get("limit") != "" {
		writeError(w, http.StatusBadRequest, "invalid attempt limit")
		return
	}
	response, err := h.svc.ListLearningAttempts(r.Context(), r.URL.Query().Get("state"), r.URL.Query().Get("cursor"), limit)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func (h *HTTPHandler) getLearningAttempt(w http.ResponseWriter, r *http.Request) {
	attempt, err := h.svc.GetLearningAttempt(r.Context(), r.PathValue("id"))
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, &mecatlv1.GetLearningAttemptResponse{Attempt: attempt})
}

func (h *HTTPHandler) retryLearningAttempt(w http.ResponseWriter, r *http.Request) {
	mutateLearningAttempt(w, r, h.svc.RetryLearningAttempt)
}

func (h *HTTPHandler) abandonLearningAttempt(w http.ResponseWriter, r *http.Request) {
	mutateLearningAttempt(w, r, h.svc.AbandonLearningAttempt)
}

func mutateLearningAttempt(w http.ResponseWriter, r *http.Request, mutate func(context.Context, string, string) (*mecatlv1.LearningAttempt, error)) {
	var body struct {
		ExpectedVersion string `json:"expected_version"`
	}
	if err := decodeLearningJSON(r, 2<<10, &body, false); err != nil {
		writeError(w, http.StatusBadRequest, "invalid attempt control")
		return
	}
	attempt, err := mutate(r.Context(), r.PathValue("id"), body.ExpectedVersion)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, &mecatlv1.MutateLearningAttemptResponse{Attempt: attempt})
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

// listCommands handles GET /v1/commands?session_id=.
func (h *HTTPHandler) listCommands(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("session_id")
	if !requireSessionAffinityValue(w, r, id) {
		return
	}
	if id == "" {
		writeServiceError(w, fmt.Errorf("%w: session_id is required", ErrInvalidArgument))
		return
	}
	cmds, err := h.svc.ListCommandsForSession(r.Context(), session.SessionID(id))
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, &mecatlv1.ListCommandsResponse{Commands: toProtoCommands(cmds)})
}

// listWorktrees handles GET /v1/worktrees?session_id=.
func (h *HTTPHandler) listWorktrees(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("session_id")
	if !requireSessionAffinityValue(w, r, id) {
		return
	}
	if id == "" {
		writeServiceError(w, fmt.Errorf("%w: session_id is required", ErrInvalidArgument))
		return
	}
	wts, err := h.svc.ListWorktreesForSession(r.Context(), session.SessionID(id))
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, &mecatlv1.ListWorktreesResponse{Worktrees: toProtoScopedWorktrees(wts)})
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
	// RequestManifest and NetworkAttempt remain debugger-only even on durable
	// read-back; neither has a public proto projection.
	enc := json.NewEncoder(w)
	for ev, iterErr := range events {
		if iterErr != nil {
			// Mid-stream fault: emit an SSE error frame and stop. The iter.Seq2
			// releases its file handle on early break per port.EventLog.Read's
			// contract.
			writeSSEError(w, flusher, map[string]string{"error": iterErr.Error()})
			return
		}
		if !isPublicEvent(ev) {
			continue
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

// watchSessionEvents handles GET /v1/sessions/{id}/watch — the DURABLE
// replay-then-follow watch as a Server-Sent Events stream (issue #821, ADR 0250).
//
// It is a NEW route, deliberately beside GET /v1/sessions/{id}/events rather than
// a widening of it. That route is a bounded replay that ends; this one replays,
// announces the boundary, and then stays open. Overloading one path with a query
// parameter would change an existing endpoint's termination behaviour for every
// client that already depends on the stream ending.
//
// Query parameters: `cursor` (opaque, empty = from the beginning) and `run_id`
// (optional filter). Both are handed to the same Service.WatchSessionEvents the
// gRPC handler uses, which is what makes the envelope sequences identical.
//
// Each frame is one WatchSessionEventsResponse — {event, cursor, phase} — so a
// client reads its resume position off the same frame that carried the event.
// This is the READ-BACK of the durable log, so ALL events are relayed including
// the three log-only kinds; do NOT copy the live-relay filter here.
func (h *HTTPHandler) watchSessionEvents(w http.ResponseWriter, r *http.Request) {
	id := session.SessionID(r.PathValue("id"))
	flusher, _ := w.(http.Flusher)
	if flusher == nil {
		writeError(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}
	// Resolved BEFORE any header is written, so a missing feature, a bad cursor, or
	// a session this caller may not read is a real status code and an RFC 9457
	// problem body rather than a 200 with an error frame inside it.
	envelopes, err := h.svc.WatchSessionEvents(r.Context(), id,
		port.Cursor(r.URL.Query().Get("cursor")), r.URL.Query().Get("run_id"))
	if err != nil {
		writeServiceError(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	enc := json.NewEncoder(w)
	for env, iterErr := range envelopes {
		if iterErr != nil {
			// Mid-stream fault, after the 200 is already committed: the status code
			// is spent, so the terminal condition has to ride the stream. It carries
			// the stable machine code so a client can tell a resumable lag from a
			// delivery gap without parsing prose. Breaking out releases the watch.
			entry := classifyError(iterErr)
			writeSSEError(w, flusher, map[string]string{"code": entry.Code, "error": iterErr.Error()})
			return
		}
		if _, err := w.Write([]byte("data: ")); err != nil {
			return // client disconnected; breaking out releases the watch
		}
		if err := enc.Encode(toProtoWatchEnvelope(env)); err != nil { // Encode appends a newline
			return
		}
		if _, err := w.Write([]byte("\n")); err != nil {
			return
		}
		flusher.Flush()
	}
}

// --- helpers ----------------------------------------------------------------

// writeSSEError writes a TERMINAL error frame as valid Server-Sent Events.
//
// The payload has to ride a `data: ` line. Per the EventSource grammar a line is
// split into `field: value` at the first colon, so a bare `{"code":"..."}` parses
// as the unrecognised field `{"code"` and is DISCARDED — a conforming client sees
// the stream go quiet and cannot tell a resumable fault from a delivery gap from
// a clean end. That silence is precisely the failure the durable watch exists to
// abolish (see ErrWatchLagging), so the framing here is load-bearing rather than
// cosmetic.
//
// The frame is tagged `event: error` so a client can ROUTE it — an SSE consumer
// otherwise has to shape-sniff the JSON against the success payload it is not,
// and on the watch route the success payload is a WatchSessionEventsResponse
// whose fields are all optional, so sniffing is unreliable by construction.
//
// Callers own the payload shape: the watch route carries the stable machine
// `code`, the older replay route carries `error` alone. Both now ARRIVE, which is
// the fix; unifying their bodies would change a shape clients may already read.
func writeSSEError(w http.ResponseWriter, flusher http.Flusher, payload any) {
	if _, err := w.Write([]byte("event: error\ndata: ")); err != nil {
		return
	}
	if err := json.NewEncoder(w).Encode(payload); err != nil { // Encode appends a newline
		return
	}
	if _, err := w.Write([]byte("\n")); err != nil {
		return
	}
	flusher.Flush()
}

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
	writeJSONBody(w, v)
}

// writeJSONBody encodes v to w. It is split out of writeJSON so the RFC 9457
// path (which sets its own Content-Type and status) shares the one encoder.
func writeJSONBody(w http.ResponseWriter, v any) {
	_ = json.NewEncoder(w).Encode(v)
}

// writeError writes an RFC 9457 problem body for a caller that already knows the
// HTTP status but has no service sentinel — request-shape failures raised inside
// a handler (malformed JSON, a missing required field, an oversized body).
//
// It derives the stable code from the status rather than inventing one per call
// site, so the ~40 existing callers keep working unchanged while every response
// still carries a machine-readable code.
func writeError(w http.ResponseWriter, code int, msg string) {
	writeProblem(w, entryForHTTPStatus(code), msg)
}

// writeServiceError writes a service sentinel error as an RFC 9457 problem.
//
// It is a REGISTRY LOOKUP, not a switch. It used to be a 49-case
// errors.Is chain maintained in parallel with toStatus in grpc.go; the two
// agreed only by discipline, and a sentinel added to one and forgotten in the
// other would have reported a different class per transport. Both now read
// errorRegistry, so they cannot disagree. See ADR 0248.
func writeServiceError(w http.ResponseWriter, err error) {
	writeProblem(w, classifyError(err), err.Error())
}
