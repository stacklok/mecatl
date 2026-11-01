package ui

import (
	"image/color"
	"reflect"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"

	"github.com/stacklok/mecatl/cmd/mecatui/theme"
	"github.com/stacklok/mecatl/cmd/mecatui/ui/internal/scrollback"
)

func transcriptThemeModel(t *testing.T, th theme.Theme, detect bool) Model {
	t.Helper()
	m := newTestModelFromDeps(Deps{Theme: th, ThemeAutoDetect: detect, Ctx: t.Context(), NoBanner: true})
	m = applyAll(m, tea.WindowSizeMsg{Width: 100, Height: 80}, tea.ColorProfileMsg{Profile: colorprofile.TrueColor})
	m.phase = phaseIdle
	m.conv.addUser("user body")
	m.conv.appendReasoning("reasoning body")
	m.conv.appendAssistant("ordinary answer with [docs](https://example.test) and `identifier`.")
	m.conv.endReasoningStream()
	m.conv.addNotice("harness body")
	m.conv.addTurnStat("turn statistics")
	m.conv.addRecoverNotice("recovered session")
	m.conv.addHook("session started", "SessionStart", "", "info")
	m.conv.addHook("denied", "PreToolUse", "Read", "blocked")
	m.conv.addDelivery("daily", "fire-1", "scheduled body")
	m.conv.addTool("done", "Read", `{"path":"done.go"}`)
	m.conv.resolveTool("done", "done", false)
	m.conv.addTool("failed", "Read", `{"path":"failed.go"}`)
	m.conv.resolveTool("failed", "failed", true)
	m.conv.addTool("finalizing", "Read", `{"path":"finalizing.go"}`)
	m.conv.resolveAvailableTool("finalizing", "available", false)
	m.conv.addTool("pending", "Read", `{"path":"pending.go"}`)
	m.refreshView()
	return m
}

func assertTranscriptTheme(t *testing.T, m *Model) {
	t.Helper()
	th := m.deps.Theme
	out := m.conversationContent()
	for _, want := range []string{
		th.Style("transcriptUserMarker").Render(theme.TranscriptUserGlyph),
		th.Style("transcriptUserHeading").Render(" you"),
		th.Style("transcriptHarnessMarker").Render(theme.TranscriptHarnessGlyph),
		th.Style("transcriptHarnessHeading").Render(" system"),
		th.Style("transcriptLLMMarker").Render(theme.TranscriptLLMGlyph),
		th.Style("transcriptLLMHeading").Render(" mecatl"),
		th.Style("transcriptLLMRail").Render("│"),
	} {
		if !strings.Contains(out, want) {
			t.Errorf("transcript lacks styled token %q", want)
		}
	}
	for _, tc := range []struct {
		index int
		role  string
	}{{0, "transcriptUserText"}, {1, "transcriptReasoningText"}, {2, "transcriptHarnessText"}} {
		block := m.rend.renderSnapshot(tc.index, m.conv.scrollback.SnapshotAt(tc.index), m.expandConversation)
		fg := ansi.NewStyle().ForegroundColor(th.Style(tc.role).GetForeground()).String()
		// SGR attributes may share a sequence with the foreground.
		fg = strings.TrimSuffix(strings.TrimPrefix(fg, "\x1b["), "m")
		if !strings.Contains(block, fg) {
			t.Errorf("block %d lacks %s foreground %q: %q", tc.index, tc.role, fg, block)
		}
	}
	if m.expandConversation {
		for _, want := range []string{
			th.Style("transcriptStatistics").Render("turn statistics"),
			"reasoning body",
		} {
			if !strings.Contains(out, want) {
				t.Errorf("expanded transcript lacks %q", want)
			}
		}
	} else {
		for _, hidden := range []string{"reasoning body", "turn statistics"} {
			if strings.Contains(stripANSIstr(out), hidden) {
				t.Errorf("compact transcript exposed %q", hidden)
			}
		}
	}
	for _, want := range []string{"⚠ recovered session", "• hook SessionStart: session started", "✗ hook PreToolUse · Read: blocked — denied", "⏰ scheduled task daily — delivery · fire fire-1", "✓", "✗", "…", "finalizing"} {
		if !strings.Contains(stripANSIstr(out), want) {
			t.Errorf("transcript lost status/provenance %q", want)
		}
	}
	fresh := newRenderer(th, m.rend.marks)
	fresh.setWidth(m.width)
	got := m.rend.renderConversationFrame(&m.conv.scrollback, m.expandConversation)
	want := fresh.renderConversationFrame(&m.conv.scrollback, m.expandConversation)
	if !reflect.DeepEqual(got, want) {
		t.Fatal("cached and fresh transcript frames differ")
	}
	view := m.View().Content
	for _, want := range []string{out, view} {
		for _, token := range []string{
			th.Style("transcriptUserMarker").Render(theme.TranscriptUserGlyph),
			th.Style("transcriptUserHeading").Render(" you"),
			th.Style("transcriptHarnessMarker").Render(theme.TranscriptHarnessGlyph),
		} {
			if !strings.Contains(want, token) {
				t.Errorf("styled transcript token %q did not reach rendered output", token)
			}
		}
		for _, role := range []string{"transcriptHarnessText", "transcriptReasoningText"} {
			fg := ansi.NewStyle().ForegroundColor(th.Style(role).GetForeground()).String()
			if !strings.Contains(want, strings.TrimSuffix(strings.TrimPrefix(fg, "\x1b["), "m")) {
				t.Errorf("%s foreground did not reach rendered output", role)
			}
		}
		if m.expandConversation && (!strings.Contains(want, th.Style("transcriptLLMMarker").Render(theme.TranscriptLLMGlyph)) ||
			!strings.Contains(want, th.Style("transcriptStatistics").Render("turn statistics"))) {
			t.Error("expanded LLM/statistics styles did not reach the rendered output")
		}
	}
}

