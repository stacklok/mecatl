package client

import (
	"context"
	"errors"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/timestamppb"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
)

// fakeScheduleClient is a scripted ScheduleServiceClient for the schedule wrapper
// tests. It embeds the interface (so it satisfies it without spelling out every
// RPC) and overrides only the ones under test.
type fakeScheduleClient struct {
	mecatlv1.ScheduleServiceClient

	listResp   *mecatlv1.ListSchedulesResponse
	getResp    *mecatlv1.GetScheduleResponse
	createResp *mecatlv1.CreateScheduleResponse
	deleteErr  error
	fireResp   *mecatlv1.FireNowResponse
	fireErr    error
	pauseErr   error
	resumeErr  error
	firesResp  *mecatlv1.ListFiresResponse
	listErr    error
	getErr     error
	createErr  error
	firesErr   error

	lastCreate *mecatlv1.CreateScheduleRequest
	lastFire   *mecatlv1.FireNowRequest
	lastFires  *mecatlv1.ListFiresRequest
}

func (f *fakeScheduleClient) ListSchedules(_ context.Context, _ *mecatlv1.ListSchedulesRequest, _ ...grpc.CallOption) (*mecatlv1.ListSchedulesResponse, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.listResp, nil
}

func (f *fakeScheduleClient) GetSchedule(_ context.Context, _ *mecatlv1.GetScheduleRequest, _ ...grpc.CallOption) (*mecatlv1.GetScheduleResponse, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	return f.getResp, nil
}

func (f *fakeScheduleClient) CreateSchedule(_ context.Context, in *mecatlv1.CreateScheduleRequest, _ ...grpc.CallOption) (*mecatlv1.CreateScheduleResponse, error) {
	f.lastCreate = in
	if f.createErr != nil {
		return nil, f.createErr
	}
	return f.createResp, nil
}

func (f *fakeScheduleClient) DeleteSchedule(_ context.Context, _ *mecatlv1.DeleteScheduleRequest, _ ...grpc.CallOption) (*mecatlv1.DeleteScheduleResponse, error) {
	if f.deleteErr != nil {
		return nil, f.deleteErr
	}
	return &mecatlv1.DeleteScheduleResponse{}, nil
}

func (f *fakeScheduleClient) FireNow(_ context.Context, in *mecatlv1.FireNowRequest, _ ...grpc.CallOption) (*mecatlv1.FireNowResponse, error) {
	f.lastFire = in
	if f.fireErr != nil {
		return nil, f.fireErr
	}
	return f.fireResp, nil
}

func (f *fakeScheduleClient) PauseSchedule(_ context.Context, _ *mecatlv1.PauseScheduleRequest, _ ...grpc.CallOption) (*mecatlv1.PauseScheduleResponse, error) {
	if f.pauseErr != nil {
		return nil, f.pauseErr
	}
	return &mecatlv1.PauseScheduleResponse{}, nil
}

func (f *fakeScheduleClient) ResumeSchedule(_ context.Context, _ *mecatlv1.ResumeScheduleRequest, _ ...grpc.CallOption) (*mecatlv1.ResumeScheduleResponse, error) {
	if f.resumeErr != nil {
		return nil, f.resumeErr
	}
	return &mecatlv1.ResumeScheduleResponse{}, nil
}

func (f *fakeScheduleClient) ListFires(_ context.Context, in *mecatlv1.ListFiresRequest, _ ...grpc.CallOption) (*mecatlv1.ListFiresResponse, error) {
	f.lastFires = in
	if f.firesErr != nil {
		return nil, f.firesErr
	}
	return f.firesResp, nil
}

// newScheduleFakeClient wraps a fakeScheduleClient in a *Client so the wrappers
// exercise the real scheduleSvc call path.
func newScheduleFakeClient(svc mecatlv1.ScheduleServiceClient) *Client {
	return &Client{scheduleSvc: svc}
}

