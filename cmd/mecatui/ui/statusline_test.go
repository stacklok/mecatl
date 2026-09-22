package ui

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	statusline "github.com/stacklok/mecatl/cmd/mecatui/statusline"
	"github.com/stacklok/mecatl/cmd/mecatui/theme"
)

func TestStatusLine_Scenario2_DefaultTemplatesPreserveChrome(t *testing.T) {
	s := statusline.NewDefaultSource(0)
	t.Cleanup(func() { _ = s.Close(context.Background()) })
	m, _, _ := newTestModel(t, theme.New("aztec", theme.AztecPalette()))
	m.deps.StatusSource = s
	m.deps.Server = "server.example"
	m.deps.ConnectionMode = "embedded"
	m.sessionID = "session-status-default"
	m.sessionTitle = "Status work"
	m.phase = phaseIdle
	m.contextTokens = 4000
	m.resolvedSessionModel.ContextWindow = 20000
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
	m = updated.(Model)
	updated, _ = m.update(waitStatusMessage(t, m.statusLineWaitCmd()))
	m = updated.(Model)
	header := stripANSIstr(m.renderHeader())
	for _, want := range []string{"mecatui", "session session-stat", "mode default", "server.example"} {
		if !strings.Contains(header, want) {
			t.Fatalf("header %q is missing shipped chrome %q", header, want)
		}
	}
	footer := stripANSIstr(m.renderFooter())
	for _, want := range []string{"ready", "ctx", "4K/20K", "cache", "0%"} {
		if !strings.Contains(footer, want) {
			t.Fatalf("footer %q is missing shipped chrome %q", footer, want)
		}
	}
}
func TestStatusLine_StaleGeneratedSurfaceNeverWraps(t *testing.T) {
	m, _, _ := newTestModel(t, theme.New("aztec", theme.AztecPalette()))
	m.deps.StatusSource = &statusSourceFake{changed: make(chan struct{})}
	m.width = 24
	m.generatedStatusLine = statusline.Result{
		Header: statusline.Surface{Present: true, Spans: []statusline.Span{{Text: "mecatui · session #too-wide"}}},
		Footer: statusline.Surface{Present: true, Spans: []statusline.Span{{Text: "ctx 70% · ↑4K ↓1K cache 75%"}}},
	}

	if header := stripANSIstr(m.renderHeader()); strings.Contains(header, "too-wide") {
		t.Fatalf("stale header was rendered instead of shed: %q", header)
	}
	footerLine := strings.Split(stripANSIstr(m.renderFooter()), "\n")[0]
	if strings.Contains(footerLine, "cache 75%") {
		t.Fatalf("stale footer was rendered instead of shed: %q", footerLine)
	}
}

func TestStatusLine_SourceDoesNotFallBackToLegacyGenerators(t *testing.T) {
	m, _, _ := newTestModel(t, theme.New("aztec", theme.AztecPalette()))
	m.deps.StatusSource = &statusSourceFake{changed: make(chan struct{})}
	m.width = 100
	m.sessionID = "legacy-must-not-leak"
	m.contextTokens = 4_000
	m.resolvedSessionModel.ContextWindow = 20_000

	if header := stripANSIstr(m.renderHeader()); strings.Contains(header, "mecatui") || strings.Contains(header, "session #") {
		t.Fatalf("source-backed empty header fell through to legacy generator: %q", header)
	}
	if footer := stripANSIstr(m.renderFooter()); strings.Contains(footer, "ctx") || strings.Contains(footer, "cache") {
		t.Fatalf("source-backed empty footer fell through to legacy generator: %q", footer)
	}
}

func TestStatusCustomization_Scenario2_PartialSurfaceOverrideKeepsDefault(t *testing.T) {
	s := statusline.NewTemplateSource(statusline.TemplateSet{Header: statusline.SurfaceTemplates{Full: `<header><accent>custom header</accent></header>`}}, 0)
	t.Cleanup(func() { _ = s.Close(context.Background()) })
	s.Submit(statusline.Input{Terminal: statusline.Terminal{HeaderAvailCols: 80, FooterAvailCols: 80}})
	select {
	case <-s.Changed():
	case <-time.After(time.Second):
		t.Fatal("no result")
	}
	line := s.Latest()
	if !strings.Contains(statusSpansText(line.Header.Spans), "custom header") || !line.Footer.Present {
		t.Fatalf("partial surfaces %#v", line)
	}
}
func TestStatusLine_Scenario2_HeaderSystemIndicatorsSurviveOverride(t *testing.T) {
	m, _, _ := newTestModel(t, theme.New("aztec", theme.AztecPalette()))
	m.width = 100
	m.generatedStatusLine.Header = statusline.Render(`<header><accent>CUSTOM</accent></header>`, nil).Header
	m.caps.Posture = postureAuto
	m.conv.filesChanged = []string{"changed.go"}
	header := stripANSIstr(m.renderHeader())
	for _, want := range []string{"CUSTOM", autoBadgeText, "✎ 1 file"} {
		if !strings.Contains(header, want) {
			t.Errorf("missing %q in %q", want, header)
		}
	}
}
func TestStatusLine_RenderStatusSpansUsesLinkThemeStyle(t *testing.T) {
	th := theme.New("aztec", theme.AztecPalette())
	span := statusline.Render(`<footer><link href="https://example.test/docs">docs</link></footer>`, nil).Footer.Spans[0]
	got := renderStatusSpans(th, []statusline.Span{span})
	want := lipgloss.NewStyle().Foreground(lipgloss.Color(th.Palette.MdLink)).Underline(true).Render("docs")
	if got != want || strings.Contains(got, "]8;") {
		t.Fatalf("link output %q", got)
	}
}
func TestStatusLine_Scenario5_DefaultCompatibility(t *testing.T) {
	s := statusline.NewDefaultSource(0)
	t.Cleanup(func() { _ = s.Close(context.Background()) })
	s.Submit(statusline.Input{
		Session: statusline.Session{Handle: "deadbeef", Mode: "default"},
		Model:   statusline.Model{ProviderID: "openai", DisplayName: "GPT-5", Route: "azure"},
		Usage: statusline.Usage{
			Input:  statusline.UsageAtom{Human: "4K"},
			Output: statusline.UsageAtom{Human: "1K"},
		},
		Context:  statusline.Context{Used: statusline.ContextAtom{Raw: 2_000, Human: "2K"}, Window: statusline.ContextAtom{Raw: 10_000, Human: "10K"}, Percent: 20},
		Terminal: statusline.Terminal{HeaderAvailCols: 80, FooterAvailCols: 80},
	})
	select {
	case <-s.Changed():
	case <-time.After(time.Second):
		t.Fatal("no result")
	}
	line := s.Latest()
	if got, want := statusSpansText(line.Header.Spans), "mecatui · session deadbeef · openai/GPT-5/azure · mode default"; got != want {
		t.Fatalf("header = %q, want %q", got, want)
	}
	if got, want := statusSpansText(line.Footer.Spans), "ctx ▒▒░░░░░░ 20% · 2K/10K · ↑4K ↓1K cache 0%"; got != want {
		t.Fatalf("footer = %q, want %q", got, want)
	}
}
