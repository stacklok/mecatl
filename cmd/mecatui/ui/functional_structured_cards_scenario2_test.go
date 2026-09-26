package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/theme"
	"github.com/stacklok/mecatl/cmd/mecatui/ui/internal/scrollback"
)

func testSnapshot(id int, payload scrollback.PayloadSnapshot) scrollback.BlockSnapshot {
	return scrollback.BlockSnapshot{ID: scrollback.BlockID(id + 1), Payload: payload}
}

func TestMecatuiFunctionalConversationCards_Scenario2_PerFamilyPreparedBlocksPreserveSemantics(t *testing.T) {
	r := newTestRenderer()
	r.setWidth(42)
	cases := []struct {
		name      string
		payload   scrollback.PayloadSnapshot
		want, bad []string
	}{
		{"user media", scrollback.UserCardSnapshot{Text: "hello\nworld", Media: []string{"image/png (inline)", "audio/wav (inline)"}}, []string{"▌ you", "hello", "world", "📎 image/png", "📎 audio/wav"}, nil},
		{"ordinary notice", scrollback.NoticeCardSnapshot{Text: "compacted"}, []string{"• compacted"}, nil},
		{"recovery notice", scrollback.NoticeCardSnapshot{Text: "recover this turn", Recover: true}, []string{"⚠ recover this turn"}, nil},
		{"hook", scrollback.HookCardSnapshot{Text: "PreToolUse hook rewrote tool arguments", Phase: "PreToolUse", Tool: "Shell", Decision: string(client.HookModified)}, []string{"✎ hook PreToolUse · Shell: modified", "rewrote tool arguments"}, []string{"PreToolUse hook rewrote"}},
		{"transient error", scrollback.ErrorCardSnapshot{Text: "upstream unavailable"}, []string{"✗ upstream unavailable"}, []string{"retrying won't help"}},
		{"permanent error", scrollback.ErrorCardSnapshot{Text: `POST "https://provider.invalid": 400 Bad Request {"error":{"message":"bad model"}}`, Permanent: true}, []string{"✗ 400 Bad Request: bad model", "retrying", "won't help", r.marks.expandTools + " shows details"}, []string{"raw payload:"}},
		{"delivery", scrollback.DeliveryCardSnapshot{ScheduleName: "nightly", FireID: "fire-7", Text: "<<<UNTRUSTED\n[scheduled task nightly completed with stop reason: end_turn]\nfinished cleanly\n<<<UNTRUSTED"}, []string{"⏰ scheduled task nightly — delivery", "fire fire-7", "│ finished cleanly"}, []string{"<<<UNTRUSTED", "completed with stop reason"}},
		{"turn stat", scrollback.TurnStatCardSnapshot{Text: "↑1.2K ↓340 · 4.1s"}, []string{"↑1.2K ↓340 · 4.1s"}, nil},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := stripANSIstr(r.renderSnapshot(i, testSnapshot(i, tc.payload), false))
			for _, want := range tc.want {
				if !strings.Contains(out, want) {
					t.Errorf("render=%q want %q", out, want)
				}
			}
			for _, bad := range tc.bad {
				if strings.Contains(out, bad) {
					t.Errorf("render=%q unwanted %q", out, bad)
				}
			}
		})
	}
	plain := testSnapshot(99, scrollback.DeliveryCardSnapshot{ScheduleName: "nightly", Text: "unmatched fence text"})
	if got := stripANSIstr(r.renderSnapshot(99, plain, false)); !strings.Contains(got, "unmatched fence text") {
		t.Fatalf("delivery body lost: %q", got)
	}
}

