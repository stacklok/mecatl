package agent_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

func TestAuxiliaryTokenUsage_Scenario2_CompactionRecordsSelectedModel(t *testing.T) {
	identity := session.ProviderModelID{ProviderID: "provider-b", ModelID: "summary-model"}
	provider := mockllm.New(mockllm.Turn{Chunks: []port.Chunk{
		{Kind: port.ChunkText, Text: "short summary"},
		{Kind: port.ChunkUsage, Usage: &session.Usage{InputTokens: 11, OutputTokens: 3}},
		{Kind: port.ChunkUsage, Usage: &session.Usage{InputTokens: 2, OutputTokens: 1}},
		{Kind: port.ChunkDone, Stop: session.StopEndTurn},
	}})
	compactor := agent.CascadeCompactor{
		Counter: agent.HeuristicTokenCounter{CharsPerToken: 1}, BudgetTokens: 1,
		LLM: provider, Model: identity.ModelID, ProviderModel: identity,
	}
	conv := overBudgetConversation()
	sess := session.New("compacted", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "test"}, session.Limits{}, time.Unix(1, 0))
	if err := sess.SeedHistory(conv.Messages); err != nil {
		t.Fatal(err)
	}
	engine := agent.NewEngine(agent.Deps{Compactor: compactor, TokenCounter: agent.HeuristicTokenCounter{CharsPerToken: 1}})
	result, err := engine.CompactSession(context.Background(), sess)
	if err != nil || !result.Changed {
		t.Fatalf("CompactSession changed=%t err=%v", result.Changed, err)
	}
	got := sess.TokenUsageSnapshot()[session.UsageKindCompaction]
	want := session.Usage{InputTokens: 13, OutputTokens: 4}
	if got.Total != want || got.Models["provider-b/summary-model"] != want {
		t.Fatalf("compaction usage = %#v, want %#v attributed to selected model", got, want)
	}
}

func TestAuxiliaryTokenUsage_Scenario2_ReflectionRecordsSelectedModel(t *testing.T) {
	input, _ := admittedInput(t)
	identity := session.ProviderModelID{ProviderID: "provider-r", ModelID: "reflection-model"}
	provider := mockllm.New(mockllm.Turn{Chunks: []port.Chunk{
		{Kind: port.ChunkText, Text: `{"kind":"abstained","candidates":[]}`},
		{Kind: port.ChunkUsage, Usage: &session.Usage{InputTokens: 7, OutputTokens: 2, CacheReadTokens: 1}},
		{Kind: port.ChunkDone, Stop: session.StopEndTurn},
	}})
	reflector, err := agent.NewEvidenceReflectorForProviderModel(provider, identity, nil, agent.ReflectionLimits{})
	if err != nil {
		t.Fatal(err)
	}
	_, usage, err := reflector.Reflect(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	want := session.Usage{InputTokens: 7, OutputTokens: 2, CacheReadTokens: 1}
	got := usage.Buckets[session.UsageKindReflection]
	if got.Total != want || got.Models["provider-r/reflection-model"] != want {
		t.Fatalf("reflection usage = %#v, want %#v attributed to selected model", got, want)
	}
}

func TestAuxiliaryTokenUsage_Scenario2_RetryAndPartialUsageAreCountedOnce(t *testing.T) {
	input, _ := admittedInput(t)
	identity := session.ProviderModelID{ProviderID: "provider-r", ModelID: "reflection-model"}
	terminal := errors.New("terminal stream failure")
	provider := mockllm.New(mockllm.Turn{Chunks: []port.Chunk{
		{Kind: port.ChunkUsage, Usage: &session.Usage{InputTokens: 5, OutputTokens: 1}},
		{Kind: port.ChunkUsage, Usage: &session.Usage{InputTokens: 8, OutputTokens: 2}},
	}, Err: terminal})
	reflector, err := agent.NewEvidenceReflectorForProviderModel(provider, identity, nil, agent.ReflectionLimits{})
	if err != nil {
		t.Fatal(err)
	}
	_, usage, err := reflector.Reflect(context.Background(), input)
	if !errors.Is(err, agent.ErrReflectionProvider) {
		t.Fatalf("error = %v, want reflection provider error", err)
	}
	want := session.Usage{InputTokens: 13, OutputTokens: 3}
	got := usage.Buckets[session.UsageKindReflection]
	if got.Total != want || got.Models["provider-r/reflection-model"] != want {
		t.Fatalf("partial/retry usage = %#v, want each emission once: %#v", got, want)
	}
}
