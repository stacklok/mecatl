package ui

import (
	"context"
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	customization "github.com/stacklok/mecatl/cmd/mecatui/customization"
	"github.com/stacklok/mecatl/cmd/mecatui/theme"
)

func canonicalFrame(source customization.Source, width int) Model {
	m := New(Deps{Ctx: context.Background(), Theme: theme.New("aztec", theme.AztecPalette()), StatusSource: source})
	m.width, m.height = width, 24
	m.phase = phaseIdle
	m.sessionID = "session-1234567890"
	m.resolvedSessionModel = client.ResolvedModel{ModelID: "canonical-model", ContextWindow: 200000}
	m.contextTokens = 100000
	m.usage.InputTokens = 1234
	return m
}

func TestCanonicalStatus_Scenario1_StockSourceOwnsSurfaces(t *testing.T) {
	for _, tc := range []struct {
		name, wantFooter, absentFooter string
		width                          int
	}{
		{name: "wide full", width: 120, wantFooter: "100K/200K"},
		{name: "narrow compact", width: 48, wantFooter: "ctx ▒▒▒▒░░░░ 50%", absentFooter: "100K/200K"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source := customization.NewDefaultSource(0)
			t.Cleanup(func() { _ = source.Close(context.Background()) })
			m := canonicalFrame(source, tc.width)
			source.Submit(m.statusLineSnapshot())
			updated, _ := m.Update(waitStatusMessage(t, m.statusLineWaitCmd()))
			m = updated.(Model)
			header, footer := stripANSIstr(m.renderHeader()), stripANSIstr(m.renderFooter())
			if !m.generatedStatusLine.Header.Present || !m.generatedStatusLine.Footer.Present || !strings.Contains(header, "canonical-model") || !strings.Contains(footer, tc.wantFooter) {
				t.Fatalf("width %d stock surfaces: header %q footer %q result %#v", tc.width, header, footer, m.generatedStatusLine)
			}
			if tc.absentFooter != "" && strings.Contains(footer, tc.absentFooter) {
				t.Fatalf("width %d retained wide footer content %q: %q", tc.width, tc.absentFooter, footer)
			}
			if strings.Count(header, "canonical-model") != 1 || strings.Count(footer, "ctx") != 1 || lipgloss.Width(strings.Split(header, "\n")[0]) > tc.width || lipgloss.Width(strings.Split(footer, "\n")[1]) > tc.width {
				t.Fatalf("width %d duplicate or oversized renderer content: %q / %q", tc.width, header, footer)
			}
		})
	}
}

func TestCanonicalStatus_Scenario1_EmptySourceDoesNotFallBack(t *testing.T) {
	source := &statusSourceFake{changed: make(chan struct{}, 1)}
	m := canonicalFrame(source, 110)
	m.caps.Posture = postureAuto
	m.generatedStatusLine = customization.Result{Header: customization.Surface{Present: true, Spans: []customization.Span{{Text: strings.Repeat("oversize", 100)}}}, Footer: customization.Surface{Present: true, Spans: []customization.Span{{Text: strings.Repeat("oversize", 100)}}}}
	for _, result := range []customization.Result{m.generatedStatusLine, {}, {Header: customization.Surface{Present: true}, Footer: customization.Surface{Present: true}}} {
		m.generatedStatusLine = result
		header, footer := stripANSIstr(m.renderHeader()), stripANSIstr(m.renderFooter())
		if strings.Contains(header, "session") || strings.Contains(header, "canonical-model") || strings.Contains(header, "oversize") || strings.Contains(header, "·") || strings.Contains(footer, "ctx") || strings.Contains(footer, "oversize") {
			t.Fatalf("source fallback: %q / %q", header, footer)
		}
		if !strings.Contains(header, "auto") || !strings.Contains(footer, "ready") || !strings.Contains(footer, "help") {
			t.Fatalf("lost mandatory chrome: %q / %q", header, footer)
		}
	}
}

