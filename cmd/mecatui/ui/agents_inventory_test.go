package ui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/theme"
)

// Tests for the /agents agent-definition INVENTORY panel: a read-only, idle-only
// overlay that fires ListAgents and renders the resolved registry the Subagent tool
// routes delegations to (name + description + model/perm/tools metadata).
// Distinct from the live-team overlay (/team, f6) in team_overlay_test.go.

// newAgentsInvModel builds an idle, sized Model wired to the given fakeAgents and
// caps, ready to open the /agents panel. It mirrors newSkillsModel.
func newAgentsInvModel(t *testing.T, ag client.AgentLister, caps client.Capabilities) Model {
	t.Helper()
	recv := &fakeRecver{script: nil, gate: make(chan struct{})}
	send := &fakeSender{}
	conv := &fakeConv{recv: recv, send: send, caps: caps}
	m := newTestModelFromDeps(Deps{
		Session:     conv,
		Conv:        conv,
		Agents:      ag,
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

func sampleAgents() *fakeAgents {
	return &fakeAgents{
		agents: []client.Agent{
			{Name: "scout", Description: "explore the codebase", Model: "gpt-5", PermissionMode: "plan", Tools: []string{"Read", "Grep"}},
			{Name: "writer", Description: "draft prose"},
		},
	}
}

// TestRunAgentsInvOpensPanel asserts runAgentsInv opens the panel, blurs the
// input, fires ListAgents, and renders the resolved defs (name + description +
// metadata) once the result lands.
func TestRunAgentsInvOpensPanel(t *testing.T) {
	fa := sampleAgents()
	m := newAgentsInvModel(t, fa, client.Capabilities{Agents: true})

	mm, cmd := m.runAgentsInv()
	m = mm.(Model)
	if m.agentsInv.view != agentsInvPanel {
		t.Fatalf("view = %v, want agentsInvPanel", m.agentsInv.view)
	}
	if !m.agentsInv.loading {
		t.Error("panel should be loading until the RPC result lands")
	}
	if m.prompt.Focused() {
		t.Error("opening the panel should blur the textarea")
	}
	if cmd == nil {
		t.Fatal("runAgentsInv should fire the ListAgents RPC command")
	}

	m = feedCmd(t, m, cmd)
	if fa.calls != 1 {
		t.Errorf("ListAgents calls = %d, want 1", fa.calls)
	}
	if m.agentsInv.loading {
		t.Error("loading should clear once the result lands")
	}
	if len(m.agentsInv.agents) != 2 {
		t.Fatalf("agents = %#v, want 2", m.agentsInv.agents)
	}
	body := stripANSIstr(m.View().Content)
	if !strings.Contains(body, "scout") || !strings.Contains(body, "explore the codebase") {
		t.Errorf("panel should render the def name + description, got:\n%s", body)
	}
	// The metadata line for scout: model/perm/tools, omitting nothing.
	if !strings.Contains(body, "model:gpt-5") || !strings.Contains(body, "perm:plan") ||
		!strings.Contains(body, "tools:Read,Grep") {
		t.Errorf("panel should render the metadata line, got:\n%s", body)
	}
}

// TestAgentsInvMetaLineOmitsEmptyFields asserts a def with no model/perm/tools
// renders no spurious metadata line, and a partial def shows only its known
// segments.
func TestAgentsInvMetaLineOmitsEmptyFields(t *testing.T) {
	if got := agentMetaLine(client.Agent{Name: "bare"}); got != "" {
		t.Errorf("agentMetaLine of a bare def = %q, want empty", got)
	}
	got := agentMetaLine(client.Agent{Model: "gpt-5", Tools: []string{"Read"}})
	if strings.Contains(got, "perm:") {
		t.Errorf("metadata line should omit the empty perm segment, got %q", got)
	}
	if !strings.Contains(got, "model:gpt-5") || !strings.Contains(got, "tools:Read") {
		t.Errorf("metadata line = %q, want model + tools segments", got)
	}
}

// TestAgentsInvPanelGatedWhileRunning asserts the panel is idle-only and a nil
// Agents dep disables it (mirrors TestSkillsPanelGatedWhileRunning).
func TestAgentsInvPanelGatedWhileRunning(t *testing.T) {
	m := newAgentsInvModel(t, sampleAgents(), client.Capabilities{Agents: true})
	m.phase = phaseRunning
	mm, _ := m.openAgentsInv()
	if mm.(Model).agentsInv.view != agentsInvNone {
		t.Error("panel opened while running")
	}

	m2 := newAgentsInvModel(t, nil, client.Capabilities{Agents: true})
	m2.deps.Agents = nil
	mm2, cmd := m2.openAgentsInv()
	if mm2.(Model).agentsInv.view != agentsInvNone {
		t.Error("panel opened with nil Agents dep")
	}
	if cmd != nil {
		t.Error("nil Agents dep should fire no command")
	}
}

// TestAgentsInvEscClosesPanel asserts esc closes the panel and restores idle input.
func TestAgentsInvEscClosesPanel(t *testing.T) {
	m := newAgentsInvModel(t, sampleAgents(), client.Capabilities{Agents: true})
	mm, cmd := m.runAgentsInv()
	m = feedCmd(t, mm.(Model), cmd)
	if m.agentsInv.view != agentsInvPanel {
		t.Fatalf("precondition: panel should be open, view=%v", m.agentsInv.view)
	}

	mm2, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
	m = mm2.(Model)
	if m.agentsInv.view != agentsInvNone {
		t.Fatalf("esc did not close the panel: %v", m.agentsInv.view)
	}
	if !m.prompt.Focused() {
		t.Error("esc should restore focus to the textarea")
	}
}

// TestAgentsInvErrorRendered asserts a ListAgents failure surfaces in the panel
// rather than silently degrading.
func TestAgentsInvErrorRendered(t *testing.T) {
	fa := &fakeAgents{err: errors.New("boom")}
	m := newAgentsInvModel(t, fa, client.Capabilities{Agents: true})
	mm, cmd := m.runAgentsInv()
	m = feedCmd(t, mm.(Model), cmd)

	if m.agentsInv.err == nil {
		t.Fatal("a ListAgents error should be recorded on the panel state")
	}
	body := stripANSIstr(m.View().Content)
	if !strings.Contains(body, "list agents") || !strings.Contains(body, "boom") {
		t.Errorf("panel should render the error, got:\n%s", body)
	}
}

// TestAgentsInvPanelLoadingRender asserts the loading branch: open the panel but
// do NOT feed the result cmd, so st.loading stays true.
func TestAgentsInvPanelLoadingRender(t *testing.T) {
	m := newAgentsInvModel(t, sampleAgents(), client.Capabilities{Agents: true})
	mm, _ := m.runAgentsInv()
	m = mm.(Model) // deliberately NOT feeding the RPC result — stay loading.
	if !m.agentsInv.loading {
		t.Fatalf("precondition: panel should be loading, view=%v loading=%v", m.agentsInv.view, m.agentsInv.loading)
	}
	body := stripANSIstr(m.View().Content)
	if !strings.Contains(body, "loading…") {
		t.Errorf("loading panel should render the loading indicator, got:\n%s", body)
	}
}

// TestUpdateAgentsInvMsgFallThrough asserts updateAgentsInvMsg returns
// handled=false for a non-AgentsMsg, so Update falls through.
func TestUpdateAgentsInvMsgFallThrough(t *testing.T) {
	m := newAgentsInvModel(t, sampleAgents(), client.Capabilities{Agents: true})
	if _, handled := m.updateAgentsInvMsg(tea.KeyPressMsg{Code: tea.KeyEnter}); handled {
		t.Error("updateAgentsInvMsg should not handle a non-AgentsMsg")
	}
}

// TestUpdateAgentsInvMsgSuccessAndError drives updateAgentsInvMsg directly: a
// success result lands the inventory and clears loading; an error result records
// the error; a subsequent success clears the stale error.
func TestUpdateAgentsInvMsgSuccessAndError(t *testing.T) {
	m := newAgentsInvModel(t, sampleAgents(), client.Capabilities{Agents: true})
	m.agentsInv = agentsInvState{view: agentsInvPanel, loading: true}

	mOK, handled := m.updateAgentsInvMsg(client.AgentsMsg{Agents: []client.Agent{{Name: "scout"}}})
	if !handled {
		t.Fatal("a success AgentsMsg should be handled")
	}
	m = mOK.(Model)
	if m.agentsInv.loading {
		t.Error("loading should clear on a success result")
	}
	if len(m.agentsInv.agents) != 1 || m.agentsInv.agents[0].Name != "scout" {
		t.Fatalf("agents = %#v, want one scout", m.agentsInv.agents)
	}

	mErr, _ := m.updateAgentsInvMsg(client.AgentsMsg{Err: errors.New("boom")})
	m = mErr.(Model)
	if m.agentsInv.err == nil {
		t.Fatal("an error AgentsMsg should record the error")
	}
	mOK2, _ := m.updateAgentsInvMsg(client.AgentsMsg{Agents: []client.Agent{{Name: "x"}}})
	m = mOK2.(Model)
	if m.agentsInv.err != nil {
		t.Errorf("a success result should clear the prior error, got %v", m.agentsInv.err)
	}
}

// TestAgentsInvEmptyStateNotEnabled asserts the panel distinguishes "agent
// definitions not enabled on this server" (caps.Agents false) from "enabled but
// none resolved" (Gap D).
func TestAgentsInvEmptyStateNotEnabled(t *testing.T) {
	disabled := agentsInvEmptyCopy(client.Capabilities{Agents: false})
	if !strings.Contains(disabled, "not enabled") {
		t.Errorf("disabled empty copy = %q, want 'not enabled' framing", disabled)
	}
	enabled := agentsInvEmptyCopy(client.Capabilities{Agents: true})
	if strings.Contains(enabled, "not enabled") {
		t.Errorf("enabled empty copy should not say 'not enabled', got %q", enabled)
	}
	if !strings.Contains(enabled, "no agent definitions resolved") {
		t.Errorf("enabled empty copy = %q, want 'no agent definitions resolved'", enabled)
	}
}

// TestAgentsInvPanelRendersBothEmptyStates renders the panel with an empty
// inventory under both caps, asserting the rendered body carries the matching
// empty-state copy (so the render path, not just the helper, is exercised).
func TestAgentsInvPanelRendersBothEmptyStates(t *testing.T) {
	th := theme.New("aztec", theme.AztecPalette())
	st := agentsInvState{view: agentsInvPanel}

	notEnabled := stripANSIstr(renderAgentsInvPanel(th, st, client.Capabilities{Agents: false}, defaultHelpKeys(), 100))
	if !strings.Contains(notEnabled, "not enabled") {
		t.Errorf("not-enabled render missing the disabled copy:\n%s", notEnabled)
	}
	enabledEmpty := stripANSIstr(renderAgentsInvPanel(th, st, client.Capabilities{Agents: true}, defaultHelpKeys(), 100))
	if !strings.Contains(enabledEmpty, "no agent definitions resolved") {
		t.Errorf("enabled-but-empty render missing the empty copy:\n%s", enabledEmpty)
	}
}

// TestAgentsInvPanelSanitizes locks the terminaltext.Sanitize wrappers: a def whose
// name/description/tools AND model/permission-mode (the CLAUDE.md-trust-class
// metadata) embed ANSI/OSC escapes must render with no raw ESC. Per repo memory
// the literal is an innocuous ANSI escape, never destructive.
func TestAgentsInvPanelSanitizes(t *testing.T) {
	th := theme.New("aztec", theme.AztecPalette())
	st := agentsInvState{view: agentsInvPanel, agents: []client.Agent{
		{
			Name:           "\x1b]0;pwned\x07evil",
			Description:    "\x1b[31mred\x1b[0m",
			Model:          "\x1b[32mgpt\x1b[0m",
			PermissionMode: "\x1b]1;x\x07plan",
			Tools:          []string{"\x1b[1mRead"},
		},
	}}
	out := stripANSIstr(renderAgentsInvPanel(th, st, client.Capabilities{Agents: true}, defaultHelpKeys(), 100))
	if strings.ContainsRune(out, 0x1b) {
		t.Errorf("raw ESC (0x1b) leaked into the rendered panel; terminaltext.Sanitize not applied:\n%q", out)
	}
	if !strings.Contains(out, "]0;pwnedevil") {
		t.Errorf("sanitized def name not rendered as inert text, got:\n%q", out)
	}
	// The model + permission-mode metadata must also survive as inert text: the ESC
	// byte is stripped, the remaining literal payload is kept (mirrors the name
	// assertion above — terminaltext.Sanitize drops 0x1b but keeps the inert remainder).
	if !strings.Contains(out, "model:[32mgpt[0m") {
		t.Errorf("sanitized model not rendered as inert text, got:\n%q", out)
	}
	if !strings.Contains(out, "perm:]1;xplan") {
		t.Errorf("sanitized permission mode not rendered as inert text, got:\n%q", out)
	}
}

// TestAgentsInvColorDoesNotAlterContent locks the colour-hint invariant (Gap A):
// a def's `color` tints only the NAME's foreground, never the row CONTENT — so
// the stripANSI'd panel is byte-identical with and without a colour, and an
// unknown/empty colour falls back cleanly. The supported-colour case must still
// emit a colour escape PRE-strip (proving the tint actually applied), but the
// POST-strip text must match the no-colour render exactly.
func TestAgentsInvColorDoesNotAlterContent(t *testing.T) {
	th := theme.New("aztec", theme.AztecPalette())
	mk := func(color string) string {
		st := agentsInvState{view: agentsInvPanel, agents: []client.Agent{
			{Name: "scout", Description: "explore", Model: "gpt-5", Color: color},
		}}
		return renderAgentsInvPanel(th, st, client.Capabilities{Agents: true}, defaultHelpKeys(), 100)
	}

	plain := mk("")          // no colour
	tinted := mk("cyan")     // a supported colour
	unknown := mk("chartre") // an unsupported colour → must behave like plain

	// stripANSI'd content is colour-invariant.
	if stripANSIstr(plain) != stripANSIstr(tinted) {
		t.Errorf("colour altered the stripANSI'd content:\nplain:  %q\ntinted: %q", stripANSIstr(plain), stripANSIstr(tinted))
	}
	if stripANSIstr(plain) != stripANSIstr(unknown) {
		t.Errorf("an unknown colour altered the stripANSI'd content:\nplain:   %q\nunknown: %q", stripANSIstr(plain), stripANSIstr(unknown))
	}

	// The supported colour must actually have tinted the name: agentNameStyle with
	// "cyan" yields a different style than the base, so the rendered (pre-strip)
	// output differs from the plain render. (Guards against the tint silently
	// no-op'ing, which would make the invariant above trivially true.)
	if tinted == plain {
		t.Error("a supported colour produced byte-identical output to no colour — the tint did not apply")
	}

	// agentNameStyle itself: a supported colour sets a foreground; empty/unknown
	// returns the base style unchanged.
	if agentNameStyle(th, "cyan").GetForeground() == th.Style("toolName").GetForeground() {
		t.Error("agentNameStyle(cyan) should set a foreground distinct from the base toolName style")
	}
	if agentNameStyle(th, "").GetForeground() != th.Style("toolName").GetForeground() {
		t.Error("agentNameStyle(empty) should fall back to the base toolName foreground")
	}
	if agentNameStyle(th, "nope").GetForeground() != th.Style("toolName").GetForeground() {
		t.Error("agentNameStyle(unknown) should fall back to the base toolName foreground")
	}
}

// --- goldens ---------------------------------------------------------------

// agentsInvGoldenAgents is the representative def set for the inventory goldens:
// a fully-specified def (model + perm + tools) and a minimal one (description
// only), so the golden locks both the full metadata line and the omit-empty path.
func agentsInvGoldenAgents() *fakeAgents {
	return &fakeAgents{
		agents: []client.Agent{
			{Name: "scout", Description: "explore the codebase and report findings", Model: "gpt-5", PermissionMode: "plan", Tools: []string{"Read", "Grep", "Glob"}},
			{Name: "writer", Description: "draft prose from the scout's notes"},
		},
	}
}

// TestAgentsInvPanelGolden locks the populated inventory panel.
func TestAgentsInvPanelGolden(t *testing.T) {
	m := newAgentsInvModel(t, agentsInvGoldenAgents(), client.Capabilities{Agents: true})
	mm, cmd := m.runAgentsInv()
	m = feedCmd(t, mm.(Model), cmd)
	if m.agentsInv.view != agentsInvPanel {
		t.Fatalf("view = %v, want agentsInvPanel", m.agentsInv.view)
	}
	got := stripANSI([]byte(m.View().Content))
	compareGolden(t, "agents_inventory.golden", got)
}

// TestAgentsInvPanelEmptyNotEnabledGolden locks the "agent definitions not
// enabled" empty state (caps.Agents false).
func TestAgentsInvPanelEmptyNotEnabledGolden(t *testing.T) {
	m := newAgentsInvModel(t, &fakeAgents{}, client.Capabilities{Agents: false})
	mm, cmd := m.runAgentsInv()
	m = feedCmd(t, mm.(Model), cmd)
	got := stripANSI([]byte(m.View().Content))
	compareGolden(t, "agents_inventory_empty_disabled.golden", got)
}

// TestAgentsInvPanelEmptyEnabledGolden locks the "enabled but none resolved"
// empty state (caps.Agents true, empty inventory).
func TestAgentsInvPanelEmptyEnabledGolden(t *testing.T) {
	m := newAgentsInvModel(t, &fakeAgents{}, client.Capabilities{Agents: true})
	mm, cmd := m.runAgentsInv()
	m = feedCmd(t, mm.(Model), cmd)
	got := stripANSI([]byte(m.View().Content))
	compareGolden(t, "agents_inventory_empty_enabled.golden", got)
}

// TestAgentsInvKeySwallowsNonEsc asserts a non-esc, non-scroll key while the
// panel is open is swallowed (handled=true) so it never leaks into idle input —
// and that a scroll key is HANDLED (not a close, not a leak) now that the panel
// scrolls.
func TestAgentsInvKeySwallowsNonEsc(t *testing.T) {
	m := newAgentsInvModel(t, sampleAgents(), client.Capabilities{Agents: true})
	mm, cmd := m.runAgentsInv()
	m = feedCmd(t, mm.(Model), cmd)

	mm2, _, handled := m.onAgentsInvKey(tea.KeyPressMsg{Code: 'j'})
	if !handled {
		t.Error("a non-esc key while the panel is open should be swallowed (handled=true)")
	}
	if mm2.(Model).agentsInv.scroll != 0 {
		t.Error("a non-scroll key should not move the scroll offset")
	}

	// A scroll key is handled too (and keeps the panel open). The 2-def sample
	// fits the window, so the offset stays clamped at 0.
	mm3, _, handled := m.onAgentsInvKey(tea.KeyPressMsg{Code: tea.KeyPgDown})
	if !handled {
		t.Error("pgdown while the panel is open should be handled")
	}
	m3 := mm3.(Model)
	if m3.agentsInv.view != agentsInvPanel {
		t.Error("pgdown should not close the panel")
	}
	if m3.agentsInv.scroll != 0 {
		t.Errorf("a fitting inventory should clamp scroll at 0, got %d", m3.agentsInv.scroll)
	}
}

// scrollAgents returns n metadata-less defs ("agent-00".."agent-NN") — one
// rendered row each, UNIQUE so a render bug that ignored st.scroll (always
// showing the first window) would be caught (the TestSoulScroll fixture rationale).
func scrollAgents(n int) *fakeAgents {
	fa := &fakeAgents{}
	for i := 0; i < n; i++ {
		fa.agents = append(fa.agents, client.Agent{Name: fmt.Sprintf("agent-%02d", i)})
	}
	return fa
}

// TestAgentsInvScroll asserts the scroll keys move (and clamp) the inventory row
// window AND that the rendered window content + the "lines X–Y of N" indicator
// actually shift (mirrors TestSoulScroll/TestSkillsScroll). It also locks the
// scroll reset on a fresh inventory result and that esc still closes the
// scrolled panel.
func TestAgentsInvScroll(t *testing.T) {
	// 30 one-row defs exceed agentsInvBodyLines (14), each window distinguishable.
	m := newAgentsInvModel(t, scrollAgents(30), client.Capabilities{Agents: true})
	mm, cmd := m.runAgentsInv()
	m = feedCmd(t, mm.(Model), cmd)

	if m.agentsInv.scroll != 0 {
		t.Fatalf("initial scroll = %d, want 0", m.agentsInv.scroll)
	}
	// At the top: window is rows 1–14 (agent-00..agent-13); the tail is NOT visible.
	top := stripANSIstr(m.View().Content)
	if !strings.Contains(top, "agent-00") || strings.Contains(top, "agent-29") {
		t.Errorf("top window should show agent-00 and NOT agent-29, got:\n%s", top)
	}
	if !strings.Contains(top, "lines 1–14 of 30") {
		t.Errorf("top indicator should read 'lines 1–14 of 30', got:\n%s", top)
	}

	// Page down once: agent-00 leaves the top, agent-14 enters.
	mm, _, _ = m.onAgentsInvKey(tea.KeyPressMsg{Code: tea.KeyPgDown})
	m = mm.(Model)
	if m.agentsInv.scroll != 1 {
		t.Errorf("scroll after pgdown = %d, want 1", m.agentsInv.scroll)
	}
	pd := stripANSIstr(m.View().Content)
	if strings.Contains(pd, "agent-00") {
		t.Errorf("after pgdown the window should no longer show agent-00, got:\n%s", pd)
	}
	if !strings.Contains(pd, "agent-14") {
		t.Errorf("after pgdown the window should reveal agent-14, got:\n%s", pd)
	}
	if !strings.Contains(pd, "lines 2–15 of 30") {
		t.Errorf("after pgdown the indicator should read 'lines 2–15 of 30', got:\n%s", pd)
	}

	// Jump to bottom; max scroll = 30 - 14 = 16: the tail becomes visible.
	mm, _, _ = m.onAgentsInvKey(tea.KeyPressMsg{Code: tea.KeyEnd})
	m = mm.(Model)
	if m.agentsInv.scroll != 16 {
		t.Errorf("scroll after End = %d, want 16 (30-14)", m.agentsInv.scroll)
	}
	bot := stripANSIstr(m.View().Content)
	if !strings.Contains(bot, "agent-29") || strings.Contains(bot, "agent-00") {
		t.Errorf("bottom window should show agent-29 and NOT agent-00, got:\n%s", bot)
	}
	if !strings.Contains(bot, "lines 17–30 of 30") {
		t.Errorf("bottom indicator should read 'lines 17–30 of 30', got:\n%s", bot)
	}

	// Pgdown past the end clamps.
	mm, _, _ = m.onAgentsInvKey(tea.KeyPressMsg{Code: tea.KeyPgDown})
	m = mm.(Model)
	if m.agentsInv.scroll != 16 {
		t.Errorf("scroll clamps at 16, got %d", m.agentsInv.scroll)
	}

	// Page up moves back (pins the ScrollU arm — removing it must fail here).
	mm, _, _ = m.onAgentsInvKey(tea.KeyPressMsg{Code: tea.KeyPgUp})
	m = mm.(Model)
	if m.agentsInv.scroll != 15 {
		t.Errorf("scroll after pgup = %d, want 15", m.agentsInv.scroll)
	}

	// Home returns to the top.
	mm, _, _ = m.onAgentsInvKey(tea.KeyPressMsg{Code: tea.KeyHome})
	m = mm.(Model)
	if m.agentsInv.scroll != 0 {
		t.Errorf("scroll after Home = %d, want 0", m.agentsInv.scroll)
	}

	// A fresh inventory result resets a stale offset (never opens mid-list).
	mm, _, _ = m.onAgentsInvKey(tea.KeyPressMsg{Code: tea.KeyEnd})
	m = mm.(Model)
	mFresh, _ := m.updateAgentsInvMsg(client.AgentsMsg{Agents: scrollAgents(30).agents})
	m = mFresh.(Model)
	if m.agentsInv.scroll != 0 {
		t.Errorf("a fresh AgentsMsg should reset scroll to 0, got %d", m.agentsInv.scroll)
	}

	// esc still closes the scrolled panel.
	mm, _, _ = m.onAgentsInvKey(tea.KeyPressMsg{Code: tea.KeyEsc})
	if mm.(Model).agentsInv.view != agentsInvNone {
		t.Error("esc should still close the scrolled panel")
	}
}

// TestAgentsInvPanelScrollGolden locks the scrolled, overflowing inventory
// panel: an inventory exceeding agentsInvBodyLines, paged down once, so the
// golden carries the windowed rows + the "lines X–Y of N" indicator + the scroll
// footer hint (the TestSoulPanelGolden pattern; the fixed window keeps it
// deterministic).
func TestAgentsInvPanelScrollGolden(t *testing.T) {
	m := newAgentsInvModel(t, scrollAgents(30), client.Capabilities{Agents: true})
	mm, cmd := m.runAgentsInv()
	m = feedCmd(t, mm.(Model), cmd)
	if m.agentsInv.view != agentsInvPanel {
		t.Fatalf("view = %v, want agentsInvPanel", m.agentsInv.view)
	}
	mm, _, _ = m.onAgentsInvKey(tea.KeyPressMsg{Code: tea.KeyPgDown})
	m = mm.(Model)
	got := stripANSI([]byte(m.View().Content))
	compareGolden(t, "agents_inventory_scroll.golden", got)
}
