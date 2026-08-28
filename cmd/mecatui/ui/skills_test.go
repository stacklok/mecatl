package ui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/theme"
)

// skillsStateOf returns the open skills surface state (the modal). It fails the
// test if the surface is not open — the pre-migration m.skills assertions route
// through this post-migration.
func skillsStateOf(t *testing.T, m Model) *skillsState {
	t.Helper()
	s, ok := m.modal.(*skillsState)
	if !ok {
		t.Fatalf("skills surface not open: modal=%T", m.modal)
	}
	return s
}

// openSkillsPanel drives the Open transition (runSkills), asserting the surface
// was installed, and returns the updated Model + the surface state.
func openSkillsPanel(t *testing.T, m Model) (Model, *skillsState) {
	t.Helper()
	mm, _ := m.runSkills()
	m = mm.(Model)
	return m, skillsStateOf(t, m)
}

// newSkillsModel builds an idle, sized Model wired to the given fakeSkills and
// the given caps, ready to open the /skills panel. It mirrors newMCPModel.
func newSkillsModel(t *testing.T, sk client.SkillLister, caps client.Capabilities) Model {
	t.Helper()
	recv := &fakeRecver{script: nil, gate: make(chan struct{})}
	send := &fakeSender{}
	conv := &fakeConv{recv: recv, send: send, caps: caps}
	m := newTestModelFromDeps(Deps{
		Session:     conv,
		Conv:        conv,
		Skills:      sk,
		Theme:       theme.New("aztec", theme.AztecPalette()),
		Server:      "127.0.0.1:8080",
		Workspace:   "/workspace",
		Mode:        "default",
		Model:       "mock-model",
		Ctx:         context.Background(),
		NoAltScreen: true,
	})
	m = applyAll(m,
		tea.WindowSizeMsg{Width: 100, Height: 30},
		client.SessionReadyMsg{SessionID: "sess-test-0001", Capabilities: caps},
	)
	return m
}

func sampleSkills() *fakeSkills {
	return &fakeSkills{
		skills: []client.Skill{
			{Name: "code-review", Description: "review a diff for bugs"},
			{Name: "deep-research", Description: "fan-out web research"},
		},
	}
}

// TestRunSkillsOpensPanel asserts runSkills opens the panel, blurs the input,
// and fires the ListSkills RPC whose result lands the inventory.
func TestRunSkillsOpensPanel(t *testing.T) {
	fs := sampleSkills()
	m := newSkillsModel(t, fs, client.Capabilities{Skills: true})

	mm, cmd := m.runSkills()
	m = mm.(Model)
	st := skillsStateOf(t, m)
	if st.view != skillsPanel {
		t.Fatalf("view = %v, want skillsPanel", st.view)
	}
	if !st.loading {
		t.Error("panel should be loading until the RPC result lands")
	}
	if m.ta.Focused() {
		t.Error("opening the panel should blur the textarea")
	}
	if cmd == nil {
		t.Fatal("runSkills should fire the ListSkills RPC command")
	}

	// Feed the RPC result back.
	m = feedCmd(t, m, cmd)
	if fs.calls != 1 {
		t.Errorf("ListSkills calls = %d, want 1", fs.calls)
	}
	if st.loading {
		t.Error("loading should clear once the result lands")
	}
	if len(st.skills) != 2 {
		t.Fatalf("skills = %#v, want 2", st.skills)
	}
	body := stripANSIstr(m.View().Content)
	if !strings.Contains(body, "code-review") || !strings.Contains(body, "fan-out web research") {
		t.Errorf("panel should render the skills, got:\n%s", body)
	}
}

// TestSkillsPanelWrapsLongDescriptions locks the overflow fix: a long skill
// description must wrap to the card's inner width instead of running off the
// right edge. It renders the panel body directly at a known width and asserts
// every line fits the wrap budget AND the description actually spilled onto >1
// indented line (so the assertion would fail if wrapping were removed).
func TestSkillsPanelWrapsLongDescriptions(t *testing.T) {
	th := theme.New("aztec", theme.AztecPalette())
	const width = 100
	budget := cardTextWidth(width)
	if budget <= 0 {
		t.Fatalf("precondition: width %d should yield a positive wrap budget", width)
	}

	long := "This is a deliberately long skill description that should wrap across " +
		"several lines instead of overflowing the panel card and running off the " +
		"right edge of the terminal the way it did before the wrapping fix landed."
	sk := []client.Skill{{Name: "wrappy", Description: long}}
	st := skillsState{view: skillsPanel, skills: sk, filtered: sk}

	plain := stripANSIstr(renderSkillsPanel(th, st, client.Capabilities{Skills: true}, defaultHelpKeys(), width))
	indented := 0
	for _, ln := range strings.Split(plain, "\n") {
		// The width check guards the WRAPPED DESCRIPTION rows (the indentWrap
		// output), not the footer hint line — the footer is a single styled
		// hint that may run past the description wrap budget, mirroring the
		// /models picker (whose footer likewise is not wrapped to the budget).
		isDesc := strings.HasPrefix(ln, "  ") && strings.TrimSpace(ln) != ""
		if isDesc {
			if w := ansi.StringWidth(ln); w > budget {
				t.Errorf("rendered description line exceeds wrap budget %d (got %d): %q", budget, w, ln)
			}
			indented++
		}
	}
	if indented < 2 {
		t.Errorf("long description should wrap onto >=2 indented lines, got %d:\n%s", indented, plain)
	}
}

