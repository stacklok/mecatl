package app

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/nofs"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/server"
	"github.com/stacklok/mecatl/internal/adapter/sessiondebug"
)

func debuggerContinuationCursor(t *testing.T, target *session.Session, store *memstore.Store, log *memstore.EventLog) string {
	t.Helper()
	inspect := sessiondebug.NewBound(target.ID, session.DebugTargetFingerprint(target), target.Owner, false, store, log)
	result, err := inspect.Execute(context.Background(), session.NewToolCall("seed", sessiondebug.ToolName, json.RawMessage(`{"view":"network"}`)), tool.Environment{})
	if err != nil || result.IsError {
		t.Fatalf("seed continuation: err=%v result=%s", err, result.Content)
	}
	start, end := strings.IndexByte(result.Content, '{'), strings.LastIndexByte(result.Content, '}')
	var out map[string]any
	if start < 0 || end < start || json.Unmarshal([]byte(result.Content[start:end+1]), &out) != nil {
		t.Fatalf("decode seed continuation: %s", result.Content)
	}
	cursor, _ := out["next_cursor"].(string)
	if cursor == "" {
		t.Fatalf("seed result omitted continuation: %s", result.Content)
	}
	return cursor
}

func debugContinuationFixture(t *testing.T) (*memstore.Store, *memstore.EventLog, *session.Session, string) {
	t.Helper()
	store := memstore.New()
	target := session.New("target-continuation", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/target", Revision: "in-tree-v1"}, session.Limits{}, time.Unix(1, 0))
	if err := store.Save(context.Background(), target); err != nil {
		t.Fatal(err)
	}
	log := memstore.NewEventLog()
	for i := 0; i < 10_000; i++ {
		if _, err := log.AppendEvent(context.Background(), target.ID, session.Event{Type: session.EvTurnStart}); err != nil {
			t.Fatal(err)
		}
	}
	attempt := session.NetworkAttemptPayload{SessionID: target.ID, RunSerial: 1, Attempt: 1, MaxAttempts: 3, RetryDisposition: "retryable", StreamProgress: "precommit", Decision: "retry", FailureClass: "connect"}
	if _, err := log.AppendEvent(context.Background(), target.ID, session.Event{Type: session.EvNetworkAttempt, NetworkAttempt: &attempt}); err != nil {
		t.Fatal(err)
	}
	return store, log, target, debuggerContinuationCursor(t, target, store, log)
}

func TestDebuggerScanContinuation_Scenario5_ModelWorkflow(t *testing.T) {
	store, log, target, cursor := debugContinuationFixture(t)
	var requests []port.LLMRequest
	provider := mockllm.NewWith([]mockllm.Option{mockllm.WithRequestObserver(func(req port.LLMRequest) { requests = append(requests, req) })},
		mockllm.ToolCallTurn(session.NewToolCall("first", sessiondebug.ToolName, json.RawMessage(`{"view":"network"}`))),
		mockllm.ToolCallTurn(session.NewToolCall("continued", sessiondebug.ToolName, json.RawMessage(`{"view":"network","cursor":"`+cursor+`"}`))),
		mockllm.TextTurn("found later evidence"))
	cfg := Config{Model: "mock-model"}
	factory := debugSessionEngineFactory(cfg, regForTest(provider, providerOpenAI, cfg.Model), provider, store, log, nil, nil, nil)
	built, err := factory(context.Background(), server.ProviderSelector{}, server.ProfileNoFS, session.ModeDefault, target.ID, session.DebugTargetFingerprint(target), target.Owner, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	debug, err := session.NewDebug("debug-continuation", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindNoFS, ID: "none", Revision: "in-tree-v1"}, session.Limits{}, time.Unix(2, 0), target.ID, target.Incarnation())
	if err != nil {
		t.Fatal(err)
	}
	drainRun(built.Engine.Run(context.Background(), debug, testEnvironment(nofs.New(), nil), agent.RunRequest{Text: "find later network evidence"}))
	if len(requests) == 0 || !strings.Contains(requests[0].System.StablePrefix, "result-root next_cursor") || !strings.Contains(requests[0].System.StablePrefix, "empty row page") || !strings.Contains(requests[0].System.StablePrefix, "event window") {
		t.Fatalf("real debug factory omitted continuation workflow: %+v", requests)
	}
	found := false
	for _, message := range debug.Conversation.Messages {
		if message.ToolResult != nil && strings.Contains(message.ToolResult.Content, `"matched_attempts":1`) {
			found = true
		}
	}
	if !found {
		t.Fatalf("model did not reach later-window evidence: %+v", debug.Conversation.Messages)
	}
}

func TestDebuggerScanContinuation_Scenario5_AdversarialModel(t *testing.T) {
	store, log, target, cursor := debugContinuationFixture(t)
	provider := mockllm.New(mockllm.ToolCallTurn(session.NewToolCall("bad", sessiondebug.ToolName, json.RawMessage(`{"view":"network","cursor":"tampered"}`))), mockllm.ToolCallTurn(session.NewToolCall("recover", sessiondebug.ToolName, json.RawMessage(`{"view":"network","cursor":"`+cursor+`"}`))), mockllm.TextTurn("recovered"))
	cfg := Config{Model: "mock-model"}
	factory := debugSessionEngineFactory(cfg, regForTest(provider, providerOpenAI, cfg.Model), provider, store, log, nil, nil, nil)
	built, err := factory(context.Background(), server.ProviderSelector{}, server.ProfileNoFS, session.ModeDefault, target.ID, session.DebugTargetFingerprint(target), target.Owner, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if built.Engine.HasTool("Read") || built.Engine.HasTool("Shell") {
		t.Fatal("debug continuation gained filesystem authority")
	}
	debug, _ := session.NewDebug("debug-adversarial", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindNoFS, ID: "none", Revision: "in-tree-v1"}, session.Limits{}, time.Unix(2, 0), target.ID, target.Incarnation())
	drainRun(built.Engine.Run(context.Background(), debug, testEnvironment(nofs.New(), nil), agent.RunRequest{Text: "inspect"}))
	persisted, err := store.Load(context.Background(), target.ID)
	if err != nil || len(persisted.Conversation.Messages) != len(target.Conversation.Messages) {
		t.Fatalf("target mutated: err=%v target=%+v", err, persisted)
	}
	var rejected, recovered bool
	for _, message := range debug.Conversation.Messages {
		if message.ToolResult != nil {
			rejected = rejected || strings.Contains(message.ToolResult.Content, "continuation is invalid or stale")
			recovered = recovered || strings.Contains(message.ToolResult.Content, `"matched_attempts":1`)
		}
	}
	if !rejected || !recovered {
		t.Fatalf("adversarial recovery rejected=%v recovered=%v messages=%+v", rejected, recovered, debug.Conversation.Messages)
	}
}