func TestTranscriptAutoDetectRenderedLightAndCaches(t *testing.T) {
	for _, expanded := range []bool{false, true} {
		t.Run(map[bool]string{false: "compact", true: "F9"}[expanded], func(t *testing.T) {
			m := transcriptThemeModel(t, theme.NewRegistry().Default(), true)
			if expanded {
				m = applyAll(m, f9)
			}
			assertTranscriptTheme(t, &m)
			dark := m.rend
			_ = m.View()
			_ = m.conversationContent()
			if len(dark.blocks.rendered) == 0 || len(dark.blocks.markdown) == 0 || dark.blocks.prefixN == 0 || dark.stream.md == nil {
				t.Fatal("dark block/markdown/join/glamour caches were not primed")
			}
			before := m.conv.testBlocks()
			m = applyAll(m, tea.BackgroundColorMsg{Color: color.White})
			if m.rend == dark || m.deps.Theme.Name != "solar" || !reflect.DeepEqual(before, m.conv.testBlocks()) {
				t.Fatal("light response failed to replace renderer without changing conversation")
			}
			assertTranscriptTheme(t, &m)
			out := m.conversationContent()
			for _, role := range []string{"transcriptUserMarker", "transcriptLLMMarker", "transcriptHarnessMarker"} {
				fg := ansi.NewStyle().ForegroundColor(dark.th.Style(role).GetForeground()).String()
				fg = strings.TrimSuffix(strings.TrimPrefix(fg, "\x1b["), "m")
				if strings.Contains(out, fg) || strings.Contains(m.View().Content, fg) {
					t.Errorf("light output retains dark %s", role)
				}
			}
			m = applyAll(m, tea.BackgroundColorMsg{Color: color.Black})
			if m.conversationContent() != out {
				t.Fatal("duplicate response changed rendered transcript")
			}
			m = applyAll(m, ctrlT)
			if s := toolcallsForTest(t, m); s == nil || len(s.entries) != 4 || m.expandConversation != expanded {
				t.Fatal("Ctrl+T changed conversation detail or tool inspector entries")
			}
			m = applyAll(m, tea.KeyPressMsg{Code: tea.KeyEsc})
			if m.conversationContent() != out {
				t.Fatal("inspector close changed transcript")
			}
			m = applyAll(m, f9)
			assertTranscriptTheme(t, &m)
		})
	}
}

