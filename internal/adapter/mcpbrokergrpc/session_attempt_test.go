package mcpbrokergrpc

import (
	p "github.com/stacklok/mecatl/contracts/gen/go/mecatl/broker/v1"
	"github.com/stacklok/mecatl/engine/session"
	c "github.com/stacklok/mecatl/internal/mcpbroker"
	"testing"
)

func TestSessionAttemptStatusValidation(t *testing.T) {
	attempt := c.BrokerAttempt{Slot: 63, Sequence: 1}
	result := session.NewToolResult("id", "ok")
	cases := []struct {
		phase       string
		disposition session.BrokerAttemptDisposition
		outcome     *c.InvocationOutcome
		valid       bool
	}{
		{"not_admitted", "", nil, true}, {"reserved", "", nil, true}, {"dispatched", "", nil, true},
		{"terminal", session.BrokerAttemptUnknown, nil, true},
		{"terminal", session.BrokerAttemptNotDispatched, &c.InvocationOutcome{Kind: c.InvocationNotDispatched, Reason: c.FailureInterrupted}, true},
		{"terminal", session.BrokerAttemptCompleted, &c.InvocationOutcome{Kind: c.InvocationCompleted, Result: &result}, true},
		{"terminal", session.BrokerAttemptCompleted, nil, false},
		{"not_admitted", session.BrokerAttemptNotDispatched, nil, false},
		{"reserved", session.BrokerAttemptNotDispatched, nil, false},
		{"dispatched", "", &c.InvocationOutcome{Kind: c.InvocationOutcomeUnknown}, false},
		{"terminal", session.BrokerAttemptUnknown, &c.InvocationOutcome{Kind: c.InvocationCompleted, Result: &result}, false},
		{"invented", "", nil, false},
	}
	for _, tc := range cases {
		original := c.AttemptStatus{Attempt: attempt, Phase: tc.phase, Disposition: tc.disposition, Outcome: tc.outcome}
		wire, err := encodeAttemptStatus(original, attempt, nil)
		if !tc.valid {
			if err == nil {
				t.Fatalf("encoded invalid: %#v", original)
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		decoded, err := decodeAttemptStatus(wire, attempt)
		if err != nil || !decoded.Valid() {
			t.Fatalf("round trip: %#v %v", decoded, err)
		}
		if _, err := decodeAttemptStatus(wire, c.BrokerAttempt{Slot: 62, Sequence: 1}); err == nil {
			t.Fatal("substituted slot accepted")
		}
		wire.ProtoReflect().SetUnknown([]byte{0x98, 0x06, 0x01})
		if _, err := decodeAttemptStatus(wire, attempt); err == nil {
			t.Fatal("unknown field accepted")
		}
	}
	for _, a := range []*p.Attempt{nil, {}, {Slot: 64, Sequence: 1}, {Slot: 0, Sequence: 0}} {
		if wireAttemptValid(a) {
			t.Fatalf("invalid framing accepted: %v", a)
		}
	}
}
