package anthropic

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/memledger"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/prompt"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

type canonicalReadTool struct {
	lifecycleReadTool
	calls []session.ToolCall
}

func (r *canonicalReadTool) Execute(_ context.Context, call session.ToolCall, _ tool.Environment) (session.ToolResult, error) {
	r.calls = append(r.calls, call)
	return session.NewToolResult(call.ID, fmt.Sprintf("result-%d", len(r.calls))), nil
}

type canonicalInstructions []string

func (canonicalInstructions) TargetScoped() bool { return false }

func (fragments canonicalInstructions) Assemble(context.Context, []string, *session.InstructionSnapshot, int) ([]session.Message, []prompt.InstructionManifest, error) {
	var messages []session.Message
	var manifest []prompt.InstructionManifest
	for _, text := range fragments {
		messages = append(messages, session.NewUserMessage(text))
		manifest = append(manifest, prompt.InstructionManifest{Kind: prompt.InstructionKindTurn0, Provenance: prompt.InstructionProvenanceCustom})
	}
	return messages, manifest, nil
}

func TestMessagesEngineCanonicalIdentityRoundTrip(t *testing.T) {
	for _, tc := range []struct {
		name      string
		fragments canonicalInstructions
	}{
		{name: "no-leading-fragments"},
		{name: "two-leading-turn0-fragments", fragments: canonicalInstructions{turn0Fragment("first fragment").Text, turn0Fragment("last fragment").Text}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			textSSE, err := os.ReadFile("testdata/text_turn.sse")
			if err != nil {
				t.Fatal(err)
			}
			requests := make(chan json.RawMessage, 3)
			var count atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body json.RawMessage
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				turn := count.Add(1)
				if turn > 3 {
					t.Error("unexpected extra HTTP request")
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				requests <- body
				w.Header().Set("Content-Type", "text/event-stream")
				if turn <= 2 {
					fmt.Fprint(w, signedRedactedTurnSSE(int(turn)))
				} else {
					_, _ = w.Write(textSSE)
				}
			}))
			defer srv.Close()

			read := &canonicalReadTool{}
			catalog := tool.NewCatalog()
			catalog.MustRegister(read)
			p := New(WithAPIKey("synthetic"), WithBaseURL(srv.URL), WithMaxTokens(16000))
			eng := agent.NewEngine(agent.Deps{
				LLM: p, Catalog: catalog, Model: "claude-sonnet-4-6",
				Policy:       permpolicy.NewPolicy([]governance.Rule{{Tool: "Read", Effect: governance.Allow}}, nil),
				Instructions: tc.fragments,
				PromptBuilder: func(prompt.Config) prompt.Layered {
					return prompt.Layered{StablePrefix: "STABLE", VolatileSuffix: "VOLATILE"}
				},
			})
			env := tool.MustEnvironment(session.EnvironmentRef{Kind: session.EnvKindMem, ID: "/ws", Revision: "v1"}, memfs.NewWorkspace("/ws"), memledger.New(), nil)
			s := session.New("canonical", session.ModeDefault, env.Ref(), session.Limits{MaxTurns: 3}, time.Unix(0, 0))
			var calls []session.ToolCallID
			var results []session.ToolResult
			var terminal *session.ResultPayload
			for ev := range eng.Run(t.Context(), s, env, agent.RunRequest{Text: "read twice"}).Events() {
				if ev.Type == session.EvToolCall {
					calls = append(calls, ev.ToolCall.ID)
				}
				if ev.Type == session.EvToolResult {
					results = append(results, *ev.ToolResult)
				}
				if ev.Type == session.EvResult {
					terminal = ev.Result
				}
			}
			if terminal == nil || terminal.Stop != session.StopEndTurn || len(read.calls) != 2 || len(calls) != 2 || len(results) != 2 || count.Load() != 3 {
				t.Fatalf("terminal=%+v executed=%v calls=%v results=%v requests=%d", terminal, read.calls, calls, results, count.Load())
			}
			if read.calls[0].ID == read.calls[1].ID {
				t.Fatal("completed provider key reuse did not get a fresh canonical ID")
			}
			for i, call := range read.calls {
				if call.ID == "" || call.ID == "toolu_reused" || call.ID != calls[i] || call.ID != results[i].CallID || call.Name != "Read" || string(call.Args) != `{"path":"x.go"}` {
					t.Fatalf("execution/event identity or arguments changed: %+v", call)
				}
				m, result := s.Conversation.Messages[1+2*i], s.Conversation.Messages[2+2*i]
				if len(m.ToolCalls) != 1 || m.ToolCalls[0].ID != call.ID || m.Replay == nil || len(m.Replay.Bindings) != 1 || m.Replay.Bindings[0] != (session.ToolCallBinding{CallID: call.ID, PairingID: "toolu_reused"}) || m.Replay.Provider == nil {
					t.Fatalf("history lost occurrence-bound identity/replay: %+v", m)
				}
				wantReplay := fmt.Sprintf(`{"v":1,"blocks":[{"t":"thinking","x":"thought\n雪","s":"SIG-%d=="},{"t":"redacted","d":"REDACTED-%d=="},{"t":"thinking","s":"EMPTY-%d=="}]}`, i+1, i+1, i+1)
				if replay := m.Replay.Provider; replay.Format != "anthropic.messages" || replay.Version != 1 || !bytes.Equal(replay.Data, []byte(wantReplay)) {
					t.Fatalf("history changed opaque replay: %+v", replay)
				}
				if result.ToolResult == nil || result.ToolResult.CallID != call.ID || result.ToolResult.IsError || result.ToolResult.Content != fmt.Sprintf("result-%d", i+1) {
					t.Fatalf("recorded result mismatched execution: %+v", result)
				}
				if results[i].Content != result.ToolResult.Content || results[i].IsError != result.ToolResult.IsError {
					t.Fatalf("event result mismatched recorded result: %+v", results[i])
				}
			}
			if err := session.ValidateToolPairing(s.Conversation.Messages); err != nil {
				t.Fatal(err)
			}

			assertJSON := func(got json.RawMessage, want string) {
				t.Helper()
				var gotValue, wantValue any
				if err := json.Unmarshal(got, &gotValue); err != nil {
					t.Fatal(err)
				}
				if err := json.Unmarshal([]byte(want), &wantValue); err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(gotValue, wantValue) {
					t.Fatalf("wire JSON changed: got %s, want %s", got, want)
				}
			}
			for turn := range 3 {
				raw := <-requests
				var body struct {
					System       json.RawMessage   `json:"system"`
					Messages     []json.RawMessage `json:"messages"`
					CacheControl json.RawMessage   `json:"cache_control"`
				}
				if err := json.Unmarshal(raw, &body); err != nil {
					t.Fatal(err)
				}
				assertJSON(body.System, `[{"type":"text","text":"STABLE","cache_control":{"type":"ephemeral"}},{"type":"text","text":"VOLATILE"}]`)
				assertJSON(body.CacheControl, `{"type":"ephemeral"}`)
				wantMarkers := 2
				if len(tc.fragments) > 0 {
					wantMarkers++
				}
				if turn > 0 {
					wantMarkers++
				}
				if got := countCacheControl(raw); got != wantMarkers || got > 4 || bytes.Contains(raw, []byte(`"ttl"`)) {
					t.Fatalf("request %d cache budget/TTL changed: markers=%d want=%d body=%s", turn+1, got, wantMarkers, raw)
				}
				messages := body.Messages
				offset := len(tc.fragments)
				if len(messages) != offset+1+2*turn {
					t.Fatalf("request %d has %d messages, want %d", turn+1, len(messages), offset+1+2*turn)
				}
				for i, text := range append(append([]string(nil), tc.fragments...), "read twice") {
					marker := ""
					if i == offset-1 || (turn == 1 && i == offset) {
						marker = `,"cache_control":{"type":"ephemeral"}`
					}
					encodedText, err := json.Marshal(text)
					if err != nil {
						t.Fatal(err)
					}
					assertJSON(messages[i], fmt.Sprintf(`{"role":"user","content":[{"type":"text","text":%s%s}]}`, encodedText, marker))
				}
				for occurrence := 1; occurrence <= turn; occurrence++ {
					resultIndex := offset + 2*occurrence
					result := messages[resultIndex]
					if turn == 2 && occurrence == 1 {
						var decoded map[string]any
						if err := json.Unmarshal(result, &decoded); err != nil {
							t.Fatal(err)
						}
						block := decoded["content"].([]any)[0].(map[string]any)
						if !reflect.DeepEqual(block["cache_control"], map[string]any{"type": "ephemeral"}) {
							t.Fatalf("request 3 message %d missing exact outer result marker: %s", resultIndex, result)
						}
						// Only this verified outer marker is removed; strict replay checks
						// below still reject markers on signed blocks or nested result text.
						delete(block, "cache_control")
						var err error
						result, err = json.Marshal(decoded)
						if err != nil {
							t.Fatal(err)
						}
					}
					assertReplayWirePair(t, messages[resultIndex-1], result, occurrence)
				}
			}
		})
	}
}
