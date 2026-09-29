package grpcdriver

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	driverv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/driver/v1"
	"github.com/stacklok/mecatl/engine/adapter/memschedulestore"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

// newWiredScheduleStore returns a grpcdriver ScheduleStore client over a
// bufconn server wrapping a fresh memschedulestore, with BOTH the
// ScheduleStoreService and ScheduleOneShotReArmerService registered over the
// SAME memschedulestore instance (which implements both). The memschedulestore
// instance is returned so a test can drive the re-armer directly when needed.
func newWiredScheduleStore(t *testing.T) (*ScheduleStore, *memschedulestore.Store) {
	t.Helper()
	backend := memschedulestore.New()
	conn := dialBufconn(t, func(gs *grpc.Server) {
		driverv1.RegisterScheduleStoreServiceServer(gs, NewScheduleStoreServer(backend))
		driverv1.RegisterScheduleOneShotReArmerServiceServer(gs, NewScheduleOneShotReArmerServer(backend))
	})
	return NewScheduleStore(conn), backend
}

// sampleScheduleCron is a cron expression the tests use for recurring schedules
// (every minute, on the minute).
const sampleScheduleCron = "* * * * *"

// TestScheduleSaveLoadOverWire is the happy-path round trip over the wire: a
// schedule with a populated Spec+State (cron trigger, limits, Mode,
// timestamps, FireTimeout) crosses encode→wire→decode→store and back intact.
func TestScheduleSaveLoadOverWire(t *testing.T) {
	st, _ := newWiredScheduleStore(t)
	ctx := context.Background()
	now := time.Unix(1_700_000_000, 0)
	in := port.Schedule{
		Spec: port.ScheduleSpec{
			Name:           "cron-sched",
			Prompt:         "rotate the keys",
			Trigger:        port.TriggerSpec{Cron: sampleScheduleCron},
			Profile:        "no-fs",
			EnvironmentRef: session.EnvironmentRef{Kind: "remote", ID: "opaque-placement", Revision: "r7"},
			PlacementScope: "deployment-a",
			Mode:           session.ModeDefault,
			Limits:         session.Limits{MaxTurns: 5, MaxToolCalls: 20},
			Mutating:       true,
			MaxFires:       3,
			Misfire:        port.MisfireFireOnceNow,
			Singleton:      true,
			Timezone:       "UTC",
			CreatedAt:      now,
			FireTimeout:    5 * time.Minute,
		},
		State: port.ScheduleState{
			NextFireAt: now.Add(time.Minute),
			Enabled:    true,
		},
	}
	if err := st.Save(ctx, in); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := st.Load(ctx, in.Spec.Name)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Spec.Name != in.Spec.Name || got.Spec.Prompt != in.Spec.Prompt || got.Spec.Trigger.Cron != in.Spec.Trigger.Cron {
		t.Errorf("Load round trip Spec = %+v, want %+v", got.Spec, in.Spec)
	}
	if got.Spec.Profile != in.Spec.Profile || got.Spec.EnvironmentRef != in.Spec.EnvironmentRef || got.Spec.PlacementScope != in.Spec.PlacementScope || got.Spec.Mode != in.Spec.Mode {
		t.Errorf("Load round trip Spec detail = %+v, want %+v", got.Spec, in.Spec)
	}
	if got.Spec.MaxFires != in.Spec.MaxFires || got.Spec.Mutating != in.Spec.Mutating || got.Spec.Singleton != in.Spec.Singleton {
		t.Errorf("Load round trip Spec flags = %+v, want %+v", got.Spec, in.Spec)
	}
	if got.Spec.FireTimeout != in.Spec.FireTimeout {
		t.Errorf("Load round trip FireTimeout = %v, want %v", got.Spec.FireTimeout, in.Spec.FireTimeout)
	}
	if got.Spec.Limits.MaxTurns != in.Spec.Limits.MaxTurns || got.Spec.Limits.MaxToolCalls != in.Spec.Limits.MaxToolCalls {
		t.Errorf("Load round trip Limits = %+v, want %+v", got.Spec.Limits, in.Spec.Limits)
	}
	if !got.State.NextFireAt.Equal(in.State.NextFireAt) || !got.State.Enabled {
		t.Errorf("Load round trip State = %+v, want %+v", got.State, in.State)
	}
}

func TestOwnedScheduleUsesVersionedEnvelopeAndRoundTripsOwnership(t *testing.T) {
	st, _ := newWiredScheduleStore(t)
	in := port.Schedule{Spec: port.ScheduleSpec{Name: "owned", PlacementOwned: true}}
	if err := st.Save(t.Context(), in); err != nil {
		t.Fatalf("Save owned schedule: %v", err)
	}
	got, err := st.Load(t.Context(), in.Spec.Name)
	if err != nil {
		t.Fatalf("Load owned schedule: %v", err)
	}
	if !got.Spec.PlacementOwned {
		t.Fatal("owned placement bit was lost over the driver envelope")
	}
}

func TestScheduleEnvelopeRejectsOwnershipFormatMismatch(t *testing.T) {
	for _, tc := range []struct {
		name   string
		format string
		owned  bool
	}{
		{name: "v1 carrying owned placement", format: ScheduleFormat, owned: true},
		{name: "owned format missing ownership", format: ownedScheduleFormat, owned: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			payload, err := json.Marshal(port.Schedule{Spec: port.ScheduleSpec{Name: "mismatch", PlacementOwned: tc.owned}})
			if err != nil {
				t.Fatal(err)
			}
			record := &driverv1.ScheduleRecord{Format: tc.format, Payload: payload}
			if _, err := decodeScheduleEnvelope("mismatch", record); err == nil {
				t.Fatal("client accepted mismatched ownership envelope")
			}
			if _, err := validateScheduleRecord("mismatch", record); status.Code(err) != codes.InvalidArgument {
				t.Fatalf("server validation = %v, want InvalidArgument", err)
			}
		})
	}
}

type v1OnlyScheduleServer struct {
	driverv1.UnimplementedScheduleStoreServiceServer
	stored int
}

func (s *v1OnlyScheduleServer) SaveSchedule(_ context.Context, req *driverv1.SaveScheduleRequest) (*driverv1.SaveScheduleResponse, error) {
	if req.GetSchedule().GetFormat() != ScheduleFormat {
		return nil, status.Errorf(codes.InvalidArgument, "unknown schedule format %q", req.GetSchedule().GetFormat())
	}
	s.stored++
	return &driverv1.SaveScheduleResponse{}, nil
}

