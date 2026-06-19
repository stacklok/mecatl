package server

import (
	"context"
	"errors"
	"io"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/session"
)

// HarnessServer implements the generated mecatlv1.HarnessServiceServer over the
// shared Service. It is the primary (gRPC) surface; the HTTP/SSE adapter wraps
// the same Service.
type HarnessServer struct {
	mecatlv1.UnimplementedHarnessServiceServer
	svc *Service
}

// NewHarnessServer constructs a HarnessServer over svc.
func NewHarnessServer(svc *Service) *HarnessServer {
	return &HarnessServer{svc: svc}
}

// compile-time assertion that HarnessServer satisfies the generated interface.
var _ mecatlv1.HarnessServiceServer = (*HarnessServer)(nil)

// CreateSession allocates a new session and returns its id.
func (h *HarnessServer) CreateSession(ctx context.Context, req *mecatlv1.CreateSessionRequest) (*mecatlv1.CreateSessionResponse, error) {
	// Session profile (issue #55): "" = default (full filesystem), "no-fs" = the
	// no-filesystem profile; anything else is a loud InvalidArgument. The
	// workspace requirement is PROFILE-AWARE and enforced in the service
	// (createSession): default requires a workspace, no-fs requires an EMPTY one
	// — so there is deliberately NO unconditional empty-workspace guard here.
	profile, err := ParseSessionProfile(req.GetProfile())
	if err != nil {
		return nil, toStatus(err)
	}
	// Per-session provider/model selector (multi-provider Phase 0, S3): the two
	// fields map to the neutral ProviderSelector; the zero selector keeps the
	// shared-engine fast path. An unknown/unavailable provider, or model_id without
	// provider_id, surfaces as InvalidArgument via toStatus.
	sel := ProviderSelector{ProviderID: req.GetProviderId(), ModelID: req.GetModelId()}
	sess, err := h.svc.CreateSessionWithProfile(ctx, req.GetWorkspace(), modeFromProto(req.GetMode()), limitsFromProto(req.GetLimits()), sel, profile)
	if err != nil {
		return nil, toStatus(err)
	}
	// session_capabilities echoes the per-session resolved input capability (catalog
	// ∩ adapter for THIS session's provider+model), which may differ from the
	// server-wide capabilities when a non-default selector was supplied. Both read
	// the composition's single source, so they cannot disagree.
	scaps := h.svc.SessionCapabilities(sess.ID)
	// resolved_model echoes the EFFECTIVE provider+model THIS session resolved to,
	// read from the composition single source (Service.ResolvedModel) — NEVER from
	// req.GetModelId(), which is empty for a default session and ambiguous for a
	// passthrough id. Same single-source discipline as session_capabilities.
	return &mecatlv1.CreateSessionResponse{
		SessionId:    string(sess.ID),
		Capabilities: h.svc.capabilities(),
		SessionCapabilities: &mecatlv1.SessionCapabilities{
			Image: scaps.Image,
			Audio: scaps.Audio,
		},
		ResolvedModel: resolvedModelToProto(h.svc.ResolvedModel(sess.ID)),
	}, nil
}

// GetSession returns a snapshot of the requested session.
func (h *HarnessServer) GetSession(ctx context.Context, req *mecatlv1.GetSessionRequest) (*mecatlv1.GetSessionResponse, error) {
	if req.GetSessionId() == "" {
		return nil, status.Error(codes.InvalidArgument, "session_id is required")
	}
	sess, err := h.svc.GetSession(ctx, session.SessionID(req.GetSessionId()))
	if err != nil {
		return nil, toStatus(err)
	}
	return &mecatlv1.GetSessionResponse{Session: toProtoSession(sess, h.svc.ResolvedModel(sess.ID))}, nil
}

// SetMode changes the permission posture of the requested session.
func (h *HarnessServer) SetMode(ctx context.Context, req *mecatlv1.SetModeRequest) (*mecatlv1.SetModeResponse, error) {
	if req.GetSessionId() == "" {
		return nil, status.Error(codes.InvalidArgument, "session_id is required")
	}
	sess, err := h.svc.SetMode(ctx, session.SessionID(req.GetSessionId()), modeFromProto(req.GetMode()))
	if err != nil {
		return nil, toStatus(err)
	}
	return &mecatlv1.SetModeResponse{Session: toProtoSession(sess, h.svc.ResolvedModel(sess.ID))}, nil
}

// CloseSession ends a session and releases its server-side resources. It returns
// NotFound only for a never-created id; an already-released session succeeds
// (idempotent). It calls Service.EndSession, NOT the void Service.CloseSession, so
// an unknown id surfaces as NotFound rather than a silent success.
func (h *HarnessServer) CloseSession(ctx context.Context, req *mecatlv1.CloseSessionRequest) (*mecatlv1.CloseSessionResponse, error) {
	if req.GetSessionId() == "" {
		return nil, status.Error(codes.InvalidArgument, "session_id is required")
	}
	if err := h.svc.EndSession(ctx, session.SessionID(req.GetSessionId())); err != nil {
		return nil, toStatus(err)
	}
	return &mecatlv1.CloseSessionResponse{}, nil
}

