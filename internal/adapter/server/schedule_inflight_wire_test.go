package server_test

import (
	"context"
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/durationpb"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

// schedule_inflight_wire_test.go pins issue #386: the in-flight scheduled-fire
// state model (LastFireStartedAt/LastFireProgressAt/FireDeadline on the state;
// StartedAt/ProgressAt/Deadline on the fire; FireTimeout on the spec) projects
// through the gRPC + HTTP schedule surface — gRPC GetSchedule/ListFires carry
// the new fields, the HTTP surface shares the SAME mappers so it agrees, and a
// CLAIMED fire (LastFireSessionID == "pending", no run started) projects the
// pending sentinel on the wire. The mappers (scheduleStateToProto/
// scheduleFireToProto/scheduleSpecToProto/protoToScheduleSpec) are the ONE
// projection path both transports share.

// TestScheduleInFlightWire_GrpcGetScheduleProjectsInFlightFields pins that a
// GetSchedule after a RecordFireStart carries the in-flight state fields +
// FireTimeout on the wire.
func TestScheduleInFlightWire_GrpcGetScheduleProjectsInFlightFields(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	svc, schedStore := newScheduleService(t, now)
	ctx := context.Background()
	gsrv := server.NewScheduleServer(svc)

	// Create with a FireTimeout via the gRPC surface (so the spec-side round-trip
	// — protoToScheduleSpec — is exercised too).
	timeout := 45 * time.Minute
	if _, err := gsrv.CreateSchedule(ctx, &mecatlv1.CreateScheduleRequest{Spec: &mecatlv1.ScheduleSpec{
		Name:        "inflight",
		Prompt:      "run",
		Mutating:    true,
		Trigger:     &mecatlv1.TriggerSpec{Cron: "0 9 * * *"},
		FireTimeout: durationpb.New(timeout),
	}}); err != nil {
		t.Fatalf("CreateSchedule: %v", err)
	}

	// Claim (sets the pending sentinel + advances NextFireAt), then RecordFireStart
	// to drive the in-flight state onto the store (LastFireStartedAt/ProgressAt/
	// FireDeadline on the state + an in-flight fire record).
	start := now.Add(time.Second)
	deadline := start.Add(timeout)
	if _, err := schedStore.ClaimNow(ctx, "inflight", now, now.Add(time.Hour)); err != nil {
		t.Fatalf("ClaimNow: %v", err)
	}
	if err := schedStore.RecordFireStart(ctx, "inflight", port.ScheduleFire{
		ID:           "fire-inflight",
		ScheduleName: "inflight",
		SessionID:    "sess-inflight",
		FiredAt:      now,
		StartedAt:    start,
		Deadline:     deadline,
	}); err != nil {
		t.Fatalf("RecordFireStart: %v", err)
	}
	// Advance progress so LastFireProgressAt is non-zero + the fire record's
	// ProgressAt is set.
	progress := start.Add(20 * time.Second)
	if err := schedStore.RecordFireProgress(ctx, "inflight", "fire-inflight", progress); err != nil {
		t.Fatalf("RecordFireProgress: %v", err)
	}

	resp, err := gsrv.GetSchedule(ctx, &mecatlv1.GetScheduleRequest{Name: "inflight"})
	if err != nil {
		t.Fatalf("GetSchedule: %v", err)
	}
	st := resp.GetSchedule().GetState()
	if got := st.GetLastFireStartedAt(); got == nil {
		t.Fatal("GetSchedule state.last_fire_started_at = nil, want a timestamp (the in-flight state must project on the wire)")
	} else if !got.AsTime().Equal(start.UTC()) {
		t.Errorf("state.last_fire_started_at = %v, want %v", got.AsTime(), start.UTC())
	}
	if got := st.GetLastFireProgressAt(); got == nil {
		t.Fatal("GetSchedule state.last_fire_progress_at = nil, want a timestamp")
	} else if !got.AsTime().Equal(progress.UTC()) {
		t.Errorf("state.last_fire_progress_at = %v, want %v", got.AsTime(), progress.UTC())
	}
	if got := st.GetFireDeadline(); got == nil {
		t.Fatal("GetSchedule state.fire_deadline = nil, want a timestamp")
	} else if !got.AsTime().Equal(deadline.UTC()) {
		t.Errorf("state.fire_deadline = %v, want %v", got.AsTime(), deadline.UTC())
	}
	// The FireTimeout round-trips through the spec (protoToScheduleSpec on the
	// create, scheduleSpecToProto on the read — both mapper halves).
	if got := resp.GetSchedule().GetSpec().GetFireTimeout(); got == nil {
		t.Fatal("GetSchedule spec.fire_timeout = nil, want the Duration (round-tripped through both mapper halves)")
	} else if got.AsDuration() != timeout {
		t.Errorf("spec.fire_timeout = %v, want %v", got.AsDuration(), timeout)
	}
}

// TestScheduleInFlightWire_GrpcListFiresProjectsInFlightFire pins that an
// in-flight fire (Stop empty, StartedAt set) projects its started/progress/
// deadline fields on the wire via ListFires, and a terminal fire leaves them
// absent.
func TestScheduleInFlightWire_GrpcListFiresProjectsInFlightFire(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	svc, schedStore := newScheduleService(t, now)
	ctx := context.Background()
	gsrv := server.NewScheduleServer(svc)

	if _, err := gsrv.CreateSchedule(ctx, &mecatlv1.CreateScheduleRequest{Spec: &mecatlv1.ScheduleSpec{
		Name: "inflight", Prompt: "run", Mutating: true,
		Trigger: &mecatlv1.TriggerSpec{Cron: "0 9 * * *"},
	}}); err != nil {
		t.Fatalf("CreateSchedule: %v", err)
	}
	start := now.Add(time.Second)
	deadline := start.Add(30 * time.Minute)
	if _, err := schedStore.ClaimNow(ctx, "inflight", now, now.Add(time.Hour)); err != nil {
		t.Fatalf("ClaimNow: %v", err)
	}
	if err := schedStore.RecordFireStart(ctx, "inflight", port.ScheduleFire{
		ID: "fire-inflight", ScheduleName: "inflight", SessionID: "sess-inflight",
		FiredAt: now, StartedAt: start, Deadline: deadline,
	}); err != nil {
		t.Fatalf("RecordFireStart: %v", err)
	}
	// Advance progress so the in-flight fire record carries ProgressAt on the
	// wire (RecordFireStart seeds the STATE's LastFireProgressAt but NOT the
	// fire record's ProgressAt — RecordFireProgress is what writes the latter).
	progress := start.Add(20 * time.Second)
	if err := schedStore.RecordFireProgress(ctx, "inflight", "fire-inflight", progress); err != nil {
		t.Fatalf("RecordFireProgress: %v", err)
	}

	// A terminal fire (RecordFire) — its started/progress/deadline must be absent
	// on the wire (zero→nil convention).
	if err := schedStore.RecordFire(ctx, port.ScheduleFire{
		ID: "fire-terminal", ScheduleName: "inflight", SessionID: "sess-terminal",
		FiredAt: now, Stop: "end_turn",
	}); err != nil {
		t.Fatalf("RecordFire: %v", err)
	}

	resp, err := gsrv.ListFires(ctx, &mecatlv1.ListFiresRequest{ScheduleName: "inflight"})
	if err != nil {
		t.Fatalf("ListFires: %v", err)
	}
	fires := resp.GetFires()
	if len(fires) != 2 {
		t.Fatalf("ListFires = %d fires, want 2 (one in-flight + one terminal)", len(fires))
	}
	inflight, terminal := (*mecatlv1.ScheduleFire)(nil), (*mecatlv1.ScheduleFire)(nil)
	for _, f := range fires {
		switch f.GetId() {
		case "fire-inflight":
			inflight = f
		case "fire-terminal":
			terminal = f
		}
	}
	if inflight == nil || terminal == nil {
		t.Fatalf("ListFires missing a fire: inflight=%v terminal=%v", inflight, terminal)
	}
	if got := inflight.GetStop(); got != "" {
		t.Errorf("in-flight fire stop = %q, want empty (in-flight)", got)
	}
	if got := inflight.GetStartedAt(); got == nil {
		t.Error("in-flight fire started_at = nil, want a timestamp")
	} else if !got.AsTime().Equal(start.UTC()) {
		t.Errorf("in-flight fire started_at = %v, want %v", got.AsTime(), start.UTC())
	}
	if got := inflight.GetDeadline(); got == nil {
		t.Error("in-flight fire deadline = nil, want a timestamp")
	} else if !got.AsTime().Equal(deadline.UTC()) {
		t.Errorf("in-flight fire deadline = %v, want %v", got.AsTime(), deadline.UTC())
	}
	if got := inflight.GetProgressAt(); got == nil {
		t.Error("in-flight fire progress_at = nil, want a timestamp (set by RecordFireProgress)")
	} else if !got.AsTime().Equal(progress.UTC()) {
		t.Errorf("in-flight fire progress_at = %v, want %v", got.AsTime(), progress.UTC())
	}
	// Terminal fire: started/progress/deadline absent (zero→nil).
	if got := terminal.GetStartedAt(); got != nil {
		t.Errorf("terminal fire started_at = %v, want nil (zero→nil on a terminal fire)", got)
	}
	if got := terminal.GetProgressAt(); got != nil {
		t.Errorf("terminal fire progress_at = %v, want nil", got)
	}
	if got := terminal.GetDeadline(); got != nil {
		t.Errorf("terminal fire deadline = %v, want nil", got)
	}
}

// TestScheduleInFlightWire_ClaimedPendingProjects pins the CLAIMED state (Claim
// happened, RecordFireStart did not — LastFireSessionID == "pending", no
// in-flight fields) projects the pending sentinel on the wire.
func TestScheduleInFlightWire_ClaimedPendingProjects(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	svc, schedStore := newScheduleService(t, now)
	ctx := context.Background()
	gsrv := server.NewScheduleServer(svc)

	if _, err := gsrv.CreateSchedule(ctx, &mecatlv1.CreateScheduleRequest{Spec: &mecatlv1.ScheduleSpec{
		Name: "claimed", Prompt: "run", Mutating: true,
		Trigger: &mecatlv1.TriggerSpec{Cron: "0 9 * * *"},
	}}); err != nil {
		t.Fatalf("CreateSchedule: %v", err)
	}
	// Claim (stamps "pending"), do NOT RecordFireStart.
	if _, err := schedStore.ClaimNow(ctx, "claimed", now, now.Add(time.Hour)); err != nil {
		t.Fatalf("ClaimNow: %v", err)
	}
	resp, err := gsrv.GetSchedule(ctx, &mecatlv1.GetScheduleRequest{Name: "claimed"})
	if err != nil {
		t.Fatalf("GetSchedule: %v", err)
	}
	st := resp.GetSchedule().GetState()
	if got := st.GetLastFireSessionId(); got != "pending" {
		t.Errorf("state.last_fire_session_id = %q, want \"pending\" (the claim sentinel must project on the wire)", got)
	}
	// No in-flight fields set yet.
	if st.GetLastFireStartedAt() != nil {
		t.Errorf("state.last_fire_started_at = %v, want nil (RecordFireStart has not run)", st.GetLastFireStartedAt())
	}
	if st.GetFireDeadline() != nil {
		t.Errorf("state.fire_deadline = %v, want nil (RecordFireStart has not run)", st.GetFireDeadline())
	}
}
