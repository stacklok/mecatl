package grpcdriver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	driverv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/driver/v1"
	"github.com/stacklok/mecatl/engine/port"
)

// ScheduleFormat is the schedule/fire-record envelope format tag this client
// writes on Save/RecordFire* and accepts on Load/LoadFire/List*: the payload is
// exactly `encoding/json` of a `port.Schedule` / `port.ScheduleFire` (the same
// encoding the in-process jsonlstore/redisstore persist). The driver
// round-trips the tag verbatim; it changes ONLY if the encoding itself is
// replaced (the Schedule/ScheduleFire structs' own schema evolution is
// additive and needs no bump). Load/LoadFire reject any other tag with an
// infrastructure error — never ErrScheduleNotFound.
//
// SIGNPOST — a future format bump MUST be read-set-accept / write-newest: the
// readers (this client's Load/LoadFire, the server wrapper's Save/RecordFire*
// decode) must keep ACCEPTING every previously-shipped format tag while
// Save/RecordFire* WRITE only the newest. The driver round-trips envelopes
// verbatim and cannot migrate them — a bump that switches the write tag and
// rejects the old tag in the same step bricks every schedule/fire already
// stored under the old format.
const ScheduleFormat = "schedule-json/1"

// ScheduleStore is a port.ScheduleStore (and, unconditionally, a
// port.ScheduleOneShotReArmer) over a remote ScheduleStoreService /
// ScheduleOneShotReArmerService driver. Encode/decode happens HERE
// (encoding/json of port.Schedule / port.ScheduleFire), harness-side: the
// driver only ever sees the opaque, format-tagged envelope, exactly as the
// SessionStore driver keeps sessnap harness-side.
//
// The client implements the OPTIONAL ScheduleOneShotReArmer UNCONDITIONALLY —
// the PrunableStore precedent: a driver that does not serve the re-arm service
// answers ReArmOneShot with UNIMPLEMENTED, which this client maps to
// port.ErrScheduleUnsupported (the sticky-disable sentinel, the
// ErrLeaseUnsupported precedent); composition then logs one INFO and
// stickily disables the one-shot re-arm path (byte-identical to the
// pre-Phase-2 at-most-once posture). This is the production degradation path,
// not an error: a driver serving only ScheduleStoreService still conforms.
type ScheduleStore struct {
	store   driverv1.ScheduleStoreServiceClient
	reArmer driverv1.ScheduleOneShotReArmerServiceClient
}

// compile-time assertions that ScheduleStore satisfies the port plus the
// optional re-arm seam. The client implements ScheduleOneShotReArmer
// UNCONDITIONALLY (the PrunableStore precedent) — a driver that does not
// serve the re-arm service answers UNIMPLEMENTED, which ReArmOneShot maps to
// port.ErrScheduleUnsupported (the sticky-disable sentinel).
var (
	_ port.ScheduleStore          = (*ScheduleStore)(nil)
	_ port.ScheduleOneShotReArmer = (*ScheduleStore)(nil)
)

// NewScheduleStore wraps an established driver connection (see Dial) as a
// port.ScheduleStore. The connection serves BOTH ScheduleStoreService AND
// ScheduleOneShotReArmerService over the same conn; a driver that omits the
// re-arm service is tolerated (ReArmOneShot maps its UNIMPLEMENTED to
// port.ErrScheduleUnsupported).
func NewScheduleStore(conn grpc.ClientConnInterface) *ScheduleStore {
	return &ScheduleStore{
		store:   driverv1.NewScheduleStoreServiceClient(conn),
		reArmer: driverv1.NewScheduleOneShotReArmerServiceClient(conn),
	}
}

// Save encodes s via encoding/json and persists it under s.Spec.Name on the
// driver, overwriting any prior record. The schedule is upserted by name.
func (st *ScheduleStore) Save(ctx context.Context, s port.Schedule) error {
	payload, err := json.Marshal(s)
	if err != nil {
		return fmt.Errorf("grpcdriver: marshal schedule: %w", err)
	}
	if _, err := st.store.SaveSchedule(ctx, &driverv1.SaveScheduleRequest{
		Name:     s.Spec.Name,
		Schedule: &driverv1.ScheduleRecord{Format: ScheduleFormat, Payload: payload},
	}); err != nil {
		return scheduleStatusToErr(ctx, "save schedule", err)
	}
	return nil
}

// Load fetches the schedule stored under name from the driver and decodes it.
// A driver NOT_FOUND maps to port.ErrScheduleNotFound (wrapped); an unknown
// envelope format, a payload that fails to decode, or a decoded schedule
// whose Spec.Name is NOT the requested one (a mis-keyed driver) is an
// infrastructure error, never not-found.
func (st *ScheduleStore) Load(ctx context.Context, name string) (port.Schedule, error) {
	resp, err := st.store.LoadSchedule(ctx, &driverv1.LoadScheduleRequest{Name: name})
	if err != nil {
		return port.Schedule{}, scheduleStatusToErr(ctx, "load schedule", err)
	}
	rec := resp.GetSchedule()
	s, err := decodeScheduleEnvelope(name, rec)
	if err != nil {
		return port.Schedule{}, err
	}
	return s, nil
}