// Converse drives one run over a bidi stream. The first frame MUST be a Prompt;
// the server then relays the run's Events while concurrently reading
// ResumeApproval / Cancel control frames, until the events channel closes (the
// terminal result was delivered) or the stream context is cancelled.
func (h *HarnessServer) Converse(stream mecatlv1.HarnessService_ConverseServer) error {
	ctx := stream.Context()

	first, err := stream.Recv()
	if err != nil {
		if errors.Is(err, io.EOF) {
			return status.Error(codes.InvalidArgument, "converse: stream closed before a prompt frame")
		}
		return err
	}
	prompt := first.GetPrompt()
	if prompt == nil {
		return status.Error(codes.InvalidArgument, "converse: first frame must be a prompt")
	}
	if prompt.GetSessionId() == "" {
		return status.Error(codes.InvalidArgument, "converse: prompt session_id is required")
	}
	if prompt.GetText() == "" && len(prompt.GetParts()) == 0 {
		return status.Error(codes.InvalidArgument, "converse: prompt text or parts is required")
	}
	parts, perr := contentFromProto(prompt.GetParts())
	if perr != nil {
		return status.Error(codes.InvalidArgument, perr.Error())
	}

	id := session.SessionID(prompt.GetSessionId())
	run, err := h.svc.StartRunContent(ctx, id, prompt.GetText(), parts)
	if err != nil {
		return toStatus(err)
	}
	defer h.svc.deregister(id, run)

	// Read subsequent control frames concurrently so an approval/cancel can be
	// delivered while events are still streaming. The reader exits on stream
	// EOF (client closed its send half) or context cancellation.
	go h.readControl(ctx, stream, run)

	// Relay events on this goroutine; the channel closes when the run ends. On
	// the FIRST Send error (the client is gone) the relay cancels the run but
	// KEEPS RANGING, discarding events until the channel closes: a run that keeps
	// emitting (a busy team / fan-out winding down) must never wedge in its own
	// sends behind a dead relay. The error is sticky — no further Send happens
	// after it — and is returned once the run has fully drained.
	//
	// The durable event-log Append (cloud-native Phase 3a) is DECOUPLED from the
	// client send: it runs for EVERY observed event, BEFORE and independent of the
	// drain-to-discard guard, so a disconnected client never stops the log (the
	// whole point of a server-side durable log is to survive the client — it must
	// record the post-disconnect tail, including the terminal EvResult). It uses a
	// cancel-detached context so a cancelled stream ctx (client gone) cannot abort
	// the durable write. This is DISTINCT from the EvPermissionAsk Persist below,
	// which is snapshot semantics gated to the healthy path: the log is append-only
	// history and must record what happened regardless of client liveness.
	logCtx := context.WithoutCancel(ctx)
	var sendErr error
	for ev := range run.Events() {
		h.svc.appendEvent(logCtx, id, ev)
		if sendErr != nil {
			continue // drain-to-discard: keep the run unwedged after a dead client
		}
		// EvApproval (3a), EvCompactionArchive (3b), and EvUserPrompt (ADR 0038) are
		// consumed by the durable log ONLY — appended above but NOT relayed to the client
		// wire (the verdict record, the pre-compaction archive, and the user-prompt record
		// are log/audit history, not client events; the client already holds its own
		// prompt). Skip the client send AFTER the Append.
		if ev.Type == session.EvApproval || ev.Type == session.EvCompactionArchive || ev.Type == session.EvUserPrompt {
			continue
		}
		// Persist when the run pauses awaiting approval so a restart leaves a
		// loadable awaiting session a client can re-attach to.
		if ev.Type == session.EvPermissionAsk {
			h.svc.Persist(ctx, id)
		}
		if err := stream.Send(&mecatlv1.ConverseResponse{Event: toProto(ev)}); err != nil {
			sendErr = err
			run.Cancel()
		}
	}
	return sendErr
}

