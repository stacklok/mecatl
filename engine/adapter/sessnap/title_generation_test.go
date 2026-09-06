package sessnap

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/session"
)

func TestSessionTitleGeneration_Scenario2_TitleMetadataRoundTrip(t *testing.T) {
	s := session.New("title-metadata", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/workspace", Revision: "in-tree-v1"}, session.Limits{}, time.Time{})
	s.SetTitleGeneration(session.TitleGenerationPending)
	s.RecordTitleSourcePrompt("first principal prompt")
	s.RecordTitleAttempt(session.TitleAttempt{ID: "attempt-1", Outcome: session.TitleAttemptDeferred})
	if err := s.RenameTitle("operator title"); err != nil {
		t.Fatalf("RenameTitle: %v", err)
	}
	if got, want := s.TitleRevision, uint64(4); got != want {
		t.Fatalf("TitleRevision before snapshot = %d, want %d", got, want)
	}
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
	if got, want := restored.Title, "operator title"; got != want {
		t.Errorf("Title = %q, want %q", got, want)
	}
	if got, want := restored.TitleProvenance, session.TitleProvenanceOperator; got != want {
		t.Errorf("TitleProvenance = %q, want %q", got, want)
	}
	if got, want := restored.TitleRevision, uint64(4); got != want {
		t.Errorf("TitleRevision = %d, want %d", got, want)
	}
	usage := restored.TokenUsage[session.UsageKindSessionTitle]
	if usage.Total != (session.Usage{InputTokens: 3, OutputTokens: 5}) || usage.Models["provider/model"] != usage.Total {
		t.Errorf("TokenUsage = %#v, want title usage", usage)
	}
}

func TestSessionTitleGeneration_Scenario5_TokenUsageRoundTripAndProjection(t *testing.T) {
	s := session.New("title-usage", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/workspace", Revision: "in-tree-v1"}, session.Limits{}, time.Time{})
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
	legacy := Snapshot{ID: "legacy", State: session.StateIdle, Mode: session.ModeDefault, EnvironmentRef: session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/workspace", Revision: "in-tree-v1"}, Usage: &session.Usage{InputTokens: 5}}
	legacyRestored, err := legacy.Restore()
	if err != nil {
		t.Fatalf("restore legacy usage: %v", err)
	}
	if got := legacyRestored.TitleRevision; got != 0 {
		t.Errorf("legacy TitleRevision = %d, want 0", got)
	}
	if got := legacyRestored.TokenUsage[session.UsageKindMain]; got.Total.InputTokens != 5 || got.Models["unknown"].InputTokens != 5 {
		t.Fatalf("legacy token usage = %#v, want unknown attribution", got)
	}
}

func TestSnapshotJSONTitleRevisionPresence(t *testing.T) {
	env := session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/workspace", Revision: "in-tree-v1"}
	withRevision := session.New("with-revision", session.ModeDefault, env, session.Limits{}, time.Time{})
	withRevision.SetTitle("title")
	snap, err := Of(withRevision)
	if err != nil {
		t.Fatalf("Of: %v", err)
	}
	body, err := json.Marshal(snap)
	if err != nil || !strings.Contains(string(body), `"title_revision":1`) {
		t.Fatalf("snapshot JSON = %s, err = %v", body, err)
	}

	withoutRevision := session.New("without-revision", session.ModeDefault, env, session.Limits{}, time.Time{})
	snap, err = Of(withoutRevision)
	if err != nil {
		t.Fatalf("Of: %v", err)
	}
	body, err = json.Marshal(snap)
	if err != nil || strings.Contains(string(body), `"title_revision"`) {
		t.Fatalf("legacy snapshot JSON = %s, err = %v", body, err)
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