func sampleProtoSchedule(name string) *mecatlv1.Schedule {
	oneShot := time.Date(2026, 7, 6, 14, 0, 0, 0, time.UTC)
	return &mecatlv1.Schedule{
		Spec: &mecatlv1.ScheduleSpec{
			Name:      name,
			Prompt:    "run the tests",
			Trigger:   &mecatlv1.TriggerSpec{OneShot: timestamppb.New(oneShot)},
			Selector:  &mecatlv1.ScheduleProviderSelector{ProviderId: "openai", ModelId: "gpt-5"},
			Profile:   "default",
			Workspace: "/repo",
			Mode:      mecatlv1.PermissionMode_PERMISSION_MODE_DEFAULT,
			Mutating:  true,
			MaxFires:  3,
			Misfire:   mecatlv1.MisfirePolicy_MISFIRE_SKIP,
			Singleton: true,
			Timezone:  "UTC",
			CreatedAt: timestamppb.New(oneShot.Add(-time.Hour)),
		},
		State: &mecatlv1.ScheduleState{
			NextFireAt:        timestamppb.New(oneShot),
			LastFireAt:        timestamppb.New(oneShot.Add(-time.Hour)),
			FireCount:         2,
			Enabled:           true,
			LastFireSessionId: "sess-fire-1",
		},
	}
}

func TestMapScheduleFieldsAndNilSafe(t *testing.T) {
	if got := mapSchedule(nil); got != (Schedule{}) {
		t.Fatalf("mapSchedule(nil) = %+v, want zero", got)
	}
	if got := mapSchedules(nil); len(got) != 0 {
		t.Fatalf("mapSchedules(nil) = %v, want empty", got)
	}
	if got := mapFire(nil); got != (ScheduleFire{}) {
		t.Fatalf("mapFire(nil) = %+v, want zero", got)
	}
	if got := mapFires(nil); len(got) != 0 {
		t.Fatalf("mapFires(nil) = %v, want empty", got)
	}

	s := mapSchedule(sampleProtoSchedule("nightly"))
	if s.Spec.Name != "nightly" || s.Spec.Prompt != "run the tests" {
		t.Fatalf("spec mismapped: %+v", s.Spec)
	}
	if s.Spec.Trigger.Cron != "" || s.Spec.Trigger.OneShot.IsZero() {
		t.Fatalf("trigger mismapped: %+v", s.Spec.Trigger)
	}
	if s.Spec.Selector.ProviderID != "openai" || s.Spec.Selector.ModelID != "gpt-5" {
		t.Fatalf("selector mismapped: %+v", s.Spec.Selector)
	}
	if s.Spec.Mode != "default" || s.Spec.Misfire != "skip" || !s.Spec.Mutating || !s.Spec.Singleton {
		t.Fatalf("spec scalar mismapped: %+v", s.Spec)
	}
	if s.Spec.MaxFires != 3 {
		t.Fatalf("max_fires = %d, want 3", s.Spec.MaxFires)
	}
	if s.State.FireCount != 2 || !s.State.Enabled || s.State.LastFireSessionID != "sess-fire-1" {
		t.Fatalf("state mismapped: %+v", s.State)
	}
	if s.State.NextFireAt.IsZero() || s.State.LastFireAt.IsZero() {
		t.Fatalf("state timestamps zero: %+v", s.State)
	}
}

func TestListSchedulesMapping(t *testing.T) {
	fake := &fakeScheduleClient{listResp: &mecatlv1.ListSchedulesResponse{Schedules: []*mecatlv1.Schedule{
		sampleProtoSchedule("a"),
		sampleProtoSchedule("b"),
	}}}
	cl := newScheduleFakeClient(fake)

	got, err := cl.ListSchedules(context.Background())
	if err != nil {
		t.Fatalf("ListSchedules: %v", err)
	}
	if len(got) != 2 || got[0].Spec.Name != "a" || got[1].Spec.Name != "b" {
		t.Fatalf("schedules = %+v", got)
	}
}

