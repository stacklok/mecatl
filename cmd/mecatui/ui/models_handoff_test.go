package ui

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/theme"
)

func newModelSwitchHandoff(t *testing.T, loader client.SessionTranscripter) (Model, *fakeConv) {
	t.Helper()
	conv := &fakeConv{recv: &fakeRecver{gate: make(chan struct{})}, send: &fakeSender{}, caps: modelsCaps(), echoSelAsResolved: true, createCount: 1}
	m := New(Deps{
		Session: conv, Conv: conv, Transcript: loader, Theme: theme.New("aztec", theme.AztecPalette()),
		Models: sampleModels(), Ctx: context.Background(), Mode: "default", NoAltScreen: true,
	})
	m = applyAll(m, tea.WindowSizeMsg{Width: 100, Height: 30}, client.SessionReadyMsg{
		SessionID: "source", Capabilities: modelsCaps(), ResolvedModel: client.ResolvedModel{ProviderID: "openai", ModelID: "gpt-5"},
	})
	m.conv.addUser("local source only")
	m.conv.appendAssistant("local assistant only")
	m.sessionTitle, m.sessionState, m.sessionCreatedAt = "Source title", "completed", 42
	m.refreshView()
	return m, conv
}

func TestModelSwitchAdoptsAuthoritativeTargetTranscript(t *testing.T) {
	loader := &handoffTranscriptLoader{transcript: client.SessionTranscript{
		SessionID: "sess-test-0002", Complete: true,
		Messages: []client.ConversationMessage{
			{Role: "user", Text: "target user"},
			{Role: "assistant", Text: "target assistant before tool", ToolCalls: []client.ConvToolCall{{ID: "call-1", Name: "Read", Args: `{"path":"target.txt"}`}}},
			{Role: "tool", ToolResult: &client.ConvToolResult{CallID: "call-1", Content: "target tool result"}},
			{Role: "assistant", Text: "target assistant after tool"},
		},
	}}
	m, conv := newModelSwitchHandoff(t, loader)
	loader.conv = conv

	mm, cmd, handled := m.chooseModel(client.ModelSelection{ProviderID: "openrouter", ModelID: "anthropic/claude"}, "Claude")
	m = mm.(Model)
	if !handled || m.phase != phaseConnecting || m.sessionID != "source" {
		t.Fatalf("handoff state = phase:%v session:%q, want connecting source", m.phase, m.sessionID)
	}
	if !strings.Contains(stripANSIstr(m.View().Content), "local source only") {
		t.Fatal("source projection must remain visible while target hydrates")
	}
	m.ta.Rewrite("must not send")
	before := m.ta.Value()
	mm, keyCmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = mm.(Model)
	if keyCmd != nil || m.ta.Value() != before {
		t.Fatal("connecting handoff must not accept input or open Converse")
	}

	m = feedCmd(t, m, cmd)
	if m.phase != phaseIdle || m.sessionID != "sess-test-0002" {
		t.Fatalf("adopted state = phase:%v session:%q, want idle target", m.phase, m.sessionID)
	}
	view := stripANSIstr(m.View().Content)
	for _, text := range []string{"target user", "target assistant before tool", "Read", "target tool result", "target assistant after tool"} {
		if !strings.Contains(view, text) {
			t.Fatalf("adopted transcript omitted %q:\n%s", text, view)
		}
	}
	if strings.Contains(view, "local source only") {
		t.Fatalf("conversation must be target-authoritative:\n%s", view)
	}
	if len(m.conv.blocks) != 4 {
		t.Fatalf("adopted transcript blocks = %d, want user, assistant, tool, assistant", len(m.conv.blocks))
	}
	tool := m.conv.blocks[2]
	if m.conv.blocks[0].kind != blockUser || m.conv.blocks[1].kind != blockAssistant || tool.kind != blockTool || m.conv.blocks[3].kind != blockAssistant {
		t.Fatalf("adopted block order = %#v", m.conv.blocks)
	}
	if tool.toolName != "Read" || tool.toolArgs != `{"path":"target.txt"}` || !tool.resolved || tool.resultBody != "target tool result" {
		t.Fatalf("adopted tool block = %#v", tool)
	}
	if got, want := conv.ops(), []string{"create", "transcript", "close"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("handoff operation order = %v, want %v", got, want)
	}
	if got := conv.closed(); !reflect.DeepEqual(got, []string{"source"}) {
		t.Fatalf("closed sessions = %v, want source after hydration", got)
	}
}

