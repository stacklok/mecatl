package ui

import (
	"context"
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
)

type compactStub struct {
	compacted bool
	err       error
	calls     []string
}

func (s *compactStub) CompactSession(_ context.Context, id string) (bool, error) {
	s.calls = append(s.calls, id)
	return s.compacted, s.err
}

func compactModel(t *testing.T, compacted bool) (Model, *compactStub, *fakeSender) {
	t.Helper()
	m, send := builtinDispatchModel(t, client.Capabilities{ManualCompaction: true}, false)
	stub := &compactStub{compacted: compacted}
	m.deps.Compactor = stub
	m.conv.addUser("existing transcript")
	return m, stub, send
}

func TestCompactTypedAndPaletteDispatchAgree(t *testing.T) {
	typed, typedStub, _ := compactModel(t, true)
	typed = typeText(t, typed, "/compact")
	typed, typedCmd := pressEnter(t, typed)
	if typedCmd == nil || !typed.compactPending {
		t.Fatalf("typed dispatch pending=%v cmd=%v", typed.compactPending, typedCmd)
	}

	palette, paletteStub, _ := compactModel(t, true)
	palette.prompt.Rewrite("/compact")
	palette.palette.open = true
	palette.palette.filtered = []client.Command{{Name: "compact", Builtin: true}}
	palette.palette.cursor = 0
	mm, paletteCmd, ran := palette.dispatchSelectedBuiltin()
	palette = mm.(Model)
	if !ran || paletteCmd == nil || !palette.compactPending {
		t.Fatalf("palette dispatch ran=%v pending=%v cmd=%v", ran, palette.compactPending, paletteCmd)
	}

	typedMsg := typedCmd().(client.SessionCompactedMsg)
	paletteMsg := paletteCmd().(client.SessionCompactedMsg)
	if typedMsg.SessionID != paletteMsg.SessionID || len(typedStub.calls) != 1 || len(paletteStub.calls) != 1 {
		t.Fatalf("typed=%#v palette=%#v calls=%v/%v", typedMsg, paletteMsg, typedStub.calls, paletteStub.calls)
	}
}

func TestCompactLocalRejectionsNeverReachModel(t *testing.T) {
	cases := []struct {
		name   string
		setup  func(*Model)
		input  string
		status string
	}{
		{"gated off", func(m *Model) { m.caps.ManualCompaction = false }, "/compact", "not available"},
		{"arguments", func(*Model) {}, "/compact now", "does not take arguments"},
		{"active", func(m *Model) { m.phase = phaseRunning }, "/compact", "run is active"},
		{"missing session", func(m *Model) { *m = m.bindSessionID("") }, "/compact", "no active session"},
		{"duplicate", func(m *Model) { m.compactPending = true }, "/compact", "already in progress"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, stub, send := compactModel(t, true)
			tc.setup(&m)
			m.prompt.Rewrite(tc.input)
			var cmd tea.Cmd
			if m.phase == phaseRunning {
				mm, c, _ := m.dispatchBareBuiltin(tc.input)
				m, cmd = mm.(Model), c
			} else {
				mm, c := m.submitPrompt()
				m, cmd = mm.(Model), c
			}
			if cmd != nil || len(stub.calls) != 0 || len(send.frames()) != 0 || !strings.Contains(stripANSIstr(m.statusMsg), tc.status) {
				t.Fatalf("cmd=%v calls=%v frames=%d status=%q", cmd, stub.calls, len(send.frames()), stripANSIstr(m.statusMsg))
			}
		})
	}
}