func TestV1OnlyServerRejectsOwnedScheduleBeforeStorage(t *testing.T) {
	oldServer := &v1OnlyScheduleServer{}
	conn := dialBufconn(t, func(gs *grpc.Server) {
		driverv1.RegisterScheduleStoreServiceServer(gs, oldServer)
	})
	st := NewScheduleStore(conn)
	err := st.Save(t.Context(), port.Schedule{Spec: port.ScheduleSpec{Name: "owned", PlacementOwned: true}})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("Save owned schedule through v1-only server = %v, want InvalidArgument", err)
	}
	if oldServer.stored != 0 {
		t.Fatalf("v1-only server stored %d owned schedules, want 0", oldServer.stored)
	}
}

// TestScheduleSaveLoadOneShotOverWire is the happy-path round trip for a
// one-shot schedule (the OneShot trigger arm).
func TestScheduleSaveLoadOneShotOverWire(t *testing.T) {
	st, _ := newWiredScheduleStore(t)
	ctx := context.Background()
	fireAt := time.Unix(1_700_000_120, 0)
	in := port.Schedule{
		Spec: port.ScheduleSpec{
			Name:      "one-shot-sched",
			Prompt:    "once",
			Trigger:   port.TriggerSpec{OneShot: fireAt},
			Mutating:  false,
			CreatedAt: time.Unix(1_700_000_000, 0),
		},
		State: port.ScheduleState{NextFireAt: fireAt, Enabled: true},
	}
	if err := st.Save(ctx, in); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := st.Load(ctx, in.Spec.Name)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !got.Spec.Trigger.OneShot.Equal(fireAt) || got.Spec.Trigger.Cron != "" {
		t.Errorf("Load round trip Trigger = %+v, want one-shot at %v", got.Spec.Trigger, fireAt)
	}
}

// TestScheduleLoadMissWrapsSentinel pins the not-found mapping: a driver
// NOT_FOUND surfaces as port.ErrScheduleNotFound (wrapped), never an opaque
// infra error.
func TestScheduleLoadMissWrapsSentinel(t *testing.T) {
	st, _ := newWiredScheduleStore(t)
	_, err := st.Load(context.Background(), "missing-sched")
	if err == nil {
		t.Fatal("Load(missing) = nil error, want not-found")
	}
	if !errors.Is(err, port.ErrScheduleNotFound) {
		t.Errorf("Load(missing) error = %v, want errors.Is(_, port.ErrScheduleNotFound)", err)
	}
}

// misKeyedScheduleServer returns a schedule whose Spec.Name differs from the
// requested name — a mis-keyed driver (the sess.ID != id guard precedent).
type misKeyedScheduleServer struct {
	driverv1.UnimplementedScheduleStoreServiceServer
}

func (misKeyedScheduleServer) LoadSchedule(context.Context, *driverv1.LoadScheduleRequest) (*driverv1.LoadScheduleResponse, error) {
	other := port.Schedule{Spec: port.ScheduleSpec{Name: "DIFFERENT-name", Prompt: "x"}}
	payload, _ := json.Marshal(other)
	return &driverv1.LoadScheduleResponse{
		Schedule: &driverv1.ScheduleRecord{Format: ScheduleFormat, Payload: payload},
	}, nil
}

// TestScheduleLoadWrongNameGuardIsInfraError pins the mis-keyed-driver guard:
// a driver returning a schedule whose Spec.Name differs from the request is an
// infrastructure error, NOT not-found (the wrong-name guard, the sess.ID != id
// precedent). The client must not silently adopt a foreign schedule.
func TestScheduleLoadWrongNameGuardIsInfraError(t *testing.T) {
	conn := dialBufconn(t, func(gs *grpc.Server) {
		driverv1.RegisterScheduleStoreServiceServer(gs, misKeyedScheduleServer{})
	})
	st := NewScheduleStore(conn)
	_, err := st.Load(context.Background(), "requested-name")
	if err == nil {
		t.Fatal("Load(mis-keyed) = nil error, want an infra error")
	}
	if errors.Is(err, port.ErrScheduleNotFound) {
		t.Errorf("Load(mis-keyed) error = %v: must NEVER be ErrScheduleNotFound (it is a mis-keyed driver, not a miss)", err)
	}
	if want := "DIFFERENT"; !strings.Contains(err.Error(), want) {
		t.Errorf("Load(mis-keyed) error %q should name the foreign schedule", err)
	}
}

// unknownFormatScheduleServer returns a syntactically valid envelope under a
// format tag this client does not speak.
type unknownFormatScheduleServer struct {
	driverv1.UnimplementedScheduleStoreServiceServer
}

func (unknownFormatScheduleServer) LoadSchedule(_ context.Context, _ *driverv1.LoadScheduleRequest) (*driverv1.LoadScheduleResponse, error) {
	return &driverv1.LoadScheduleResponse{
		Schedule: &driverv1.ScheduleRecord{Format: "schedule-json/99", Payload: []byte(`{}`)},
	}, nil
}

// TestScheduleLoadUnknownFormatIsInfraError pins the format-versioning rule: a
// schedule whose envelope carries an unknown format tag is an INFRASTRUCTURE
// error naming the format — never ErrScheduleNotFound.
func TestScheduleLoadUnknownFormatIsInfraError(t *testing.T) {
	conn := dialBufconn(t, func(gs *grpc.Server) {
		driverv1.RegisterScheduleStoreServiceServer(gs, unknownFormatScheduleServer{})
	})
	st := NewScheduleStore(conn)
	_, err := st.Load(context.Background(), "any-name")
	if err == nil {
		t.Fatal("Load(unknown format) = nil error, want an infra error")
	}
	if errors.Is(err, port.ErrScheduleNotFound) {
		t.Errorf("Load(unknown format) error = %v: must NEVER be ErrScheduleNotFound", err)
	}
	if want := "schedule-json/99"; !strings.Contains(err.Error(), want) {
		t.Errorf("Load(unknown format) error %q does not name the offending format %q", err, want)
	}
}