// TestSkillsPanelGatedWhileRunning asserts the panel is idle-only and a nil
// Skills dep disables it (mirrors TestMCPOverlayGating).
func TestSkillsPanelGatedWhileRunning(t *testing.T) {
	m := newSkillsModel(t, sampleSkills(), client.Capabilities{Skills: true})
	m.phase = phaseRunning
	mm, _ := m.runSkills()
	if mm.(Model).modal != nil {
		t.Error("panel opened while running")
	}

	m2 := newSkillsModel(t, nil, client.Capabilities{Skills: true})
	m2.deps.Skills = nil
	mm2, cmd := m2.runSkills()
	if mm2.(Model).modal != nil {
		t.Error("panel opened with nil Skills dep")
	}
	if cmd != nil {
		t.Error("nil Skills dep should fire no command")
	}
}

// TestSkillsEscClosesPanel asserts esc closes the panel and restores idle input.
func TestSkillsEscClosesPanel(t *testing.T) {
	m := newSkillsModel(t, sampleSkills(), client.Capabilities{Skills: true})
	mm, cmd := m.runSkills()
	m = feedCmd(t, mm.(Model), cmd)
	if skillsStateOf(t, m).view != skillsPanel {
		t.Fatalf("precondition: panel should be open, view=%v", skillsStateOf(t, m).view)
	}

	mm2, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
	m = mm2.(Model)
	if m.modal != nil {
		t.Fatalf("esc did not close the panel: modal=%v", m.modal)
	}
	if !m.ta.Focused() {
		t.Error("esc should restore focus to the textarea")
	}
}

func TestSkillsRequestEpochSurvivesCloseReopen(t *testing.T) {
	m := newSkillsModel(t, sampleSkills(), client.Capabilities{Skills: true})
	m, st := openSkillsPanel(t, m)
	first := st.requestID
	firstEpoch := m.skillsEpoch
	// Close via the surface (esc on the empty filter) — the Model nils the modal.
	_, _, closed := st.HandleKey(tea.KeyPressMsg{Code: tea.KeyEsc})
	if !closed {
		t.Fatal("esc on an empty filter should close the panel")
	}
	m.modal = nil
	m, st = openSkillsPanel(t, m)
	if st.requestID <= first {
		t.Fatalf("reopened epoch=%d, first=%d", st.requestID, first)
	}
	// The epoch survives on the MODEL, not just the surface copy: m.skillsEpoch
	// is the correlation source that must increment across close/reopen.
	if m.skillsEpoch <= firstEpoch {
		t.Fatalf("Model epoch=%d, firstEpoch=%d (close/reopen must advance m.skillsEpoch, not just st.requestID)", m.skillsEpoch, firstEpoch)
	}
	_, handled, _ := st.HandleMsg(client.LearnedSkillsMsg{RequestID: first, Project: st.project, Generations: map[string]uint64{"": 99}, Skills: []client.LearnedSkill{{ID: "stale"}}})
	if !handled {
		t.Fatal("learned response was not handled")
	}
	if got := st.learned; len(got) != 0 {
		t.Fatalf("delayed response replaced reopened state: %#v", got)
	}
}

// TestSkillsErrorRendered asserts a ListSkills failure surfaces in the panel
// rather than silently degrading.
func TestSkillsErrorRendered(t *testing.T) {
	fs := &fakeSkills{err: errors.New("boom")}
	m := newSkillsModel(t, fs, client.Capabilities{Skills: true})
	mm, cmd := m.runSkills()
	m = feedCmd(t, mm.(Model), cmd)

	if skillsStateOf(t, m).err == nil {
		t.Fatal("a ListSkills error should be recorded on the panel state")
	}
	body := stripANSIstr(m.View().Content)
	if !strings.Contains(body, "list skills") || !strings.Contains(body, "boom") {
		t.Errorf("panel should render the error, got:\n%s", body)
	}
}

// TestSkillsPanelLoadingRender asserts the loading branch of renderSkillsPanel:
// open the panel but do NOT feed the result cmd, so st.loading stays true, and
// assert the rendered output carries the loading indicator (mirrors mcp_test.go's
// in-flight "refreshing…" footer assertion).
func TestSkillsPanelLoadingRender(t *testing.T) {
	m := newSkillsModel(t, sampleSkills(), client.Capabilities{Skills: true})
	mm, _ := m.runSkills()
	m = mm.(Model) // deliberately NOT feeding the RPC result — stay loading.
	if st := skillsStateOf(t, m); !st.loading {
		t.Fatalf("precondition: panel should be loading, view=%v loading=%v", st.view, st.loading)
	}
	body := stripANSIstr(m.View().Content)
	if !strings.Contains(body, "loading…") {
		t.Errorf("loading panel should render the loading indicator, got:\n%s", body)
	}
}

// TestSkillsHandleMsgFallThrough asserts the surface's HandleMsg returns
// handled=false for a non-RPC msg (a blink tick, a stream event), so the Model's
// generic reducer can see it.
func TestSkillsHandleMsgFallThrough(t *testing.T) {
	m := newSkillsModel(t, sampleSkills(), client.Capabilities{Skills: true})
	_, st := openSkillsPanel(t, m)
	if _, handled, _ := st.HandleMsg(struct{}{}); handled {
		t.Error("HandleMsg should not handle a non-RPC msg")
	}
}

