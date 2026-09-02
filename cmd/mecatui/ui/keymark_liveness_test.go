package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/theme"
)

// overrideAll rebinds EVERY rebindable action to a distinct sentinel chord so a
// liveness test can assert each rendered affordance carries its override and NOT
// the default. It bypasses the validator (applyKeyOverrides directly), so the
// sentinels only need to be distinct, not validator-legal. The approval keys
// (Allow/AllowAlways/Deny) use bare runes so the bracketed-mnemonic and
// approvalMnemonic paths render them legibly.
func overrideAll() map[string][]string {
	return map[string][]string{
		"Submit":           {"ctrl+f1"},
		"Newline":          {"ctrl+f2"},
		"Paste":            {"ctrl+f3"},
		"SelectAll":        {"ctrl+f31"},
		"CopySelection":    {"ctrl+f32"},
		"Cancel":           {"ctrl+f4"},
		"ClearPrompt":      {"ctrl+f33"},
		"Effort":           {"ctrl+f5"},
		"MCPPanel":         {"ctrl+f6"},
		"Resources":        {"ctrl+f7"},
		"Prompts":          {"ctrl+f8"},
		"Agents":           {"ctrl+f9"},
		"ModeSwitch":       {"ctrl+f10"},
		"ExpandTools":      {"ctrl+f11"},
		"Help":             {"ctrl+f12"},
		"Quit":             {"ctrl+f13"},
		"ScrollU":          {"ctrl+f14"},
		"ScrollD":          {"ctrl+f15"},
		"Close":            {"ctrl+f16"},
		"Allow":            {"y"},
		"AllowAlways":      {"q"},
		"Deny":             {"n"},
		"Choose":           {"ctrl+f17"},
		"NextTab":          {"ctrl+f18"},
		"JumpTop":          {"ctrl+f19"},
		"JumpEnd":          {"ctrl+f20"},
		"CancelChild":      {"ctrl+f21"},
		"Tasks":            {"ctrl+f22"},
		"Findings":         {"ctrl+f23"},
		"Refresh":          {"ctrl+f24"},
		"SetGlobalDefault": {"ctrl+f25"},
		"Up":               {"ctrl+f26"},
		"Down":             {"ctrl+f27"},
		"ScrollTop":        {"ctrl+f28"},
		"ScrollBottom":     {"ctrl+f29"},
		"EditBack":         {"ctrl+f30"},
	}
}

// remderRenderer builds a renderer seeded with the FULL override set's live
// markings, mirroring how New() threads keyMarkings(keys) into newRenderer — the
// seam TestHelpReflectsKeyOverride exercises for the help body, extended here to
// the inline-card affordances (issue #457).
func remderRenderer() *renderer {
	r := newRenderer(theme.New("aztec", theme.AztecPalette()), keyMarkings(applyKeyOverrides(defaultKeys(), overrideAll())))
	r.setWidth(100)
	return r
}

// TestInlineCardsReflectKeyOverride proves the inline-card affordances that
// reference rebindable chords read the LIVE markings (issue #457): the
// reasoning header, the subagent live line, the team header, the team "+N more"
// roll-up, and the collapse/arg-rollup markers must each carry the overridden
// ExpandTools/Agents chord and NOT the default. Each subtest builds the card via
// the same renderer path the model uses (remderRenderer) so the seam is real.
func TestInlineCardsReflectKeyOverride(t *testing.T) {
	const (
		wantExpand = "ctrl+f11" // overridden ExpandTools
		wantAgents = "ctrl+f9"  // overridden Agents
	)
	t.Run("reasoning collapsed/expanded header", func(t *testing.T) {
		r := remderRenderer()
		b := block{reasoning: "line one\nline two"}
		collapsed := stripANSIstr(r.renderReasoning(&b, false))
		if !strings.Contains(collapsed, wantExpand+" expand") {
			t.Errorf("collapsed reasoning header should carry %q, got %q", wantExpand+" expand", collapsed)
		}
		if strings.Contains(collapsed, "ctrl+t") {
			t.Errorf("collapsed reasoning header still shows the default ctrl+t: %q", collapsed)
		}
		expanded := stripANSIstr(r.renderReasoning(&b, true))
		if !strings.Contains(expanded, wantExpand+" collapse") {
			t.Errorf("expanded reasoning header should carry %q, got %q", wantExpand+" collapse", expanded)
		}
		if strings.Contains(expanded, "ctrl+t") {
			t.Errorf("expanded reasoning header still shows the default ctrl+t: %q", expanded)
		}
	})

	t.Run("subagent live line trace affordance", func(t *testing.T) {
		r := remderRenderer()
		b := block{subCurrent: "Grep", subToolCount: 2,
			subUsage: client.Usage{InputTokens: 100, OutputTokens: 20}}
		line := r.subagentLiveLine(&b)
		if !strings.Contains(line, wantExpand+" trace") {
			t.Errorf("subagent live line should carry %q trace, got %q", wantExpand, line)
		}
		if strings.Contains(line, "ctrl+t") {
			t.Errorf("subagent live line still shows the default ctrl+t: %q", line)
		}
	})

	t.Run("team header trace/collapse affordance", func(t *testing.T) {
		r := remderRenderer()
		b := block{teamLanes: []teamLane{{name: "lead", lead: true}, {name: "scout"}}}
		collapsed := r.teamHeader(&b, false)
		if !strings.Contains(collapsed, wantExpand+" trace") {
			t.Errorf("collapsed team header should carry %q trace, got %q", wantExpand, collapsed)
		}
		if strings.Contains(collapsed, "ctrl+t") {
			t.Errorf("collapsed team header still shows the default ctrl+t: %q", collapsed)
		}
		expanded := r.teamHeader(&b, true)
		if !strings.Contains(expanded, wantExpand+" collapse") {
			t.Errorf("expanded team header should carry %q collapse, got %q", wantExpand, expanded)
		}
	})

	t.Run("team roll-up advertises live agents chord", func(t *testing.T) {
		// A team exceeding maxTeamLanes renders a "+N more · <agents>" roll-up.
		r := remderRenderer()
		c := &conversation{}
		c.addTool("t1", "Team", `{"goal":"ship"}`)
		var big []client.TeamMemberSpec
		big = append(big, client.TeamMemberSpec{Name: "lead", Lead: true})
		for i := 0; i < maxTeamLanes+2; i++ {
			big = append(big, client.TeamMemberSpec{Name: "m" + string(rune('a'+i))})
		}
		c.setTeamStart("t1", "", big)
		out := stripANSIstr(r.renderBlock(0, &c.blocks[0], false))
		if !strings.Contains(out, "more · "+wantAgents) {
			t.Errorf("team roll-up should carry the live agents chord %q, got %q", wantAgents, out)
		}
		if strings.Contains(out, "ctrl+a") {
			t.Errorf("team roll-up still shows the default ctrl+a: %q", out)
		}
	})

	t.Run("collapse marker carries live expand chord", func(t *testing.T) {
		r := remderRenderer()
		got := r.collapseMarker(3)
		if !strings.Contains(got, wantExpand+" expand") {
			t.Errorf("collapseMarker should carry %q expand, got %q", wantExpand, got)
		}
		if strings.Contains(got, "ctrl+t") {
			t.Errorf("collapseMarker still shows the default ctrl+t: %q", got)
		}
	})

	t.Run("arg rollup marker carries live expand chord", func(t *testing.T) {
		r := remderRenderer()
		withCount := r.argRollupMarker(2)
		if !strings.Contains(withCount, wantExpand+" expand") {
			t.Errorf("argRollupMarker(2) should carry %q expand, got %q", wantExpand, withCount)
		}
		zero := r.argRollupMarker(0)
		if !strings.Contains(zero, wantExpand+" expand") {
			t.Errorf("argRollupMarker(0) should carry %q expand, got %q", wantExpand, zero)
		}
		if strings.Contains(zero, "ctrl+t") {
			t.Errorf("argRollupMarker still shows the default ctrl+t: %q", zero)
		}
	})

	t.Run("diff collapse marker carries live expand chord", func(t *testing.T) {
		r := remderRenderer()
		var sb strings.Builder
		for i := 0; i < 30; i++ {
			sb.WriteString("line\n")
		}
		args := `{"path":"big.txt","content":` + jsonQuote(sb.String()) + `}`
		out, ok := r.renderToolDiff("Write", args, false)
		if !ok {
			t.Fatal("Write diff should render")
		}
		plain := stripANSIstr(out)
		if !strings.Contains(plain, wantExpand+" expand") {
			t.Errorf("collapsed diff should carry %q expand, got %q", wantExpand, plain)
		}
		if strings.Contains(plain, "ctrl+t") {
			t.Errorf("collapsed diff still shows the default ctrl+t: %q", plain)
		}
	})
}

