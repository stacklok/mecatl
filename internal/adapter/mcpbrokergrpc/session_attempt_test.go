package mcpbrokergrpc

import (
	"testing"

	p "github.com/stacklok/mecatl/contracts/gen/go/mecatl/broker/v1"
	"github.com/stacklok/mecatl/engine/session"
)

func TestSessionOccurrenceValidation(t *testing.T) {
	attempt := session.NewBrokerAttempt()
	wire := attemptToWire(attempt)
	if !wireAttemptValid(wire) || attemptFromWire(wire) != attempt {
		t.Fatal("opaque occurrence did not round trip")
	}
	// Reserved former slot fields cannot be interpreted as an occurrence.
	wire.ProtoReflect().SetUnknown([]byte{0x08, 0x01})
	if wireAttemptValid(wire) {
		t.Fatal("legacy slot framing accepted")
	}
	for _, a := range []*p.Attempt{nil, {}, {Id: "provider-call-id"}} {
		if wireAttemptValid(a) {
			t.Fatalf("invalid framing accepted: %v", a)
		}
	}
}
