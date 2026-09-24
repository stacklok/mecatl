package agent_test

import (
	"context"
	"errors"
	"iter"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/learning"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
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

type usageReturningObserver struct {
	usage session.AuxiliaryUsage
}

func (usageReturningObserver) Observe(context.Context, learning.Trajectory) error { return nil }
func (o usageReturningObserver) ObserveWithUsage(context.Context, learning.Trajectory) (session.AuxiliaryUsage, error) {
	return o.usage, nil
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

	store := memstore.New()
	persistedUsage := session.Usage{InputTokens: 5, OutputTokens: 1}
	observer := usageReturningObserver{usage: session.AuxiliaryUsage{Buckets: map[session.UsageKind]session.TokenUsage{
		session.UsageKindMain: {Models: map[string]session.Usage{"provider-r/reflection-model": persistedUsage}},
	}}}
	owned := newSession(t, session.Limits{})
	eng := newEngine(agent.Deps{
		LLM: mockllm.New(mockllm.TextTurn("done")), Catalog: tool.NewCatalog(), Store: store,
		LearningMode: learning.Auto, LearningObserver: observer,
	})
	for range eng.Run(t.Context(), owned, agent.MemEnv("/ws"), agent.RunRequest{Text: "reflect"}).Events() {
	}
	reloaded, loadErr := store.Load(t.Context(), owned.ID)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	persistedBucket := reloaded.TokenUsageSnapshot()[session.UsageKindReflection]
	if persistedBucket.Models["provider-r/reflection-model"] != persistedUsage {
		t.Fatalf("persisted automatic reflection usage = %#v, want %#v", persistedBucket, persistedUsage)
	}
	if reloaded.UsageFor(session.UsageKindMain) != (session.Usage{}) {
		t.Fatalf("automatic reflection injected main usage: %+v", reloaded.UsageFor(session.UsageKindMain))
	}
}

type retryingProvider struct {
	attempts []*mockllm.Provider
}

func (p retryingProvider) Capabilities() port.ProviderCapabilities {
	return p.attempts[0].Capabilities()
}

func (p retryingProvider) Stream(ctx context.Context, req port.LLMRequest) (iter.Seq2[port.Chunk, error], error) {
	return func(yield func(port.Chunk, error) bool) {
		for i, attempt := range p.attempts {
			seq, err := attempt.Stream(ctx, req)
			if err != nil {
				if i == len(p.attempts)-1 {
					yield(port.Chunk{}, err)
				}
				continue
			}
			retry := false
			for chunk, streamErr := range seq {
				if streamErr != nil && i < len(p.attempts)-1 {
					retry = true
					break
				}
				if !yield(chunk, streamErr) {
					return
				}
			}
			if !retry {
				return
			}
		}
	}, nil
}

func TestAuxiliaryTokenUsage_Scenario2_RetryAndPartialUsageAreCountedOnce(t *testing.T) {
	input, _ := admittedInput(t)
	identity := session.ProviderModelID{ProviderID: "provider-r", ModelID: "reflection-model"}
	terminal := errors.New("retryable stream failure")
	first := mockllm.New(mockllm.Turn{Chunks: []port.Chunk{
		{Kind: port.ChunkUsage, Usage: &session.Usage{InputTokens: 5, OutputTokens: 1}},
	}, Err: terminal})
	second := mockllm.New(mockllm.Turn{Chunks: []port.Chunk{
		{Kind: port.ChunkText, Text: `{"kind":"abstained","candidates":[]}`},
		{Kind: port.ChunkUsage, Usage: &session.Usage{InputTokens: 8, OutputTokens: 2}},
		{Kind: port.ChunkDone, Stop: session.StopEndTurn},
	}})
	provider := retryingProvider{attempts: []*mockllm.Provider{first, second}}
	reflector, err := agent.NewEvidenceReflectorForProviderModel(provider, identity, nil, agent.ReflectionLimits{})
	if err != nil {
		t.Fatal(err)
	}
	_, usage, err := reflector.Reflect(context.Background(), input)
	if err != nil {
		t.Fatalf("reflection after retry: %v", err)
	}
	if first.Calls() != 1 || second.Calls() != 1 {
		t.Fatalf("physical attempts = (%d,%d), want (1,1)", first.Calls(), second.Calls())
	}
	want := session.Usage{InputTokens: 13, OutputTokens: 3}
	got := usage.Buckets[session.UsageKindReflection]
	if got.Total != want || got.Models["provider-r/reflection-model"] != want {
		t.Fatalf("partial/retry usage = %#v, want each emission once: %#v", got, want)
	}
}
