package grpcdriver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"iter"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	driverv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/driver/v1"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

// This file is the driver half of ADR 0250. The cursor is OPAQUE on this
// transport in the strongest sense: unlike every other backend, this client does
// not encode or decode one. The remote driver owns the encoding AND the
// generation basis, and the client round-trips the string verbatim — which is
// what lets a driver back its log with storage whose positions this repo knows
// nothing about.

const (
	// cursorErrorDomain scopes the ErrorInfo reasons below. ADR 0248 pinned
	// google.rpc.ErrorInfo as the gRPC error carrier for the harness API; the
	// driver protocol reuses it rather than inventing a second mechanism.
	cursorErrorDomain = "mecatl.stacklok.com"

	// reasonCursorMalformed / reasonCursorExpired travel in an ErrorInfo so the
	// client can restore the EXACT sentinel.
	//
	// A status CODE alone would not do: InvalidArgument also carries "session_id
	// is required", so mapping by code would turn a request-shape bug into a
	// "your cursor is corrupt" report and send the consumer to restart from the
	// beginning for no reason. The two sentinels are behaviourally different
	// (expired is retryable from the beginning; malformed indicates a bug or
	// tampering), so the distinction has to survive the wire intact.
	reasonCursorMalformed = "cursor_malformed"
	reasonCursorExpired   = "cursor_expired"
)

// ErrDriverCursorUnsupported reports that the remote driver does not implement
// the cursor RPCs — an older driver process speaking only port.EventLog.
//
// It exists because a type assertion cannot answer this question across a wire:
// this client satisfies port.CursorEventLog by construction, so composition
// would otherwise believe every driver supports cursors and discover otherwise
// only when a watch failed. ADR 0250 requires that a backend without cursor
// support be reported as UNSUPPORTED rather than silently degraded, and this is
// the value that makes that reportable.
var ErrDriverCursorUnsupported = errors.New("grpcdriver: remote driver does not support event-log cursors")

// compile-time assertion that EventLog satisfies the cursor port too.
var _ port.CursorEventLog = (*EventLog)(nil)

// AppendEvent records ev on the driver and returns the cursor it reports.
//
// The returned cursor is the driver's own token, passed through untouched. This
// client never calls port.EncodeCursor: doing so would wrap a token whose
// generation basis lives in another process, and the harness has no basis of its
// own to put in it.
func (l *EventLog) AppendEvent(ctx context.Context, id session.SessionID, ev session.Event) (port.Cursor, error) {
	payload, err := json.Marshal(ev)
	if err != nil {
		return "", fmt.Errorf("grpcdriver: marshal event: %w", err)
	}
	resp, err := l.client.Append(ctx, &driverv1.AppendRequest{
		SessionId: string(id),
		Event:     &driverv1.LoggedEvent{Format: EventLogFormat, Payload: payload},
	})
	if err != nil {
		return "", cursorRPCErr(ctx, "append event", err)
	}
	return port.Cursor(resp.GetCursor()), nil
}

// AppendGap records a gap marker on the driver and returns its cursor.
func (l *EventLog) AppendGap(ctx context.Context, id session.SessionID, reason string) (port.Cursor, error) {
	resp, err := l.client.AppendGap(ctx, &driverv1.AppendGapRequest{
		SessionId: string(id),
		Reason:    reason,
	})
	if err != nil {
		return "", cursorRPCErr(ctx, "append gap", err)
	}
	return port.Cursor(resp.GetCursor()), nil
}

