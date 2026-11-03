package ui

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/internal/terminaltext"
)

func openTestWorkspace(t *testing.T, m Model) (Model, *workspaceState, tea.Cmd) {
	t.Helper()
	mm, cmd := m.openWorkspace()
	m = mm.(Model)
	s, ok := m.modal.(*workspaceState)
	if !ok {
		t.Fatalf("workspace did not open: %T", m.modal)
	}
	if !s.available {
		return m, s, nil
	}
	batch, ok := cmd().(tea.BatchMsg)
	if !ok || len(batch) != 2 {
		t.Fatal("expected exactly one inspection and filter blink")
	}
	return m, s, batch[0]
}

func TestWorkspaceFrozenObservationsAndRootStream(t *testing.T) {
	m, _ := newQueueModel(t)
	m.phase = phaseRunning
	calls := 0
	sample := WorkspaceSnapshot{Directory: "/repo-first", CapturedAt: time.Unix(100, 0), Git: &WorkspaceGitSnapshot{
		Root: "/repo-first", Branch: "branch-first", Files: []WorkspaceGitFile{{Path: "file-first", Untracked: true}},
	}}
	m.deps.WorkspaceSnapshot = func(_ context.Context, id string) (WorkspaceSnapshot, error) {
		calls++
		if id != m.sessionID {
			t.Fatalf("inspection session = %q", id)
		}
		return sample, nil
	}
	m = applyAll(m, client.ToolCallMsg{ID: "a", Name: "Edit", Args: `{"path":"A"}`})
	m.prompt.Rewrite("unsent draft")
	m.queued = []string{"next"}
	steer := &steerState{}
	m.steer = steer
	m.activePlacement = client.Placement{Kind: "worktree", Label: "captured placement"}
	m, s, load := openTestWorkspace(t, m)
	if m.prompt.Focused() || m.prompt.Value() != "unsent draft" || m.steer != steer || !reflect.DeepEqual(m.queued, []string{"next"}) || m.phase != phaseRunning {
		t.Fatal("opening changed draft, queue, steer or phase")
	}
	first := load()
	m.activePlacement.Label = "later placement"
	m = applyAll(m,
		client.ToolResultMsg{CallID: "a", IsError: true, Content: "Edit failed"},
		client.ToolCallMsg{ID: "b", Name: "Write", Args: `{"path":"B","content":"x"}`},
		client.ToolResultMsg{CallID: "b", IsError: true, Content: "Write failed"},
		client.AssistantDeltaMsg{Turn: 1, Text: "root still streaming"},
		renderTickMsg{},
	)
	if !s.loading || !reflect.DeepEqual(s.observed, []string{"A"}) || !reflect.DeepEqual(m.conv.testChangedFiles(), []string{"A", "B"}) {
		t.Fatalf("pending snapshot=%v root=%v loading=%v", s.observed, m.conv.testChangedFiles(), s.loading)
	}
	m = applyAll(m, first)
	before, _ := s.Render(120, 60)
	sample = WorkspaceSnapshot{Directory: "/repo-second", CapturedAt: time.Unix(200, 0), Git: &WorkspaceGitSnapshot{
		Root: "/repo-second", Branch: "branch-second", Files: []WorkspaceGitFile{{Path: "file-second", Untracked: true}},
	}}
	m = applyAll(m, client.AssistantDeltaMsg{Turn: 1, Text: "; still running"}, renderTickMsg{})
	if !reflect.DeepEqual(s.observed, []string{"A"}) || !reflect.DeepEqual(m.conv.testChangedFiles(), []string{"A", "B"}) {
		t.Fatalf("snapshot=%v root=%v", s.observed, m.conv.testChangedFiles())
	}
	body, _ := s.Render(120, 60)
	if body != before || calls != 1 {
		t.Fatalf("open inspector changed after backing sample/root events: calls=%d\n%s", calls, body)
	}
	for _, want := range []string{"/repo-first", "Git: branch-first", `?? "file-first"`, "captured placement", `"A"`, "Observed Edit/Write intentions", "Announced intent, not success or ownership", "Shell", "delegated", "resumed history", "Non-atomic"} {
		if !strings.Contains(stripANSIstr(body), want) {
			t.Fatalf("missing %q: %s", want, body)
		}
	}
	if strings.Contains(body, "later placement") || strings.Contains(body, `"B"`) || !strings.Contains(stripANSIstr(m.conversationContent()), "root still streaming") {
		t.Fatalf("surface live-synced or swallowed stream event: %q", stripANSIstr(m.conversationContent()))
	}
	m = applyAll(m, tea.KeyPressMsg{Code: 'x', Text: "x"}, tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.modal != nil || !m.prompt.Focused() || m.phase != phaseRunning || m.prompt.Value() != "unsent draft" {
		t.Fatal("Esc with nonempty filter must close without changing run or draft")
	}
	m, reopened, load := openTestWorkspace(t, m)
	m = applyAll(m, load())
	if !reflect.DeepEqual(reopened.observed, []string{"A", "B"}) || calls != 2 {
		t.Fatalf("reopen snapshot=%v calls=%d", reopened.observed, calls)
	}
	body, _ = reopened.Render(120, 60)
	for _, want := range []string{"/repo-second", "Git: branch-second", `?? "file-second"`, `"A"`, `"B"`, "Announced intent, not success or ownership"} {
		if !strings.Contains(stripANSIstr(body), want) {
			t.Fatalf("reopened inspector missing %q:\n%s", want, stripANSIstr(body))
		}
	}
	for _, stale := range []string{"/repo-first", "branch-first", "file-first"} {
		if strings.Contains(body, stale) {
			t.Fatalf("reopened inspector retained %q", stale)
		}
	}
	_ = m.View()
	_ = m.View()
	if calls != 2 {
		t.Fatal("render triggered inspection")
	}
}

func TestWorkspaceRequestLifetimeCorrelation(t *testing.T) {
	m, _ := newQueueModel(t)
	var contexts []context.Context
	m.deps.WorkspaceSnapshot = func(ctx context.Context, id string) (WorkspaceSnapshot, error) {
		contexts = append(contexts, ctx)
		return WorkspaceSnapshot{Directory: "/" + id}, nil
	}
	m, first, load := openTestWorkspace(t, m)
	late := load().(workspaceResultMsg)
	m = applyAll(m, tea.KeyPressMsg{Code: tea.KeyEscape})
	if !first.closed || contexts[0].Err() != context.Canceled {
		t.Fatal("close did not cancel request")
	}
	m = applyAll(m, late) // completion with no surface
	m, second, load := openTestWorkspace(t, m)
	m = applyAll(m, late)
	if !second.loading || second.snapshot.Directory != "" {
		t.Fatal("old owner completed new lifetime")
	}
	current := load().(workspaceResultMsg)
	wrongSession := current
	wrongSession.sourceID = "wrong-session"
	m = applyAll(m, wrongSession)
	if !second.loading {
		t.Fatal("wrong session accepted")
	}
	m = applyAll(m, current)
	if second.loading || second.snapshot.Directory == "" {
		t.Fatal("current completion dropped")
	}
	m = applyAll(m, tea.KeyPressMsg{Code: tea.KeyEscape})
	m, third, load := openTestWorkspace(t, m)
	late = load().(workspaceResultMsg)
	m = applyAll(m, client.SessionReadyMsg{SessionID: "replacement"}, late)
	if m.modal != nil || !third.closed || contexts[2].Err() != context.Canceled || m.sessionID != "replacement" {
		t.Fatal("session rebind retained inspector")
	}
}

func TestWorkspaceApprovalAndTeardownPriority(t *testing.T) {
	for _, event := range []struct {
		name string
		msg  tea.Msg
	}{
		{"approval", client.PermissionAskMsg{AskID: "sess-test-0001:1:write", Tool: "Write", Args: `{"path":"x"}`}},
		{"same-session-rebind", client.SessionReadyMsg{SessionID: "sess-test-0001"}},
		{"stream-teardown", client.StreamClosedMsg{}},
	} {
		t.Run(event.name, func(t *testing.T) {
			m, _ := newQueueModel(t)
			m.phase = phaseRunning
			var ctx context.Context
			m.deps.WorkspaceSnapshot = func(c context.Context, _ string) (WorkspaceSnapshot, error) {
				ctx = c
				return WorkspaceSnapshot{}, nil
			}
			m, s, load := openTestWorkspace(t, m)
			late := load()
			m = applyAll(m, event.msg, late)
			if !s.closed || ctx.Err() != context.Canceled || m.modal == s {
				t.Fatal("priority teardown retained inspector or request")
			}
			if event.name == "approval" {
				if m.phase != phaseAwaitingApproval || approvalSurfaceFor(&m) == nil {
					t.Fatal("approval did not replace inspector")
				}
				before := m.modal
				mm, cmd := m.openWorkspace()
				if mm.(Model).modal != before || cmd != nil {
					t.Fatal("workspace replaced approval")
				}
			}
		})
	}
}

func TestWorkspaceSurvivesRunCompletionAndQueue(t *testing.T) {
	for _, queued := range []bool{false, true} {
		for _, pending := range []bool{false, true} {
			t.Run(fmt.Sprintf("queue=%v/pending=%v", queued, pending), func(t *testing.T) {
				m, _ := newQueueModel(t, &fakeRecver{}, &fakeRecver{})
				m = startRunning(t, m, "initial")
				if queued {
					m = enqueue(t, m, "follow-up")
				}
				m.prompt.Rewrite("draft")
				var requestCtx context.Context
				m.deps.WorkspaceSnapshot = func(ctx context.Context, _ string) (WorkspaceSnapshot, error) {
					requestCtx = ctx
					return WorkspaceSnapshot{Directory: "/resolved"}, nil
				}
				m, s, load := openTestWorkspace(t, m)
				result := load()
				if !pending {
					m = applyAll(m, result)
				}
				generation := m.streamGen
				m = applyAll(m, streamMsg{gen: generation, msg: client.ResultMsg{Stop: "end_turn"}})
				if m.modal != s || s.closed || m.prompt.Focused() || m.streamGen <= generation || s.loading != pending {
					t.Fatal("completion lost snapshot/input ownership or terminal reduction")
				}
				if pending && requestCtx.Err() != nil {
					t.Fatal("run completion cancelled pending workspace request")
				}
				if queued {
					if m.phase != phaseRunning || len(m.queued) != 0 || !strings.Contains(m.conversationContent(), "follow-up") {
						t.Fatal("workspace blocked queued-run transition")
					}
				} else if m.phase != phaseIdle || m.prompt.Value() != "draft" {
					t.Fatal("completion changed draft or failed to settle idle")
				}
				m = applyAll(m, result)
				if s.loading || s.snapshot.Directory != "/resolved" {
					t.Fatal("pending result did not survive run completion")
				}
				m = applyAll(m, tea.KeyPressMsg{Code: tea.KeyEscape})
				if m.modal != nil || !m.prompt.Focused() {
					t.Fatal("close did not restore prompt focus")
				}
			})
		}
	}
}

func TestWorkspaceDoesNotReplaceOtherSurfaces(t *testing.T) {
	m, _ := newQueueModel(t)
	mm, _ := m.runHelp()
	m = mm.(Model)
	before := m.modal
	mm, cmd := m.openWorkspace()
	if mm.(Model).modal != before || cmd != nil {
		t.Fatal("workspace replaced another modal")
	}
	// The opt-in must not change the lifetime of existing surfaces.
	m.phase = phaseRunning
	m = applyAll(m, client.ResultMsg{Stop: "end_turn"})
	if m.modal != nil || !m.prompt.Focused() {
		t.Fatal("ordinary modal unexpectedly survived completion")
	}
}

func TestWorkspaceGitMarkersFilteringAndScope(t *testing.T) {
	m, _ := newQueueModel(t)
	m.conv.recordFileChange("./nested/../new name")
	m.conv.recordFileChange("../old name")
	m.conv.recordFileChange("/repo/unmatched")
	m.deps.WorkspaceSnapshot = func(context.Context, string) (WorkspaceSnapshot, error) {
		return WorkspaceSnapshot{
			Directory: "/repo/sub", CapturedAt: time.Unix(123, 0),
			Git: &WorkspaceGitSnapshot{Root: "/repo", Branch: "topic", Upstream: "origin/topic", Divergence: &WorkspaceGitDivergence{Ahead: 2, Behind: 3},
				Counts: WorkspaceGitCounts{Staged: 1, Unstaged: 2, Conflicted: 1, Untracked: 1},
				Files: []WorkspaceGitFile{
					{Path: "sub/new name", OriginalPath: "old name", Index: 'R', Worktree: 'M'},
					{Path: "conflict", Index: 'U', Worktree: 'U', Conflicted: true},
					{Path: "untracked", Untracked: true},
				}},
		}, nil
	}
	m, s, load := openTestWorkspace(t, m)
	m = applyAll(m, load())
	body, _ := s.Render(124, 70)
	plain := stripANSIstr(body)
	for _, want := range []string{`RM "sub/new name" ← "old name" [observed]`, "UU", "[conflicted]", `?? "untracked"`, "ahead 2", "behind 3", "counts overlap", "Submodule working trees not inspected", "Unmatched observed", `"/repo/unmatched"`, "Inspection captured:"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("missing %q:\n%s", want, plain)
		}
	}
	if !strings.Contains(body, m.deps.Theme.Style("toolArgs").Render("RM ")+m.deps.Theme.Style("viewport").Bold(true).Render(`"sub/new name" ← "old name"`)+m.deps.Theme.Style("toolArgs").Render(" [observed]")) {
		t.Fatal("rename paths must stand out from status and observation markers")
	}
	if strings.Contains(plain, `"../old name"`) || strings.Contains(plain, `"./nested/../new name"`) {
		t.Fatal("matched rename observations also listed as unmatched")
	}
	m = applyAll(m, tea.PasteMsg{Content: "new name"})
	body, _ = s.Render(124, 70)
	plain = stripANSIstr(body)
	if !strings.Contains(plain, `RM "sub/new name"`) || strings.Contains(plain, `UU "conflict"`) || strings.Contains(plain, `?? "untracked"`) || strings.Contains(plain, `"/repo/unmatched"`) {
		t.Fatalf("filter did not limit files:\n%s", plain)
	}
	m = applyAll(m, tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.modal != nil {
		t.Fatal("Esc must close rather than clear filter")
	}
}

func TestWorkspaceInspectorPresentationRows(t *testing.T) {
	m, _ := newQueueModel(t)
	m.activePlacement = client.Placement{Kind: "worktree", Label: "captured"}
	m.conv.recordFileChange("changed.go")
	m.conv.recordFileChange("intent-only.go")
	capturedAt, openedAt := time.Unix(123, 0), time.Unix(456, 0)
	m.deps.WorkspaceSnapshot = func(context.Context, string) (WorkspaceSnapshot, error) {
		return WorkspaceSnapshot{
			Directory: "/repo", CapturedAt: capturedAt,
			Git: &WorkspaceGitSnapshot{Root: "/repo", Branch: "topic", Files: []WorkspaceGitFile{
				{Path: "changed.go", Index: 'M', Worktree: ' '},
				{Path: "conflict.go", Index: 'U', Worktree: 'U', Conflicted: true},
			}},
		}, nil
	}
	m, s, load := openTestWorkspace(t, m)
	s.openedAt = openedAt
	m = applyAll(m, load())
	body, _ := s.Render(124, 70)
	plain := stripANSIstr(body)
	for _, want := range []string{"Session context", "Git state", "Git files", "Observed Edit/Write intentions", "Unmatched observed paths", `M  "changed.go" [observed]`, `UU "conflict.go" [conflicted]`, `"intent-only.go"`} {
		if !strings.Contains(plain, want) {
			t.Fatalf("missing %q:\n%s", want, plain)
		}
	}
	gitTime := strings.Index(plain, "Inspection captured: "+capturedAt.Format(time.RFC3339))
	observedTime := strings.Index(plain, "Captured: "+openedAt.Format(time.RFC3339))
	if gitTime < 0 || observedTime < 0 ||
		strings.Index(plain, "Git state") >= strings.Index(plain, "Git files") ||
		strings.Index(plain, "Git files") >= strings.Index(plain, "Observed Edit/Write intentions") ||
		gitTime <= strings.Index(plain, "Git state") || gitTime >= strings.Index(plain, "Git files") ||
		observedTime <= strings.Index(plain, "Observed Edit/Write intentions") {
		t.Fatalf("sections are not grouped by provenance or retained their distinct timestamps:\n%s", plain)
	}
	for _, styled := range []string{
		m.deps.Theme.Style("toolName").Render("Git state"),
		m.deps.Theme.Style("toolName").Render("Git: ") + m.deps.Theme.Style("viewport").Render("topic"),
		m.deps.Theme.Style("warning").Render("Non-atomic snapshot; may already be stale."),
		m.deps.Theme.Style("viewport").Bold(true).Render(`"changed.go"`),
		m.deps.Theme.Style("viewport").Bold(true).Render(`"conflict.go"`),
		m.deps.Theme.Style("viewport").Bold(true).Render(`"intent-only.go"`),
	} {
		if !strings.Contains(body, styled) {
			t.Fatalf("missing semantic style %q in %q", styled, body)
		}
	}

	m, _ = newQueueModel(t)
	m.deps.ConnectionMode = "connect"
	m.deps.WorkspaceSnapshot = func(context.Context, string) (WorkspaceSnapshot, error) {
		return WorkspaceSnapshot{Directory: "/remote"}, errors.New("failed\x1b[2J\tinspection")
	}
	m, s, load = openTestWorkspace(t, m)
	m = applyAll(m, load())
	body, _ = s.Render(30, 24)
	plain = stripANSIstr(body)
	if !strings.Contains(plain, "Session context") || !strings.Contains(plain, "Connection: remote") || strings.Contains(body, "\x1b[2J") || strings.Contains(body, "\t") {
		t.Fatalf("narrow remote rendering lost identity or safety: %q", body)
	}
	errorText := terminaltext.ExpandTabs(terminaltext.Sanitize("Local inspection unavailable: failed\x1b[2J\tinspection"), terminaltext.DefaultTabWidth)
	if !strings.Contains(strings.Join(s.styledRows(s.content(), 100), "\n"), m.deps.Theme.Style("errorText").Render(errorText)) {
		t.Fatal("inspection error did not retain error styling")
	}
}

func TestWorkspaceLexicalJoin(t *testing.T) {
	for _, tc := range []struct {
		dir, root, observed, path string
		want                      bool
	}{
		{"/repo/sub", "/repo", "../a", "a", true},
		{"/repo/sub", "/repo", "/repo/a", "a", true},
		{"/repo/sub", "/repo", "a", "a", false},
		{"/repo/sub", "/repo", "../../outside", "outside", false},
		{"/repo", "/repo", "symlink/../a", "a", true},
		{"", "", "remote/../a", "a", false},
		{"relative", "/repo", "a", "a", false},
	} {
		if got := workspacePathMatches(filepath.FromSlash(tc.dir), filepath.FromSlash(tc.root), tc.observed, tc.path); got != tc.want {
			t.Errorf("join %+v = %v", tc, got)
		}
	}
}

func TestWorkspaceUnavailableNonRepoAndOpaqueRemote(t *testing.T) {
	for _, tc := range []struct {
		name    string
		inspect WorkspaceSnapshotFunc
		want    string
	}{
		{"unavailable", nil, "Local inspection unavailable"},
		{"nonrepo", func(context.Context, string) (WorkspaceSnapshot, error) {
			return WorkspaceSnapshot{Directory: "/verified"}, nil
		}, "Confirmed non-repository"},
		{"error", func(context.Context, string) (WorkspaceSnapshot, error) {
			return WorkspaceSnapshot{Directory: "/verified", Git: &WorkspaceGitSnapshot{Branch: "must-not-use", Counts: WorkspaceGitCounts{Staged: 999}}}, errors.New("failed\x1b[2J\tinspection")
		}, "Local inspection unavailable:"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, _ := newQueueModel(t)
			m.deps.WorkspaceSnapshot = tc.inspect
			m.deps.ConnectionMode, m.deps.Server = "connect", "https://do-not-display.invalid/private"
			m.deps.Workspace = "/not-authoritative"
			m.activePlacement = client.Placement{Kind: "remote", Label: "known\x1b[2J\tlabel"}
			m.conv.recordFileChange("opaque/../path")
			m.conv.recordFileChange("path")
			m, s, load := openTestWorkspace(t, m)
			if load != nil {
				m = applyAll(m, load())
			}
			body, _ := s.Render(120, 60)
			plain := stripANSIstr(body)
			for _, want := range []string{tc.want, "Connection: remote", `"opaque/../path"`, `"path"`} {
				if !strings.Contains(plain, want) {
					t.Fatalf("missing %q:\n%s", want, plain)
				}
			}
			for _, bad := range []string{"\x1b[2J", "\t", "must-not-use", "999", "not-authoritative", "do-not-display"} {
				if strings.Contains(body, bad) {
					t.Fatalf("unsafe/unauthorized display %q", bad)
				}
			}
			if !strings.Contains(plain, "Captured observed paths") || strings.Contains(plain, "Unmatched observed paths") || strings.Contains(plain, "May be clean, committed") {
				t.Fatal("uninspected observations were presented as compared with Git")
			}
			if tc.name != "nonrepo" && strings.Contains(plain, "Confirmed non-repository") {
				t.Fatal("unavailable inspection claimed confirmed non-repository")
			}
		})
	}
}

