package client

import (
	"context"
	"time"

	tea "charm.land/bubbletea/v2"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
)

// The scheduled-tasks discovery + management surface (issue #234, Phase 3a): a
// plain client-owned mirror of the schedule.proto value objects, the unary RPC
// wrappers that map proto → the structs, and the tea.Cmd constructors the ui's
// /schedule overlay calls. As with the rest of this package, NO proto type leaks
// past this file — the ui renders purely from the structs and msgs below, and
// the mapping is exercised offline against a fake client.
//
// Phase 3a scope: the overlay lists/inspects/pauses/resumes/fires-now/deletes
// schedules. UpdateSchedule is intentionally omitted (no in-overlay edit-in-place
// form in v1); create-from-the-overlay is likewise deferred. A caller that needs
// to author a schedule uses the CLI (mecated schedule create) or settings.yaml.

// ScheduleTrigger is the sum type for a schedule's firing trigger: a cron
// expression OR a one-shot wall-clock instant. Exactly one is set (a zero OneShot
// means "not set"). Mirrors mecatlv1.TriggerSpec.
type ScheduleTrigger struct {
	Cron    string
	OneShot time.Time // zero = not set
}

// ScheduleSelector is the provider+model pair a schedule's fires run on. Both
// empty means "use the deployment default". Mirrors mecatlv1.ScheduleProviderSelector.
type ScheduleSelector struct {
	ProviderID string
	ModelID    string
}

// ScheduleSpec is the immutable definition of a schedule — the "what to run and
// when" half. Mirrors mecatlv1.ScheduleSpec. Parts (the OPTIONAL multimodal
// extension) is OMITTED for v1: the /schedule overlay does not author multimodal
// prompts, and a future phase that does will add the field here then.
type ScheduleSpec struct {
	Name      string
	Prompt    string
	Trigger   ScheduleTrigger
	Selector  ScheduleSelector
	Profile   string
	Mode      string
	Mutating  bool
	MaxFires  int32
	Misfire   string // canonical: "fire_once_now" (default) | "skip"
	Singleton bool
	Timezone  string
	CreatedAt time.Time
	// FireTimeout is the per-fire wall-clock deadline (issue #386). Zero means
	// "use the deployment default" (which may itself be zero for "no explicit
	// deadline"). Mirrors mecatlv1.ScheduleSpec.fire_timeout.
	FireTimeout time.Duration
}

// ScheduleState is the durable FIRING state of a schedule — the mutable half that
// advances as the schedule fires. Mirrors mecatlv1.ScheduleState.
type ScheduleState struct {
	NextFireAt        time.Time
	LastFireAt        time.Time
	FireCount         int32
	Enabled           bool
	DeletionPending   bool
	LastFireSessionID string
	// LastFireStartedAt is when the current fire's run began (RecordFireStart),
	// the in-flight liveness marker. Zero means the run has not started. #386.
	LastFireStartedAt time.Time
	// LastFireProgressAt is the last observed progress instant for the current
	// fire. Zero means no progress observed. #386.
	LastFireProgressAt time.Time
	// FireDeadline is the current fire's wall-clock deadline (RecordFireStart).
	// Zero means no explicit deadline. #386.
	FireDeadline time.Time
}

// Schedule is the aggregate value object: the immutable Spec plus the durable
// State. Mirrors mecatlv1.Schedule.
type Schedule struct {
	Spec  ScheduleSpec
	State ScheduleState
}

// ScheduleFire is one fire record: the outcome of a single Claim→run→RecordFire
// cycle. Mirrors mecatlv1.ScheduleFire.
type ScheduleFire struct {
	ID           string
	ScheduleName string
	SessionID    string
	FiredAt      time.Time
	Stop         string
	Err          string
	// StartedAt is when the fire's run began (RecordFireStart). Zero on a
	// terminal-only fire. An in-flight fire has Stop empty + StartedAt set. #386.
	StartedAt time.Time
	// ProgressAt is the last observed progress instant for the fire. #386.
	ProgressAt time.Time
	// Deadline is the fire's wall-clock deadline (RecordFireStart). #386.
	Deadline time.Time
}

