package sessnap

import (
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/session"
)

func TestSessionTitleGeneration_Scenario2_TitleMetadataRoundTrip(t *testing.T) {
	s := session.New("title-metadata", session.ModeDefault, "/workspace", session.Limits{}, time.Time{})
	s.SetTitleGeneration(session.TitleGenerationPending)
	s.RecordTitleSourcePrompt("first principal prompt")
	s.RecordTitleAttempt(session.TitleAttempt{ID: "attempt-1", Outcome: session.TitleAttemptDeferred})
	s.RecordAuxiliaryUsage(session.AuxiliaryUsage{
		Operation:  session.AuxiliaryOperationSessionTitle,
		ProviderID: "provider",
		ModelID:    "model",
		Usage:      session.Usage{InputTokens: 3, OutputTokens: 5},
		Outcome:    session.TitleAttemptDeferred,
	})

	snap, err := Of(s)
	if err != nil {
		t.Fatalf("Of: %v", err)
	}
	restored, err := snap.Restore()
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if got, want := restored.TitleGeneration, session.TitleGenerationPending; got != want {
		t.Errorf("TitleGeneration = %q, want %q", got, want)
	}
	if got, want := restored.TitleSourcePrompts(), []string{"first principal prompt"}; !equalStrings(got, want) {
		t.Errorf("TitleSourcePrompts = %#v, want %#v", got, want)
	}
	attempts := restored.TitleAttempts()
	if len(attempts) != 1 || attempts[0].ID != "attempt-1" || attempts[0].Outcome != session.TitleAttemptDeferred {
		t.Errorf("TitleAttempts = %#v, want deferred attempt", attempts)
	}
	usage := restored.AuxiliaryUsage()
	if len(usage) != 1 || usage[0].Usage != (session.Usage{InputTokens: 3, OutputTokens: 5}) {
		t.Errorf("AuxiliaryUsage = %#v, want title usage", usage)
	}
}

func TestSessionTitleGeneration_Scenario5_AuxiliaryUsageRoundTripAndProjection(t *testing.T) {
	s := session.New("title-usage", session.ModeDefault, "/workspace", session.Limits{}, time.Time{})
	s.RecordAuxiliaryUsage(session.AuxiliaryUsage{
		Operation: session.AuxiliaryOperationSessionTitle,
		Usage:     session.Usage{InputTokens: 13, OutputTokens: 8},
	})

	snap, err := Of(s)
	if err != nil {
		t.Fatalf("Of: %v", err)
	}
	if len(snap.AuxiliaryUsage) != 1 || snap.AuxiliaryUsage[0].Usage.OutputTokens != 8 {
		t.Fatalf("snapshot auxiliary usage = %#v, want projected entry", snap.AuxiliaryUsage)
	}
	restored, err := snap.Restore()
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if got := restored.AuxiliaryUsage(); len(got) != 1 || got[0].Usage.InputTokens != 13 {
		t.Errorf("round-trip auxiliary usage = %#v, want input=13", got)
	}
	if restored.Usage != (session.Usage{}) {
		t.Errorf("main Usage = %#v, want zero", restored.Usage)
	}
	legacy := Snapshot{ID: "legacy", State: session.StateIdle, Mode: session.ModeDefault, Usage: &session.Usage{InputTokens: 5}}
	legacyRestored, err := legacy.Restore()
	if err != nil {
		t.Fatalf("restore legacy usage: %v", err)
	}
	if got := legacyRestored.TokenUsage[session.UsageKindMain]; got.Total.InputTokens != 5 || got.Models["unknown"].InputTokens != 5 {
		t.Fatalf("legacy token usage = %#v, want unknown attribution", got)
	}
}

func equalStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