// Delete removes the schedule stored under name on the driver. It is
// idempotent harness-side: a driver NOT_FOUND (a thin driver surfacing its
// primitive's miss) maps to success, per the port.ScheduleStore contract.
func (st *ScheduleStore) Delete(ctx context.Context, name string) error {
	if _, err := st.store.DeleteSchedule(ctx, &driverv1.DeleteScheduleRequest{Name: name}); err != nil {
		if status.Code(err) == codes.NotFound {
			return nil
		}
		return scheduleStatusToErr(ctx, "delete schedule", err)
	}
	return nil
}

// List returns the driver's full stored-schedule inventory. The records are
// decoded harness-side; an unknown envelope format or undecodable payload on
// any record is an infrastructure error.
func (st *ScheduleStore) List(ctx context.Context) ([]port.Schedule, error) {
	resp, err := st.store.ListSchedules(ctx, &driverv1.ListSchedulesRequest{})
	if err != nil {
		return nil, scheduleStatusToErr(ctx, "list schedules", err)
	}
	records := resp.GetSchedules()
	out := make([]port.Schedule, 0, len(records))
	for _, rec := range records {
		s, derr := decodeScheduleEnvelope("", rec)
		if derr != nil {
			return nil, derr
		}
		out = append(out, s)
	}
	return out, nil
}

// Due returns the schedules whose NextFireAt is <= now AND Enabled AND (when
// MaxFires > 0) FireCount < MaxFires. The driver computes the due set; the
// records are decoded harness-side.
func (st *ScheduleStore) Due(ctx context.Context, now time.Time) ([]port.Schedule, error) {
	resp, err := st.store.DueSchedules(ctx, &driverv1.DueSchedulesRequest{Now: timestamppb.New(now)})
	if err != nil {
		return nil, scheduleStatusToErr(ctx, "due schedules", err)
	}
	records := resp.GetSchedules()
	out := make([]port.Schedule, 0, len(records))
	for _, rec := range records {
		s, derr := decodeScheduleEnvelope("", rec)
		if derr != nil {
			return nil, derr
		}
		out = append(out, s)
	}
	return out, nil
}

// Claim is the at-most-once atomic advance over the wire. The driver applies
// it atomically (the durable NextFireAt advance IS the at-most-once fence —
// the harness client cannot fence over the wire); a zero nextFire is the "no
// further fire" sentinel. A driver NOT_FOUND maps to port.ErrScheduleNotFound.
func (st *ScheduleStore) Claim(ctx context.Context, name string, now, nextFire time.Time) (port.Schedule, error) {
	resp, err := st.store.ClaimSchedule(ctx, &driverv1.ClaimScheduleRequest{
		Name:     name,
		Now:      timestamppb.New(now),
		NextFire: scheduleTimeToProto(nextFire),
	})
	if err != nil {
		return port.Schedule{}, scheduleStatusToErr(ctx, "claim schedule", err)
	}
	s, err := decodeScheduleEnvelope(name, resp.GetSchedule())
	if err != nil {
		return port.Schedule{}, err
	}
	return s, nil
}

// ClaimNow is the manual-trigger variant of Claim over the wire (no
// due-check). The driver fences on LastFireAt: a ClaimNow at the SAME now as
// a prior ClaimNow is rejected (the driver maps that to an error the harness
// surfaces). A driver NOT_FOUND maps to port.ErrScheduleNotFound.
func (st *ScheduleStore) ClaimNow(ctx context.Context, name string, now, nextFire time.Time) (port.Schedule, error) {
	resp, err := st.store.ClaimScheduleNow(ctx, &driverv1.ClaimScheduleNowRequest{
		Name:     name,
		Now:      timestamppb.New(now),
		NextFire: scheduleTimeToProto(nextFire),
	})
	if err != nil {
		return port.Schedule{}, scheduleStatusToErr(ctx, "claim-now schedule", err)
	}
	s, err := decodeScheduleEnvelope(name, resp.GetSchedule())
	if err != nil {
		return port.Schedule{}, err
	}
	return s, nil
}

// SetEnabled atomically sets the schedule's Enabled flag. A driver NOT_FOUND
// maps to port.ErrScheduleNotFound.
func (st *ScheduleStore) SetEnabled(ctx context.Context, name string, enabled bool) error {
	if _, err := st.store.SetScheduleEnabled(ctx, &driverv1.SetScheduleEnabledRequest{Name: name, Enabled: enabled}); err != nil {
		return scheduleStatusToErr(ctx, "set-enabled schedule", err)
	}
	return nil
}

// BeginDelete starts or resumes the driver's atomic deletion transaction.
func (st *ScheduleStore) BeginDelete(ctx context.Context, name, deletionID string) (port.Schedule, error) {
	resp, err := st.store.BeginScheduleDelete(ctx, &driverv1.BeginScheduleDeleteRequest{Name: name, DeletionId: deletionID})
	if err != nil {
		return port.Schedule{}, scheduleStatusToErr(ctx, "begin schedule deletion", err)
	}
	return decodeScheduleEnvelope(name, resp.GetSchedule())
}