// TestSkillsPanelSanitizesNames locks the sanitizeTerminal wrapper against
// deletion: open the panel with a skill whose NAME embeds ANSI/OSC escapes, feed
// the inventory, render, and assert no raw ESC (0x1b) survives in the output
// (mirrors sanitize_test.go's 0x1b guard). Per repo memory the literal is an
// innocuous ANSI escape, never a destructive-looking command.
func TestSkillsPanelSanitizesNames(t *testing.T) {
	fs := &fakeSkills{
		skills: []client.Skill{
			{Name: "\x1b]0;pwned\x07evil", Description: "\x1b[31mred\x1b[0m"},
		},
	}
	m := newSkillsModel(t, fs, client.Capabilities{Skills: true})
	mm, cmd := m.runSkills()
	m = feedCmd(t, mm.(Model), cmd)

	// stripANSI removes the LEGITIMATE theme styling escapes; what remains must
	// carry NO raw ESC — if any survives, it came from the server-derived skill
	// name/description and sanitizeTerminal was not applied.
	out := stripANSIstr(m.View().Content)
	if strings.ContainsRune(out, 0x1b) {
		t.Errorf("raw ESC (0x1b) leaked into the rendered panel; sanitizeTerminal not applied:\n%q", out)
	}
	// The sanitized name still renders as inert text (ESC stripped, body kept).
	if !strings.Contains(out, "]0;pwnedevil") {
		t.Errorf("sanitized skill name not rendered as inert text, got:\n%q", out)
	}
}

// TestSkillsKeySwallowsNonEsc asserts a non-esc, non-scroll key while the panel
// is open feeds the filter (handled=true, so it never leaks into idle input) and
// does NOT move the scroll offset — and that a scroll key is HANDLED (not a
// close, not a leak). The panel is now type-to-filter (issue #176), so a typed
// "j" accumulates into the filter rather than being swallowed outright.
func TestSkillsKeySwallowsNonEsc(t *testing.T) {
	m := newSkillsModel(t, sampleSkills(), client.Capabilities{Skills: true})
	mm, cmd := m.runSkills()
	m = feedCmd(t, mm.(Model), cmd)

	st := skillsStateOf(t, m)
	_, handled, _ := st.HandleKey(tea.KeyPressMsg{Code: 'j', Text: "j"})
	if !handled {
		t.Error("a non-esc key while the panel is open should be handled (handled=true)")
	}
	if st.scroll != 0 {
		t.Error("a non-scroll key should not move the scroll offset")
	}
	if got := st.filter.Value(); got != "j" {
		t.Errorf("typing 'j' should feed the filter, got value %q want \"j\"", got)
	}

	// A scroll key is handled too (and keeps the panel open). The 2-skill sample
	// fits the window, so the offset stays clamped at 0.
	_, handled, _ = st.HandleKey(tea.KeyPressMsg{Code: tea.KeyPgDown})
	if !handled {
		t.Error("pgdown while the panel is open should be handled")
	}
	if st.view != skillsPanel {
		t.Error("pgdown should not close the panel")
	}
	if st.scroll != 0 {
		t.Errorf("a fitting inventory should clamp scroll at 0, got %d", st.scroll)
	}
}

// typeSkillsFilter feeds each rune of s into the open skills panel's focused
// filter input via the surface's HandleKey (the production routing), asserting
// each key is handled. It mirrors typeFilter in models_test.go.
func typeSkillsFilter(t *testing.T, m Model, s string) Model {
	t.Helper()
	st := skillsStateOf(t, m)
	for _, r := range s {
		_, handled, _ := st.HandleKey(tea.KeyPressMsg{Code: r, Text: string(r)})
		if !handled {
			t.Fatalf("typing %q should be handled by the open skills panel", r)
		}
	}
	return m
}

// TestSkillsFilterNarrows: typing "deep" narrows to the single deep-research row,
// the scroll clamps to 0, and the rendered body shows only deep-research.
func TestSkillsFilterNarrows(t *testing.T) {
	m := newSkillsModel(t, sampleSkills(), client.Capabilities{Skills: true})
	mm, cmd := m.runSkills()
	m = feedCmd(t, mm.(Model), cmd)

	m = typeSkillsFilter(t, m, "deep")
	st := skillsStateOf(t, m)
	if len(st.filtered) != 1 {
		t.Fatalf("filtered len = %d, want 1 (only deep-research matches)", len(st.filtered))
	}
	if st.filtered[0].Name != "deep-research" {
		t.Fatalf("filtered[0].Name = %q, want deep-research", st.filtered[0].Name)
	}
	if st.scroll != 0 {
		t.Errorf("scroll after narrowing = %d, want 0 (clamped)", st.scroll)
	}
	body := stripANSIstr(m.View().Content)
	if !strings.Contains(body, "deep-research") {
		t.Errorf("rendered body should show deep-research, got:\n%s", body)
	}
	if strings.Contains(body, "code-review") {
		t.Errorf("rendered body should NOT show the filtered-out code-review, got:\n%s", body)
	}
}

// TestSkillsFilterCaseInsensitive: "DEEP" matches deep-research.
func TestSkillsFilterCaseInsensitive(t *testing.T) {
	m := newSkillsModel(t, sampleSkills(), client.Capabilities{Skills: true})
	mm, cmd := m.runSkills()
	m = feedCmd(t, mm.(Model), cmd)

	m = typeSkillsFilter(t, m, "DEEP")
	if st := skillsStateOf(t, m); len(st.filtered) != 1 || st.filtered[0].Name != "deep-research" {
		t.Errorf("case-insensitive filter \"DEEP\" should match deep-research, got %+v", st.filtered)
	}
}

