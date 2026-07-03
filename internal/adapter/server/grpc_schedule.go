package server

import (
	"context"
	"fmt"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/engine/port"
)

// grpc_schedule.go implements the ScheduleService RPCs over the shared Service.
// It mirrors grpc_team.go's convention: a thin struct embedding the generated
// UnimplementedServer (forward-compat) and holding the *Service; each handler
// validates required fields, delegates to the Service, maps the result, and
// wraps errors via toStatus (which maps ErrNoScheduleStore → Unimplemented,
// port.ErrScheduleNotFound → NotFound, etc.). When no ScheduleStore is wired
// the Service methods return ErrNoScheduleStore, so the schedule RPCs honestly
// report Unimplemented rather than pretending to succeed.

// ScheduleServer implements mecatlv1.ScheduleServiceServer over the shared
// Service. It embeds UnimplementedScheduleServiceServer for forward
// compatibility (require_unimplemented_servers=true in buf.gen.yaml), mirroring
// HarnessServer's embed of UnimplementedHarnessServiceServer.
type ScheduleServer struct {
	mecatlv1.UnimplementedScheduleServiceServer
	svc *Service
}

// NewScheduleServer constructs a ScheduleServer over svc.
func NewScheduleServer(svc *Service) *ScheduleServer {
	return &ScheduleServer{svc: svc}
}

// compile-time assertion that ScheduleServer satisfies the generated interface.
var _ mecatlv1.ScheduleServiceServer = (*ScheduleServer)(nil)

// CreateSchedule saves a new schedule (upsert by name) and returns the created
// aggregate. The create-seam (composition) validates the spec fail-closed — the
// trigger XOR, the prompt-or-parts rule, the cron grammar, the Mutating/Mode
// invariant — so the handler only checks the wire-required fields (spec non-nil,
// name non-empty) and delegates.
func (h *ScheduleServer) CreateSchedule(ctx context.Context, req *mecatlv1.CreateScheduleRequest) (*mecatlv1.CreateScheduleResponse, error) {
	spec := req.GetSpec()
	if spec == nil {
		return nil, status.Error(codes.InvalidArgument, "spec is required")
	}
	if spec.GetName() == "" {
		return nil, status.Error(codes.InvalidArgument, "spec.name is required")
	}
	portSpec, err := protoToScheduleSpec(spec)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	sched, err := h.svc.CreateSchedule(ctx, portSpec)
	if err != nil {
		return nil, toStatus(err)
	}
	return &mecatlv1.CreateScheduleResponse{Schedule: scheduleToProto(sched)}, nil
}

// GetSchedule returns the schedule stored under name.
func (h *ScheduleServer) GetSchedule(ctx context.Context, req *mecatlv1.GetScheduleRequest) (*mecatlv1.GetScheduleResponse, error) {
	if req.GetName() == "" {
		return nil, status.Error(codes.InvalidArgument, "name is required")
	}
	sched, err := h.svc.GetSchedule(ctx, req.GetName())
	if err != nil {
		return nil, toStatus(err)
	}
	return &mecatlv1.GetScheduleResponse{Schedule: scheduleToProto(sched)}, nil
}

// ListSchedules returns all stored schedules.
func (h *ScheduleServer) ListSchedules(ctx context.Context, _ *mecatlv1.ListSchedulesRequest) (*mecatlv1.ListSchedulesResponse, error) {
	schedules, err := h.svc.ListSchedules(ctx)
	if err != nil {
		return nil, toStatus(err)
	}
	out := make([]*mecatlv1.Schedule, 0, len(schedules))
	for _, s := range schedules {
		out = append(out, scheduleToProto(s))
	}
	return &mecatlv1.ListSchedulesResponse{Schedules: out}, nil
}

// UpdateSchedule updates an existing schedule's spec (the State half is
// preserved on overwrite — the Service.Load+Save discipline).
func (h *ScheduleServer) UpdateSchedule(ctx context.Context, req *mecatlv1.UpdateScheduleRequest) (*mecatlv1.UpdateScheduleResponse, error) {
	spec := req.GetSpec()
	if spec == nil {
		return nil, status.Error(codes.InvalidArgument, "spec is required")
	}
	if spec.GetName() == "" {
		return nil, status.Error(codes.InvalidArgument, "spec.name is required")
	}
	portSpec, err := protoToScheduleSpec(spec)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	sched, err := h.svc.UpdateSchedule(ctx, portSpec)
	if err != nil {
		return nil, toStatus(err)
	}
	return &mecatlv1.UpdateScheduleResponse{Schedule: scheduleToProto(sched)}, nil
}

// DeleteSchedule removes the schedule stored under name. Idempotent.
func (h *ScheduleServer) DeleteSchedule(ctx context.Context, req *mecatlv1.DeleteScheduleRequest) (*mecatlv1.DeleteScheduleResponse, error) {
	if req.GetName() == "" {
		return nil, status.Error(codes.InvalidArgument, "name is required")
	}
	if err := h.svc.DeleteSchedule(ctx, req.GetName()); err != nil {
		return nil, toStatus(err)
	}
	return &mecatlv1.DeleteScheduleResponse{}, nil
}

