package ui

import (
	"strings"
	"testing"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/theme"
)

func TestResumableSessionStatusMetrics_Scenario3_StartupResumeRestoresStatus(t *testing.T) {
	resume := &client.ResumeSelection{
		Row: client.SessionListItem{ID: "resumed-main", Title: "Restored chat"},
		Snapshot: client.SessionSnapshot{
			ResolvedModel:    client.ResolvedModel{ContextWindow: 200_000},
			Usage:            client.Usage{InputTokens: 120_000, OutputTokens: 4_000, CacheReadTokens: 90_000},
			ContextOccupancy: &client.ContextOccupancy{InputTokens: 40_000, Estimated: true},
		},
	}
	m := New(Deps{Resume: resume, Theme: theme.New("aztec", theme.AztecPalette()), NoAltScreen: true})

	if m.usage != resume.Snapshot.Usage || m.contextTokens != 40_000 || !m.contextEstimated {
		t.Fatalf("startup resume did not adopt snapshot status: usage=%+v context=%d estimated=%t", m.usage, m.contextTokens, m.contextEstimated)
	}
	footer := stripANSIstr(m.fitFooter("connected", 160))
	for _, want := range []string{"~40K/200K", "20%", "↑120K", "↓4K", "cache 75%"} {
		if !strings.Contains(footer, want) {
			t.Fatalf("footer = %q, want %q before a prompt", footer, want)
		}
	}
}

func TestResumableSessionStatusMetrics_Scenario3_UnknownAndProvisionalStatus(t *testing.T) {
	resume := &client.ResumeSelection{
		Row: client.SessionListItem{ID: "legacy-main"},
		Snapshot: client.SessionSnapshot{
			ResolvedModel: client.ResolvedModel{ContextWindow: 0},
			Usage:         client.Usage{InputTokens: 120_000},
		},
	}
	m := New(Deps{Resume: resume, Theme: theme.New("aztec", theme.AztecPalette()), NoAltScreen: true})
	if m.contextKnown {
		t.Fatal("legacy snapshot without occupancy must remain unknown")
	}
	if footer := stripANSIstr(m.fitFooter("connected", 160)); !strings.Contains(footer, "ctx ?") || strings.Contains(footer, "120K/") {
		t.Fatalf("legacy footer = %q, want an unknown context numerator, never cumulative input", footer)
	}

	m = applyAll(m, client.ResolvedModelMsg{SessionID: "legacy-main", Resolved: client.ResolvedModel{ContextWindow: 200_000}})
	if m.resolvedSessionModel.ContextWindow != 200_000 {
		t.Fatalf("resolved-model refresh did not heal provisional window: %d", m.resolvedSessionModel.ContextWindow)
	}
	if m.usage.InputTokens != 120_000 || m.contextKnown {
		t.Fatalf("resolved-model refresh applied stale metrics: usage=%+v contextKnown=%t", m.usage, m.contextKnown)
	}
	if footer := stripANSIstr(m.fitFooter("connected", 160)); !strings.Contains(footer, "?/200K") || strings.Contains(footer, "120K/200K") {
		t.Fatalf("healed legacy footer = %q, want unknown numerator with healed denominator", footer)
	}
}
