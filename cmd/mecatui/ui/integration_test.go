package ui

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/theme"
)

// TestFooterContextMeterWithWindow drives a result through Update and asserts the
// footer shows a populated context meter (bar + percentage + used/total) when a
// context window is configured, plus the session usage facets.
func TestFooterContextMeterWithWindow(t *testing.T) {
	th := theme.New("aztec", theme.AztecPalette())
	m := New(Deps{
		Theme: th,
		Model: "mock-model",
	})
	// The footer denominator is the SERVER-echoed per-model window (resolve-at-use,
	// live-first server-side); set it as the test would receive it on SessionReady.
	m.resolvedSessionModel = client.ResolvedModel{ContextWindow: 200000}
	m = applyAll(m, tea.WindowSizeMsg{Width: 120, Height: 30})
	// The meter's numerator comes from the per-turn TurnEndMsg (current
	// occupancy); the facets come from the cumulative ResultMsg total.
	m = applyAll(m, client.TurnEndMsg{Turn: 1, Usage: client.Usage{InputTokens: 40000, OutputTokens: 345}})
	m = applyAll(m, client.ResultMsg{
		Stop:  "end_turn",
		Usage: client.Usage{InputTokens: 40000, OutputTokens: 345, CacheReadTokens: 35200},
	})

	footer := stripANSIstr(m.renderFooter())
	if !strings.Contains(footer, "ctx ") || !strings.ContainsAny(footer, ctxGlyphOk+ctxGlyphEmpty) {
		t.Errorf("expected a context bar in footer:\n%s", footer)
	}
	if !strings.Contains(footer, "20%") {
		t.Errorf("expected 20%% context in footer:\n%s", footer)
	}
	if !strings.Contains(footer, "40K/200K") {
		t.Errorf("expected used/total in footer:\n%s", footer)
	}
	if !strings.Contains(footer, "↑40K") || !strings.Contains(footer, "↓345") {
		t.Errorf("expected usage facets in footer:\n%s", footer)
	}
	if !strings.Contains(footer, "cache 88%") {
		t.Errorf("expected cache-hit rate in footer:\n%s", footer)
	}
}

// TestFooterContextMeterUnknownWindow asserts the meter degrades to just the
// current size when no window is known.
func TestFooterContextMeterUnknownWindow(t *testing.T) {
	m := New(Deps{Theme: theme.New("aztec", theme.AztecPalette())})
	m = applyAll(m, tea.WindowSizeMsg{Width: 120, Height: 30})
	m = applyAll(m, client.TurnEndMsg{Turn: 1, Usage: client.Usage{InputTokens: 7903}})

	footer := stripANSIstr(m.renderFooter())
	if !strings.Contains(footer, "ctx 7.9K") {
		t.Errorf("expected bare context size in footer:\n%s", footer)
	}
	if strings.ContainsAny(footer, ctxGlyphOk+ctxGlyphWarn+ctxGlyphDanger+ctxGlyphEmpty) {
		t.Errorf("expected no bar without a window:\n%s", footer)
	}
}