// SchedulesMsg carries a ListSchedules result for the /schedule overlay. Err is
// set on failure; the overlay surfaces it rather than silently degrading.
type SchedulesMsg struct {
	Schedules []Schedule
	Err       error
}

// ScheduleMsg carries a Get/Create result for the /schedule overlay. Err is set
// on failure.
type ScheduleMsg struct {
	Schedule Schedule
	Err      error
}

// ScheduleFiresMsg carries a ListFires result for the /schedule overlay's inspect
// sub-view. Err is set on failure.
type ScheduleFiresMsg struct {
	Fires []ScheduleFire
	Err   error
}

// ScheduleActionMsg carries the outcome of a pause/resume/delete/fire-now action.
// Action is "paused"/"resumed"/"deleted"/"fired"; FireID is set only for "fired"
// (the per-fire session id, pollable via GetFire). Err is set on failure.
type ScheduleActionMsg struct {
	Name   string
	Action string
	FireID string
	Err    error
}

// ListSchedules lists all stored schedules (the /schedule overlay's initial fetch).
func (c *Client) ListSchedules(ctx context.Context) ([]Schedule, error) {
	resp, err := c.scheduleSvc.ListSchedules(ctx, &mecatlv1.ListSchedulesRequest{})
	if err != nil {
		return nil, err
	}
	return mapSchedules(resp.GetSchedules()), nil
}

// GetSchedule returns the schedule stored under name (the inspect sub-view's
// refresh).
func (c *Client) GetSchedule(ctx context.Context, name string) (Schedule, error) {
	resp, err := c.scheduleSvc.GetSchedule(ctx, &mecatlv1.GetScheduleRequest{Name: name})
	if err != nil {
		return Schedule{}, err
	}
	return mapSchedule(resp.GetSchedule()), nil
}

// CreateSchedule saves a new schedule (an upsert by name) and returns the created
// aggregate. It is the SINGLE proto-build point for Create (scheduleSpecToProto).
// Exported for the planned in-overlay Create form (Phase 3b); the v1 overlay does
// not call it.
func (c *Client) CreateSchedule(ctx context.Context, spec ScheduleSpec) (Schedule, error) {
	resp, err := c.scheduleSvc.CreateSchedule(ctx, &mecatlv1.CreateScheduleRequest{Spec: scheduleSpecToProto(spec)})
	if err != nil {
		return Schedule{}, err
	}
	return mapSchedule(resp.GetSchedule()), nil
}

// DeleteSchedule removes the schedule stored under name. Idempotent.
func (c *Client) DeleteSchedule(ctx context.Context, name string) error {
	_, err := c.scheduleSvc.DeleteSchedule(ctx, &mecatlv1.DeleteScheduleRequest{Name: name})
	return err
}

// FireNow forces an immediate fire of the schedule, returning the per-fire
// session id (fire_id == session_id on the wire).
func (c *Client) FireNow(ctx context.Context, name string) (fireID, sessionID string, err error) {
	resp, err := c.scheduleSvc.FireNow(ctx, &mecatlv1.FireNowRequest{Name: name})
	if err != nil {
		return "", "", err
	}
	return resp.GetFireId(), resp.GetSessionId(), nil
}

// PauseSchedule disables a schedule without deleting it (Enabled=false).
func (c *Client) PauseSchedule(ctx context.Context, name string) error {
	_, err := c.scheduleSvc.PauseSchedule(ctx, &mecatlv1.PauseScheduleRequest{Name: name})
	return err
}

// ResumeSchedule re-enables a paused schedule (Enabled=true).
func (c *Client) ResumeSchedule(ctx context.Context, name string) error {
	_, err := c.scheduleSvc.ResumeSchedule(ctx, &mecatlv1.ResumeScheduleRequest{Name: name})
	return err
}

// ListFires returns the fire records for a schedule (the inspect sub-view).
func (c *Client) ListFires(ctx context.Context, scheduleName string) ([]ScheduleFire, error) {
	resp, err := c.scheduleSvc.ListFires(ctx, &mecatlv1.ListFiresRequest{ScheduleName: scheduleName})
	if err != nil {
		return nil, err
	}
	return mapFires(resp.GetFires()), nil
}

