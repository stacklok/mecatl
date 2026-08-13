package agent_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/prompt"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

type scriptedProfileSource struct {
	mu      sync.Mutex
	results []profileSourceResult
	calls   int
}
type profileSourceResult struct {
	entries []tool.MemoryEntry
	err     error
}

func (s *scriptedProfileSource) List(context.Context, string) ([]tool.MemoryEntry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.results[min(s.calls, len(s.results)-1)]
	s.calls++
	return append([]tool.MemoryEntry(nil), r.entries...), r.err
}

func TestOperatorProfileRefreshAndCustomBuilder(t *testing.T) {
	first := tool.MemoryEntry{Key: "user/editor", Value: "vim"}
	second := first
	second.Value = "zed"
	src := &scriptedProfileSource{results: []profileSourceResult{{entries: []tool.MemoryEntry{first}}, {entries: []tool.MemoryEntry{second}}}}
	var cfgs []prompt.Config
	eng := newEngine(agent.Deps{LLM: mockllm.New(mockllm.ToolCallTurn(toolCall("c1", "Ping", `{}`)), mockllm.TextTurn("done")), Catalog: catalogWith(t, &fakeTool{name: "Ping", exec: okExec}), OperatorProfileSource: src, PromptBuilder: func(c prompt.Config) prompt.Layered { cfgs = append(cfgs, c); return prompt.Build(c) }})
	sess := session.New("profile-refresh", session.ModeDefault, "/ws", session.Limits{}, time.Unix(0, 0))
	drain(eng.Run(context.Background(), sess, agent.MemEnv("/ws"), agent.RunRequest{Text: "hi"}))
	if src.calls != 2 || len(cfgs) != 2 || cfgs[0].OperatorProfile.Entries[0].Value != "vim" || cfgs[1].OperatorProfile.Entries[0].Value != "zed" {
		t.Fatalf("refresh calls/configs = %d/%#v", src.calls, cfgs)
	}
	for _, m := range sess.Conversation.Messages {
		if strings.Contains(m.Text, "operator-profile") {
			t.Fatalf("profile persisted: %q", m.Text)
		}
	}
}

func TestOperatorProfilePreservesTurnZeroProjectMemoryIndex(t *testing.T) {
	fact := tool.MemoryEntry{Key: "user/editor", Value: "zed"}
	src := &scriptedProfileSource{results: []profileSourceResult{{entries: []tool.MemoryEntry{fact}}}}
	assembler := &fakeAssembler{msg: "<memory-index>project/deploy — manual gate</memory-index>"}
	var request port.LLMRequest
	llm := mockllm.NewWith([]mockllm.Option{mockllm.WithRequestObserver(func(got port.LLMRequest) { request = got })}, mockllm.TextTurn("done"))
	eng := newEngine(agent.Deps{LLM: llm, Catalog: tool.NewCatalog(), Instructions: assembler, OperatorProfileSource: src})
	sess := session.New("profile-index", session.ModeDefault, "/ws", session.Limits{}, time.Unix(0, 0))
	drain(eng.Run(context.Background(), sess, agent.MemEnv("/ws"), agent.RunRequest{Text: "deploy"}))
	if assembler.called != 1 || len(request.Messages) < 2 || !strings.Contains(request.Messages[0].Text, "project/deploy") {
		t.Fatalf("project-memory fragment lost: called=%d messages=%#v", assembler.called, request.Messages)
	}
	if !strings.Contains(request.System.VolatileSuffix, "zed") {
		t.Fatalf("operator profile absent from volatile suffix: %q", request.System.VolatileSuffix)
	}
	for _, message := range sess.Conversation.Messages {
		if strings.Contains(message.Text, "project/deploy") || strings.Contains(message.Text, "zed") {
			t.Fatalf("ephemeral profile/index persisted: %q", message.Text)
		}
	}
}

func TestOperatorProfileFailureFallbackWarnOnce(t *testing.T) {
	fact := tool.MemoryEntry{Key: "user/locale", Value: "français"}
	src := &scriptedProfileSource{results: []profileSourceResult{{err: errors.New("first")}, {entries: []tool.MemoryEntry{fact}}, {err: errors.New("again")}}}
	var suffixes []string
	llm := mockllm.NewWith([]mockllm.Option{mockllm.WithRequestObserver(func(r port.LLMRequest) { suffixes = append(suffixes, r.System.VolatileSuffix) })}, mockllm.ToolCallTurn(toolCall("c1", "Ping", `{}`)), mockllm.ToolCallTurn(toolCall("c2", "Ping", `{}`)), mockllm.TextTurn("done"))
	diag := newCapturingDiag()
	eng := newEngine(agent.Deps{LLM: llm, Catalog: catalogWith(t, &fakeTool{name: "Ping", exec: okExec}), OperatorProfileSource: src, Diagnostics: diag})
	sess := session.New("profile-fail", session.ModeDefault, "/ws", session.Limits{}, time.Unix(0, 0))
	drain(eng.Run(context.Background(), sess, agent.MemEnv("/ws"), agent.RunRequest{Text: "hi"}))
	if len(suffixes) != 3 || strings.Contains(suffixes[0], "operator-profile") || !strings.Contains(suffixes[1], "français") || suffixes[1] != suffixes[2] {
		t.Fatalf("suffixes = %#v", suffixes)
	}
	warns := 0
	for _, r := range diag.snapshot() {
		if r.level == port.LevelWarn && strings.Contains(r.msg, "operator-profile refresh failed") {
			warns++
			if r.attrs["session"] != "profile-fail" {
				t.Fatalf("uncorrelated warning: %#v", r.attrs)
			}
		}
	}
	if warns != 1 {
		t.Fatalf("warns=%d", warns)
	}
}
