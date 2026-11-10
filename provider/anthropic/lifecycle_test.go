package anthropic

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/memledger"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/adapter/sessnap"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

type lifecycleReadTool struct{}

func (lifecycleReadTool) Spec() tool.ToolSpec {
	return tool.ToolSpec{Name: "Read", Schema: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}},"required":["path"],"additionalProperties":false}`)}
}
func (lifecycleReadTool) ReadOnly() bool { return true }
func (lifecycleReadTool) Execute(_ context.Context, call session.ToolCall, _ tool.Environment) (session.ToolResult, error) {
	return session.NewToolResult(call.ID, "result-1"), nil
}

func TestMessagesEngineSnapshotReplayLifecycle(t *testing.T) {
	textSSE, err := os.ReadFile("testdata/text_turn.sse")
	if err != nil {
		t.Fatal(err)
	}
	for _, legacy := range []bool{false, true} {
		t.Run(map[bool]string{false: "current", true: "legacy"}[legacy], func(t *testing.T) {
			var requests []struct {
				Messages []json.RawMessage `json:"messages"`
			}
			first := !legacy
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request struct {
					Messages []json.RawMessage `json:"messages"`
				}
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Error(err)
					w.WriteHeader(400)
					return
				}
				requests = append(requests, request)
				w.Header().Set("Content-Type", "text/event-stream")
				if first {
					_, _ = w.Write([]byte(signedRedactedTurnSSE(1)))
					first = false
				} else {
					_, _ = w.Write(textSSE)
				}
			}))
			defer srv.Close()
			p := New(WithAPIKey("fixture"), WithBaseURL(srv.URL), WithMaxTokens(16000), WithConversationCaching(false))
			env := tool.MustEnvironment(session.EnvironmentRef{Kind: session.EnvKindMem, ID: "/ws", Revision: "v1"}, memfs.NewWorkspace("/ws"), memledger.New(), nil)
			catalog := tool.NewCatalog()
			catalog.MustRegister(lifecycleReadTool{})
			eng := agent.NewEngine(agent.Deps{LLM: p, Catalog: catalog, Model: "claude-sonnet-4-6", Policy: permpolicy.NewPolicy([]governance.Rule{{Tool: "Read", Effect: governance.Allow}}, nil)})
			var s *session.Session
			if legacy {
				s, err = sessnap.Unmarshal([]byte(`{"id":"old","state":"idle","mode":"default","environment_ref":{"Kind":"mem","ID":"/ws","Revision":"v1"},"created_at":"2023-11-14T22:13:20Z","messages":[{"role":"user","text":"go"},{"role":"assistant","text":"Checking.","reasoning":"{\"v\":1,\"blocks\":[{\"t\":\"thinking\",\"x\":\"thought\\n雪\",\"s\":\"SIG-1==\"},{\"t\":\"redacted\",\"d\":\"REDACTED-1==\"},{\"t\":\"thinking\",\"s\":\"EMPTY-1==\"}]}","tool_calls":[{"ID":"toolu_reused","Name":"Read","Args":{"path":"x.go"}}]},{"role":"tool","tool_result":{"CallID":"toolu_reused","Content":"result-1"}}]}`))
				if err != nil {
					t.Fatal(err)
				}
			} else {
				s = session.New("current", session.ModeDefault, env.Ref(), session.Limits{}, time.Unix(0, 0))
				for ev := range eng.Run(t.Context(), s, env, agent.RunRequest{Text: "go"}).Events() {
					if ev.Result != nil && ev.Result.Error != "" {
						t.Fatal(ev.Result.Error)
					}
				}
			}
			wire, err := sessnap.Marshal(s)
			if err != nil {
				t.Fatal(err)
			}
			s, err = sessnap.Unmarshal(wire)
			if err != nil {
				t.Fatal(err)
			}
			// Recreate the adapter and engine: no in-process replay cache survives.
			p = New(WithAPIKey("fixture"), WithBaseURL(srv.URL), WithMaxTokens(16000), WithConversationCaching(false))
			eng = agent.NewEngine(agent.Deps{LLM: p, Catalog: catalog, Model: "claude-sonnet-4-6", Policy: permpolicy.NewPolicy([]governance.Rule{{Tool: "Read", Effect: governance.Allow}}, nil)})
			want := session.CloneMessages(s.Conversation.Messages)
			if s.State == session.StateCompleted {
				if err := s.Reopen(); err != nil {
					t.Fatal(err)
				}
			}
			for ev := range eng.Run(t.Context(), s, env, agent.RunRequest{Text: "resume"}).Events() {
				if ev.Result != nil && ev.Result.Error != "" {
					t.Fatal(ev.Result.Error)
				}
			}
			last := requests[len(requests)-1]
			assertReplayWirePair(t, last.Messages[1], last.Messages[2], 1)
			if !reflect.DeepEqual(s.Conversation.Messages[:len(want)], want) {
				t.Fatal("restart changed canonical identity or private carrier")
			}
		})
	}
}

func TestLegacyMessagesReplayFallbackPolicy(t *testing.T) {
	for _, tc := range []struct {
		reasoning string
		want      []reasoningBlock
	}{
		{"ciphertext", nil},
		{`{"v":2,"blocks":[{"t":"thinking","x":"private","s":"sig"}]}`, nil},
		{`{"v":1,"blocks":[{"t":"future","d":"private"},{"t":"thinking","x":"old incomplete"},{"t":"redacted","d":"exact"}]}`, []reasoningBlock{{Kind: "thinking", Thinking: "old incomplete"}, {Kind: "redacted", Data: "exact"}}},
	} {
		data, err := json.Marshal(map[string]string{"reasoning": tc.reasoning, "reasoning_item_id": "ignored", "phase": "ignored"})
		if err != nil {
			t.Fatal(err)
		}
		blocks, err := unpackReplay(&session.ProviderReplay{Format: "mecatl.legacy-message", Version: 1, Data: data})
		if err != nil || !reflect.DeepEqual(blocks, tc.want) {
			t.Fatal("legacy fallback policy changed")
		}
	}
	for _, data := range [][]byte{[]byte(`{"reasoning":42}`), []byte(`null`), {0xff}} {
		blocks, err := unpackReplay(&session.ProviderReplay{Format: "mecatl.legacy-message", Version: 1, Data: data})
		if err != nil || len(blocks) != 0 {
			t.Fatal("malformed generic legacy envelope must remain a no-op")
		}
	}
}