// ReadAfter streams the driver's records after the given cursor.
//
// A cursor rejection arrives as a status carrying an ErrorInfo reason and is
// restored to the exact sentinel, so errors.Is(err, port.ErrCursorExpired)
// works identically against a remote driver and a local store.
func (l *EventLog) ReadAfter(ctx context.Context, id session.SessionID, after port.Cursor, opts port.ReadOptions) iter.Seq2[port.LogRecord, error] {
	return func(yield func(port.LogRecord, error) bool) {
		// A child context cancelled on early break releases the stream's
		// resources (the iter.Seq2 early-exit obligation).
		streamCtx, cancel := context.WithCancel(ctx)
		defer cancel()

		stream, err := l.client.ReadAfter(streamCtx, &driverv1.ReadAfterRequest{
			SessionId: string(id),
			Cursor:    string(after),
			Limit:     int32(opts.Limit), //nolint:gosec // a page limit is far below int32.
			Follow:    opts.Follow,
		})
		if err != nil {
			yield(port.LogRecord{}, cursorRPCErr(ctx, "read events after", err))
			return
		}
		for {
			resp, err := stream.Recv()
			if errors.Is(err, io.EOF) {
				return // clean end of stream
			}
			if err != nil {
				if ctx.Err() != nil {
					// A cancelled follow ends CLEANLY: cancellation is how a
					// watch is meant to stop.
					return
				}
				yield(port.LogRecord{}, cursorRPCErr(ctx, "read events after", err))
				return
			}
			rec, err := logRecordFromProto(id, resp)
			if err != nil {
				yield(port.LogRecord{}, err)
				return
			}
			if !yield(rec, nil) {
				return // consumer broke out early; cancel releases the stream
			}
		}
	}
}

// logRecordFromProto restores one streamed record, rejecting an unknown kind
// rather than guessing.
func logRecordFromProto(id session.SessionID, resp *driverv1.ReadAfterResponse) (port.LogRecord, error) {
	rec := port.LogRecord{
		Kind:      port.LogRecordKind(resp.GetKind()),
		GapReason: resp.GetGapReason(),
		Cursor:    port.Cursor(resp.GetCursor()),
		Live:      resp.GetLive(),
	}
	switch rec.Kind {
	case port.LogRecordGap:
		// A gap carries no event, by construction (ADR 0250 decision 5).
		return rec, nil
	case port.LogRecordEvent:
		env := resp.GetEvent()
		if got := env.GetFormat(); got != EventLogFormat {
			return port.LogRecord{}, fmt.Errorf("grpcdriver: read events %q: unknown event format %q (this client speaks %q)", id, got, EventLogFormat)
		}
		if err := json.Unmarshal(env.GetPayload(), &rec.Event); err != nil {
			return port.LogRecord{}, fmt.Errorf("grpcdriver: read events %q: decode event: %w", id, err)
		}
		return rec, nil
	default:
		// An unknown kind is NOT treated as an event. `kind` is an open string so
		// a future record kind is a minor release, but this client cannot know
		// what such a record MEANS, and silently folding it into the event stream
		// is how an unknown delivery signal becomes a fabricated transcript entry.
		return port.LogRecord{}, fmt.Errorf("grpcdriver: read events %q: unknown record kind %q", id, rec.Kind)
	}
}

// cursorRPCErr maps a driver status onto the port's cursor sentinels, falling
// back to the shared rpcErr translation.
func cursorRPCErr(ctx context.Context, op string, err error) error {
	st, ok := status.FromError(err)
	if !ok {
		return rpcErr(ctx, op, err)
	}
	if st.Code() == codes.Unimplemented {
		return fmt.Errorf("grpcdriver: %s: %w", op, ErrDriverCursorUnsupported)
	}
	for _, detail := range st.Details() {
		info, isInfo := detail.(*errdetails.ErrorInfo)
		if !isInfo || info.GetDomain() != cursorErrorDomain {
			continue
		}
		switch info.GetReason() {
		case reasonCursorMalformed:
			return fmt.Errorf("grpcdriver: %s: %w: %s", op, port.ErrCursorMalformed, st.Message())
		case reasonCursorExpired:
			return fmt.Errorf("grpcdriver: %s: %w: %s", op, port.ErrCursorExpired, st.Message())
		}
	}
	return rpcErr(ctx, op, err)
}

// cursorStatus is the server-side inverse: it stamps the sentinel's reason into
// an ErrorInfo so the client can restore it exactly.
func cursorStatus(err error) error {
	switch {
	case errors.Is(err, port.ErrCursorMalformed):
		return cursorStatusWithReason(codes.InvalidArgument, reasonCursorMalformed, err)
	case errors.Is(err, port.ErrCursorExpired):
		return cursorStatusWithReason(codes.FailedPrecondition, reasonCursorExpired, err)
	default:
		return eventLogStatus(err)
	}
}

