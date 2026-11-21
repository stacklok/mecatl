package app

import (
	"context"
	"slices"
	"sync"
	"testing"

	agents "github.com/stacklok/mecatl/engine/adapter/agentfs"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/prompt"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/hookexec"
)

// adapterRecorder records each adapter an entry builds, as "<provider>:<effort>".
type adapterRecorder struct {
	mu    sync.Mutex
	built []string
}

func (r *adapterRecorder) adapter(pid, effort string) port.LLMProvider {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.built = append(r.built, pid+":"+effort)
	return mockllm.New(mockllm.TextTurn(pid + ":" + effort))
}

// twoProviderEffortRegistry builds an OpenAI and an Anthropic entry the way
// buildProviderRegistry does: each shared provider is an effortProvider at the
// operator default over its own Auto adapter.
func twoProviderEffortRegistry(operatorEffort string) (*providerRegistry, *adapterRecorder) {
	rec := &adapterRecorder{}
	reg := &providerRegistry{entries: map[string]providerEntry{}, defaultID: providerOpenAI, meta: newMetadataFixture()}
	for _, pid := range []string{providerOpenAI, providerAnthropic} {
		entry := providerEntry{id: pid, available: true, defaultEffort: operatorEffort,
			remint: func(effort string, _ port.ProviderCapabilities) port.LLMProvider { return rec.adapter(pid, effort) }}
		if pid == providerAnthropic {
			entry.protocol = protocolAnthropicMessages
		}
		entry.provider = reg.newEffortProvider(entry, "", operatorEffort, newEffortAdapters(rec.adapter(pid, ""), func(effort string) port.LLMProvider {
			return entry.remint(effort, port.ProviderCapabilities{})
		}))
		reg.entries[pid] = entry
	}
	return reg, rec
}

func sharedEffort(reg *providerRegistry, pid string) *effortProvider {
	return reg.entries[pid].provider.(*effortProvider)
}

// sessionEffortProvider wraps pid's shared adapters at requested, as a session does.
func sessionEffortProvider(reg *providerRegistry, pid, model, requested string) *effortProvider {
	return reg.newEffortProvider(reg.entries[pid], model, requested, sharedEffort(reg, pid).adapters)
}

func TestEffortStreamUsesTheAppliedAdapter(t *testing.T) {
	reg, rec := twoProviderEffortRegistry("")
	sessionProvider := sessionEffortProvider(reg, providerOpenAI, "gpt-5", "high")
	if _, err := sessionProvider.Stream(context.Background(), port.LLMRequest{}); err != nil {
		t.Fatalf("Stream: %v", err)
	}
	// gpt-5 lists high in the catalog, and a request naming no model is checked
	// against the session's model.
	if want := []string{"openai:", "anthropic:", "openai:high"}; !slices.Equal(rec.built, want) {
		t.Fatalf("adapters built = %q, want %q", rec.built, want)
	}
	if _, err := sessionProvider.Stream(context.Background(), port.LLMRequest{Model: "gpt-5"}); err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if len(rec.built) != 3 {
		t.Fatalf("a level's adapter was built twice: %q", rec.built)
	}
}

func TestEffortDropWarnsOnce(t *testing.T) {
	reg, _ := twoProviderEffortRegistry("")
	diag := &recordingDiag{}
	reg.diag = diag
	reg.meta.setMetadataFixture(map[string][]modelEntry{providerOpenAI: {
		{ID: "no-effort", Effort: knownEffortLevels(nil)},
		{ID: "low-only", Effort: knownEffortLevels([]string{effortLow})},
	}})
	ctx := context.Background()
	sessionProvider := sessionEffortProvider(reg, providerOpenAI, "no-effort", effortHigh)
	for range 3 {
		if got := sessionProvider.appliedFor(ctx, "no-effort"); got != "" {
			t.Fatalf("applied = %q, want none", got)
		}
	}
	if got := sessionProvider.appliedFor(ctx, "low-only"); got != "" {
		t.Fatalf("applied = %q, want none", got)
	}
	if n := countMessages(diag, "doesn't accept the requested level"); n != 2 {
		t.Fatalf("drop WARNs = %d, want one per model: %v", n, diag.messages())
	}
	other := sessionProvider.withRequested(effortHigh)
	other.appliedFor(ctx, "no-effort")
	if n := countMessages(diag, "doesn't accept the requested level"); n != 3 {
		t.Fatalf("another session's drop was not narrated: %d WARNs", n)
	}
}