func TestModelSwitchHandoffFailuresRetainSource(t *testing.T) {
	cases := []struct {
		name   string
		setup  func(*fakeConv, *handoffTranscriptLoader)
		closed []string
	}{
		{"create", func(c *fakeConv, _ *handoffTranscriptLoader) { c.createErr = errors.New("create failed") }, nil},
		{"rpc", func(_ *fakeConv, l *handoffTranscriptLoader) { l.err = errors.New("transcript failed") }, []string{"sess-test-0002"}},
		{"incomplete", func(_ *fakeConv, l *handoffTranscriptLoader) { l.transcript.Complete = false }, []string{"sess-test-0002"}},
		{"mismatched", func(_ *fakeConv, l *handoffTranscriptLoader) { l.transcript.SessionID = "wrong" }, []string{"sess-test-0002"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			loader := &handoffTranscriptLoader{transcript: client.SessionTranscript{SessionID: "sess-test-0002", Complete: true}}
			m, conv := newModelSwitchHandoff(t, loader)
			loader.conv = conv
			tc.setup(conv, loader)
			mm, cmd, _ := m.chooseModel(client.ModelSelection{ProviderID: "openai", ModelID: "gpt-5-mini"}, "GPT-5 mini")
			m = feedCmd(t, mm.(Model), cmd)
			if m.phase != phaseIdle || m.sessionID != "source" || m.sessionTitle != "Source title" || m.sessionState != "completed" || m.sessionCreatedAt != 42 {
				t.Fatalf("failure must retain source exactly: phase:%v id:%q title:%q state:%q created:%d", m.phase, m.sessionID, m.sessionTitle, m.sessionState, m.sessionCreatedAt)
			}
			if !strings.Contains(stripANSIstr(m.View().Content), "local source only") || strings.Contains(stripANSIstr(m.statusMsg), "conversation kept") {
				t.Fatalf("failure must preserve source and never claim carryover: view=%q status=%q", m.View().Content, m.statusMsg)
			}
			if got := conv.closed(); !reflect.DeepEqual(got, tc.closed) {
				t.Fatalf("closed sessions = %v, want %v", got, tc.closed)
			}
		})
	}
}

func TestModelSwitchSourceCloseFailureDoesNotUndoTarget(t *testing.T) {
	loader := &handoffTranscriptLoader{transcript: client.SessionTranscript{SessionID: "sess-test-0002", Complete: true, Messages: []client.ConversationMessage{{Role: "assistant", Text: "target"}}}}
	m, conv := newModelSwitchHandoff(t, loader)
	loader.conv = conv
	conv.closeErr = errors.New("source close failed")
	mm, cmd, _ := m.chooseModel(client.ModelSelection{ProviderID: "openai", ModelID: "gpt-5-mini"}, "GPT-5 mini")
	m = feedCmd(t, mm.(Model), cmd)
	if m.phase != phaseIdle || m.sessionID != "sess-test-0002" || !strings.Contains(stripANSIstr(m.View().Content), "target") {
		t.Fatalf("source close failure must keep adopted target: phase:%v id:%q", m.phase, m.sessionID)
	}
}

func TestModelSwitchIgnoresStaleHandoffResult(t *testing.T) {
	m, _ := newModelSwitchHandoff(t, modelSwitchTranscriptLoader{})
	m.modelSwitchRequestToken = 2
	m.phase = phaseIdle
	mm, _ := m.Update(modelSwitchReadyMsg{token: 1, sourceID: "source", ready: client.SessionReadyMsg{SessionID: "stale"}, transcript: client.SessionTranscript{SessionID: "stale", Complete: true}})
	m = mm.(Model)
	if m.sessionID != "source" || m.phase != phaseIdle {
		t.Fatalf("stale handoff result mutated source: phase:%v id:%q", m.phase, m.sessionID)
	}
}

func TestModelSwitchIgnoresStaleHandoffFailure(t *testing.T) {
	m, _ := newModelSwitchHandoff(t, modelSwitchTranscriptLoader{})
	m.modelSwitchRequestToken = 2
	m.phase = phaseConnecting
	m.statusMsg = "current handoff"
	m.pendingModelSwitchNote = "current receipt"

	mm, cmd := m.Update(modelSwitchFailedMsg{token: 1, sourceID: "source", model: "stale", err: errors.New("stale failure")})
	m = mm.(Model)
	if cmd != nil {
		t.Fatal("stale failure must not rearm the source live feed")
	}
	if m.phase != phaseConnecting || m.sessionID != "source" || m.statusMsg != "current handoff" || m.pendingModelSwitchNote != "current receipt" {
		t.Fatalf("stale failure altered current handoff: phase:%v id:%q status:%q note:%q", m.phase, m.sessionID, m.statusMsg, m.pendingModelSwitchNote)
	}
}

func TestModelSwitchWithoutTranscriptRetainsSourceAndCleansTarget(t *testing.T) {
	m, conv := newModelSwitchHandoff(t, nil)
	mm, cmd, _ := m.chooseModel(client.ModelSelection{ProviderID: "openai", ModelID: "gpt-5-mini"}, "GPT-5 mini")
	m = feedCmd(t, mm.(Model), cmd)

	if m.phase != phaseIdle || m.sessionID != "source" || m.sessionTitle != "Source title" || m.sessionState != "completed" || m.sessionCreatedAt != 42 {
		t.Fatalf("missing transcript must retain source exactly: phase:%v id:%q title:%q state:%q created:%d", m.phase, m.sessionID, m.sessionTitle, m.sessionState, m.sessionCreatedAt)
	}
	if !strings.Contains(stripANSIstr(m.View().Content), "local source only") || strings.Contains(stripANSIstr(m.statusMsg), "conversation kept") {
		t.Fatalf("missing transcript must preserve source without a success receipt: view=%q status=%q", m.View().Content, m.statusMsg)
	}
	if got, want := conv.ops(), []string{"create", "close"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("missing transcript operations = %v, want target-only cleanup %v", got, want)
	}
	if got, want := conv.closed(), []string{"sess-test-0002"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("missing transcript closed sessions = %v, want only unused target %v", got, want)
	}
}
