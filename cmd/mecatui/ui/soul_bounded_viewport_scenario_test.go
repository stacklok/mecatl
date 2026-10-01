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
	s := soulActive(m)
	for _, height := range []int{10, 24, 60} {
		out, _ := s.Render(94, height)
		if s.viewport == nil {
			t.Fatalf("height %d: no viewport: %q", height, out)
		}
		title := len(strings.Split(ansi.Wrap("Soul (persona)", 94, ""), "\n"))
		meta := len(strings.Split(ansi.Wrap(renderSoulMeta(s.soul), 94, ""), "\n"))
		footer := len(strings.Split(ansi.Wrap(soulPanelFooter(s.soul, s.deps.marks), 94, ""), "\n"))
		want := height - title - meta - footer - 4 // three separators and overflow indicator
		if s.viewport.Height() != want {
			t.Fatalf("height %d: viewport %d, want %d", height, s.viewport.Height(), want)
		}
		if lipgloss.Height(out) > height || !strings.Contains(ansi.Strip(out), fmt.Sprintf("lines 1–%d of 90", want)) {
			t.Fatalf("height %d: unexpected view %q", height, ansi.Strip(out))
		}
	}
	short := soulScenario(t, "one row")
	body, _ := soulActive(short).Render(94, 10)
	if strings.Contains(ansi.Strip(body), "lines 1") || soulActive(short).viewport.Height() != 10-1-1-len(strings.Split(ansi.Wrap(soulPanelFooter(soulActive(short).soul, soulActive(short).deps.marks), 94, ""), "\n"))-3 {
		t.Fatalf("non-overflow geometry/indicator: %q", ansi.Strip(body))
	}

	for _, size := range [][2]int{{1, 1}, {10, 3}, {94, 5}} {
		out, _ := s.Render(size[0], size[1])
		want := ansi.Cut(s.deps.marks.closeOnly+" close", 0, size[0])
		if s.viewport != nil || ansi.Strip(out) != want || lipgloss.Height(out) > size[1] {
			t.Fatalf("compact %v: %q, want close only %q", size, ansi.Strip(out), want)
		}
	}
	out, _ := s.Render(0, 0)
	if out != "" {
		t.Fatalf("nonpositive: %q", out)
	}
}

func TestMecatuiSoulBoundedViewport_Scenario1_WrappedPhysicalEndpointsAndResize(t *testing.T) {
	content := "spaced words and words\n" + strings.Repeat("longtoken", 12) + "\n界界界界界\ncontinuation at end"
	m := soulScenario(t, content)
	s := soulActive(m)
	s.Render(18, 16)
	expected := []string{}
	for _, line := range strings.Split(content, "\n") {
		expected = append(expected, strings.Split(ansi.Wrap(line, 18, ""), "\n")...)
	}
	if s.total != len(expected) || s.total <= s.viewport.Height() {
		t.Fatalf("physical rows %d, want %d", s.total, len(expected))
	}
	for i, row := range expected {
		s.viewport.Move(bounded.Top, s.total)
		for range i {
			s.viewport.Move(bounded.LineDown, s.total)
		}
		window, _ := s.Render(18, 16)
		if !strings.Contains("\n"+ansi.Strip(window)+"\n", "\n"+row+"\n") {
			t.Fatalf("physical row %d %q unreachable: %q", i, row, ansi.Strip(window))
		}
	}

	s.viewport.Move(bounded.End, s.total)
	out, _ := s.Render(18, 16)
	if !strings.Contains(ansi.Strip(out), "continuation at\nend") || s.viewport.Offset() != s.total-s.viewport.Height() {
		t.Fatalf("last physical row missing: %q", ansi.Strip(out))
	}
	s.Render(35, 25)
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
	m := soulScenario(t, soulScenarioRows())
	s := soulActive(m)
	m.keys.Up = key.NewBinding(key.WithKeys("ctrl+a"))
	m.keys.Down = key.NewBinding(key.WithKeys("ctrl+b"))
	m.keys.ScrollD = key.NewBinding(key.WithKeys("ctrl+g"))
	m.keys.ScrollU = key.NewBinding(key.WithKeys("ctrl+h"))
	m.keys.ScrollTop = key.NewBinding(key.WithKeys("ctrl+j"))
	m.keys.ScrollBottom = key.NewBinding(key.WithKeys("ctrl+k"))
	s.deps.keys = m.keys
	_ = m.View()
	h := s.viewport.Height()
	for _, step := range []struct {
		key  rune
		want int
	}{{'b', 1}, {'g', 1 + h}, {'h', 1}, {'a', 0}, {'k', 90 - h}, {'j', 0}} {
		mm, _ := m.Update(tea.KeyPressMsg{Code: step.key, Mod: tea.ModCtrl})
		m = mm.(Model)
		if got := s.viewport.Offset(); got != step.want {
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
	s.Render(5, 1)
	if s.viewport != nil {
		t.Fatal("compact constructed viewport")
	}
	if _, handled := s.HandleWheel(tea.MouseWheelMsg{Button: tea.MouseWheelDown}); !handled {
		t.Fatal("compact soul must consume wheel")
	}
	mm, _ = m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelDown})
	m = mm.(Model)
	if s.viewport != nil || !m.vp.AtBottom() {
		t.Fatal("compact wheel escaped soul or reconstructed viewport")
	}
}