// GetFire returns the fire record stored under fireID. Included for completeness
// (the overlay uses ListFires; a future per-fire drill-down would use this).
func (c *Client) GetFire(ctx context.Context, fireID string) (ScheduleFire, error) {
	resp, err := c.scheduleSvc.GetFire(ctx, &mecatlv1.GetFireRequest{FireId: fireID})
	if err != nil {
		return ScheduleFire{}, err
	}
	return mapFire(resp.GetFire()), nil
}

// ScheduleLister is the subset of *Client the ui's /schedule overlay needs.
// Splitting it out keeps the ui injectable with a fake for offline tests; *Client
// satisfies it.
type ScheduleLister interface {
	ListSchedules(ctx context.Context) ([]Schedule, error)
	GetSchedule(ctx context.Context, name string) (Schedule, error)
	CreateSchedule(ctx context.Context, spec ScheduleSpec) (Schedule, error)
	DeleteSchedule(ctx context.Context, name string) error
	FireNow(ctx context.Context, name string) (fireID, sessionID string, err error)
	PauseSchedule(ctx context.Context, name string) error
	ResumeSchedule(ctx context.Context, name string) error
	ListFires(ctx context.Context, scheduleName string) ([]ScheduleFire, error)
}

// compile-time assertion that *Client satisfies ScheduleLister.
var _ ScheduleLister = (*Client)(nil)

// ListSchedulesCmd fetches the schedule list off the update goroutine; the
// result (success or error) arrives as a SchedulesMsg.
func ListSchedulesCmd(ctx context.Context, c ScheduleLister) tea.Cmd {
	return func() tea.Msg {
		schedules, err := c.ListSchedules(ctx)
		if err != nil {
			return SchedulesMsg{Err: err}
		}
		return SchedulesMsg{Schedules: schedules}
	}
}

// GetScheduleCmd fetches a single schedule off the update goroutine; the result
// arrives as a ScheduleMsg.
func GetScheduleCmd(ctx context.Context, c ScheduleLister, name string) tea.Cmd {
	return func() tea.Msg {
		sched, err := c.GetSchedule(ctx, name)
		if err != nil {
			return ScheduleMsg{Err: err}
		}
		return ScheduleMsg{Schedule: sched}
	}
}

// CreateScheduleCmd creates a schedule off the update goroutine; the result
// arrives as a ScheduleMsg. Exported for the planned in-overlay Create form
// (Phase 3b); the v1 overlay does not call it.
func CreateScheduleCmd(ctx context.Context, c ScheduleLister, spec ScheduleSpec) tea.Cmd {
	return func() tea.Msg {
		sched, err := c.CreateSchedule(ctx, spec)
		if err != nil {
			return ScheduleMsg{Err: err}
		}
		return ScheduleMsg{Schedule: sched}
	}
}

// DeleteScheduleCmd deletes a schedule off the update goroutine; the result
// arrives as a ScheduleActionMsg{Action:"deleted"}.
func DeleteScheduleCmd(ctx context.Context, c ScheduleLister, name string) tea.Cmd {
	return func() tea.Msg {
		err := c.DeleteSchedule(ctx, name)
		return ScheduleActionMsg{Name: name, Action: "deleted", Err: err}
	}
}

// FireNowCmd forces an immediate fire off the update goroutine; the result
// arrives as a ScheduleActionMsg{Action:"fired"} carrying the fire id.
func FireNowCmd(ctx context.Context, c ScheduleLister, name string) tea.Cmd {
	return func() tea.Msg {
		fireID, _, err := c.FireNow(ctx, name)
		return ScheduleActionMsg{Name: name, Action: "fired", FireID: fireID, Err: err}
	}
}

// PauseScheduleCmd pauses a schedule off the update goroutine; the result arrives
// as a ScheduleActionMsg{Action:"paused"}.
func PauseScheduleCmd(ctx context.Context, c ScheduleLister, name string) tea.Cmd {
	return func() tea.Msg {
		err := c.PauseSchedule(ctx, name)
		return ScheduleActionMsg{Name: name, Action: "paused", Err: err}
	}
}