// TestContextMeterTracksLatestTurnNotCumulative pins the two-axis usage model:
// m.contextTokens is CURRENT occupancy — assigned (not summed) from each
// TurnEndMsg's InputTokens — while m.usage is the SESSION-CUMULATIVE total fed
// only by the terminal ResultMsg. A ResultMsg must never overwrite the meter
// with the run's cumulative input, and a TurnEndMsg must never inflate the
// session totals (adding both would double-count).
func TestContextMeterTracksLatestTurnNotCumulative(t *testing.T) {
	m := New(Deps{Theme: theme.New("aztec", theme.AztecPalette())})
	m.resolvedSessionModel = client.ResolvedModel{ContextWindow: 200000}
	m = applyAll(m, tea.WindowSizeMsg{Width: 120, Height: 30})

	// Two turns: the meter shows the LATEST turn's prompt size, not the sum.
	m = applyAll(m, client.TurnEndMsg{Turn: 1, Usage: client.Usage{InputTokens: 1000, OutputTokens: 50}})
	m = applyAll(m, client.TurnEndMsg{Turn: 2, Usage: client.Usage{InputTokens: 1200, OutputTokens: 30}})
	if m.contextTokens != 1200 {
		t.Errorf("contextTokens = %d, want 1200 (latest turn, not the 2200 sum)", m.contextTokens)
	}
	// Per-turn usage must NOT feed the cumulative session total.
	if m.usage.InputTokens != 0 {
		t.Errorf("usage.InputTokens = %d, want 0 before the terminal result", m.usage.InputTokens)
	}

	// The terminal result carries the run's CUMULATIVE usage: it feeds the
	// session totals and leaves the occupancy meter alone.
	m = applyAll(m, client.ResultMsg{
		Stop:  "end_turn",
		Usage: client.Usage{InputTokens: 2200, OutputTokens: 80, CacheReadTokens: 1100},
	})
	if m.contextTokens != 1200 {
		t.Errorf("contextTokens = %d after result, want 1200 (cumulative total must not overwrite occupancy)", m.contextTokens)
	}
	if m.usage.InputTokens != 2200 {
		t.Errorf("usage.InputTokens = %d, want 2200 (the run's cumulative total, folded once)", m.usage.InputTokens)
	}

	// The cache-hit facet is cumulative cache-read / cumulative input: 1100/2200 = 50%.
	footer := stripANSIstr(m.renderFooter())
	if !strings.Contains(footer, "cache 50%") {
		t.Errorf("expected cumulative cache-hit rate 50%% in footer:\n%s", footer)
	}
	// And the meter renders the occupancy numerator, not the cumulative total.
	if !strings.Contains(footer, "1.2K/200K") {
		t.Errorf("expected ctx 1.2K/200K (latest turn) in footer:\n%s", footer)
	}
}

// TestFooterNarrowWidthTiers asserts the footer sheds detail in priority order
// as width shrinks — facets first, the context signal last. Context % must
// survive in every tier where anything fits beside the left status.
func TestFooterNarrowWidthTiers(t *testing.T) {
	th := theme.New("aztec", theme.AztecPalette())
	m := New(Deps{Theme: th})
	m.resolvedSessionModel = client.ResolvedModel{ContextWindow: 200000}
	m = applyAll(m, client.TurnEndMsg{Turn: 1, Usage: client.Usage{InputTokens: 140000, OutputTokens: 345}})
	m = applyAll(m, client.ResultMsg{
		Stop:  "end_turn",
		Usage: client.Usage{InputTokens: 140000, OutputTokens: 345, CacheReadTokens: 70000},
	})
	left := "ready"

	// Wide: full tier — meter (with used/total) AND facets.
	wide := stripANSIstr(m.fitFooter(left, 120))
	if !strings.Contains(wide, "140K/200K") || !strings.Contains(wide, "↑140K") {
		t.Errorf("wide footer should be the full tier:\n%q", wide)
	}

	// Medium: meter survives, facets dropped.
	med := stripANSIstr(m.fitFooter(left, 40))
	if strings.Contains(med, "↑140K") {
		t.Errorf("medium footer should drop io/cache facets first:\n%q", med)
	}
	if !strings.Contains(med, "ctx ") || !strings.Contains(med, "70%") {
		t.Errorf("medium footer should keep the context signal:\n%q", med)
	}

	// Tight: even the minimal bar-less percentage must carry the context %.
	tight := stripANSIstr(m.fitFooter(left, 22))
	if !strings.Contains(tight, "ctx ") || !strings.Contains(tight, "70%") {
		t.Errorf("tight footer should still show ctx %%:\n%q", tight)
	}
	if strings.Contains(tight, "140K/200K") {
		t.Errorf("tight footer should not carry used/total:\n%q", tight)
	}

	// Too narrow for anything: just the left status, no usage bleed-through.
	none := stripANSIstr(m.fitFooter(left, 10))
	if strings.Contains(none, "ctx") {
		t.Errorf("ultra-narrow footer should drop the usage segment entirely:\n%q", none)
	}
}

