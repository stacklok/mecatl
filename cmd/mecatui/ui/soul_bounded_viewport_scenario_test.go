package ui

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/ui/internal/bounded"
)

func soulScenario(t *testing.T, content string) Model {
	t.Helper()
	m := newSoulModel(t, &fakeSoul{soul: client.Soul{Content: content, Present: true, Provenance: client.SoulProvenanceUser}}, client.Capabilities{Soul: true})
	mm, cmd := m.runSoul()
	return feedCmd(t, mm.(Model), cmd)
}

func soulScenarioRows() string {
	var rows []string
	for i := 0; i < 90; i++ {
		rows = append(rows, fmt.Sprintf("unique-row-%02d", i))
	}
	return strings.Join(rows, "\n")
}

func TestMecatuiSoulBoundedViewport_Scenario1_WidthCapAndFrameAccounting(t *testing.T) {
	for _, width := range []int{24, 80, 180} {
		m := soulScenario(t, "soul body")
		m.width = width
		m.vp.SetWidth(width)
		out := m.renderModalSurface()
		frame := m.deps.Theme.Style("askCard").GetHorizontalFrameSize()
		s := soulActive(m)
		if s.viewport == nil || s.bodyWidth != min(width, 128)-frame {
			t.Fatalf("width %d: viewport/inner width %d, want %d", width, s.bodyWidth, min(width, 128)-frame)
		}
		for _, line := range strings.Split(out, "\n") {
			if ansi.StringWidth(ansi.Strip(line)) > width {
				t.Fatalf("line exceeds %d: %q", width, ansi.Strip(line))
			}
		}
		border := ""
		for _, line := range strings.Split(ansi.Strip(out), "\n") {
			if strings.Contains(line, "┏") {
				border = line
				break
			}
		}
		left := strings.Index(border, "┏")
		if left != (width-min(width, 128))/2 || ansi.StringWidth(strings.TrimSpace(border)) != min(width, 128) {
			t.Fatalf("width %d: card not centered/capped: %q", width, border)
		}
	}
}

func TestMecatuiSoulBoundedViewport_Scenario1_UsesAvailableHeightAndCompactFallback(t *testing.T) {
	m := soulScenario(t, soulScenarioRows())
	for _, height := range []int{32, 52, 80} {
		m = resize(m, 94, height)
		out := m.View().Content
		s := soulActive(m)
		if s.viewport == nil {
			t.Fatalf("height %d: no viewport: %q", height, out)
		}
		offerH := m.vp.Height() - m.deps.Theme.Style("askCard").GetVerticalFrameSize()
		title := len(strings.Split(ansi.Wrap("Soul (persona)", s.bodyWidth, ""), "\n"))
		meta := len(strings.Split(ansi.Wrap(renderSoulMeta(s.soul), s.bodyWidth, ""), "\n"))
		footer := len(strings.Split(ansi.Wrap(soulPanelFooter(s.soul, s.deps.marks), s.bodyWidth, ""), "\n"))
		want := offerH - title - meta - footer - 4 // three separators and overflow indicator
		if s.viewport.Height() != want {
			t.Fatalf("height %d: viewport %d, want %d", height, s.viewport.Height(), want)
		}
		if m.metrics.outerBounds.y1-m.metrics.outerBounds.y0 > m.vp.Height() || !strings.Contains(ansi.Strip(out), fmt.Sprintf("lines 1–%d of 90", want)) {
			t.Fatalf("height %d: unexpected view %q", height, ansi.Strip(out))
		}
	}
	short := soulScenario(t, "one row")
	short = resize(short, 94, 32)
	body := short.View().Content
	s := soulActive(short)
	if strings.Contains(ansi.Strip(body), "lines 1") || s.viewport.Height() != short.vp.Height()-short.deps.Theme.Style("askCard").GetVerticalFrameSize()-1-1-len(strings.Split(ansi.Wrap(soulPanelFooter(s.soul, s.deps.marks), s.bodyWidth, ""), "\n"))-3 {
		t.Fatalf("non-overflow geometry/indicator: %q", ansi.Strip(body))
	}

	for _, size := range [][2]int{{10, 11}, {94, 11}} {
		m = resize(m, size[0], size[1])
		out := m.View().Content
		s := soulActive(m)
		want := ansi.Cut(s.deps.marks.closeOnly+" close", 0, size[0])
		if s.viewport != nil || !strings.Contains(ansi.Strip(out), want) || m.metrics.outerBounds.y1-m.metrics.outerBounds.y0 > m.vp.Height() {
			t.Fatalf("compact %v: %q, want close only %q", size, ansi.Strip(out), want)
		}
	}
	m = resize(m, 94, 0)
	m.vp.SetHeight(0)
	_ = m.View()
	if out := m.renderBody(); out != "" {
		t.Fatalf("nonpositive offer: %q", out)
	}
}

