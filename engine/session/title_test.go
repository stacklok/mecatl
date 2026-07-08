package session

import (
	"strings"
	"testing"
)

func TestSetTitleOnce(t *testing.T) {
	s := newTestSession(Limits{})
	if s.Title != "" {
		t.Fatalf("new session Title = %q, want empty", s.Title)
	}
	// First genuine prompt sticks.
	s.SetTitle("Fix the flaky CI job")
	if got, want := s.Title, "Fix the flaky CI job"; got != want {
		t.Fatalf("after first SetTitle Title = %q, want %q", got, want)
	}
	// A second prompt does NOT overwrite (set-once).
	s.SetTitle("Refactor the auth layer")
	if got, want := s.Title, "Fix the flaky CI job"; got != want {
		t.Fatalf("after second SetTitle Title = %q, want %q (set-once)", got, want)
	}
	// An empty/whitespace-only text on an already-set session is a no-op.
	s.SetTitle("   ")
	if got, want := s.Title, "Fix the flaky CI job"; got != want {
		t.Fatalf("after empty SetTitle Title = %q, want %q", got, want)
	}
}

func TestSetTitleEmptyDoesNotSeed(t *testing.T) {
	s := newTestSession(Limits{})
	// An empty prompt never seeds (e.g. a multimodal-only prompt).
	s.SetTitle("")
	if s.Title != "" {
		t.Fatalf("after empty SetTitle Title = %q, want empty", s.Title)
	}
	// Whitespace-only is equivalent to empty.
	s.SetTitle("   \t\n  ")
	if s.Title != "" {
		t.Fatalf("after whitespace SetTitle Title = %q, want empty", s.Title)
	}
	// A later genuine prompt still seeds (the empty ones did not set-once).
	s.SetTitle("real prompt")
	if got, want := s.Title, "real prompt"; got != want {
		t.Fatalf("after genuine SetTitle Title = %q, want %q", got, want)
	}
}

func TestSetTitleClamps(t *testing.T) {
	s := newTestSession(Limits{})
	long := strings.Repeat("x", 200)
	s.SetTitle(long)
	// Clamped to maxTitleRunes (120) + "…".
	if got, want := s.Title, strings.Repeat("x", maxTitleRunes)+"…"; got != want {
		t.Fatalf("clamped Title len = %d, want %d (+…)", len([]rune(got)), maxTitleRunes+1)
	}
}

func TestClampTitle(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"empty", "", ""},
		{"whitespace-only", "   \t ", ""},
		{"short unchanged", "hello world", "hello world"},
		{"leading/trailing trimmed", "  hello  ", "hello"},
		{"exactly at limit", strings.Repeat("a", maxTitleRunes), strings.Repeat("a", maxTitleRunes)},
		{"over limit truncated", strings.Repeat("a", maxTitleRunes+1), strings.Repeat("a", maxTitleRunes) + "…"},
		{"over limit by a lot", strings.Repeat("a", 500), strings.Repeat("a", maxTitleRunes) + "…"},
		{"rune-safe (multibyte)", strings.Repeat("é", maxTitleRunes+5), strings.Repeat("é", maxTitleRunes) + "…"},
		{"multibyte under limit", "café résumé", "café résumé"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ClampTitle(tc.in)
			if got != tc.want {
				t.Fatalf("ClampTitle(%q) = %q (len runes %d), want %q (len runes %d)",
					tc.in, got, len([]rune(got)), tc.want, len([]rune(tc.want)))
			}
			// Bound: never exceeds maxTitleRunes+1 (the +1 is the "…").
			if n := len([]rune(got)); n > maxTitleRunes+1 {
				t.Fatalf("ClampTitle(%q) rune len %d exceeds max %d+1", tc.in, n, maxTitleRunes)
			}
		})
	}
}

func TestIsSynthesisedSummary(t *testing.T) {
	tests := []struct {
		text string
		want bool
	}{
		{"", false},
		{"a real user prompt", false},
		{CompactionSummaryMarker, true},
		{CompactionSummaryMarker + " Earlier turns were summarised…", true},
		{Tier4SummaryMarker, true},
		{Tier4SummaryMarker + "\n## Goal\n…", true},
		{"[conversation compacted]", true},   // exact marker
		{"[earlier turns summarised]", true}, // exact marker
		{"conversation compacted", false},    // missing the leading bracket
	}
	for _, tc := range tests {
		if got := IsSynthesisedSummary(tc.text); got != tc.want {
			t.Fatalf("IsSynthesisedSummary(%q) = %v, want %v", tc.text, got, tc.want)
		}
	}
}

func TestIsGenuineUserPrompt(t *testing.T) {
	tests := []struct {
		name string
		m    Message
		want bool
	}{
		{"genuine user text", NewUserMessage("fix the bug"), true},
		{"empty user text is still genuine (no summary prefix)", NewUserMessage(""), true},
		{"assistant role", NewAssistantMessage("hi", "", nil), false},
		{"system role", NewSystemMessage("sys"), false},
		{"tool role", NewToolMessage(NewToolResult("c1", "ok")), false},
		{"compaction summary marker", NewUserMessage(CompactionSummaryMarker + " …"), false},
		{"tier4 summary marker", NewUserMessage(Tier4SummaryMarker + "\n…"), false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsGenuineUserPrompt(tc.m); got != tc.want {
				t.Fatalf("IsGenuineUserPrompt(role=%s) = %v, want %v", tc.m.Role, got, tc.want)
			}
		})
	}
}