// TestSkillsFilterByDescription: "research" matches the deep-research DESCRIPTION
// (not its name), proving Description is a match field.
func TestSkillsFilterByDescription(t *testing.T) {
	m := newSkillsModel(t, sampleSkills(), client.Capabilities{Skills: true})
	mm, cmd := m.runSkills()
	m = feedCmd(t, mm.(Model), cmd)

	m = typeSkillsFilter(t, m, "research")
	if st := skillsStateOf(t, m); len(st.filtered) != 1 || st.filtered[0].Name != "deep-research" {
		t.Errorf("filter \"research\" should match deep-research by description, got %+v", st.filtered)
	}
}

// TestSkillsFilterEscClears asserts the two-stage esc: a non-empty filter is
// cleared (panel stays open, filtered restored to the full list); a second esc
// (now-empty filter) closes the panel.
func TestSkillsFilterEscClears(t *testing.T) {
	m := newSkillsModel(t, sampleSkills(), client.Capabilities{Skills: true})
	mm, cmd := m.runSkills()
	m = feedCmd(t, mm.(Model), cmd)
	m = typeSkillsFilter(t, m, "deep")
	st := skillsStateOf(t, m)
	if len(st.filtered) != 1 {
		t.Fatalf("precondition: filtered len = %d, want 1", len(st.filtered))
	}

	// First esc: clears the filter, stays open.
	_, handled, closed := st.HandleKey(tea.KeyPressMsg{Code: tea.KeyEsc})
	if !handled {
		t.Fatal("esc should be handled by the open panel")
	}
	if closed || st.view != skillsPanel {
		t.Error("first esc (non-empty filter) should keep the panel open")
	}
	if st.filter.Value() != "" {
		t.Errorf("first esc should clear the filter, value = %q", st.filter.Value())
	}
	if len(st.filtered) != 2 {
		t.Errorf("filtered should be restored to the full list, len = %d want 2", len(st.filtered))
	}

	// Second esc: empty filter ⇒ closes.
	_, _, closed = st.HandleKey(tea.KeyPressMsg{Code: tea.KeyEsc})
	if !closed {
		t.Error("second esc (empty filter) should close the panel")
	}
}

// TestSkillsFilterNoMatchNote asserts the filter-no-match note renders (and the
// empty-state "No skills configured" copy does NOT — the inventory is non-empty,
// just unmatched).
func TestSkillsFilterNoMatchNote(t *testing.T) {
	m := newSkillsModel(t, sampleSkills(), client.Capabilities{Skills: true})
	mm, cmd := m.runSkills()
	m = feedCmd(t, mm.(Model), cmd)

	m = typeSkillsFilter(t, m, "zzzzz")
	if st := skillsStateOf(t, m); len(st.filtered) != 0 {
		t.Fatalf("filtered len = %d, want 0 (no match)", len(st.filtered))
	}
	body := stripANSIstr(m.View().Content)
	if !strings.Contains(body, `no skills match "zzzzz" — esc to clear`) {
		t.Errorf("body should carry the no-match note, got:\n%s", body)
	}
	if strings.Contains(body, "No skills configured") {
		t.Errorf("body should NOT carry the empty-state copy (inventory is non-empty), got:\n%s", body)
	}
}

// TestSkillsFilterEmptyShowsFull: an empty query ⇒ filtered == skills (the full
// list). Guards against a filter that drops everything on an empty query.
func TestSkillsFilterEmptyShowsFull(t *testing.T) {
	m := newSkillsModel(t, sampleSkills(), client.Capabilities{Skills: true})
	mm, cmd := m.runSkills()
	m = feedCmd(t, mm.(Model), cmd)

	if st := skillsStateOf(t, m); len(st.filtered) != len(st.skills) {
		t.Errorf("empty filter: filtered len = %d, want %d (== skills)", len(st.filtered), len(st.skills))
	}
}

// TestSkillsFilterJKNotIntercepted guards the arrow-only routing (the j/k-in-
// Up/Down trap, mirroring TestModelsFilterDoesNotInterceptJK): typing a query
// containing j/k must accumulate into the filter and NOT move the scroll offset.
func TestSkillsFilterJKNotIntercepted(t *testing.T) {
	fs := &fakeSkills{skills: []client.Skill{
		{Name: "kotlin-thing", Description: "a jvm skill"},
		{Name: "java-thing", Description: "also jvm"},
		{Name: "code-review", Description: "review a diff"},
	}}
	m := newSkillsModel(t, fs, client.Capabilities{Skills: true})
	mm, cmd := m.runSkills()
	m = feedCmd(t, mm.(Model), cmd)

	m = typeSkillsFilter(t, m, "kotlin")
	st := skillsStateOf(t, m)
	if st.filter.Value() != "kotlin" {
		t.Errorf("filter value = %q, want \"kotlin\" (k/o/t/l/i/n must type, not navigate)", st.filter.Value())
	}
	if st.scroll != 0 {
		t.Errorf("scroll moved to %d while typing \"kotlin\"; j/k must not be intercepted as nav", st.scroll)
	}
	if len(st.filtered) != 1 || st.filtered[0].Name != "kotlin-thing" {
		t.Errorf("filter \"kotlin\" should narrow to kotlin-thing, got %+v", st.filtered)
	}
}