func TestListSchedulesError(t *testing.T) {
	fake := &fakeScheduleClient{listErr: errors.New("boom")}
	cl := newScheduleFakeClient(fake)
	if _, err := cl.ListSchedules(context.Background()); err == nil {
		t.Fatal("ListSchedules should surface the error")
	}
}

func TestGetScheduleMapping(t *testing.T) {
	fake := &fakeScheduleClient{getResp: &mecatlv1.GetScheduleResponse{Schedule: sampleProtoSchedule("only")}}
	cl := newScheduleFakeClient(fake)

	s, err := cl.GetSchedule(context.Background(), "only")
	if err != nil {
		t.Fatalf("GetSchedule: %v", err)
	}
	if s.Spec.Name != "only" {
		t.Fatalf("name = %q, want only", s.Spec.Name)
	}
}

func TestCreateScheduleBuildsProto(t *testing.T) {
	spec := ScheduleSpec{
		Name:     "daily",
		Prompt:   "ship it",
		Trigger:  ScheduleTrigger{Cron: "*/5 * * * *"},
		Mode:     "plan",
		Misfire:  "skip",
		MaxFires: 7,
		Selector: ScheduleSelector{ProviderID: "anthropic"},
	}
	fake := &fakeScheduleClient{createResp: &mecatlv1.CreateScheduleResponse{Schedule: sampleProtoSchedule("daily")}}
	cl := newScheduleFakeClient(fake)

	if _, err := cl.CreateSchedule(context.Background(), spec); err != nil {
		t.Fatalf("CreateSchedule: %v", err)
	}
	if fake.lastCreate == nil || fake.lastCreate.GetSpec() == nil {
		t.Fatal("create request not sent")
	}
	got := fake.lastCreate.GetSpec()
	if got.GetName() != "daily" || got.GetPrompt() != "ship it" {
		t.Fatalf("spec proto mismapped: %+v", got)
	}
	if got.GetTrigger().GetCron() != "*/5 * * * *" || got.GetTrigger().GetOneShot() != nil {
		t.Fatalf("trigger proto mismapped: %+v", got.GetTrigger())
	}
	if got.GetMode() != mecatlv1.PermissionMode_PERMISSION_MODE_PLAN {
		t.Fatalf("mode proto = %v, want PLAN", got.GetMode())
	}
	if got.GetMisfire() != mecatlv1.MisfirePolicy_MISFIRE_SKIP {
		t.Fatalf("misfire proto = %v, want SKIP", got.GetMisfire())
	}
	if got.GetMaxFires() != 7 {
		t.Fatalf("max_fires proto = %d, want 7", got.GetMaxFires())
	}
	if got.GetSelector().GetProviderId() != "anthropic" {
		t.Fatalf("selector proto = %+v", got.GetSelector())
	}
}

func TestFireNowReturnsIDs(t *testing.T) {
	fake := &fakeScheduleClient{fireResp: &mecatlv1.FireNowResponse{FireId: "fire-1", SessionId: "sess-fire-1"}}
	cl := newScheduleFakeClient(fake)

	fireID, sessID, err := cl.FireNow(context.Background(), "nightly")
	if err != nil {
		t.Fatalf("FireNow: %v", err)
	}
	if fireID != "fire-1" || sessID != "sess-fire-1" {
		t.Fatalf("ids = %q/%q, want fire-1/sess-fire-1", fireID, sessID)
	}
	if fake.lastFire.GetName() != "nightly" {
		t.Fatalf("request name = %q, want nightly", fake.lastFire.GetName())
	}
}

func TestFireNowError(t *testing.T) {
	fake := &fakeScheduleClient{fireErr: errors.New("scheduler down")}
	cl := newScheduleFakeClient(fake)
	if _, _, err := cl.FireNow(context.Background(), "x"); err == nil {
		t.Fatal("FireNow should surface the error")
	}
}