func TestMecatuiSoulBoundedViewport_Scenario1_WrappedPhysicalEndpointsAndResize(t *testing.T) {
	content := "spaced words and words\n" + strings.Repeat("longtoken ", 80) + "\n界界界界界\ncontinuation at end"
	m := soulScenario(t, content)
	m = resize(m, 30, 42)
	_ = m.View()
	s := soulActive(m)
	bodyWidth := s.bodyWidth
	expected := []string{}
	for _, line := range strings.Split(content, "\n") {
		expected = append(expected, strings.Split(ansi.Wrap(line, bodyWidth, ""), "\n")...)
	}
	if s.total != len(expected) || s.total <= s.viewport.Height() {
		t.Fatalf("physical rows %d, want %d", s.total, len(expected))
	}
	for i, row := range expected {
		s.viewport.Move(bounded.Top, s.total)
		for range i {
			s.viewport.Move(bounded.LineDown, s.total)
		}
		m = resize(m, 30, 42)
		window := m.View().Content
		s = soulActive(m)
		if !strings.Contains(ansi.Strip(window), row) {
			t.Fatalf("physical row %d %q unreachable: %q", i, row, ansi.Strip(window))
		}
	}

	s.viewport.Move(bounded.End, s.total)
	out := m.View().Content
	if !strings.Contains(ansi.Strip(out), "continuation at end") || s.viewport.Offset() != s.total-s.viewport.Height() {
		t.Fatalf("last physical row missing: %q", ansi.Strip(out))
	}
	m = resize(m, 35, 31)
	_ = m.View()
	s = soulActive(m)
	if s.viewport.Offset() > max(0, s.total-s.viewport.Height()) {
		t.Fatal("resize did not clamp")
	}
}

func TestMecatuiSoulBoundedViewport_Scenario1_PresentationAndNonBodyStates(t *testing.T) {
	m := soulScenario(t, soulScenarioRows())
	s := soulActive(m)
	s.Render(48, 15)
	s.viewport.Move(bounded.End, s.total)
	cases := []struct {
		name   string
		mutate func()
		want   string
	}{
		{"loading", func() { s.loading = true }, "loading…"},
		{"empty", func() { s.loading = false; s.soul = client.Soul{} }, "No user or project soul is available."},
		{"untrusted", func() { s.soul = client.Soul{Provenance: client.SoulProvenanceProject} }, "not loaded because this workspace is"},
		{"error", func() { s.err = errors.New("failure\x1b[2J") }, "get soul: failure"},
	}
	for _, tc := range cases {
		tc.mutate()
		out, _ := s.Render(48, 15)
		if !strings.Contains(ansi.Strip(out), tc.want) || strings.Contains(out, "\x1b[2J") || strings.Contains(ansi.Strip(out), "unique-row") || lipgloss.Height(out) > 15 {
			t.Fatalf("%s: %q", tc.name, ansi.Strip(out))
		}
		compact, _ := s.Render(48, 3)
		if s.viewport != nil || ansi.Strip(compact) != s.deps.marks.closeOnly+" close" {
			t.Fatalf("%s compact was not close-only: %q", tc.name, ansi.Strip(compact))
		}
	}
	s.Render(48, 15)
	s.HandleMsg(soulResultMsg{owner: s, result: client.SoulMsg{Soul: client.Soul{Content: "new result", Present: true}}})
	out, _ := s.Render(48, 15)
	if s.viewport.Offset() != 0 || !strings.Contains(out, "new result") || strings.Contains(out, "unique-row") {
		t.Fatalf("new result: %q", out)
	}
}

func TestMecatuiSoulBoundedViewport_Scenario2_OpenResultAndClose(t *testing.T) {
	m := newSoulModel(t, sampleSoul(), client.Capabilities{Soul: true})
	m.phase = phaseRunning
	_, blocked := m.runSoul()
	if blocked != nil || m.modal != nil {
		t.Fatal("running session opened soul")
	}
	m.phase = phaseIdle
	m.deps.Soul = nil
	_, blocked = m.runSoul()
	if blocked != nil || m.modal != nil {
		t.Fatal("missing collaborator opened soul")
	}
	m.deps.Soul = sampleSoul()
	mm, old := m.runSoul()
	m = mm.(Model)
	mm, closeCmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
	m = mm.(Model)
	_ = closeCmd
	if m.modal != nil || !m.prompt.Focused() {
		t.Fatal("close/refocus failed")
	}
	mm, fresh := m.runSoul()
	m = mm.(Model)
	mm, _ = m.Update(old())
	m = mm.(Model)
	if !soulActive(m).loading || soulActive(m).soul.Content != "" {
		t.Fatal("prior open replaced new loading")
	}
	mm, _ = m.Update(tea.KeyPressMsg{Code: 'x'})
	m = mm.(Model)
	if m.modal == nil || m.prompt.Focused() {
		t.Fatal("unrelated key leaked")
	}
	m = feedCmd(t, m, fresh)
	if soulActive(m).loading || soulActive(m).soul.Content == "" {
		t.Fatal("current result missing")
	}
	s := soulActive(m)
	_ = m.View()
	before := s.soul.Content
	mm, _ = m.Update(soulResultMsg{owner: &soulState{}, result: client.SoulMsg{Err: errors.New("stale error")}})
	m = mm.(Model)
	if s.loading || s.err != nil || s.soul.Content != before || s.viewport.Offset() != 0 {
		t.Fatal("superseded error replaced current open")
	}
}