// scrollSkills returns n description-less skills ("skill-00".."skill-NN") — one
// rendered row each, UNIQUE so a render bug that ignored st.scroll (always
// showing the first window) would be caught (the TestSoulScroll fixture rationale).
func scrollSkills(n int) *fakeSkills {
	fs := &fakeSkills{}
	for i := 0; i < n; i++ {
		fs.skills = append(fs.skills, client.Skill{Name: fmt.Sprintf("skill-%02d", i)})
	}
	return fs
}

// TestSkillsScrollViaUpdate routes a scroll key through the REAL m.Update path
// (onKey → onOverlayKey → dispatchSurfaceKey → HandleKey), NOT the surface's
// HandleKey helper that bypasses the dispatchSurfaceKey routing — the peer of
// soul_test.go's TestSoulScrollViaUpdate. It pins that a scroll key reaches the
// open modal through the full dispatch and moves the window without closing the
// panel.
func TestSkillsScrollViaUpdate(t *testing.T) {
	m := newSkillsModel(t, scrollSkills(30), client.Capabilities{Skills: true})
	mm, cmd := m.runSkills()
	m = feedCmd(t, mm.(Model), cmd)
	st := skillsStateOf(t, m)
	if st.scroll != 0 {
		t.Fatalf("precondition: initial scroll = %d, want 0", st.scroll)
	}

	mm2, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyPgDown})
	m = mm2.(Model)
	st = skillsStateOf(t, m) // panics/fails if the panel closed
	if st.scroll != 1 {
		t.Errorf("scroll after pgdown via m.Update = %d, want 1", st.scroll)
	}
}

// TestSkillsWheelScrollsPanel asserts a mouse wheel while the skills panel is
// open is CONSUMED by the panel (HandleWheel handled=true — the default-consume
// contract): the panel's own scroll window moves and the conversation viewport
// behind it does NOT (the consume contract). It is the peer of soul_test.go's
// TestSoulWheelConsumedWhileOpen, driving tea.MouseWheelMsg through the REAL
// m.Update path (onMouseMsg → onMouseWheel → m.modal.HandleWheel). The
// description-less scrollSkills fixture yields one rendered row per skill, so
// 30 skills overflow the fixed skillsBodyLines window and the wheel can move.
func TestSkillsWheelScrollsPanel(t *testing.T) {
	m := newSkillsModel(t, scrollSkills(30), client.Capabilities{Skills: true})
	// Long transcript so the viewport WOULD be scrollable if the wheel fell
	// through; start stuck at the bottom like a fresh stream (the soul peer
	// fixture).
	m.conv.addUser("show me a long answer")
	m.conv.appendAssistant(strings.Repeat("line of streamed output\n", 120))
	m.phase = phaseIdle
	m.stuck = true
	m.refreshView()
	if !m.vp.AtBottom() {
		t.Fatal("precondition: viewport should start at the bottom")
	}

	mm, cmd := m.runSkills()
	m = feedCmd(t, mm.(Model), cmd)
	st := skillsStateOf(t, m)
	if st.scroll != 0 {
		t.Fatalf("precondition: initial scroll = %d, want 0", st.scroll)
	}

	// Wheel down through the REAL Update path; the panel consumes it.
	mm2, _ := m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelDown})
	m = mm2.(Model)
	st = skillsStateOf(t, m) // fails if the panel closed
	if st.scroll != 1 {
		t.Errorf("skills scroll after wheel-down = %d, want 1 (the panel owns the wheel)", st.scroll)
	}
	if !m.stuck {
		t.Error("a consumed wheel must NOT unstick the view (the panel eats it)")
	}
	if !m.vp.AtBottom() {
		t.Error("a consumed wheel must NOT scroll the conversation viewport (the panel eats it)")
	}
}

// TestLearnedMsgsViaUpdate drives the three learned-lifecycle RPC Msgs
// (LearnedSkillsMsg / LearnedSkillMsg / SkillDiffMsg) through the REAL
// m.Update path (update → dispatchNonInputMsg → dispatchSurfaceMsg →
// HandleMsg), NOT st.HandleMsg directly, asserting each lands on the surface
// state. Along with TestSkillsMsgViaUpdate (SkillsMsg) this pins the
// dispatchSurfaceMsg → HandleMsg routing for ALL four RPC Msgs.
func TestLearnedMsgsViaUpdate(t *testing.T) {
	skill := client.LearnedSkill{Project: "/project", ID: "skill-1", Name: "review", OwnerAgent: "agent", Version: "v2", Revision: "r2", State: "active"}
	m := newSkillsModel(t, sampleSkills(), client.Capabilities{Skills: true})
	m, _ = openSkillsPanel(t, m) // open via runSkills; deliberately NOT feeding its cmd
	st := skillsStateOf(t, m)

	// (1) LearnedSkillsMsg lands the learned list.
	mm2, _ := m.Update(client.LearnedSkillsMsg{
		RequestID:   st.requestID,
		Project:     st.project,
		Skills:      []client.LearnedSkill{skill},
		Generations: map[string]uint64{"/project": 1},
	})
	m = mm2.(Model)
	st = skillsStateOf(t, m)
	if len(st.learned) != 1 || st.learned[0].ID != "skill-1" {
		t.Fatalf("LearnedSkillsMsg via m.Update did not land the learned list: %#v", st.learned)
	}

	// (2) LearnedSkillMsg opens the detail view.
	mm3, _ := m.Update(client.LearnedSkillMsg{
		RequestID:       st.requestID,
		Project:         "/project",
		Skill:           &skill,
		Generation:      1,
		SelectedSkillID: "skill-1",
		SelectedVersion: "v2",
	})
	m = mm3.(Model)
	st = skillsStateOf(t, m)
	if st.view != skillsDetail || st.detail == nil || st.detail.ID != "skill-1" {
		t.Fatalf("LearnedSkillMsg via m.Update did not open the detail: view=%v detail=%#v", st.view, st.detail)
	}

	// (3) SkillDiffMsg lands the diff on the open detail.
	mm4, _ := m.Update(client.SkillDiffMsg{
		RequestID: st.requestID,
		Project:   "/project",
		SkillID:   "skill-1",
		Version:   "v2",
		Diff:      "- old\n+ new",
	})
	m = mm4.(Model)
	st = skillsStateOf(t, m)
	if st.diff != "- old\n+ new" {
		t.Fatalf("SkillDiffMsg via m.Update did not land the diff: %q", st.diff)
	}
	// The error-free diff clears any recorded error.
	if st.err != nil {
		t.Fatalf("a clean SkillDiffMsg should leave err nil, got %v", st.err)
	}
}