// CompleteDelete conditionally removes the exact deletion-marked incarnation.
func (st *ScheduleStore) CompleteDelete(ctx context.Context, name, deletionID string) error {
	_, err := st.store.CompleteScheduleDelete(ctx, &driverv1.CompleteScheduleDeleteRequest{Name: name, DeletionId: deletionID})
	if err != nil {
		return scheduleStatusToErr(ctx, "complete schedule deletion", err)
	}
	return nil
}

// RecordFire records the terminal outcome of a fire and clears the in-flight
// state. It is IDEMPOTENT per fire id over the wire (the driver MUST NOT
// duplicate a repeat record); a driver NOT_FOUND (the schedule was deleted
// between Claim and RecordFire) maps to port.ErrScheduleNotFound.
func (st *ScheduleStore) RecordFire(ctx context.Context, f port.ScheduleFire) error {
	payload, err := json.Marshal(f)
	if err != nil {
		return fmt.Errorf("grpcdriver: marshal fire: %w", err)
	}
	if _, err := st.store.RecordFire(ctx, &driverv1.RecordFireRequest{
		FireId: f.ID,
		Fire:   &driverv1.ScheduleFireRecord{Format: ScheduleFormat, Payload: payload},
	}); err != nil {
		return scheduleStatusToErr(ctx, "record fire", err)
	}
	return nil
}

// RecordFireStart persists the IN-FLIGHT fire over the wire. It is IDEMPOTENT
// per fire id. A driver NOT_FOUND (the schedule was deleted between Claim and
// RecordFireStart) maps to port.ErrScheduleNotFound.
func (st *ScheduleStore) RecordFireStart(ctx context.Context, name string, fire port.ScheduleFire) error {
	payload, err := json.Marshal(fire)
	if err != nil {
		return fmt.Errorf("grpcdriver: marshal fire: %w", err)
	}
	if _, err := st.store.RecordFireStart(ctx, &driverv1.RecordFireStartRequest{
		Name:   name,
		FireId: fire.ID,
		Fire:   &driverv1.ScheduleFireRecord{Format: ScheduleFormat, Payload: payload},
	}); err != nil {
		return scheduleStatusToErr(ctx, "record-fire-start", err)
	}
	return nil
}

// RecordFireProgress advances the in-flight fire's last-observed-progress
// instant. It is BEST-EFFORT and IDEMPOTENT over the wire. A driver NOT_FOUND
// (the schedule was deleted) maps to port.ErrScheduleNotFound.
func (st *ScheduleStore) RecordFireProgress(ctx context.Context, name string, fireID string, at time.Time) error {
	if _, err := st.store.RecordFireProgress(ctx, &driverv1.RecordFireProgressRequest{
		Name:   name,
		FireId: fireID,
		At:     timestamppb.New(at),
	}); err != nil {
		return scheduleStatusToErr(ctx, "record-fire-progress", err)
	}
	return nil
}

// LoadFire fetches the fire record stored under fireID from the driver and
// decodes it. A driver NOT_FOUND maps to port.ErrScheduleNotFound (wrapped);
// an unknown envelope format, a payload that fails to decode, or a decoded
// fire whose ID is NOT the requested one (a mis-keyed driver) is an
// infrastructure error, never not-found.
func (st *ScheduleStore) LoadFire(ctx context.Context, fireID string) (port.ScheduleFire, error) {
	resp, err := st.store.LoadFire(ctx, &driverv1.LoadFireRequest{FireId: fireID})
	if err != nil {
		return port.ScheduleFire{}, scheduleStatusToErr(ctx, "load fire", err)
	}
	rec := resp.GetFire()
	f, err := decodeFireEnvelope(fireID, rec)
	if err != nil {
		return port.ScheduleFire{}, err
	}
	return f, nil
}

// ListFires returns the fire records for a schedule. An unknown SCHEDULE maps
// to port.ErrScheduleNotFound; an empty fire list for an existing schedule is
// a successful empty slice.
func (st *ScheduleStore) ListFires(ctx context.Context, scheduleName string) ([]port.ScheduleFire, error) {
	resp, err := st.store.ListFires(ctx, &driverv1.ListFiresRequest{ScheduleName: scheduleName})
	if err != nil {
		return nil, scheduleStatusToErr(ctx, "list fires", err)
	}
	records := resp.GetFires()
	out := make([]port.ScheduleFire, 0, len(records))
	for _, rec := range records {
		f, derr := decodeFireEnvelope("", rec)
		if derr != nil {
			return nil, derr
		}
		out = append(out, f)
	}
	return out, nil
}

// ReArmOneShot re-enables the named one-shot schedule, sets its NextFireAt to
// nextFire, and increments OneShotRetryCount over the wire. The driver applies
// it atomically (the at-most-once fence for the re-arm). A driver NOT_FOUND
// maps to port.ErrScheduleNotFound. A driver that does not serve the re-arm
// service answers UNIMPLEMENTED → port.ErrScheduleUnsupported (the sticky-
// disable sentinel; the production degradation path — see the type doc).
func (st *ScheduleStore) ReArmOneShot(ctx context.Context, name string, nextFire time.Time) error {
	if _, err := st.reArmer.ReArmOneShot(ctx, &driverv1.ReArmOneShotRequest{
		Name:     name,
		NextFire: timestamppb.New(nextFire),
	}); err != nil {
		return scheduleStatusToErr(ctx, "rearm one-shot", err)
	}
	return nil
}