// ResumeScheduleCmd resumes a schedule off the update goroutine; the result
// arrives as a ScheduleActionMsg{Action:"resumed"}.
func ResumeScheduleCmd(ctx context.Context, c ScheduleLister, name string) tea.Cmd {
	return func() tea.Msg {
		err := c.ResumeSchedule(ctx, name)
		return ScheduleActionMsg{Name: name, Action: "resumed", Err: err}
	}
}

// ListFiresCmd fetches a schedule's fire records off the update goroutine; the
// result arrives as a ScheduleFiresMsg.
func ListFiresCmd(ctx context.Context, c ScheduleLister, scheduleName string) tea.Cmd {
	return func() tea.Msg {
		fires, err := c.ListFires(ctx, scheduleName)
		if err != nil {
			return ScheduleFiresMsg{Err: err}
		}
		return ScheduleFiresMsg{Fires: fires}
	}
}

// mapSchedule maps a proto Schedule to the plain struct (nil-safe).
func mapSchedule(in *mecatlv1.Schedule) Schedule {
	if in == nil {
		return Schedule{}
	}
	return Schedule{Spec: mapScheduleSpec(in.GetSpec()), State: mapScheduleState(in.GetState())}
}

// mapSchedules maps proto Schedules to the plain structs (nil-safe, fresh slice).
func mapSchedules(in []*mecatlv1.Schedule) []Schedule {
	out := make([]Schedule, 0, len(in))
	for _, s := range in {
		out = append(out, mapSchedule(s))
	}
	return out
}

// mapScheduleSpec maps a proto ScheduleSpec to the plain struct (nil-safe).
func mapScheduleSpec(in *mecatlv1.ScheduleSpec) ScheduleSpec {
	if in == nil {
		return ScheduleSpec{}
	}
	out := ScheduleSpec{
		Name:      in.GetName(),
		Prompt:    in.GetPrompt(),
		Profile:   in.GetProfile(),
		Mode:      modeStringFromProto(in.GetMode()),
		Mutating:  in.GetMutating(),
		MaxFires:  in.GetMaxFires(),
		Misfire:   misfireStringFromProto(in.GetMisfire()),
		Singleton: in.GetSingleton(),
		Timezone:  in.GetTimezone(),
	}
	if t := in.GetTrigger(); t != nil {
		out.Trigger = ScheduleTrigger{Cron: t.GetCron()}
		if ts := t.GetOneShot(); ts != nil {
			out.Trigger.OneShot = ts.AsTime()
		}
	}
	if sel := in.GetSelector(); sel != nil {
		out.Selector = ScheduleSelector{ProviderID: sel.GetProviderId(), ModelID: sel.GetModelId()}
	}
	if ca := in.GetCreatedAt(); ca != nil {
		out.CreatedAt = ca.AsTime()
	}
	if d := in.GetFireTimeout(); d != nil {
		out.FireTimeout = d.AsDuration()
	}
	return out
}

// mapScheduleState maps a proto ScheduleState to the plain struct (nil-safe).
func mapScheduleState(in *mecatlv1.ScheduleState) ScheduleState {
	if in == nil {
		return ScheduleState{}
	}
	out := ScheduleState{
		FireCount:         in.GetFireCount(),
		Enabled:           in.GetEnabled(),
		DeletionPending:   in.GetDeletionPending(),
		LastFireSessionID: in.GetLastFireSessionId(),
	}
	if ts := in.GetNextFireAt(); ts != nil {
		out.NextFireAt = ts.AsTime()
	}
	if ts := in.GetLastFireAt(); ts != nil {
		out.LastFireAt = ts.AsTime()
	}
	if ts := in.GetLastFireStartedAt(); ts != nil {
		out.LastFireStartedAt = ts.AsTime()
	}
	if ts := in.GetLastFireProgressAt(); ts != nil {
		out.LastFireProgressAt = ts.AsTime()
	}
	if ts := in.GetFireDeadline(); ts != nil {
		out.FireDeadline = ts.AsTime()
	}
	return out
}