func TestCanonicalStatus_Scenario1_NoSourceMinimalIdentity(t *testing.T) {
	m := canonicalFrame(nil, 110)
	m.caps.Posture = postureAuto
	for _, id := range []string{"", "safe-1234567890", "hostile\x1b[31m\nmore-1234567890", "\xff"} {
		m.sessionID = id
		header, footer := stripANSIstr(m.renderHeader()), stripANSIstr(m.renderFooter())
		if handle := client.SessionHandle(id); handle == "" {
			if strings.Contains(header, "session") || strings.Contains(header, "connecting") {
				t.Fatalf("unknown/invalid identity: %q", header)
			}
		} else if !strings.Contains(header, "session "+handle) {
			t.Fatalf("known identity: %q", header)
		}
		if strings.Contains(header, "canonical-model") || strings.Contains(header, "[31m") || strings.Contains(footer, "ctx") || strings.Contains(footer, "↑") || !strings.Contains(header, "auto") || !strings.Contains(footer, "ready") || !strings.Contains(footer, "help") {
			t.Fatalf("unsafe/legacy identity or lost chrome: %q / %q", header, footer)
		}
	}
}

func TestCanonicalStatus_Scenario1_NoSourceDebugKeepsBothIdentities(t *testing.T) {
	m := canonicalFrame(nil, 120)
	m.sessionID = "debug-session-1234567890\x1b[31m"
	m.deps.DebugTarget = "target-session-1234567890\x1b[31m"
	m.sessionTitle = "source identity"
	cue := "⚠ DEBUG target " + client.SessionHandle(m.deps.DebugTarget)
	handle := "session " + client.SessionHandle(m.sessionID)

	t.Run("no source", func(t *testing.T) {
		header := stripANSIstr(m.renderHeader())
		if i, j := strings.Index(header, cue), strings.Index(header, handle); i < 0 || j <= i || strings.Contains(header, "[31m") || strings.Contains(header, "canonical-model") {
			t.Fatalf("debugger and target identities: %q", header)
		}
		if footer := stripANSIstr(m.renderFooter()); strings.Contains(footer, "ctx") || !strings.Contains(footer, "help") {
			t.Fatalf("nil-source footer: %q", footer)
		}
	})
	t.Run("source backed", func(t *testing.T) {
		m = stockStatusFrame(t, m)
		header := stripANSIstr(m.renderHeader())
		if i, j := strings.Index(header, cue), strings.Index(header, "source identity"); i < 0 || j <= i || strings.Contains(header, handle) {
			t.Fatalf("debug prefix and stock identity: %q", header)
		}
	})
}

func TestCanonicalStatus_Scenario2_DebugWarningPrecedesGeneratedHeader(t *testing.T) {
	for _, sourceKind := range []string{"stock", "configured"} {
		m := canonicalFrame(&statusSourceFake{changed: make(chan struct{}, 1)}, 120)
		m.deps.DebugTarget = "hostile\x1b[31m\nmore-1234567890"
		text := "custom header"
		if sourceKind == "stock" {
			source := customization.NewDefaultSource(0)
			t.Cleanup(func() { _ = source.Close(context.Background()) })
			m.deps.StatusSource = source
			source.Submit(m.statusLineSnapshot())
			updated, _ := m.Update(waitStatusMessage(t, m.statusLineWaitCmd()))
			m = updated.(Model)
			text = "canonical-model"
		} else {
			m.generatedStatusLine.Header = customization.Surface{Present: true, Spans: []customization.Span{{Text: text}}}
		}
		cue := "⚠ DEBUG target " + client.SessionHandle(m.deps.DebugTarget)
		header := stripANSIstr(m.renderHeader())
		if i, j := strings.Index(header, cue), strings.Index(header, text); i < 0 || j <= i || strings.Contains(header, "[31m") {
			t.Fatalf("%s warning/header: %q", sourceKind, header)
		}
		m.width = 38
		header = strings.Join(strings.Fields(stripANSIstr(m.renderHeader())), " ")
		if !strings.Contains(header, cue) || strings.Contains(header, "custom header") || strings.Contains(header, "canonical-model") {
			t.Fatalf("%s narrow warning: %q", sourceKind, header)
		}
	}
}

