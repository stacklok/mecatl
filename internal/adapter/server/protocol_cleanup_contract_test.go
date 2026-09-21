package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"google.golang.org/protobuf/reflect/protoreflect"
)

func TestCanonicalHarnessProtocolReservations(t *testing.T) {
	messages := mecatlv1.File_mecatl_v1_harness_proto.Messages()
	assertReservedFields(t, messages.ByName("SessionSummary"), map[protoreflect.FieldNumber]protoreflect.Name{7: "title", 14: "title_provenance"})
	assertReservedFields(t, messages.ByName("Session"), map[protoreflect.FieldNumber]protoreflect.Name{10: "title", 11: "capabilities", 12: "title_provenance"})
	assertReservedFields(t, messages.ByName("CreateSessionResponse"), map[protoreflect.FieldNumber]protoreflect.Name{2: "capabilities"})
	assertReservedFields(t, messages.ByName("ResumeApproval"), map[protoreflect.FieldNumber]protoreflect.Name{2: "allow"})
	assertReservedFields(t, messages.ByName("Approval"), map[protoreflect.FieldNumber]protoreflect.Name{5: "allow_always"})
	assertReservedFields(t, messages.ByName("Result"), map[protoreflect.FieldNumber]protoreflect.Name{5: "permanent"})
	assertReservedFields(t, messages.ByName("Event"), map[protoreflect.FieldNumber]protoreflect.Name{9: "usage"})

	approval := messages.ByName("Approval")
	if field := approval.Fields().ByName("verdict"); field == nil || field.Number() != 6 || field.Kind() != protoreflect.EnumKind || field.Enum().FullName() != "mecatl.v1.ApprovalVerdict" {
		t.Fatalf("Approval.verdict = %v, want ApprovalVerdict field 6", field)
	}
	caps := messages.ByName("ServerCapabilities")
	if caps.Fields().ByName("bash") != nil || caps.Fields().ByName("shell").Number() != 6 {
		t.Fatalf("ServerCapabilities must expose shell=6 only")
	}

	service := mecatlv1.File_mecatl_v1_harness_proto.Services().ByName("HarnessService")
	for _, method := range []protoreflect.Name{"PlanSessionMigration", "ApplySessionMigration", "ResumeSessionMigration", "CancelSessionMigration", "GetSessionMigrationJob"} {
		if service.Methods().ByName(method) != nil {
			t.Fatalf("removed migration method %s remains in descriptor", method)
		}
	}
}

func TestLegacySessionControlRoutesAreAbsent(t *testing.T) {
	handler := NewHTTPHandler(&Service{})
	for _, path := range []string{"approve", "cancel", "steer", "cancel-steer"} {
		req := httptest.NewRequest(http.MethodPost, "/v1/sessions/session-1/"+path, strings.NewReader(`{}`))
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Errorf("POST legacy /%s status = %d, want 404", path, rec.Code)
		}
	}
}

func assertReservedFields(t *testing.T, message protoreflect.MessageDescriptor, removed map[protoreflect.FieldNumber]protoreflect.Name) {
	t.Helper()
	for number, name := range removed {
		if message.Fields().ByNumber(number) != nil || message.Fields().ByName(name) != nil {
			t.Errorf("%s still exposes removed %s=%d", message.FullName(), name, number)
		}
		reservedNumber := false
		for i := 0; i < message.ReservedRanges().Len(); i++ {
			r := message.ReservedRanges().Get(i)
			if number >= r[0] && number < r[1] {
				reservedNumber = true
				break
			}
		}
		if !reservedNumber {
			t.Errorf("%s does not reserve field number %d", message.FullName(), number)
		}
		reservedName := false
		for i := 0; i < message.ReservedNames().Len(); i++ {
			if message.ReservedNames().Get(i) == name {
				reservedName = true
				break
			}
		}
		if !reservedName {
			t.Errorf("%s does not reserve field name %s", message.FullName(), name)
		}
	}
}