// TestFooterReflectsKeyOverride proves the footer affordances read the LIVE
// markings (issue #457): the help line (help/quit/submit/cancel), the plan-approval
// mnemonics (allow/allowAlways/deny), the quitArmed cue, the fatal-screen cue, and
// the team/subagent/parallel agents-overlay advertisements must each carry the
// overridden chord and NOT the default.
func TestFooterReflectsKeyOverride(t *testing.T) {
	km := applyKeyOverrides(defaultKeys(), overrideAll())
	m, _, _ := newTestModel(t, theme.New("aztec", theme.AztecPalette()))
	m.keys = km
	m.rend = newRenderer(m.deps.Theme, keyMarkings(km))
	m = applyAll(m,
		tea.WindowSizeMsg{Width: 120, Height: 30},
		client.SessionReadyMsg{SessionID: "sess-test-0001", Capabilities: allOnCaps()},
	)

	t.Run("idle help line", func(t *testing.T) {
		m.phase = phaseIdle
		got := stripANSIstr(m.renderFooter())
		// help line: "<help> help · / commands · <quit> quit"
		if !strings.Contains(got, "ctrl+f31 select all") {
			t.Errorf("footer help line should carry the overridden SelectAll chord ctrl+f31: %q", got)
		}
		if !strings.Contains(got, "ctrl+f32 copy") {
			t.Errorf("footer help line should carry the overridden CopySelection chord ctrl+f32: %q", got)
		}
		if strings.Contains(got, "ctrl+g select all") || strings.Contains(got, "ctrl+shift+c copy") {
			t.Errorf("footer help line still shows a default selection chord: %q", got)
		}
		if !strings.Contains(got, "ctrl+f12 help") {
			t.Errorf("footer help line should carry the overridden help chord ctrl+f12: %q", got)
		}
		if !strings.Contains(got, "ctrl+f13 quit") {
			t.Errorf("footer help line should carry the overridden quit chord ctrl+f13: %q", got)
		}
		if strings.Contains(got, "? help") || strings.Contains(got, "ctrl+c quit") {
			t.Errorf("footer help line still shows a default chord: %q", got)
		}
	})

	t.Run("running queue/cancel affordances", func(t *testing.T) {
		m.phase = phaseRunning
		got := stripANSIstr(m.renderFooter())
		if !strings.Contains(got, "ctrl+f1 queue") {
			t.Errorf("running footer should carry the overridden submit ctrl+f1: %q", got)
		}
		if !strings.Contains(got, "ctrl+f33 clear") || !strings.Contains(got, "ctrl+f4 cancel") {
			t.Errorf("running footer should carry the overridden clear/cancel chords: %q", got)
		}
		if strings.Contains(got, "enter queue") || strings.Contains(got, "esc cancel") || strings.Contains(got, "ctrl+u clear") {
			t.Errorf("running footer still shows a default chord: %q", got)
		}
	})

	t.Run("plan-approval mnemonics", func(t *testing.T) {
		m.phase = phaseAwaitingApproval
		openApprovalSurface(&m).ask = pendingAsk{Tool: "PresentPlan", offerAlways: true, Args: `{"plan":"x"}`}
		got := stripANSIstr(m.renderFooter())
		// approvalMnemonic upper-cases the bare rune: y→Y, q→Q, n→N.
		if !strings.Contains(got, "Y approve & run") {
			t.Errorf("plan-approval footer should carry the overridden allow mnemonic Y: %q", got)
		}
		if !strings.Contains(got, "Q auto-accept") {
			t.Errorf("plan-approval footer should carry the overridden allowAlways mnemonic Q: %q", got)
		}
		if !strings.Contains(got, "N iterate") {
			t.Errorf("plan-approval footer should carry the overridden deny mnemonic N: %q", got)
		}
		if strings.Contains(got, "A approve") || strings.Contains(got, "W auto-accept") || strings.Contains(got, "D iterate") {
			t.Errorf("plan-approval footer still shows a default mnemonic: %q", got)
		}
	})

	t.Run("quitArmed cue", func(t *testing.T) {
		m.phase = phaseIdle
		m.quitArmed = true
		got := stripANSIstr(m.renderFooter())
		if !strings.Contains(got, "ctrl+f13 again to quit") {
			t.Errorf("quitArmed cue should carry the overridden quit chord ctrl+f13: %q", got)
		}
		if strings.Contains(got, "ctrl+c again") {
			t.Errorf("quitArmed cue still shows the default ctrl+c: %q", got)
		}
		m.quitArmed = false
	})

	t.Run("quitArmed statusMsg carries the live override (not the stale default)", func(t *testing.T) {
		// The arm path (onQuitKey) sets m.statusMsg LIVE from m.keys.Quit, so the
		// footer-left hint advertises the rebound chord and not the frozen
		// "ctrl+c" default (issue #457 SPEC gap: quitHint was a constant). Drive
		// the real arm reducer and assert the footer-left (idleFooterLeft →
		// statusMsg) and the footer-help cue BOTH carry the override.
		mm := m
		mm.phase = phaseIdle
		mm.quitArmed = false
		mm.statusMsg = ""
		mm, _ = pressKey(mm, tea.KeyPressMsg{Code: tea.KeyF13, Mod: tea.ModCtrl})
		if !mm.quitArmed {
			t.Fatal("overridden ctrl+f13 should arm the quit guard")
		}
		want := "press ctrl+f13 again to quit"
		if mm.statusMsg != want {
			t.Errorf("statusMsg should carry the live quit hint %q, got %q", want, mm.statusMsg)
		}
		if strings.Contains(mm.statusMsg, "ctrl+c") {
			t.Errorf("statusMsg still advertises the stale default ctrl+c: %q", mm.statusMsg)
		}
		got := stripANSIstr(mm.renderFooter())
		if !strings.Contains(got, want) {
			t.Errorf("armed footer-left should show the live override %q: %q", want, got)
		}
		if strings.Contains(got, "press ctrl+c again to quit") {
			t.Errorf("armed footer still shows the stale default ctrl+c hint: %q", got)
		}
	})

	t.Run("fatal screen cue", func(t *testing.T) {
		m.phase = phaseFatal
		m.fatalErr = "boom"
		got := stripANSIstr(m.renderFatal())
		if !strings.Contains(got, "press ctrl+f13 to quit") {
			t.Errorf("fatal screen should carry the overridden quit chord ctrl+f13: %q", got)
		}
		if strings.Contains(got, "press ctrl+c") {
			t.Errorf("fatal screen still shows the default ctrl+c: %q", got)
		}
	})

	t.Run("team/subagent agents advertisement", func(t *testing.T) {
		// Build a live team block + subagent fleet so fitFooter renders the
		// agents prefix tiers that advertise the overlay chord.
		m.phase = phaseIdle
		m.conv = conversation{}
		m.conv.addTool("t1", "Team", `{"goal":"ship"}`)
		m.conv.setTeamStart("t1", "team-abc", roster())
		m.conv.addTeamMember(member("lead", "tool.call", client.TeamMsg{ToolName: "Edit"}))
		m.conv.setSubagentStart("p1", "scout", "", "", "", "")
		m.conv.addSubagentTool(client.SubagentMsg{Kind: client.SubagentTool, ParentCallID: "p1", ToolName: "Grep", ToolCount: 1})
		m.refreshView()
		got := stripANSIstr(m.fitFooter("ready", 160))
		if !strings.Contains(got, "ctrl+f9") {
			t.Errorf("footer agents prefix should carry the overridden agents chord ctrl+f9: %q", got)
		}
		if strings.Contains(got, "ctrl+a") {
			t.Errorf("footer agents prefix still shows the default ctrl+a: %q", got)
		}
	})
}