func TestMecatuiFunctionalConversationCards_Scenario2_PermanentErrorReplayMatchesLive(t *testing.T) {
	const payload = `POST "https://provider.invalid": 400 Bad Request {"error":{"message":"invalid model"}}`
	msg := client.ResultMsg{Stop: stopError, Error: payload, Permanent: true}
	live, _, _ := newTestModel(t, theme.New("aztec", theme.AztecPalette()))
	live.phase = phaseRunning
	live = applyAll(live, msg)
	liveCard := live.conv.testBlocks()[len(live.conv.testBlocks())-1]
	replay := sessionsState{}
	replay.applyReplayEvent(msg)
	replayCard := replay.transcript.testBlocks()[0]
	if p, ok := replayCard.Payload.(scrollback.ErrorCardSnapshot); !ok || !p.Permanent {
		t.Fatal("persisted permanent result replayed as transient")
	}
	r := newTestRenderer()
	for _, expanded := range []bool{false, true} {
		gotLive := stripANSIstr(r.renderSnapshot(0, liveCard, expanded))
		r.blocks.rendered = nil
		gotReplay := stripANSIstr(r.renderSnapshot(0, replayCard, expanded))
		if gotReplay != gotLive {
			t.Errorf("replay=%q live=%q", gotReplay, gotLive)
		}
		if expanded && (!strings.Contains(gotReplay, "raw payload:") || !strings.Contains(gotReplay, payload)) {
			t.Errorf("expanded omitted payload")
		}
	}
}

func TestMecatuiFunctionalConversationCards_Scenario2_PlainCardsRemainTerminalSafe(t *testing.T) {
	const hostile = "visible\tcolumn\nnext-row\x00\x1b]0;spoof\a\u009d9;title\x7f\u202e\u2066\x1b[2J"
	r := newTestRenderer()
	r.setWidth(48)
	payloads := []scrollback.PayloadSnapshot{scrollback.UserCardSnapshot{Text: hostile, Media: []string{hostile}}, scrollback.NoticeCardSnapshot{Text: hostile}, scrollback.HookCardSnapshot{Text: hostile, Phase: hostile, Tool: hostile, Decision: string(client.HookBlocked)}, scrollback.ErrorCardSnapshot{Text: hostile}, scrollback.ErrorCardSnapshot{Text: hostile, Permanent: true}, scrollback.DeliveryCardSnapshot{ScheduleName: hostile, FireID: hostile, Text: hostile}, scrollback.TurnStatCardSnapshot{Text: hostile}}
	for i, p := range payloads {
		out := stripANSIstr(r.renderSnapshot(i, testSnapshot(i, p), true))
		for _, forbidden := range []string{"\x00", "\a", "\x1b", "\u009d", "\x7f", "\u202e", "\u2066"} {
			if strings.Contains(out, forbidden) {
				t.Fatalf("card retained unsafe %q", forbidden)
			}
		}
	}
}

func TestMecatuiFunctionalConversationCards_Scenario2_MarkdownAndInputExceptionsRemainSeparate(t *testing.T) {
	r := newTestRenderer()
	r.setWidth(48)
	assistant := testSnapshot(0, scrollback.AssistantCardSnapshot{Text: "## answer ❤️\n\n1. *formatted* item", Reasoning: "first summary line\nsecond summary line"})
	markdownBefore := r.mdRenders
	collapsed := r.renderSnapshot(0, assistant, false)
	collapsedPlain := stripANSIstr(collapsed)
	if r.mdRenders != markdownBefore+1 || strings.Contains(collapsedPlain, "*formatted*") {
		t.Fatal("assistant bypassed Markdown")
	}
	if !strings.Contains(collapsedPlain, "reasoning summary · 2 lines · ctrl+t expand") {
		t.Fatal("reasoning summary missing")
	}
	for _, line := range strings.Split(collapsed, "\n") {
		if ansi.StringWidthWc(line) != ansi.StringWidth(line) {
			t.Error("emoji widths differ")
		}
	}
	expanded := stripANSIstr(r.renderSnapshot(0, assistant, true))
	if !strings.Contains(expanded, "first summary line") {
		t.Fatal("expanded reasoning missing")
	}
	m, _, _ := newTestModel(t, theme.New("aztec", theme.AztecPalette()))
	m = applyAll(m, tea.WindowSizeMsg{Width: 48, Height: 30}, client.SessionReadyMsg{SessionID: "sess-test-0001"})
	m.prompt.Rewrite("draft")
	input := m.renderInput()
	if inputRailStyle(m.deps.Theme, m.inputMode()).GetBackground() != m.deps.Theme.Color("bgPanel") || lipgloss.Width(input) != m.width {
		t.Fatal("input rail changed")
	}
}
