package app

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/permconfig"
	"github.com/stacklok/mecatl/internal/adapter/server"
	"github.com/stacklok/mecatl/internal/adapter/store/jsonlstore"
)

func recoveryAppConfig(t *testing.T, endpoint string) Config {
	t.Helper()
	return isolateConfig(t, Config{
		Workspace: t.TempDir(), StoreDir: t.TempDir(), NoSoul: true, NoUserModel: true,
		Model: "gpt-5", UseOpenAI: true, OpenAIKey: "test", AllowAllTools: true,
		ContextWindowOverride: defaultContextWindowTokens,
		ProviderOverrides:     permconfig.ProviderOverrides{providerOpenAI: {BaseURL: endpoint + "/v1"}},
		envDetector:           fakeEnv(nil), liveModelHTTPClient: offlineHTTPClient(),
		LLMMaxAttempts: 3, LLMRecoveryBudget: 5 * time.Second,
		LLMBreakerThreshold: 1, LLMBreakerCooldown: 500 * time.Millisecond,
	})
}

// The recorder is called by the real dispatch path, not by provider replay.
// Counting successful AND failed executions catches a repeated create-only Write.
type recoveryToolRecorder struct {
	mu           sync.Mutex
	writes       int
	writeSession session.SessionID
}

func (r *recoveryToolRecorder) ToolCall(id session.SessionID, call session.ToolCall, _ session.ToolResult, _, _ time.Duration) {
	if call.Name == "Write" {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.writes++
		r.writeSession = id
	}
}

