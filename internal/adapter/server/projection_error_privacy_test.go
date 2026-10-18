package server

import (
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/encoding/protojson"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/memledger"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

func TestProjectionFailureEngineResultDoesNotExposeProviderIdentity(t *testing.T) {
	const canary = "PRIVATE-PROVIDER-PAIRING-CANARY"
	env := tool.MustEnvironment(session.EnvironmentRef{Kind: session.EnvKindMem, ID: "/ws", Revision: "v1"}, memfs.NewWorkspace("/ws"), memledger.New(), nil)
	s := session.New("projection-failure", session.ModeDefault, env.Ref(), session.Limits{}, time.Unix(0, 0))
	var calls, results []session.Message
	for _, id := range []session.ToolCallID{"canonical-a", "canonical-b"} {
		m := session.NewAssistantMessage("", []session.ToolCall{session.NewToolCall(id, "Read", nil)})
		m.Replay = &session.MessageReplay{Bindings: []session.ToolCallBinding{{CallID: id, PairingID: canary}}}
		calls = append(calls, m)
		results = append(results, session.NewToolMessage(session.NewToolResult(id, "ok")))
	}
	if err := s.SeedHistory(append(calls, results...)); err != nil {
		t.Fatal(err)
	}
	llm := mockllm.New(mockllm.TextTurn("unreachable"))
	e := agent.NewEngine(agent.Deps{LLM: llm, Catalog: tool.NewCatalog()})
	failed := false
	for ev := range e.Run(t.Context(), s, env, agent.RunRequest{Text: "continue"}).Events() {
		if ev.Result != nil {
			failed = ev.Result.Stop == session.StopError && strings.Contains(ev.Result.Error, "still outstanding")
			if strings.Contains(ev.Result.Error, canary) {
				t.Fatal("engine Result.Error exposed provider identity")
			}
		}
		raw, err := protojson.Marshal(toProto(ev))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), canary) {
			t.Fatal("public event mapper exposed provider identity")
		}
	}
	if !failed || llm.Calls() != 0 {
		t.Fatal("did not exercise buildRequest projection failure")
	}
}
