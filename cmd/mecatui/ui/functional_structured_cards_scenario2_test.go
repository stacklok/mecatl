package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/theme"
)

func TestMecatuiFunctionalConversationCards_Scenario2_PerFamilyPreparedBlocksPreserveSemantics(t *testing.T) {
	r := newTestRenderer()
	r.setWidth(42)

	cases := []struct {
		name string
		b    block
		want []string
		bad  []string
	}{
		{"user media", block{kind: blockUser, raw: "hello\nworld", media: []string{"image/png (inline)", "audio/wav (inline)"}}, []string{"▌ you", "hello", "world", "📎 image/png", "📎 audio/wav"}, nil},
		{"ordinary notice", block{kind: blockNotice, raw: "compacted"}, []string{"• compacted"}, nil},
		{"recovery notice", block{kind: blockNotice, raw: "recover this turn", recover: true}, []string{"⚠ recover this turn"}, nil},
		{"hook", block{kind: blockHook, raw: "PreToolUse hook rewrote tool arguments", hookPhase: "PreToolUse", hookTool: "Shell", hookDecision: string(client.HookModified)}, []string{"✎ hook PreToolUse · Shell: modified", "rewrote tool arguments"}, []string{"PreToolUse hook rewrote"}},
		{"transient error", block{kind: blockError, raw: "upstream unavailable"}, []string{"✗ upstream unavailable"}, []string{"retrying won't help"}},
		{"permanent error", block{kind: blockError, raw: `POST "https://provider.invalid": 400 Bad Request {"error":{"message":"bad model"}}`, permanent: true}, []string{"✗ 400 Bad Request: bad model", "retrying", "won't help", r.marks.expandTools + " shows details"}, []string{"raw payload:"}},
		{"delivery", block{kind: blockDelivery, toolName: "nightly", deliveryFireID: "fire-7", raw: "<<<UNTRUSTED\n[scheduled task nightly completed with stop reason: end_turn]\nfinished cleanly\n<<<UNTRUSTED"}, []string{"⏰ scheduled task nightly — delivery", "fire fire-7", "│ finished cleanly"}, []string{"<<<UNTRUSTED", "completed with stop reason"}},
		{"turn stat", block{kind: blockTurnStat, raw: "↑1.2K ↓340 · 4.1s"}, []string{"↑1.2K ↓340 · 4.1s"}, nil},
	}
	for i := range cases {
		t.Run(cases[i].name, func(t *testing.T) {
			out := stripANSIstr(r.renderBlock(i, &cases[i].b, false))
			for _, want := range cases[i].want {
				if !strings.Contains(out, want) {
					t.Errorf("render = %q, want token %q", out, want)
				}
			}
			for _, bad := range cases[i].bad {
				if strings.Contains(out, bad) {
					t.Errorf("render = %q, unwanted token %q", out, bad)
				}
			}
		})
	}

	plain := block{kind: blockDelivery, toolName: "nightly", raw: "unmatched fence text"}
	if got := stripANSIstr(r.renderBlock(99, &plain, false)); !strings.Contains(got, "unmatched fence text") {
		t.Fatalf("unrecognized delivery body was not preserved: %q", got)
	}
}

func TestMecatuiFunctionalConversationCards_Scenario2_PermanentErrorReplayMatchesLive(t *testing.T) {
	const payload = `POST "https://provider.invalid": 400 Bad Request {"error":{"message":"invalid model"}}`
	msg := client.ResultMsg{Stop: stopError, Error: payload, Permanent: true}

	live, _, _ := newTestModel(t, theme.New("aztec", theme.AztecPalette()))
	live.phase = phaseRunning
	live = applyAll(live, msg)
	liveBlock := live.conv.blocks[len(live.conv.blocks)-1]

	replay := sessionsState{}
	replay.applyReplayEvent(msg)
	if len(replay.transcript.blocks) != 1 {
		t.Fatalf("replay blocks = %d, want 1", len(replay.transcript.blocks))
	}
	replayBlock := replay.transcript.blocks[0]
	if !replayBlock.permanent {
		t.Fatal("persisted permanent result replayed as transient")
	}

	r := newTestRenderer()
	for _, expanded := range []bool{false, true} {
		gotLive := stripANSIstr(r.renderBlock(0, &liveBlock, expanded))
		r.blocks.rendered = nil
		gotReplay := stripANSIstr(r.renderBlock(0, &replayBlock, expanded))
		if gotReplay != gotLive {
			t.Errorf("expanded=%v replay = %q, live = %q", expanded, gotReplay, gotLive)
		}
		if expanded && (!strings.Contains(gotReplay, "raw payload:") || !strings.Contains(gotReplay, payload)) {
			t.Errorf("expanded replay omitted raw payload: %q", gotReplay)
		}
	}
}

