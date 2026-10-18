package learning

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/session"
)

func TestMarkerOverlappingReasoningIdentifierIsNeverProjected(t *testing.T) {
	for _, id := range []string{
		historicalIdentifierMarker + "-private-item",
		"prefix-" + historicalIdentifierMarker + "-private-item",
		"identifier]-private-item",
		"private-[historical",
		historicalIdentifierMarker,
	} {
		t.Run(id, func(t *testing.T) {
			text := "private-" + historicalIdentifierMarker + "-private-item " + id
			message := session.NewAssistantMessage(text, nil)
			message.Legacy = &session.LegacyMessageIdentity{ReasoningItemID: id, Calls: []session.LegacyCallIdentity{{ProviderID: "i"}, {ProviderID: "i"}}}
			input := NewInput(Trajectory{SessionID: "s", Messages: []session.Message{message}}, nil, nil, nil)
			ref, err := MessageEvidenceRef(input, 0, "")
			if err != nil {
				t.Fatal(err)
			}
			for range 8 {
				for _, project := range []func(Input) (Projection, error){ProjectInput, ProjectVerifiedInput} {
					display, err := project(input)
					if err != nil {
						t.Fatal(err)
					}
					if display.Messages[0].Text != "" {
						t.Fatalf("affected field was not withheld: %q", display.Messages[0].Text)
					}
					raw, err := json.Marshal(display)
					if err != nil {
						t.Fatal(err)
					}
					if strings.Contains(string(raw), id) || len(raw) > maxProjectionTextBytes {
						t.Fatalf("private identifier exposed or output unbounded: %d bytes", len(raw))
					}
				}
				preview, err := EvidencePreview(input, ref)
				if err != nil || strings.Contains(preview, id) || len(preview) > maxEvidencePreviewBytes {
					t.Fatalf("unsafe preview: %q %v", preview, err)
				}
			}
		})
	}
}
