package server

import (
	"testing"

	"google.golang.org/protobuf/reflect/protoreflect"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/engine/session"
)

func TestADR_0362_PlanContinuationFailureWire(t *testing.T) {
	prompt := (&mecatlv1.Prompt{}).ProtoReflect().Descriptor()
	optIn := prompt.Fields().ByName("server_owned_plan_continuation")
	if optIn == nil || optIn.Number() != 4 || optIn.Kind() != protoreflect.BoolKind {
		t.Fatalf("Prompt.server_owned_plan_continuation = %v, want bool field 4", optIn)
	}
	event := (&mecatlv1.Event{}).ProtoReflect().Descriptor()
	failureField := event.Fields().ByName("plan_continuation_failure")
	if failureField == nil || failureField.Number() != 25 || failureField.Message().FullName() != "mecatl.v1.PlanContinuationFailure" {
		t.Fatalf("Event.plan_continuation_failure = %v, want message field 25", failureField)
	}
	assertExactProtoFields(t, failureField.Message(), map[protoreflect.Name]protoreflect.FieldNumber{
		"plan_run_id": 1,
		"ask_id":      2,
	})
	got := toProto(session.Event{
		Type: session.EvPlanContinuationFailed,
		PlanContinuationFailure: &session.PlanContinuationFailurePayload{
			PlanRunID: "approved-run",
			AskID:     "approved-ask",
		},
	})
	if got.GetType() != "plan.continuation_failed" || got.GetRunId() != "" || got.GetText() != "" || got.GetResult() != nil {
		t.Fatalf("failure projection has unsafe or terminal fields: %+v", got)
	}
	payload := got.ProtoReflect().Get(failureField).Message()
	if !payload.IsValid() || payload.Get(failureField.Message().Fields().ByName("plan_run_id")).String() != "approved-run" || payload.Get(failureField.Message().Fields().ByName("ask_id")).String() != "approved-ask" {
		t.Fatalf("failure projection lost exact correlation: %+v", got)
	}
}