// TestSkillsScrollClampedInRender pins the ImGui clamp-in-Render guarantee:
// the scroll window's clamp target is re-derived AT THE TOP OF Render from the
// FRESH offered width (s.width is a view cache Render refreshes every frame),
// so widening the terminal — which SHRINKS the wrapped row total for a given
// inventory — re-clamps a now-out-of-bounds scroll on the next Render even if no
// HandleKey/HandleWheel call runs against the new width. A Render that did not
// refresh s.width (or clamped only in HandleKey) would leave the stale offset
// until a key arrived; a HandleKey AFTER the widen would then clamp against the
// stale view-cache width.
func TestSkillsScrollClampedInRender(t *testing.T) {
	// A long description per skill: each wraps to a couple of lines at the WIDE
	// width but ~10 lines at the NARROW one, so the row total grows a lot when
	// narrowed and shrinks back when widened — the WIDENING direction is the one
	// that can leave a bottom-pinned scroll out of bounds (the rowTotal never
	// shrinks HandleKey-side, only Render-side).
	desc := strings.Repeat("w ", 90)
	fs := &fakeSkills{}
	for i := 0; i < 30; i++ {
		fs.skills = append(fs.skills, client.Skill{Name: fmt.Sprintf("skill-%02d", i), Description: desc})
	}
	m := newSkillsModel(t, fs, client.Capabilities{Skills: true})
	// Open NARROW so the bottom jump pins a large offset against the large total.
	mm2, _ := m.Update(tea.WindowSizeMsg{Width: 40, Height: 30})
	m = mm2.(Model)
	mm, cmd := m.runSkills()
	m = feedCmd(t, mm.(Model), cmd)
	st := skillsStateOf(t, m)

	// Prime the view cache (Render refreshes s.width from the fresh width), then
	// jump to the bottom: the clamp is against the narrow width's row total.
	_ = m.View()
	card := m.deps.Theme.Style("askCard")
	contentWidth := m.width - card.GetBorderLeftSize() - card.GetBorderRightSize() - card.GetPaddingLeft() - card.GetPaddingRight()
	if st.width != contentWidth {
		t.Fatalf("precondition: Render should refresh the view-cache width to %d, got %d", contentWidth, st.width)
	}
	_, _, _ = st.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnd})
	narrowTotal := st.rowTotal(st.deps.theme, contentWidth)
	if st.scroll != narrowTotal-skillsBodyLines {
		t.Fatalf("precondition: scroll after End at width 40 = %d, want %d (rowTotal %d − %d)",
			st.scroll, narrowTotal-skillsBodyLines, narrowTotal, skillsBodyLines)
	}

	// Widen the terminal via the production resize path, then render a frame. The
	// next Render derives the clamp from the FRESH width: the shrunken row total
	// re-clamps the offset DOWN to the new maximum — and s.width moves with it.
	mm3, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = mm3.(Model)
	_ = m.View()
	wideWidth := m.width - card.GetBorderLeftSize() - card.GetBorderRightSize() - card.GetPaddingLeft() - card.GetPaddingRight()
	wideTotal := st.rowTotal(st.deps.theme, wideWidth)
	if wideTotal >= narrowTotal {
		t.Fatalf("precondition: widening should shrink the row total, narrow=%d wide=%d", narrowTotal, wideTotal)
	}
	if st.width != wideWidth {
		t.Errorf("Render should refresh the view-cache width to %d, got %d", wideWidth, st.width)
	}
	if st.scroll != wideTotal-skillsBodyLines {
		t.Errorf("after widening, Render should re-clamp scroll to %d (rowTotal %d − %d), got %d",
			wideTotal-skillsBodyLines, wideTotal, skillsBodyLines, st.scroll)
	}
}

// TestSkillsMsgViaUpdate routes a client.SkillsMsg through the REAL m.Update
// path (update → dispatchNonInputMsg → dispatchSurfaceMsg → HandleMsg), NOT the
// surface's HandleMsg helper — the peer of the soul path. It pins that an RPC
// result reaches the open modal via the full dispatch and lands the inventory.
func TestSkillsMsgViaUpdate(t *testing.T) {
	m := newSkillsModel(t, sampleSkills(), client.Capabilities{Skills: true})
	m, _ = openSkillsPanel(t, m) // open via runSkills; deliberately NOT feeding its cmd

	mm2, _ := m.Update(client.SkillsMsg{Skills: []client.Skill{{Name: "code-review"}, {Name: "deep-research"}}})
	m = mm2.(Model)
	st := skillsStateOf(t, m)
	if st.loading {
		t.Fatal("loading should clear once the result lands via m.Update")
	}
	if len(st.skills) != 2 {
		t.Fatalf("skills = %#v, want 2 landed via m.Update", st.skills)
	}
}