// TestPlanReviewActionBarReflectsKeyOverride proves the plan-review action bar
// reads the LIVE approval and scroll chords (issue #457): the bracketed
// mnemonics and ScrollU/ScrollD/ScrollTop/ScrollBottom hint must carry the
// overrides and NOT the defaults. The GENERIC permission modal's buttons are
// covered by TestPermissionModalButtonsReflectKeyOverride (they degrade to an
// honest standalone form under an override; the default word-embedded form is
// byte-identical via TestPermissionModalButtonsDefaultBytesUnchanged).
func TestPlanReviewActionBarReflectsKeyOverride(t *testing.T) {
	km := applyKeyOverrides(defaultKeys(), overrideAll())
	m, _, _ := newTestModel(t, theme.New("aztec", theme.AztecPalette()))
	m.keys = km
	m.rend = newRenderer(m.deps.Theme, keyMarkings(km))
	m = applyAll(m,
		tea.WindowSizeMsg{Width: 120, Height: 30},
		client.SessionReadyMsg{SessionID: "sess-test-0001", Capabilities: allOnCaps()},
	)
	m.phase = phaseAwaitingApproval
	openApprovalSurface(&m).ask = pendingAsk{Tool: "PresentPlan", offerAlways: true, Args: `{"plan":"do the thing"}`}
	_ = m.View()
	got := stripANSIstr(m.View().Content)
	// A bare-rune override degrades to the honest standalone form ("[Y] approve
	// & run"), not the wordplay stem ("[Y]pprove & run") — approvalMnemonic
	// upper-cases the bare rune: y→Y, q→Q, n→N.
	for _, want := range []string{"[Y] approve & run", "[Q] auto-accept edits", "[N] iterate"} {
		if !strings.Contains(got, want) {
			t.Errorf("plan-review button should carry the overridden standalone form %q: %q", want, got)
		}
	}
	if strings.Contains(got, "[A]pprove") || strings.Contains(got, "[W] auto-accept") || strings.Contains(got, "[D] iterate") {
		t.Errorf("plan-review buttons still show a default mnemonic: %q", got)
	}
	// The wordplay stem is never glued onto a rebound chord.
	if strings.Contains(got, "]pprove") || strings.Contains(got, "Y]pprove") {
		t.Errorf("plan-review button glues a rebound chord onto the word stem: %q", got)
	}
	if !strings.Contains(got, "scroll: ↑/↓ · ctrl+f14/ctrl+f15 · ctrl+f28/ctrl+f29 · mouse wheel") {
		t.Errorf("plan-review scroll hint should carry live scroll/jump chords: %q", got)
	}
	if strings.Contains(got, "pgup/pgdn") || strings.Contains(got, "home/end") {
		t.Errorf("plan-review scroll hint still shows defaults: %q", got)
	}
}

// TestPlanReviewActionBarModifiedChordDegrades pins the #457 follow-up fix: a
// MODIFIED approval chord (e.g. Allow: ctrl+y — legal because the approval keys
// are not in the validator's globalOpen bare-rune set) must degrade the
// plan-review action-bar buttons to the honest standalone form
// ("[ctrl+y] approve & run"), never glue the modified chord onto the word's
// stem ("[ctrl+y]pprove & run").
func TestPlanReviewActionBarModifiedChordDegrades(t *testing.T) {
	km := applyKeyOverrides(defaultKeys(), map[string][]string{
		"Allow": {"ctrl+y"}, "AllowAlways": {"ctrl+q"}, "Deny": {"ctrl+n"},
	})
	m, _, _ := newTestModel(t, theme.New("aztec", theme.AztecPalette()))
	m.keys = km
	m.rend = newRenderer(m.deps.Theme, keyMarkings(km))
	m = applyAll(m,
		tea.WindowSizeMsg{Width: 160, Height: 30},
		client.SessionReadyMsg{SessionID: "sess-test-0001", Capabilities: allOnCaps()},
	)
	m.phase = phaseAwaitingApproval
	openApprovalSurface(&m).ask = pendingAsk{Tool: "PresentPlan", offerAlways: true, Args: `{"plan":"do the thing"}`}
	_ = m.View()
	got := stripANSIstr(m.View().Content)
	for _, want := range []string{"[ctrl+y] approve & run", "[ctrl+q] auto-accept edits", "[ctrl+n] iterate"} {
		if !strings.Contains(got, want) {
			t.Errorf("plan-review button should carry the standalone modified-chord form %q: %q", want, got)
		}
	}
	if strings.Contains(got, "]pprove") {
		t.Errorf("plan-review button glues a modified chord onto the word stem: %q", got)
	}
}

