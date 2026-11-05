package learning_test

import (
	"testing"

	"github.com/stacklok/mecatl/engine/learning"
	"github.com/stacklok/mecatl/engine/session"
)

func TestScopedSignalsPreserveEvidenceProtocolAndCoordinates(t *testing.T) {
	messages := []session.Message{session.NewUserMessage("old context"), session.NewUserMessage("remember that focused tests come first")}
	for _, protocol := range []learning.EvidenceProtocol{learning.ReflectionEvidenceV2} {
		t.Run(string(protocol), func(t *testing.T) {
			input := reflectionInput(messages...)
			input.Protocol = protocol
			signals := learning.DetectSignalsScoped(input, learning.DetectionScope{Current: learning.MessageSpan{Start: 1, End: 2}})
			if len(signals) != 1 || len(signals[0].Evidence) != 1 {
				t.Fatalf("scoped signals = %#v", signals)
			}
			ref := signals[0].Evidence[0]
			if ref.Protocol != protocol || ref.Ordinal != 1 {
				t.Fatalf("protocol or source coordinate changed: %#v", ref)
			}
			if err := learning.ResolveEvidence(input, ref); err != nil {
				t.Fatalf("scoped evidence cannot resolve against its original input: %v", err)
			}
		})
	}

	selected := materialize(t, learning.MaterializationRequest{Trajectory: sourceTrajectory(messages...), Explicit: true})
	signals := learning.DetectSignalsScoped(selected.Input, learning.DetectionScope{Current: learning.MessageSpan{Start: 1, End: 2}})
	if len(signals) != 1 || len(signals[0].Evidence) != 1 {
		t.Fatalf("materialized scoped signals = %#v", signals)
	}
	ref := signals[0].Evidence[0]
	if ref.Protocol != learning.ReflectionEvidenceV2 || ref.Ordinal != 1 || ref.OriginalMessage == nil || *ref.OriginalMessage != 1 || ref.AggregateDigest != selected.Manifest.Digest {
		t.Fatalf("manifest remapping changed protocol or coordinates: %#v", ref)
	}
	if err := learning.ResolveEvidence(selected.Input, ref); err != nil {
		t.Fatalf("remapped scoped evidence cannot resolve: %v", err)
	}
}