// scheduleTimeToProto maps a time.Time to a *timestamppb.Timestamp: the zero
// time maps to nil (the "no further fire" sentinel on Claim/ClaimNow), a
// non-zero time to timestamppb.New(t). The driver treats an absent next_fire
// as the sentinel.
func scheduleTimeToProto(t time.Time) *timestamppb.Timestamp {
	if t.IsZero() {
		return nil
	}
	return timestamppb.New(t)
}

// protoTimeOrZero maps a *timestamppb.Timestamp back to a time.Time: nil maps
// to the zero time, a present value to .AsTime().
func protoTimeOrZero(ts *timestamppb.Timestamp) time.Time {
	if ts == nil {
		return time.Time{}
	}
	return ts.AsTime()
}

// decodeScheduleEnvelope decodes a ScheduleRecord envelope into a
// port.Schedule. expectedName is the requested name ("" for List/Due, where
// the caller accepts any name); when non-empty, a decoded schedule whose
// Spec.Name differs is a mis-keyed-driver infrastructure error (the
// sess.ID != id guard precedent), never not-found. An unknown envelope format
// is an infrastructure error (never not-found); a payload that fails to
// unmarshal is an infrastructure error.
func decodeScheduleEnvelope(expectedName string, rec *driverv1.ScheduleRecord) (port.Schedule, error) {
	if rec == nil {
		return port.Schedule{}, fmt.Errorf("grpcdriver: schedule envelope is nil")
	}
	if got := rec.GetFormat(); got != ScheduleFormat {
		return port.Schedule{}, fmt.Errorf("grpcdriver: unknown schedule format %q (this client speaks %q)", got, ScheduleFormat)
	}
	var s port.Schedule
	if err := json.Unmarshal(rec.GetPayload(), &s); err != nil {
		return port.Schedule{}, fmt.Errorf("grpcdriver: decode schedule: %w", err)
	}
	if expectedName != "" && s.Spec.Name != expectedName {
		return port.Schedule{}, fmt.Errorf("grpcdriver: load %q: driver returned the schedule of a DIFFERENT name %q (mis-keyed driver)", expectedName, s.Spec.Name)
	}
	return s, nil
}

// decodeFireEnvelope decodes a ScheduleFireRecord envelope into a
// port.ScheduleFire. expectedID is the requested fire id ("" for ListFires,
// where the caller accepts any id); when non-empty, a decoded fire whose ID
// differs is a mis-keyed-driver infrastructure error, never not-found. An
// unknown envelope format or undecodable payload is an infrastructure error.
func decodeFireEnvelope(expectedID string, rec *driverv1.ScheduleFireRecord) (port.ScheduleFire, error) {
	if rec == nil {
		return port.ScheduleFire{}, fmt.Errorf("grpcdriver: fire envelope is nil")
	}
	if got := rec.GetFormat(); got != ScheduleFormat {
		return port.ScheduleFire{}, fmt.Errorf("grpcdriver: unknown fire format %q (this client speaks %q)", got, ScheduleFormat)
	}
	var f port.ScheduleFire
	if err := json.Unmarshal(rec.GetPayload(), &f); err != nil {
		return port.ScheduleFire{}, fmt.Errorf("grpcdriver: decode fire: %w", err)
	}
	if expectedID != "" && f.ID != expectedID {
		return port.ScheduleFire{}, fmt.Errorf("grpcdriver: load fire %q: driver returned the fire of a DIFFERENT id %q (mis-keyed driver)", expectedID, f.ID)
	}
	return f, nil
}

// scheduleStatusToErr maps an RPC status onto the port.ScheduleStore sentinels.
// NOT_FOUND → port.ErrScheduleNotFound (wrapped); UNIMPLEMENTED →
// port.ErrScheduleUnsupported (the sticky-disable signal, the
// ErrLeaseUnsupported precedent); a ctx-done caller wraps ctx.Err() (like
// rpcErr) so errors.Is(_, context.Canceled/DeadlineExceeded) holds
// harness-side; everything else is an opaque infrastructure failure (no
// transient/permanent classification — resilience, if ever needed, is a
// decorator; see the package doc).
func scheduleStatusToErr(ctx context.Context, op string, err error) error {
	switch status.Code(err) {
	case codes.NotFound:
		return fmt.Errorf("grpcdriver: %s: %w (rpc: %v)", op, port.ErrScheduleNotFound, err)
	case codes.Unimplemented:
		return fmt.Errorf("grpcdriver: %s: %w (rpc: %v)", op, port.ErrScheduleUnsupported, err)
	case codes.Aborted:
		return fmt.Errorf("grpcdriver: %s: %w (rpc: %v)", op, port.ErrScheduleDeleting, err)
	case codes.FailedPrecondition:
		return fmt.Errorf("grpcdriver: %s: %w (rpc: %v)", op, port.ErrScheduleActiveFire, err)
	default:
		return rpcErr(ctx, op, err)
	}
}

