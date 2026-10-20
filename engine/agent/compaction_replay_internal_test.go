package agent

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

type overlappingReplayCompactor struct{}

func (overlappingReplayCompactor) Compact(_ context.Context, conv *session.Conversation) ([]session.Message, string, session.AuxiliaryUsage, error) {
	out := session.CloneMessages(conv.Messages[:4])
	out[0].Text = ""
	out[2].Text = ""
	out[1], out[2] = out[2], out[1]
	return out, "invalid replacement", session.AuxiliaryUsage{}, nil
}

func TestAutomaticCompactionRejectsOverlappingProviderPairing(t *testing.T) {
	env := memEnv("/ws")
	s := session.New("overlap-compaction", session.ModeDefault, env.Ref(), session.Limits{}, time.Unix(0, 0))
	var history []session.Message
	for _, id := range []session.ToolCallID{"a", "b"} {
		m := session.NewAssistantMessage(strings.Repeat("original ", 30), []session.ToolCall{session.NewToolCall(id, "Read", nil)})
		m.Replay = &session.MessageReplay{Bindings: []session.ToolCallBinding{{CallID: id, PairingID: "private-pair"}}, Provider: &session.ProviderReplay{Format: "opaque", Version: 1, Data: []byte("private bytes")}}
		history = append(history, m, session.NewToolMessage(session.NewToolResult(id, "read")))
	}
	if err := s.SeedHistory(history); err != nil {
		t.Fatal(err)
	}
	want := session.CloneMessages(s.Conversation.Messages)
	llm := mockllm.NewWith([]mockllm.Option{mockllm.WithRequestObserver(func(req port.LLMRequest) {
		if len(req.Messages) < len(want) || req.Messages[0].Text != want[0].Text || req.Messages[1].ToolResult == nil {
			t.Error("automatic compaction replaced request with invalid candidate")
		}
	})}, mockllm.TextTurn("done"))
	e := NewEngine(Deps{LLM: llm, Catalog: tool.NewCatalog(), ContextWindow: func() int { return 1 }, Compactor: overlappingReplayCompactor{}})
	for ev := range e.Run(t.Context(), s, env, RunRequest{Text: "continue"}).Events() {
		if ev.Type == session.EvCompaction || ev.Type == session.EvCompactionArchive {
			t.Fatal("invalid candidate emitted success")
		}
	}
	if llm.Calls() != 1 || !reflect.DeepEqual(s.Conversation.Messages[:len(want)], want) {
		t.Fatal("automatic rejection changed history/replay or stopped normal inference")
	}
}

type cancellingReplayCompactor struct{ cancel context.CancelFunc }

func (c cancellingReplayCompactor) Compact(_ context.Context, conv *session.Conversation) ([]session.Message, string, session.AuxiliaryUsage, error) {
	conv.Messages[0].Replay.Provider.Data[0] = '!'
	conv.Messages[0].Replay.Bindings[0].PairingID = "changed"
	conv.Messages[0].Legacy.Ordinal = 0
	c.cancel()
	return []session.Message{session.NewUserMessage("small candidate")}, "summary", session.AuxiliaryUsage{}, nil
}

func TestAutomaticCompactionCancellationOwnsReplay(t *testing.T) {
	env := memEnv("/ws")
	s := session.New("cancel-compaction", session.ModeDefault, env.Ref(), session.Limits{}, time.Unix(0, 0))
	m := session.NewAssistantMessage("long original assistant message", []session.ToolCall{session.NewToolCall("canonical", "Read", nil)})
	m.Replay = &session.MessageReplay{
		Bindings: []session.ToolCallBinding{{CallID: "canonical", PairingID: "provider"}},
		Provider: &session.ProviderReplay{Format: "opaque", Version: 7, Data: []byte("bytes")},
	}
	m.Legacy = &session.LegacyMessageIdentity{Namespace: "source", Ordinal: 9}
	if err := s.SeedHistory([]session.Message{m, session.NewToolMessage(session.NewToolResult("canonical", "read"))}); err != nil {
		t.Fatal(err)
	}
	want := session.CloneMessages(s.Conversation.Messages)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	llm := mockllm.New(mockllm.TextTurn("must not run"))
	e := NewEngine(Deps{LLM: llm, Catalog: tool.NewCatalog(), ContextWindow: func() int { return 1 }, Compactor: cancellingReplayCompactor{cancel: cancel}})
	for range e.Run(ctx, s, env, RunRequest{Text: "continue"}).Events() {
	}
	if !reflect.DeepEqual(s.Conversation.Messages[:len(want)], want) {
		t.Fatal("cancelled automatic compaction mutated active replay/provenance or committed its candidate")
	}
	if llm.Calls() != 0 {
		t.Fatal("cancelled compaction continued to the model")
	}
}
