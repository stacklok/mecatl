package eventsource_test

import (
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/eventsource"
	"github.com/stacklok/mecatl/engine/session"
)

func TestFoldRestoresTitleGenerationMetadata(t *testing.T) {
	m := meta()
	m.Title = "Generated title"
	m.TitleProvenance = session.TitleProvenanceGenerated
	m.TitleGeneration = session.TitleGenerationGenerated
	m.TitleSourcePrompts = []string{"first prompt", "second prompt"}
	m.TitleAttempts = []session.TitleAttempt{{ID: "attempt-1", Outcome: session.TitleAttemptSucceeded}}
	m.AuxiliaryUsage = []session.AuxiliaryUsage{{
		Operation: session.AuxiliaryOperationSessionTitle,
		Usage:     session.Usage{InputTokens: 5, OutputTokens: 3},
		Outcome:   session.TitleAttemptSucceeded,
	}}

	s, err := eventsource.Fold(m, seq(nil))
	if err != nil {
		t.Fatalf("Fold: %v", err)
	}
	if got, want := s.TitleGeneration, session.TitleGenerationGenerated; got != want {
		t.Errorf("TitleGeneration = %q, want %q", got, want)
	}
	if got := s.TitleSourcePrompts(); len(got) != 2 || got[1] != "second prompt" {
		t.Errorf("TitleSourcePrompts = %#v, want restored metadata", got)
	}
	if got := s.TitleAttempts(); len(got) != 1 || got[0].ID != "attempt-1" {
		t.Errorf("TitleAttempts = %#v, want restored attempt", got)
	}
	if got := s.AuxiliaryUsage(); len(got) != 1 || got[0].Usage.OutputTokens != 3 {
		t.Errorf("AuxiliaryUsage = %#v, want restored ledger", got)
	}
	if got := s.Usage; got != (session.Usage{}) {
		t.Errorf("main Usage = %#v, want unchanged", got)
	}
}
