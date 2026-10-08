package server_test

import (
	"testing"

	"google.golang.org/protobuf/reflect/protoreflect"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
)

func TestCreateSessionAPIHasNoMicroVMGuestEgressPolicy(t *testing.T) {
	fields := (&mecatlv1.CreateSessionRequest{}).ProtoReflect().Descriptor().Fields()
	for _, name := range []protoreflect.Name{"guest_egress", "guest_egress_mode", "guest_allow", "microvm_guest_egress", "microvm_guest_allow"} {
		if fields.ByName(name) != nil {
			t.Errorf("public CreateSessionRequest exposes host-operator policy field %q", name)
		}
	}
}