// TestFooterTeamSegmentTiers asserts the live-team footer summary segment tiers
// alongside the context meter, and that the team segment (an advertisement) is the
// FIRST thing dropped under width pressure while the context % survives longest.
func TestFooterTeamSegmentTiers(t *testing.T) {
	th := theme.New("aztec", theme.AztecPalette())
	m := New(Deps{Theme: th})
	m.resolvedSessionModel = client.ResolvedModel{ContextWindow: 200000}
	m = applyAll(m, tea.WindowSizeMsg{Width: 200, Height: 30})
	m = applyAll(m, client.TurnEndMsg{Turn: 1, Usage: client.Usage{InputTokens: 140000, OutputTokens: 345}})
	m = applyAll(m, client.ResultMsg{
		Stop:  "end_turn",
		Usage: client.Usage{InputTokens: 140000, OutputTokens: 345, CacheReadTokens: 70000},
	})
	m = seedTeam(m, func(c *conversation) {
		c.setTeamStart("t1", "team-x", roster()) // lead + scout, both working → 2/2
	})
	left := "ready"

	// Wide: full team tier (glyph + team-id + k/N working + ctrl+a agents) AND the
	// full context meter (ctx + %).
	wide := stripANSIstr(m.fitFooter(left, 200))
	for _, want := range []string{teamLiveGlyph, "team-x", "2/2 working", "ctrl+a agents", "ctx ", "70%"} {
		if !strings.Contains(wide, want) {
			t.Errorf("wide footer should contain %q:\n%q", want, wide)
		}
	}

	// Medium: team segment degrades to the id-less "⟳ k/N working · ctrl+a"; the
	// context % must still be present.
	med := stripANSIstr(m.fitFooter(left, 56))
	if !strings.Contains(med, teamLiveGlyph) || !strings.Contains(med, "2/2 working") {
		t.Errorf("medium footer should keep the team summary:\n%q", med)
	}
	if strings.Contains(med, "team-x") {
		t.Errorf("medium footer should drop the team id:\n%q", med)
	}
	if !strings.Contains(med, "ctx ") || !strings.Contains(med, "70%") {
		t.Errorf("medium footer should keep the context signal:\n%q", med)
	}

	// Tight: the team segment is dropped entirely; the context % wins (survives
	// longest). This locks the priority: context % over the team advertisement.
	tight := stripANSIstr(m.fitFooter(left, 18))
	if strings.Contains(tight, teamLiveGlyph) {
		t.Errorf("tight footer should drop the team segment:\n%q", tight)
	}
	if !strings.Contains(tight, "ctx ") || !strings.Contains(tight, "70%") {
		t.Errorf("tight footer should still show ctx %%:\n%q", tight)
	}
}

// TestFooterNoTeamSegment is the regression guard for the byte-identical no-team
// path: with no team seeded the footer carries no team glyph and no "team-" id.
func TestFooterNoTeamSegment(t *testing.T) {
	th := theme.New("aztec", theme.AztecPalette())
	m := New(Deps{Theme: th})
	m.resolvedSessionModel = client.ResolvedModel{ContextWindow: 200000}
	m = applyAll(m, client.ResultMsg{
		Stop:  "end_turn",
		Usage: client.Usage{InputTokens: 140000, OutputTokens: 345},
	})
	footer := stripANSIstr(m.fitFooter("ready", 200))
	if strings.Contains(footer, teamLiveGlyph) || strings.Contains(footer, "team-") {
		t.Errorf("no-team footer must carry no team segment:\n%q", footer)
	}
}

