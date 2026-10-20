package anthropic_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/memledger"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/provider/anthropic"
)

type noopHooks struct{}

func (noopHooks) Run(context.Context, governance.HookEvent) (governance.HookOutcome, error) {
	return governance.HookOutcome{}, nil
}

// reasoningOnlySSE is a turn whose extended thinking spends the whole output
// budget: one thinking block, no text, no tool_use, then the given stop_reason.
func reasoningOnlySSE(stop string) string {
	return "event: message_start\n" +
		`data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"test","content":[],"usage":{"input_tokens":10,"output_tokens":1}}}` + "\n\n" +
		"event: content_block_start\n" +
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":"","signature":""}}` + "\n\n" +
		"event: content_block_delta\n" +
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"planning every phase"}}` + "\n\n" +
		"event: content_block_delta\n" +
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"sig"}}` + "\n\n" +
		"event: content_block_stop\n" +
		`data: {"type":"content_block_stop","index":0}` + "\n\n" +
		"event: message_delta\n" +
		`data: {"type":"message_delta","delta":{"stop_reason":"` + stop + `","stop_sequence":null},"usage":{"output_tokens":4096}}` + "\n\n" +
		"event: message_stop\n" +
		`data: {"type":"message_stop"}` + "\n\n"
}

// TestReasoningOnlyTerminalStopNamesProviderReason drives a reasoning-only
// terminal turn through the real adapter and agent loop. The run must fail with
// a cause that names the Anthropic stop_reason, not only the collapsed "error".
func TestReasoningOnlyTerminalStopNamesProviderReason(t *testing.T) {
	for _, stop := range []string{"max_tokens", "refusal"} {
		t.Run(stop, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, reasoningOnlySSE(stop))
			}))
			defer srv.Close()

			e := agent.NewEngine(agent.Deps{
				LLM:     anthropic.New(anthropic.WithAPIKey("test-key"), anthropic.WithBaseURL(srv.URL)),
				Catalog: tool.NewCatalog(),
				Policy:  permpolicy.NewPolicy(permpolicy.AllowAllFloorRules(), nil),
				Hooks:   noopHooks{},
				Model:   "test",
			})
			ref := session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"}
			env := tool.MustEnvironment(ref, memfs.NewWorkspace("/ws"), memledger.New(), nil)
			sess := session.New("s1", session.ModeDefault, ref, session.Limits{}, time.Unix(0, 0))
			r := e.Run(context.Background(), sess, env, agent.RunRequest{Text: "do all of the phases"})

			var res *session.ResultPayload
			for ev := range r.Events() {
				if ev.Result != nil {
					res = ev.Result
				}
			}
			if res == nil {
				t.Fatal("run ended without a result event")
			}
			if res.Stop != session.StopError {
				t.Fatalf("stop = %q, want %q", res.Stop, session.StopError)
			}
			want := `(provider stop reason "` + stop + `")`
			if !strings.Contains(res.Error, want) {
				t.Fatalf("error = %q, want it to contain %q", res.Error, want)
			}
		})
	}
}