// scheduleStoreServer adapts a port.ScheduleStore to
// ScheduleStoreServiceServer. It is a thin translator: it decodes the
// envelope to the port value, calls the wrapped port.ScheduleStore, and
// re-encodes. All atomicity lives in the wrapped backend (the wire adds
// nothing — a conforming driver applies Claim/ClaimNow/SetEnabled/RecordFire*
// with the same atomic-advance semantics as the in-process store).
type scheduleStoreServer struct {
	driverv1.UnimplementedScheduleStoreServiceServer
	store port.ScheduleStore
}

// NewScheduleStoreServer wraps st as a ScheduleStoreService driver server.
func NewScheduleStoreServer(st port.ScheduleStore) driverv1.ScheduleStoreServiceServer {
	return &scheduleStoreServer{store: st}
}

// SaveSchedule validates the request (blank name, nil envelope, wrong format,
// empty payload, a name mismatch, an undecodable payload — all
// INVALID_ARGUMENT) and persists the decoded schedule in the wrapped store.
func (s *scheduleStoreServer) SaveSchedule(ctx context.Context, req *driverv1.SaveScheduleRequest) (*driverv1.SaveScheduleResponse, error) {
	if req.GetName() == "" {
		return nil, status.Error(codes.InvalidArgument, "name is required")
	}
	sched, err := validateScheduleRecord(req.GetName(), req.GetSchedule())
	if err != nil {
		return nil, err
	}
	if sched.Spec.Name != req.GetName() {
		return nil, status.Errorf(codes.InvalidArgument,
			"name %q does not match the schedule payload's Spec.Name %q (the top-level name is the storage key; the two must agree)",
			req.GetName(), sched.Spec.Name)
	}
	if err := s.store.Save(ctx, sched); err != nil {
		return nil, scheduleStoreStatus(err)
	}
	return &driverv1.SaveScheduleResponse{}, nil
}

// LoadSchedule fetches the schedule from the wrapped store and re-encodes it
// into the envelope. A store not-found (port.ErrScheduleNotFound) maps to
// NOT_FOUND.
func (s *scheduleStoreServer) LoadSchedule(ctx context.Context, req *driverv1.LoadScheduleRequest) (*driverv1.LoadScheduleResponse, error) {
	if req.GetName() == "" {
		return nil, status.Error(codes.InvalidArgument, "name is required")
	}
	sched, err := s.store.Load(ctx, req.GetName())
	if err != nil {
		return nil, scheduleStoreStatus(err)
	}
	payload, err := json.Marshal(sched)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "encode schedule: %v", err)
	}
	return &driverv1.LoadScheduleResponse{
		Schedule: &driverv1.ScheduleRecord{Format: ScheduleFormat, Payload: payload},
	}, nil
}

// DeleteSchedule removes the schedule from the wrapped store. It is
// idempotent: the port contract says an unknown name is success, so a
// NOT_FOUND from the wrapped store is tolerated and mapped to OK here.
func (s *scheduleStoreServer) DeleteSchedule(ctx context.Context, req *driverv1.DeleteScheduleRequest) (*driverv1.DeleteScheduleResponse, error) {
	if req.GetName() == "" {
		return nil, status.Error(codes.InvalidArgument, "name is required")
	}
	if err := s.store.Delete(ctx, req.GetName()); err != nil {
		// The port contract: Delete is idempotent (unknown name = success).
		// A wrapped store that returns ErrScheduleNotFound is tolerated and
		// mapped to OK here — mirroring the harness client's driver-NOT_FOUND
		// tolerance (the wire client maps a driver NOT_FOUND to nil too).
		if isScheduleNotFound(err) {
			return &driverv1.DeleteScheduleResponse{}, nil
		}
		return nil, scheduleStoreStatus(err)
	}
	return &driverv1.DeleteScheduleResponse{}, nil
}

// ListSchedules returns the wrapped store's full inventory, re-encoded.
func (s *scheduleStoreServer) ListSchedules(ctx context.Context, _ *driverv1.ListSchedulesRequest) (*driverv1.ListSchedulesResponse, error) {
	schedules, err := s.store.List(ctx)
	if err != nil {
		return nil, scheduleStoreStatus(err)
	}
	out := make([]*driverv1.ScheduleRecord, 0, len(schedules))
	for _, sched := range schedules {
		payload, merr := json.Marshal(sched)
		if merr != nil {
			return nil, status.Errorf(codes.Internal, "encode schedule: %v", merr)
		}
		out = append(out, &driverv1.ScheduleRecord{Format: ScheduleFormat, Payload: payload})
	}
	return &driverv1.ListSchedulesResponse{Schedules: out}, nil
}