func TestPauseResumeDelete(t *testing.T) {
	ctx := context.Background()
	t.Run("pause", func(t *testing.T) {
		fake := &fakeScheduleClient{pauseErr: errors.New("nope")}
		cl := newScheduleFakeClient(fake)
		if err := cl.PauseSchedule(ctx, "x"); err == nil {
			t.Fatal("PauseSchedule should surface the error")
		}
	})
	t.Run("resume", func(t *testing.T) {
		fake := &fakeScheduleClient{resumeErr: errors.New("nope")}
		cl := newScheduleFakeClient(fake)
		if err := cl.ResumeSchedule(ctx, "x"); err == nil {
			t.Fatal("ResumeSchedule should surface the error")
		}
	})
	t.Run("delete", func(t *testing.T) {
		fake := &fakeScheduleClient{deleteErr: errors.New("nope")}
		cl := newScheduleFakeClient(fake)
		if err := cl.DeleteSchedule(ctx, "x"); err == nil {
			t.Fatal("DeleteSchedule should surface the error")
		}
	})
}

func TestListFiresMapping(t *testing.T) {
	fake := &fakeScheduleClient{firesResp: &mecatlv1.ListFiresResponse{Fires: []*mecatlv1.ScheduleFire{
		{Id: "f1", ScheduleName: "nightly", SessionId: "s1", Stop: "end_turn", Err: ""},
		{Id: "f2", ScheduleName: "nightly", SessionId: "s2", Stop: "error", Err: "boom"},
	}}}
	cl := newScheduleFakeClient(fake)

	fires, err := cl.ListFires(context.Background(), "nightly")
	if err != nil {
		t.Fatalf("ListFires: %v", err)
	}
	if fake.lastFires.GetScheduleName() != "nightly" {
		t.Fatalf("request schedule_name = %q, want nightly", fake.lastFires.GetScheduleName())
	}
	if len(fires) != 2 || fires[0].ID != "f1" || fires[1].Err != "boom" {
		t.Fatalf("fires = %+v", fires)
	}
}

// TestScheduleCmdsAssertMsgTypes pins that each tea.Cmd constructor yields the
// documented msg type (success + error arms), against a fake ScheduleLister.
type stubScheduleLister struct {
	schedules []Schedule
	sched     Schedule
	fires     []ScheduleFire
	fireID    string
	err       error
}

func (s stubScheduleLister) ListSchedules(_ context.Context) ([]Schedule, error) {
	return s.schedules, s.err
}
func (s stubScheduleLister) GetSchedule(_ context.Context, _ string) (Schedule, error) {
	return s.sched, s.err
}
func (s stubScheduleLister) CreateSchedule(_ context.Context, _ ScheduleSpec) (Schedule, error) {
	return s.sched, s.err
}
func (s stubScheduleLister) DeleteSchedule(_ context.Context, _ string) error { return s.err }
func (s stubScheduleLister) FireNow(_ context.Context, _ string) (string, string, error) {
	return s.fireID, s.fireID, s.err
}
func (s stubScheduleLister) PauseSchedule(_ context.Context, _ string) error  { return s.err }
func (s stubScheduleLister) ResumeSchedule(_ context.Context, _ string) error { return s.err }
func (s stubScheduleLister) ListFires(_ context.Context, _ string) ([]ScheduleFire, error) {
	return s.fires, s.err
}