// TestSkillsScroll asserts the scroll keys move (and clamp) the inventory row
// window AND that the rendered window content + the "lines X–Y of N" indicator
// actually shift (mirrors TestSoulScroll). It also locks the scroll reset on a
// fresh inventory result and that esc still closes the scrolled panel.
func TestSkillsScroll(t *testing.T) {
	// 30 one-row skills exceed skillsBodyLines (14), each window distinguishable.
	m := newSkillsModel(t, scrollSkills(30), client.Capabilities{Skills: true})
	mm, cmd := m.runSkills()
	m = feedCmd(t, mm.(Model), cmd)
	st := skillsStateOf(t, m)

	if st.scroll != 0 {
		t.Fatalf("initial scroll = %d, want 0", st.scroll)
	}
	// At the top: window is rows 1–14 (skill-00..skill-13); the tail is NOT visible.
	top := stripANSIstr(m.View().Content)
	if !strings.Contains(top, "skill-00") || strings.Contains(top, "skill-29") {
		t.Errorf("top window should show skill-00 and NOT skill-29, got:\n%s", top)
	}
	if !strings.Contains(top, "lines 1–14 of 30") {
		t.Errorf("top indicator should read 'lines 1–14 of 30', got:\n%s", top)
	}

	// Page down once: skill-00 leaves the top, skill-14 enters.
	_, _, _ = st.HandleKey(tea.KeyPressMsg{Code: tea.KeyPgDown})
	if st.scroll != 1 {
		t.Errorf("scroll after pgdown = %d, want 1", st.scroll)
	}
	pd := stripANSIstr(m.View().Content)
	if strings.Contains(pd, "skill-00") {
		t.Errorf("after pgdown the window should no longer show skill-00, got:\n%s", pd)
	}
	if !strings.Contains(pd, "skill-14") {
		t.Errorf("after pgdown the window should reveal skill-14, got:\n%s", pd)
	}
	if !strings.Contains(pd, "lines 2–15 of 30") {
		t.Errorf("after pgdown the indicator should read 'lines 2–15 of 30', got:\n%s", pd)
	}

	// Jump to bottom; max scroll = 30 - 14 = 16: the tail becomes visible.
	_, _, _ = st.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnd})
	if st.scroll != 16 {
		t.Errorf("scroll after End = %d, want 16 (30-14)", st.scroll)
	}
	bot := stripANSIstr(m.View().Content)
	if !strings.Contains(bot, "skill-29") || strings.Contains(bot, "skill-00") {
		t.Errorf("bottom window should show skill-29 and NOT skill-00, got:\n%s", bot)
	}
	if !strings.Contains(bot, "lines 17–30 of 30") {
		t.Errorf("bottom indicator should read 'lines 17–30 of 30', got:\n%s", bot)
	}

	// Pgdown past the end clamps.
	_, _, _ = st.HandleKey(tea.KeyPressMsg{Code: tea.KeyPgDown})
	if st.scroll != 16 {
		t.Errorf("scroll clamps at 16, got %d", st.scroll)
	}

	// Page up moves back (pins the ScrollU arm — removing it must fail here).
	_, _, _ = st.HandleKey(tea.KeyPressMsg{Code: tea.KeyPgUp})
	if st.scroll != 15 {
		t.Errorf("scroll after pgup = %d, want 15", st.scroll)
	}

	// Home returns to the top.
	_, _, _ = st.HandleKey(tea.KeyPressMsg{Code: tea.KeyHome})
	if st.scroll != 0 {
		t.Errorf("scroll after Home = %d, want 0", st.scroll)
	}

	// A fresh inventory result resets a stale offset (never opens mid-list).
	_, _, _ = st.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnd})
	_, _, _ = st.HandleMsg(client.SkillsMsg{Skills: scrollSkills(30).skills})
	if st.scroll != 0 {
		t.Errorf("a fresh SkillsMsg should reset scroll to 0, got %d", st.scroll)
	}

	// esc still closes the scrolled panel.
	_, _, closed := st.HandleKey(tea.KeyPressMsg{Code: tea.KeyEsc})
	if !closed {
		t.Error("esc should still close the scrolled panel")
	}
}

// TestSkillsErrorClearedOnSuccess asserts a success result after an error clears
// the recorded error (the panel doesn't keep showing a stale failure).
func TestSkillsErrorClearedOnSuccess(t *testing.T) {
	m := newSkillsModel(t, sampleSkills(), client.Capabilities{Skills: true})

	_, st := openSkillsPanel(t, m)

	// First drive an error result.
	_, _, _ = st.HandleMsg(client.SkillsMsg{Err: errors.New("boom")})
	if st.err == nil {
		t.Fatal("precondition: error should be recorded")
	}

	// Then a successful result must clear it.
	_, _, _ = st.HandleMsg(client.SkillsMsg{Skills: []client.Skill{{Name: "x"}}})
	if st.err != nil {
		t.Errorf("a successful result should clear the prior error, got %v", st.err)
	}
}

