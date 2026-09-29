package grpcdriver

import (
	"context"
	"encoding/json"
	"errors"
	"iter"
	"strings"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/stacklok/mecatl/adapters/jsonlstore"
	driverv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/driver/v1"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

// newWiredEventLog returns a grpcdriver EventLog client over a bufconn server
// wrapping the given backend port.EventLog.
func newWiredEventLog(t *testing.T, backend port.EventLog) *EventLog {
	t.Helper()
	conn := dialBufconn(t, func(gs *grpc.Server) {
		driverv1.RegisterEventLogServiceServer(gs, NewEventLogServer(backend))
	})
	return NewEventLog(conn)
}

// faultBackend is a port.EventLog whose Read yields `good` events and then an
// error item (zero Event, faultErr) — the mid-stream fault the port contract
// promises Read surfaces as "(session.Event{}, err) then STOP". Append always
// succeeds. It exercises the SERVER wrapper's backend-Read-error branch
// (eventLogStatus) and, through it, the CLIENT's stream-error branch.
type faultBackend struct {
	good     []session.Event
	faultErr error
}

func (faultBackend) Append(context.Context, session.SessionID, session.Event) error { return nil }

func (b faultBackend) Read(context.Context, session.SessionID) iter.Seq2[session.Event, error] {
	return func(yield func(session.Event, error) bool) {
		for _, ev := range b.good {
			if !yield(ev, nil) {
				return
			}
		}
		// The fault: a zero event plus the error. A correct consumer stops here.
		yield(session.Event{}, b.faultErr)
	}
}

// TestEventLogReadMidStreamFaultStopsAfterError is the headline streaming-risk
// gate (cloud-native 3c MUST-ADD): the port contract says Read yields
// (session.Event{}, err) on a fault and then RETURNS — no further events. The
// server-streaming RPC must honour that end-to-end. A fault-injecting backend
// yields N good events then an error; the client's Read must yield exactly the
// N good events, then ONE error item carrying a ZERO session.Event, then
// nothing — and the error must surface as a non-OK gRPC status.
//
// MUTATION-KILL: this fails if eventlog.go's Read drops the "return after a
// yielded error" guarantee. Concretely, deleting the bare `return` after
// `yield(session.Event{}, rpcErr(...))` in the client's stream-error branch (so
// the loop keeps calling stream.Recv after surfacing the fault) makes
// `afterErr` non-zero — the test then fails on "yielded N events after the
// error item, want 0". Equally, dropping the server's `return eventLogStatus(err)`
// on the backend-Read-error branch (so it Sends nothing and ends the stream
// cleanly) makes the client see a CLEAN EOF and never surface the fault — the
// test then fails on "want a terminal error item, got a clean end".
func TestEventLogReadMidStreamFaultStopsAfterError(t *testing.T) {
	good := []session.Event{
		{Type: session.EvMessageDelta, Seq: 0, Text: "first"},
		{Type: session.EvMessageDelta, Seq: 1, Text: "second"},
	}
	client := newWiredEventLog(t, faultBackend{
		good:     good,
		faultErr: errors.New("backend exploded mid-stream"),
	})

	var (
		gotGood   []session.Event
		gotErr    error
		errEvent  session.Event
		afterErr  int
		sawTheErr bool
	)
	for ev, err := range client.Read(context.Background(), "fault-id") {
		if sawTheErr {
			// Anything after the error item is a contract violation.
			afterErr++
			continue
		}
		if err != nil {
			sawTheErr = true
			gotErr = err
			errEvent = ev
			continue
		}
		gotGood = append(gotGood, ev)
	}

	if len(gotGood) != len(good) {
		t.Fatalf("client read %d good events before the fault, want %d", len(gotGood), len(good))
	}
	for i := range good {
		if gotGood[i].Text != good[i].Text || gotGood[i].Seq != good[i].Seq {
			t.Errorf("good event[%d] = %+v, want %+v", i, gotGood[i], good[i])
		}
	}
	if !sawTheErr {
		t.Fatal("client Read ended without a terminal error item, want the backend fault surfaced")
	}
	if errEvent != (session.Event{}) {
		t.Errorf("error item carried a NON-zero event %+v, want session.Event{} (the port contract)", errEvent)
	}
	if afterErr != 0 {
		t.Errorf("client yielded %d events AFTER the error item, want 0 (Read must STOP after a fault)", afterErr)
	}
	// The fault must surface as a non-OK status (the server mapped the backend
	// error through eventLogStatus → Internal; the client wrapped it with rpcErr,
	// and status.FromError unwraps to find the embedded gRPC status).
	if st, ok := status.FromError(gotErr); !ok || st.Code() == codes.OK {
		t.Errorf("surfaced error %v did not carry a non-OK gRPC status (ok=%v, code=%v)", gotErr, ok, st.Code())
	}
}

// TestEventLogReadUnknownFormatIsInfraError pins the client's unknown-format
// branch: a raw server returning a valid envelope under a format tag this
// client does not speak is a mid-stream infra error (zero event), never a
// silently-skipped record.
func TestEventLogReadUnknownFormatIsInfraError(t *testing.T) {
	conn := dialBufconn(t, func(gs *grpc.Server) {
		driverv1.RegisterEventLogServiceServer(gs, badEnvelopeServer{
			format:  "eventlog-json/99",
			payload: []byte(`{}`),
		})
	})
	client := NewEventLog(conn)

	var gotErr error
	var yields int
	for ev, err := range client.Read(context.Background(), "any-id") {
		yields++
		if err != nil {
			gotErr = err
			if ev != (session.Event{}) {
				t.Errorf("unknown-format error item carried a non-zero event %+v, want zero", ev)
			}
			break
		}
		t.Fatalf("unknown-format stream yielded a good event %+v, want only the error item", ev)
	}
	if gotErr == nil {
		t.Fatal("Read(unknown format) yielded no error, want an infra error")
	}
	if want := "eventlog-json/99"; !strings.Contains(gotErr.Error(), want) {
		t.Errorf("unknown-format error %q does not name the offending tag %q", gotErr, want)
	}
}

// TestEventLogReadDecodeFaultIsInfraError pins the client's decode-fault
// branch: a payload under the CORRECT format tag that is not valid
// session.Event JSON surfaces as a zero-event error.
func TestEventLogReadDecodeFaultIsInfraError(t *testing.T) {
	conn := dialBufconn(t, func(gs *grpc.Server) {
		driverv1.RegisterEventLogServiceServer(gs, badEnvelopeServer{
			format:  EventLogFormat,
			payload: []byte(`{not valid json`),
		})
	})
	client := NewEventLog(conn)

	var gotErr error
	for ev, err := range client.Read(context.Background(), "any-id") {
		if err != nil {
			gotErr = err
			if ev != (session.Event{}) {
				t.Errorf("decode-fault error item carried a non-zero event %+v, want zero", ev)
			}
			break
		}
		t.Fatalf("decode-fault stream yielded a good event %+v, want only the error item", ev)
	}
	if gotErr == nil {
		t.Fatal("Read(undecodable payload) yielded no error, want an infra error")
	}
	if !strings.Contains(gotErr.Error(), "decode event") {
		t.Errorf("decode-fault error %q does not mention the decode failure", gotErr)
	}
}

// badEnvelopeServer is a raw EventLogService server (bypassing the wrapper) that
// streams ONE envelope with a caller-chosen format/payload, so the client's
// unknown-format and decode-fault branches can be hit directly on the wire.
type badEnvelopeServer struct {
	driverv1.UnimplementedEventLogServiceServer
	format  string
	payload []byte
}

func (s badEnvelopeServer) Read(_ *driverv1.ReadRequest, stream grpc.ServerStreamingServer[driverv1.ReadResponse]) error {
	return stream.Send(&driverv1.ReadResponse{
		Event: &driverv1.LoggedEvent{Format: s.format, Payload: s.payload},
	})
}

// TestEventLogAppendServerRejectsBadEnvelope pins the server wrapper's
// Append validation: a blank session_id, a missing/empty/wrong-format/
// undecodable envelope are all INVALID_ARGUMENT before the backend is touched.
func TestEventLogAppendServerRejectsBadEnvelope(t *testing.T) {
	goodPayload, err := json.Marshal(session.Event{Type: session.EvMessageDelta, Seq: 1})
	if err != nil {
		t.Fatalf("marshal event: %v", err)
	}
	cases := []struct {
		name string
		req  *driverv1.AppendRequest
	}{
		{"blank session id", &driverv1.AppendRequest{Event: &driverv1.LoggedEvent{Format: EventLogFormat, Payload: goodPayload}}},
		{"nil event", &driverv1.AppendRequest{SessionId: "s1"}},
		{"unknown format", &driverv1.AppendRequest{SessionId: "s1", Event: &driverv1.LoggedEvent{Format: "eventlog-json/99", Payload: goodPayload}}},
		{"empty payload", &driverv1.AppendRequest{SessionId: "s1", Event: &driverv1.LoggedEvent{Format: EventLogFormat}}},
		{"undecodable payload", &driverv1.AppendRequest{SessionId: "s1", Event: &driverv1.LoggedEvent{Format: EventLogFormat, Payload: []byte(`{not json`)}}},
	}
	// A raw client against the wrapper over a memstore backend.
	conn := dialBufconn(t, func(gs *grpc.Server) {
		driverv1.RegisterEventLogServiceServer(gs, NewEventLogServer(memstore.NewEventLog()))
	})
	raw := driverv1.NewEventLogServiceClient(conn)
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := raw.Append(context.Background(), c.req)
			if status.Code(err) != codes.InvalidArgument {
				t.Errorf("Append(%s) status = %v, want InvalidArgument", c.name, status.Code(err))
			}
		})
	}
}

