package grpcdriver

import (
	"fmt"
	"reflect"
	"strconv"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	driverv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/driver/v1"
	"github.com/stacklok/mecatl/engine/adapter/eventcodec"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

type legacyEventFixture struct {
	driverv1.UnimplementedEventLogServiceServer
	events []*driverv1.LoggedEvent
}

func (s legacyEventFixture) Read(_ *driverv1.ReadRequest, stream grpc.ServerStreamingServer[driverv1.ReadResponse]) error {
	for _, ev := range s.events {
		if err := stream.Send(&driverv1.ReadResponse{Event: ev}); err != nil {
			return err
		}
	}
	return nil
}

func (s legacyEventFixture) ReadAfter(req *driverv1.ReadAfterRequest, stream grpc.ServerStreamingServer[driverv1.ReadAfterResponse]) error {
	start := 0
	if req.Cursor != "" {
		var err error
		start, err = strconv.Atoi(req.Cursor)
		if err != nil {
			return err
		}
	}
	for i := start; i < len(s.events); i++ {
		if req.Limit > 0 && i-start >= int(req.Limit) {
			break
		}
		if err := stream.Send(&driverv1.ReadAfterResponse{Kind: string(port.LogRecordEvent), Event: s.events[i], Cursor: strconv.Itoa(i + 1), Live: req.Follow}); err != nil {
			return err
		}
	}
	return nil
}

func TestLegacyEventDecodeAppendReadAndCursor(t *testing.T) {
	ctx := t.Context()
	old := &driverv1.LoggedEvent{Format: "eventlog-json/1", Payload: []byte(`{"Type":"tool.result","Seq":8,"ToolResult":{"CallID":"private-provider","Content":"old"}}`)}
	call := session.NewToolCall(session.NewToolCallID(), "Read", []byte(`{}`))
	current := session.Event{Type: session.EvToolCall, Seq: 91, ToolCall: &call}
	payload, err := eventcodec.Marshal(current)
	if err != nil {
		t.Fatal(err)
	}
	fixture := legacyEventFixture{events: []*driverv1.LoggedEvent{old, old, {Format: "eventlog-json/1", Payload: []byte(`{"Type":"message.delta","Seq":37,"Text":"old text"}`)}, {Format: EventLogFormat, Payload: payload}}}
	conn := dialBufconn(t, func(gs *grpc.Server) { driverv1.RegisterEventLogServiceServer(gs, fixture) })
	client := NewEventLog(conn)
	var events []session.Event
	for ev, err := range client.Read(ctx, "s") {
		if err != nil {
			t.Fatal(err)
		}
		events = append(events, ev)
	}
	want := []session.Event{{Type: session.EvMessageDelta, Seq: 37, Text: "old text"}, current}
	if !reflect.DeepEqual(events, want) {
		t.Fatalf("mixed events: %+v", events)
	}
	var after port.Cursor
	for i := 1; i <= 2; i++ {
		count := 0
		for rec, err := range client.ReadAfter(ctx, "s", after, port.ReadOptions{Limit: 1}) {
			if err != nil {
				t.Fatal(err)
			}
			if rec.Cursor != port.Cursor(strconv.Itoa(i)) || rec.Kind != port.LogRecordGap || rec.GapReason != eventcodec.ErrSkipLegacyRecord.Error() || !reflect.DeepEqual(rec.Event, session.Event{}) {
				t.Fatalf("skip gap: %+v", rec)
			}
			after = rec.Cursor
			count++
		}
		if count != 1 {
			t.Fatalf("omitted-only page count %d", count)
		}
	}
	count := 0
	for rec, err := range client.ReadAfter(ctx, "s", "", port.ReadOptions{Follow: true}) {
		if err != nil {
			t.Fatal(err)
		}
		if !rec.Live || rec.Cursor != port.Cursor(strconv.Itoa(count+1)) {
			t.Fatalf("follow metadata: %+v", rec)
		}
		if count >= 2 && !reflect.DeepEqual(rec.Event, want[count-2]) {
			t.Fatalf("follow event: %+v", rec)
		}
		count++
	}
	if count != 4 {
		t.Fatalf("follow count %d", count)
	}
	backend := &countedEventLog{EventLog: memstore.NewEventLog()}
	server := NewEventLogServer(backend)
	if _, err := server.Append(ctx, &driverv1.AppendRequest{SessionId: "s", Event: old}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("unsupported append: %v", err)
	}
	if backend.writes != 0 {
		t.Fatal("unsupported append reached backend")
	}
}

func TestLegacyEventMalformedNotSkipped(t *testing.T) {
	for i, env := range []*driverv1.LoggedEvent{
		{Format: "eventlog-json/1", Payload: []byte(`{"Type":"tool.result","ToolResult":false}`)},
		{Format: EventLogFormat, Payload: []byte(`{"identity_schema":2,"Type":"tool.result","ToolResult":false}`)},
		{Format: "eventlog-json/1", Payload: []byte(`{"Type":"unknown"}`)},
		{Format: "eventlog-json/1", Payload: []byte(`{"identity_schema":2,"Type":"tool.result"}`)},
		{Format: "unknown/99", Payload: []byte(`{"Type":"tool.result"}`)},
		{Format: "eventlog-json/1", Payload: []byte(`{`)},
	} {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			conn := dialBufconn(t, func(gs *grpc.Server) {
				driverv1.RegisterEventLogServiceServer(gs, legacyEventFixture{events: []*driverv1.LoggedEvent{env}})
			})
			client := NewEventLog(conn)
			count := 0
			for _, err := range client.Read(t.Context(), "s") {
				if err == nil {
					t.Fatal("malformed event accepted")
				}
				count++
			}
			if count != 1 {
				t.Fatal("missing read error")
			}
			count = 0
			for rec, err := range client.ReadAfter(t.Context(), "s", "", port.ReadOptions{}) {
				if err == nil || rec.Cursor != "" {
					t.Fatalf("malformed event became gap: %+v %v", rec, err)
				}
				count++
			}
			if count != 1 {
				t.Fatal("missing cursor error")
			}
		})
	}
}
