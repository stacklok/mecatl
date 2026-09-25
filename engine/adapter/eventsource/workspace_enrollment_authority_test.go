package eventsource_test

import (
	"reflect"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/eventsource"
	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/session"
)

func TestWorkspaceEnrollmentAuthority_Scenario2_EventSourceRestoresProvenanceBeforeAwaiting(t *testing.T) {
	authority := session.Authority{CapabilitySet: governance.CapabilitySet{Tools: []string{"Read", "broker-old"}}, Provenance: "test"}
	call := toolCall("c1", "Shell", `{"command":"ls"}`)
	ask := session.PendingAsk{AskID: "s1:0:c1:r0", Tool: "Shell", Reason: "needs approval"}
	for _, tc := range []struct {
		name    string
		keys    []string
		present bool
	}{
		{name: "absent"},
		{name: "present empty", present: true},
		{name: "present nonempty", keys: []string{"broker-old"}, present: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, awaiting := range []bool{false, true} {
				t.Run(map[bool]string{false: "ordinary", true: "awaiting"}[awaiting], func(t *testing.T) {
					m := meta()
					m.Authority = &authority
					m.WorkspaceEnrollmentBrokerKeys = tc.keys
					m.WorkspaceEnrollmentBrokerKeysPresent = tc.present
					events := []session.Event(nil)
					if awaiting {
						events = []session.Event{{Type: session.EvTurnStart, Turn: 0}, {Type: session.EvToolCall, Turn: 0, ToolCall: &call}, {Type: session.EvPermissionAsk, Turn: 0, Ask: &ask}}
					}
					s, err := eventsource.Fold(m, seq(events))
					if err != nil {
						t.Fatalf("Fold: %v", err)
					}
					if awaiting && s.State != session.StateAwaiting {
						t.Fatalf("state = %q, want awaiting", s.State)
					}
					keys, present := s.WorkspaceEnrollmentBrokerKeys()
					if present != tc.present || !reflect.DeepEqual(keys, tc.keys) {
						t.Fatalf("ledger = %q, %t; want %q, %t", keys, present, tc.keys, tc.present)
					}
				})
			}
		})
	}

	m := meta()
	m.Authority = &authority
	m.WorkspaceEnrollmentBrokerKeys = []string{"broker-old"}
	if _, err := eventsource.Fold(m, seq(nil)); err == nil {
		t.Fatal("Fold accepted invalid metadata")
	}
}