func TestMecatuiFunctionalConversationCards_Scenario2_PlainCardsRemainTerminalSafe(t *testing.T) {
	const hostile = "visible\tcolumn\nnext-row\x00\x1b]0;spoof\a\u009d9;title\x7f\u202e\u2066\x1b[2J"
	r := newTestRenderer()
	r.setWidth(48)
	blocks := []block{
		{kind: blockUser, raw: hostile, media: []string{hostile}},
		{kind: blockNotice, raw: hostile},
		{kind: blockHook, raw: hostile, hookPhase: hostile, hookTool: hostile, hookDecision: string(client.HookBlocked)},
		{kind: blockError, raw: hostile},
		{kind: blockError, raw: hostile, permanent: true},
		{kind: blockDelivery, toolName: hostile, deliveryFireID: hostile, raw: hostile},
		{kind: blockTurnStat, raw: hostile},
	}
	for i := range blocks {
		out := stripANSIstr(r.renderBlock(i, &blocks[i], true))
		for _, forbidden := range []string{"\x00", "\a", "\x1b", "\u009d", "\x7f", "\u202e", "\u2066"} {
			if strings.Contains(out, forbidden) {
				t.Fatalf("block %d retained unsafe %q in %q", i, forbidden, out)
			}
		}
		if !strings.Contains(out, "visible") || !strings.Contains(out, "column") || !strings.Contains(out, "next-row") {
			t.Errorf("block %d lost permitted newline/tab content: %q", i, out)
		}
	}

	markdown := stripANSIstr(r.markdown("**markdown**  \nsecond line"))
	if strings.Contains(markdown, "**markdown**") || !strings.Contains(markdown, "markdown") {
		t.Fatalf("assistant Markdown did not retain its Glamour path: %q", markdown)
	}
}

func TestMecatuiFunctionalConversationCards_Scenario2_MarkdownAndInputExceptionsRemainSeparate(t *testing.T) {
	r := newTestRenderer()
	r.setWidth(48)
	assistant := &block{
		kind:      blockAssistant,
		raw:       "## answer ❤️\n\n1. *formatted* item",
		reasoning: "first summary line\nsecond summary line",
	}

	preparedBefore := r.toolCardPrepares
	markdownBefore := r.mdRenders
	collapsed := r.renderBlockFresh(0, assistant, false)
	collapsedPlain := stripANSIstr(collapsed)
	if r.toolCardPrepares != preparedBefore {
		t.Fatalf("assistant rendering prepared a tool card: %d → %d", preparedBefore, r.toolCardPrepares)
	}
	if got, want := r.mdRenders, markdownBefore+1; got != want {
		t.Fatalf("assistant output bypassed Glamour markdown rendering: renders = %d, want %d", got, want)
	}
	if strings.Contains(collapsedPlain, "*formatted*") {
		t.Fatalf("assistant output bypassed Glamour markdown rendering: %q", collapsedPlain)
	}
	if !strings.Contains(collapsedPlain, "reasoning summary · 2 lines · ctrl+t expand") || strings.Contains(collapsedPlain, "first summary line") {
		t.Fatalf("collapsed reasoning escaped its separate summary renderer: %q", collapsedPlain)
	}
	for _, line := range strings.Split(collapsed, "\n") {
		if got, want := ansi.StringWidthWc(line), ansi.StringWidth(line); got != want {
			t.Errorf("assistant line has mismatched emoji widths: WcWidth=%d, GraphemeWidth=%d: %q", got, want, stripANSIstr(line))
		}
	}

	expandedPlain := stripANSIstr(r.renderBlockFresh(0, assistant, true))
	for _, want := range []string{"summary of the model's reasoning", "may not", "reflect its actual process", "first summary line", "second summary line"} {
		if !strings.Contains(expandedPlain, want) {
			t.Errorf("expanded reasoning omitted %q: %q", want, expandedPlain)
		}
	}
	if r.toolCardPrepares != preparedBefore {
		t.Fatalf("expanded assistant rendering prepared a card: %d → %d", preparedBefore, r.toolCardPrepares)
	}

	m, _, _ := newTestModel(t, theme.New("aztec", theme.AztecPalette()))
	m = applyAll(m, tea.WindowSizeMsg{Width: 48, Height: 30}, client.SessionReadyMsg{SessionID: "sess-test-0001"})
	m.prompt.Rewrite("draft")
	m.rend.inputValid = false
	input := m.renderInput()
	if got, want := inputRailStyle(m.deps.Theme, m.inputMode()).GetBackground(), m.deps.Theme.Color("bgPanel"); got != want {
		t.Fatalf("input rail background = %v, want fixed panel background %v", got, want)
	}
	if got, want := lipgloss.Width(input), m.width; got != want {
		t.Errorf("input rail width = %d, want fixed background width %d", got, want)
	}
	if !strings.Contains(input, "48;2;21;32;28") {
		t.Errorf("input rail lost the fixed panel background: %q", input)
	}
}