// mapFire maps a proto ScheduleFire to the plain struct (nil-safe).
func mapFire(in *mecatlv1.ScheduleFire) ScheduleFire {
	if in == nil {
		return ScheduleFire{}
	}
	out := ScheduleFire{
		ID:           in.GetId(),
		ScheduleName: in.GetScheduleName(),
		SessionID:    in.GetSessionId(),
		Stop:         in.GetStop(),
		Err:          in.GetErr(),
	}
	if ts := in.GetFiredAt(); ts != nil {
		out.FiredAt = ts.AsTime()
	}
	if ts := in.GetStartedAt(); ts != nil {
		out.StartedAt = ts.AsTime()
	}
	if ts := in.GetProgressAt(); ts != nil {
		out.ProgressAt = ts.AsTime()
	}
	if ts := in.GetDeadline(); ts != nil {
		out.Deadline = ts.AsTime()
	}
	return out
}

// mapFires maps proto ScheduleFires to the plain structs (nil-safe, fresh slice).
func mapFires(in []*mecatlv1.ScheduleFire) []ScheduleFire {
	out := make([]ScheduleFire, 0, len(in))
	for _, f := range in {
		out = append(out, mapFire(f))
	}
	return out
}

// scheduleSpecToProto maps the plain ScheduleSpec to its proto projection — the
// ONE proto-build point for Create. Trigger is set when either Cron or OneShot is
// non-zero; Selector when either id is non-empty; CreatedAt when non-zero (the
// create-seam stamps it itself, so a caller need not set it). Parts are omitted
// for v1 (see ScheduleSpec doc-comment).
func scheduleSpecToProto(in ScheduleSpec) *mecatlv1.ScheduleSpec {
	out := &mecatlv1.ScheduleSpec{
		Name:      in.Name,
		Prompt:    in.Prompt,
		Profile:   in.Profile,
		Mode:      modeToProtoFromString(in.Mode),
		Mutating:  in.Mutating,
		MaxFires:  in.MaxFires,
		Misfire:   misfireToProtoFromString(in.Misfire),
		Singleton: in.Singleton,
		Timezone:  in.Timezone,
	}
	if in.Trigger.Cron != "" || !in.Trigger.OneShot.IsZero() {
		out.Trigger = &mecatlv1.TriggerSpec{Cron: in.Trigger.Cron}
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
	if in.FireTimeout > 0 {
		out.FireTimeout = durationpb.New(in.FireTimeout)
	}
	return out
}

// misfireStringFromProto maps the proto MisfirePolicy enum to the canonical
// string ("fire_once_now" / "skip"), defaulting UNSPECIFIED to the zero value's
// default (fire_once_now).
func misfireStringFromProto(m mecatlv1.MisfirePolicy) string {
	switch m {
	case mecatlv1.MisfirePolicy_MISFIRE_SKIP:
		return "skip"
	default:
		return "fire_once_now"
	}
}

// misfireToProtoFromString maps the canonical string to the proto enum. An
// unknown value defaults to FIRE_ONCE_NOW (the wire default).
func misfireToProtoFromString(s string) mecatlv1.MisfirePolicy {
	switch s {
	case "skip":
		return mecatlv1.MisfirePolicy_MISFIRE_SKIP
	default:
		return mecatlv1.MisfirePolicy_MISFIRE_FIRE_ONCE_NOW
	}
}

// modeStringFromProto maps the proto PermissionMode enum to the canonical UI
// string, reusing ModeString (the harness-mode discipline). Unspecified defaults
// to "default".
func modeStringFromProto(m mecatlv1.PermissionMode) string {
	return ModeString(m)
}

// modeToProtoFromString maps a UI mode string to the proto enum, reusing
// ModeFromString. Unknown/empty maps to UNSPECIFIED (the server defaults that to
// DEFAULT).
func modeToProtoFromString(s string) mecatlv1.PermissionMode {
	return ModeFromString(s)
}
