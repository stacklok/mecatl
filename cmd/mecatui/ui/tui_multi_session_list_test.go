package ui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/ui/internal/scrollback"
)

// windowListFixture opens four sessions, one per status, with titles.
func windowListFixture(t *testing.T) (*windowDriver, map[string]int) {
	t.Helper()
	d := readyWindow(t, newWindowConv(), nil)
	keys := map[string]int{"idle": d.w.activeKey}
	keys["running"] = d.addCreated()
	keys["approval"] = d.addCreated()
	keys["failed"] = d.addCreated()
	d.mutate(keys["idle"], func(m *Model) { m.sessionTitle = "Check disk" })
	d.mutate(keys["running"], func(m *Model) { m.sessionTitle = "Fix module"; m.phase = phaseRunning })
	d.mutate(keys["approval"], func(m *Model) { m.sessionTitle = "Move files"; m.phase = phaseAwaitingApproval })
	d.mutate(keys["failed"], func(m *Model) { m.sessionTitle = "Ban check"; m.lastRunFailed = true })
	return d, keys
}

func visibleKeys(w window) []int {
	var out []int
	for _, row := range w.visibleRows() {
		out = append(out, row.key)
	}
	return out
}

func TestTUIMultiSession_ListFilterTabs(t *testing.T) {
	d, keys := windowListFixture(t)
	d.press(tea.KeyPressMsg{Code: tea.KeyLeft})

	view := windowView(d)
	for _, tab := range []string{"All 4", "needs approval 1", "running 1", "idle 1", "failed 1", "tab/shift+tab filter"} {
		if !strings.Contains(view, tab) {
			t.Errorf("filter tabs are missing %q:\n%s", tab, view)
		}
	}

	// Select the approval row, then tab to the needs-approval filter: the
	// selection stays on it and only it is shown.
	for range 4 {
		d.press(tea.KeyPressMsg{Code: tea.KeyUp})
	}
	for range 4 {
		if d.w.visibleRows()[d.w.list.cursor].key == keys["approval"] {
			break
		}
		d.press(tea.KeyPressMsg{Code: tea.KeyDown})
	}
	if d.w.visibleRows()[d.w.list.cursor].key != keys["approval"] {
		t.Fatal("could not select the approval row")
	}
	d.press(tea.KeyPressMsg{Code: tea.KeyTab})
	if d.w.list.filter != windowStatusNeedsApproval {
		t.Fatalf("tab filter = %q, want %q", d.w.list.filter, windowStatusNeedsApproval)
	}
	if got := visibleKeys(d.w); len(got) != 1 || got[0] != keys["approval"] {
		t.Fatalf("needs-approval rows = %v, want [%d]", got, keys["approval"])
	}
	if view := windowView(d); strings.Contains(view, "Check disk") || !strings.Contains(view, "Move files") {
		t.Fatalf("the filtered table must show only the approval session:\n%s", view)
	}

	// shift+tab goes back to All, then wraps to the last filter.
	d.press(tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift})
	if d.w.list.filter != "" || len(d.w.visibleRows()) != 4 {
		t.Fatalf("shift+tab must return to All, got filter %q with %d rows", d.w.list.filter, len(d.w.visibleRows()))
	}
	if row := d.w.visibleRows()[d.w.list.cursor]; row.key != keys["approval"] {
		t.Fatalf("returning to All must keep the selection, got row %d", row.key)
	}
	d.press(tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift})
	if d.w.list.filter != windowStatusFailed {
		t.Fatalf("shift+tab from All must wrap to %q, got %q", windowStatusFailed, d.w.list.filter)
	}

	// enter switches to the filtered row, not the row at that session index.
	d.press(tea.KeyPressMsg{Code: tea.KeyEnter})
	if d.w.activeKey != keys["failed"] {
		t.Fatalf("enter in the failed filter switched to %d, want %d", d.w.activeKey, keys["failed"])
	}
}

func TestTUIMultiSession_ListEmptyFilter(t *testing.T) {
	d := readyWindow(t, newWindowConv(), nil)
	d.press(tea.KeyPressMsg{Code: tea.KeyLeft})
	d.press(tea.KeyPressMsg{Code: tea.KeyTab}) // needs approval: no rows
	if view := windowView(d); !strings.Contains(view, "no needs approval sessions") {
		t.Fatalf("an empty filter must say so:\n%s", view)
	}
	d.press(keyText('d'), tea.KeyPressMsg{Code: tea.KeyEnter})
	if d.w.deleteConfirm != nil || d.w.list == nil {
		t.Fatal("d and enter on an empty filter must do nothing")
	}
}

