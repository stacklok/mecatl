package ui

// Tests for FEATURE 1: the "files changed this session" summary — a pure-ui
// feature derived from observed tool.call events (no proto/server change).

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/theme"
)

// TestMutatedPath gates the changed-files accumulation to file-mutating tools
// (Edit/Write, keyed by "path") and rejects read-only / malformed calls.
func TestMutatedPath(t *testing.T) {
	cases := []struct {
		name, tool, args, want string
		ok                     bool
	}{
		{"edit", "Edit", `{"path":"a.go","old_string":"x","new_string":"y"}`, "a.go", true},
		{"write", "Write", `{"path":"b/c.txt","content":"hi"}`, "b/c.txt", true},
		{"read is not mutating", "Read", `{"path":"a.go"}`, "", false},
		{"grep is not mutating", "Grep", `{"path":"."}`, "", false},
		{"bash is not mutating", "Bash", `{"command":"rm x"}`, "", false},
		{"edit malformed args", "Edit", "not json", "", false},
		{"edit empty path", "Edit", `{"path":"","old_string":"x","new_string":"y"}`, "", false},
		{"write no path", "Write", `{"content":"x"}`, "", false},
		{"unknown tool", "Frobnicate", `{"path":"x"}`, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := mutatedPath(tc.tool, tc.args)
			if ok != tc.ok || got != tc.want {
				t.Errorf("mutatedPath(%q,%q) = (%q,%v), want (%q,%v)", tc.tool, tc.args, got, ok, tc.want, tc.ok)
			}
		})
	}
}

// TestRecordFileChangeDedupesAndOrders asserts the tracker preserves first-seen
// order and ignores duplicates (and empty paths).
func TestRecordFileChangeDedupesAndOrders(t *testing.T) {
	var m Model
	m.conv.recordFileChange("a.go")
	m.conv.recordFileChange("b.go")
	m.conv.recordFileChange("a.go") // dup
	m.conv.recordFileChange("")     // ignored
	m.conv.recordFileChange("c.go")
	m.conv.recordFileChange("b.go") // dup

	want := []string{"a.go", "b.go", "c.go"}
	if len(m.conv.filesChanged) != len(want) {
		t.Fatalf("filesChanged = %v, want %v", m.conv.filesChanged, want)
	}
	for i := range want {
		if m.conv.filesChanged[i] != want[i] {
			t.Fatalf("filesChanged = %v, want %v", m.conv.filesChanged, want)
		}
	}
}

// TestChangedFilesAccumulatesFromToolCalls drives ToolCallMsgs through Update and
// asserts only Edit/Write contribute (deduped), and that a read-only call does
// not pollute the set.
func TestChangedFilesAccumulatesFromToolCalls(t *testing.T) {
	m, _, _ := newTestModel(t, theme.New("aztec", theme.AztecPalette()))
	m.phase = phaseRunning
	m = applyAll(m,
		client.ToolCallMsg{ID: "1", Name: "Read", Args: `{"path":"only-read.go"}`},
		client.ToolCallMsg{ID: "2", Name: "Edit", Args: `{"path":"edited.go","old_string":"a","new_string":"b"}`},
		client.ToolCallMsg{ID: "3", Name: "Write", Args: `{"path":"made.go","content":"x"}`},
		client.ToolCallMsg{ID: "4", Name: "Edit", Args: `{"path":"edited.go","old_string":"b","new_string":"c"}`}, // dup path
	)
	want := []string{"edited.go", "made.go"}
	if strings.Join(m.conv.filesChanged, ",") != strings.Join(want, ",") {
		t.Errorf("filesChanged = %v, want %v", m.conv.filesChanged, want)
	}
}

// TestChangedFilesIndicator covers the muted "✎ N files" header indicator.
func TestChangedFilesIndicator(t *testing.T) {
	var m Model
	if got := m.changedFilesIndicator(); got != "" {
		t.Errorf("empty session indicator = %q, want empty", got)
	}
	m.conv.recordFileChange("a.go")
	if got := m.changedFilesIndicator(); got != "✎ 1 file" {
		t.Errorf("indicator = %q, want %q", got, "✎ 1 file")
	}
	m.conv.recordFileChange("b.go")
	if got := m.changedFilesIndicator(); got != "✎ 2 files" {
		t.Errorf("indicator = %q, want %q", got, "✎ 2 files")
	}
}

// TestRenderChangedFiles covers the ctrl+t expansion list.
func TestRenderChangedFiles(t *testing.T) {
	r := newTestRenderer()
	if out := r.renderChangedFiles(nil); out != "" {
		t.Errorf("empty list should render nothing, got %q", out)
	}
	out := stripANSIstr(r.renderChangedFiles([]string{"a.go", "dir/b.txt"}))
	if !strings.Contains(out, "✎ 2 files changed this session") {
		t.Errorf("missing header, got %q", out)
	}
	if !strings.Contains(out, "a.go") || !strings.Contains(out, "dir/b.txt") {
		t.Errorf("missing paths, got %q", out)
	}
}

func TestStatusLineHeaderReservationOnlyAddsGapForSystemLane(t *testing.T) {
	m := Model{width: 80, stuck: true}
	if got, want := m.statusLineGeometry().headerAvailable, 78; got != want {
		t.Fatalf("header availability without a right lane = %d, want %d", got, want)
	}

	m.conv.filesChanged = make([]string, 100)
	if tail := m.changedFilesIndicator(); tail != "✎ 100 files" {
		t.Fatalf("100-file indicator = %q", tail)
	}
	withLane := m.statusLineGeometry().headerAvailable
	if got, want := withLane, 80-2-lipgloss.Width(m.changedFilesIndicator())-headerGapPad; got != want {
		t.Fatalf("header availability with changed-files lane = %d, want %d", got, want)
	}

	m.conv.filesChanged = make([]string, 10_000)
	if got, want := m.changedFilesIndicator(), "✎ 999+ files"; got != want {
		t.Fatalf("changed-files indicator must remain bounded: %q, want %q", got, want)
	}
}

// TestChangedFilesHeaderShowsIndicator drives a full render and asserts the
// header carries the indicator once an Edit is observed, and the ctrl+t-expanded
// view folds in the path list.
func TestChangedFilesHeaderShowsIndicator(t *testing.T) {
	m, _, _ := newTestModel(t, theme.New("aztec", theme.AztecPalette()))
	m = applyAll(m, tea.WindowSizeMsg{Width: 100, Height: 30}, client.SessionReadyMsg{SessionID: "sess-test-0001"})
	m.phase = phaseRunning
	m = applyAll(m, client.ToolCallMsg{ID: "1", Name: "Write", Args: `{"path":"out.txt","content":"hi"}`})

	header := stripANSIstr(m.renderHeader())
	if !strings.Contains(header, "✎ 1 file") {
		t.Errorf("header missing changed-files indicator: %q", header)
	}

	// Collapsed body must NOT list the path; expanded (ctrl+t) must.
	collapsed := stripANSIstr(m.vp.View())
	if strings.Contains(collapsed, "changed this session") {
		t.Errorf("collapsed view should not show the changed-files list")
	}
	m.expandTools = true
	m.refreshView()
	expanded := stripANSIstr(m.vp.View())
	if !strings.Contains(expanded, "out.txt") || !strings.Contains(expanded, "changed this session") {
		t.Errorf("expanded view should fold in the changed-files list, got %q", expanded)
	}
}
