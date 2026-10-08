package ui

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/theme"
	"github.com/stacklok/mecatl/cmd/mecatui/ui/internal/bounded"
)

func assertAgentsInventoryFits(t *testing.T, rendered string, width, height int) {
	t.Helper()
	if got := lipgloss.Height(rendered); got > height {
		t.Fatalf("rendered height = %d, want <= %d\n%s", got, height, ansi.Strip(rendered))
	}
	for i, line := range strings.Split(rendered, "\n") {
		if got := ansi.StringWidth(ansi.Strip(line)); got > width {
			t.Fatalf("line %d width = %d, want <= %d: %q", i, got, width, ansi.Strip(line))
		}
	}
}

func agentsInventoryCardWidth(rendered string) int {
	for _, line := range strings.Split(rendered, "\n") {
		plain := ansi.Strip(line)
		if strings.TrimSpace(plain) != "" {
			return ansi.StringWidth(strings.TrimLeft(plain, " "))
		}
	}
	return 0
}

func inventoryFixture(n int) []client.Agent {
	out := make([]client.Agent, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, client.Agent{
			Name:           fmt.Sprintf("agent-%02d", i),
			Description:    strings.Repeat(fmt.Sprintf("description-%02d ", i), 8),
			Model:          "provider/model",
			PermissionMode: "plan",
			Tools:          []string{"Read", "Grep", "Glob"},
		})
	}
	return out
}

func TestMecatuiAgentInventoryBoundedViewport_Scenario1_WidthCapAndFrameAccounting(t *testing.T) {
	th := theme.New("aztec", theme.AztecPalette())
	for _, width := range []int{24, 80, 180} {
		t.Run(fmt.Sprint(width), func(t *testing.T) {
			layout := newAgentsInvLayout(th, defaultHelpKeys(), width, 80)
			if !layout.normal {
				t.Fatalf("width %d with tall geometry unexpectedly compact", width)
			}
			wantOuter := min(128, width)
			if layout.outerWidth != wantOuter {
				t.Fatalf("outer width = %d, want %d", layout.outerWidth, wantOuter)
			}
			frame := th.Style("askCard").GetHorizontalFrameSize()
			if layout.bodyWidth != layout.outerWidth-frame {
				t.Fatalf("body width = %d, want measured outer %d - frame %d", layout.bodyWidth, layout.outerWidth, frame)
			}
			st := agentsInvState{view: agentsInvPanel, agents: inventoryFixture(2), viewport: newAgentsInvViewport()}
			out := renderAgentsInvOverlay(th, st, client.Capabilities{Agents: true}, defaultHelpKeys(), width, 80)
			assertAgentsInventoryFits(t, out, width, 80)
			if got := agentsInventoryCardWidth(out); got != wantOuter {
				t.Fatalf("rendered card width = %d, want %d", got, wantOuter)
			}
		})
	}
}

func TestMecatuiAgentInventoryBoundedViewport_Scenario1_UsesAvailableHeightWithOneGeometryPath(t *testing.T) {
	th := theme.New("aztec", theme.AztecPalette())
	for _, height := range []int{10, 24, 60} {
		t.Run(fmt.Sprint(height), func(t *testing.T) {
			st := agentsInvState{view: agentsInvPanel, agents: inventoryFixture(20), viewport: newAgentsInvViewport()}
			layout := newAgentsInvLayout(th, defaultHelpKeys(), 100, height)
			if !layout.normal {
				t.Fatalf("height %d unexpectedly compact", height)
			}
			out := renderAgentsInvOverlay(th, st, client.Capabilities{Agents: true}, defaultHelpKeys(), 100, height)
			assertAgentsInventoryFits(t, out, 100, height)
			rows := agentsInvRowLines(th, st.agents, layout.bodyWidth)
			bodyHeight, ok := layout.viewportHeight(len(rows))
			if !ok {
				t.Fatal("overflowing inventory unexpectedly has no viewport geometry")
			}
			if got := st.viewport.Height(); got != bodyHeight {
				t.Fatalf("viewport height = %d, geometry body height = %d", got, bodyHeight)
			}
			st.viewport.Move(bounded.End, len(rows))
			want := max(0, len(rows)-bodyHeight)
			if got := st.viewport.Offset(); got != want {
				t.Fatalf("navigation clamp = %d, want %d from render budget", got, want)
			}
		})
	}
}

