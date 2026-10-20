package grpcdriver

import (
	"context"
	"reflect"
	"testing"

	"google.golang.org/grpc"

	driverv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/driver/v1"
	"github.com/stacklok/mecatl/engine/adapter/eventcodec"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

func TestLegacyEventDecodeAppendReadAndCursor(t *testing.T) {
	ctx := context.Background()
	raw := []byte(`{"Type":"tool.result","Seq":8,"ToolResult":{"CallID":"private-provider","Content":"old"}}`)
	conn := dialBufconn(t, func(gs *grpc.Server) {
		driverv1.RegisterEventLogServiceServer(gs, badEnvelopeServer{format: "eventlog-json/1", payload: raw})
	})
	client := NewEventLog(conn)
	var old session.Event
	for e, err := range client.Read(ctx, "s") {
		if err != nil {
			t.Fatal(err)
		}
		old = e
	}
	if old.Type != session.EvHistorical || old.ToolResult != nil || old.LegacyIdentity() == nil {
		t.Fatal("direct grpc reader reinterpreted provider ID")
	}
	backend := memstore.NewEventLog()
	server := NewEventLogServer(backend)
	// The decoding wrapper has its own ingress; it must not decode old data as
	// canonical just because this caller speaks the outer transport format.
	if _, err := server.Append(ctx, &driverv1.AppendRequest{SessionId: "s", Event: &driverv1.LoggedEvent{Format: "eventlog-json/1", Payload: raw}}); err != nil {
		t.Fatal(err)
	}
	wired := newWiredEventLog(t, backend)
	for e, err := range wired.Read(ctx, "s") {
		if err != nil {
			t.Fatal(err)
		}
		if e.ToolResult != nil || e.Historical == nil || string(e.LegacyIdentity().Payload) != string(raw) {
			t.Fatal("wrapper lost private provenance")
		}
		old = e
	}
	for rec, err := range wired.ReadAfter(ctx, "s", "", port.ReadOptions{}) {
		if err != nil {
			t.Fatal(err)
		}
		if rec.Event.ToolResult != nil || !reflect.DeepEqual(rec.Event.LegacyIdentity(), old.LegacyIdentity()) {
			t.Fatal("grpc cursor decoder differs")
		}
	}
	encoded, err := eventcodec.Marshal(old)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := eventcodec.Unmarshal(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(old.LegacyIdentity(), restored.LegacyIdentity()) {
		t.Fatal("reencode provenance lost")
	}
	if err := wired.Append(ctx, "copy", restored); err != nil {
		t.Fatal(err)
	}
	for e, err := range wired.Read(ctx, "copy") {
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(e.LegacyIdentity(), old.LegacyIdentity()) {
			t.Fatal("driver append changed source identity")
		}
	}
}
