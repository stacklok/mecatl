package session

import (
	"strings"
	"testing"
	"time"
)

func TestSessionTitleGeneration_Scenario2_GenuinePromptCandidatesRoundTrip(t *testing.T) {
	s := newTestSession(Limits{})

	for _, prompt := range []string{
		"  First\nprincipal prompt  ",
		"\tSecond principal prompt\t",
		strings.Repeat("x", 2_100),
		"fourth prompt is ignored",
	} {
		s.RecordTitleSourcePrompt(prompt)
	}

	got := s.TitleSourcePrompts()
	want := []string{
		"  First\nprincipal prompt  ",
		"\tSecond principal prompt\t",
		strings.Repeat("x", 2_000),
	}
	if len(got) != len(want) {
		t.Fatalf("candidate count = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("candidate[%d] = %q, want %q", i, got[i], want[i])
		}
	}
	got[0] = "mutated caller copy"
	if s.TitleSourcePrompts()[0] != want[0] {
		t.Fatal("TitleSourcePrompts returned mutable aggregate storage")
	}
}

func TestSessionTitleGeneration_Scenario5_RecordsAuxiliaryUsage(t *testing.T) {
	s := newTestSession(Limits{})
	mainUsage := s.Usage
	at := time.Date(2026, time.August, 30, 12, 0, 0, 0, time.UTC)

	s.RecordAuxiliaryUsage(AuxiliaryUsage{
		Operation:  AuxiliaryOperationSessionTitle,
		ProviderID: " openrouter\n",
		ModelID:    " model \t",
		Usage:      Usage{InputTokens: 11, OutputTokens: 7},
		RecordedAt: at,
		Outcome:    TitleAttemptSucceeded,
	})

	entries := s.AuxiliaryUsage()
	if len(entries) != 1 {
		t.Fatalf("auxiliary entries = %d, want 1", len(entries))
	}
	if got, want := entries[0].Operation, AuxiliaryOperationSessionTitle; got != want {
		t.Errorf("operation = %q, want %q", got, want)
	}
	if got, want := entries[0].ProviderID, "openrouter"; got != want {
		t.Errorf("provider = %q, want %q", got, want)
	}
	if got, want := entries[0].ModelID, "model"; got != want {
		t.Errorf("model = %q, want %q", got, want)
	}
	if s.Usage != mainUsage {
		t.Fatalf("main Usage = %#v, want unchanged %#v", s.Usage, mainUsage)
	}
}

func TestSessionTitleGeneration_Scenario5_AuxiliaryUsageIsBoundedAndClosed(t *testing.T) {
	s := newTestSession(Limits{})
	for range 17 {
		s.RecordAuxiliaryUsage(AuxiliaryUsage{Operation: AuxiliaryOperationSessionTitle, ProviderID: "p", ModelID: "m"})
	}
	entries := s.AuxiliaryUsage()
	if len(entries) != 16 {
		t.Fatalf("auxiliary entries = %d, want bounded 16", len(entries))
	}
	for _, entry := range entries {
		if entry.Operation != AuxiliaryOperationSessionTitle {
			t.Errorf("operation = %q, want session_title", entry.Operation)
		}
	}
}

func TestADR_0284_AuxiliaryUsageDoesNotSpendRunBudget(t *testing.T) {
	s := newTestSession(Limits{MaxTurns: 1})
	before := s.Usage
	s.RecordAuxiliaryUsage(AuxiliaryUsage{
		Operation: AuxiliaryOperationSessionTitle,
		Usage:     Usage{InputTokens: 500, OutputTokens: 500},
	})
	if got := s.Usage; got != before {
		t.Fatalf("Usage = %#v, want main budget unchanged %#v", got, before)
	}
}

func TestSessionTitleGeneration_Scenario5_OnlyTitleIsPlumbed(t *testing.T) {
	s := newTestSession(Limits{})
	s.SetTitle("First prompt fallback")
	if err := s.SetGeneratedTitle("  Generated\n title  "); err != nil {
		t.Fatalf("SetGeneratedTitle: %v", err)
	}
	if got, want := s.Title, "Generated title"; got != want {
		t.Fatalf("Title = %q, want %q", got, want)
	}
	if got, want := s.TitleProvenance, TitleProvenanceGenerated; got != want {
		t.Errorf("provenance = %q, want %q", got, want)
	}
	if got := s.TitleSourcePrompts(); len(got) != 0 {
		t.Errorf("generated title unexpectedly changed source prompts: %#v", got)
	}
	if err := s.SetGeneratedTitle(strings.Repeat("x", 81)); err != nil {
		t.Fatalf("SetGeneratedTitle long: %v", err)
	}
	if got := len([]rune(s.Title)); got != 80 {
		t.Errorf("generated title runes = %d, want 80 total including ellipsis", got)
	}
}