// TestScheduleServerWrapperSaveRejectsBadEnvelope pins the server wrapper's
// SaveSchedule pre-validation: every malformed envelope shape — blank name,
// nil schedule, unknown format, empty payload, undecodable payload, and a
// top-level name that disagrees with the payload's Spec.Name — is
// INVALID_ARGUMENT (never stored, never Internal). Invoked directly on the
// wrapper: the validation is the wrapper's own, no wire needed.
func TestScheduleServerWrapperSaveRejectsBadEnvelope(t *testing.T) {
	srv := NewScheduleStoreServer(memschedulestore.New())
	ctx := context.Background()

	goodPayload, _ := json.Marshal(port.Schedule{
		Spec: port.ScheduleSpec{Name: "name-1", Prompt: "p", Trigger: port.TriggerSpec{Cron: sampleScheduleCron}},
	})

	cases := []struct {
		name string
		req  *driverv1.SaveScheduleRequest
	}{
		{
			name: "blank name",
			req:  &driverv1.SaveScheduleRequest{Name: "", Schedule: &driverv1.ScheduleRecord{Format: ScheduleFormat, Payload: goodPayload}},
		},
		{
			name: "nil schedule",
			req:  &driverv1.SaveScheduleRequest{Name: "name-1"},
		},
		{
			name: "unknown format",
			req:  &driverv1.SaveScheduleRequest{Name: "name-1", Schedule: &driverv1.ScheduleRecord{Format: "schedule-json/99", Payload: goodPayload}},
		},
		{
			name: "empty payload",
			req:  &driverv1.SaveScheduleRequest{Name: "name-1", Schedule: &driverv1.ScheduleRecord{Format: ScheduleFormat, Payload: nil}},
		},
		{
			name: "undecodable payload",
			req:  &driverv1.SaveScheduleRequest{Name: "name-1", Schedule: &driverv1.ScheduleRecord{Format: ScheduleFormat, Payload: []byte("{not json")}},
		},
		{
			name: "name disagrees with payload Spec.Name",
			req:  &driverv1.SaveScheduleRequest{Name: "name-OTHER", Schedule: &driverv1.ScheduleRecord{Format: ScheduleFormat, Payload: goodPayload}},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := srv.SaveSchedule(ctx, c.req)
			if err == nil {
				t.Fatal("SaveSchedule(bad envelope) = nil error, want InvalidArgument")
			}
			if got := status.Code(err); got != codes.InvalidArgument {
				t.Errorf("SaveSchedule(bad envelope) code = %v (%v), want InvalidArgument", got, err)
			}
		})
	}

	// And the well-formed control: the same payload under the MATCHING name is
	// accepted (so the table above fails for validation, not incidentals).
	if _, err := srv.SaveSchedule(ctx, &driverv1.SaveScheduleRequest{
		Name:     "name-1",
		Schedule: &driverv1.ScheduleRecord{Format: ScheduleFormat, Payload: goodPayload},
	}); err != nil {
		t.Fatalf("SaveSchedule(well-formed control) = %v, want nil", err)
	}
}

// TestScheduleServerWrapperSaveBadEnvelopeNeverCallsBackend asserts the server
// wrapper rejects invalid requests BEFORE the wrapped backend is touched (the
// counting fake is never called for any invalid case).
func TestScheduleServerWrapperSaveBadEnvelopeNeverCallsBackend(t *testing.T) {
	cs := &countingScheduleStore{}
	srv := NewScheduleStoreServer(cs)
	ctx := context.Background()

	goodPayload, _ := json.Marshal(port.Schedule{
		Spec: port.ScheduleSpec{Name: "name-1", Prompt: "p", Trigger: port.TriggerSpec{Cron: sampleScheduleCron}},
	})
	bad := []struct {
		name string
		req  *driverv1.SaveScheduleRequest
	}{
		{"blank name", &driverv1.SaveScheduleRequest{Name: "", Schedule: &driverv1.ScheduleRecord{Format: ScheduleFormat, Payload: goodPayload}}},
		{"nil schedule", &driverv1.SaveScheduleRequest{Name: "name-1"}},
		{"unknown format", &driverv1.SaveScheduleRequest{Name: "name-1", Schedule: &driverv1.ScheduleRecord{Format: "schedule-json/99", Payload: goodPayload}}},
		{"empty payload", &driverv1.SaveScheduleRequest{Name: "name-1", Schedule: &driverv1.ScheduleRecord{Format: ScheduleFormat, Payload: nil}}},
		{"undecodable", &driverv1.SaveScheduleRequest{Name: "name-1", Schedule: &driverv1.ScheduleRecord{Format: ScheduleFormat, Payload: []byte("{not json")}}},
		{"name mismatch", &driverv1.SaveScheduleRequest{Name: "name-OTHER", Schedule: &driverv1.ScheduleRecord{Format: ScheduleFormat, Payload: goodPayload}}},
	}
	for _, c := range bad {
		t.Run(c.name, func(t *testing.T) {
			if _, err := srv.SaveSchedule(ctx, c.req); err == nil {
				t.Fatalf("SaveSchedule(%s) = nil, want InvalidArgument", c.name)
			}
			if cs.saves != 0 {
				t.Errorf("after SaveSchedule(%s): backend called %d times, want 0 (validation must precede the backend)", c.name, cs.saves)
			}
		})
	}
}

// countingScheduleStore counts Save and RecordFire/RecordFireStart calls (a
// port.ScheduleStore fake) so validation tables can assert the backend is never
// called for an invalid request.
type countingScheduleStore struct {
	saves       int
	recordFires int
}

func (s *countingScheduleStore) Save(context.Context, port.Schedule) error {
	s.saves++
	return nil
}

func (*countingScheduleStore) Load(context.Context, string) (port.Schedule, error) {
	return port.Schedule{}, port.ErrScheduleNotFound
}

func (*countingScheduleStore) Delete(context.Context, string) error { return nil }

func (*countingScheduleStore) List(context.Context) ([]port.Schedule, error) {
	return nil, nil
}

func (*countingScheduleStore) Due(context.Context, time.Time) ([]port.Schedule, error) {
	return nil, nil
}

func (*countingScheduleStore) Claim(context.Context, string, time.Time, time.Time) (port.Schedule, error) {
	return port.Schedule{}, port.ErrScheduleNotFound
}

func (*countingScheduleStore) ClaimNow(context.Context, string, time.Time, time.Time) (port.Schedule, error) {
	return port.Schedule{}, port.ErrScheduleNotFound
}

func (*countingScheduleStore) SetEnabled(context.Context, string, bool) error {
	return port.ErrScheduleNotFound
}

func (s *countingScheduleStore) RecordFire(context.Context, port.ScheduleFire) error {
	s.recordFires++
	return nil
}

