package grpcdriver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"iter"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	driverv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/driver/v1"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

// EventLogFormat is the event-log envelope format tag this client writes on
// Append and accepts on Read: the payload is exactly json.Marshal of a
// session.Event (one event per message). It is the SAME tag the local
// jsonlstore writes inside its {"v":...,"ev":...} on-disk record
// (jsonlstore.eventLogFormat) — the wire and the file version the SAME event
// encoding, so a log written by one and read by the other agrees. The driver
// round-trips the tag verbatim; it changes only if the event encoding itself
// is replaced (session.Event's own schema evolution is additive and needs no
// bump). Read rejects any other tag as an infrastructure fault — a
// forward-incompatible log must fail loud, never silently skip.
//
// SIGNPOST — a future format bump MUST be read-set-accept / write-newest: the
// readers (this client's Read, the server wrapper's Append decode-or-passthrough)
// must keep ACCEPTING every previously-shipped tag while Append WRITES only the
// newest. The driver round-trips envelopes verbatim and cannot migrate them.
const EventLogFormat = "eventlog-json/1"

// EventLog is a port.EventLog over a remote EventLogService driver. Encode
// (session.Event → JSON) happens HERE on Append and decode (JSON →
// session.Event) HERE on Read, harness-side: the driver only ever sees the
// opaque format-tagged envelope, exactly as the SessionStore driver keeps
// sessnap harness-side.
type EventLog struct {
	client driverv1.EventLogServiceClient
}

// compile-time assertion that EventLog satisfies the port.
var _ port.EventLog = (*EventLog)(nil)

// NewEventLog wraps an established driver connection (see Dial) as a
// port.EventLog.
func NewEventLog(conn grpc.ClientConnInterface) *EventLog {
	return &EventLog{client: driverv1.NewEventLogServiceClient(conn)}
}

// Append encodes ev to its session.Event JSON and records it under id on the
// driver, in append order. A marshal failure is a client-side error (no RPC).
// The relay treats any non-nil error as "not recorded" and WARNs.
func (l *EventLog) Append(ctx context.Context, id session.SessionID, ev session.Event) error {
	payload, err := json.Marshal(ev)
	if err != nil {
		return fmt.Errorf("grpcdriver: marshal event: %w", err)
	}
	if _, err := l.client.Append(ctx, &driverv1.AppendRequest{
		SessionId: string(id),
		Event:     &driverv1.LoggedEvent{Format: EventLogFormat, Payload: payload},
	}); err != nil {
		return rpcErr(ctx, "append event", err)
	}
	return nil
}

// Read streams the session's recorded events from the driver and decodes each
// envelope back into a session.Event, yielding them in append order. An empty
// stream (a session never appended to) yields an EMPTY sequence — absence is
// data, never an error. A mid-stream fault — an unknown envelope format, a
// payload that fails to decode, or an RPC/stream error — yields
// (session.Event{}, err) and then stops, honouring the port.EventLog contract
// (Read yields no further events after an error).
func (l *EventLog) Read(ctx context.Context, id session.SessionID) iter.Seq2[session.Event, error] {
	return func(yield func(session.Event, error) bool) {
		// A child context cancelled on early break releases the stream's
		// resources (the iter.Seq2 early-exit obligation).
		streamCtx, cancel := context.WithCancel(ctx)
		defer cancel()

		stream, err := l.client.Read(streamCtx, &driverv1.ReadRequest{SessionId: string(id)})
		if err != nil {
			yield(session.Event{}, rpcErr(ctx, "read events", err))
			return
		}
		for {
			resp, err := stream.Recv()
			if errors.Is(err, io.EOF) {
				return // clean end of stream (empty stream → empty sequence)
			}
			if err != nil {
				yield(session.Event{}, rpcErr(ctx, "read events", err))
				return
			}
			env := resp.GetEvent()
			if got := env.GetFormat(); got != EventLogFormat {
				yield(session.Event{}, fmt.Errorf("grpcdriver: read events %q: unknown event format %q (this client speaks %q)", id, got, EventLogFormat))
				return
			}
			var ev session.Event
			if err := json.Unmarshal(env.GetPayload(), &ev); err != nil {
				yield(session.Event{}, fmt.Errorf("grpcdriver: read events %q: decode event: %w", id, err))
				return
			}
			if !yield(ev, nil) {
				return // consumer broke out early; cancel releases the stream
			}
		}
	}
}