func TestMecatuiAgentInventoryBoundedViewport_Scenario1_AllStatesFitOfferedGeometry(t *testing.T) {
	th := theme.New("aztec", theme.AztecPalette())
	states := []struct {
		name string
		st   agentsInvState
		caps client.Capabilities
	}{
		{"populated", agentsInvState{view: agentsInvPanel, agents: inventoryFixture(8), viewport: newAgentsInvViewport()}, client.Capabilities{Agents: true}},
		{"loading", agentsInvState{view: agentsInvPanel, loading: true}, client.Capabilities{Agents: true}},
		{"error", agentsInvState{view: agentsInvPanel, err: errors.New(strings.Repeat("very long unsafe error ", 20) + "\x1b[31m")}, client.Capabilities{Agents: true}},
		{"enabled-empty", agentsInvState{view: agentsInvPanel}, client.Capabilities{Agents: true}},
		{"disabled", agentsInvState{view: agentsInvPanel}, client.Capabilities{}},
	}
	for _, size := range [][2]int{{18, 5}, {42, 12}, {100, 30}} {
		for _, tc := range states {
			t.Run(fmt.Sprintf("%s-%dx%d", tc.name, size[0], size[1]), func(t *testing.T) {
				out := renderAgentsInvOverlay(th, tc.st, tc.caps, defaultHelpKeys(), size[0], size[1])
				assertAgentsInventoryFits(t, out, size[0], size[1])
			})
		}
	}
}

func TestMecatuiAgentInventoryBoundedViewport_Scenario1_CompactFallbackAndNonpositiveGeometry(t *testing.T) {
	th := theme.New("aztec", theme.AztecPalette())
	for _, size := range [][2]int{{1, 1}, {10, 3}, {40, 4}} {
		st := agentsInvState{view: agentsInvPanel, agents: inventoryFixture(4)}
		out := renderAgentsInvOverlay(th, st, client.Capabilities{Agents: true}, defaultHelpKeys(), size[0], size[1])
		assertAgentsInventoryFits(t, out, size[0], size[1])
		if strings.Contains(ansi.Strip(out), "Agent definitions") || !strings.Contains(ansi.Strip(out), "esc") && size[0] >= 3 {
			t.Fatalf("compact %v was not close-only: %q", size, ansi.Strip(out))
		}
	}
	m := newAgentsInvModel(t, sampleAgents(), client.Capabilities{Agents: true})
	opened, cmd := m.openAgentsInv()
	m = feedCmd(t, opened.(Model), cmd)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 10, Height: 3})
	m = updated.(Model)
	_ = m.View()
	if m.agentsInv.viewport != nil {
		t.Fatalf("compact model retained browsable viewport: %#v", m.agentsInv.viewport)
	}
	updated, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	if updated.(Model).agentsInv.viewport != nil {
		t.Fatal("compact input constructed a browsable viewport")
	}

	for _, size := range [][2]int{{0, 10}, {-1, 10}, {10, 0}, {10, -1}} {
		if got := renderAgentsInvOverlay(th, agentsInvState{view: agentsInvPanel}, client.Capabilities{}, defaultHelpKeys(), size[0], size[1]); got != "" {
			t.Fatalf("geometry %v rendered %q, want empty", size, ansi.Strip(got))
		}
	}
}

func TestMecatuiAgentInventoryBoundedViewport_Scenario2_ClampsPhysicalBrowsing(t *testing.T) {
	th := theme.New("aztec", theme.AztecPalette())
	st := agentsInvState{view: agentsInvPanel, agents: inventoryFixture(12), viewport: newAgentsInvViewport()}
	renderAgentsInvOverlay(th, st, client.Capabilities{Agents: true}, defaultHelpKeys(), 50, 15)
	layout := newAgentsInvLayout(th, defaultHelpKeys(), 50, 15)
	rows := agentsInvRowLines(th, st.agents, layout.bodyWidth)
	seen := make(map[string]bool, len(rows))
	for step := 0; step <= len(rows); step++ {
		for _, row := range st.viewport.View(rows).Rows {
			seen[ansi.Strip(row)] = true
		}
		st.viewport.Move(bounded.LineDown, len(rows))
	}
	for i, row := range rows {
		if !seen[ansi.Strip(row)] {
			t.Fatalf("physical row %d was never reachable: %q", i, ansi.Strip(row))
		}
	}
	st.viewport.Move(bounded.End, len(rows))
	if st.viewport.Offset() == 0 {
		t.Fatal("long wrapped inventory did not reach a later physical line")
	}
	bottom := renderAgentsInvOverlay(th, st, client.Capabilities{Agents: true}, defaultHelpKeys(), 50, 15)
	if !strings.Contains(ansi.Strip(bottom), "description-11") {
		t.Fatalf("last definition's final physical rows are unreachable at end:\n%s", ansi.Strip(bottom))
	}
	st.viewport.Move(bounded.LineDown, len(rows))
	if got, want := st.viewport.Offset(), max(0, len(rows)-st.viewport.Height()); got != want {
		t.Fatalf("end clamp = %d, want %d", got, want)
	}
	renderAgentsInvOverlay(th, st, client.Capabilities{Agents: true}, defaultHelpKeys(), 50, 30)
	if st.viewport.Offset() > max(0, len(rows)-st.viewport.Height()) {
		t.Fatalf("resize left blank reachable page at offset %d", st.viewport.Offset())
	}
	st.agents = inventoryFixture(1)
	short := agentsInvRowLines(th, st.agents, newAgentsInvLayout(th, defaultHelpKeys(), 50, 30).bodyWidth)
	renderAgentsInvOverlay(th, st, client.Capabilities{Agents: true}, defaultHelpKeys(), 50, 30)
	if st.viewport.Offset() != 0 || len(short) == 0 {
		t.Fatalf("content replacement did not clamp to nonblank first page: offset=%d rows=%d", st.viewport.Offset(), len(short))
	}
}