func TestScheduleCmdsSuccessMsgs(t *testing.T) {
	ctx := context.Background()
	stub := stubScheduleLister{
		schedules: []Schedule{{Spec: ScheduleSpec{Name: "a"}}},
		sched:     Schedule{Spec: ScheduleSpec{Name: "a"}},
		fires:     []ScheduleFire{{ID: "f1"}},
		fireID:    "fire-1",
	}
	if m, ok := ListSchedulesCmd(ctx, stub)().(SchedulesMsg); !ok || m.Err != nil || len(m.Schedules) != 1 {
		t.Fatalf("ListSchedulesCmd msg = %+v", m)
	}
	if m, ok := GetScheduleCmd(ctx, stub, "a")().(ScheduleMsg); !ok || m.Err != nil || m.Schedule.Spec.Name != "a" {
		t.Fatalf("GetScheduleCmd msg = %+v", m)
	}
	if m, ok := CreateScheduleCmd(ctx, stub, ScheduleSpec{Name: "a"})().(ScheduleMsg); !ok || m.Err != nil {
		t.Fatalf("CreateScheduleCmd msg = %+v", m)
	}
	if m, ok := DeleteScheduleCmd(ctx, stub, "a")().(ScheduleActionMsg); !ok || m.Err != nil || m.Action != "deleted" {
		t.Fatalf("DeleteScheduleCmd msg = %+v", m)
	}
	if m, ok := FireNowCmd(ctx, stub, "a")().(ScheduleActionMsg); !ok || m.Err != nil || m.Action != "fired" || m.FireID != "fire-1" {
		t.Fatalf("FireNowCmd msg = %+v", m)
	}
	if m, ok := PauseScheduleCmd(ctx, stub, "a")().(ScheduleActionMsg); !ok || m.Err != nil || m.Action != "paused" {
		t.Fatalf("PauseScheduleCmd msg = %+v", m)
	}
	if m, ok := ResumeScheduleCmd(ctx, stub, "a")().(ScheduleActionMsg); !ok || m.Err != nil || m.Action != "resumed" {
		t.Fatalf("ResumeScheduleCmd msg = %+v", m)
	}
	if m, ok := ListFiresCmd(ctx, stub, "a")().(ScheduleFiresMsg); !ok || m.Err != nil || len(m.Fires) != 1 {
		t.Fatalf("ListFiresCmd msg = %+v", m)
	}
}

func TestScheduleCmdsErrorMsgs(t *testing.T) {
	ctx := context.Background()
	stub := stubScheduleLister{err: errors.New("boom")}
	if m, ok := ListSchedulesCmd(ctx, stub)().(SchedulesMsg); !ok || m.Err == nil {
		t.Fatalf("ListSchedulesCmd error msg = %+v", m)
	}
	if m, ok := GetScheduleCmd(ctx, stub, "a")().(ScheduleMsg); !ok || m.Err == nil {
		t.Fatalf("GetScheduleCmd error msg = %+v", m)
	}
	if m, ok := CreateScheduleCmd(ctx, stub, ScheduleSpec{})().(ScheduleMsg); !ok || m.Err == nil {
		t.Fatalf("CreateScheduleCmd error msg = %+v", m)
	}
	if m, ok := DeleteScheduleCmd(ctx, stub, "a")().(ScheduleActionMsg); !ok || m.Err == nil {
		t.Fatalf("DeleteScheduleCmd error msg = %+v", m)
	}
	if m, ok := FireNowCmd(ctx, stub, "a")().(ScheduleActionMsg); !ok || m.Err == nil {
		t.Fatalf("FireNowCmd error msg = %+v", m)
	}
	if m, ok := PauseScheduleCmd(ctx, stub, "a")().(ScheduleActionMsg); !ok || m.Err == nil {
		t.Fatalf("PauseScheduleCmd error msg = %+v", m)
	}
	if m, ok := ResumeScheduleCmd(ctx, stub, "a")().(ScheduleActionMsg); !ok || m.Err == nil {
		t.Fatalf("ResumeScheduleCmd error msg = %+v", m)
	}
	if m, ok := ListFiresCmd(ctx, stub, "a")().(ScheduleFiresMsg); !ok || m.Err == nil {
		t.Fatalf("ListFiresCmd error msg = %+v", m)
	}
}