func TestServerProviderRecovery_Scenario2_EffectiveDelayNeverRetriesEarly_ComposedSecrecy(t *testing.T) {
	const sentinel = "RECOVERY-RAW-SECRET-7c91"
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	var mu sync.Mutex
	calls := 0
	positiveControl := false
	var laterRequests []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
		}
		mu.Lock()
		defer mu.Unlock()
		calls++
		if calls > 1 {
			laterRequests = append(laterRequests, string(body))
		}
		w.Header().Set("Content-Type", "application/json")
		switch calls {
		case 1:
			w.Header().Set("X-Raw-Secret", sentinel)
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = fmt.Fprintf(w, `{"error":{"code":"server_error","message":"unavailable","raw_secret":%q}}`, sentinel)
			positiveControl = true
		case 2:
			w.Header().Set("Content-Type", "text/event-stream")
			writeRecoveryToolTurn(w, "glob-after-recovery", "Glob", `{"pattern":"*"}`)
		default:
			w.Header().Set("Content-Type", "text/event-stream")
			writeRecoveryTextTurn(w, "recovered without projection")
		}
	}))
	defer srv.Close()
	diag := &recordingDiag{}
	cfg := recoveryAppConfig(t, srv.URL)
	cfg.Diagnostics = diag
	cfg.LLMBreakerThreshold = 5
	built, err := buildIsolated(t, ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer built.Close()
	sess, err := built.Service.CreateSession(ctx, session.ModeDefault, defaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	relay := httptest.NewServer(server.NewHTTPHandler(built.Service))
	var streamed []sseEvent
	resultSeen := false
	promptOverHTTP(t, relay.URL, string(sess.ID), "recover safely", func(event sseEvent) {
		streamed = append(streamed, event)
		resultSeen = resultSeen || event.Type == string(session.EvResult)
	})
	relay.Close()
	if !resultSeen {
		t.Fatal("composed relay produced no terminal result")
	}
	built.Close()
	store, err := jsonlstore.New(cfg.StoreDir)
	if err != nil {
		t.Fatal(err)
	}
	persisted, err := store.Load(ctx, sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	var logged []session.Event
	loggedUsage := false
	loggedTerminal := false
	for event, readErr := range store.Read(ctx, sess.ID) {
		if readErr != nil {
			t.Fatal(readErr)
		}
		logged = append(logged, event)
		if event.Result != nil && event.Result.Usage.InputTokens == 2 && event.Result.Usage.OutputTokens == 2 {
			loggedUsage = true
			loggedTerminal = event.Result.Stop == session.StopEndTurn && event.Result.Text == "recovered without projection"
		}
	}
	wantUsage := session.Usage{InputTokens: 2, OutputTokens: 2}
	if persisted.UsageFor(session.UsageKindMain) != wantUsage || !loggedUsage || !loggedTerminal {
		t.Fatalf("persisted usage=%+v logged_usage=%v logged_terminal=%v, want %+v", persisted.UsageFor(session.UsageKindMain), loggedUsage, loggedTerminal, wantUsage)
	}
	mu.Lock()
	projection := fmt.Sprint(streamed, logged, persisted.Conversation.Messages, persisted.TokenUsageSnapshot(), diag.msgs, diag.attrs, laterRequests)
	controlsOK := positiveControl && calls == 3 && len(laterRequests) == 2
	mu.Unlock()
	if !controlsOK {
		t.Fatalf("secrecy controls not exercised: positive=%v calls=%d later=%d", positiveControl, calls, len(laterRequests))
	}
	if strings.Contains(projection, sentinel) {
		t.Fatalf("raw provider response projected into composed surfaces: %s", projection)
	}
}

func TestServerProviderRecovery_Scenario1_RootAndDirectWriteChildRecoverBeforeTerminal(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	var mu sync.Mutex
	calls := map[string]int{}
	failedAt := map[string]time.Time{}
	var rootID, childID string
	recorder := &recoveryToolRecorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		id := r.Header.Get("X-Mecatl-Session-ID")
		if id == "" {
			t.Error("model request missing originating session")
		}
		calls[id]++
		n := calls[id]
		var body struct {
			Input []struct {
				Type   string `json:"type"`
				CallID string `json:"call_id"`
				Output string `json:"output"`
			}
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		hasResult := func(want string) bool {
			for _, item := range body.Input {
				if item.Type == "function_call_output" && item.CallID == want && item.Output != "" {
					return true
				}
			}
			return false
		}
		w.Header().Set("Content-Type", "text/event-stream")
		if strings.HasPrefix(id, "subagent-") {
			if childID != "" && childID != id {
				t.Errorf("replacement child %q after %q", id, childID)
			}
			childID = id
			switch n {
			case 1:
				writeRecoveryToolTurn(w, "write-1", "Write", `{"path":"beta.txt","content":"written once\n"}`)
			case 2:
				if !hasResult("write-1") {
					t.Error("child failure occurred without recorded Write result")
				}
				recorder.mu.Lock()
				if recorder.writes != 1 {
					t.Errorf("writes before recovery = %d", recorder.writes)
				}
				recorder.mu.Unlock()
				failedAt[id] = time.Now()
				writeRecoveryFailure(w)
			case 3:
				if !hasResult("write-1") {
					t.Error("recovered child lost earlier tool result")
				}
				if time.Since(failedAt[id]) < 500*time.Millisecond {
					t.Error("child retried before breaker cooldown")
				}
				writeRecoveryTextTurn(w, "child recovered")
			default:
				t.Errorf("unexpected child call %d", n)
				writeRecoveryTextTurn(w, "unexpected")
			}
			return
		}
		rootID = id
		switch n {
		case 1:
			writeRecoveryToolTurn(w, "delegate", "Subagent", `{"prompt":"write beta","mode":"read-write"}`)
		case 2:
			if !hasResult("delegate") || calls[childID] != 3 {
				t.Error("parent continued before child recovered with a tool result")
			}
			failedAt[id] = time.Now()
			writeRecoveryFailure(w)
		case 3:
			if !hasResult("delegate") {
				t.Error("root recovery lost Subagent result")
			}
			if time.Since(failedAt[id]) < 500*time.Millisecond {
				t.Error("root retried before breaker cooldown")
			}
			writeRecoveryTextTurn(w, "parent complete")
		default:
			t.Errorf("unexpected root call %d", n)
			writeRecoveryTextTurn(w, "unexpected")
		}
	}))
	defer srv.Close()
	cfg := recoveryAppConfig(t, srv.URL)
	cfg.ToolCallRecorder = recorder
	cfg.MetricsRoleScoper = func(string) (port.EventSink, port.ToolCallRecorder) { return nil, recorder }
	built, err := buildIsolated(t, ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer built.Close()
	parent, err := built.Service.CreateSession(ctx, session.ModeDefault, defaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	run, err := built.Service.StartRun(ctx, parent.ID, "delegate once")
	if err != nil {
		t.Fatal(err)
	}
	results := 0
	var final string
	for ev := range run.Events() {
		if ev.Type == session.EvToolResult && ev.ToolResult != nil {
			results++
			if ev.ToolResult.CallID != "delegate" || ev.ToolResult.IsError || !strings.Contains(ev.ToolResult.Content, "child recovered") {
				t.Errorf("early or mismatched parent tool result: %+v", ev.ToolResult)
			}
		}
		if ev.Type == session.EvResult && ev.Result != nil {
			if ev.Result.Stop != session.StopEndTurn {
				t.Errorf("root terminal = %+v", ev.Result)
			}
			final = ev.Result.Text
		}
	}
	built.Service.FinishRun(parent.ID, run)
	if results != 1 || final != "parent complete" {
		t.Fatalf("parent results=%d final=%q", results, final)
	}
	built.Close()
	mu.Lock()
	defer mu.Unlock()
	wantChild := "subagent-" + string(parent.ID) + "-delegate"
	if rootID != string(parent.ID) || childID != wantChild || len(calls) != 2 || calls[rootID] != 3 || calls[childID] != 3 {
		t.Fatalf("origin/call accounting: root=%q child=%q calls=%v", rootID, childID, calls)
	}
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	if recorder.writes != 1 || string(recorder.writeSession) != childID {
		t.Fatalf("Write executions=%d in %q", recorder.writes, recorder.writeSession)
	}
	body, err := os.ReadFile(filepath.Join(cfg.Workspace, "beta.txt"))
	if err != nil || string(body) != "written once\n" {
		t.Fatalf("direct write = %q, %v", body, err)
	}
	store, err := jsonlstore.New(cfg.StoreDir)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := store.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(stored) != 2 {
		t.Fatalf("durable sessions=%d, want root + one child", len(stored))
	}
	for _, id := range []session.SessionID{parent.ID, session.SessionID(childID)} {
		sess, err := store.Load(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		users, toolResults := 0, 0
		for _, m := range sess.Conversation.Messages {
			if m.Role == session.RoleUser {
				users++
			}
			if m.ToolResult != nil {
				toolResults++
				if m.ToolResult.IsError {
					t.Errorf("persisted failed result in %s: %+v", id, m.ToolResult)
				}
			}
		}
		if users != 1 || toolResults != 1 {
			t.Errorf("%s history: prompts=%d results=%d", id, users, toolResults)
		}
		if err := session.ValidateToolPairing(sess.Conversation.Messages); err != nil {
			t.Error(err)
		}
	}
}

func writeRecoveryFailure(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusServiceUnavailable)
	_, _ = io.WriteString(w, `{"error":{"code":"server_error","message":"unavailable"}}`)
}

func writeRecoveryToolTurn(w http.ResponseWriter, callID, name, args string) {
	_, _ = fmt.Fprintf(w, "event: response.output_item.done\ndata: {\"type\":\"response.output_item.done\",\"sequence_number\":0,\"output_index\":0,\"item\":{\"id\":\"fc_1\",\"type\":\"function_call\",\"call_id\":%q,\"name\":%q,\"arguments\":%q}}\n\n", callID, name, args)
	_, _ = io.WriteString(w, "event: response.completed\ndata: {\"type\":\"response.completed\",\"sequence_number\":1,\"response\":{\"status\":\"completed\",\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\n")
}

func writeRecoveryTextTurn(w http.ResponseWriter, text string) {
	_, _ = fmt.Fprintf(w, "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"sequence_number\":0,\"delta\":%q}\n\n", text)
	_, _ = io.WriteString(w, "event: response.completed\ndata: {\"type\":\"response.completed\",\"sequence_number\":1,\"response\":{\"status\":\"completed\",\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\n")
}