// TestPlanReviewActionBarDefaultBytesUnchanged is the byte-identical guard for
// the DEFAULT approval chords: with no overrides the plan-review action bar
// renders the historical word-embedded form ("[A]pprove & run" / "[W]
// auto-accept edits" / "[D] iterate") so the goldens and pre-#457 output stay
// byte-for-byte.
func TestPlanReviewActionBarDefaultBytesUnchanged(t *testing.T) {
	m, _, _ := newTestModel(t, theme.New("aztec", theme.AztecPalette()))
	m = applyAll(m,
		tea.WindowSizeMsg{Width: 120, Height: 30},
		client.SessionReadyMsg{SessionID: "sess-test-0001", Capabilities: allOnCaps()},
	)
	m.phase = phaseAwaitingApproval
	openApprovalSurface(&m).ask = pendingAsk{Tool: "PresentPlan", offerAlways: true, Args: `{"plan":"do the thing"}`}
	_ = m.View()
	got := stripANSIstr(m.View().Content)
	for _, want := range []string{"[A]pprove & run", "[W] auto-accept edits", "[D] iterate"} {
		if !strings.Contains(got, want) {
			t.Errorf("plan-review default button should render %q byte-for-byte: %q", want, got)
		}
	}
}

// TestPermissionModalButtonsReflectKeyOverride proves the GENERIC permission
// modal's button labels are honest about the LIVE approval chords (issue #457
// SPEC/UX gap: they were hard-coded to the word-embedded form). With the DEFAULT
// a/w/d chords the historical word-embedded form ("[A]llow"/"Al[w]ays"/
// "[D]eny" + the "al[w]ays allows …" footnote) renders byte-for-byte; under an
// override the buttons degrade to an honest standalone form
// ("[Y] allow"/"[Q] always allow"/"[N] deny") carrying the rebound chord and
// NOT the default. The footer plan-approval line and the plan-review action bar
// (standalone mnemonics) are covered by their own tests.
func TestPermissionModalButtonsReflectKeyOverride(t *testing.T) {
	km := applyKeyOverrides(defaultKeys(), overrideAll())
	r := newRenderer(theme.New("aztec", theme.AztecPalette()), keyMarkings(km))
	r.setWidth(100)
	ask := pendingAsk{Tool: "Edit", offerAlways: true, Args: `{"path":"a","old_string":"x","new_string":"y"}`}
	got := stripANSIstr(renderApprovalModalWithRenderer(r, ask, false, 100, 30))
	for _, want := range []string{"[Y] allow", "[Q] always allow", "[N] deny"} {
		if !strings.Contains(got, want) {
			t.Errorf("modal buttons should carry the overridden standalone form %q: %q", want, got)
		}
	}
	for _, stale := range []string{"[A]llow", "Al[w]ays", "[D]eny", "al[w]ays allows"} {
		if strings.Contains(got, stale) {
			t.Errorf("modal buttons still show the default word-embedded form %q: %q", stale, got)
		}
	}
	// The always-allow footnote names the live chord honestly.
	if !strings.Contains(got, "always (q) allows") {
		t.Errorf("always-allow footnote should name the overridden chord q: %q", got)
	}
}

// TestPermissionModalButtonsDefaultBytesUnchanged is the byte-identical guard
// for the DEFAULT approval chords: with no overrides the generic permission
// modal renders the historical word-embedded form ("[A]llow"/"Al[w]ays"/
// "[D]eny" + the "al[w]ays allows …" footnote) so the goldens and the pre-#457
// output stay byte-for-byte.
func TestPermissionModalButtonsDefaultBytesUnchanged(t *testing.T) {
	r := newRenderer(theme.New("aztec", theme.AztecPalette()), defaultHelpKeys())
	r.setWidth(100)
	ask := pendingAsk{Tool: "Edit", offerAlways: true, Args: `{"path":"a","old_string":"x","new_string":"y"}`}
	got := stripANSIstr(renderApprovalModalWithRenderer(r, ask, false, 100, 30))
	for _, want := range []string{"[A]llow", "Al[w]ays", "[D]eny", "al[w]ays allows this exact command for the rest of this session"} {
		if !strings.Contains(got, want) {
			t.Errorf("default modal buttons should stay the historical word-embedded form %q: %q", want, got)
		}
	}
}

// TestPermissionModalButtonsModifiedChordStandalone covers the modified-chord
// arm: an approval key rebound to a modified chord ("ctrl+y") renders the
// honest standalone form with the chord verbatim (approvalMnemonic leaves a
// modified chord unchanged), not a broken word-embedded wordplay.
func TestPermissionModalButtonsModifiedChordStandalone(t *testing.T) {
	km := applyKeyOverrides(defaultKeys(), map[string][]string{
		"Allow":       {"ctrl+y"},
		"AllowAlways": {"ctrl+q"},
		"Deny":        {"ctrl+n"},
	})
	r := newRenderer(theme.New("aztec", theme.AztecPalette()), keyMarkings(km))
	r.setWidth(100)
	ask := pendingAsk{Tool: "Edit", offerAlways: true, Args: `{"path":"a","old_string":"x","new_string":"y"}`}
	got := stripANSIstr(renderApprovalModalWithRenderer(r, ask, false, 100, 30))
	for _, want := range []string{"[ctrl+y] allow", "[ctrl+q] always allow", "[ctrl+n] deny"} {
		if !strings.Contains(got, want) {
			t.Errorf("modal buttons should carry the modified-chord standalone form %q: %q", want, got)
		}
	}
}