func TestCompactCompletionPreservesStateAndNextPromptSession(t *testing.T) {
	m, stub, send := compactModel(t, true)
	m.queued = []string{"queued follow-up"}
	m.resolvedSessionModel = client.ResolvedModel{ProviderID: "provider", ModelID: "model"}
	m.prompt.Rewrite("/compact")
	mm, cmd := m.submitPrompt()
	m = mm.(Model)
	beforeBlocks := len(m.conv.blocks)
	m.prompt.Rewrite("do not overtake")
	blocked, blockedCmd := m.submitPrompt()
	m = blocked.(Model)
	if blockedCmd != nil || m.prompt.Value() != "do not overtake" || !strings.Contains(stripANSIstr(m.statusMsg), "wait for session compaction") {
		t.Fatalf("pending prompt overtook compact: cmd=%v input=%q status=%q", blockedCmd, m.prompt.Value(), stripANSIstr(m.statusMsg))
	}
	msg := cmd().(client.SessionCompactedMsg)
	mm, _ = m.Update(msg)
	m = mm.(Model)
	if m.compactPending || len(m.conv.blocks) != beforeBlocks+1 || !strings.Contains(m.conv.blocks[len(m.conv.blocks)-1].raw, "Model history compacted.") {
		t.Fatalf("pending=%v blocks=%#v", m.compactPending, m.conv.blocks)
	}
	if m.prompt.Value() != "do not overtake" || len(m.queued) != 1 || m.resolvedSessionModel.ModelID != "model" {
		t.Fatalf("compact changed compose/model state: input=%q queue=%v model=%+v", m.prompt.Value(), m.queued, m.resolvedSessionModel)
	}
	if len(stub.calls) != 1 || len(send.frames()) != 0 {
		t.Fatalf("compact calls=%v converse frames=%d", stub.calls, len(send.frames()))
	}

	id := m.sessionID
	m.prompt.Rewrite("next prompt")
	mm, promptCmd := m.submitPrompt()
	m = mm.(Model)
	runBatchLeaves(promptCmd)
	frames := send.frames()
	if m.sessionID != id || len(frames) == 0 || frames[0].GetPrompt().GetSessionId() != id {
		t.Fatalf("session=%q frames=%#v", m.sessionID, frames)
	}
}

func TestCompactNoOpErrorAndStaleCompletion(t *testing.T) {
	m, _, _ := compactModel(t, false)
	m.compactPending = true
	m.compactRequestToken = 4
	mm, _ := m.Update(client.SessionCompactedMsg{SessionID: m.sessionID, RequestToken: 4})
	m = mm.(Model)
	if !strings.Contains(m.conv.blocks[len(m.conv.blocks)-1].raw, "already compact") {
		t.Fatalf("notice = %q", m.conv.blocks[len(m.conv.blocks)-1].raw)
	}

	m.compactPending = true
	m.compactRequestToken = 5
	mm, _ = m.Update(client.SessionCompactedMsg{SessionID: m.sessionID, RequestToken: 5, Err: errors.New("busy")})
	m = mm.(Model)
	if m.compactPending || !strings.Contains(stripANSIstr(m.statusMsg), "busy") {
		t.Fatalf("pending=%v status=%q", m.compactPending, stripANSIstr(m.statusMsg))
	}

	m.compactPending = true
	m.compactRequestToken = 6
	m = m.bindSessionID("replacement")
	if m.compactPending {
		t.Fatal("session switch retained compactPending")
	}
	blocks := len(m.conv.blocks)
	status := m.statusMsg
	mm, _ = m.Update(client.SessionCompactedMsg{SessionID: "sess-test-0001", RequestToken: 6, Compacted: true})
	m = mm.(Model)
	if len(m.conv.blocks) != blocks || m.statusMsg != status {
		t.Fatal("stale compact completion mutated replacement session")
	}
	m.prompt.Rewrite("replacement prompt")
	mm, promptCmd := m.submitPrompt()
	m = mm.(Model)
	if promptCmd == nil {
		t.Fatal("replacement session remained blocked after compactPending cleared")
	}
}

func TestCompactSameSessionStaleTokenDoesNotClearPending(t *testing.T) {
	m, _, _ := compactModel(t, true)
	m.compactPending = true
	m.compactRequestToken = 9
	blocks := len(m.conv.blocks)
	mm, _ := m.Update(client.SessionCompactedMsg{SessionID: m.sessionID, RequestToken: 8, Compacted: true})
	m = mm.(Model)
	if !m.compactPending || m.compactRequestToken != 9 || len(m.conv.blocks) != blocks {
		t.Fatalf("stale same-session completion changed state: pending=%v token=%d blocks=%d", m.compactPending, m.compactRequestToken, len(m.conv.blocks))
	}
}
