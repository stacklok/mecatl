package memstore_test

import (
	"context"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/eventcodec"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/port"
)

func TestEventLogOwnsHistoricalCarrier(t *testing.T) {
	ctx := context.Background()
	log := memstore.NewEventLog()
	e, err := eventcodec.Unmarshal([]byte(`{"Type":"tool.result","ToolResult":{"CallID":"private","Content":"visible","Parts":[{"BlockKind":"image","Kind":"image","MIMEType":"image/png","Data":"AQI="}]}}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := log.Append(ctx, "s", e); err != nil {
		t.Fatal(err)
	}
	e.Historical.Parts[0].Data[0] = 9
	for stored, err := range log.Read(ctx, "s") {
		if err != nil {
			t.Fatal(err)
		}
		if stored.Historical.Parts[0].Data[0] != 1 || stored.LegacyIdentity().Coordinate == "" {
			t.Fatal("append alias or missing durable coordinate")
		}
		stored.Historical.Parts[0].Data[0] = 8
		private := stored.LegacyIdentity()
		private.Payload[0] = 'x'
	}
	for rec, err := range log.ReadAfter(ctx, "s", "", port.ReadOptions{}) {
		if err != nil {
			t.Fatal(err)
		}
		if rec.Event.Historical.Parts[0].Data[0] != 1 || rec.Event.LegacyIdentity().Payload[0] != '{' {
			t.Fatal("read alias")
		}
		rec.Event.Historical.Parts[0].Data[0] = 7
	}
	for stored, err := range log.Read(ctx, "s") {
		if err != nil {
			t.Fatal(err)
		}
		if stored.Historical.Parts[0].Data[0] != 1 {
			t.Fatal("cursor read alias")
		}
	}
}