// TestEventLogFormatMatchesJSONLStore pins SHOULD-ADD 4 (codec drift): the gRPC
// wire format tag and the jsonlstore on-disk record tag are TWO hand-maintained
// "eventlog-json/1" constants — a bump on one side silently makes a local log
// unreadable by the driver. Assert they are equal so the drift fails the build.
func TestEventLogFormatMatchesJSONLStore(t *testing.T) {
	if EventLogFormat != jsonlstore.EventLogFormat {
		t.Fatalf("grpcdriver.EventLogFormat %q != jsonlstore.EventLogFormat %q: the wire and the on-disk record must version the ONE event codec",
			EventLogFormat, jsonlstore.EventLogFormat)
	}
}

// TestEventLogCrossCodecRoundTrip pins the one-codec claim end-to-end: an event
// written through the jsonlstore on-disk path is read back through the grpcdriver
// decode path (a jsonlstore Store is the backend behind the driver wrapper), and
// vice versa an event appended via the driver lands readable in the jsonlstore
// file. Same Store instance behind the wire wrapper = the wire encode/decode and
// the file encode/decode must agree on the payload bytes.
func TestEventLogCrossCodecRoundTrip(t *testing.T) {
	ctx := context.Background()
	store, err := jsonlstore.New(t.TempDir())
	if err != nil {
		t.Fatalf("jsonlstore.New: %v", err)
	}
	client := newWiredEventLog(t, store)

	const id session.SessionID = "cross-codec"
	want := []session.Event{
		{Type: session.EvReasoningDelta, Seq: 1, Text: "via the file path"},
		{Type: session.EvApproval, Seq: 2, Approval: &session.ApprovalPayload{
			AskID: "cross-codec:0:c1:r1", Verdict: session.VerdictStringAllowAlways, Tool: "Write", Call: "c1", AllowAlways: true,
		}},
	}

	// (a) WRITE via the local file path (Store.Append), READ via the driver decode
	// path (client.Read over the wire wrapping the same Store).
	for _, ev := range want {
		if aerr := store.Append(ctx, id, ev); aerr != nil {
			t.Fatalf("local Append: %v", aerr)
		}
	}
	var viaWire []session.Event
	for ev, rerr := range client.Read(ctx, id) {
		if rerr != nil {
			t.Fatalf("driver Read of a file-written log: %v", rerr)
		}
		viaWire = append(viaWire, ev)
	}
	assertSameEvents(t, "file-write/wire-read", viaWire, want)

	// (b) WRITE via the driver path (client.Append over the wire), READ via the
	// local file path (Store.Read) — the reverse direction, under a fresh id.
	const id2 session.SessionID = "cross-codec-rev"
	for _, ev := range want {
		if aerr := client.Append(ctx, id2, ev); aerr != nil {
			t.Fatalf("driver Append: %v", aerr)
		}
	}
	var viaFile []session.Event
	for ev, rerr := range store.Read(ctx, id2) {
		if rerr != nil {
			t.Fatalf("local Read of a wire-written log: %v", rerr)
		}
		viaFile = append(viaFile, ev)
	}
	assertSameEvents(t, "wire-write/file-read", viaFile, want)
}

// assertSameEvents compares two event slices by their JSON encoding (tracks
// whatever fields session.Event carries without re-listing each).
func assertSameEvents(t *testing.T, dir string, got, want []session.Event) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s: read %d events, want %d", dir, len(got), len(want))
	}
	for i := range want {
		gj, wj := mustEventJSON(t, got[i]), mustEventJSON(t, want[i])
		if gj != wj {
			t.Errorf("%s: event[%d] differs:\n got %s\nwant %s", dir, i, gj, wj)
		}
	}
}

func mustEventJSON(t *testing.T, ev session.Event) string {
	t.Helper()
	b, err := json.Marshal(ev)
	if err != nil {
		t.Fatalf("marshal event: %v", err)
	}
	return string(b)
}
