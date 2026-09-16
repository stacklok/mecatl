package statusline

import (
	"context"
	"strings"
	"testing"
	"time"
)

// TestADR_0247_SourceOwnsDefaultSurfaces pins the source boundary:
// absent template surfaces are source-owned defaults, never a UI fallback.
func TestADR_0247_SourceOwnsDefaultSurfaces(t *testing.T) {
	source := NewDefaultSource(0)
	t.Cleanup(func() { _ = source.Close(context.Background()) })

	source.Submit(Input{
		Terminal: Terminal{HeaderAvailCols: 80, FooterAvailCols: 80},
		Clock:    Clock{Now: time.Date(2026, time.August, 28, 12, 0, 0, 0, time.UTC)},
	})
	select {
	case <-source.Changed():
	case <-time.After(time.Second):
		t.Fatal("source did not publish default surfaces")
	}
	line := source.Latest()
	if !line.Header.Present || !line.Footer.Present {
		t.Fatalf("source defaults = %#v, want both surfaces present", line)
	}
}

func TestStatusLine_DefaultTemplatesExposeLegacyDisplayAtoms(t *testing.T) {
	source := NewDefaultSource(0)
	t.Cleanup(func() { _ = source.Close(context.Background()) })
	source.Submit(Input{
		Session: Session{Handle: "deadbeef", Mode: "plan"},
		Model:   Model{ProviderID: "openai", DisplayName: "GPT-5", Route: "azure"},
		Server:  ServerTarget{DisplayTarget: "server.example"},
		Usage:   Usage{Input: UsageAtom{Raw: 4_000, Human: "4K"}, Output: UsageAtom{Raw: 1_000, Human: "1K"}, CacheWrite: UsageAtom{Raw: 500, Human: "500"}, CacheReadPercent: 75},
		Context: Context{Used: ContextAtom{Raw: 7_000, Human: "7K"}, Window: ContextAtom{Raw: 10_000, Human: "10K"}, Percent: 70},
		Delegation: Delegation{
			Parallel:  DelegationSummary{Running: 1, Finished: 2},
			Subagents: DelegationSummary{Running: 3, Finished: 4},
			Team:      LiveTeam{ID: "abc", Working: 1, Total: 2},
		},
		Terminal: Terminal{HeaderAvailCols: 120, FooterAvailCols: 200},
	})
	select {
	case <-source.Changed():
	case <-time.After(time.Second):
		t.Fatal("source did not publish")
	}
	line := source.Latest()
	if got, want := statusSurfaceText(line.Header), "mecatui · openai/GPT-5/azure · mode plan · server.example"; got != want {
		t.Fatalf("header = %q, want %q", got, want)
	}
	if got := statusSurfaceText(line.Footer); !strings.Contains(got, "⑂ parallel 1◐ 2✓") || !strings.Contains(got, "⛭ subagents 3◐ 4✓") || !strings.Contains(got, "⟳ team-abc · 1/2 working") || !strings.Contains(got, "ctx ▓▓▓▓▓▓░░ 70% · 7K/10K") || !strings.Contains(got, "↑4K ↓1K ⊕500 cache 75%") {
		t.Fatalf("footer = %q, want all legacy display atoms", got)
	}
	if len(line.Header.Spans) < 3 || line.Header.Spans[0].Token != TokenPrimary || line.Header.Spans[1].Token != TokenWarning || line.Header.Spans[2].Token != TokenText {
		t.Fatalf("header spans = %#v, want primary identity, warning mode, and bright connection", line.Header.Spans)
	}
	for _, surface := range []Surface{line.Header, line.Footer} {
		if strings.Contains(statusSurfaceText(surface), "\n") {
			t.Fatalf("surface wrapped: %#v", surface)
		}
	}
}

func TestStatusLine_DefaultFooterSheddingPreservesContextPriority(t *testing.T) {
	input := Input{
		Usage:      Usage{Input: UsageAtom{Raw: 4_000, Human: "4K"}, Output: UsageAtom{Raw: 1_000, Human: "1K"}, CacheReadPercent: 75},
		Context:    Context{Used: ContextAtom{Raw: 7_000, Human: "7K"}, Window: ContextAtom{Raw: 10_000, Human: "10K"}, Percent: 70},
		Delegation: Delegation{Parallel: DelegationSummary{Running: 1, Finished: 2}, Subagents: DelegationSummary{Running: 3, Finished: 4}, Team: LiveTeam{ID: "abc", Working: 1, Total: 2}},
	}
	cases := []struct {
		name, want, absent string
		width              int
	}{
		{name: "full", width: 200, want: "⑂ parallel 1◐ 2✓"},
		{name: "delegation compacts before context", width: 50, want: "⑂ 1◐ 2✓", absent: "parallel"},
		{name: "context survives narrow width", width: 12, want: "ctx 70%", absent: "parallel"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			source := NewDefaultSource(0)
			t.Cleanup(func() { _ = source.Close(context.Background()) })
			input.Terminal.FooterAvailCols = tc.width
			source.Submit(input)
			select {
			case <-source.Changed():
			case <-time.After(time.Second):
				t.Fatal("source did not publish")
			}
			got := statusSurfaceText(source.Latest().Footer)
			if !strings.Contains(got, tc.want) || (tc.absent != "" && strings.Contains(got, tc.absent)) {
				t.Fatalf("footer = %q, want %q and no %q", got, tc.want, tc.absent)
			}
			if strings.Contains(got, "\n") {
				t.Fatalf("footer wrapped: %q", got)
			}
		})
	}
}

func TestStatusLine_Scenario2_PartialSurfaceOverrideKeepsDefault(t *testing.T) {
	source := NewTemplateSource(TemplateSet{Header: SurfaceTemplates{
		Full:    `<header><accent>custom header</accent></header>`,
		Compact: `<header><accent>custom</accent></header>`,
		Minimal: `<header><accent>c</accent></header>`,
	}}, 0)
	t.Cleanup(func() { _ = source.Close(context.Background()) })

	source.Submit(Input{Terminal: Terminal{HeaderAvailCols: 80, FooterAvailCols: 80}})
	select {
	case <-source.Changed():
	case <-time.After(time.Second):
		t.Fatal("source did not publish partial override")
	}
	line := source.Latest()
	if got, want := statusSurfaceText(line.Header), "custom header"; got != want {
		t.Fatalf("header = %q, want custom surface %q", got, want)
	}
	if got := statusSurfaceText(line.Footer); !line.Footer.Present || !strings.Contains(got, "ctx") {
		t.Fatalf("footer = %q, want matching shipped default", got)
	}
}