func TestWorkspaceCompactResizeInputAndScrollIsolation(t *testing.T) {
	m := scrollModel(t)
	m = applyAll(m, tea.KeyPressMsg{Code: tea.KeyPgUp})
	offset := m.vp.YOffset()
	m.prompt.Rewrite("untouched")
	for i := range 100 {
		m.conv.recordFileChange(fmt.Sprintf("file-%03d\t\x1b[2J\n中", i))
	}
	m, s, _ := openTestWorkspace(t, m)
	m.phase = phaseRunning
	m = applyAll(m, client.AssistantDeltaMsg{Turn: 1, Text: "stream under modal"}, renderTickMsg{})
	if m.vp.YOffset() != offset || m.conversationView.mode == followTail {
		t.Fatal("streaming under inspector lost off-tail reading position")
	}
	_ = m.View()
	m = applyAll(m, tea.MouseWheelMsg{Button: tea.MouseWheelDown})
	if s.viewport.Offset() == 0 || m.vp.YOffset() != offset {
		t.Fatal("wheel did not scroll inspector exclusively")
	}
	filterBefore, scrollBefore := s.filter.Value(), s.viewport.Offset()
	for _, size := range []tea.WindowSizeMsg{{Width: 4, Height: 3}, {Width: 80, Height: 0}, {Width: 0, Height: 30}} {
		m = applyAll(m, size,
			tea.KeyPressMsg{Code: 'z', Text: "z"}, tea.KeyPressMsg{Code: tea.KeyEnter},
			tea.PasteMsg{Content: "hidden paste"}, tea.MouseWheelMsg{Button: tea.MouseWheelDown},
			tea.MouseClickMsg{X: 1, Y: 1, Button: tea.MouseLeft},
		)
		if !s.compact || s.filter.Value() != filterBefore || s.viewport.Offset() != scrollBefore || m.prompt.Value() != "untouched" || m.vp.YOffset() != offset {
			t.Fatal("compact input changed hidden controls/conversation")
		}
	}
	m = applyAll(m, tea.WindowSizeMsg{Width: 100, Height: 30}, tea.PasteMsg{Content: "file-099"})
	if s.compact || s.filter.Value() != "file-099" || m.prompt.Value() != "untouched" {
		t.Fatal("resize did not restore filter ownership before input")
	}
	for _, size := range [][2]int{{1, 1}, {12, 6}, {30, 12}, {124, 30}} {
		body, _ := s.Render(size[0], size[1])
		if lipgloss.Width(body) > size[0] || lipgloss.Height(body) > size[1] || strings.Contains(body, "\t") || strings.Contains(body, "\x1b[2J") {
			t.Fatalf("unsafe render at %v: %dx%d %q", size, lipgloss.Width(body), lipgloss.Height(body), body)
		}
	}
	m = applyAll(m, tea.WindowSizeMsg{Width: 4, Height: 3}, tea.KeyPressMsg{Code: tea.KeyF10})
	if m.modal != nil || !m.prompt.Focused() || m.prompt.Value() != "untouched" {
		t.Fatal("compact F10 failed to close safely")
	}
}