// TestFooterTeamDoneDropsSegment asserts a team that has ENDED drops the footer
// segment — latestTeamBlock still returns it, so liveTeamBlock's !teamDone gate is
// what hides it. This is deliberately NARROWER than the ctrl+a overlay, which
// opens on the last-seen team (done or not) to review a finished roster.
func TestFooterTeamDoneDropsSegment(t *testing.T) {
	th := theme.New("aztec", theme.AztecPalette())
	m := New(Deps{Theme: th})
	m.resolvedSessionModel = client.ResolvedModel{ContextWindow: 200000}
	m = applyAll(m, client.ResultMsg{
		Stop:  "end_turn",
		Usage: client.Usage{InputTokens: 140000},
	})
	m = seedTeam(m, func(c *conversation) {
		c.setTeamStart("t1", "team-x", roster())
		c.setTeamEnd("t1", "team-x", 3, "end_turn", client.Usage{InputTokens: 100}, nil)
	})
	footer := stripANSIstr(m.fitFooter("ready", 200))
	if strings.Contains(footer, teamLiveGlyph) || strings.Contains(footer, "team-x") {
		t.Errorf("ended team must drop the footer segment:\n%q", footer)
	}
}

// TestAgentsInvSlashCommandEndToEnd drives the FULL palette path for the /agents
// definition inventory (issue #15, Gap A): type "/agents", press enter, and assert
// the panel opens, fires ListAgents, and renders the resolved defs in the
// conversation viewport. It goes through the real keypress→palette→builtin
// dispatch→Update reducer→render chain (not a direct runAgentsInv call), so a
// regression in the palette gating, the caps/wired filter, or the AgentsMsg
// reduction surfaces here. caps.Agents + a wired AgentLister are both set, which is
// what registers the /agents built-in.
func TestAgentsInvSlashCommandEndToEnd(t *testing.T) {
	fa := sampleAgents()
	m := newAgentsInvModel(t, fa, client.Capabilities{Agents: true})

	// Type the built-in name; the palette opens on "/".
	m = typeText(t, m, "/agents")
	if !m.palette.open {
		t.Fatal("palette should be open after typing /agents")
	}

	// Enter runs the selected built-in (opens the panel + fires the RPC). Drive the
	// returned command to completion so the AgentsMsg is reduced back in.
	mm, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = feedCmd(t, mm.(Model), cmd)

	if m.agentsInv.view != agentsInvPanel {
		t.Fatalf("/agents+enter should open the inventory panel, view=%v", m.agentsInv.view)
	}
	if fa.calls != 1 {
		t.Errorf("ListAgents calls = %d, want 1 (the /agents built-in fired the RPC)", fa.calls)
	}
	body := stripANSIstr(m.View().Content)
	if !strings.Contains(body, "scout") || !strings.Contains(body, "explore the codebase") {
		t.Errorf("the rendered panel should carry the resolved defs, got:\n%s", body)
	}
	if !strings.Contains(body, "model:gpt-5") || !strings.Contains(body, "tools:Read,Grep") {
		t.Errorf("the rendered panel should carry the def metadata, got:\n%s", body)
	}
}

// TestSoulSlashCommandEndToEnd drives the /soul built-in through the real palette +
// keypress reducer: typing "/soul"+enter opens the read-only persona panel and fires
// GetSoul, rendering the projected content + metadata.
func TestSoulSlashCommandEndToEnd(t *testing.T) {
	fs := sampleSoul()
	m := newSoulModel(t, fs, client.Capabilities{Soul: true})

	m = typeText(t, m, "/soul")
	if !m.palette.open {
		t.Fatal("palette should be open after typing /soul")
	}
	mm, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = feedCmd(t, mm.(Model), cmd)

	if s := soulActive(m); s == nil || s.view != soulPanel {
		t.Fatalf("/soul+enter should open the persona panel (modal), got %+v", m.modal)
	}
	if fs.calls != 1 {
		t.Errorf("GetSoul calls = %d, want 1 (the /soul built-in fired the RPC)", fs.calls)
	}
	body := stripANSIstr(m.View().Content)
	if !strings.Contains(body, "terse and direct") {
		t.Errorf("the rendered panel should carry the persona content, got:\n%s", body)
	}
	if !strings.Contains(body, "user ·") {
		t.Errorf("the rendered panel should carry the provenance/trust metadata, got:\n%s", body)
	}
}