// TestSkillsPanelScrollGolden locks the scrolled, overflowing inventory panel:
// an inventory exceeding skillsBodyLines, paged down once, so the golden carries
// the windowed rows + the "lines X–Y of N" indicator + the scroll footer hint
// (the TestSoulPanelGolden pattern; the fixed window keeps it deterministic).
func TestSkillsPanelScrollGolden(t *testing.T) {
	m := newSkillsModel(t, scrollSkills(30), client.Capabilities{Skills: true})
	mm, cmd := m.runSkills()
	m = feedCmd(t, mm.(Model), cmd)
	st := skillsStateOf(t, m)
	if st.view != skillsPanel {
		t.Fatalf("view = %v, want skillsPanel", st.view)
	}
	_, _, _ = st.HandleKey(tea.KeyPressMsg{Code: tea.KeyPgDown})
	got := stripANSI([]byte(m.View().Content))
	compareGolden(t, "skills_scroll.golden", got)
}

// TestSkillsPanelGolden locks the base panel (sample skills, empty filter) so the
// filter row + the new footer hint are part of the locked surface.
func TestSkillsPanelGolden(t *testing.T) {
	m := newSkillsModel(t, sampleSkills(), client.Capabilities{Skills: true})
	mm, cmd := m.runSkills()
	m = feedCmd(t, mm.(Model), cmd)
	if st := skillsStateOf(t, m); st.view != skillsPanel {
		t.Fatalf("view = %v, want skillsPanel", st.view)
	}
	got := stripANSI([]byte(m.View().Content))
	compareGolden(t, "skills.golden", got)
}

// TestSkillsPanelFilteredGolden locks the panel narrowed by a filter ("deep" →
// deep-research only), so the filtered body + the filter input value are locked.
func TestSkillsPanelFilteredGolden(t *testing.T) {
	m := newSkillsModel(t, sampleSkills(), client.Capabilities{Skills: true})
	mm, cmd := m.runSkills()
	m = feedCmd(t, mm.(Model), cmd)
	m = typeSkillsFilter(t, m, "deep")
	got := stripANSI([]byte(m.View().Content))
	compareGolden(t, "skills_filtered.golden", got)
}

// TestSkillsPanelNomatchGolden locks the filter-no-match note ("zzzzz" → no
// skills match), the distinct recovery copy a reviewer can eyeball.
func TestSkillsPanelNomatchGolden(t *testing.T) {
	m := newSkillsModel(t, sampleSkills(), client.Capabilities{Skills: true})
	mm, cmd := m.runSkills()
	m = feedCmd(t, mm.(Model), cmd)
	m = typeSkillsFilter(t, m, "zzzzz")
	got := stripANSI([]byte(m.View().Content))
	compareGolden(t, "skills_nomatch.golden", got)
}

// TestSkillsEmptyStateNotEnabled asserts the panel distinguishes "skills not
// enabled on this server" (caps.Skills false) from "enabled but none configured".
func TestSkillsEmptyStateNotEnabled(t *testing.T) {
	// caps.Skills false, empty inventory → "not enabled" copy.
	disabled := skillsEmptyCopy(client.Capabilities{Skills: false})
	if !strings.Contains(disabled, "not enabled") {
		t.Errorf("disabled empty copy = %q, want 'not enabled' framing", disabled)
	}
	// caps.Skills true, empty inventory → "none configured" copy.
	enabled := skillsEmptyCopy(client.Capabilities{Skills: true})
	if strings.Contains(enabled, "not enabled") {
		t.Errorf("enabled empty copy should not say 'not enabled', got %q", enabled)
	}
	if !strings.Contains(enabled, "No skills configured") {
		t.Errorf("enabled empty copy = %q, want 'No skills configured'", enabled)
	}
}

// TestFilterSkillsPreservesOrder mirrors TestModelsFilterPreservesOrder: a query
// matching >1 row keeps the input order (the server's name-sort). A reordering
// regression in filterSkills would pass the E2E narrowing tests but fail here.
func TestFilterSkillsPreservesOrder(t *testing.T) {
	in := []client.Skill{
		{Name: "z-skill", Description: "zzz last"},
		{Name: "a-skill", Description: "aaa first"},
		{Name: "m-skill", Description: "mmm middle"},
	}
	got := filterSkills(in, "skill")
	if len(got) != 3 {
		t.Fatalf("filter \"skill\" → %d rows, want 3", len(got))
	}
	for i := range in {
		if got[i].Name != in[i].Name {
			t.Errorf("filtered[%d].Name = %q, want %q (input order must be preserved)", i, got[i].Name, in[i].Name)
		}
	}
}

// TestFilterSkillsUppercaseField exercises the field-side strings.ToLower arm: a
// lowercase query matches an UPPERCASE field value (the existing
// TestSkillsFilterCaseInsensitive only tests an uppercase QUERY vs lowercase fields).
func TestFilterSkillsUppercaseField(t *testing.T) {
	in := []client.Skill{{Name: "DEEP-RESEARCH", Description: "REVIEW"}}
	got := filterSkills(in, "deep")
	if len(got) != 1 {
		t.Fatalf("filter \"deep\" vs uppercase field → %d, want 1", len(got))
	}
	got2 := filterSkills(in, "review")
	if len(got2) != 1 {
		t.Fatalf("filter \"review\" vs uppercase desc → %d, want 1", len(got2))
	}
}