// DueSchedules returns the due schedules from the wrapped store, re-encoded.
func (s *scheduleStoreServer) DueSchedules(ctx context.Context, req *driverv1.DueSchedulesRequest) (*driverv1.DueSchedulesResponse, error) {
	if req.GetNow() == nil {
		return nil, status.Error(codes.InvalidArgument, "now is required")
	}
	schedules, err := s.store.Due(ctx, req.GetNow().AsTime())
	if err != nil {
		return nil, scheduleStoreStatus(err)
	}
	out := make([]*driverv1.ScheduleRecord, 0, len(schedules))
	for _, sched := range schedules {
		payload, merr := json.Marshal(sched)
		if merr != nil {
			return nil, status.Errorf(codes.Internal, "encode schedule: %v", merr)
		}
		out = append(out, &driverv1.ScheduleRecord{Format: ScheduleFormat, Payload: payload})
	}
	return &driverv1.DueSchedulesResponse{Schedules: out}, nil
}

// ClaimSchedule performs the atomic advance in the wrapped store and returns
// the updated schedule, re-encoded.
func (s *scheduleStoreServer) ClaimSchedule(ctx context.Context, req *driverv1.ClaimScheduleRequest) (*driverv1.ClaimScheduleResponse, error) {
	if req.GetName() == "" {
		return nil, status.Error(codes.InvalidArgument, "name is required")
	}
	if req.GetNow() == nil {
		return nil, status.Error(codes.InvalidArgument, "now is required")
	}
	sched, err := s.store.Claim(ctx, req.GetName(), req.GetNow().AsTime(), protoTimeOrZero(req.GetNextFire()))
	if err != nil {
		return nil, scheduleStoreStatus(err)
	}
	payload, err := json.Marshal(sched)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "encode schedule: %v", err)
	}
	return &driverv1.ClaimScheduleResponse{
		Schedule: &driverv1.ScheduleRecord{Format: ScheduleFormat, Payload: payload},
	}, nil
}

// ClaimScheduleNow performs the manual-trigger atomic advance in the wrapped
// store and returns the updated schedule, re-encoded.
func (s *scheduleStoreServer) ClaimScheduleNow(ctx context.Context, req *driverv1.ClaimScheduleNowRequest) (*driverv1.ClaimScheduleNowResponse, error) {
	if req.GetName() == "" {
		return nil, status.Error(codes.InvalidArgument, "name is required")
	}
	if req.GetNow() == nil {
		return nil, status.Error(codes.InvalidArgument, "now is required")
	}
	sched, err := s.store.ClaimNow(ctx, req.GetName(), req.GetNow().AsTime(), protoTimeOrZero(req.GetNextFire()))
	if err != nil {
		return nil, scheduleStoreStatus(err)
	}
	payload, err := json.Marshal(sched)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "encode schedule: %v", err)
	}
	return &driverv1.ClaimScheduleNowResponse{
		Schedule: &driverv1.ScheduleRecord{Format: ScheduleFormat, Payload: payload},
	}, nil
}

// SetScheduleEnabled sets the Enabled flag in the wrapped store.
func (s *scheduleStoreServer) SetScheduleEnabled(ctx context.Context, req *driverv1.SetScheduleEnabledRequest) (*driverv1.SetScheduleEnabledResponse, error) {
	if req.GetName() == "" {
		return nil, status.Error(codes.InvalidArgument, "name is required")
	}
	if err := s.store.SetEnabled(ctx, req.GetName(), req.GetEnabled()); err != nil {
		return nil, scheduleStoreStatus(err)
	}
	return &driverv1.SetScheduleEnabledResponse{}, nil
}

func (s *scheduleStoreServer) BeginScheduleDelete(ctx context.Context, req *driverv1.BeginScheduleDeleteRequest) (*driverv1.BeginScheduleDeleteResponse, error) {
	if req.GetName() == "" || req.GetDeletionId() == "" {
		return nil, status.Error(codes.InvalidArgument, "name and deletion_id are required")
	}
	store, ok := s.store.(port.ScheduleDeletionStore)
	if !ok {
		return nil, status.Error(codes.Unimplemented, "atomic schedule deletion is not supported")
	}
	sched, err := store.BeginDelete(ctx, req.GetName(), req.GetDeletionId())
	if err != nil {
		return nil, scheduleStoreStatus(err)
	}
	payload, err := json.Marshal(sched)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "encode schedule: %v", err)
	}
	return &driverv1.BeginScheduleDeleteResponse{Schedule: &driverv1.ScheduleRecord{Format: ScheduleFormat, Payload: payload}}, nil
}

func (s *scheduleStoreServer) CompleteScheduleDelete(ctx context.Context, req *driverv1.CompleteScheduleDeleteRequest) (*driverv1.CompleteScheduleDeleteResponse, error) {
	if req.GetName() == "" || req.GetDeletionId() == "" {
		return nil, status.Error(codes.InvalidArgument, "name and deletion_id are required")
	}
	store, ok := s.store.(port.ScheduleDeletionStore)
	if !ok {
		return nil, status.Error(codes.Unimplemented, "atomic schedule deletion is not supported")
	}
	if err := store.CompleteDelete(ctx, req.GetName(), req.GetDeletionId()); err != nil {
		return nil, scheduleStoreStatus(err)
	}
	return &driverv1.CompleteScheduleDeleteResponse{}, nil
}