// FireNow forces an immediate fire of the schedule, returning the per-fire
// session id (fire_id == session_id on the wire).
func (h *ScheduleServer) FireNow(ctx context.Context, req *mecatlv1.FireNowRequest) (*mecatlv1.FireNowResponse, error) {
	if req.GetName() == "" {
		return nil, status.Error(codes.InvalidArgument, "name is required")
	}
	fire, err := h.svc.FireNow(ctx, req.GetName())
	if err != nil {
		return nil, toStatus(err)
	}
	return &mecatlv1.FireNowResponse{FireId: fire.ID, SessionId: string(fire.SessionID)}, nil
}

// PauseSchedule disables a schedule without deleting it (Enabled=false).
func (h *ScheduleServer) PauseSchedule(ctx context.Context, req *mecatlv1.PauseScheduleRequest) (*mecatlv1.PauseScheduleResponse, error) {
	if req.GetName() == "" {
		return nil, status.Error(codes.InvalidArgument, "name is required")
	}
	if err := h.svc.PauseSchedule(ctx, req.GetName()); err != nil {
		return nil, toStatus(err)
	}
	return &mecatlv1.PauseScheduleResponse{}, nil
}

// ResumeSchedule re-enables a paused schedule (Enabled=true).
func (h *ScheduleServer) ResumeSchedule(ctx context.Context, req *mecatlv1.ResumeScheduleRequest) (*mecatlv1.ResumeScheduleResponse, error) {
	if req.GetName() == "" {
		return nil, status.Error(codes.InvalidArgument, "name is required")
	}
	if err := h.svc.ResumeSchedule(ctx, req.GetName()); err != nil {
		return nil, toStatus(err)
	}
	return &mecatlv1.ResumeScheduleResponse{}, nil
}

// GetFire returns the fire record stored under fire_id.
func (h *ScheduleServer) GetFire(ctx context.Context, req *mecatlv1.GetFireRequest) (*mecatlv1.GetFireResponse, error) {
	if req.GetFireId() == "" {
		return nil, status.Error(codes.InvalidArgument, "fire_id is required")
	}
	fire, err := h.svc.GetFire(ctx, req.GetFireId())
	if err != nil {
		return nil, toStatus(err)
	}
	return &mecatlv1.GetFireResponse{Fire: scheduleFireToProto(fire)}, nil
}

// ListFires returns the fire records for a schedule.
func (h *ScheduleServer) ListFires(ctx context.Context, req *mecatlv1.ListFiresRequest) (*mecatlv1.ListFiresResponse, error) {
	if req.GetScheduleName() == "" {
		return nil, status.Error(codes.InvalidArgument, "schedule_name is required")
	}
	fires, err := h.svc.ListFires(ctx, req.GetScheduleName())
	if err != nil {
		return nil, toStatus(err)
	}
	out := make([]*mecatlv1.ScheduleFire, 0, len(fires))
	for _, f := range fires {
		out = append(out, scheduleFireToProto(f))
	}
	return &mecatlv1.ListFiresResponse{Fires: out}, nil
}

// --- schedule mappers (proto ↔ port value objects) --------------------------

// protoToScheduleSpec maps the proto ScheduleSpec to the port.ScheduleSpec the
// Service methods take. It reuses the existing contentFromProto / limitsFromProto
// / modeFromProto mappers (the single content-validation path). TriggerSpec maps
// the cron string + one_shot Timestamp → time.Time. An absent trigger is a value
// error (the create-seam's Trigger.Validate rejects it). Timestamps without a
// created_at are left zero (the create-seam stamps CreatedAt itself, so a caller
// need not set it — an honest overwrite, not a silent default).
func protoToScheduleSpec(in *mecatlv1.ScheduleSpec) (port.ScheduleSpec, error) {
	out := port.ScheduleSpec{
		Name:      in.GetName(),
		Prompt:    in.GetPrompt(),
		Profile:   in.GetProfile(),
		Workspace: in.GetWorkspace(),
		Mode:      modeFromProto(in.GetMode()),
		Limits:    limitsFromProto(in.GetLimits()),
		Mutating:  in.GetMutating(),
		MaxFires:  int(in.GetMaxFires()),
		Singleton: in.GetSingleton(),
		Timezone:  in.GetTimezone(),
		Misfire:   misfireFromProto(in.GetMisfire()),
	}
	if sel := in.GetSelector(); sel != nil {
		out.Selector = port.ScheduleProviderSelector{
			ProviderID: sel.GetProviderId(),
			ModelID:    sel.GetModelId(),
		}
	}
	if t := in.GetTrigger(); t != nil {
		out.Trigger = port.TriggerSpec{
			Cron: t.GetCron(),
		}
		if ts := t.GetOneShot(); ts != nil {
			out.Trigger.OneShot = ts.AsTime()
		}
	} else {
		// A nil trigger is structurally invalid; surface it here so the
		// handler returns InvalidArgument rather than passing TriggerNone to
		// the create-seam (which would also reject it, but with a port error).
		return port.ScheduleSpec{}, fmt.Errorf("trigger is required")
	}
	parts, err := contentFromProto(in.GetParts())
	if err != nil {
		return port.ScheduleSpec{}, err
	}
	out.Parts = parts
	if ca := in.GetCreatedAt(); ca != nil {
		out.CreatedAt = ca.AsTime()
	}
	return out, nil
}

