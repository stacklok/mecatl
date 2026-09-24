package mcpbrokergrpc

import (
	"strings"
	"testing"

	brokerv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/broker/v1"
	"github.com/stacklok/mecatl/engine/session"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/reflect/protoreflect"
)

func TestStageCredentialCustodyRejectsHostProfileGuard(t *testing.T) {
	guard := &brokerv1.ContinuityGuard{
		SessionId:          "session",
		SessionIncarnation: string(session.NewIncarnationID()),
		OwnerPartition:     append([]byte{1}, make([]byte, 31)...),
		WorkloadPartition:  append([]byte{2}, make([]byte, 31)...),
		ProfileDigest:      append([]byte{3}, make([]byte, 31)...),
		Providers:          []string{"provider"},
	}
	if _, err := stageContinuityGuardFromWire(guard); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("stage guard with host profile fields = %v, want invalid argument", err)
	}
	guard.ProfileDigest = nil
	guard.Providers = nil
	if _, err := stageContinuityGuardFromWire(guard); err != nil {
		t.Fatalf("stage guard without host profile fields = %v", err)
	}
}

func TestCredentialContinuityWireFieldNumbers(t *testing.T) {
	file := brokerv1.File_mecatl_broker_v1_broker_proto
	attach := file.Messages().ByName("AttachRequest")
	if got := attach.Fields().ByName("expected_binding").Number(); got != 3 {
		t.Fatalf("AttachRequest.expected_binding = %d, want 3", got)
	}
	for _, name := range []protoreflect.Name{"ContinuityGuard", "CustodyAssertion", "StageCredentialCustodyRequest", "StageCredentialCustodyResponse", "CommitCredentialCustodyRequest", "RecoverCredentialAttachmentRequest", "RecoverCredentialAttachmentResponse", "TombstoneCredentialCustodyRequest"} {
		if file.Messages().ByName(name) == nil {
			t.Fatalf("missing continuity message %s", name)
		}
	}
	if got := file.Enums().ByName("BrokerErrorReason").Values().ByName("BROKER_ERROR_REASON_CONTINUITY_UNAVAILABLE").Number(); got != 7 {
		t.Fatalf("continuity reason = %d, want 7", got)
	}
	stage := file.Messages().ByName("StageCredentialCustodyResponse")
	if field := stage.Fields().ByName("profile_digest"); field == nil || field.Number() != 3 {
		t.Fatalf("StageCredentialCustodyResponse.profile_digest = %v, want field 3", field)
	}
	if field := stage.Fields().ByName("providers"); field == nil || field.Number() != 4 {
		t.Fatalf("StageCredentialCustodyResponse.providers = %v, want field 4", field)
	}
}

func TestCredentialContinuityAdditiveWireCompatibility(t *testing.T) {
	file := brokerv1.File_mecatl_broker_v1_broker_proto
	attach := file.Messages().ByName("AttachRequest")
	if attach.Fields().ByNumber(1).Name() != "session_id" || attach.Fields().ByNumber(2).Name() != "broker_incarnation" || attach.Fields().ByNumber(3).Name() != "expected_binding" {
		t.Fatalf("AttachRequest fields are not additive: %v", attach.Fields())
	}
}

func TestCredentialContinuityHasNoReverseAuthorityRPC(t *testing.T) {
	service := brokerv1.File_mecatl_broker_v1_broker_proto.Services().ByName("BrokerService")
	for i := 0; i < service.Methods().Len(); i++ {
		name := strings.ToLower(string(service.Methods().Get(i).Name()))
		if strings.Contains(name, "reverse") || strings.Contains(name, "donor") || strings.Contains(name, "export") {
			t.Fatalf("reverse-authority RPC %q", name)
		}
	}
}
func TestCredentialContinuityProtocolContainsNoForbiddenFields(t *testing.T) {
	for _, name := range []protoreflect.Name{"ContinuityGuard", "CustodyAssertion"} {
		fields := brokerv1.File_mecatl_broker_v1_broker_proto.Messages().ByName(name).Fields()
		for i := 0; i < fields.Len(); i++ {
			field := strings.ToLower(string(fields.Get(i).Name()))
			for _, forbidden := range []string{"token", "tsid", "handle", "binding"} {
				if strings.Contains(field, forbidden) {
					t.Fatalf("continuity authority surface contains %q", forbidden)
				}
			}
		}
	}
}