func TestTUIMultiSession_ListUpdatedTracksActivity(t *testing.T) {
	d := readyWindow(t, newWindowConv(), nil)
	t0 := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	now := t0
	d.w.now = func() time.Time { return now }
	key := d.w.activeKey
	i := d.w.index(key)
	d.w.sessions[i].updatedAt = t0

	// A redraw with no status or transcript change keeps the time.
	now = t0.Add(5 * time.Minute)
	d.w = d.w.withModel(i, d.w.sessions[i].model)
	if got := d.w.sessions[i].updatedAt; !got.Equal(t0) {
		t.Fatalf("an unchanged session moved its Updated time to %v", got)
	}
	d.press(tea.KeyPressMsg{Code: tea.KeyLeft})
	if view := windowView(d); !strings.Contains(view, "5m ago") {
		t.Fatalf("the Updated column must show the age:\n%s", view)
	}

	// A new transcript card moves it.
	m := d.w.sessions[i].model
	m.conv.scrollback.Messages().AddAssistant(scrollback.AssistantInput{Text: "done"})
	d.w = d.w.withModel(i, m)
	if got := d.w.sessions[i].updatedAt; !got.Equal(now) {
		t.Fatalf("a new transcript card must move Updated to %v, got %v", now, got)
	}
	if view := windowView(d); !strings.Contains(view, "just now") {
		t.Fatalf("a fresh change must read just now:\n%s", view)
	}

	for _, tc := range []struct {
		age  time.Duration
		want string
	}{{30 * time.Second, "just now"}, {11 * time.Minute, "11m ago"}, {2 * time.Hour, "2h ago"}, {26 * 24 * time.Hour, "26d ago"}} {
		if got := windowUpdated(t0, t0.Add(tc.age)); got != tc.want {
			t.Errorf("windowUpdated(%v) = %q, want %q", tc.age, got, tc.want)
		}
	}
	if got := windowUpdated(time.Time{}, t0); got != "" {
		t.Errorf("an unknown time must render empty, got %q", got)
	}
}

func TestTUIMultiSession_ListDetails(t *testing.T) {
	d := readyWindow(t, newWindowConv(), nil)
	d.mutate(d.w.activeKey, func(m *Model) {
		m.sessionTitle = "Check main disk storage"
		m.activePlacement = client.Placement{Kind: "local", Label: "Local workspace"}
		m.resolvedSessionModel = client.ResolvedModel{ProviderID: "p", ModelID: "gpt-test"}
		m.usage = client.Usage{InputTokens: 636_000, OutputTokens: 7_790}
		m.conv.scrollback.Messages().AddUser(scrollback.UserInput{Text: "why is the\ndisk full"})
		m.conv.scrollback.Messages().AddAssistant(scrollback.AssistantInput{Text: "Removed all five entries from fstab."})
	})
	d.press(tea.KeyPressMsg{Code: tea.KeyLeft})
	view := windowView(d)
	for _, want := range []string{
		"project: Local workspace", "Session details", "Check main disk storage",
		"Last message", "Removed all five entries from fstab.",
		"Project", "Model: gpt-test", "Tokens: 636K in · 7.8K out",
		"Prompt", "why is the disk full",
	} {
		if !strings.Contains(view, want) {
			t.Errorf("session details are missing %q:\n%s", want, view)
		}
	}

	// A narrow window stacks the details under the table.
	d.send(tea.WindowSizeMsg{Width: 70, Height: 32})
	if view := windowView(d); !strings.Contains(view, "Session details") || !strings.Contains(view, "Model: gpt-test") {
		t.Fatalf("a narrow window must still show the details:\n%s", view)
	}
}

func TestTUIMultiSession_ListRowKeepsBranchWhenTight(t *testing.T) {
	row := windowRow{label: "A long session title that will not fit", branch: "mecatl/brave-otter"}
	got := windowRowName(row, true, 34)
	if !strings.HasSuffix(got, "(mecatl/brave-otter)") {
		t.Fatalf("a tight row must keep its branch, got %q", got)
	}
	if got := windowRowName(windowRow{label: "Plan"}, true, 40); got != "Plan · current" {
		t.Fatalf("a roomy row keeps the current marker, got %q", got)
	}
}

func TestTUIMultiSession_FooterShowsSessionsKey(t *testing.T) {
	d := readyWindow(t, newWindowConv(), nil)
	if view := windowView(d); !strings.Contains(view, "← sessions · ? help") {
		t.Fatalf("the footer must lead with the session list key:\n%s", view)
	}
	rebound := readyWindow(t, newWindowConv(), func(deps *Deps) { deps.KeyOverrides = map[string][]string{"Sessions": {"ctrl+o"}} })
	if view := windowView(rebound); !strings.Contains(view, "ctrl+o sessions · ? help") {
		t.Fatalf("a rebound sessions key must show in the footer:\n%s", view)
	}
}