func TestWorkspacePendingInspectionCancellation(t *testing.T) {
	m, _ := newQueueModel(t)
	started := make(chan struct{})
	m.deps.WorkspaceSnapshot = func(ctx context.Context, _ string) (WorkspaceSnapshot, error) {
		close(started)
		<-ctx.Done()
		return WorkspaceSnapshot{}, ctx.Err()
	}
	m, s, load := openTestWorkspace(t, m)
	result := make(chan tea.Msg, 1)
	go func() { result <- load() }()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("inspection did not start")
	}
	m = applyAll(m, tea.KeyPressMsg{Code: tea.KeyF10})
	select {
	case late := <-result:
		m = applyAll(m, late)
	case <-time.After(5 * time.Second):
		t.Fatal("close failed to release inspection")
	}
	if !s.closed || m.modal != nil || !m.prompt.Focused() {
		t.Fatal("late cancelled result changed closed lifetime")
	}
}

func TestWorkspaceGitBranchStatesAndSafeNames(t *testing.T) {
	for _, tc := range []struct {
		name string
		git  WorkspaceGitSnapshot
		want string
	}{
		{"empty", WorkspaceGitSnapshot{Root: "/repo", Branch: "main"}, "No Git changes in inspected scope"},
		{"unborn", WorkspaceGitSnapshot{Root: "/repo", Branch: "new", Unborn: true}, "new (unborn)"},
		{"detached", WorkspaceGitSnapshot{Root: "/repo", Detached: true, Head: "0123456789abcdef"}, "detached HEAD 0123456789ab"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, _ := newQueueModel(t)
			git := tc.git
			if tc.name != "empty" {
				git.Files = []WorkspaceGitFile{{Path: "space \t\n\x1b[2J中\xff", OriginalPath: "old\nname", Index: 'R', Worktree: '.'}}
			}
			m.deps.WorkspaceSnapshot = func(context.Context, string) (WorkspaceSnapshot, error) {
				return WorkspaceSnapshot{Directory: "/repo", Git: &git}, nil
			}
			m, s, load := openTestWorkspace(t, m)
			m = applyAll(m, load())
			body, _ := s.Render(124, 60)
			plain := stripANSIstr(body)
			if !strings.Contains(plain, tc.want) || strings.Contains(plain, "ahead 0") || strings.Contains(plain, "behind 0") || strings.Contains(body, "\x1b[2J") || strings.Contains(body, "\t") {
				t.Fatalf("incorrect branch/unknown divergence or unsafe filename:\n%s", body)
			}
			if tc.name != "empty" && (!strings.Contains(plain, `\n`) || !strings.Contains(plain, `\xff`)) {
				t.Fatal("raw hostile name was not safely preserved for presentation")
			}
		})
	}
}

func TestWorkspaceCardCapAndCentering(t *testing.T) {
	m, _ := newQueueModel(t)
	m = applyAll(m, tea.WindowSizeMsg{Width: 200, Height: 60})
	m, _, _ = openTestWorkspace(t, m)
	_ = m.View()
	bounds := m.metrics.outerBounds
	if width := bounds.x1 - bounds.x0; width != 128 || bounds.x0 != (m.width-width)/2 {
		t.Fatalf("workspace card bounds = %+v, want centered cap 128", bounds)
	}
}
