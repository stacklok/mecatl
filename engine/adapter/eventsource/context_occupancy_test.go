package eventsource_test

import (
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/eventsource"
	"github.com/stacklok/mecatl/engine/session"
)

func TestResumableSessionStatusMetrics_Scenario1_EventSourceMetadataRoundTrip(t *testing.T) {
	occupancy := session.ContextOccupancy{InputTokens: 512, Estimated: true}
	m := meta()
	m.LatestContextOccupancy = &occupancy

	folded, err := eventsource.Fold(m, seq([]session.Event{{
		Type:    session.EvTurnEnd,
		TurnEnd: &session.TurnEndPayload{Usage: session.Usage{InputTokens: 999}, Estimated: false},
	}}))
	if err != nil {
		t.Fatalf("Fold: %v", err)
	}
	got, ok := folded.LatestContextOccupancy()
	if !ok || got != occupancy {
		t.Fatalf("LatestContextOccupancy = (%+v, %v), want (%+v, true)", got, ok, occupancy)
	}

	legacy, err := eventsource.Fold(meta(), seq(nil))
	if err != nil {
		t.Fatalf("Fold legacy metadata: %v", err)
	}
	if got, ok := legacy.LatestContextOccupancy(); ok {
		t.Fatalf("legacy LatestContextOccupancy = (%+v, true), want absent", got)
	}
}