func TestMecatuiSoulBoundedViewport_Scenario2_RemappableNavigation(t *testing.T) {
	m := soulScenario(t, strings.Repeat("wrapped physical soul rows ", 80))
	m.keys.Up = key.NewBinding(key.WithKeys("ctrl+a"))
	m.keys.Down = key.NewBinding(key.WithKeys("ctrl+b"))
	m.keys.ScrollD = key.NewBinding(key.WithKeys("ctrl+g"))
	m.keys.ScrollU = key.NewBinding(key.WithKeys("ctrl+h"))
	m.keys.ScrollTop = key.NewBinding(key.WithKeys("ctrl+j"))
	m.keys.ScrollBottom = key.NewBinding(key.WithKeys("ctrl+k"))
	s := soulActive(m)
	s.deps.keys = m.keys
	m = resize(m, 30, 36)
	_ = m.View()
	s = soulActive(m)
	h, total := s.viewport.Height(), s.total
	if total <= h {
		t.Fatalf("wrapped content did not overflow: total=%d height=%d", total, h)
	}
	for _, step := range []struct {
		key  rune
		want int
	}{{'b', 1}, {'g', min(1+h, total-h)}, {'h', 1}, {'a', 0}, {'k', total - h}, {'j', 0}} {
		mm, _ := m.Update(tea.KeyPressMsg{Code: step.key, Mod: tea.ModCtrl})
		m = mm.(Model)
		if got := soulActive(m).viewport.Offset(); got != step.want {
			t.Fatalf("ctrl+%c offset %d, want %d", step.key, got, step.want)
		}
	}
}

func TestMecatuiSoulBoundedViewport_Scenario2_WheelOwnershipAndCompactIsolation(t *testing.T) {
	m := soulScenario(t, strings.Repeat("one two three four five six seven eight ", 40))
	m.conv.addUser("long transcript")
	m.conv.appendAssistant(strings.Repeat("conversation row\n", 120))
	m.conversationView.mode = followTail
	m.refreshView()
	if !m.vp.AtBottom() {
		t.Fatal("conversation must start at bottom")
	}
	s := soulActive(m)
	_ = m.View()
	if s.viewport == nil {
		t.Fatal("expected viewport")
	}
	mm, _ := m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelDown})
	m = mm.(Model)
	if s.viewport.Offset() != 1 {
		t.Fatalf("wheel offset = %d", s.viewport.Offset())
	}
	mm, _ = m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelUp})
	m = mm.(Model)
	if s.viewport.Offset() != 0 || !m.vp.AtBottom() || m.conversationView.mode != followTail {
		t.Fatal("wheel up must return to soul top without scrolling conversation")
	}
	mm, _ = m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelUp})
	m = mm.(Model)
	if s.viewport.Offset() != 0 || !m.vp.AtBottom() {
		t.Fatal("top wheel escaped soul")
	}
	if _, handled := s.HandleWheel(tea.MouseWheelMsg{Button: tea.MouseWheelUp}); !handled {
		t.Fatal("soul must consume wheel at top")
	}
	s.viewport.Move(bounded.End, s.total)
	end := s.viewport.Offset()
	mm, _ = m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelDown})
	m = mm.(Model)
	if s.viewport.Offset() != end || !m.vp.AtBottom() {
		t.Fatal("end wheel escaped soul")
	}
	m = resize(m, 10, 11)
	_ = m.View()
	s = soulActive(m)
	if s.viewport != nil {
		t.Fatal("compact constructed viewport")
	}
	mm, _ = m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelDown})
	m = mm.(Model)
	if soulActive(m).viewport != nil || !m.vp.AtBottom() {
		t.Fatal("compact wheel escaped soul or reconstructed viewport")
	}

	m = resize(m, 94, 30)
	_ = m.View()
	mm, _ = m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelDown})
	m = mm.(Model)
	offset, conversationOffset := soulActive(m).viewport.Offset(), m.vp.YOffset()
	m = resize(m, 94, 0)
	m.vp.SetHeight(0)
	_ = m.View()
	if out := m.renderBody(); out != "" {
		t.Fatalf("zero-height modal should not render: %q", out)
	}
	mm, _ = m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelDown})
	m = mm.(Model)
	if got := soulActive(m).viewport.Offset(); got != offset {
		t.Fatalf("zero-height wheel moved invisible soul from %d to %d", offset, got)
	}
	if m.vp.YOffset() != conversationOffset {
		t.Fatalf("zero-height wheel moved hidden conversation from %d to %d", conversationOffset, m.vp.YOffset())
	}
}
