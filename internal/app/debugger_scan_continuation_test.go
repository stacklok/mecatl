package app

import (
	"context"
	"encoding/json"
	"errors"
	"iter"
	"reflect"
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
	calls             int
	requests          []port.LLMRequest
	cursor            string
	adversarialCursor string
	onRequest         func()
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
	if p.onRequest != nil {
		p.onRequest()
	}
	step := p.calls
	if p.adversarialCursor != "" {
		step--
	}
	var turn mockllm.Turn
	switch step {
	case 0:
		args, _ := json.Marshal(map[string]any{"view": "network", "cursor": p.adversarialCursor})
		turn = mockllm.ToolCallTurn(session.NewToolCall("bad", sessiondebug.ToolName, args))
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
	if len(requests) == 0 || !strings.Contains(requests[0].System.StablePrefix, "result-root next_cursor") || !strings.Contains(requests[0].System.StablePrefix, "empty row page") || !strings.Contains(requests[0].System.StablePrefix, "event window") || !strings.Contains(requests[0].System.StablePrefix, "retry the same request") || !strings.Contains(requests[0].System.StablePrefix, "same cursor if one was supplied") || !strings.Contains(requests[0].System.StablePrefix, "snapshot status/transcript") {
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

type debuggerReadAuditLog struct {
	*memstore.EventLog
	reads []session.SessionID
}

func (l *debuggerReadAuditLog) Read(ctx context.Context, id session.SessionID) iter.Seq2[session.Event, error] {
	l.reads = append(l.reads, id)
	return l.EventLog.Read(ctx, id)
}

func (l *debuggerReadAuditLog) ReadAfter(ctx context.Context, id session.SessionID, after port.Cursor, opts port.ReadOptions) iter.Seq2[port.LogRecord, error] {
	l.reads = append(l.reads, id)
	return l.EventLog.ReadAfter(ctx, id, after, opts)
}

type debuggerNetworkPage struct {
	View            string                          `json:"view"`
	MatchedAttempts int                             `json:"matched_attempts"`
	Attempts        []session.NetworkAttemptPayload `json:"attempts"`
	NextCursor      string                          `json:"next_cursor"`
	EventWindow     struct {
		RecordsScanned int    `json:"records_scanned"`
		StopReason     string `json:"stop_reason"`
	} `json:"event_window"`
}

func decodeDebuggerNetworkPage(t *testing.T, result session.ToolResult) debuggerNetworkPage {
	t.Helper()
	if result.IsError {
		t.Fatalf("network tool error: %s", result.Content)
	}
	start, end := strings.IndexByte(result.Content, '{'), strings.LastIndexByte(result.Content, '}')
	var page debuggerNetworkPage
	if start < 0 || end < start || json.Unmarshal([]byte(result.Content[start:end+1]), &page) != nil || page.View != "network" {
		t.Fatalf("invalid network evidence: %s", result.Content)
	}
	return page
}

func TestDebuggerScanContinuation_Scenario5_AdversarialModel(t *testing.T) {
	for _, name := range []string{"malformed", "wrong-root"} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			store, log, target, _ := debugContinuationFixture(t)
			before, err := store.Load(ctx, target.ID)
			if err != nil {
				t.Fatal(err)
			}
			var beforeEvents []session.Event
			for ev, readErr := range log.Read(ctx, target.ID) {
				if readErr != nil {
					t.Fatal(readErr)
				}
				beforeEvents = append(beforeEvents, ev)
			}

			foreign := session.New("foreign-continuation", target.Mode, target.EnvironmentRef, target.Limits, time.Unix(3, 0))
			if err := store.Save(ctx, foreign); err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 10_000; i++ {
				if _, err := log.AppendEvent(ctx, foreign.ID, session.Event{Type: session.EvTurnStart}); err != nil {
					t.Fatal(err)
				}
			}
			foreignMarker := strings.Repeat("ab", 32)
			foreignAttempt := session.NetworkAttemptPayload{SessionID: foreign.ID, RunSerial: 73, Attempt: 1, MaxAttempts: 3, RetryDisposition: "retryable", StreamProgress: "precommit", Decision: "retry", FailureClass: "connect", CorrelationKind: "request", CorrelationDigest: foreignMarker}
			if _, err := log.AppendEvent(ctx, foreign.ID, session.Event{Type: session.EvNetworkAttempt, NetworkAttempt: &foreignAttempt}); err != nil {
				t.Fatal(err)
			}
			foreignCursor := debuggerContinuationCursor(t, foreign, store, log)
			audit := &debuggerReadAuditLog{EventLog: log}
			// The exact attack token must still reveal the planted evidence when
			// only the inspector's root binding changes to its rightful scope.
			assertRightfulScope := func() {
				t.Helper()
				inspect := sessiondebug.NewBound(foreign.ID, session.DebugTargetFingerprint(foreign), foreign.Owner, false, store, audit)
				args, _ := json.Marshal(map[string]any{"view": "network", "cursor": foreignCursor})
				result, err := inspect.Execute(ctx, session.NewToolCall("control", sessiondebug.ToolName, args), tool.Environment{})
				if err != nil {
					t.Fatal(err)
				}
				page := decodeDebuggerNetworkPage(t, result)
				want := foreignAttempt
				want.SessionID = "" // Session IDs are deliberately absent from network rows.
				if page.MatchedAttempts != 1 || !reflect.DeepEqual(page.Attempts, []session.NetworkAttemptPayload{want}) || page.NextCursor != "" || page.EventWindow.RecordsScanned != 1 || page.EventWindow.StopReason != "end_of_log" {
					t.Fatalf("rightful-scope control did not return the planted suffix: %+v", page)
				}
				if len(audit.reads) == 0 {
					t.Fatal("rightful-scope control did not exercise the event-read audit")
				}
				for _, id := range audit.reads {
					if id != foreign.ID {
						t.Fatalf("rightful-scope control read %q", id)
					}
				}
				audit.reads = nil
			}
			assertRightfulScope()

			badCursor := foreignCursor
			if name == "malformed" {
				badCursor = "tampered"
			}
			var readsAtRequest []int
			provider := &resultDrivenContinuationProvider{
				adversarialCursor: badCursor,
				onRequest:         func() { readsAtRequest = append(readsAtRequest, len(audit.reads)) },
			}
			cfg := isolateConfig(t, Config{Model: "mock-model"})
			factory := debugSessionEngineFactory(cfg, regForTest(provider, providerOpenAI, cfg.Model), provider, store, audit, nil, nil, nil)
			built, err := factory(ctx, server.ProviderSelector{}, server.ProfileNoFS, session.ModeDefault, target.ID, session.DebugTargetFingerprint(target), target.Owner, nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			if built.Engine.HasTool("Read") || built.Engine.HasTool("Shell") {
				t.Fatal("debug continuation gained filesystem authority")
			}
			debug, err := session.NewDebug("debug-adversarial", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindNoFS, ID: "none", Revision: "in-tree-v1"}, session.Limits{}, time.Unix(2, 0), target.ID, target.Incarnation())
			if err != nil {
				t.Fatal(err)
			}
			drainRun(built.Engine.Run(ctx, debug, testEnvironment(nofs.New(), nil), agent.RunRequest{Text: "inspect"}))
			if len(provider.requests) != 4 {
				t.Fatalf("model requests = %d, want rejection, restart, continuation, completion", len(provider.requests))
			}
			if readsAtRequest[0] != 0 || readsAtRequest[1] != 0 || readsAtRequest[2] == 0 || readsAtRequest[3] <= readsAtRequest[2] {
				t.Fatalf("invalid token accessed event data or recovery skipped reads: %v", readsAtRequest)
			}
			for _, id := range audit.reads {
				if id != target.ID {
					t.Fatalf("target-bound model queried unauthorized event data for %q", id)
				}
			}
			for i, req := range provider.requests {
				if len(req.Tools) != 1 || req.Tools[0].Name != sessiondebug.ToolName {
					t.Fatalf("request %d tools = %+v, want exactly InspectSession", i, req.Tools)
				}
				for _, msg := range req.Messages {
					if msg.ToolResult != nil && strings.Contains(msg.ToolResult.Content, foreignMarker) {
						t.Fatalf("request %d exposed foreign evidence to the model", i)
					}
				}
			}
			results := map[session.ToolCallID]session.ToolResult{}
			var calls []session.ToolCall
			for _, msg := range debug.Conversation.Messages {
				calls = append(calls, msg.ToolCalls...)
				if msg.ToolResult != nil {
					results[msg.ToolResult.CallID] = *msg.ToolResult
					if strings.Contains(msg.ToolResult.Content, foreignMarker) {
						t.Fatal("debug transcript exposed foreign evidence")
					}
				}
			}
			if len(calls) != 3 || len(results) != 3 {
				t.Fatalf("tool calls/results = %d/%d, want 3/3", len(calls), len(results))
			}
			for i, want := range []map[string]string{{"view": "network", "cursor": badCursor}, {"view": "network"}, {"view": "network", "cursor": provider.cursor}} {
				var args map[string]string
				if err := json.Unmarshal(calls[i].Args, &args); err != nil || calls[i].Name != sessiondebug.ToolName || !reflect.DeepEqual(args, want) {
					t.Fatalf("call %d did not follow rejection/restart/continuation workflow: %+v, %v", i, calls[i], err)
				}
			}
			if bad := results["bad"]; !bad.IsError || bad.Content != "continuation is invalid or stale; restart this view without cursors" {
				t.Fatalf("invalid token did not fail closed with the recovery instruction: %+v", bad)
			}
			first := decodeDebuggerNetworkPage(t, results["first"])
			if first.MatchedAttempts != 0 || len(first.Attempts) != 0 || first.NextCursor == "" || first.NextCursor != provider.cursor || first.EventWindow.RecordsScanned != 10_000 || first.EventWindow.StopReason != "scan_limit" {
				t.Fatalf("recovery did not restart at the empty initial window and derive its cursor: %+v", first)
			}
			continued := decodeDebuggerNetworkPage(t, results["continued"])
			wantAttempt := session.NetworkAttemptPayload{RunSerial: 1, Attempt: 1, MaxAttempts: 3, RetryDisposition: "retryable", StreamProgress: "precommit", Decision: "retry", FailureClass: "connect"}
			if continued.MatchedAttempts != 1 || !reflect.DeepEqual(continued.Attempts, []session.NetworkAttemptPayload{wantAttempt}) || continued.NextCursor != "" || continued.EventWindow.RecordsScanned != 1 || continued.EventWindow.StopReason != "end_of_log" {
				t.Fatalf("recovery did not reach exactly the target's later evidence: %+v", continued)
			}
			persisted, err := store.Load(ctx, target.ID)
			if err != nil || !reflect.DeepEqual(persisted, before) || !reflect.DeepEqual(target, before) {
				t.Fatalf("full target snapshot mutated: err=%v", err)
			}
			var afterEvents []session.Event
			for ev, readErr := range log.Read(ctx, target.ID) {
				if readErr != nil {
					t.Fatal(readErr)
				}
				afterEvents = append(afterEvents, ev)
			}
			if !reflect.DeepEqual(afterEvents, beforeEvents) {
				t.Fatal("target event log mutated")
			}
			audit.reads = nil
			assertRightfulScope()
		})
	}
}