func TestTranscriptAutoDetectRetainsAnchoredSelection(t *testing.T) {
	m := transcriptThemeModel(t, theme.NewRegistry().Default(), true)
	const text = "ordinary text"
	for range 40 {
		m.conv.addNotice("selected " + text)
	}
	m = applyAll(m, tea.WindowSizeMsg{Width: 100, Height: 30}, tea.KeyPressMsg{Code: tea.KeyPgUp})
	if m.conversationView.mode != anchored || m.vp.YOffset() == 0 || m.vp.AtBottom() {
		t.Fatal("fixture must be anchored away from both ends of the conversation")
	}
	selectedLine := -1
	lines := strings.Split(m.vp.GetContent(), "\n")
	for i := m.vp.YOffset() + 1; i < m.vp.YOffset()+m.vp.Height()-1; i++ {
		if strings.Contains(ansi.Strip(lines[i]), text) {
			selectedLine = i
			break
		}
	}
	if selectedLine < 0 {
		t.Fatal("ordinary notice body not visible in anchored viewport")
	}
	plain := ansi.Strip(lines[selectedLine])
	x := lipgloss.Width(plain[:strings.Index(plain, text)])
	y := convTopRow(m) + selectedLine - m.vp.YOffset()
	m, _ = pressMouse(m, tea.MouseLeft, x, y)
	m, _ = motionMouse(m, x+len(text), y)
	if !m.sel.active || selectedText(m.vp.GetContent(), m.sel) != text || m.sel.snapshot != text || m.sel.copied != text || m.sel.anchorPoint.blockID == 0 {
		t.Fatalf("fixture must select actual ordinary body text with logical endpoints: %+v", m.sel)
	}
	beforeSelection, beforeAnchor := m.sel, m.conversationView.anchor
	beforeOffset, beforeMode, beforeHeight := m.vp.YOffset(), m.conversationView.mode, m.vp.Height()
	dark := m.rend
	darkBG := selectionBgSGR(t, m)
	if !strings.Contains(m.View().Content, openSelectionSGR(t, m)+text) ||
		len(dark.blocks.rendered) == 0 || len(dark.blocks.markdown) == 0 || dark.blocks.prefixN == 0 || dark.stream.md == nil {
		t.Fatal("dark selection and block/markdown/join/glamour caches were not primed")
	}
	m = applyAll(m, tea.BackgroundColorMsg{Color: color.White})
	if m.deps.Theme.Name != "solar" || m.rend == dark {
		t.Fatal("real light response did not switch theme and rebuild renderer")
	}
	if m.sel != beforeSelection || selectedText(m.vp.GetContent(), m.sel) != text || m.vp.Height() != beforeHeight ||
		m.conversationView.anchor != beforeAnchor || m.conversationView.mode != beforeMode {
		t.Fatalf("light response changed selection payload/endpoints or anchored reading position: selection=%+v want=%+v anchor=%+v want=%+v offset=%d want=%d mode=%v want=%v height=%d want=%d", m.sel, beforeSelection, m.conversationView.anchor, beforeAnchor, m.vp.YOffset(), beforeOffset, m.conversationView.mode, beforeMode, m.vp.Height(), beforeHeight)
	}
	lightBG := selectionBgSGR(t, m)
	if lightBG == darkBG || !strings.Contains(m.View().Content, openSelectionSGR(t, m)+text) || strings.Contains(m.View().Content, darkBG) {
		t.Fatal("selected text did not acquire the light selection style, or retained dark styling")
	}
	// The notice does not rewrap here; F9 only changes earlier assistant/stat rows.
	for _, msg := range []tea.Msg{tea.WindowSizeMsg{Width: 80, Height: 30}, f9, f9} {
		m = applyAll(m, msg)
		if !m.sel.active || m.sel.snapshot != text || selectedText(m.vp.GetContent(), m.sel) != text ||
			m.sel.anchorPoint != beforeSelection.anchorPoint || m.sel.headPoint != beforeSelection.headPoint ||
			m.conversationView.anchor != beforeAnchor || m.conversationView.mode != anchored {
			t.Fatalf("%T changed logical selection or reading anchor", msg)
		}
		if !strings.Contains(m.View().Content, openSelectionSGR(t, m)+text) || strings.Contains(m.View().Content, darkBG) {
			t.Fatalf("%T lost light selection highlight", msg)
		}
	}
}

