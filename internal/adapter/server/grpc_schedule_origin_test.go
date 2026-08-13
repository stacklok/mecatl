package server

import (
	"testing"

	"google.golang.org/protobuf/reflect/protoreflect"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
)

func TestProtoToScheduleSpecLeavesOriginSessionIDEmpty(t *testing.T) {
	got, err := protoToScheduleSpec(&mecatlv1.ScheduleSpec{
		Name:    "nightly",
		Prompt:  "run checks",
		Trigger: &mecatlv1.TriggerSpec{Cron: "0 1 * * *"},
	})
	if err != nil {
		t.Fatalf("protoToScheduleSpec: %v", err)
	}
	if got.OriginSessionID != "" {
		t.Fatalf("OriginSessionID = %q, want empty for a wire-created schedule", got.OriginSessionID)
	}
}

// This descriptor assertion makes adding origin_session_id to the wire an
// explicit review event: the routing key must remain in-process-only.
func TestScheduleSpecDescriptorHasNoOriginSessionID(t *testing.T) {
	desc := (&mecatlv1.ScheduleSpec{}).ProtoReflect().Descriptor()
	if field := desc.Fields().ByName(protoreflect.Name("origin_session_id")); field != nil {
		t.Fatalf("ScheduleSpec unexpectedly exposes origin_session_id as field %d", field.Number())
	}
}