// readControl reads ResumeApproval / Cancel / CancelChild frames until the
// client closes its send half or the context is cancelled, dispatching each
// onto run.
func (*HarnessServer) readControl(ctx context.Context, stream mecatlv1.HarnessService_ConverseServer, run *agent.Run) {
	for {
		if ctx.Err() != nil {
			return
		}
		frame, err := stream.Recv()
		if err != nil {
			// EOF means "no more inputs"; any other error means the stream is
			// gone. Either way stop reading; the relay loop owns termination.
			return
		}
		switch k := frame.GetKind().(type) {
		case *mecatlv1.ConverseRequest_ResumeApproval:
			if k.ResumeApproval != nil {
				ra := k.ResumeApproval
				run.Approve(ra.GetAskId(), verdictFromResumeApproval(ra.GetVerdict(), ra.GetAllow()))
			}
		case *mecatlv1.ConverseRequest_Cancel:
			run.Cancel()
		case *mecatlv1.ConverseRequest_CancelChild:
			if k.CancelChild != nil {
				// Per-child cancel, addressed by the child session id. A false return
				// (unknown / already-done child) is ignored by design on the stream: the
				// finished-as-you-pressed race is benign and the observable outcome is the
				// child's terminal event (clients disable the key for done lanes).
				_ = run.CancelChild(k.CancelChild.GetChildId())
			}
		default:
			// A second Prompt or an unknown frame is ignored: the run is
			// already driving and a new prompt cannot start a second run here.
		}
	}
}

// --- MCP inspection RPCs -----------------------------------------------------

// ListMcpResources returns the resource snapshots for the requested server
// (empty server = all). Nil provider yields an empty list.
func (h *HarnessServer) ListMcpResources(ctx context.Context, req *mecatlv1.ListMcpResourcesRequest) (*mecatlv1.ListMcpResourcesResponse, error) {
	res, err := h.svc.ListMcpResources(ctx, req.GetServer())
	if err != nil {
		return nil, toStatus(err)
	}
	return &mecatlv1.ListMcpResourcesResponse{Resources: toProtoMcpResources(res)}, nil
}

// ReadMcpResource reads a single resource by URI from the named server.
func (h *HarnessServer) ReadMcpResource(ctx context.Context, req *mecatlv1.ReadMcpResourceRequest) (*mecatlv1.ReadMcpResourceResponse, error) {
	if req.GetServer() == "" || req.GetUri() == "" {
		return nil, status.Error(codes.InvalidArgument, "server and uri are required")
	}
	c, err := h.svc.ReadMcpResource(ctx, req.GetServer(), req.GetUri())
	if err != nil {
		return nil, toStatus(err)
	}
	return &mecatlv1.ReadMcpResourceResponse{
		Contents: []*mecatlv1.McpResourceContents{toProtoMcpResourceContents(c)},
	}, nil
}

// ListMcpPrompts returns the prompt snapshots for the requested server
// (empty server = all). Nil provider yields an empty list.
func (h *HarnessServer) ListMcpPrompts(ctx context.Context, req *mecatlv1.ListMcpPromptsRequest) (*mecatlv1.ListMcpPromptsResponse, error) {
	ps, err := h.svc.ListMcpPrompts(ctx, req.GetServer())
	if err != nil {
		return nil, toStatus(err)
	}
	return &mecatlv1.ListMcpPromptsResponse{Prompts: toProtoMcpPrompts(ps)}, nil
}

// GetMcpPrompt expands a named prompt with arguments on the named server.
func (h *HarnessServer) GetMcpPrompt(ctx context.Context, req *mecatlv1.GetMcpPromptRequest) (*mecatlv1.GetMcpPromptResponse, error) {
	if req.GetServer() == "" || req.GetName() == "" {
		return nil, status.Error(codes.InvalidArgument, "server and name are required")
	}
	res, err := h.svc.GetMcpPrompt(ctx, req.GetServer(), req.GetName(), req.GetArguments())
	if err != nil {
		return nil, toStatus(err)
	}
	msgs := make([]*mecatlv1.McpPromptMessage, 0, len(res.Messages))
	for _, m := range res.Messages {
		msgs = append(msgs, toProtoMcpPromptMessage(m))
	}
	return &mecatlv1.GetMcpPromptResponse{Description: res.Description, Messages: msgs}, nil
}

// ListMcpSources returns the resolved MCP source inventory snapshot.
func (h *HarnessServer) ListMcpSources(ctx context.Context, _ *mecatlv1.ListMcpSourcesRequest) (*mecatlv1.ListMcpSourcesResponse, error) {
	infos := h.svc.ListMcpSources(ctx)
	out := make([]*mecatlv1.McpSource, 0, len(infos))
	for _, s := range infos {
		out = append(out, toProtoMcpSource(s))
	}
	return &mecatlv1.ListMcpSourcesResponse{Sources: out}, nil
}

// ListToolHiveGroups returns the distinct, non-empty ToolHive groups derived
// from the inventory snapshot.
func (h *HarnessServer) ListToolHiveGroups(ctx context.Context, _ *mecatlv1.ListToolHiveGroupsRequest) (*mecatlv1.ListToolHiveGroupsResponse, error) {
	return &mecatlv1.ListToolHiveGroupsResponse{Groups: h.svc.ListToolHiveGroups(ctx)}, nil
}