// scheduleToProto maps the port.Schedule back to its proto projection (the
// inverse of protoToScheduleSpec). It reuses contentToProto / limitsToProto /
// modeToProto. Zero-value timestamps project to nil (the proto convention — a
// zero time has no wire representation).
func scheduleToProto(in port.Schedule) *mecatlv1.Schedule {
	return &mecatlv1.Schedule{
		Spec:  scheduleSpecToProto(in.Spec),
		State: scheduleStateToProto(in.State),
	}
}

// scheduleSpecToProto maps port.ScheduleSpec → proto ScheduleSpec.
func scheduleSpecToProto(in port.ScheduleSpec) *mecatlv1.ScheduleSpec {
	out := &mecatlv1.ScheduleSpec{
		Name:      in.Name,
		Prompt:    in.Prompt,
		Parts:     contentToProto(in.Parts),
		Profile:   in.Profile,
		Workspace: in.Workspace,
		Mode:      modeToProto(in.Mode),
		Limits:    limitsToProto(in.Limits),
		Mutating:  in.Mutating,
		MaxFires:  clampInt32(in.MaxFires),
		Misfire:   misfireToProto(in.Misfire),
		Singleton: in.Singleton,
		Timezone:  in.Timezone,
	}
	if in.Trigger.Cron != "" || !in.Trigger.OneShot.IsZero() {
		out.Trigger = &mecatlv1.TriggerSpec{
			Cron: in.Trigger.Cron,
		}
		if !in.Trigger.OneShot.IsZero() {
			out.Trigger.OneShot = timestamppb.New(in.Trigger.OneShot)
		}
	}
	if in.Selector.ProviderID != "" || in.Selector.ModelID != "" {
		out.Selector = &mecatlv1.ScheduleProviderSelector{
			ProviderId: in.Selector.ProviderID,
			ModelId:    in.Selector.ModelID,
		}
	}
	if !in.CreatedAt.IsZero() {
		out.CreatedAt = timestamppb.New(in.CreatedAt)
	}
	return out
}

// scheduleStateToProto maps port.ScheduleState → proto ScheduleState.
func scheduleStateToProto(in port.ScheduleState) *mecatlv1.ScheduleState {
	out := &mecatlv1.ScheduleState{
		FireCount:         clampInt32(in.FireCount),
		Enabled:           in.Enabled,
		LastFireSessionId: string(in.LastFireSessionID),
	}
	if !in.NextFireAt.IsZero() {
		out.NextFireAt = timestamppb.New(in.NextFireAt)
	}
	if !in.LastFireAt.IsZero() {
		out.LastFireAt = timestamppb.New(in.LastFireAt)
	}
	return out
}

// scheduleFireToProto maps port.ScheduleFire → proto ScheduleFire. Stop is a
// session.StopReason (a string-typed value); it projects verbatim as the
// passthrough string the wire carries.
func scheduleFireToProto(in port.ScheduleFire) *mecatlv1.ScheduleFire {
	out := &mecatlv1.ScheduleFire{
		Id:           in.ID,
		ScheduleName: in.ScheduleName,
		SessionId:    string(in.SessionID),
		Stop:         string(in.Stop),
		Err:          in.Err,
	}
	if !in.FiredAt.IsZero() {
		out.FiredAt = timestamppb.New(in.FiredAt)
	}
	return out
}

// misfireFromProto maps the proto MisfirePolicy enum to the port.MisfirePolicy,
// defaulting UNSPECIFIED to the zero value (MisfireFireOnceNow, the intended
// default per the port doc-comment).
func misfireFromProto(m mecatlv1.MisfirePolicy) port.MisfirePolicy {
	switch m {
	case mecatlv1.MisfirePolicy_MISFIRE_SKIP:
		return port.MisfireSkip
	default:
		// UNSPECIFIED and FIRE_ONCE_NOW both map to the zero value
		// (MisfireFireOnceNow), the documented default.
		return port.MisfireFireOnceNow
	}
}

// misfireToProto maps the port.MisfirePolicy to its proto enum.
func misfireToProto(m port.MisfirePolicy) mecatlv1.MisfirePolicy {
	switch m {
	case port.MisfireSkip:
		return mecatlv1.MisfirePolicy_MISFIRE_SKIP
	default:
		return mecatlv1.MisfirePolicy_MISFIRE_FIRE_ONCE_NOW
	}
}