// RecordFire records the terminal fire outcome in the wrapped store. It
// validates the envelope (blank fire_id, nil envelope, wrong format, empty
// payload, an id mismatch, an undecodable payload — all INVALID_ARGUMENT).
func (s *scheduleStoreServer) RecordFire(ctx context.Context, req *driverv1.RecordFireRequest) (*driverv1.RecordFireResponse, error) {
	if req.GetFireId() == "" {
		return nil, status.Error(codes.InvalidArgument, "fire_id is required")
	}
	fire, err := validateFireRecord(req.GetFireId(), req.GetFire())
	if err != nil {
		return nil, err
	}
	if fire.ID != req.GetFireId() {
		return nil, status.Errorf(codes.InvalidArgument,
			"fire_id %q does not match the fire payload's ID %q (the top-level fire_id is the storage key; the two must agree)",
			req.GetFireId(), fire.ID)
	}
	if err := s.store.RecordFire(ctx, fire); err != nil {
		return nil, scheduleStoreStatus(err)
	}
	return &driverv1.RecordFireResponse{}, nil
}

// RecordFireStart persists the in-flight fire in the wrapped store. It
// validates the envelope (blank name, blank fire_id, nil envelope, wrong
// format, empty payload, an id mismatch, an undecodable payload — all
// INVALID_ARGUMENT).
func (s *scheduleStoreServer) RecordFireStart(ctx context.Context, req *driverv1.RecordFireStartRequest) (*driverv1.RecordFireStartResponse, error) {
	if req.GetName() == "" {
		return nil, status.Error(codes.InvalidArgument, "name is required")
	}
	if req.GetFireId() == "" {
		return nil, status.Error(codes.InvalidArgument, "fire_id is required")
	}
	fire, err := validateFireRecord(req.GetFireId(), req.GetFire())
	if err != nil {
		return nil, err
	}
	if fire.ID != req.GetFireId() {
		return nil, status.Errorf(codes.InvalidArgument,
			"fire_id %q does not match the fire payload's ID %q (the top-level fire_id is the storage key; the two must agree)",
			req.GetFireId(), fire.ID)
	}
	if err := s.store.RecordFireStart(ctx, req.GetName(), fire); err != nil {
		return nil, scheduleStoreStatus(err)
	}
	return &driverv1.RecordFireStartResponse{}, nil
}

// RecordFireProgress advances the in-flight fire's progress instant in the
// wrapped store.
func (s *scheduleStoreServer) RecordFireProgress(ctx context.Context, req *driverv1.RecordFireProgressRequest) (*driverv1.RecordFireProgressResponse, error) {
	if req.GetName() == "" {
		return nil, status.Error(codes.InvalidArgument, "name is required")
	}
	if req.GetFireId() == "" {
		return nil, status.Error(codes.InvalidArgument, "fire_id is required")
	}
	if req.GetAt() == nil {
		return nil, status.Error(codes.InvalidArgument, "at is required")
	}
	if err := s.store.RecordFireProgress(ctx, req.GetName(), req.GetFireId(), req.GetAt().AsTime()); err != nil {
		return nil, scheduleStoreStatus(err)
	}
	return &driverv1.RecordFireProgressResponse{}, nil
}

// LoadFire fetches the fire record from the wrapped store and re-encodes it.
// A store not-found (port.ErrScheduleNotFound) maps to NOT_FOUND.
func (s *scheduleStoreServer) LoadFire(ctx context.Context, req *driverv1.LoadFireRequest) (*driverv1.LoadFireResponse, error) {
	if req.GetFireId() == "" {
		return nil, status.Error(codes.InvalidArgument, "fire_id is required")
	}
	fire, err := s.store.LoadFire(ctx, req.GetFireId())
	if err != nil {
		return nil, scheduleStoreStatus(err)
	}
	payload, err := json.Marshal(fire)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "encode fire: %v", err)
	}
	return &driverv1.LoadFireResponse{
		Fire: &driverv1.ScheduleFireRecord{Format: ScheduleFormat, Payload: payload},
	}, nil
}

// ListFires returns the wrapped store's fire records for a schedule,
// re-encoded. A store not-found (the SCHEDULE) maps to NOT_FOUND; an empty
// fire list for an existing schedule is a successful empty slice.
func (s *scheduleStoreServer) ListFires(ctx context.Context, req *driverv1.ListFiresRequest) (*driverv1.ListFiresResponse, error) {
	if req.GetScheduleName() == "" {
		return nil, status.Error(codes.InvalidArgument, "schedule_name is required")
	}
	fires, err := s.store.ListFires(ctx, req.GetScheduleName())
	if err != nil {
		return nil, scheduleStoreStatus(err)
	}
	out := make([]*driverv1.ScheduleFireRecord, 0, len(fires))
	for _, fire := range fires {
		payload, merr := json.Marshal(fire)
		if merr != nil {
			return nil, status.Errorf(codes.Internal, "encode fire: %v", merr)
		}
		out = append(out, &driverv1.ScheduleFireRecord{Format: ScheduleFormat, Payload: payload})
	}
	return &driverv1.ListFiresResponse{Fires: out}, nil
}