func cursorStatusWithReason(code codes.Code, reason string, err error) error {
	st := status.New(code, err.Error())
	withInfo, attachErr := st.WithDetails(&errdetails.ErrorInfo{
		Reason: reason,
		Domain: cursorErrorDomain,
	})
	if attachErr != nil {
		// Attaching a detail can only fail on a marshalling fault. The bare
		// status still carries the right CODE, so degrade rather than replacing a
		// precise cursor rejection with an opaque Internal.
		return st.Err()
	}
	return withInfo.Err()
}

// cursorLog reports the wrapped backend's cursor half, or Unimplemented.
//
// UNIMPLEMENTED rather than Internal is the load-bearing choice: it is the wire
// spelling of "this backend did not opt in", which the client turns back into
// ErrDriverCursorUnsupported so composition can advertise the feature as
// unsupported. Reporting Internal would make a deliberate non-adoption look like
// a driver fault.
func (s *eventLogServer) cursorLog() (port.CursorEventLog, error) {
	cursor, ok := s.log.(port.CursorEventLog)
	if !ok {
		return nil, status.Error(codes.Unimplemented, "this driver's event log does not support cursors")
	}
	return cursor, nil
}

// Append records the event and reports the cursor when the backend has one.
//
// It overrides nothing: the base Append is defined in eventlog.go and this file
// only adds the cursor-bearing RPCs. The cursor is reported on AppendEvent's
// behalf through the SAME AppendRequest, so a cursor-capable backend needs no
// second write path.
func (s *eventLogServer) AppendGap(ctx context.Context, req *driverv1.AppendGapRequest) (*driverv1.AppendResponse, error) {
	if req.GetSessionId() == "" {
		return nil, status.Error(codes.InvalidArgument, "session_id is required")
	}
	log, err := s.cursorLog()
	if err != nil {
		return nil, err
	}
	cursor, err := log.AppendGap(ctx, session.SessionID(req.GetSessionId()), req.GetReason())
	if err != nil {
		return nil, cursorStatus(err)
	}
	return &driverv1.AppendResponse{Cursor: string(cursor)}, nil
}

// ReadAfter streams the wrapped log's records after the request's cursor.
func (s *eventLogServer) ReadAfter(req *driverv1.ReadAfterRequest, stream grpc.ServerStreamingServer[driverv1.ReadAfterResponse]) error {
	if req.GetSessionId() == "" {
		return status.Error(codes.InvalidArgument, "session_id is required")
	}
	if req.GetLimit() < 0 {
		return status.Error(codes.InvalidArgument, "limit must not be negative")
	}
	log, err := s.cursorLog()
	if err != nil {
		return err
	}
	ctx := stream.Context()
	for rec, err := range log.ReadAfter(ctx, session.SessionID(req.GetSessionId()), port.Cursor(req.GetCursor()), port.ReadOptions{
		Limit:  int(req.GetLimit()),
		Follow: req.GetFollow(),
	}) {
		if err != nil {
			return cursorStatus(err)
		}
		resp, merr := logRecordToProto(rec)
		if merr != nil {
			return merr
		}
		if serr := stream.Send(resp); serr != nil {
			return serr
		}
	}
	return nil
}

// logRecordToProto projects one record onto the wire.
func logRecordToProto(rec port.LogRecord) (*driverv1.ReadAfterResponse, error) {
	out := &driverv1.ReadAfterResponse{
		Kind:      string(rec.Kind),
		GapReason: rec.GapReason,
		Cursor:    string(rec.Cursor),
		Live:      rec.Live,
	}
	if rec.Kind == port.LogRecordGap {
		return out, nil
	}
	payload, err := json.Marshal(rec.Event)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "encode event: %v", err)
	}
	out.Event = &driverv1.LoggedEvent{Format: EventLogFormat, Payload: payload}
	return out, nil
}
