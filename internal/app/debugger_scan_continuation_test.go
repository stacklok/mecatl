package app

import (
	"context"
	"encoding/json"
	"errors"
	"iter"
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

type resultDrivenContinuationProvider struct {
	calls    int
	requests []port.LLMRequest
	cursor   string
}

func (*resultDrivenContinuationProvider) Capabilities() port.ProviderCapabilities {
	return port.ProviderCapabilities{}
}

func cursorFromActualToolResult(req port.LLMRequest) (string, error) {
	for i := len(req.Messages) - 1; i >= 0; i-- {
		result := req.Messages[i].ToolResult
		if result == nil {
			continue
		}
		start, end := strings.IndexByte(result.Content, '{'), strings.LastIndexByte(result.Content, '}')
		if start < 0 || end < start {
			continue
		}
		var out struct {
			NextCursor string `json:"next_cursor"`
		}
		if json.Unmarshal([]byte(result.Content[start:end+1]), &out) == nil && out.NextCursor != "" {
			return out.NextCursor, nil
		}
	}
	return "", errors.New("previous actual tool result omitted next_cursor")
}

func (p *resultDrivenContinuationProvider) Stream(ctx context.Context, req port.LLMRequest) (iter.Seq2[port.Chunk, error], error) {
	p.requests = append(p.requests, req)
	p.calls++
	var turn mockllm.Turn
	switch p.calls {
	case 1:
		turn = mockllm.ToolCallTurn(session.NewToolCall("first", sessiondebug.ToolName, json.RawMessage(`{"view":"network"}`)))
	case 2:
		cursor, err := cursorFromActualToolResult(req)
		if err != nil {
			return nil, err
		}
		p.cursor = cursor
		args, _ := json.Marshal(map[string]any{"view": "network", "cursor": cursor})
		turn = mockllm.ToolCallTurn(session.NewToolCall("continued", sessiondebug.ToolName, args))
	default:
		turn = mockllm.TextTurn("found later evidence")
	}
	return mockllm.New(turn).Stream(ctx, req)
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
	store, log, target, _ := debugContinuationFixture(t)
	provider := &resultDrivenContinuationProvider{}
	cfg := isolateConfig(t, Config{Model: "mock-model"})
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
	requests := provider.requests
	if provider.cursor == "" {
		t.Fatal("second tool call did not derive a cursor from the actual first result")
	}
	actualCursor, err := cursorFromActualToolResult(requests[1])
	if err != nil || actualCursor != provider.cursor {
		t.Fatalf("derived cursor %q does not equal actual tool-result cursor %q: %v", provider.cursor, actualCursor, err)
	}
	if _, err := cursorFromActualToolResult(port.LLMRequest{Messages: []session.Message{session.NewToolMessage(session.NewToolResult("missing", `{"view":"network"}`))}}); err == nil {
		t.Fatal("provider negative control accepted a prior tool result without next_cursor")
	}
	if len(requests) == 0 || !strings.Contains(requests[0].System.StablePrefix, "result-root next_cursor") || !strings.Contains(requests[0].System.StablePrefix, "empty row page") || !strings.Contains(requests[0].System.StablePrefix, "event window") || !strings.Contains(requests[0].System.StablePrefix, "retry the same cursor") || !strings.Contains(requests[0].System.StablePrefix, "snapshot status/transcript") {
		t.Fatalf("real debug factory omitted continuation workflow: %+v", requests)
	}
	var inspectSchema string
	for _, spec := range requests[0].Tools {
		if spec.Name == sessiondebug.ToolName {
			inspectSchema = string(spec.Schema)
		}
	}
	if !strings.Contains(inspectSchema, `"cursor"`) || !strings.Contains(inspectSchema, `"maxLength":16384`) {
		t.Fatalf("actual provider request omitted continuation schema: %s", inspectSchema)
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