func TestTranscriptAutoDetectRenderedFallbacks(t *testing.T) {
	custom, err := theme.ParseTheme([]byte(`{"name":"custom","palette":{"user":"#112233","assistant":"#334455","text":"#ddeeff","textMuted":"#778899","info":"#556677"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if custom.Palette.User != "#112233" || custom.Palette.Text != "#ddeeff" {
		t.Fatal("custom theme fixture did not install palette overrides")
	}
	for _, tc := range []struct {
		name   string
		th     theme.Theme
		detect bool
		reply  color.Color
	}{
		{"absent", theme.NewRegistry().Default(), true, nil},
		{"dark", theme.NewRegistry().Default(), true, color.Black},
		{"explicit builtin", theme.NewRegistry().Default(), false, color.White},
		{"explicit light", theme.Solar(), false, color.Black},
		{"custom", custom, false, color.White},
		{"custom New", theme.New("custom-new", custom.Palette), false, color.White},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := transcriptThemeModel(t, tc.th, tc.detect)
			m = applyAll(m, f9)
			before := m.conversationContent()
			if tc.reply != nil {
				m = applyAll(m, tea.BackgroundColorMsg{Color: tc.reply})
			}
			if m.conversationContent() != before || m.deps.Theme.Name != tc.th.Name {
				t.Fatal("fallback or explicit theme changed rendered transcript")
			}
			assertTranscriptTheme(t, &m)
			if tc.reply != nil {
				m = applyAll(m, tea.BackgroundColorMsg{Color: color.White})
				if m.conversationContent() != before {
					t.Fatal("late light response changed fallback or explicit theme")
				}
			}
		})
	}
}

func TestTranscriptAnswerPreservesMarkdown(t *testing.T) {
	for _, th := range []theme.Theme{theme.NewRegistry().Default(), theme.Solar()} {
		r := newRenderer(th, defaultHelpKeys())
		r.setWidth(100)
		p := scrollback.AssistantCardSnapshot{Text: "ordinary answer with [docs](https://example.test) and `identifier`."}
		out := r.renderSnapshot(0, testSnapshot(0, p), false)
		for _, hex := range []string{th.Palette.MdText, th.Palette.MdLink, th.Palette.MdQuote} {
			fg := ansi.NewStyle().ForegroundColor(lipgloss.Color(hex)).String()
			if !strings.Contains(out, strings.TrimSuffix(strings.TrimPrefix(fg, "\x1b["), "m")) {
				t.Errorf("%s answer lost Markdown colour %s: %q", th.Name, hex, out)
			}
		}
		actorFG := ansi.NewStyle().ForegroundColor(th.Style("transcriptLLMMarker").GetForeground()).String()
		body := strings.Join(strings.Split(out, "\n")[2:], "\n")
		if strings.Contains(body, "\x1b[3;") || strings.Contains(body, "\x1b[3m") ||
			strings.Contains(body, strings.TrimSuffix(strings.TrimPrefix(actorFG, "\x1b["), "m")) {
			t.Errorf("%s normal answer became italic or actor-accent washed", th.Name)
		}
	}
}
