package learning

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stacklok/mecatl/engine/session"
)

func TestLegacyRedactionSinglePassBoundedAndIdempotent(t *testing.T) {
	for _, tc := range []struct {
		name, text string
		ids        []string
	}{
		{"repeated-short-id", "visible i", []string{"i", "i", "i", "i", "i", "i", "i", "i"}},
		{"overlapping-ids", "visible abc ab a", []string{"a", "ab", "abc", "i", "historical", "ident", "["}},
		{"marker-prefixed-id", historicalIdentifierMarker + "-private-item", []string{historicalIdentifierMarker + "-private-item"}},
		{"marker-contained-in-id", "prefix-" + historicalIdentifierMarker + "-private-item", []string{"prefix-" + historicalIdentifierMarker + "-private-item"}},
		{"marker-tail-overlap", historicalIdentifierMarker + "-private-item", []string{"identifier]-private-item"}},
		{"marker-head-overlap", "private-" + historicalIdentifierMarker, []string{"private-[historical"}},
		{"marker-exact-id", historicalIdentifierMarker, []string{historicalIdentifierMarker}},
		{"marker-substring-id", historicalIdentifierMarker + " historical", []string{"historical", "i"}},
		{"canonical-id", "private pr\u200bivate", []string{"pr\u200bivate", "private"}},
		{"output-ceiling", strings.Repeat("i", 2<<20), []string{"i", "i", "i"}},
		{"utf8-ceiling", strings.Repeat("Ω", maxProjectionTextBytes/2-1) + "i", []string{"i"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := redactLegacyText(tc.text, tc.ids)
			if strings.HasPrefix(tc.name, "marker-") && tc.name != "marker-substring-id" && got != "" {
				t.Fatalf("marker shielding exposed private text: %q", got)
			}
			if len(got) > maxProjectionTextBytes || !utf8.ValidString(got) {
				t.Fatalf("unbounded or invalid redaction: bytes=%d", len(got))
			}
			original := got
			for range 8 {
				got = redactLegacyText(got, tc.ids)
				if got != original {
					t.Fatalf("redaction rescanned inserted marker: bytes=%d original=%d", len(got), len(original))
				}
			}
			withoutMarkers := strings.ReplaceAll(got, historicalIdentifierMarker, "")
			for _, id := range tc.ids {
				if strings.Contains(withoutMarkers, id) || strings.Contains(withoutMarkers, safeProjectionText("", id)) {
					t.Fatalf("identifier survived redaction: %q", id)
				}
			}
		})
	}
}

func TestVerifiedProjectionRedactsOnceIncludingPrivateReasoningID(t *testing.T) {
	legacy := &session.LegacyMessageIdentity{ReasoningItemID: "reasoning-private"}
	for range 8 {
		legacy.Calls = append(legacy.Calls, session.LegacyCallIdentity{ProviderID: "i", ItemID: "overlap"})
	}
	legacy.Calls = append(legacy.Calls, session.LegacyCallIdentity{ProviderID: "overlapping"})
	message := session.NewAssistantMessage("visible i overlapping overlap reasoning-private", nil)
	message.Legacy = legacy
	input := NewInput(Trajectory{SessionID: "s", Messages: []session.Message{message}}, nil, nil, nil)
	projected, err := ProjectInput(input)
	if err != nil {
		t.Fatal(err)
	}
	want := projected.Messages[0].Text
	for range 8 {
		verified, err := ProjectVerifiedInput(input)
		if err != nil {
			t.Fatal(err)
		}
		if verified.Messages[0].Text != want || len(want) > maxProjectionTextBytes {
			t.Fatal("verified boundary redacted already redacted text")
		}
		if _, err := verified.ReflectionInput(); err != nil {
			t.Fatalf("lost private binding: %v", err)
		}
		raw, err := json.Marshal(verified)
		if err != nil {
			t.Fatal(err)
		}
		for _, private := range []string{"reasoning-private", "overlapping", "ReasoningItemID", "legacyIdentifiers"} {
			if strings.Contains(string(raw), private) {
				t.Fatalf("private provenance exposed: %q", private)
			}
		}
	}
	ref, err := MessageEvidenceRef(input, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	preview, err := EvidencePreview(input, ref)
	if err != nil || len(preview) > maxEvidencePreviewBytes || strings.Contains(preview, "reasoning-private") {
		t.Fatalf("unsafe preview: %q %v", preview, err)
	}
}