// TestApprovalMnemonic pins the footer approval-mnemonic helper: a bare lowercase
// rune is upper-cased (the historical "A"/"W"/"D" from "a"/"w"/"d"), while a
// modified or multi-rune chord is returned verbatim (upper-casing only the first
// letter of "ctrl+y" would mangle it).
func TestApprovalMnemonic(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"a", "A"},
		{"w", "W"},
		{"d", "D"},
		{"y", "Y"},
		{"n", "N"},
		{"ctrl+y", "ctrl+y"}, // modified chord stays verbatim
		{"f5", "f5"},         // special chord stays verbatim
		{"", ""},             // empty stays empty
	}
	for _, c := range cases {
		if got := approvalMnemonic(c.in); got != c.want {
			t.Errorf("approvalMnemonic(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestDefaultFooterHelp pins the default footer help affordances. The selection
// shortcuts follow help and slash commands, then quit; live-key tests separately
// prove these markings update when operators rebind them.
func TestDefaultFooterHelp(t *testing.T) {
	m, _, _ := newTestModel(t, theme.New("aztec", theme.AztecPalette()))
	m = applyAll(m,
		tea.WindowSizeMsg{Width: 120, Height: 30},
		client.SessionReadyMsg{SessionID: "sess-test-0001", Capabilities: allOnCaps()},
	)
	m.phase = phaseIdle
	got := stripANSIstr(m.renderFooter())
	if !strings.Contains(got, "? help · / commands · ctrl+g select all · ctrl+shift+c copy · ctrl+u clear · ctrl+c quit") {
		t.Errorf("default footer help line = %q", got)
	}

	m.phase = phaseAwaitingApproval
	openApprovalSurface(&m).ask = pendingAsk{Tool: "PresentPlan", offerAlways: true, Args: `{"plan":"x"}`}
	got = stripANSIstr(m.renderFooter())
	if !strings.Contains(got, "A approve & run · W auto-accept · D iterate") {
		t.Errorf("default plan-approval line should be the historical literal, got %q", got)
	}

	// Team footer advertisement.
	m.phase = phaseIdle
	m.conv = conversation{}
	m.conv.addTool("t1", "Team", `{"goal":"ship"}`)
	m.conv.setTeamStart("t1", "team-abc", roster())
	m.conv.addTeamMember(member("lead", "tool.call", client.TeamMsg{ToolName: "Edit"}))
	m.refreshView()
	got = stripANSIstr(m.fitFooter("ready", 160))
	if !strings.Contains(got, "ctrl+a agents") {
		t.Errorf("default team footer should carry the historical ctrl+a literal, got %q", got)
	}
}

// TestDefaultInlineCardsBytesUnchanged is the byte-identical guard for the default
// keymap on the inline-card affordances: with NO overrides the reasoning header,
// subagent live line, team header, roll-up, and collapse/arg-rollup markers must
// render EXACTLY the historical "ctrl+t" / "ctrl+a" literals.
func TestDefaultInlineCardsBytesUnchanged(t *testing.T) {
	r := newTestRenderer()
	// Reasoning header.
	b := block{reasoning: "line one\nline two"}
	if got := stripANSIstr(r.renderReasoning(&b, false)); !strings.Contains(got, "ctrl+t expand") {
		t.Errorf("default reasoning header should carry ctrl+t expand, got %q", got)
	}
	if got := stripANSIstr(r.renderReasoning(&b, true)); !strings.Contains(got, "ctrl+t collapse") {
		t.Errorf("default reasoning header should carry ctrl+t collapse, got %q", got)
	}
	// Subagent live line.
	sb := block{subCurrent: "Grep", subToolCount: 1}
	if got := r.subagentLiveLine(&sb); !strings.Contains(got, "ctrl+t trace") {
		t.Errorf("default subagent live line should carry ctrl+t trace, got %q", got)
	}
	// Team header.
	tb := block{teamLanes: []teamLane{{name: "lead", lead: true}}}
	if got := r.teamHeader(&tb, false); !strings.Contains(got, "ctrl+t trace") {
		t.Errorf("default team header should carry ctrl+t trace, got %q", got)
	}
	if got := r.teamHeader(&tb, true); !strings.Contains(got, "ctrl+t collapse") {
		t.Errorf("default team header should carry ctrl+t collapse, got %q", got)
	}
	// Collapse + arg rollup markers.
	if got := r.collapseMarker(1); !strings.Contains(got, "ctrl+t expand") {
		t.Errorf("default collapseMarker should carry ctrl+t expand, got %q", got)
	}
	if got := r.argRollupMarker(0); !strings.Contains(got, "ctrl+t expand") {
		t.Errorf("default argRollupMarker should carry ctrl+t expand, got %q", got)
	}
	// Team roll-up advertises ctrl+a.
	c := &conversation{}
	c.addTool("t1", "Team", `{"goal":"ship"}`)
	var big []client.TeamMemberSpec
	big = append(big, client.TeamMemberSpec{Name: "lead", Lead: true})
	for i := 0; i < maxTeamLanes+2; i++ {
		big = append(big, client.TeamMemberSpec{Name: "m" + string(rune('a'+i))})
	}
	c.setTeamStart("t1", "", big)
	if got := stripANSIstr(r.renderBlock(0, &c.blocks[0], false)); !strings.Contains(got, "more · ctrl+a") {
		t.Errorf("default team roll-up should carry ctrl+a, got %q", got)
	}
}

// liveHK is the full-override helpKeys, the shared fixture for the overlay
// liveness tests (issue #457): every rebindable overlay action is rebound to a
// distinct sentinel so a test can assert each overlay's footer hint carries its
// override and NOT the default.
func liveHK() helpKeys { return keyMarkings(applyKeyOverrides(defaultKeys(), overrideAll())) }

// TestAgentsOverlayHintsReflectKeyOverride proves the agents overlay's roster/focus
// footers read the LIVE Up/Down/Choose/closeOnly/nextTab/cancelChild/jumpTop/jumpEnd
// markings (issue #457): every displayed chord must carry the override and NOT the
// default. Each subtest drives the free render function with liveHK() so the seam is
// real.
func TestAgentsOverlayHintsReflectKeyOverride(t *testing.T) {
	hk := liveHK()
	th := theme.New("aztec", theme.AztecPalette())
	const (
		wantUp     = "ctrl+f26"
		wantDown   = "ctrl+f27"
		wantChoose = "ctrl+f17"
		wantClose  = "ctrl+f16"
		wantTab    = "ctrl+f18"
		wantCancel = "ctrl+f21"
		wantJump   = "ctrl+f19"
	)
	t.Run("subagent roster", func(t *testing.T) {
		fleet := []subagentLane{{childID: "c1", goal: "g"}, {childID: "c2", goal: "h"}}
		got := stripANSIstr(renderSubagentRoster(th, subagentState{}, fleet, hk, 0))
		for _, want := range []string{wantUp + "/" + wantDown, wantChoose + " focus", wantCancel + " cancel", wantTab + " switch", wantClose + " close", wantJump + "/" + "ctrl+f20"} {
			if !strings.Contains(got, want) {
				t.Errorf("subagent roster hint missing %q: %q", want, got)
			}
		}
		for _, stale := range []string{"↑/↓", "enter focus", "x cancel", "tab switch", "esc close", "home/end"} {
			if strings.Contains(got, stale) {
				t.Errorf("subagent roster hint still shows a default %q: %q", stale, got)
			}
		}
	})
	t.Run("subagent focus", func(t *testing.T) {
		fleet := []subagentLane{{childID: "c1", goal: "g"}}
		got := stripANSIstr(renderSubagentFocus(th, fleet, "c1", hk, 0, 0))
		if !strings.Contains(got, wantCancel+" cancel · "+wantClose+" back") {
			t.Errorf("subagent focus hint should carry live cancel+back: %q", got)
		}
		if strings.Contains(got, "x cancel · esc back") {
			t.Errorf("subagent focus hint still shows defaults: %q", got)
		}
	})
	t.Run("parallel roster", func(t *testing.T) {
		groups := []parallelGroup{{parentCallID: "p1", branches: []parallelBranch{{index: 0}}}}
		got := stripANSIstr(renderParallelRoster(th, parallelState{}, groups, hk, 0))
		// The jump pair uses the FULL joined keys (jumpTopFull · jumpEndFull).
		if !strings.Contains(got, "ctrl+f19·ctrl+f20 first/last") {
			t.Errorf("parallel roster hint missing live jump pair: %q", got)
		}
		if !strings.Contains(got, wantUp+"/"+wantDown) {
			t.Errorf("parallel roster hint missing live up/down %q: %q", wantUp+"/"+wantDown, got)
		}
		if strings.Contains(got, "↑/↓ select") || strings.Contains(got, "home/g·end/G") {
			t.Errorf("parallel roster hint still shows defaults: %q", got)
		}
	})
}

// TestTeamOverlayHintsReflectKeyOverride proves the team overlay's roster/tasks/findings
// footers read the LIVE markings (issue #457).
func TestTeamOverlayHintsReflectKeyOverride(t *testing.T) {
	hk := liveHK()
	th := theme.New("aztec", theme.AztecPalette())
	b := &block{teamLanes: []teamLane{{name: "lead", lead: true}, {name: "scout"}}}
	t.Run("roster", func(t *testing.T) {
		got := stripANSIstr(renderTeamRoster(th, teamState{}, b, hk, 0))
		for _, want := range []string{"ctrl+f26/ctrl+f27 select", "ctrl+f17 focus", "ctrl+f21 cancel", "ctrl+f22 tasks", "ctrl+f23 findings", "ctrl+f18 switch", "ctrl+f16 close"} {
			if !strings.Contains(got, want) {
				t.Errorf("team roster hint missing %q: %q", want, got)
			}
		}
		for _, stale := range []string{"↑/↓ select", "enter focus", "x cancel", "t tasks", "f findings", "tab switch", "esc close"} {
			if strings.Contains(got, stale) {
				t.Errorf("team roster hint still shows a default %q: %q", stale, got)
			}
		}
	})
	t.Run("tasks sub-view", func(t *testing.T) {
		got := stripANSIstr(renderTeamTasks(th, b, hk, 0))
		if !strings.Contains(got, "ctrl+f22 roster · ctrl+f16 close") {
			t.Errorf("tasks hint should carry live flip+close: %q", got)
		}
		if strings.Contains(got, "t roster · esc close") {
			t.Errorf("tasks hint still shows defaults: %q", got)
		}
	})
	t.Run("findings sub-view", func(t *testing.T) {
		got := stripANSIstr(renderTeamFindings(th, b, hk, 0))
		if !strings.Contains(got, "ctrl+f23 roster · ctrl+f16 close") {
			t.Errorf("findings hint should carry live flip+close: %q", got)
		}
	})
}

// TestMCPOverlayHintsReflectKeyOverride proves the MCP overlay's panel/resource/prompt
// footers read the LIVE Up/Down/Choose/Close/Refresh markings (issue #457).
func TestMCPOverlayHintsReflectKeyOverride(t *testing.T) {
	hk := liveHK()
	th := theme.New("aztec", theme.AztecPalette())
	caps := client.Capabilities{MCP: true}
	render := func(st mcpState) string {
		p := &st
		p.deps = surfaceDeps{theme: th, caps: caps, marks: hk}
		body, _ := p.Render(100, 30)
		return stripANSIstr(body)
	}
	t.Run("panel footer", func(t *testing.T) {
		got := render(mcpState{view: mcpPanel})
		if !strings.Contains(got, "ctrl+f24 refresh · ctrl+f16 close") {
			t.Errorf("panel footer should carry live refresh+close: %q", got)
		}
		if strings.Contains(got, "r refresh · esc close") {
			t.Errorf("panel footer still shows defaults: %q", got)
		}
	})
	t.Run("resources list", func(t *testing.T) {
		got := render(mcpState{view: mcpResources, resources: []client.MCPResource{{Name: "r1", Server: "s"}}})
		if !strings.Contains(got, "ctrl+f26/ctrl+f27 move · ctrl+f17 read · ctrl+f16 close") {
			t.Errorf("resources hint should carry live move/read/close: %q", got)
		}
		if strings.Contains(got, "↑/↓ move · enter read · esc close") {
			t.Errorf("resources hint still shows defaults: %q", got)
		}
	})
	t.Run("prompts list", func(t *testing.T) {
		got := render(mcpState{view: mcpPrompts, prompts: []client.MCPPrompt{{Name: "p1", Server: "s"}}})
		if !strings.Contains(got, "ctrl+f26/ctrl+f27 move · ctrl+f17 select · ctrl+f16 close") {
			t.Errorf("prompts hint should carry live move/select/close: %q", got)
		}
	})
	t.Run("prompt arguments", func(t *testing.T) {
		got := render(mcpState{view: mcpPromptArgs, argPrompt: client.MCPPrompt{Name: "p1"}})
		if !strings.Contains(got, "↑/↓ field · ctrl+f17 next/submit · ctrl+f16 back") {
			t.Errorf("prompt-args hint should keep fixed arrows and carry live choose/close: %q", got)
		}
		if strings.Contains(got, "enter next/submit") || strings.Contains(got, "esc back") {
			t.Errorf("prompt-args hint still shows default keyMap chords: %q", got)
		}
	})
}

// TestEffortOverlayHintsReflectKeyOverride proves the effort picker footer reads the
// LIVE Up/Down/Choose/Close markings (issue #457).
func TestEffortOverlayHintsReflectKeyOverride(t *testing.T) {
	hk := liveHK()
	th := theme.New("aztec", theme.AztecPalette())
	got := stripANSIstr(renderEffortOverlay(th, effortState{view: effortPanel}, "auto", false, hk, 100, 30))
	if !strings.Contains(got, "ctrl+f26/ctrl+f27 move · ctrl+f17 apply · ctrl+f16 close") {
		t.Errorf("effort hint should carry live move/apply/close: %q", got)
	}
	if strings.Contains(got, "↑/↓ move · enter apply · esc close") {
		t.Errorf("effort hint still shows defaults: %q", got)
	}
}

// TestModelsOverlayHintsReflectKeyOverride proves the models picker footer reads the
// LIVE ScrollU/Choose/SetGlobalDefault/Close markings (issue #457). The ↑/↓
// arrows stay literal because the handler consumes those bare keys directly.
func TestModelsOverlayHintsReflectKeyOverride(t *testing.T) {
	hk := liveHK()
	th := theme.New("aztec", theme.AztecPalette())
	catalog := modelCatalog{models: []client.ModelInfo{{ID: "m1"}}}
	picker := modelsState{view: modelsPanel, filtered: []client.ModelInfo{{ID: "m1"}}}
	got := stripANSIstr(renderModelsPanel(th, catalog, picker, client.Capabilities{ModelSelection: true}, "", hk, modelsRowBudgetFor(30, modelsPanelFixedRows(picker, "", hk))))
	if !strings.Contains(got, "↑/↓/ctrl+f14 move · ctrl+f17 use · ctrl+f25 set global default · ctrl+f16 clear filter / close") {
		t.Errorf("models hint should carry live page/use/set/close: %q", got)
	}
	for _, stale := range []string{"↑/↓/pgup move", "enter use · ctrl+g set global default · esc clear filter / close"} {
		if strings.Contains(got, stale) {
			t.Errorf("models hint still shows default %q: %q", stale, got)
		}
	}
	// The ↑/↓ arrows stay literal: the handler consumes those fixed bare keys.
	if !strings.Contains(got, "↑/↓/") {
		t.Errorf("models hint should keep the fixed ↑/↓ arrows: %q", got)
	}
}

// TestSessionsOverlayHintsReflectKeyOverride proves the sessions overlay panel +
// transcript footers read the LIVE Choose/NextTab/Close markings (issue #457).
func TestSessionsOverlayHintsReflectKeyOverride(t *testing.T) {
	hk := liveHK()
	th := theme.New("aztec", theme.AztecPalette())
	t.Run("panel", func(t *testing.T) {
		st := sessionsState{view: sessionsPanel, filtered: []client.SessionListItem{{ID: "s1", State: "idle", Capabilities: client.SessionInventoryCapabilities{PublicChat: true}}}}
		got := stripANSIstr(renderSessionsOverlay(th, st, client.Capabilities{}, "sess", "", hk, 100, 30))
		if !strings.Contains(got, "ctrl+f17: continue  ctrl+f18: switch  ctrl+f16: close") {
			t.Errorf("sessions panel hint should carry live continue/switch/close: %q", got)
		}
		if strings.Contains(got, "enter: continue  tab: switch  esc: close") {
			t.Errorf("sessions panel hint still shows defaults: %q", got)
		}
	})
	t.Run("transcript", func(t *testing.T) {
		st := sessionsState{view: sessionsTranscript}
		got := stripANSIstr(renderSessionsOverlay(th, st, client.Capabilities{}, "sess", "content", hk, 100, 30))
		if !strings.Contains(got, "ctrl+f16: Back") {
			t.Errorf("transcript hint should carry live close/back: %q", got)
		}
		if strings.Contains(got, "esc: back") {
			t.Errorf("transcript hint still shows default esc: %q", got)
		}
	})
}

// TestScheduleOverlayHintsReflectKeyOverride proves the schedule overlay panel/confirm
// footers read the LIVE Choose/Close markings, while the genuinely-local c/p/r/f/d//
// action keys stay literal (issue #457).
func TestScheduleOverlayHintsReflectKeyOverride(t *testing.T) {
	hk := liveHK()
	th := theme.New("aztec", theme.AztecPalette())
	t.Run("panel", func(t *testing.T) {
		st := scheduleState{view: schedulePanel, filtered: []client.Schedule{{Spec: client.ScheduleSpec{Name: "n1"}}}}
		got := stripANSIstr(renderScheduleOverlay(th, st, client.Capabilities{}, false, hk, 100, 30))
		if !strings.Contains(got, "ctrl+f17: inspect  c: create  p: pause  r: resume  f: fire-now  d: delete  /: filter  ctrl+f16: close") {
			t.Errorf("schedule panel hint should carry live inspect/close + literal c/p/r/f/d: %q", got)
		}
		if strings.Contains(got, "enter: inspect") || strings.Contains(got, "esc: close") {
			t.Errorf("schedule panel hint still shows a default: %q", got)
		}
	})
	t.Run("confirm", func(t *testing.T) {
		st := scheduleState{view: scheduleConfirm, confirm: client.Schedule{Spec: client.ScheduleSpec{Name: "n1"}}}
		got := stripANSIstr(renderScheduleOverlay(th, st, client.Capabilities{}, false, hk, 100, 30))
		if !strings.Contains(got, "ctrl+f17: delete  ctrl+f16: back") {
			t.Errorf("schedule confirm hint should carry live delete/back: %q", got)
		}
	})
}

// TestWorktreesOverlayHintsReflectKeyOverride proves the worktrees overlay panel/confirm
// footers read the LIVE Choose/Close markings (issue #457).
func TestWorktreesOverlayHintsReflectKeyOverride(t *testing.T) {
	hk := liveHK()
	th := theme.New("aztec", theme.AztecPalette())
	t.Run("panel", func(t *testing.T) {
		st := worktreesState{view: worktreesPanel, filtered: []client.Worktree{{Selector: testWorktreeSelector("opaque"), Label: "p", Branch: "b"}}}
		got := stripANSIstr(renderWorktreesOverlay(th, st, client.Capabilities{}, hk, 100, 30))
		if !strings.Contains(got, "ctrl+f17: select  ctrl+f16: close") {
			t.Errorf("worktrees panel hint should carry live select/close: %q", got)
		}
		if strings.Contains(got, "enter: select  esc: close") {
			t.Errorf("worktrees panel hint still shows defaults: %q", got)
		}
	})
}

// TestSkillsOverlayHintsReflectKeyOverride proves the skills panel footer reads the
// LIVE scroll + Close markings, while the literal ↑/↓ stays (bare up/down, not
// keyMap-bound) (issue #457).
func TestSkillsOverlayHintsReflectKeyOverride(t *testing.T) {
	hk := liveHK()
	th := theme.New("aztec", theme.AztecPalette())
	st := &skillsState{view: skillsPanel, skills: []client.Skill{{Name: "s1"}}, filtered: []client.Skill{{Name: "s1"}}, deps: surfaceDeps{theme: th, caps: client.Capabilities{Skills: true}, marks: hk}}
	body, _ := st.Render(100, 30)
	got := stripANSIstr(body)
	// ↑/↓ stays literal; pgup/pgdn scroll + close are live (scrollMarking returns
	// the live "<scrollU>/<scrollD>" pair once either half is remapped).
	if !strings.Contains(got, "↑/↓/ctrl+f14/ctrl+f15 scroll · ctrl+f16 clear filter / close") {
		t.Errorf("skills hint should carry live scroll+close with literal ↑/↓: %q", got)
	}
	if strings.Contains(got, "esc clear filter / close") {
		t.Errorf("skills hint still shows default close: %q", got)
	}
}

// TestSoulAndUserModelOverlayHintsReflectKeyOverride proves the soul/user-model
// read-only panels read the LIVE scroll + Close markings (issue #457).
func TestSoulAndUserModelOverlayHintsReflectKeyOverride(t *testing.T) {
	hk := liveHK()
	th := theme.New("aztec", theme.AztecPalette())
	t.Run("soul", func(t *testing.T) {
		st := soulState{view: soulPanel, soul: client.Soul{Present: true, Provenance: client.SoulProvenanceUser, SizeBytes: 10, SHA256: "abc"},
			deps: surfaceDeps{theme: th, marks: hk, caps: client.Capabilities{Soul: true}}}
		body, _ := st.Render(100, 30)
		got := stripANSIstr(body)
		if !strings.Contains(got, "ctrl+f14/ctrl+f15 scroll · ctrl+f16 close") {
			t.Errorf("soul hint should carry live scroll+close: %q", got)
		}
		if strings.Contains(got, "esc close") {
			t.Errorf("soul hint still shows default close: %q", got)
		}
	})
	t.Run("user model", func(t *testing.T) {
		st := userModelState{view: userModelPanel, model: client.UserModel{Entries: []client.UserModelEntry{{Key: "k"}}}}
		got := stripANSIstr(renderUserModelOverlay(th, st, client.Capabilities{UserModel: true}, hk, 100, 30))
		if !strings.Contains(got, "ctrl+f16 close") {
			t.Errorf("user-model hint should carry live close: %q", got)
		}
		if strings.Contains(got, "esc close") {
			t.Errorf("user-model hint still shows default close: %q", got)
		}
	})
}

// TestAgentsInvOverlayHintsReflectKeyOverride proves the agent-definitions inventory
// footer reads the LIVE scroll + Close markings (issue #457).
func TestAgentsInvOverlayHintsReflectKeyOverride(t *testing.T) {
	hk := liveHK()
	th := theme.New("aztec", theme.AztecPalette())
	st := agentsInvState{view: agentsInvPanel, agents: []client.Agent{{Name: "a1"}}}
	got := stripANSIstr(renderAgentsInvOverlay(th, st, client.Capabilities{Agents: true}, hk, 100, 30))
	if !strings.Contains(got, "ctrl+f14/ctrl+f15 scroll · ctrl+f16 close") {
		t.Errorf("agents-inv hint should carry live scroll+close: %q", got)
	}
	if strings.Contains(got, "esc close") {
		t.Errorf("agents-inv hint still shows default close: %q", got)
	}
}

// TestAncillaryHintsReflectKeyOverride covers the non-overlay chord surfaces found
// by the final audit: textarea/welcome prompt hints, queued-follow-up affordances,
// post-picker status text, and nested help prose. All must read the same live keyMap.
func TestAncillaryHintsReflectKeyOverride(t *testing.T) {
	th := theme.New("aztec", theme.AztecPalette())
	m := newTestModelFromDeps(Deps{Theme: th, KeyOverrides: overrideAll(), NoAltScreen: true})

	t.Run("textarea placeholder", func(t *testing.T) {
		got := m.prompt.Placeholder()
		for _, want := range []string{"ctrl+f1 to send", "ctrl+f2 for newline", "ctrl+f12 for help"} {
			if !strings.Contains(got, want) {
				t.Errorf("placeholder missing live hint %q: %q", want, got)
			}
		}
		for _, stale := range []string{"enter to send", "shift+enter for newline", "? for help"} {
			if strings.Contains(got, stale) {
				t.Errorf("placeholder still shows default %q: %q", stale, got)
			}
		}
	})

	t.Run("welcome prompt", func(t *testing.T) {
		m.width = 100
		m.vp.SetHeight(30)
		for name, got := range map[string]string{
			"splash": stripANSIstr(m.renderZeroState()),
			"plain":  stripANSIstr(m.legacyZeroStateBody()),
		} {
			if !strings.Contains(got, "press ctrl+f1") {
				t.Errorf("%s welcome prompt should carry live submit: %q", name, got)
			}
			if strings.Contains(got, "press enter") {
				t.Errorf("%s welcome prompt still shows default enter: %q", name, got)
			}
		}
	})

	t.Run("queue", func(t *testing.T) {
		mm := m
		mm.queued = []string{"follow up"}
		got := stripANSIstr(mm.renderQueue())
		if !strings.Contains(got, "ctrl+f30 edit") || strings.Contains(got, "↑ edit") {
			t.Errorf("active queue hint should carry live EditBack: %q", got)
		}
		mm.queuePaused = "error"
		got = stripANSIstr(mm.renderQueue())
		if !strings.Contains(got, "ctrl+f1 sends · ctrl+f30 edit · ctrl+f4 clears") {
			t.Errorf("paused queue hint should carry live Submit/EditBack/Cancel: %q", got)
		}
		if strings.Contains(got, "enter sends") || strings.Contains(got, "esc clears") {
			t.Errorf("paused queue hint still shows defaults: %q", got)
		}
	})

	t.Run("status prompts", func(t *testing.T) {
		inserted, _ := m.insertIntoInput("payload", "loaded prompt")
		got := inserted.(Model).statusMsg
		if !strings.Contains(got, "press ctrl+f1 to send") || strings.Contains(got, "press enter") {
			t.Errorf("MCP insert status should carry live Submit: %q", got)
		}

		mm := m
		mm.queued = []string{"follow up"}
		mm.pendingMode = "plan"
		queued, _ := mm.popAndSubmit()
		got = stripANSIstr(queued.(Model).statusMsg)
		if !strings.Contains(got, "press ctrl+f1 to continue") || strings.Contains(got, "press enter") {
			t.Errorf("queued-mode status should carry live Submit: %q", got)
		}
	})

	t.Run("nested help prose", func(t *testing.T) {
		got := stripANSIstr(helpBody(th, client.Capabilities{}, m.helpKeyMarkings()))
		for _, want := range []string{"ctrl+f18 to switch", "ctrl+f29 resumes auto-follow", "ctrl+f4 clears"} {
			if !strings.Contains(got, want) {
				t.Errorf("help body missing live nested hint %q: %q", want, got)
			}
		}
		for _, stale := range []string{"tab to switch", "end resumes auto-follow", "esc clears"} {
			if strings.Contains(got, stale) {
				t.Errorf("help body still shows default nested hint %q: %q", stale, got)
			}
		}
	})
}