// TestUserModelSlashCommandEndToEnd drives the /usermodel built-in through the real
// palette + keypress reducer: typing "/usermodel"+enter opens the read-only panel and
// fires GetUserModel, rendering the live index.
func TestUserModelSlashCommandEndToEnd(t *testing.T) {
	fum := sampleUserModel()
	m := newUserModelModel(t, fum, client.Capabilities{UserModel: true})

	m = typeText(t, m, "/usermodel")
	if !m.palette.open {
		t.Fatal("palette should be open after typing /usermodel")
	}
	mm, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = feedCmd(t, mm.(Model), cmd)

	if m.userModel.view != userModelPanel {
		t.Fatalf("/usermodel+enter should open the panel, view=%v", m.userModel.view)
	}
	if fum.calls != 1 {
		t.Errorf("GetUserModel calls = %d, want 1 (the /usermodel built-in fired the RPC)", fum.calls)
	}
	body := stripANSIstr(m.View().Content)
	if !strings.Contains(body, "the operator's name") {
		t.Errorf("the rendered panel should carry the live entries, got:\n%s", body)
	}
}

// TestTeamOverlayCtrlAMidRunEndToEnd drives the live-team overlay open MID-RUN via
// the real keypress reducer (issue #15, Gap B): with a team streaming, ctrl+a
// opens the roster overlay (rendered in the viewport) without enqueuing or
// cancelling. It complements the unit test by going through Update + View.
func TestTeamOverlayCtrlAMidRunEndToEnd(t *testing.T) {
	m := newMCPModel(t, aztec(), nil)
	m.caps.Teams = true
	m = seedTeam(m, func(c *conversation) {
		c.setTeamStart("t1", "team-x", roster())
		c.addTeamMember(member("scout", "tool.call", client.TeamMsg{ToolName: "Grep"}))
	})
	m.phase = phaseRunning

	mm, _ := m.Update(ctrlKey('a'))
	m = mm.(Model)
	if m.team.view != teamRoster {
		t.Fatalf("ctrl+a mid-run should open the roster overlay, view=%v", m.team.view)
	}
	if len(m.queued) != 0 {
		t.Errorf("ctrl+a mid-run must not enqueue, queue=%v", m.queued)
	}
	if m.phase != phaseRunning {
		t.Errorf("ctrl+a mid-run must not change the phase, got %v", m.phase)
	}
	body := stripANSIstr(m.View().Content)
	if !strings.Contains(body, "agents · 2 members") || !strings.Contains(body, "[lead]") {
		t.Errorf("the rendered overlay should show the live roster, got:\n%s", body)
	}
}

// TestHeaderTruncatesLongModel asserts a long model id is capped in the header.
// The model segment renders from the EFFECTIVE model the server resolved (echoed on
// SessionReadyMsg), so the test drives a create response with a long resolved id —
// the header shows it (no model segment while still connecting, by design).
func TestHeaderTruncatesLongModel(t *testing.T) {
	long := "anthropic/claude-opus-4-8-with-a-really-long-suffix-2026"
	conv := &fakeConv{recv: &fakeRecver{}, send: &fakeSender{},
		resolvedModel: client.ResolvedModel{ProviderID: "anthropic", ModelID: long}}
	m := New(Deps{Theme: theme.New("aztec", theme.AztecPalette()), Session: conv, Conv: conv, Ctx: context.Background()})
	m = applyAll(m,
		tea.WindowSizeMsg{Width: 200, Height: 30},
		client.SessionReadyMsg{SessionID: "sess-test-0001", ResolvedModel: client.ResolvedModel{ProviderID: "anthropic", ModelID: long}},
	)
	header := stripANSIstr(m.renderHeader())
	if strings.Contains(header, long) {
		t.Errorf("long model id should be truncated in header:\n%q", header)
	}
	if !strings.Contains(header, "…") {
		t.Errorf("truncated model should carry an ellipsis:\n%q", header)
	}
	if !strings.Contains(header, "anthropic/claude") {
		t.Errorf("truncation should keep the model prefix:\n%q", header)
	}
}

