package server

//revive:disable:exported // RPC methods mirror the generated HarnessService interface

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
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
	sel := ProviderSelector{ProviderID: req.GetProviderId(), ModelID: req.GetModelId(), ReasoningEffort: req.GetReasoningEffort()}
	var opts []CreateSessionOption
	if src := req.GetSourceSessionId(); src != "" {
		opts = append(opts, WithSourceSession(session.SessionID(src)))
	}
	sess, err := h.svc.CreateSessionWithProfile(ctx, req.GetWorkspace(), modeFromProto(req.GetMode()), limitsFromProto(req.GetLimits()), sel, profile, opts...)
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
	proto := toProtoSession(sess, h.svc.ResolvedModel(sess.ID), h.svc.capabilities())
	// Lazy display-time fallback: a session whose snapshot Title was never seeded
	// (or is empty) gets a derived label so GetSession shows one without a
	// write-on-read — sess.Title is NOT mutated.
	if sess.Title == "" {
		proto.Title = DeriveTitle(sess)
	}
	return &mecatlv1.GetSessionResponse{Session: proto}, nil
}

// GetSessionTranscript returns the owned session's snapshot-derived transcript.
func (h *HarnessServer) GetSessionTranscript(ctx context.Context, req *mecatlv1.GetSessionTranscriptRequest) (*mecatlv1.GetSessionTranscriptResponse, error) {
	if req.GetSessionId() == "" {
		return nil, status.Error(codes.InvalidArgument, "session_id is required")
	}
	transcript, err := h.svc.GetTranscript(ctx, session.SessionID(req.GetSessionId()))
	if err != nil {
		return nil, toStatus(err)
	}
	return toProtoTranscript(transcript), nil
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
	return &mecatlv1.SetModeResponse{Session: toProtoSession(sess, h.svc.ResolvedModel(sess.ID), h.svc.capabilities())}, nil
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

// RenameSession explicitly replaces an idle main session's persisted title.
func (h *HarnessServer) RenameSession(ctx context.Context, req *mecatlv1.RenameSessionRequest) (*mecatlv1.RenameSessionResponse, error) {
	if req.GetSessionId() == "" || strings.TrimSpace(req.GetTitle()) == "" {
		return nil, status.Error(codes.InvalidArgument, "session_id and non-blank title are required")
	}
	sess, err := h.svc.RenameSession(ctx, session.SessionID(req.GetSessionId()), req.GetTitle())
	if err != nil {
		return nil, toStatus(err)
	}
	return &mecatlv1.RenameSessionResponse{Session: toProtoSession(sess, h.svc.ResolvedModel(sess.ID), h.svc.capabilities())}, nil
}

// DeleteSession physically removes an idle main session and store-managed sidecars.
func (h *HarnessServer) DeleteSession(ctx context.Context, req *mecatlv1.DeleteSessionRequest) (*mecatlv1.DeleteSessionResponse, error) {
	if req.GetSessionId() == "" {
		return nil, status.Error(codes.InvalidArgument, "session_id is required")
	}
	if err := h.svc.DeleteSession(ctx, session.SessionID(req.GetSessionId())); err != nil {
		return nil, toStatus(err)
	}
	return &mecatlv1.DeleteSessionResponse{}, nil
}

// ForkSession creates a peer session from an existing session's history snapshot
// (ADR 0065). The new session inherits the source's mode, workspace, limits, and
// provider/model/profile labels; same provider and model only, with the ONE
// optional selector delta being a reasoning-effort override (ADR 0068, empty
// inherits). The source must be at a turn boundary (idle/terminal); a
// running/awaiting source is rejected with FailedPrecondition. No streaming — the
// fork is synchronous.
func (h *HarnessServer) ForkSession(ctx context.Context, req *mecatlv1.ForkSessionRequest) (*mecatlv1.ForkSessionResponse, error) {
	if req.GetSourceSessionId() == "" {
		return nil, status.Error(codes.InvalidArgument, "source_session_id is required")
	}
	id, err := h.svc.ForkSession(ctx, session.SessionID(req.GetSourceSessionId()), req.GetTitle(), req.GetReasoningEffort())
	if err != nil {
		return nil, toStatus(err)
	}
	return &mecatlv1.ForkSessionResponse{SessionId: string(id)}, nil
}

func adoptionBindingsFromProto(binding *mecatlv1.AdoptionBindings) (AdoptionBindings, error) {
	if binding == nil {
		return AdoptionBindings{}, fmt.Errorf("%w: bindings are required", ErrInvalidArgument)
	}
	profile, err := ParseSessionProfile(binding.GetProfile())
	if err != nil {
		return AdoptionBindings{}, err
	}
	return AdoptionBindings{
		Workspace:      binding.GetWorkspace(),
		EnvironmentRef: session.EnvironmentRef{Kind: session.EnvironmentKind(binding.GetEnvironmentKind()), ID: binding.GetEnvironmentId()},
		ProviderID:     binding.GetProviderId(), ModelID: binding.GetModelId(), Profile: profile,
	}, nil
}

func adoptionBindingsToProto(binding AdoptionBindings) *mecatlv1.AdoptionBindings {
	return &mecatlv1.AdoptionBindings{Workspace: binding.Workspace, EnvironmentKind: string(binding.EnvironmentRef.Kind), EnvironmentId: binding.EnvironmentRef.ID, ProviderId: binding.ProviderID, ModelId: binding.ModelID, Profile: string(binding.Profile)}
}

func (h *HarnessServer) PreflightSessionAdoption(ctx context.Context, req *mecatlv1.PreflightSessionAdoptionRequest) (*mecatlv1.PreflightSessionAdoptionResponse, error) {
	if req.GetSourceSessionId() == "" {
		return nil, status.Error(codes.InvalidArgument, "source_session_id is required")
	}
	bindings, err := adoptionBindingsFromProto(req.GetBindings())
	if err != nil {
		return nil, toStatus(err)
	}
	result, err := h.svc.PreflightSessionAdoption(ctx, session.SessionID(req.GetSourceSessionId()), bindings)
	if err != nil {
		return nil, toStatus(err)
	}
	return &mecatlv1.PreflightSessionAdoptionResponse{Eligible: result.Eligible, ReasonCode: string(result.Reason), Bindings: adoptionBindingsToProto(result.Bindings)}, nil
}

func (h *HarnessServer) AdoptSession(ctx context.Context, req *mecatlv1.AdoptSessionRequest) (*mecatlv1.AdoptSessionResponse, error) {
	if req.GetSourceSessionId() == "" || req.GetIdempotencyKey() == "" {
		return nil, status.Error(codes.InvalidArgument, "source_session_id and idempotency_key are required")
	}
	bindings, err := adoptionBindingsFromProto(req.GetBindings())
	if err != nil {
		return nil, toStatus(err)
	}
	sess, err := h.svc.AdoptSession(ctx, session.SessionID(req.GetSourceSessionId()), req.GetIdempotencyKey(), bindings)
	if err != nil {
		return nil, toStatus(err)
	}
	caps := h.svc.SessionCapabilities(sess.ID)
	return &mecatlv1.AdoptSessionResponse{SessionId: string(sess.ID), SourceSessionId: string(adoptionSourceID(sess)), Capabilities: h.svc.capabilities(), SessionCapabilities: &mecatlv1.SessionCapabilities{Image: caps.Image, Audio: caps.Audio}, ResolvedModel: resolvedModelToProto(h.svc.ResolvedModel(sess.ID))}, nil
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

	logCtx := context.WithoutCancel(ctx)

	// Inject a pre-flight EvRecoverNotice when the session just recovered from a
	// PERMANENT failure — surface the advisory BEFORE the main event loop burns a
	// provider call on the same unrecoverable error. Emitted ONCE per recovery
	// (RecoverNotice consumes the entry on the first call).
	if notice := h.svc.RecoverNotice(id); notice != "" {
		ev := session.Event{Type: session.EvRecoverNotice, Text: notice}
		// Durable log first (cancel-detached, survives client disconnect).
		h.svc.appendEvent(logCtx, id, ev)
		// Forward to the client wire.
		if err := stream.Send(&mecatlv1.ConverseResponse{Event: toProto(ev)}); err != nil {
			return err
		}
	}

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
	var sendErr error
	for ev := range run.Events() {
		if sendErr != nil {
			// drain-to-discard: the client is gone. Still append to the durable
			// log (it must record the post-disconnect tail), but skip Persist /
			// auto-approve / the client send.
			h.svc.appendEvent(logCtx, id, ev)
			continue
		}
		if !h.svc.relayEvent(ctx, logCtx, id, ev, true) {
			continue // log-only event: consumed by the durable log, not relayed to the client wire
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

// ListModels returns the resolved selectable-model inventory snapshot plus
// (issue #262) the per-provider live-listing status. ListModels itself
// triggers the on-demand refresh (when installed), so ProviderStatuses is read
// AFTER it to reflect the just-completed refresh.
func (h *HarnessServer) ListModels(ctx context.Context, _ *mecatlv1.ListModelsRequest) (*mecatlv1.ListModelsResponse, error) {
	models := h.svc.ListModels(ctx)
	return &mecatlv1.ListModelsResponse{Models: models, ProviderStatus: h.svc.ProviderStatuses()}, nil
}

// GetSoul returns the resolved soul (persona) snapshot.
func (h *HarnessServer) GetSoul(ctx context.Context, _ *mecatlv1.GetSoulRequest) (*mecatlv1.GetSoulResponse, error) {
	return &mecatlv1.GetSoulResponse{Soul: h.svc.GetSoul(ctx)}, nil
}

// GetUserModel returns the current user-model index snapshot.
func (h *HarnessServer) GetUserModel(ctx context.Context, req *mecatlv1.GetUserModelRequest) (*mecatlv1.GetUserModelResponse, error) {
	resp, err := h.svc.GetUserModel(ctx)
	if err != nil {
		return nil, toStatus(err)
	}
	detail, err := h.svc.GetUserModelDetail(ctx, req.GetKey())
	if err != nil {
		return nil, toStatus(err)
	}
	resp.Detail = detail
	return resp, nil
}

// ReflectSession submits one caller-owned completed session to the bounded coordinator.
func (h *HarnessServer) ReflectSession(ctx context.Context, req *mecatlv1.ReflectSessionRequest) (*mecatlv1.ReflectSessionResponse, error) {
	receipt, err := h.svc.ReflectSession(ctx, session.SessionID(req.GetSessionId()))
	if err != nil {
		return nil, toStatus(err)
	}
	return &mecatlv1.ReflectSessionResponse{Receipt: receipt}, nil
}

// GenerateDreamPlan creates a retained manual consolidation review.
func (h *HarnessServer) GenerateDreamPlan(ctx context.Context, req *mecatlv1.GenerateDreamPlanRequest) (*mecatlv1.GenerateDreamPlanResponse, error) {
	review, err := h.svc.GenerateDream(ctx, DreamTarget(req.GetTarget()))
	if err != nil {
		return nil, toStatus(normalizeDreamError(err))
	}
	return &mecatlv1.GenerateDreamPlanResponse{Plan: toProtoDreamReview(review)}, nil
}

// DecideDreamPlan applies or dismisses the exact retained review plan.
func (h *HarnessServer) DecideDreamPlan(ctx context.Context, req *mecatlv1.DecideDreamPlanRequest) (*mecatlv1.DecideDreamPlanResponse, error) {
	receipt, err := h.svc.DecideDream(ctx, req.GetPlanId(), DreamDecision(req.GetDecision()))
	if err != nil && (!errors.Is(err, ErrDreamApplyFailed) || receipt.ID == "") {
		return nil, toStatus(normalizeDreamError(err))
	}
	return &mecatlv1.DecideDreamPlanResponse{Receipt: toProtoDreamReceipt(receipt)}, nil
}

// ListLearningProposals returns one bounded proposal page for the caller partition.
func (h *HarnessServer) ListLearningProposals(ctx context.Context, req *mecatlv1.ListLearningProposalsRequest) (*mecatlv1.ListLearningProposalsResponse, error) {
	resp, err := h.svc.ListLearningProposals(ctx, req.GetStatus(), req.GetCursor(), int(req.GetLimit()), req.GetProject())
	if err != nil {
		return nil, toStatus(err)
	}
	return resp, nil
}

// GetLearningProposal returns bounded detail for one caller-owned proposal.
func (h *HarnessServer) GetLearningProposal(ctx context.Context, req *mecatlv1.GetLearningProposalRequest) (*mecatlv1.GetLearningProposalResponse, error) {
	proposal, err := h.svc.GetLearningProposal(ctx, req.GetId(), req.GetProject())
	if err != nil {
		return nil, toStatus(err)
	}
	return &mecatlv1.GetLearningProposalResponse{Proposal: proposal}, nil
}

// DecideLearningProposal applies a version-checked approve or reject decision.
func (h *HarnessServer) DecideLearningProposal(ctx context.Context, req *mecatlv1.DecideLearningProposalRequest) (*mecatlv1.DecideLearningProposalResponse, error) {
	proposal, err := h.svc.DecideLearningProposal(ctx, req.GetId(), req.GetExpectedVersion(), req.GetDecision(), req.GetReason(), req.GetProject())
	if err != nil {
		return nil, toStatus(err)
	}
	return &mecatlv1.DecideLearningProposalResponse{Proposal: proposal}, nil
}

// UndoLearningPromotion applies a version-checked compensating promotion undo.
func (h *HarnessServer) UndoLearningPromotion(ctx context.Context, req *mecatlv1.UndoLearningPromotionRequest) (*mecatlv1.UndoLearningPromotionResponse, error) {
	proposal, err := h.svc.UndoLearningPromotion(ctx, req.GetId(), req.GetExpectedVersion(), req.GetProject())
	if err != nil {
		return nil, toStatus(err)
	}
	return &mecatlv1.UndoLearningPromotionResponse{Proposal: proposal}, nil
}

func (h *HarnessServer) ListLearnedSkills(ctx context.Context, req *mecatlv1.ListLearnedSkillsRequest) (*mecatlv1.ListLearnedSkillsResponse, error) {
	resp, err := h.svc.ListLearnedSkills(ctx, req)
	if err != nil {
		return nil, toStatus(err)
	}
	return resp, nil
}
func (h *HarnessServer) GetLearnedSkill(ctx context.Context, req *mecatlv1.GetLearnedSkillRequest) (*mecatlv1.GetLearnedSkillResponse, error) {
	resp, err := h.svc.GetLearnedSkill(ctx, req)
	if err != nil {
		return nil, toStatus(err)
	}
	return resp, nil
}
func (h *HarnessServer) DiffLearnedSkillVersions(ctx context.Context, req *mecatlv1.DiffLearnedSkillVersionsRequest) (*mecatlv1.DiffLearnedSkillVersionsResponse, error) {
	resp, err := h.svc.DiffLearnedSkillVersions(ctx, req)
	if err != nil {
		return nil, toStatus(err)
	}
	return resp, nil
}
func (h *HarnessServer) ActivateLearnedSkill(ctx context.Context, req *mecatlv1.MutateLearnedSkillRequest) (*mecatlv1.MutateLearnedSkillResponse, error) {
	resp, err := h.svc.ActivateLearnedSkill(ctx, req)
	if err != nil {
		return nil, toStatus(err)
	}
	return resp, nil
}
func (h *HarnessServer) RejectLearnedSkill(ctx context.Context, req *mecatlv1.MutateLearnedSkillRequest) (*mecatlv1.MutateLearnedSkillResponse, error) {
	resp, err := h.svc.RejectLearnedSkill(ctx, req)
	if err != nil {
		return nil, toStatus(err)
	}
	return resp, nil
}
func (h *HarnessServer) ArchiveLearnedSkill(ctx context.Context, req *mecatlv1.MutateLearnedSkillRequest) (*mecatlv1.MutateLearnedSkillResponse, error) {
	resp, err := h.svc.ArchiveLearnedSkill(ctx, req)
	if err != nil {
		return nil, toStatus(err)
	}
	return resp, nil
}
func (h *HarnessServer) RollbackLearnedSkill(ctx context.Context, req *mecatlv1.RollbackLearnedSkillRequest) (*mecatlv1.MutateLearnedSkillResponse, error) {
	resp, err := h.svc.RollbackLearnedSkill(ctx, req)
	if err != nil {
		return nil, toStatus(err)
	}
	return resp, nil
}
func (h *HarnessServer) ListSkillChanges(ctx context.Context, req *mecatlv1.ListSkillChangesRequest) (*mecatlv1.ListSkillChangesResponse, error) {
	resp, err := h.svc.ListSkillChanges(ctx, req)
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

// StreamSessionEvents replays a session's durable event log as a server stream
// of Event envelopes (issue #245 Phase 1; cloud-native Phase 3a read-back).
func (h *HarnessServer) StreamSessionEvents(req *mecatlv1.StreamSessionEventsRequest, stream grpc.ServerStreamingServer[mecatlv1.Event]) error {
	if req.GetSessionId() == "" {
		return status.Error(codes.InvalidArgument, ErrInvalidArgument.Error())
	}
	events, err := h.svc.StreamSessionEvents(stream.Context(), session.SessionID(req.GetSessionId()))
	if err != nil {
		return toStatus(err)
	}
	// CRITICAL (replay-vs-live): the LIVE Converse relay SKIPS the three log-only
	// event kinds (EvApproval/EvCompactionArchive/EvUserPrompt) on the client wire
	// because they are persistence-only. StreamSessionEvents is the READ-BACK of the
	// durable log itself — a client opening a PAST session WANTS the verdicts and
	// user prompts (they ARE the transcript). So relay ALL events through toProto,
	// including the three log-only kinds. They are already metadata-only/redacted by
	// construction (gauntlet #7). Do NOT copy the live-relay filter here.
	for ev, iterErr := range events {
		if iterErr != nil {
			return status.Error(codes.Internal, iterErr.Error())
		}
		if err := stream.Send(toProto(ev)); err != nil {
			return err
		}
	}
	return nil
}

// StreamSessionLive is the LIVE per-session event stream (ADR 0075
// fire-result-delivery Scenario 6 / Wave 3): a thin transport over the in-process
// per-session subscription registry (Service.Subscribe / PublishSessionEvent). It
// is the UNIFIED bridge serving BOTH the embedded mecatui (which dials its
// in-process server over a real gRPC UNIX socket) AND a remote mecated — ONE
// proto, ONE TUI consumption path.
//
// Relay discipline: the live wire applies the SAME log-only skip as the live
// Converse relay, with ONE narrow exception — a fire-result DELIVERY note (an
// EvUserPrompt whose text starts with the renderFireDelivery provenance header
// "[scheduled task ") is RELAYED so a connected client renders the delivery card
// as it happens (AC6.2). The other two log-only kinds (EvApproval,
// EvCompactionArchive) stay SKIPPED, and a non-delivery EvUserPrompt stays
// skipped too (the client already holds its own prompt). The delivery note is
// metadata-only/redacted by construction (gauntlet #7).
//
// Drain-to-discard: a Send error (dead/disconnected client) cancels the
// subscription and the handler returns — the in-process registry's
// PublishSessionEvent is already non-blocking (a full subscriber channel drops
// the event, AC6.3), so a dead client never wedges the delivery run. The
// durable log records the tail regardless (it is appended by the relay/loop,
// independent of this stream).
func (h *HarnessServer) StreamSessionLive(req *mecatlv1.StreamSessionLiveRequest, stream grpc.ServerStreamingServer[mecatlv1.Event]) error {
	if req.GetSessionId() == "" {
		return status.Error(codes.InvalidArgument, ErrInvalidArgument.Error())
	}
	id := session.SessionID(req.GetSessionId())
	ch, unsub, err := h.svc.Subscribe(stream.Context(), id)
	if err != nil {
		return toStatus(err)
	}
	defer unsub()
	ctx := stream.Context()
	for {
		select {
		case <-ctx.Done():
			// The client cancelled or disconnected — exit cleanly. The deferred
			// unsub closes the subscription channel; PublishSessionEvent drops
			// further events for this (now-gone) subscriber.
			return nil
		case ev, ok := <-ch:
			if !ok {
				// The subscription channel was closed (unsub by the registry, e.g.
				// at Service.Close). Exit cleanly.
				return nil
			}
			// Apply the live-wire relay discipline: relay the delivery EvUserPrompt
			// (the ONE narrow exception) and every non-log-only event; skip the two
			// other log-only kinds and a non-delivery EvUserPrompt.
			if !relayLiveEvent(ev) {
				continue
			}
			if err := stream.Send(toProto(ev)); err != nil {
				// The client is gone — exit cleanly. The delivery run is NOT wedged
				// (PublishSessionEvent is non-blocking; a dead subscriber's channel
				// is closed by unsub, so further publishes drop).
				return nil
			}
		}
	}
}

// relayLiveEvent reports whether a live-subscription event should be relayed on
// the client wire. It mirrors the live Converse relay's log-only skip with ONE
// narrow exception: a fire-result DELIVERY note (an EvUserPrompt whose text
// starts with the renderFireDelivery provenance header) is relayed so the
// connected client renders the delivery card as it happens. The other two
// log-only kinds (EvApproval, EvCompactionArchive) and a non-delivery
// EvUserPrompt stay skipped (they are persistence-only; the client holds its
// own verdict/compaction/prompt view).
func relayLiveEvent(ev session.Event) bool {
	switch ev.Type {
	case session.EvApproval, session.EvCompactionArchive:
		return false
	case session.EvUserPrompt:
		// Relay the delivery note only — the renderFireDelivery provenance header
		// is the single detection pattern (mirrors the TUI client's
		// deliveryNotePrefix). A non-delivery EvUserPrompt stays skipped.
		//
		// renderFireDelivery wraps the note in agent.FenceUntrusted, so the
		// recorded text starts with the untrusted-fence opener
		// ("<<<UNTRUSTED\n") FOLLOWED by the "[scheduled task " provenance
		// header on the next line. The detection matches that FENCED form so a
		// delivery note (which is always fenced) is relayed while a non-delivery
		// user prompt (which is never fenced) stays skipped. A bare
		// HasPrefix("[scheduled task ") would NEVER match the real note (the
		// fence opener precedes the header) and would FALSE-match a user who
		// literally typed "[scheduled task …"; the fenced discriminator closes
		// both.
		return ev.UserPrompt != nil && isDeliveryNoteText(ev.UserPrompt.Text)
	default:
		return true
	}
}

// deliveryNoteHeaderPrefix is the literal provenance header renderFireDelivery
// emits INSIDE the fenced-untrusted block (the first line after the
// "<<<UNTRUSTED\n" opener). It is the single detection pattern the live-wire
// relay + the TUI client key off; it must match the SAME literal
// renderFireDelivery produces (internal/app/scheduler_delivery.go) and the TUI
// client's deliveryNotePrefix (cmd/mecatui/client/msgs.go). It is the shared
// contract between the server relay and the client projection.
const deliveryNoteHeaderPrefix = "[scheduled task "

// deliveryNoteFenceOpener is the leading fence marker renderFireDelivery wraps
// EVERY delivery note in (agent.FenceUntrusted writes "<<<UNTRUSTED\n" then the
// body). Detection keys off the fence opener FOLLOWED by the header prefix so a
// non-delivery user prompt (never fenced) cannot match.
const deliveryNoteFenceOpener = "<<<UNTRUSTED\n"

// isDeliveryNoteText reports whether text is a fenced fire-result delivery
// note: it starts with the untrusted-fence opener and its first content line
// begins with the delivery provenance header. This is the precise shape
// renderFireDelivery produces; a plain user prompt (un-fenced) never matches.
func isDeliveryNoteText(text string) bool {
	if !strings.HasPrefix(text, deliveryNoteFenceOpener) {
		return false
	}
	return strings.HasPrefix(text[len(deliveryNoteFenceOpener):], deliveryNoteHeaderPrefix)
}

// ListSessions returns the stored-session inventory — the picker metadata a
// client renders to let an operator open an EXISTING session by id (issue #245
// Phase 1).
func (h *HarnessServer) ListSessions(ctx context.Context, req *mecatlv1.ListSessionsRequest) (*mecatlv1.ListSessionsResponse, error) {
	page, err := h.svc.ListSessionPage(ctx, ListSessionsPageRequest{
		PageSize: int(req.GetPageSize()), Cursor: req.GetCursor(),
	})
	if err != nil {
		return nil, toStatus(err)
	}
	return &mecatlv1.ListSessionsResponse{
		Sessions: toProtoSessionSummaries(page.Sessions), NextCursor: page.NextCursor,
		TotalCount: ClampInt32(page.TotalCount),
	}, nil
}

// GetStorageHealth returns authenticated aggregate storage status.
func (h *HarnessServer) GetStorageHealth(ctx context.Context, _ *mecatlv1.GetStorageHealthRequest) (*mecatlv1.GetStorageHealthResponse, error) {
	health, err := h.svc.StorageHealth(ctx)
	if err != nil {
		return nil, toStatus(err)
	}
	return toProtoStorageHealth(health), nil
}

func toProtoStorageHealth(h StorageHealth) *mecatlv1.GetStorageHealthResponse {
	resp := &mecatlv1.GetStorageHealthResponse{
		Available: h.Available, UnavailableReason: h.UnavailableReason,
		CurrentBytes: h.CurrentBytes, CurrentBytesAvailable: h.CurrentBytesAvailable,
		ReclaimableBytes: h.ReclaimableBytes, ReclaimableBytesAvailable: h.ReclaimableBytesAvailable,
		SessionCount: h.SessionCount, FileCount: h.FileCount, V1Count: h.V1Count, V2Count: h.V2Count,
		MainCount: h.MainCount, ChildCount: h.ChildCount, ScheduledCount: h.ScheduledCount,
		UnknownCount: h.UnknownCount, CorruptCount: h.CorruptCount,
		Policy: &mecatlv1.RetentionPolicy{
			MainMaxAgeSeconds: int64(h.Policy.MainMaxAge.Seconds()), MainMaxCount: ClampInt32(h.Policy.MainMaxCount),
			ChildMaxAgeSeconds: int64(h.Policy.ChildMaxAge.Seconds()), ChildMaxCount: ClampInt32(h.Policy.ChildMaxCount),
			ScheduledMaxAgeSeconds: int64(h.Policy.ScheduledMaxAge.Seconds()), ScheduledMaxCount: ClampInt32(h.Policy.ScheduledMaxCount),
			SweepCadenceSeconds: int64(h.Policy.SweepCadence.Seconds()),
		},
		LastSweepAvailable: h.LastSweepAvailable,
		NextSweepAvailable: h.NextSweepAvailable,
		ActiveJob:          h.ActiveJob, LastFailure: h.LastFailure,
	}
	if h.LastSweepAvailable {
		resp.LastSweepUnix = h.LastSweep.Unix()
	}
	if h.NextSweepAvailable {
		resp.NextSweepUnix = h.NextSweep.Unix()
	}
	return resp
}

func toProtoMigrationPlan(plan MigrationPlan) *mecatlv1.SessionMigrationPlan {
	return &mecatlv1.SessionMigrationPlan{
		PlanId: plan.ID, Available: plan.Available, UnavailableReason: plan.UnavailableReason,
		V1Families: plan.V1Families, V2Families: plan.V2Families, InvalidFamilies: plan.InvalidFamilies,
		SkippedFamilies: plan.SkippedFamilies, CurrentBytes: plan.CurrentBytes,
		ReclaimableBytes: plan.ReclaimableBytes, TemporaryBytes: plan.TemporaryBytes,
	}
}

func toProtoMigrationJob(job MigrationJob) *mecatlv1.SessionMigrationJob {
	out := &mecatlv1.SessionMigrationJob{
		JobId: job.ID, State: job.State, V1Families: job.V1Families, V2Families: job.V2Families,
		InvalidFamilies: job.InvalidFamilies, SkippedFamilies: job.SkippedFamilies,
		CurrentBytes: job.CurrentBytes, ReclaimableBytes: job.ReclaimableBytes, TemporaryBytes: job.TemporaryBytes,
		Processed: job.Processed, Migrated: job.Migrated, Failed: job.Failed,
		Errors: make([]*mecatlv1.SessionMigrationItemError, 0, len(job.Errors)),
	}
	for _, item := range job.Errors {
		out.Errors = append(out.Errors, &mecatlv1.SessionMigrationItemError{ItemHandle: item.ItemHandle, ReasonCode: item.ReasonCode, Message: item.Message})
	}
	return out
}

func (h *HarnessServer) PlanSessionMigration(ctx context.Context, _ *mecatlv1.PlanSessionMigrationRequest) (*mecatlv1.SessionMigrationPlan, error) {
	plan, err := h.svc.PlanSessionMigration(ctx)
	if err != nil {
		return nil, toStatus(err)
	}
	return toProtoMigrationPlan(plan), nil
}

func (h *HarnessServer) ApplySessionMigration(ctx context.Context, req *mecatlv1.ApplySessionMigrationRequest) (*mecatlv1.SessionMigrationJob, error) {
	job, err := h.svc.ApplySessionMigration(ctx, req.GetPlanId(), int(req.GetBatchSize()))
	if err != nil {
		return nil, toStatus(err)
	}
	return toProtoMigrationJob(job), nil
}

func (h *HarnessServer) ResumeSessionMigration(ctx context.Context, req *mecatlv1.ResumeSessionMigrationRequest) (*mecatlv1.SessionMigrationJob, error) {
	job, err := h.svc.ResumeSessionMigration(ctx, req.GetJobId(), int(req.GetBatchSize()))
	if err != nil {
		return nil, toStatus(err)
	}
	return toProtoMigrationJob(job), nil
}

func (h *HarnessServer) CancelSessionMigration(ctx context.Context, req *mecatlv1.CancelSessionMigrationRequest) (*mecatlv1.SessionMigrationJob, error) {
	job, err := h.svc.CancelSessionMigration(ctx, req.GetJobId())
	if err != nil {
		return nil, toStatus(err)
	}
	return toProtoMigrationJob(job), nil
}

func (h *HarnessServer) GetSessionMigrationJob(ctx context.Context, req *mecatlv1.GetSessionMigrationJobRequest) (*mecatlv1.SessionMigrationJob, error) {
	job, err := h.svc.SessionMigrationJob(ctx, req.GetJobId())
	if err != nil {
		return nil, toStatus(err)
	}
	return toProtoMigrationJob(job), nil
}

// PlanSessionCleanup returns a caller-bound read-only retention plan.
func (h *HarnessServer) PlanSessionCleanup(ctx context.Context, req *mecatlv1.PlanSessionCleanupRequest) (*mecatlv1.PlanSessionCleanupResponse, error) {
	scope := CleanupScope{}
	for _, kind := range req.GetKinds() {
		scope.Kinds = append(scope.Kinds, session.SessionKind(kind))
	}
	plan, err := h.svc.PlanSessionCleanup(ctx, scope)
	if err != nil {
		return nil, toStatus(err)
	}
	return toProtoCleanupPlan(plan), nil
}

func (h *HarnessServer) ApplySessionCleanup(ctx context.Context, req *mecatlv1.ApplySessionCleanupRequest) (*mecatlv1.CleanupJob, error) {
	job, err := h.svc.ApplySessionCleanup(ctx, req.GetConfirmationToken())
	if err != nil {
		return nil, toStatus(err)
	}
	return toProtoCleanupJob(job), nil
}

func (h *HarnessServer) CancelSessionCleanup(ctx context.Context, req *mecatlv1.CancelSessionCleanupRequest) (*mecatlv1.CleanupJob, error) {
	job, err := h.svc.CancelSessionCleanup(ctx, req.GetJobId())
	if err != nil {
		return nil, toStatus(err)
	}
	return toProtoCleanupJob(job), nil
}

func (h *HarnessServer) GetSessionCleanupJob(ctx context.Context, req *mecatlv1.GetSessionCleanupJobRequest) (*mecatlv1.CleanupJob, error) {
	job, err := h.svc.SessionCleanupJob(ctx, req.GetJobId())
	if err != nil {
		return nil, toStatus(err)
	}
	return toProtoCleanupJob(job), nil
}

func toProtoCleanupPlan(plan CleanupPlan) *mecatlv1.PlanSessionCleanupResponse {
	resp := &mecatlv1.PlanSessionCleanupResponse{
		ConfirmationToken: plan.Token, Available: plan.Available, UnavailableReason: plan.UnavailableReason,
		Generation: plan.Generation, PolicyVersion: plan.PolicyVersion, EstimatedBytes: plan.EstimatedBytes,
		PlannedJobId:   plan.JobID,
		EligibleCounts: &mecatlv1.CleanupCounts{Total: ClampInt32(plan.EligibleCounts.Total), ByKind: mapStringInt32(plan.EligibleCounts.ByKind), ByState: mapStringInt32(plan.EligibleCounts.ByState), ByReason: mapStringInt32(plan.EligibleCounts.ByReason)},
		Protected:      &mecatlv1.CleanupCounts{Total: ClampInt32(plan.Protected.Total), ByKind: mapStringInt32(plan.Protected.ByKind), ByState: mapStringInt32(plan.Protected.ByState), ByReason: mapStringInt32(plan.Protected.ByReason)},
	}
	for _, item := range plan.Eligible {
		resp.Eligible = append(resp.Eligible, &mecatlv1.CleanupCandidate{SessionId: string(item.ID), Kind: string(item.Kind), State: string(item.State), Reason: item.Reason, ModifiedAtUnix: item.ModifiedAt.Unix(), EstimatedBytes: item.EstimatedBytes})
	}
	return resp
}

func mapStringInt32(values map[string]int) map[string]int32 {
	out := make(map[string]int32, len(values))
	for key, value := range values {
		out[key] = ClampInt32(value)
	}
	return out
}

func toProtoCleanupJob(job CleanupJob) *mecatlv1.CleanupJob {
	out := &mecatlv1.CleanupJob{JobId: job.ID, State: job.State, Processed: ClampInt32(job.Processed), Deleted: ClampInt32(job.Deleted), Skipped: ClampInt32(job.Skipped), Stale: ClampInt32(job.Stale), Failed: ClampInt32(job.Failed)}
	for _, item := range job.Errors {
		out.Errors = append(out.Errors, &mecatlv1.CleanupItemError{ItemHandle: item.ItemHandle, ReasonCode: item.ReasonCode, Message: item.Message})
	}
	return out
}

// toStatus maps service sentinel errors to gRPC status codes.
//
//nolint:gocyclo // a flat error→code classifier; a switch is the correct shape.
func toStatus(err error) error {
	switch {
	case errors.Is(err, ErrManagementUnauthorized):
		return status.Error(codes.PermissionDenied, err.Error())
	case errors.Is(err, ErrStorageHealthBackend):
		return status.Error(codes.Internal, err.Error())
	case errors.Is(err, ErrMigrationUnsupported):
		return status.Error(codes.Unimplemented, err.Error())
	case errors.Is(err, ErrMigrationConflict):
		return status.Error(codes.Aborted, err.Error())
	case errors.Is(err, ErrMigrationBackend):
		return status.Error(codes.Internal, err.Error())
	case errors.Is(err, ErrCleanupPlanStale):
		return status.Error(codes.Aborted, err.Error())
	case errors.Is(err, ErrCleanupUnsupported):
		return status.Error(codes.Unimplemented, err.Error())
	case errors.Is(err, ErrCleanupBackend):
		return status.Error(codes.Internal, err.Error())
	case errors.Is(err, ErrInvalidArgument):
		return status.Error(codes.InvalidArgument, err.Error())
	case errors.Is(err, ErrNotFound):
		return status.Error(codes.NotFound, err.Error())
	case errors.Is(err, ErrTeamNotFound):
		return status.Error(codes.NotFound, err.Error())
	case errors.Is(err, ErrChildNotFound):
		return status.Error(codes.NotFound, err.Error())
	case errors.Is(err, ErrLearningUnavailable):
		return status.Error(codes.Unimplemented, err.Error())
	case errors.Is(err, ErrProposalConflict):
		return status.Error(codes.Aborted, err.Error())
	case errors.Is(err, ErrDreamUnavailable):
		return status.Error(codes.Unimplemented, ErrDreamUnavailable.Error())
	case errors.Is(err, ErrDreamNotFound):
		return status.Error(codes.NotFound, ErrDreamNotFound.Error())
	case errors.Is(err, ErrDreamInProgress):
		return status.Error(codes.Aborted, ErrDreamInProgress.Error())
	case errors.Is(err, ErrDreamConflict):
		return status.Error(codes.FailedPrecondition, ErrDreamConflict.Error())
	case errors.Is(err, ErrDreamTerminalConflict):
		return status.Error(codes.AlreadyExists, ErrDreamTerminalConflict.Error())
	case errors.Is(err, ErrDreamCapacity):
		return status.Error(codes.ResourceExhausted, ErrDreamCapacity.Error())
	case errors.Is(err, ErrDreamGenerateFailed):
		return status.Error(codes.Internal, ErrDreamGenerateFailed.Error())
	case errors.Is(err, ErrDreamApplyFailed):
		return status.Error(codes.Internal, ErrDreamApplyFailed.Error())
	case errors.Is(err, ErrDreamDeadline):
		return status.Error(codes.DeadlineExceeded, ErrDreamDeadline.Error())
	case errors.Is(err, ErrDreamRequestFailed):
		return status.Error(codes.Internal, ErrDreamRequestFailed.Error())
	case errors.Is(err, ErrFailedPrecondition):
		return status.Error(codes.FailedPrecondition, err.Error())
	case errors.Is(err, ErrNoActiveRun):
		return status.Error(codes.FailedPrecondition, err.Error())
	case errors.Is(err, ErrNotAwaitingPlan):
		// ApprovePlan precondition (issue #206, Wave 4): the session is not parked
		// awaiting a plan-originated ask. FailedPrecondition (HTTP 409).
		return status.Error(codes.FailedPrecondition, err.Error())
	case errors.Is(err, ErrSessionLeasedElsewhere):
		// Cloud-native Phase 4: another replica holds the session's single-writer
		// lease. Well-formed request, transiently owned elsewhere — FailedPrecondition
		// (consistent with ErrNoActiveRun; HTTP maps it to 409 Conflict).
		return status.Error(codes.FailedPrecondition, err.Error())
	case errors.Is(err, ErrUnavailable):
		// ADR 0048 drain gate: this replica is draining (graceful shutdown) and
		// refuses new run-entries. Unavailable (HTTP 503) so the client retries a
		// survivor.
		return status.Error(codes.Unavailable, err.Error())
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
	case errors.Is(err, ErrNoScheduleStore):
		// The configured store backend does not implement ScheduleStore: the
		// schedule RPCs are not available on this deployment. Unimplemented.
		return status.Error(codes.Unimplemented, err.Error())
	case errors.Is(err, ErrNoEventLog):
		// No durable EventLog (cloud-native Phase 3a) is configured: the
		// StreamSessionEvents read-back surface is not available on this
		// deployment. Unimplemented (HTTP 501).
		return status.Error(codes.Unimplemented, err.Error())
	case errors.Is(err, ErrSessionDeleteUnsupported):
		return status.Error(codes.Unimplemented, err.Error())
	case errors.Is(err, port.ErrSessionMetadataCursorRestart):
		return status.Error(codes.Aborted, err.Error())
	case errors.Is(err, port.ErrSessionMetadataPagingUnsupported):
		return status.Error(codes.Unimplemented, err.Error())
	case errors.Is(err, ErrSchedulerNotRunning):
		// A ScheduleStore is available but no in-process scheduler is wired to
		// drive a manual FireNow. FailedPrecondition (HTTP 412), distinct from
		// ErrNoScheduleStore's Unimplemented (the store itself works fine).
		return status.Error(codes.FailedPrecondition, err.Error())
	case errors.Is(err, ErrScheduleDisabled):
		// FireNow on a paused/done schedule. FailedPrecondition (HTTP 412).
		return status.Error(codes.FailedPrecondition, err.Error())
	case errors.Is(err, ErrScheduleExhausted):
		// FireNow on an already-fired one-shot. FailedPrecondition (HTTP 412).
		return status.Error(codes.FailedPrecondition, err.Error())
	case errors.Is(err, ErrFireNowOverlap):
		// FireNow singleton-overlap skip. FailedPrecondition (HTTP 412) — the
		// schedule exists and is well-formed, it is just running.
		return status.Error(codes.FailedPrecondition, err.Error())
	case errors.Is(err, ErrScheduleNotLeader):
		// FireNow on a standby (non-leader) replica. FailedPrecondition (HTTP
		// 412); the message names the leader to redirect to.
		return status.Error(codes.FailedPrecondition, err.Error())
	case errors.Is(err, port.ErrScheduleNotFound):
		// A schedule/fire not found from the store. NotFound (HTTP 404).
		return status.Error(codes.NotFound, err.Error())
	case errors.Is(err, port.ErrScheduleUnsupported):
		// The backend can never store schedules (a sticky-disable case).
		// Unimplemented (HTTP 501).
		return status.Error(codes.Unimplemented, err.Error())
	case errors.Is(err, ErrInternal):
		return status.Error(codes.Internal, err.Error())
	default:
		return status.Error(codes.Internal, err.Error())
	}
}