// ListAgents returns the resolved agent-definition inventory snapshot.
func (h *HarnessServer) ListAgents(ctx context.Context, _ *mecatlv1.ListAgentsRequest) (*mecatlv1.ListAgentsResponse, error) {
	return &mecatlv1.ListAgentsResponse{Agents: h.svc.ListAgents(ctx)}, nil
}

// ListSkills returns the resolved skills-inventory snapshot.
func (h *HarnessServer) ListSkills(ctx context.Context, _ *mecatlv1.ListSkillsRequest) (*mecatlv1.ListSkillsResponse, error) {
	return &mecatlv1.ListSkillsResponse{Skills: h.svc.ListSkills(ctx)}, nil
}

// ListModels returns the resolved selectable-model inventory snapshot.
func (h *HarnessServer) ListModels(ctx context.Context, _ *mecatlv1.ListModelsRequest) (*mecatlv1.ListModelsResponse, error) {
	return &mecatlv1.ListModelsResponse{Models: h.svc.ListModels(ctx)}, nil
}

// GetSoul returns the resolved soul (persona) snapshot.
func (h *HarnessServer) GetSoul(ctx context.Context, _ *mecatlv1.GetSoulRequest) (*mecatlv1.GetSoulResponse, error) {
	return &mecatlv1.GetSoulResponse{Soul: h.svc.GetSoul(ctx)}, nil
}

// GetUserModel returns the current user-model index snapshot.
func (h *HarnessServer) GetUserModel(ctx context.Context, _ *mecatlv1.GetUserModelRequest) (*mecatlv1.GetUserModelResponse, error) {
	resp, err := h.svc.GetUserModel(ctx)
	if err != nil {
		return nil, toStatus(err)
	}
	return resp, nil
}

// ListCommands returns the available slash commands for the requested workspace.
func (h *HarnessServer) ListCommands(ctx context.Context, req *mecatlv1.ListCommandsRequest) (*mecatlv1.ListCommandsResponse, error) {
	cmds, err := h.svc.ListCommands(ctx, req.GetWorkspace())
	if err != nil {
		return nil, toStatus(err)
	}
	return &mecatlv1.ListCommandsResponse{Commands: toProtoCommands(cmds)}, nil
}

// ListWorktrees returns the git worktrees of the repo rooted at the requested
// workspace (issue #102).
func (h *HarnessServer) ListWorktrees(ctx context.Context, req *mecatlv1.ListWorktreesRequest) (*mecatlv1.ListWorktreesResponse, error) {
	wts, err := h.svc.ListWorktrees(ctx, req.GetWorkspace())
	if err != nil {
		return nil, toStatus(err)
	}
	return &mecatlv1.ListWorktreesResponse{Worktrees: toProtoWorktrees(wts)}, nil
}

// toStatus maps service sentinel errors to gRPC status codes.
func toStatus(err error) error {
	switch {
	case errors.Is(err, ErrInvalidArgument):
		return status.Error(codes.InvalidArgument, err.Error())
	case errors.Is(err, ErrNotFound):
		return status.Error(codes.NotFound, err.Error())
	case errors.Is(err, ErrTeamNotFound):
		return status.Error(codes.NotFound, err.Error())
	case errors.Is(err, ErrChildNotFound):
		return status.Error(codes.NotFound, err.Error())
	case errors.Is(err, ErrFailedPrecondition):
		return status.Error(codes.FailedPrecondition, err.Error())
	case errors.Is(err, ErrNoActiveRun):
		return status.Error(codes.FailedPrecondition, err.Error())
	case errors.Is(err, ErrSessionLeasedElsewhere):
		// Cloud-native Phase 4: another replica holds the session's single-writer
		// lease. Well-formed request, transiently owned elsewhere — FailedPrecondition
		// (consistent with ErrNoActiveRun; HTTP maps it to 409 Conflict).
		return status.Error(codes.FailedPrecondition, err.Error())
	case errors.Is(err, ErrNoMCPProvider):
		return status.Error(codes.FailedPrecondition, err.Error())
	case errors.Is(err, ErrTeamsDisabled):
		return status.Error(codes.FailedPrecondition, err.Error())
	case errors.Is(err, ErrTeamRunning):
		return status.Error(codes.FailedPrecondition, err.Error())
	case errors.Is(err, ErrTeamNotRunning):
		return status.Error(codes.FailedPrecondition, err.Error())
	case errors.Is(err, ErrTooManyTeams):
		return status.Error(codes.ResourceExhausted, err.Error())
	case errors.Is(err, ErrTooManySessionEngines):
		return status.Error(codes.ResourceExhausted, err.Error())
	case errors.Is(err, ErrInternal):
		return status.Error(codes.Internal, err.Error())
	default:
		return status.Error(codes.Internal, err.Error())
	}
}