func (s *countingScheduleStore) RecordFireStart(context.Context, string, port.ScheduleFire) error {
	s.recordFires++
	return nil
}

func (*countingScheduleStore) RecordFireProgress(context.Context, string, string, time.Time) error {
	return nil
}

func (*countingScheduleStore) LoadFire(context.Context, string) (port.ScheduleFire, error) {
	return port.ScheduleFire{}, port.ErrScheduleNotFound
}

func (*countingScheduleStore) ListFires(context.Context, string) ([]port.ScheduleFire, error) {
	return nil, nil
}

// TestScheduleRecordFireIdempotentOverWire pins RecordFire idempotency over
// the wire: recording the same fire id twice → second call nil, fire NOT
// duplicated in ListFires (driven against the real server wrapper over
// memschedulestore).
func TestScheduleRecordFireIdempotentOverWire(t *testing.T) {
	st, _ := newWiredScheduleStore(t)
	ctx := context.Background()
	now := time.Unix(1_700_000_000, 0)
	next := now.Add(time.Minute)
	const name = "rec-fire-idem"
	if err := st.Save(ctx, port.Schedule{
		Spec:  port.ScheduleSpec{Name: name, Prompt: "p", Trigger: port.TriggerSpec{Cron: sampleScheduleCron}},
		State: port.ScheduleState{NextFireAt: now, Enabled: true},
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if _, err := st.Claim(ctx, name, now, next); err != nil {
		t.Fatalf("Claim: %v", err)
	}
	fire := port.ScheduleFire{
		ID:           "fire-idem-1",
		ScheduleName: name,
		SessionID:    "real-session-1",
		FiredAt:      now,
		Stop:         session.StopEndTurn,
	}
	if err := st.RecordFire(ctx, fire); err != nil {
		t.Fatalf("RecordFire #1: %v", err)
	}
	// Idempotent re-record: same id, a would-be mutation (StopError) must NOT stick.
	fire2 := fire
	fire2.Stop = session.StopError
	fire2.Err = "transient"
	if err := st.RecordFire(ctx, fire2); err != nil {
		t.Fatalf("RecordFire #2 (idempotent): %v", err)
	}
	fires, err := st.ListFires(ctx, name)
	if err != nil {
		t.Fatalf("ListFires: %v", err)
	}
	if len(fires) != 1 {
		t.Fatalf("ListFires = %d records, want 1 (idempotent — no duplication)", len(fires))
	}
	if fires[0].ID != fire.ID || fires[0].Stop != session.StopEndTurn {
		t.Errorf("ListFires[0] = %+v, want the original fire (StopEndTurn, not the idempotent re-record's StopError)", fires[0])
	}
}

// TestScheduleRecordFireStartProgressLoadFireOverWire pins the issue #386
// in-flight paths over the wire: start an in-flight fire, advance progress,
// LoadFire shows the progress; then a terminal RecordFire flips it (Stop set)
// and clears the in-flight state on the schedule.
func TestScheduleRecordFireStartProgressLoadFireOverWire(t *testing.T) {
	st, _ := newWiredScheduleStore(t)
	ctx := context.Background()
	now := time.Unix(1_700_000_000, 0)
	next := now.Add(time.Minute)
	const name = "rec-fire-inflight"
	if err := st.Save(ctx, port.Schedule{
		Spec:  port.ScheduleSpec{Name: name, Prompt: "p", Trigger: port.TriggerSpec{Cron: sampleScheduleCron}, FireTimeout: 5 * time.Minute},
		State: port.ScheduleState{NextFireAt: now, Enabled: true},
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if _, err := st.Claim(ctx, name, now, next); err != nil {
		t.Fatalf("Claim: %v", err)
	}
	start := now.Add(time.Second)
	deadline := start.Add(5 * time.Minute)
	fire := port.ScheduleFire{
		ID:           "fire-inflight-wire",
		ScheduleName: name,
		SessionID:    "real-session-1",
		FiredAt:      now,
		StartedAt:    start,
		Deadline:     deadline,
	}
	if err := st.RecordFireStart(ctx, name, fire); err != nil {
		t.Fatalf("RecordFireStart: %v", err)
	}
	// The schedule's in-flight state is set.
	got, err := st.Load(ctx, name)
	if err != nil {
		t.Fatalf("Load after RecordFireStart: %v", err)
	}
	if got.State.LastFireSessionID != "real-session-1" {
		t.Errorf("LastFireSessionID = %q, want real-session-1", got.State.LastFireSessionID)
	}
	if !got.State.LastFireStartedAt.Equal(start) {
		t.Errorf("LastFireStartedAt = %v, want %v", got.State.LastFireStartedAt, start)
	}
	if !got.State.FireDeadline.Equal(deadline) {
		t.Errorf("FireDeadline = %v, want %v", got.State.FireDeadline, deadline)
	}

	// Advance progress.
	p1 := start.Add(2 * time.Second)
	if err := st.RecordFireProgress(ctx, name, fire.ID, p1); err != nil {
		t.Fatalf("RecordFireProgress: %v", err)
	}
	got, err = st.Load(ctx, name)
	if err != nil {
		t.Fatalf("Load after RecordFireProgress: %v", err)
	}
	if !got.State.LastFireProgressAt.Equal(p1) {
		t.Errorf("LastFireProgressAt = %v, want %v", got.State.LastFireProgressAt, p1)
	}
	// LoadFire shows the in-flight fire (Stop empty, StartedAt set).
	loaded, err := st.LoadFire(ctx, fire.ID)
	if err != nil {
		t.Fatalf("LoadFire (in-flight): %v", err)
	}
	if loaded.ID != fire.ID || loaded.Stop != "" || !loaded.StartedAt.Equal(start) {
		t.Errorf("LoadFire (in-flight) = %+v, want Stop empty, StartedAt %v", loaded, start)
	}

	// Terminal RecordFire flips it (Stop set) and clears the in-flight state.
	fire.Stop = session.StopEndTurn
	if err := st.RecordFire(ctx, fire); err != nil {
		t.Fatalf("RecordFire (terminal): %v", err)
	}
	got, err = st.Load(ctx, name)
	if err != nil {
		t.Fatalf("Load after RecordFire: %v", err)
	}
	if !got.State.LastFireStartedAt.IsZero() || !got.State.FireDeadline.IsZero() {
		t.Errorf("in-flight state after terminal RecordFire = %+v, want cleared (LastFireStartedAt/FireDeadline zero)", got.State)
	}
	loaded, err = st.LoadFire(ctx, fire.ID)
	if err != nil {
		t.Fatalf("LoadFire (terminal): %v", err)
	}
	if loaded.Stop != session.StopEndTurn {
		t.Errorf("LoadFire (terminal) Stop = %q, want %q (flipped terminal)", loaded.Stop, session.StopEndTurn)
	}
}

// TestScheduleClaimOverWire pins Claim's atomic advance over the wire: claim
// advances NextFireAt/FireCount, sets LastFireSessionID to the pending
// sentinel, Due no longer returns the slot; a second Due→ empty. And Claim on
// a deleted/unknown schedule → ErrScheduleNotFound.
func TestScheduleClaimOverWire(t *testing.T) {
	st, _ := newWiredScheduleStore(t)
	ctx := context.Background()
	now := time.Unix(1_700_000_000, 0)
	next := now.Add(time.Minute)
	const name = "claim-wire"
	if err := st.Save(ctx, port.Schedule{
		Spec:  port.ScheduleSpec{Name: name, Prompt: "p", Trigger: port.TriggerSpec{Cron: sampleScheduleCron}},
		State: port.ScheduleState{NextFireAt: now, Enabled: true},
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	// Due returns the slot.
	due, err := st.Due(ctx, now)
	if err != nil {
		t.Fatalf("Due: %v", err)
	}
	if len(due) != 1 || due[0].Spec.Name != name {
		t.Fatalf("Due = %+v, want one slot for %q", due, name)
	}
	// Claim advances.
	claimed, err := st.Claim(ctx, name, now, next)
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if claimed.State.LastFireSessionID != port.PendingFireSessionID {
		t.Errorf("Claim LastFireSessionID = %q, want pending sentinel %q", claimed.State.LastFireSessionID, port.PendingFireSessionID)
	}
	if claimed.State.FireCount != 1 {
		t.Errorf("Claim FireCount = %d, want 1", claimed.State.FireCount)
	}
	if !claimed.State.NextFireAt.Equal(next) {
		t.Errorf("Claim NextFireAt = %v, want %v", claimed.State.NextFireAt, next)
	}
	// Due at the SAME now no longer returns the slot (NextFireAt advanced past now).
	due, err = st.Due(ctx, now)
	if err != nil {
		t.Fatalf("Due after claim: %v", err)
	}
	if len(due) != 0 {
		t.Errorf("Due after claim = %d slots, want 0 (NextFireAt advanced past now)", len(due))
	}
	// Claim on an unknown schedule → ErrScheduleNotFound.
	if _, err := st.Claim(ctx, "ghost-sched", now, next); !errors.Is(err, port.ErrScheduleNotFound) {
		t.Errorf("Claim(unknown) = %v, want errors.Is(_, port.ErrScheduleNotFound)", err)
	}
}

// TestScheduleClaimNowFenceOverWire pins ClaimNow's at-most-once fence over the
// wire: ClaimNow on a not-yet-due slot succeeds, and a ClaimNow at the SAME
// now again is rejected per the port contract (LastFireAt == now fence). It
// verifies what memschedulestore does and asserts the same over the wire.
func TestScheduleClaimNowFenceOverWire(t *testing.T) {
	st, _ := newWiredScheduleStore(t)
	ctx := context.Background()
	now := time.Unix(1_700_000_000, 0)
	next := now.Add(time.Minute)
	const name = "claimnow-wire"
	if err := st.Save(ctx, port.Schedule{
		Spec:  port.ScheduleSpec{Name: name, Prompt: "p", Trigger: port.TriggerSpec{Cron: sampleScheduleCron}},
		State: port.ScheduleState{NextFireAt: next, Enabled: true}, // not yet due at `now`
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	// ClaimNow on a not-yet-due slot succeeds (no due-check).
	if _, err := st.ClaimNow(ctx, name, now, next); err != nil {
		t.Fatalf("ClaimNow (not-yet-due): %v", err)
	}
	// A ClaimNow at the SAME now again is rejected (LastFireAt == now fence).
	if _, err := st.ClaimNow(ctx, name, now, next); !errors.Is(err, port.ErrScheduleNotFound) {
		t.Errorf("ClaimNow (same now) = %v, want errors.Is(_, port.ErrScheduleNotFound) (LastFireAt fence)", err)
	}
	// A ClaimNow at a LATER now succeeds (crash-recoverable: the fence self-heals).
	later := now.Add(time.Minute)
	next2 := later.Add(time.Minute)
	if _, err := st.ClaimNow(ctx, name, later, next2); err != nil {
		t.Fatalf("ClaimNow (later now): %v", err)
	}
}

// TestScheduleContextCancelPassthrough pins the ctx row: after a failed RPC
// with a done caller ctx, the returned error satisfies errors.Is(_,
// context.Canceled) so the harness's cancellation handling holds over the wire.
func TestScheduleContextCancelPassthrough(t *testing.T) {
	st, _ := newWiredScheduleStore(t)

	t.Run("cancelled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := st.Load(ctx, "any-name")
		if !errors.Is(err, context.Canceled) {
			t.Errorf("Load(cancelled ctx) error = %v, want errors.Is(_, context.Canceled)", err)
		}
	})

	t.Run("deadline exceeded", func(t *testing.T) {
		ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
		defer cancel()
		_, err := st.Load(ctx, "any-name")
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("Load(expired ctx) error = %v, want errors.Is(_, context.DeadlineExceeded)", err)
		}
	})
}

// TestScheduleReArmOneShotOverWire is the re-arm happy path over the wire
// (server wrapper over memschedulestore which implements it): a disabled
// one-shot → re-armed (Enabled=true, NextFireAt=nextFire, OneShotRetryCount
// incremented).
func TestScheduleReArmOneShotOverWire(t *testing.T) {
	st, _ := newWiredScheduleStore(t)
	ctx := context.Background()
	now := time.Unix(1_700_000_000, 0)
	const name = "rearm-wire"
	if err := st.Save(ctx, port.Schedule{
		Spec: port.ScheduleSpec{
			Name:              name,
			Prompt:            "once",
			Trigger:           port.TriggerSpec{OneShot: now},
			Mutating:          true,
			OneShotRetry:      true,
			OneShotMaxRetries: 3,
		},
		State: port.ScheduleState{NextFireAt: now, Enabled: true},
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	// Claim the one-shot (Claim disables it — the at-most-once advance).
	if _, err := st.Claim(ctx, name, now, time.Time{}); err != nil {
		t.Fatalf("Claim: %v", err)
	}
	got, _ := st.Load(ctx, name)
	if got.State.Enabled {
		t.Fatalf("Claim did not disable the one-shot (Enabled=true)")
	}
	// ReArm re-enables + advances NextFireAt + increments the counter.
	nextFire := now.Add(time.Minute)
	if err := st.ReArmOneShot(ctx, name, nextFire); err != nil {
		t.Fatalf("ReArmOneShot: %v", err)
	}
	got, err := st.Load(ctx, name)
	if err != nil {
		t.Fatalf("Load after ReArmOneShot: %v", err)
	}
	if !got.State.Enabled {
		t.Errorf("Enabled = false, want true (ReArmOneShot re-enabled)")
	}
	if !got.State.NextFireAt.Equal(nextFire) {
		t.Errorf("NextFireAt = %v, want %v (ReArmOneShot advanced)", got.State.NextFireAt, nextFire)
	}
	if got.State.OneShotRetryCount != 1 {
		t.Errorf("OneShotRetryCount = %d, want 1 (incremented)", got.State.OneShotRetryCount)
	}
	// ReArmOneShot on an unknown name wraps ErrScheduleNotFound.
	if err := st.ReArmOneShot(ctx, "rearm-missing", nextFire); !errors.Is(err, port.ErrScheduleNotFound) {
		t.Errorf("ReArmOneShot(unknown) = %v, want errors.Is(_, port.ErrScheduleNotFound)", err)
	}
}

// TestScheduleReArmOneShotUnsupportedWhenReArmerNotRegistered pins the
// production degradation path: a server that registers ONLY
// ScheduleStoreService (not the re-armer) answers ReArmOneShot with
// UNIMPLEMENTED, which the client maps to port.ErrScheduleUnsupported (the
// sticky-disable sentinel).
func TestScheduleReArmOneShotUnsupportedWhenReArmerNotRegistered(t *testing.T) {
	conn := dialBufconn(t, func(gs *grpc.Server) {
		// ONLY the schedule store service is registered — no re-armer.
		driverv1.RegisterScheduleStoreServiceServer(gs, NewScheduleStoreServer(memschedulestore.New()))
	})
	st := NewScheduleStore(conn)
	err := st.ReArmOneShot(context.Background(), "any-name", time.Unix(1_700_000_060, 0))
	if err == nil {
		t.Fatal("ReArmOneShot (no re-armer registered) = nil error, want ErrScheduleUnsupported")
	}
	if !errors.Is(err, port.ErrScheduleUnsupported) {
		t.Errorf("ReArmOneShot (no re-armer) error = %v, want errors.Is(_, port.ErrScheduleUnsupported)", err)
	}
}

// notFoundDeleteScheduleServer is a thin driver whose DeleteSchedule surfaces
// its primitive's NOT_FOUND instead of the contract's idempotent OK.
type notFoundDeleteScheduleServer struct {
	driverv1.UnimplementedScheduleStoreServiceServer
}

func (notFoundDeleteScheduleServer) DeleteSchedule(context.Context, *driverv1.DeleteScheduleRequest) (*driverv1.DeleteScheduleResponse, error) {
	return nil, status.Error(codes.NotFound, "no such schedule")
}

// TestScheduleDeleteToleratesDriverNotFound pins the client's idempotency
// mapping: a driver NOT_FOUND on DeleteSchedule is success (the port contract
// says unknown name = success), mirroring the session-store
// TestDeleteToleratesDriverNotFound precedent. The client maps a driver
// NOT_FOUND to nil — see schedulestore.go Delete.
func TestScheduleDeleteToleratesDriverNotFound(t *testing.T) {
	conn := dialBufconn(t, func(gs *grpc.Server) {
		driverv1.RegisterScheduleStoreServiceServer(gs, notFoundDeleteScheduleServer{})
	})
	st := NewScheduleStore(conn)
	if err := st.Delete(context.Background(), "ghost"); err != nil {
		t.Errorf("Delete mapping a driver NOT_FOUND = %v, want nil (idempotent success)", err)
	}
}

// misKeyedFireServer returns a fire whose ID differs from the requested id.
type misKeyedFireServer struct {
	driverv1.UnimplementedScheduleStoreServiceServer
}

func (misKeyedFireServer) LoadFire(context.Context, *driverv1.LoadFireRequest) (*driverv1.LoadFireResponse, error) {
	other := port.ScheduleFire{ID: "DIFFERENT-fire-id", ScheduleName: "x", FiredAt: time.Unix(1_700_000_000, 0)}
	payload, _ := json.Marshal(other)
	return &driverv1.LoadFireResponse{
		Fire: &driverv1.ScheduleFireRecord{Format: ScheduleFormat, Payload: payload},
	}, nil
}

// TestScheduleLoadFireWrongIDGuardIsInfraError pins the mis-keyed-driver guard
// for LoadFire: a driver returning a fire whose ID differs from the request is
// an infrastructure error, NOT ErrScheduleNotFound (the Load wrong-name guard
// precedent). The client must not silently adopt a foreign fire record.
func TestScheduleLoadFireWrongIDGuardIsInfraError(t *testing.T) {
	conn := dialBufconn(t, func(gs *grpc.Server) {
		driverv1.RegisterScheduleStoreServiceServer(gs, misKeyedFireServer{})
	})
	st := NewScheduleStore(conn)
	_, err := st.LoadFire(context.Background(), "requested-fire-id")
	if err == nil {
		t.Fatal("LoadFire(mis-keyed) = nil error, want an infra error")
	}
	if errors.Is(err, port.ErrScheduleNotFound) {
		t.Errorf("LoadFire(mis-keyed) error = %v: must NEVER be ErrScheduleNotFound (it is a mis-keyed driver, not a miss)", err)
	}
	if want := "DIFFERENT"; !strings.Contains(err.Error(), want) {
		t.Errorf("LoadFire(mis-keyed) error %q should name the foreign fire", err)
	}
}

// unknownFormatFireServer returns a fire record envelope under a format tag
// this client does not speak.
type unknownFormatFireServer struct {
	driverv1.UnimplementedScheduleStoreServiceServer
}

func (unknownFormatFireServer) LoadFire(context.Context, *driverv1.LoadFireRequest) (*driverv1.LoadFireResponse, error) {
	return &driverv1.LoadFireResponse{
		Fire: &driverv1.ScheduleFireRecord{Format: "schedule-json/99", Payload: []byte("{}")},
	}, nil
}

// TestScheduleLoadFireUnknownFormatIsInfraError pins the format-versioning rule
// for LoadFire: a fire record whose envelope carries an unknown format tag is
// an INFRASTRUCTURE error naming the format — never ErrScheduleNotFound.
func TestScheduleLoadFireUnknownFormatIsInfraError(t *testing.T) {
	conn := dialBufconn(t, func(gs *grpc.Server) {
		driverv1.RegisterScheduleStoreServiceServer(gs, unknownFormatFireServer{})
	})
	st := NewScheduleStore(conn)
	_, err := st.LoadFire(context.Background(), "any-fire-id")
	if err == nil {
		t.Fatal("LoadFire(unknown format) = nil error, want an infra error")
	}
	if errors.Is(err, port.ErrScheduleNotFound) {
		t.Errorf("LoadFire(unknown format) error = %v: must NEVER be ErrScheduleNotFound", err)
	}
	if want := "schedule-json/99"; !strings.Contains(err.Error(), want) {
		t.Errorf("LoadFire(unknown format) error %q does not name the offending format %q", err, want)
	}
}

// TestScheduleServerWrapperRecordFireRejectsBadEnvelope pins the server
// wrapper's RecordFire pre-validation: every malformed envelope shape — blank
// fire_id, nil fire, unknown format, empty payload, undecodable payload, and
// a top-level fire_id that disagrees with the payload's ID — is
// INVALID_ARGUMENT (never stored, never Internal). Invoked directly on the
// wrapper, mirroring the Save-validation table.
func TestScheduleServerWrapperRecordFireRejectsBadEnvelope(t *testing.T) {
	cs := &countingScheduleStore{}
	srv := NewScheduleStoreServer(cs)
	ctx := context.Background()

	goodPayload, _ := json.Marshal(port.ScheduleFire{
		ID: "fire-1", ScheduleName: "sched-x", FiredAt: time.Unix(1_700_000_000, 0),
	})

	cases := []struct {
		name string
		req  *driverv1.RecordFireRequest
	}{
		{
			name: "blank fire_id",
			req: &driverv1.RecordFireRequest{
				FireId: "",
				Fire:   &driverv1.ScheduleFireRecord{Format: ScheduleFormat, Payload: goodPayload},
			},
		},
		{
			name: "nil fire",
			req:  &driverv1.RecordFireRequest{FireId: "fire-1"},
		},
		{
			name: "unknown format",
			req: &driverv1.RecordFireRequest{
				FireId: "fire-1",
				Fire:   &driverv1.ScheduleFireRecord{Format: "schedule-json/99", Payload: goodPayload},
			},
		},
		{
			name: "empty payload",
			req: &driverv1.RecordFireRequest{
				FireId: "fire-1",
				Fire:   &driverv1.ScheduleFireRecord{Format: ScheduleFormat, Payload: nil},
			},
		},
		{
			name: "undecodable payload",
			req: &driverv1.RecordFireRequest{
				FireId: "fire-1",
				Fire:   &driverv1.ScheduleFireRecord{Format: ScheduleFormat, Payload: []byte("{not json")},
			},
		},
		{
			name: "fire_id disagrees with payload ID",
			req: &driverv1.RecordFireRequest{
				FireId: "fire-OTHER",
				Fire:   &driverv1.ScheduleFireRecord{Format: ScheduleFormat, Payload: goodPayload},
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := srv.RecordFire(ctx, c.req)
			if err == nil {
				t.Fatal("RecordFire(bad envelope) = nil error, want InvalidArgument")
			}
			if got := status.Code(err); got != codes.InvalidArgument {
				t.Errorf("RecordFire(bad envelope) code = %v (%v), want InvalidArgument", got, err)
			}
			if cs.recordFires != 0 {
				t.Errorf("after RecordFire(%s): backend called %d times, want 0 (validation must precede the backend)", c.name, cs.recordFires)
			}
		})
	}

	// And the well-formed control: the same payload under the MATCHING fire_id is
	// accepted (so the table above fails for validation, not incidentals).
	if _, err := srv.RecordFire(ctx, &driverv1.RecordFireRequest{
		FireId: "fire-1",
		Fire:   &driverv1.ScheduleFireRecord{Format: ScheduleFormat, Payload: goodPayload},
	}); err != nil {
		t.Fatalf("RecordFire(well-formed control) = %v, want nil", err)
	}
}

// TestScheduleServerWrapperRecordFireStartRejectsBadEnvelope pins the server
// wrapper's RecordFireStart pre-validation: blank name, blank fire_id, nil
// fire, wrong format, empty payload, undecodable payload, id mismatch — all
// INVALID_ARGUMENT (never stored, never Internal).
func TestScheduleServerWrapperRecordFireStartRejectsBadEnvelope(t *testing.T) {
	cs := &countingScheduleStore{}
	srv := NewScheduleStoreServer(cs)
	ctx := context.Background()

	goodPayload, _ := json.Marshal(port.ScheduleFire{
		ID: "fire-1", ScheduleName: "sched-x", FiredAt: time.Unix(1_700_000_000, 0),
	})

	cases := []struct {
		name string
		req  *driverv1.RecordFireStartRequest
	}{
		{
			name: "blank name",
			req: &driverv1.RecordFireStartRequest{
				Name:   "",
				FireId: "fire-1",
				Fire:   &driverv1.ScheduleFireRecord{Format: ScheduleFormat, Payload: goodPayload},
			},
		},
		{
			name: "blank fire_id",
			req: &driverv1.RecordFireStartRequest{
				Name:   "sched-x",
				FireId: "",
				Fire:   &driverv1.ScheduleFireRecord{Format: ScheduleFormat, Payload: goodPayload},
			},
		},
		{
			name: "nil fire",
			req:  &driverv1.RecordFireStartRequest{Name: "sched-x", FireId: "fire-1"},
		},
		{
			name: "unknown format",
			req: &driverv1.RecordFireStartRequest{
				Name:   "sched-x",
				FireId: "fire-1",
				Fire:   &driverv1.ScheduleFireRecord{Format: "schedule-json/99", Payload: goodPayload},
			},
		},
		{
			name: "empty payload",
			req: &driverv1.RecordFireStartRequest{
				Name:   "sched-x",
				FireId: "fire-1",
				Fire:   &driverv1.ScheduleFireRecord{Format: ScheduleFormat, Payload: nil},
			},
		},
		{
			name: "undecodable payload",
			req: &driverv1.RecordFireStartRequest{
				Name:   "sched-x",
				FireId: "fire-1",
				Fire:   &driverv1.ScheduleFireRecord{Format: ScheduleFormat, Payload: []byte("{not json")},
			},
		},
		{
			name: "fire_id disagrees with payload ID",
			req: &driverv1.RecordFireStartRequest{
				Name:   "sched-x",
				FireId: "fire-OTHER",
				Fire:   &driverv1.ScheduleFireRecord{Format: ScheduleFormat, Payload: goodPayload},
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := srv.RecordFireStart(ctx, c.req)
			if err == nil {
				t.Fatal("RecordFireStart(bad envelope) = nil error, want InvalidArgument")
			}
			if got := status.Code(err); got != codes.InvalidArgument {
				t.Errorf("RecordFireStart(bad envelope) code = %v (%v), want InvalidArgument", got, err)
			}
			if cs.recordFires != 0 {
				t.Errorf("after RecordFireStart(%s): backend called %d times, want 0 (validation must precede the backend)", c.name, cs.recordFires)
			}
		})
	}

	// And the well-formed control.
	if _, err := srv.RecordFireStart(ctx, &driverv1.RecordFireStartRequest{
		Name:   "sched-x",
		FireId: "fire-1",
		Fire:   &driverv1.ScheduleFireRecord{Format: ScheduleFormat, Payload: goodPayload},
	}); err != nil {
		t.Fatalf("RecordFireStart(well-formed control) = %v, want nil", err)
	}
}

// TestScheduleServerWrapperRejectsBlankScalarFields is one table test covering
// blank-scalar validation across every remaining RPC: blank name/fire_id on
// LoadSchedule, DeleteSchedule, ClaimSchedule, ClaimScheduleNow,
// SetScheduleEnabled, RecordFireProgress, LoadFire, ListFires, ReArmOneShot;
// nil timestamps on DueSchedules (now), ClaimSchedule (now), ClaimScheduleNow
// (now), RecordFireProgress (at), ReArmOneShot (next_fire). All must be
// INVALID_ARGUMENT. Invoked directly on the server wrappers, mirroring the
// Save-validation table's direct-call posture.
func TestScheduleServerWrapperRejectsBlankScalarFields(t *testing.T) {
	ctx := context.Background()
	storeSrv := NewScheduleStoreServer(memschedulestore.New())
	reArmSrv := NewScheduleOneShotReArmerServer(memschedulestore.New())

	now := time.Unix(1_700_000_000, 0)
	type fnCase struct {
		name string
		fn   func() error
	}

	cases := []fnCase{
		{
			name: "LoadSchedule blank name",
			fn:   func() error { _, e := storeSrv.LoadSchedule(ctx, &driverv1.LoadScheduleRequest{Name: ""}); return e },
		},
		{
			name: "DeleteSchedule blank name",
			fn: func() error {
				_, e := storeSrv.DeleteSchedule(ctx, &driverv1.DeleteScheduleRequest{Name: ""})
				return e
			},
		},
		{
			name: "ClaimSchedule blank name",
			fn: func() error {
				_, e := storeSrv.ClaimSchedule(ctx, &driverv1.ClaimScheduleRequest{Name: ""})
				return e
			},
		},
		{
			name: "ClaimSchedule nil now",
			fn: func() error {
				_, e := storeSrv.ClaimSchedule(ctx, &driverv1.ClaimScheduleRequest{Name: "sched-x"})
				return e
			},
		},
		{
			name: "ClaimScheduleNow blank name",
			fn: func() error {
				_, e := storeSrv.ClaimScheduleNow(ctx, &driverv1.ClaimScheduleNowRequest{Name: ""})
				return e
			},
		},
		{
			name: "ClaimScheduleNow nil now",
			fn: func() error {
				_, e := storeSrv.ClaimScheduleNow(ctx, &driverv1.ClaimScheduleNowRequest{Name: "sched-x"})
				return e
			},
		},
		{
			name: "SetScheduleEnabled blank name",
			fn: func() error {
				_, e := storeSrv.SetScheduleEnabled(ctx, &driverv1.SetScheduleEnabledRequest{Name: ""})
				return e
			},
		},
		{
			name: "RecordFireProgress blank name",
			fn: func() error {
				_, e := storeSrv.RecordFireProgress(ctx, &driverv1.RecordFireProgressRequest{
					Name: "", FireId: "f", At: timestamppb.New(now),
				})
				return e
			},
		},
		{
			name: "RecordFireProgress blank fire_id",
			fn: func() error {
				_, e := storeSrv.RecordFireProgress(ctx, &driverv1.RecordFireProgressRequest{
					Name: "sched-x", FireId: "", At: timestamppb.New(now),
				})
				return e
			},
		},
		{
			name: "RecordFireProgress nil at",
			fn: func() error {
				_, e := storeSrv.RecordFireProgress(ctx, &driverv1.RecordFireProgressRequest{
					Name: "sched-x", FireId: "f",
				})
				return e
			},
		},
		{
			name: "LoadFire blank fire_id",
			fn:   func() error { _, e := storeSrv.LoadFire(ctx, &driverv1.LoadFireRequest{FireId: ""}); return e },
		},
		{
			name: "ListFires blank schedule_name",
			fn:   func() error { _, e := storeSrv.ListFires(ctx, &driverv1.ListFiresRequest{ScheduleName: ""}); return e },
		},
		{
			name: "ReArmOneShot blank name",
			fn: func() error {
				_, e := reArmSrv.ReArmOneShot(ctx, &driverv1.ReArmOneShotRequest{Name: "", NextFire: timestamppb.New(now)})
				return e
			},
		},
		{
			name: "ReArmOneShot nil next_fire",
			fn: func() error {
				_, e := reArmSrv.ReArmOneShot(ctx, &driverv1.ReArmOneShotRequest{Name: "sched-x"})
				return e
			},
		},
		{
			name: "DueSchedules nil now",
			fn:   func() error { _, e := storeSrv.DueSchedules(ctx, &driverv1.DueSchedulesRequest{}); return e },
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.fn()
			if err == nil {
				t.Fatalf("%s = nil error, want InvalidArgument", c.name)
			}
			if got := status.Code(err); got != codes.InvalidArgument {
				t.Errorf("%s code = %v (%v), want InvalidArgument", c.name, got, err)
			}
		})
	}
}