func TestAppliedEffort(t *testing.T) {
	listed := knownEffortLevels([]string{effortLow, effortHigh})
	for _, tc := range []struct {
		name, requested string
		levels          effortLevels
		failOpen        bool
		want            string
	}{
		{"auto sends nothing", "", listed, true, ""},
		{"listed level", effortHigh, listed, true, effortHigh},
		{"unlisted level", effortMax, listed, true, ""},
		{"known none", effortHigh, knownEffortLevels(nil), true, ""},
		{"unknown fails open", effortMax, effortLevels{}, true, effortMax},
		{"unknown on Chat Completions", effortMax, effortLevels{}, false, ""},
		{"listed on Chat Completions", effortHigh, listed, false, effortHigh},
	} {
		if got := appliedEffort(tc.requested, tc.levels, tc.failOpen); got != tc.want {
			t.Errorf("%s: appliedEffort = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// TestEffortCrossProviderSubagentInherits: a child on another provider requests
// the session's level and is checked against its own model; the shared entry
// keeps the operator default, and an explicit Auto session is inherited too.
func TestEffortCrossProviderSubagentInherits(t *testing.T) {
	ctx := context.Background()
	reg, _ := twoProviderEffortRegistry(effortHigh)
	parent := sessionEffortProvider(reg, providerOpenAI, "gpt-5", effortMax)

	child, pid, _, _ := resolveChildProvider(Config{}, reg, agents.AgentDef{Provider: providerAnthropic, Model: "claude-opus-4-7"}, parent, providerOpenAI, "gpt-5")
	wrapped, ok := child.(*effortProvider)
	if !ok || pid != providerAnthropic || wrapped.requested != effortMax {
		t.Fatalf("child = %T on %q requesting %q, want an Anthropic effortProvider requesting max", child, pid, wrapped.requested)
	}
	if got := wrapped.appliedFor(ctx, "claude-opus-4-7"); got != effortMax {
		t.Errorf("claude-opus-4-7 applied = %q, want max", got)
	}
	if got := wrapped.appliedFor(ctx, "claude-opus-4-5"); got != "" {
		t.Errorf("claude-opus-4-5 applied = %q, want none (lists low, medium, high)", got)
	}
	if got := sharedEffort(reg, providerAnthropic).requested; got != effortHigh {
		t.Errorf("shared Anthropic entry requests %q, want the operator default high", got)
	}
	if wrapped.adapters != sharedEffort(reg, providerAnthropic).adapters {
		t.Error("the inheriting child does not share the entry's adapters")
	}

	same, _, _, _ := resolveChildProvider(Config{}, reg, agents.AgentDef{Model: "o3"}, parent, providerOpenAI, "gpt-5")
	if same != port.LLMProvider(parent) {
		t.Error("a same-provider child does not reuse the session's provider")
	}

	autoParent := sessionEffortProvider(reg, providerOpenAI, "gpt-5", "")
	autoChild, _, _, _ := resolveChildProvider(Config{}, reg, agents.AgentDef{Provider: providerAnthropic, Model: "claude-opus-4-7"}, autoParent, providerOpenAI, "gpt-5")
	if got := autoChild.(*effortProvider).requested; got != "" {
		t.Errorf("child of an Auto session requests %q, want Auto", got)
	}
}

// TestEffortHousekeepingAtAuto: compaction, slots, the guardrail checker and the
// ask reviewer get the Auto adapter even when the session requests max.
func TestEffortHousekeepingAtAuto(t *testing.T) {
	reg, _ := twoProviderEffortRegistry(effortMax)
	sessionProvider := sessionEffortProvider(reg, providerOpenAI, "gpt-5", effortMax)
	openAIAuto := sharedEffort(reg, providerOpenAI).adapters.at("")
	anthropicAuto := sharedEffort(reg, providerAnthropic).adapters.at("")
	if atAuto(sessionProvider) != openAIAuto {
		t.Fatal("atAuto did not return the Auto adapter")
	}
	if plain := mockllm.New(); atAuto(plain) != port.LLMProvider(plain) {
		t.Fatal("atAuto changed a provider that isn't effort-wrapped")
	}

	aliases := ModelAliases{"small": {Model: "gpt-5-mini"}, "other": {ProviderID: providerAnthropic, Model: "claude-haiku-4-5"}}
	for _, tc := range []struct {
		name string
		slot string
		want port.LLMProvider
	}{
		{"unconfigured slot", "", openAIAuto},
		{"same-provider slot", "small", openAIAuto},
		{"other-provider slot", "other", anthropicAuto},
	} {
		cfg := Config{ModelAliasTargets: aliases, ModelSlots: map[string]string{}}
		if tc.slot != "" {
			cfg.ModelSlots[slotCompaction] = tc.slot
		}
		if got, _, _, _ := resolveAuxiliarySlotTarget(cfg, reg, slotCompaction, sessionProvider, providerOpenAI, "gpt-5"); got != tc.want {
			t.Errorf("%s: slot provider is not the Auto adapter", tc.name)
		}
	}

	cfg := Config{Compaction: "cascade", modelProviderRegistry: reg}
	deps := engineDepsForProvider(cfg, sessionProvider, session.ProviderModelID{ProviderID: providerOpenAI, ModelID: "gpt-5"}, func() int { return 128000 },
		memstore.New(), permpolicy.NewPolicy(nil, nil), hookexec.New(nil), nil, prompt.NewMultiAssembler())
	if deps.LLM != port.LLMProvider(sessionProvider) {
		t.Error("the main engine lost the session's effort provider")
	}
	if compactor, ok := deps.Compactor.(agent.CascadeCompactor); !ok || compactor.LLM != openAIAuto {
		t.Errorf("compactor = %T, want a cascade compactor on the Auto adapter", deps.Compactor)
	}

	guard := Config{guardrailConfigured: true, guardrailProviderID: providerAnthropic, guardrailModel: "claude-haiku-4-5"}
	if deps, ok := guardrailsCheckerDeps(guard, reg, sessionProvider, providerOpenAI, ""); !ok || deps.LLM != anthropicAuto {
		t.Errorf("guardrail checker deps ok=%v, want the Anthropic Auto adapter", ok)
	}
	if deps, ok := askAdjudicatorDeps(Config{SubagentAskReviewerModel: "small", ModelAliasTargets: aliases}, reg, sessionProvider, providerOpenAI, "gpt-5"); !ok || deps.LLM != openAIAuto {
		t.Errorf("ask reviewer deps ok=%v, want the OpenAI Auto adapter", ok)
	}
}

func TestEffortStartupWarnsWhenDefaultModelRefusesOperatorDefault(t *testing.T) {
	for _, tc := range []struct {
		model string
		warns bool
	}{
		{"claude-opus-4-5", true},  // lists low, medium, high
		{"claude-opus-4-7", false}, // lists max
		{"unknown-model", false},   // unknown fails open
	} {
		reg, _ := twoProviderEffortRegistry(effortMax)
		diag := &recordingDiag{}
		reg.diag, reg.defaultID, reg.defaultModel = diag, providerAnthropic, tc.model
		warnOperatorEffortNotAccepted(context.Background(), reg, effortMax)
		if got := diag.has("doesn't accept the operator default"); got != tc.warns {
			t.Errorf("%s: startup WARN = %v, want %v", tc.model, got, tc.warns)
		}
	}
}

// TestAwaitEffortLevelsAsksAnUnansweredListerOnce: unknown levels on a provider
// whose listing hasn't answered trigger one bounded listing; known levels or an
// answered provider don't.
func TestAwaitEffortLevelsAsksAnUnansweredListerOnce(t *testing.T) {
	lister := &fakeLister{models: []modelEntry{{ID: "listed", Effort: knownEffortLevels([]string{effortLow})}}}
	d := discoveryFixture(t, map[string]providerEntry{"custom": {lister: lister}})
	ctx := context.Background()
	awaitEffortLevels(ctx, d.reg, "custom", "gpt-5-unlisted-elsewhere")
	if got := lister.calls.Load(); got != 1 {
		t.Fatalf("listing calls = %d, want 1", got)
	}
	if got := d.reg.effortLevelsFor("custom", "listed"); !got.known || got.source != effortLevelsFromLive {
		t.Fatalf("levels after the wait = %+v, want live", got)
	}
	awaitEffortLevels(ctx, d.reg, "custom", "still-unknown")
	if got := lister.calls.Load(); got != 1 {
		t.Fatalf("listing calls = %d after the provider answered, want 1", got)
	}
}
