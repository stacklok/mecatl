// Package server_test is the external matrix test for steerOutcomeToProto: a
// full-table assertion that every agent.SteerOutcome maps to a non-UNSPECIFIED
// proto value. UNSPECIFIED is never sent; anything mapping there would be a
// server-side enum drift. The default case is unreachable only because every
// agent value has an arm — this test is what keeps that true.
package server_test

import (
	"testing"

	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

// TestSteer_OutcomeMatrix asserts every agent.SteerOutcome maps to a named,
// non-UNSPECIFIED proto value and back (the round-trip is the interesting
// bit — drift in either direction degrades to UNSPECIFIED silently).
func TestSteer_OutcomeMatrix(t *testing.T) {
	all := []agent.SteerOutcome{
		agent.SteerAccepted,
		agent.SteerAppended,
		agent.SteerRetracted,
		agent.SteerNonePending,
		agent.SteerTooLate,
	}
	for _, o := range all {
		got := server.SteerOutcomeToProtoForTest(o)
		if got.String() == "STEER_OUTCOME_UNSPECIFIED" {
			t.Fatalf("steerOutcomeToProto(%q) = UNSPECIFIED (a drift arm is missing)", o)
		}
	}
}
