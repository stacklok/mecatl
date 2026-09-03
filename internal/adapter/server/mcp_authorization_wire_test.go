package server

import (
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/reflect/protoreflect"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/engine/session"
)

func TestAuthorizationWireEventIsSafeCorrelationOnly(t *testing.T) {
	t.Parallel()

	got := toProto(session.Event{
		Type: session.EvAuthorizationRequired,
		Authorization: &session.AuthorizationPayload{
			AuthorizationID: "authorization-1",
			DisplayName:     "GitHub Enterprise",
			Call:            "call-1",
			ExpiresAt:       time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC),
			Status:          session.AuthorizationPending,
		},
	}).GetAuthorization()
	if got == nil {
		t.Fatal("authorization payload is nil")
	}
	if got.GetAuthorizationId() != "authorization-1" || got.GetDisplayName() != "GitHub Enterprise" || got.GetCallId() != "call-1" || got.GetStatus() != "pending" {
		t.Fatalf("authorization correlation = %+v", got)
	}
	if got.GetExpiresAt() == nil || !got.GetExpiresAt().AsTime().Equal(time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)) {
		t.Fatalf("authorization expiry = %v", got.GetExpiresAt())
	}

	assertExactProtoFields(t, got.ProtoReflect().Descriptor(), map[protoreflect.Name]protoreflect.FieldNumber{
		"authorization_id": 1,
		"call_id":          2,
		"expires_at":       3,
		"status":           4,
		"display_name":     5,
	})
	for _, forbidden := range []string{"url", "argument", "binding", "code", "verifier", "token", "credential", "secret"} {
		fields := got.ProtoReflect().Descriptor().Fields()
		for i := 0; i < fields.Len(); i++ {
			if strings.Contains(strings.ToLower(string(fields.Get(i).Name())), forbidden) {
				t.Fatalf("authorization event exposes forbidden field %q", fields.Get(i).Name())
			}
		}
	}
}

func TestAuthorizationWireControlsAreCorrelationOnly(t *testing.T) {
	t.Parallel()

	assertAuthorizationControlRequest(t, (&mecatlv1.GetMcpAuthorizationPresentationRequest{}).ProtoReflect().Descriptor())
	assertAuthorizationControlEnvelope(t, (&mecatlv1.RecheckMcpAuthorizationRequest{}).ProtoReflect().Descriptor())
	assertAuthorizationControlEnvelope(t, (&mecatlv1.CancelMcpAuthorizationRequest{}).ProtoReflect().Descriptor())
	assertExactProtoFields(t, (&mecatlv1.GetMcpAuthorizationPresentationResponse{}).ProtoReflect().Descriptor(), map[protoreflect.Name]protoreflect.FieldNumber{
		"url": 1,
	})
	assertAuthorizationEventResponse(t, (&mecatlv1.RecheckMcpAuthorizationResponse{}).ProtoReflect().Descriptor())
	assertAuthorizationEventResponse(t, (&mecatlv1.CancelMcpAuthorizationResponse{}).ProtoReflect().Descriptor())

	service := mecatlv1.File_mecatl_v1_harness_proto.Services().ByName("HarnessService")
	if service == nil {
		t.Fatal("HarnessService descriptor is nil")
	}
	assertAuthorizationMethod(t, service, "GetMcpAuthorizationPresentation", "mecatl.v1.GetMcpAuthorizationPresentationRequest", "mecatl.v1.GetMcpAuthorizationPresentationResponse", false, false)
	assertAuthorizationMethod(t, service, "RecheckMcpAuthorization", "mecatl.v1.RecheckMcpAuthorizationRequest", "mecatl.v1.RecheckMcpAuthorizationResponse", true, true)
	assertAuthorizationMethod(t, service, "CancelMcpAuthorization", "mecatl.v1.CancelMcpAuthorizationRequest", "mecatl.v1.CancelMcpAuthorizationResponse", true, true)

}

func assertAuthorizationControlRequest(t *testing.T, request protoreflect.MessageDescriptor) {
	t.Helper()
	assertExactProtoFields(t, request, map[protoreflect.Name]protoreflect.FieldNumber{
		"session_id":       1,
		"authorization_id": 2,
	})
}

func assertAuthorizationControlEnvelope(t *testing.T, request protoreflect.MessageDescriptor) {
	t.Helper()
	assertExactProtoFields(t, request, map[protoreflect.Name]protoreflect.FieldNumber{
		"session_id": 1, "authorization_id": 2, "resume_approval": 10, "cancel": 11,
	})
	for _, field := range []protoreflect.Name{"resume_approval", "cancel"} {
		if request.Fields().ByName(field).ContainingOneof() == nil {
			t.Fatalf("%s.%s is not in the control oneof", request.FullName(), field)
		}
	}
}

func assertAuthorizationEventResponse(t *testing.T, response protoreflect.MessageDescriptor) {
	t.Helper()
	assertExactProtoFields(t, response, map[protoreflect.Name]protoreflect.FieldNumber{"event": 1})
	if got := response.Fields().ByName("event").Message().FullName(); got != "mecatl.v1.Event" {
		t.Fatalf("%s.event type = %s, want mecatl.v1.Event", response.FullName(), got)
	}
}

func assertExactProtoFields(t *testing.T, message protoreflect.MessageDescriptor, want map[protoreflect.Name]protoreflect.FieldNumber) {
	t.Helper()
	fields := message.Fields()
	if fields.Len() != len(want) {
		t.Fatalf("%s has %d fields, want %d", message.FullName(), fields.Len(), len(want))
	}
	for name, number := range want {
		field := fields.ByName(name)
		if field == nil || field.Number() != number {
			t.Fatalf("%s.%s field number = %v, want %d", message.FullName(), name, field, number)
		}
	}
}

func assertAuthorizationMethod(t *testing.T, service protoreflect.ServiceDescriptor, name protoreflect.Name, input, output protoreflect.FullName, clientStreaming, serverStreaming bool) {
	t.Helper()
	method := service.Methods().ByName(name)
	if method == nil {
		t.Fatalf("%s method is missing", name)
	}
	if method.Input().FullName() != input || method.Output().FullName() != output || method.IsStreamingClient() != clientStreaming || method.IsStreamingServer() != serverStreaming {
		t.Fatalf("%s signature = %s -> %s (client_stream=%t server_stream=%t)", name, method.Input().FullName(), method.Output().FullName(), method.IsStreamingClient(), method.IsStreamingServer())
	}
}