func TestMecatuiAgentInventoryBoundedViewport_Scenario2_NonInventoryStatesClearBrowsing(t *testing.T) {
	th := theme.New("aztec", theme.AztecPalette())
	stale := agentsInvState{view: agentsInvPanel, agents: []client.Agent{{Name: "stale-agent"}}, viewport: agentsTestViewport(1)}
	base := newAgentsInvModel(t, sampleAgents(), client.Capabilities{Agents: true})

	loadingModel := base
	loadingModel.agentsInv = stale
	opened, _ := loadingModel.openAgentsInv()
	loadingModel = opened.(Model)

	errorModel := base
	errorModel.agentsInv = stale
	errorResult, _ := errorModel.updateAgentsInvMsg(agentsInvResultMsg{generation: errorModel.agentsInv.generation, result: client.AgentsMsg{Err: errors.New("boom")}})
	errorModel = errorResult.(Model)

	emptyModel := base
	emptyModel.agentsInv = stale
	emptyResult, _ := emptyModel.updateAgentsInvMsg(agentsInvResultMsg{generation: emptyModel.agentsInv.generation, result: client.AgentsMsg{}})
	emptyModel = emptyResult.(Model)

	cases := []struct {
		name string
		st   agentsInvState
		caps client.Capabilities
		want string
	}{
		{"loading", loadingModel.agentsInv, client.Capabilities{Agents: true}, "loading…"},
		{"error", errorModel.agentsInv, client.Capabilities{Agents: true}, "list agents: boom"},
		{"empty", emptyModel.agentsInv, client.Capabilities{Agents: true}, "no agent definitions resolved"},
		{"disabled", emptyModel.agentsInv, client.Capabilities{}, "not enabled"},
	}
	for _, tc := range cases {
		out := renderAgentsInvOverlay(th, tc.st, tc.caps, defaultHelpKeys(), 80, 20)
		if tc.st.viewport != nil || len(tc.st.agents) != 0 {
			t.Errorf("%s state retained stale browsing: viewport=%v agents=%#v", tc.name, tc.st.viewport, tc.st.agents)
		}
		plain := ansi.Strip(out)
		if strings.Contains(plain, "stale-agent") {
			t.Errorf("%s state rendered planted stale inventory:\n%s", tc.name, plain)
		}
		if !strings.Contains(plain, tc.want) {
			t.Errorf("%s state lost distinct copy %q:\n%s", tc.name, tc.want, plain)
		}
	}
}

func TestMecatuiAgentInventoryBoundedViewport_Scenario2_PreservesSafeRowPresentation(t *testing.T) {
	th := theme.New("aztec", theme.AztecPalette())
	unsafe := "\x1b[31munsafe-long-value-" + strings.Repeat("x", 40) + "\x1b[0m"
	mk := func(color string) agentsInvState {
		return agentsInvState{view: agentsInvPanel, viewport: newAgentsInvViewport(), agents: []client.Agent{{
			Name: unsafe, Description: unsafe, Model: unsafe, PermissionMode: unsafe, Tools: []string{unsafe}, Color: color,
		}}}
	}
	plain, tinted := mk(""), mk("cyan")
	plainOut := renderAgentsInvOverlay(th, plain, client.Capabilities{Agents: true}, defaultHelpKeys(), 40, 12)
	tintedOut := renderAgentsInvOverlay(th, tinted, client.Capabilities{Agents: true}, defaultHelpKeys(), 40, 12)
	if ansi.Strip(plainOut) != ansi.Strip(tintedOut) {
		t.Fatal("color hint changed layout-visible content")
	}
	layout := newAgentsInvLayout(th, defaultHelpKeys(), 40, 12)
	rows := agentsInvRowLines(th, tinted.agents, layout.bodyWidth)
	joinedRows := strings.Join(rows, "\n")
	if strings.Contains(joinedRows, "\x1b[31munsafe") {
		t.Fatalf("server escape survived sanitization before styling: %q", joinedRows)
	}
	if !strings.Contains(ansi.Strip(joinedRows), "[31munsafe") {
		t.Fatalf("sanitized server text was not preserved as inert content: %q", ansi.Strip(joinedRows))
	}
	for i, row := range rows {
		if ansi.StringWidth(ansi.Strip(row)) > layout.bodyWidth {
			t.Fatalf("physical row %d exceeds body width: %q", i, ansi.Strip(row))
		}
	}
	tinted.viewport.Move(bounded.LineDown, len(rows))
	page := renderAgentsInvOverlay(th, tinted, client.Capabilities{Agents: true}, defaultHelpKeys(), 40, 12)
	for i, row := range strings.Split(page, "\n") {
		if strings.Contains(row, "\x1b[") && !strings.Contains(row, "\x1b[0m") && !strings.Contains(row, "\x1b[m") {
			t.Fatalf("rendered row %d leaks ANSI styling across boundary: %q", i, row)
		}
	}
}
