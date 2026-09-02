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
	s.RecordTokenUsage(session.UsageKindSessionTitle, "provider", "model", session.Usage{InputTokens: 3, OutputTokens: 5})

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
	usage := restored.TokenUsage[session.UsageKindSessionTitle]
	if usage.Total != (session.Usage{InputTokens: 3, OutputTokens: 5}) || usage.Models["provider/model"] != usage.Total {
		t.Errorf("TokenUsage = %#v, want title usage", usage)
	}
}

func TestSessionTitleGeneration_Scenario5_TokenUsageRoundTripAndProjection(t *testing.T) {
	s := session.New("title-usage", session.ModeDefault, "/workspace", session.Limits{}, time.Time{})
	s.RecordTokenUsage(session.UsageKindSessionTitle, "provider", "model", session.Usage{InputTokens: 13, OutputTokens: 8})

	snap, err := Of(s)
	if err != nil {
		t.Fatalf("Of: %v", err)
	}
	if usage := snap.TokenUsage[session.UsageKindSessionTitle]; usage.Total.OutputTokens != 8 || usage.Models["provider/model"].InputTokens != 13 {
		t.Fatalf("snapshot token usage = %#v, want projected title usage", usage)
	}
	restored, err := snap.Restore()
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if usage := restored.TokenUsage[session.UsageKindSessionTitle]; usage.Total.InputTokens != 13 || usage.Models["provider/model"].OutputTokens != 8 {
		t.Errorf("round-trip token usage = %#v, want title usage", usage)
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