func TestCanonicalStatus_Scenario2_DebugDisclosureSurvivesMissingSourceAndFatal(t *testing.T) {
	for _, kind := range []string{"missing", "empty", "stale", "oversize"} {
		m := canonicalFrame(nil, 40)
		m.deps.DebugTarget = "target-1234567890"
		m.deps.DebugMCP = []string{"github"}
		if kind != "missing" {
			source := &statusSourceFake{changed: make(chan struct{}, 1)}
			m.deps.StatusSource = source
			if kind == "stale" {
				m.generatedStatusLine.Header = customization.Surface{Present: true, Spans: []customization.Span{{Text: "old status"}}}
				source.publish(customization.Result{})
				updated, _ := m.Update(waitStatusMessage(t, m.statusLineWaitCmd()))
				m = updated.(Model)
				if m.generatedStatusLine.Header.Present {
					t.Fatal("stale header survived source update")
				}
			}
			if kind == "empty" {
				m.generatedStatusLine.Header = customization.Surface{Present: true}
			}
			if kind == "oversize" {
				m.generatedStatusLine.Header = customization.Surface{Present: true, Spans: []customization.Span{{Text: strings.Repeat("too-wide", 50)}}}
			}
		}
		for _, fatal := range []bool{false, true} {
			if fatal {
				m.phase = phaseFatal
			}
			rendered := m.renderHeader()
			if fatal {
				rendered = m.View().Content
			}
			plain := strings.Join(strings.Fields(stripANSIstr(rendered)), " ")
			if !strings.Contains(plain, "⚠ DEBUG target "+client.SessionHandle(m.deps.DebugTarget)) || !strings.Contains(plain, "PRIVACY: target evidence sent to the configured model may include prompts, assistant output, tool arguments/results, file paths, and secrets") || !strings.Contains(plain, "availability does not authorize publication or sending.") {
				t.Fatalf("%s fatal=%t missing disclosure: %q", kind, fatal, plain)
			}
			if kind == "empty" && strings.Contains(plain, "·") {
				t.Fatalf("empty generated header added separator: %q", plain)
			}
			if fatal {
				if lipgloss.Height(rendered) > m.height || !strings.Contains(plain, "quit") {
					t.Fatalf("%s fatal outside viewport or no exit: %q", kind, plain)
				}
			}
		}
	}
}

func TestCanonicalStatus_Scenario2_DebugCommandSourceReceivesReservedWidth(t *testing.T) {
	m := canonicalFrame(nil, 120)
	m.deps.DebugTarget = "target-1234567890"
	m.caps.Posture = postureAuto
	m.conv.recordFileChange("changed.go")
	cue := "⚠ DEBUG target " + client.SessionHandle(m.deps.DebugTarget)
	budget := m.width - 2 - lipgloss.Width(cue) - 5 - lipgloss.Width(autoBadgeText) - 2 - lipgloss.Width("✎ 1 file") - headerGapPad
	if budget <= 0 {
		t.Fatalf("no command header budget: %d", budget)
	}
	identity := strings.Repeat("x", budget)
	// Match the exact JSON field received on stdin, not a renderer-local width.
	script := fmt.Sprintf(`read -r input; case "$input" in *'"HeaderAvailCols":%d,'*) printf '<status><header><text>%s</text></header></status>' ;; *) printf '<status><header><text>wrong-budget</text></header></status>' ;; esac`, budget, identity)
	source := customization.NewCommandSource(customization.Command{Path: "/bin/sh", Args: []string{"-c", script}, LaunchDir: t.TempDir()})
	t.Cleanup(func() { _ = source.Close(context.Background()) })
	m.deps.StatusSource = source
	m.submitStatusLine()
	updated, _ := m.Update(waitStatusMessage(t, m.statusLineWaitCmd()))
	m = updated.(Model)
	header := stripANSIstr(m.renderHeader())
	first := strings.Split(header, "\n")[0]
	if i, j := strings.Index(first, cue), strings.Index(first, identity); i < 0 || j <= i || !strings.Contains(first, autoBadgeText) || !strings.Contains(first, "✎ 1 file") || strings.Contains(header, "wrong-budget") || lipgloss.Width(first) > m.width {
		t.Fatalf("command source width %d displaced debug warning or indicator: %q (surface %#v)", budget, header, m.generatedStatusLine.Header)
	}
	if !m.generatedStatusLine.Header.Present || !statusSurfaceFits(m.generatedStatusLine.Header, budget) || !strings.Contains(header, "PRIVACY:") {
		t.Fatalf("command source did not fit beside mandatory debug chrome: %#v / %q", m.generatedStatusLine.Header, header)
	}
}