// TestEditCardRendersDiffInConversation drives an Edit tool call through the
// conversation and asserts the rendered viewport shows a red/green diff (not raw
// JSON args).
func TestEditCardRendersDiffInConversation(t *testing.T) {
	m := New(Deps{Theme: theme.New("aztec", theme.AztecPalette())})
	m = applyAll(m, tea.WindowSizeMsg{Width: 100, Height: 30})
	m.phase = phaseRunning
	m = applyAll(m,
		client.ToolCallMsg{ID: "e1", Name: "Edit", Args: `{"path":"x.go","old_string":"foo","new_string":"bar"}`},
	)
	view := stripANSIstr(m.rend.renderConversation(&m.conv, m.expandTools))
	if !strings.Contains(view, "- foo") || !strings.Contains(view, "+ bar") {
		t.Errorf("expected diff lines in conversation, got:\n%s", view)
	}
	if strings.Contains(view, `"old_string"`) {
		t.Errorf("Edit card should not show raw JSON args:\n%s", view)
	}
}

// TestExpandToolsToggle asserts ctrl+t flips the global expand flag.
func TestExpandToolsToggle(t *testing.T) {
	m := New(Deps{Theme: theme.New("aztec", theme.AztecPalette())})
	m = applyAll(m, tea.WindowSizeMsg{Width: 100, Height: 30})
	if m.expandTools {
		t.Fatal("expandTools should start false")
	}
	m = applyAll(m, tea.KeyPressMsg{Code: 't', Mod: tea.ModCtrl})
	if !m.expandTools {
		t.Error("ctrl+t should set expandTools true")
	}
	// The footer is now the minimal "? help · … · ctrl+c quit" line; the full
	// chord list (including "ctrl+t … details") moved into the "?" help overlay.
	footer := stripANSIstr(m.renderFooter())
	if !strings.Contains(footer, "? help") || !strings.Contains(footer, "ctrl+c quit") {
		t.Errorf("footer should carry the minimal help line:\n%s", footer)
	}
	if strings.Contains(footer, "ctrl+t details") {
		t.Errorf("footer should no longer carry the full chord list:\n%s", footer)
	}
	m = applyAll(m, tea.KeyPressMsg{Code: 't', Mod: tea.ModCtrl})
	if m.expandTools {
		t.Error("ctrl+t should toggle expandTools back to false")
	}
}

// TestReasoningBlockCollapsedThenExpanded asserts a reasoning delta renders as a
// dim, collapsed one-line "reasoning summary" header by default (the full text
// hidden), and that ctrl+t expands it to show the streamed reasoning text under
// a lossy-summary caveat. It also asserts the reasoning renders ABOVE the
// assistant answer of the same turn.
func TestReasoningBlockCollapsedThenExpanded(t *testing.T) {
	m := New(Deps{Theme: theme.New("aztec", theme.AztecPalette())})
	m = applyAll(m, tea.WindowSizeMsg{Width: 100, Height: 30})
	m.phase = phaseRunning
	m = applyAll(m,
		client.TurnStartMsg{Turn: 1},
		client.ReasoningDeltaMsg{Turn: 1, Text: "first I will inspect the file\nthen I will edit it"},
		client.AssistantDeltaMsg{Turn: 1, Text: "Here is the answer."},
	)

	// Collapsed (default): the "reasoning summary" header is shown, body hidden.
	collapsed := stripANSIstr(m.rend.renderConversation(&m.conv, m.expandTools))
	if !strings.Contains(collapsed, "reasoning summary · 2 lines · ctrl+t expand") {
		t.Errorf("expected collapsed reasoning-summary header, got:\n%s", collapsed)
	}
	if strings.Contains(collapsed, "inspect the file") {
		t.Errorf("collapsed reasoning must hide the body text:\n%s", collapsed)
	}
	if strings.Contains(collapsed, "may not reflect") {
		t.Errorf("collapsed reasoning must not show the expanded caveat:\n%s", collapsed)
	}

	// Reasoning must precede the assistant answer.
	if ri, ai := strings.Index(collapsed, "reasoning summary"), strings.Index(collapsed, "Here is the answer"); ri < 0 || ai < 0 || ri > ai {
		t.Errorf("reasoning (%d) should render above the answer (%d):\n%s", ri, ai, collapsed)
	}

	// Expanded (ctrl+t): the caveat + the full reasoning text become visible.
	m = applyAll(m, tea.KeyPressMsg{Code: 't', Mod: tea.ModCtrl})
	expanded := stripANSIstr(m.rend.renderConversation(&m.conv, m.expandTools))
	if !strings.Contains(expanded, "may not reflect its actual process") {
		t.Errorf("expanded reasoning should carry the lossy-summary caveat:\n%s", expanded)
	}
	if !strings.Contains(expanded, "inspect the file") || !strings.Contains(expanded, "then I will edit it") {
		t.Errorf("expanded reasoning should show the body text:\n%s", expanded)
	}
}