// validateScheduleRecord rejects a missing envelope, an unknown format, or an
// empty payload (all INVALID_ARGUMENT), and returns the decoded schedule. It
// does NOT check the name match — the caller does (the name lives at the
// top-level request, not in the envelope validation).
func validateScheduleRecord(_ string, rec *driverv1.ScheduleRecord) (port.Schedule, error) {
	if rec == nil {
		return port.Schedule{}, status.Error(codes.InvalidArgument, "schedule is required")
	}
	if got := rec.GetFormat(); got != ScheduleFormat {
		return port.Schedule{}, status.Errorf(codes.InvalidArgument, "unknown schedule format %q (this server speaks %q)", got, ScheduleFormat)
	}
	if len(rec.GetPayload()) == 0 {
		return port.Schedule{}, status.Error(codes.InvalidArgument, "schedule payload is empty")
	}
	var sched port.Schedule
	if err := json.Unmarshal(rec.GetPayload(), &sched); err != nil {
		return port.Schedule{}, status.Errorf(codes.InvalidArgument, "decode schedule: %v", err)
	}
	return sched, nil
}

// validateFireRecord rejects a missing envelope, an unknown format, or an
// empty payload (all INVALID_ARGUMENT), and returns the decoded fire. The
// caller checks the id match.
func validateFireRecord(_ string, rec *driverv1.ScheduleFireRecord) (port.ScheduleFire, error) {
	if rec == nil {
		return port.ScheduleFire{}, status.Error(codes.InvalidArgument, "fire is required")
	}
	if got := rec.GetFormat(); got != ScheduleFormat {
		return port.ScheduleFire{}, status.Errorf(codes.InvalidArgument, "unknown fire format %q (this server speaks %q)", got, ScheduleFormat)
	}
	if len(rec.GetPayload()) == 0 {
		return port.ScheduleFire{}, status.Error(codes.InvalidArgument, "fire payload is empty")
	}
	var fire port.ScheduleFire
	if err := json.Unmarshal(rec.GetPayload(), &fire); err != nil {
		return port.ScheduleFire{}, status.Errorf(codes.InvalidArgument, "decode fire: %v", err)
	}
	return fire, nil
}

// scheduleStoreStatus maps a wrapped store's error onto the driver protocol's
// status vocabulary: the not-found sentinel → NOT_FOUND, the unsupported
// sentinel → UNIMPLEMENTED, context errors → CANCELLED / DEADLINE_EXCEEDED,
// everything else → INTERNAL. (Mirrors storeStatus/leaseStatus.)
func scheduleStoreStatus(err error) error {
	switch {
	case isScheduleNotFound(err):
		return status.Error(codes.NotFound, err.Error())
	case errors.Is(err, port.ErrScheduleDeleting):
		return status.Error(codes.Aborted, err.Error())
	case errors.Is(err, port.ErrScheduleActiveFire):
		return status.Error(codes.FailedPrecondition, err.Error())
	case isScheduleUnsupported(err):
		return status.Error(codes.Unimplemented, err.Error())
	case errors.Is(err, context.Canceled):
		return status.Error(codes.Canceled, err.Error())
	case errors.Is(err, context.DeadlineExceeded):
		return status.Error(codes.DeadlineExceeded, err.Error())
	default:
		return status.Error(codes.Internal, err.Error())
	}
}

// isScheduleNotFound reports whether err wraps port.ErrScheduleNotFound.
func isScheduleNotFound(err error) bool { return errors.Is(err, port.ErrScheduleNotFound) }

// isScheduleUnsupported reports whether err wraps port.ErrScheduleUnsupported.
func isScheduleUnsupported(err error) bool { return errors.Is(err, port.ErrScheduleUnsupported) }

// scheduleOneShotReArmerServer adapts a port.ScheduleOneShotReArmer to
// ScheduleOneShotReArmerServiceServer. It is a thin translator; the atomicity
// lives in the wrapped re-armer.
type scheduleOneShotReArmerServer struct {
	driverv1.UnimplementedScheduleOneShotReArmerServiceServer
	reArmer port.ScheduleOneShotReArmer
}

// NewScheduleOneShotReArmerServer wraps reArmer as a
// ScheduleOneShotReArmerService driver server.
func NewScheduleOneShotReArmerServer(reArmer port.ScheduleOneShotReArmer) driverv1.ScheduleOneShotReArmerServiceServer {
	return &scheduleOneShotReArmerServer{reArmer: reArmer}
}

// ReArmOneShot validates the request (blank name, nil next_fire — both
// INVALID_ARGUMENT) and re-arms the one-shot schedule in the wrapped re-armer.
func (s *scheduleOneShotReArmerServer) ReArmOneShot(ctx context.Context, req *driverv1.ReArmOneShotRequest) (*driverv1.ReArmOneShotResponse, error) {
	if req.GetName() == "" {
		return nil, status.Error(codes.InvalidArgument, "name is required")
	}
	if req.GetNextFire() == nil {
		return nil, status.Error(codes.InvalidArgument, "next_fire is required")
	}
	if err := s.reArmer.ReArmOneShot(ctx, req.GetName(), req.GetNextFire().AsTime()); err != nil {
		return nil, scheduleStoreStatus(err)
	}
	return &driverv1.ReArmOneShotResponse{}, nil
}
