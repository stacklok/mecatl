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

type recoverySecrecyProjection struct {
	RawSSE         string
	DurableEvents  []session.Event
	PersistedState json.RawMessage
	Diagnostics    []any
	ModelRequests  []string
}

func marshalRecoveryProjection(t *testing.T, projection recoverySecrecyProjection) []byte {
	t.Helper()
	encoded, err := json.Marshal(projection)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func recoveryTerminalFromSSE(t *testing.T, raw []byte) session.ResultPayload {
	t.Helper()
	for _, line := range strings.Split(string(raw), "\n") {
		data, ok := strings.CutPrefix(strings.TrimSpace(line), "data: ")
		if !ok {
			continue
		}
		var event struct {
			Type   string                 `json:"type"`
			Result *session.ResultPayload `json:"result"`
		}
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			t.Fatalf("decode full SSE event: %v", err)
		}
		if event.Type == string(session.EvResult) && event.Result != nil {
			return *event.Result
		}
	}
	t.Fatal("full SSE body contained no terminal result")
	return session.ResultPayload{}
}

func recoveryPromptRawSSE(ctx context.Context, t *testing.T, baseURL string, id session.SessionID) []byte {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/v1/sessions/"+string(id)+"/prompt", strings.NewReader(`{"text":"recover safely"}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("drain SSE through terminal: %v", err)
	}
	if resp.StatusCode != http.StatusOK || len(raw) == 0 {
		t.Fatalf("SSE status=%d body=%q", resp.StatusCode, raw)
	}
	return raw
}

func TestServerProviderRecovery_Scenario2_EffectiveDelayNeverRetriesEarly_ComposedSecrecy(t *testing.T) {
	const sentinel = "RECOVERY-RAW-SECRET-7c91"
	for _, outcome := range []string{"successful recovery", "terminal exhaustion"} {
		t.Run(outcome, func(t *testing.T) {
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
				if calls == 1 {
					w.Header().Set("Content-Type", "application/json")
					w.Header().Set("X-Raw-Secret", sentinel)
					w.WriteHeader(http.StatusServiceUnavailable)
					_, _ = fmt.Fprintf(w, `{"error":{"code":"server_error","message":"unavailable","raw_secret":%q}}`, sentinel)
					positiveControl = true
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				if calls == 2 {
					writeRecoveryToolTurn(w, "glob-after-recovery", "Glob", `{"pattern":"*"}`)
					return
				}
				writeRecoveryTextTurn(w, "recovered without projection")
			}))
			defer srv.Close()
			diag := &recordingDiag{}
			cfg := recoveryAppConfig(t, srv.URL)
			cfg.Diagnostics = diag
			cfg.LLMBreakerThreshold = 5
			if outcome == "terminal exhaustion" {
				cfg.LLMMaxAttempts = 1
			}
			built, err := buildIsolated(t, ctx, cfg)
			if err != nil {
				t.Fatal(err)
			}
			sess, err := built.Service.CreateSession(ctx, session.ModeDefault, defaultLimits())
			if err != nil {
				t.Fatal(err)
			}
			relay := httptest.NewServer(server.NewHTTPHandler(built.Service))
			rawSSE := recoveryPromptRawSSE(ctx, t, relay.URL, sess.ID)
			relay.Close()
			terminal := recoveryTerminalFromSSE(t, rawSSE)
			if outcome == "successful recovery" {
				if terminal.Stop != session.StopEndTurn || terminal.Text != "recovered without projection" {
					t.Fatalf("successful terminal=%+v", terminal)
				}
			} else if terminal.Stop != session.StopError || !strings.Contains(terminal.Error, "unavailable") {
				t.Fatalf("exhausted terminal did not retain safe cause: %+v", terminal)
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
			for event, readErr := range store.Read(ctx, sess.ID) {
				if readErr != nil {
					t.Fatal(readErr)
				}
				logged = append(logged, event)
			}
			persistedJSON, err := json.Marshal(persisted)
			if err != nil {
				t.Fatal(err)
			}
			diagnostics := make([]any, 0, len(diag.msgs))
			for i, msg := range diag.msgs {
				diagnostics = append(diagnostics, []any{msg, diag.attrs[i]})
			}
			mu.Lock()
			served, actualCalls := positiveControl, calls
			requests := append([]string(nil), laterRequests...)
			mu.Unlock()
			wantCalls := 3
			if outcome == "terminal exhaustion" {
				wantCalls = 1
			}
			if !served || actualCalls != wantCalls || len(requests) != wantCalls-1 {
				t.Fatalf("secrecy controls not exercised: positive=%v calls=%d later=%d", served, actualCalls, len(requests))
			}
			projection := recoverySecrecyProjection{
				RawSSE: string(rawSSE), DurableEvents: logged, PersistedState: persistedJSON,
				Diagnostics: diagnostics, ModelRequests: requests,
			}
			if encoded := marshalRecoveryProjection(t, projection); strings.Contains(string(encoded), sentinel) {
				t.Fatalf("raw provider response projected into composed surfaces: %s", encoded)
			}

			// Mutation controls prove both nested terminal errors and persisted payloads
			// are part of the inspected projection rather than pointer/empty stand-ins.
			mutatedEvents := append([]session.Event(nil), logged...)
			for i := range mutatedEvents {
				if mutatedEvents[i].Result != nil {
					result := *mutatedEvents[i].Result
					result.Error = sentinel
					mutatedEvents[i].Result = &result
					break
				}
			}
			mutation := projection
			mutation.DurableEvents = mutatedEvents
			if !strings.Contains(string(marshalRecoveryProjection(t, mutation)), sentinel) {
				t.Fatal("terminal Result.Error mutation escaped the secrecy oracle")
			}
			mutation = projection
			mutatedPersisted, err := json.Marshal(map[string]any{"actual": json.RawMessage(persistedJSON), "mutation": sentinel})
			if err != nil {
				t.Fatal(err)
			}
			mutation.PersistedState = mutatedPersisted
			if !strings.Contains(string(marshalRecoveryProjection(t, mutation)), sentinel) {
				t.Fatal("persisted payload mutation escaped the secrecy oracle")
			}
		})
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