// TestReasoningInterleavedRendersOnce asserts that interleaved reasoning/answer
// deltas within one turn (reasoning→text→reasoning→text) fold into a SINGLE
// reasoning region above the merged answer, with a truthful line count — not
// multiple stacked reasoning blocks. This is the correctness fix: reasoning is
// an attribute of the turn's assistant block, not a reordered sibling.
func TestReasoningInterleavedRendersOnce(t *testing.T) {
	m := New(Deps{Theme: theme.New("aztec", theme.AztecPalette())})
	m = applyAll(m, tea.WindowSizeMsg{Width: 100, Height: 30})
	m.phase = phaseRunning
	m = applyAll(m,
		client.TurnStartMsg{Turn: 1},
		client.ReasoningDeltaMsg{Turn: 1, Text: "step one\n"},
		client.AssistantDeltaMsg{Turn: 1, Text: "Answer part A. "},
		client.ReasoningDeltaMsg{Turn: 1, Text: "step two\n"},
		client.AssistantDeltaMsg{Turn: 1, Text: "Answer part B."},
		client.TurnEndMsg{Turn: 1, Usage: client.Usage{InputTokens: 1200, OutputTokens: 340}, DurationMs: 4100},
	)

	// Exactly one assistant block, with both reasoning fragments merged into it.
	var asst int
	for i := range m.conv.blocks {
		if m.conv.blocks[i].kind == blockAssistant {
			asst++
			if m.conv.blocks[i].reasoning != "step one\nstep two\n" {
				t.Errorf("reasoning not merged onto the block: %q", m.conv.blocks[i].reasoning)
			}
		}
	}
	if asst != 1 {
		t.Fatalf("want exactly 1 assistant block, got %d", asst)
	}

	// Exactly one collapsed reasoning header, with the true 2-line count.
	view := stripANSIstr(m.rend.renderConversation(&m.conv, false))
	if got := strings.Count(view, "reasoning summary"); got != 1 {
		t.Errorf("want exactly one reasoning region, got %d:\n%s", got, view)
	}
	if !strings.Contains(view, "reasoning summary · 2 lines · ctrl+t expand") {
		t.Errorf("merged reasoning should report 2 lines:\n%s", view)
	}
	// Reasoning above both answer fragments.
	ri := strings.Index(view, "reasoning summary")
	ai := strings.Index(view, "Answer part A")
	if ri < 0 || ai < 0 || ri > ai {
		t.Errorf("reasoning (%d) should render above the merged answer (%d):\n%s", ri, ai, view)
	}
}

// TestReasoningLiveAffordance asserts that while reasoning is streaming and no
// answer text has arrived, the collapsed header reads "reasoning…", flipping to
// the static expandable form once answer text begins.
func TestReasoningLiveAffordance(t *testing.T) {
	m := New(Deps{Theme: theme.New("aztec", theme.AztecPalette())})
	m = applyAll(m, tea.WindowSizeMsg{Width: 100, Height: 30})
	m.phase = phaseRunning
	m = applyAll(m,
		client.TurnStartMsg{Turn: 1},
		client.ReasoningDeltaMsg{Turn: 1, Text: "thinking about it\n"},
	)
	live := stripANSIstr(m.rend.renderConversation(&m.conv, false))
	if !strings.Contains(live, "reasoning…") {
		t.Errorf("streaming reasoning (no answer yet) should show the live affordance:\n%s", live)
	}
	if strings.Contains(live, "ctrl+t expand") {
		t.Errorf("live reasoning should not yet show the static expand hint:\n%s", live)
	}

	// Answer text begins → flips to the static, expandable header.
	m = applyAll(m, client.AssistantDeltaMsg{Turn: 1, Text: "Done."})
	settled := stripANSIstr(m.rend.renderConversation(&m.conv, false))
	if strings.Contains(settled, "reasoning…") {
		t.Errorf("reasoning should stop showing the live affordance once answer begins:\n%s", settled)
	}
	if !strings.Contains(settled, "reasoning summary · 1 line · ctrl+t expand") {
		t.Errorf("settled reasoning should show the static header:\n%s", settled)
	}
}