func TestCanonicalStatus_Scenario2_DebugReservationReachesSource(t *testing.T) {
	source := &statusSourceFake{changed: make(chan struct{}, 1)}
	m := canonicalFrame(source, 120)
	m.deps.DebugTarget = "target-1234567890"
	m.width = 100
	m = applyAll(m, tea.WindowSizeMsg{Width: 120, Height: 24})
	first, ok := source.lastInput()
	if !ok {
		t.Fatal("no input")
	}
	warningWidth := lipgloss.Width(m.debugHeaderTarget())
	if first.Terminal.HeaderAvailCols != m.statusLineGeometry().headerAvailable || first.Terminal.HeaderAvailCols > 120-2-warningWidth {
		t.Fatalf("warning not reserved: %#v", first.Terminal)
	}
	m = applyAll(m, client.SessionReadyMsg{SessionID: m.sessionID, Capabilities: client.Capabilities{Posture: postureAuto}})
	second, _ := source.lastInput()
	if second.Terminal.HeaderAvailCols >= first.Terminal.HeaderAvailCols {
		t.Fatalf("posture did not reserve: %d -> %d", first.Terminal.HeaderAvailCols, second.Terminal.HeaderAvailCols)
	}
	m = applyAll(m, client.ToolCallMsg{ID: "write-1", Name: "Write", Args: `{"path":"changed.go","content":"x"}`})
	withNavigation, _ := source.lastInput()
	if withNavigation.Terminal.HeaderAvailCols >= second.Terminal.HeaderAvailCols || !strings.Contains(stripANSIstr(m.renderHeader()), "✎ 1 file") {
		t.Fatalf("navigation did not reserve: %d -> %d", second.Terminal.HeaderAvailCols, withNavigation.Terminal.HeaderAvailCols)
	}
	m = applyAll(m, tea.WindowSizeMsg{Width: 80, Height: 24})
	third, _ := source.lastInput()
	if third.Terminal.HeaderAvailCols >= second.Terminal.HeaderAvailCols || third.Terminal.HeaderAvailCols != m.statusLineGeometry().headerAvailable {
		t.Fatalf("resize reservation: %d -> %d", second.Terminal.HeaderAvailCols, third.Terminal.HeaderAvailCols)
	}
	stock := customization.NewDefaultSource(0)
	t.Cleanup(func() { _ = stock.Close(context.Background()) })
	m.deps.StatusSource = stock
	stock.Submit(m.statusLineSnapshot())
	updated, _ := m.Update(waitStatusMessage(t, m.statusLineWaitCmd()))
	m = updated.(Model)
	if !m.generatedStatusLine.Header.Present || !statusSurfaceFits(m.generatedStatusLine.Header, third.Terminal.HeaderAvailCols) || !strings.Contains(stripANSIstr(m.renderHeader()), "⚠ DEBUG target") {
		t.Fatalf("stock source displaced warning at reserved width %d: %#v", third.Terminal.HeaderAvailCols, m.generatedStatusLine.Header)
	}
}
