package server

import (
	"context"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/engine/agent"
)

// grpc_team.go implements the agent-team RPCs over the shared Service. Event
// mapping reuses toProto (mapper.go); only the per-member tag is new.

// CreateTeam allocates a new agent team and returns its id, enrolling the optional
// initial roster atomically (any member failure abandons the whole team). The
// enrolled roster is echoed back so the caller need not follow up with ListTeam.
func (h *HarnessServer) CreateTeam(ctx context.Context, req *mecatlv1.CreateTeamRequest) (*mecatlv1.CreateTeamResponse, error) {
	id, enrolled, err := h.svc.CreateTeam(ctx, req.GetWorkspace(), req.GetName(), req.GetGoal(), int(req.GetMaxTeamTokens()), fromProtoTeammateSpecs(req.GetMembers()))
	if err != nil {
		return nil, toStatus(err)
	}
	return &mecatlv1.CreateTeamResponse{TeamId: id, Members: toProtoTeamMembers(enrolled)}, nil
}

// fromProtoTeammateSpecs maps the proto initial-roster specs to agent.MemberSpec,
// the per-member fields shared with SpawnTeammate (minus team_id).
func fromProtoTeammateSpecs(specs []*mecatlv1.TeammateSpec) []agent.MemberSpec {
	if len(specs) == 0 {
		return nil
	}
	out := make([]agent.MemberSpec, 0, len(specs))
	for _, sp := range specs {
		out = append(out, agent.MemberSpec{
			Name:          sp.GetName(),
			AgentType:     sp.GetAgentType(),
			Lead:          sp.GetLead(),
			Mutating:      sp.GetMutating(),
			InitialPrompt: sp.GetInitialPrompt(),
		})
	}
	return out
}

// SpawnTeammate enrols a member in a team and returns its roster entry.
func (h *HarnessServer) SpawnTeammate(ctx context.Context, req *mecatlv1.SpawnTeammateRequest) (*mecatlv1.SpawnTeammateResponse, error) {
	if req.GetTeamId() == "" || req.GetName() == "" {
		return nil, status.Error(codes.InvalidArgument, "team_id and name are required")
	}
	m, err := h.svc.SpawnTeammate(ctx, req.GetTeamId(), agent.MemberSpec{
		Name:          req.GetName(),
		AgentType:     req.GetAgentType(),
		Lead:          req.GetLead(),
		Mutating:      req.GetMutating(),
		InitialPrompt: req.GetInitialPrompt(),
	})
	if err != nil {
		return nil, toStatus(err)
	}
	return &mecatlv1.SpawnTeammateResponse{Member: toProtoTeamMember(m)}, nil
}

// SendTeammateMessage posts a message into a member's inbox.
func (h *HarnessServer) SendTeammateMessage(ctx context.Context, req *mecatlv1.SendTeammateMessageRequest) (*mecatlv1.SendTeammateMessageResponse, error) {
	if req.GetTeamId() == "" || req.GetTo() == "" || req.GetBody() == "" {
		return nil, status.Error(codes.InvalidArgument, "team_id, to and body are required")
	}
	if err := h.svc.SendTeammateMessage(ctx, req.GetTeamId(), req.GetFrom(), req.GetTo(), req.GetBody()); err != nil {
		return nil, toStatus(err)
	}
	return &mecatlv1.SendTeammateMessageResponse{}, nil
}

// CancelTeammate cancels one member of a running team (issue #29).
func (h *HarnessServer) CancelTeammate(ctx context.Context, req *mecatlv1.CancelTeammateRequest) (*mecatlv1.CancelTeammateResponse, error) {
	if req.GetTeamId() == "" || req.GetMember() == "" {
		return nil, status.Error(codes.InvalidArgument, "team_id and member are required")
	}
	if err := h.svc.CancelTeammate(ctx, req.GetTeamId(), req.GetMember()); err != nil {
		return nil, toStatus(err)
	}
	return &mecatlv1.CancelTeammateResponse{}, nil
}

// RunTeam drives the team to quiescence, streaming every member event tagged with
// the producing member, then ends the stream with the single terminal frame
// carrying TeamEvent.outcome (issue #36). Supervisor.Run serialises sink calls
// through a single forwarder, so stream.Send is never invoked concurrently. A send
// failure cancels the run so the team stops promptly rather than running on to
// quiescence unseen; the outcome frame is sent only when no send has failed
// (relay-drain discipline — a dead client gets no further writes).
func (h *HarnessServer) RunTeam(req *mecatlv1.RunTeamRequest, stream mecatlv1.HarnessService_RunTeamServer) error {
	if req.GetTeamId() == "" {
		return status.Error(codes.InvalidArgument, "team_id is required")
	}
	ctx, cancel := context.WithCancel(stream.Context())
	defer cancel()

	var sendErr error
	out, err := h.svc.RunTeam(ctx, req.GetTeamId(), func(te agent.TeamEvent) {
		if sendErr != nil {
			return
		}
		if !isPublicEvent(te.Event) {
			return
		}
		if e := stream.Send(&mecatlv1.TeamEvent{Member: te.Member, Event: toProto(te.Event)}); e != nil {
			sendErr = e
			cancel()
		}
	})
	if err != nil {
		return toStatus(err)
	}
	if sendErr != nil {
		return sendErr
	}
	return stream.Send(&mecatlv1.TeamEvent{Outcome: toProtoTeamOutcome(out)})
}

// ListTeam returns a snapshot of the team roster, task list, and completion state.
func (h *HarnessServer) ListTeam(ctx context.Context, req *mecatlv1.ListTeamRequest) (*mecatlv1.ListTeamResponse, error) {
	if req.GetTeamId() == "" {
		return nil, status.Error(codes.InvalidArgument, "team_id is required")
	}
	members, tasks, quiescent, err := h.svc.ListTeam(ctx, req.GetTeamId())
	if err != nil {
		return nil, toStatus(err)
	}
	return &mecatlv1.ListTeamResponse{
		Members:   toProtoTeamMembers(members),
		Tasks:     toProtoTeamTasks(tasks),
		Quiescent: quiescent,
	}, nil
}

// CleanupTeam tears down a finished team.
func (h *HarnessServer) CleanupTeam(ctx context.Context, req *mecatlv1.CleanupTeamRequest) (*mecatlv1.CleanupTeamResponse, error) {
	if req.GetTeamId() == "" {
		return nil, status.Error(codes.InvalidArgument, "team_id is required")
	}
	if err := h.svc.CleanupTeam(ctx, req.GetTeamId()); err != nil {
		return nil, toStatus(err)
	}
	return &mecatlv1.CleanupTeamResponse{}, nil
}