// TestTurnEndStatLine asserts a non-trivial TurnEndMsg appends a muted inline
// stat line that leads with cost (tokens) then time, carries no turn index, and
// omits the duration segment when no clock reported one.
func TestTurnEndStatLine(t *testing.T) {
	m := New(Deps{Theme: theme.New("aztec", theme.AztecPalette())})
	m = applyAll(m, tea.WindowSizeMsg{Width: 100, Height: 30})
	m.phase = phaseRunning

	m = applyAll(m, client.TurnEndMsg{Turn: 2, Usage: client.Usage{InputTokens: 1200, OutputTokens: 340}, DurationMs: 4100})
	withDur := stripANSIstr(m.rend.renderConversation(&m.conv, m.expandTools))
	if !strings.Contains(withDur, "↑1.2K ↓340 · 4.1s") {
		t.Errorf("expected cost-first per-turn stat line with duration, got:\n%s", withDur)
	}
	if strings.Contains(withDur, "turn 2") {
		t.Errorf("stat line should not carry a turn index:\n%s", withDur)
	}

	// A turn with substantial tokens but no duration (no clock) omits the elapsed
	// segment but still renders (it is not trivial).
	m = applyAll(m, client.TurnEndMsg{Turn: 3, Usage: client.Usage{InputTokens: 500, OutputTokens: 20}, DurationMs: 0})
	noDur := stripANSIstr(m.rend.renderConversation(&m.conv, m.expandTools))
	if !strings.Contains(noDur, "↑500 ↓20") {
		t.Errorf("expected per-turn stat line, got:\n%s", noDur)
	}
	if strings.Contains(noDur, "↑500 ↓20 ·") {
		t.Errorf("turn with no clock should omit the duration segment:\n%s", noDur)
	}
}

// TestTurnEndTrivialSuppressed asserts a near-empty turn (tiny tokens, sub-second
// or no duration) produces NO stat line, so a long run isn't littered.
func TestTurnEndTrivialSuppressed(t *testing.T) {
	m := New(Deps{Theme: theme.New("aztec", theme.AztecPalette())})
	m = applyAll(m, tea.WindowSizeMsg{Width: 100, Height: 30})
	m.phase = phaseRunning

	before := len(m.conv.blocks)
	// Tiny tokens, no clock → trivial → suppressed.
	m = applyAll(m, client.TurnEndMsg{Turn: 1, Usage: client.Usage{InputTokens: 5, OutputTokens: 2}, DurationMs: 0})
	// Tiny tokens, sub-second duration → still trivial → suppressed.
	m = applyAll(m, client.TurnEndMsg{Turn: 2, Usage: client.Usage{InputTokens: 10, OutputTokens: 0}, DurationMs: 300})
	if len(m.conv.blocks) != before {
		t.Errorf("trivial turns should add no stat block; blocks grew %d→%d", before, len(m.conv.blocks))
	}

	// A turn over the duration threshold is NOT trivial even with tiny tokens.
	m = applyAll(m, client.TurnEndMsg{Turn: 3, Usage: client.Usage{InputTokens: 10, OutputTokens: 0}, DurationMs: 1500})
	view := stripANSIstr(m.rend.renderConversation(&m.conv, false))
	if !strings.Contains(view, "↑10 ↓0 · 1.5s") {
		t.Errorf("a turn with a measurable duration should not be suppressed:\n%s", view)
	}
}
