package session

import (
	"strings"
	"testing"
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

func TestSessionTitleGeneration_Scenario5_RecordsTokenUsage(t *testing.T) {
	s := newTestSession(Limits{})
	mainUsage := s.Usage

	s.RecordTokenUsage(UsageKindSessionTitle, " openrouter\n", " model \t", Usage{InputTokens: 11, OutputTokens: 7})
	s.RecordTokenUsage(UsageKindSessionTitle, "anthropic", "haiku", Usage{InputTokens: 3, OutputTokens: 5})

	got := s.TokenUsage[UsageKindSessionTitle]
	want := Usage{InputTokens: 14, OutputTokens: 12}
	if got.Total != want || got.Models["openrouter/model"] != (Usage{InputTokens: 11, OutputTokens: 7}) || got.Models["anthropic/haiku"] != (Usage{InputTokens: 3, OutputTokens: 5}) {
		t.Fatalf("canonical title token usage = %#v, want model-attributed total %#v", got, want)
	}
	if s.Usage != mainUsage {
		t.Fatalf("main Usage = %#v, want unchanged %#v", s.Usage, mainUsage)
	}
}

func TestADR_0284_TitleUsageDoesNotSpendRunBudget(t *testing.T) {
	s := newTestSession(Limits{MaxTurns: 1})
	before := s.Usage
	s.RecordTokenUsage(UsageKindSessionTitle, "", "", Usage{InputTokens: 500, OutputTokens: 500})
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