// eventLogServer adapts a port.EventLog to EventLogServiceServer. It confines
// all proto/status translation — the wrapped backend speaks only the port.
//
// The wrapper round-trips the OPAQUE envelope: Append stores (format, payload)
// verbatim by re-decoding the payload into a session.Event for the backend's
// value-typed port (the wire carries bytes; the port carries a value), and
// Read re-encodes each backend event into the same envelope. The wrapped
// backend NEVER sees the wire bytes — exactly the SessionStore server
// posture, which decodes the snapshot into a *session.Session for its
// value-typed port.
type eventLogServer struct {
	driverv1.UnimplementedEventLogServiceServer
	log port.EventLog
}

// NewEventLogServer wraps log as an EventLogService driver server.
func NewEventLogServer(log port.EventLog) driverv1.EventLogServiceServer {
	return &eventLogServer{log: log}
}

// Append validates the envelope (rejecting a blank session_id, a missing
// envelope, a non-EventLogFormat tag, or an empty/undecodable payload — all
// INVALID_ARGUMENT) and records the restored event in the wrapped log.
func (s *eventLogServer) Append(ctx context.Context, req *driverv1.AppendRequest) (*driverv1.AppendResponse, error) {
	if req.GetSessionId() == "" {
		return nil, status.Error(codes.InvalidArgument, "session_id is required")
	}
	env := req.GetEvent()
	if env == nil {
		return nil, status.Error(codes.InvalidArgument, "event is required")
	}
	if got := env.GetFormat(); got != EventLogFormat {
		return nil, status.Errorf(codes.InvalidArgument, "unknown event format %q (this server speaks %q)", got, EventLogFormat)
	}
	if len(env.GetPayload()) == 0 {
		return nil, status.Error(codes.InvalidArgument, "event payload is empty")
	}
	var ev session.Event
	if err := json.Unmarshal(env.GetPayload(), &ev); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "decode event: %v", err)
	}
	id := session.SessionID(req.GetSessionId())
	// ONE write RPC serves both ports. When the backend implements the cursor
	// half, append through it so the response can report WHERE the record
	// landed; otherwise append through the shipped port and leave the cursor
	// unset, which an EventLog-only client already ignores.
	//
	// A second "AppendEvent" RPC was the alternative and is worse: two write
	// paths would have to agree on ordering and durability for the same log, and
	// a client calling the wrong one against a cursor-capable driver would get a
	// silently position-less append.
	if cursor, ok := s.log.(port.CursorEventLog); ok {
		at, err := cursor.AppendEvent(ctx, id, ev)
		if err != nil {
			return nil, eventLogStatus(err)
		}
		return &driverv1.AppendResponse{Cursor: string(at)}, nil
	}
	if err := s.log.Append(ctx, id, ev); err != nil {
		return nil, eventLogStatus(err)
	}
	return &driverv1.AppendResponse{}, nil
}

// Read replays the wrapped log's events for the session, re-encoding each into
// the opaque envelope and streaming it. An empty log yields an empty stream (no
// NOT_FOUND). A backend Read error (yielded mid-iteration) terminates the
// stream with a non-OK status; a marshal or Send failure does the same.
func (s *eventLogServer) Read(req *driverv1.ReadRequest, stream grpc.ServerStreamingServer[driverv1.ReadResponse]) error {
	if req.GetSessionId() == "" {
		return status.Error(codes.InvalidArgument, "session_id is required")
	}
	ctx := stream.Context()
	for ev, err := range s.log.Read(ctx, session.SessionID(req.GetSessionId())) {
		if err != nil {
			return eventLogStatus(err)
		}
		payload, merr := json.Marshal(ev)
		if merr != nil {
			return status.Errorf(codes.Internal, "encode event: %v", merr)
		}
		if serr := stream.Send(&driverv1.ReadResponse{
			Event: &driverv1.LoggedEvent{Format: EventLogFormat, Payload: payload},
		}); serr != nil {
			return serr
		}
	}
	return nil
}
