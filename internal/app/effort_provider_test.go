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

// TestEffortChangeWarnsOnce: a level a model doesn't accept is lowered to one it
// lists, or dropped when it lists none below it, and warned once per session and
// model.
func TestEffortChangeWarnsOnce(t *testing.T) {
	reg, _ := twoProviderEffortRegistry("")
	diag := &recordingDiag{}
	reg.diag, sharedEffort(reg, providerAnthropic).diag = diag, diag
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
	if got := sessionProvider.appliedFor(ctx, "low-only"); got != effortLow {
		t.Fatalf("applied = %q, want low, the highest level it lists below high", got)
	}
	if !diag.has("lists no effort level at or below the requested one") || !diag.has("doesn't accept the requested level") {
		t.Fatalf("WARNs don't say the level was dropped and lowered: %v", diag.messages())
	}
	if n := countMessages(diag, "reasoning-effort: the model"); n != 2 {
		t.Fatalf("effort WARNs = %d, want one per model: %v", n, diag.messages())
	}
	// Children that inherit the session's level share its narration, however many
	// child engines are built.
	for range 3 {
		child := inheritEffort(sessionProvider, sharedEffort(reg, providerAnthropic)).(*effortProvider)
		if got := child.appliedFor(ctx, "claude-haiku-4-5"); got != "" { // the catalog lists no effort
			t.Fatalf("child applied = %q, want none", got)
		}
	}
	if n := countMessages(diag, "reasoning-effort: the model"); n != 3 {
		t.Fatalf("effort WARNs = %d after three inheriting children, want one more", n)
	}
	other := sessionProvider.requesting(effortHigh, &sync.Map{})
	other.appliedFor(ctx, "no-effort")
	if n := countMessages(diag, "reasoning-effort: the model"); n != 4 {
		t.Fatalf("another session's change was not narrated: %d WARNs", n)
	}
}

// TestEffortChatCompletionsDropNamesTheSetting: a Chat Completions model nothing
// knows the levels of gets no effort, and both the per-request and the startup
// warning name models.effort_levels.
func TestEffortChatCompletionsDropNamesTheSetting(t *testing.T) {
	ctx := context.Background()
	reg, _ := twoProviderEffortRegistry("")
	entry := reg.entries[providerOpenAI]
	entry.protocol = protocolOpenAIChatCompletions
	reg.entries[providerOpenAI] = entry
	diag := &recordingDiag{}
	reg.diag = diag
	if got := sessionEffortProvider(reg, providerOpenAI, "chat-model", effortHigh).appliedFor(ctx, "chat-model"); got != "" {
		t.Fatalf("applied = %q, want none", got)
	}
	if !diag.has("the model is on Chat Completions and models.effort_levels doesn't list it") {
		t.Fatalf("drop WARN doesn't name the setting: %v", diag.messages())
	}
	reg.defaultID, reg.defaultModel = providerOpenAI, "chat-model"
	warnOperatorEffortNotAccepted(ctx, reg, effortHigh)
	if !diag.has("the default model is on Chat Completions and models.effort_levels doesn't list it") {
		t.Fatalf("startup WARN doesn't name the setting: %v", diag.messages())
	}
}

// TestProviderEffortLevels pins the provider rules every effort decision reads:
// the mock never sends effort; a Chat Completions model needs known levels; an
// unknown model on a capped provider gets at most high; anything else unknown
// fails open.
func TestProviderEffortLevels(t *testing.T) {
	remint := func(string, port.ProviderCapabilities) port.LLMProvider { return mockllm.New() }
	unknown := effortLevelsResolution{source: effortLevelsUnknown}
	listed := effortLevelsResolution{knownEffortLevels([]string{effortHigh}), effortLevelsFromLive}
	responses := providerEntry{remint: remint, protocol: protocolOpenAIResponses}
	chat := providerEntry{remint: remint, protocol: protocolOpenAIChatCompletions}
	for _, tc := range []struct {
		name, pid string
		entry     providerEntry
		found     bool
		own       effortLevelsResolution
		source    effortLevelsSource
		applied   string
	}{
		{"unknown fails open", "gateway", responses, true, unknown, effortLevelsUnknown, effortMax},
		{"unregistered provider fails open", "gone", providerEntry{}, false, unknown, effortLevelsUnknown, effortMax},
		{"Chat Completions needs known levels", "opencode", chat, true, unknown, effortLevelsNotConfigured, ""},
		{"Chat Completions listing that publishes levels", "opencode", chat, true, listed, effortLevelsFromLive, effortHigh},
		{"the mock never sends effort", providerMock, providerEntry{}, true, listed, effortLevelsNotSent, ""},
		{"unknown on OpenAI is capped at high", providerOpenAI, responses, true, unknown, effortLevelsCapped, effortHigh},
		{"unknown on the ToolHive gateway is capped at high", providerToolhive, responses, true, unknown, effortLevelsCapped, effortHigh},
		{"unknown on OpenRouter is capped at high", providerOpenRouter, responses, true, unknown, effortLevelsCapped, effortHigh},
		{"unknown on Codex is capped at high", providerOpenAICodex, responses, true, unknown, effortLevelsCapped, effortHigh},
		{"a capped provider's listed levels decide", providerOpenAI, responses, true, effortLevelsResolution{knownEffortLevels([]string{effortMax}), effortLevelsFromLive}, effortLevelsFromLive, effortMax},
	} {
		got := providerEffortLevels(tc.pid, tc.entry, tc.found, tc.own)
		if applied := appliedEffort(effortMax, got.effortLevels); got.source != tc.source || applied != tc.applied {
			t.Errorf("%s: source=%q applied=%q, want %q and %q", tc.name, got.source, applied, tc.source, tc.applied)
		}
	}
}

func TestAppliedEffort(t *testing.T) {
	listed := knownEffortLevels([]string{effortLow, effortHigh})
	for _, tc := range []struct {
		name, requested string
		levels          effortLevels
		want            string
	}{
		{"auto sends nothing", "", listed, ""},
		{"listed level", effortHigh, listed, effortHigh},
		{"above every listed level", effortMax, listed, effortHigh},
		{"between listed levels", effortMedium, listed, effortLow},
		{"below every listed level", effortLow, knownEffortLevels([]string{effortMedium, effortHigh}), ""},
		{"known none", effortHigh, knownEffortLevels(nil), ""},
		{"unknown fails open", effortMax, effortLevels{}, effortMax},
	} {
		if got := appliedEffort(tc.requested, tc.levels); got != tc.want {
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
	if got := wrapped.appliedFor(ctx, "claude-opus-4-5"); got != effortHigh {
		t.Errorf("claude-opus-4-5 applied = %q, want high (lists low, medium, high)", got)
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
		pid, model string
		warning    string
	}{
		{providerAnthropic, "claude-opus-4-5", "the default model doesn't accept the requested level"}, // lists low, medium, high
		{providerAnthropic, "claude-opus-4-7", ""},                                                     // lists max
		{providerAnthropic, "unknown-model", ""},                                                       // unknown fails open
		{providerOpenAI, "unknown-model", "the default model's effort levels are unknown"},             // capped at high
	} {
		reg, _ := twoProviderEffortRegistry(effortMax)
		diag := &recordingDiag{}
		reg.diag, reg.defaultID, reg.defaultModel = diag, tc.pid, tc.model
		warnOperatorEffortNotAccepted(context.Background(), reg, effortMax)
		if got := countMessages(diag, "reasoning-effort:"); (tc.warning == "") != (got == 0) || (tc.warning != "" && !diag.has(tc.warning)) {
			t.Errorf("%s/%s: startup WARNs = %v, want %q", tc.pid, tc.model, diag.messages(), tc.warning)
		}
	}
}

// TestAwaitEffortLevelsAsksAnUnansweredListerOnce: levels neither the setting
// nor a listing knows, on a provider whose listing hasn't answered, trigger one
// bounded listing, even when the catalog knows the model; an answered provider
// doesn't.
func TestAwaitEffortLevelsAsksAnUnansweredListerOnce(t *testing.T) {
	lister := &fakeLister{models: []modelEntry{{ID: "listed", Effort: knownEffortLevels([]string{effortLow})}}}
	d := discoveryFixture(t, map[string]providerEntry{"custom": {lister: lister}})
	ctx := context.Background()
	awaitEffortLevels(ctx, d.reg, "custom", "gpt-5-unlisted-elsewhere")
	if got := lister.calls.Load(); got != 1 {
		t.Fatalf("listing calls = %d, want 1", got)
	}
	if got := d.reg.modelEffortLevels("custom", "listed"); !got.known || got.source != effortLevelsFromLive {
		t.Fatalf("levels after the wait = %+v, want live", got)
	}
	awaitEffortLevels(ctx, d.reg, "custom", "still-unknown")
	if got := lister.calls.Load(); got != 1 {
		t.Fatalf("listing calls = %d after the provider answered, want 1", got)
	}

	// The Anthropic catalog knows claude-opus-4-5 (low, medium, high); a gateway
	// route's listing still decides.
	route := &fakeLister{models: []modelEntry{{ID: "claude-opus-4-5", Effort: knownEffortLevels([]string{effortMax})}}}
	gw := discoveryFixture(t, map[string]providerEntry{providerToolhiveAnthropic: {lister: route}})
	awaitEffortLevels(ctx, gw.reg, providerToolhiveAnthropic, "claude-opus-4-5")
	if got := route.calls.Load(); got != 1 {
		t.Fatalf("listing calls = %d for a catalogued model, want 1", got)
	}
	if got := gw.reg.modelEffortLevels(providerToolhiveAnthropic, "claude-opus-4-5"); got.source != effortLevelsFromLive || !slices.Equal(got.levels, []string{effortMax}) {
		t.Fatalf("levels after the wait = %+v, want the listing's max", got)
	}
}

// TestAwaitRunEffortLevelsOnlyForAnOperatorDefault: before a run on the shared
// engine, the listing is waited for only when the operator default is a level.
func TestAwaitRunEffortLevelsOnlyForAnOperatorDefault(t *testing.T) {
	for _, tc := range []struct {
		operatorEffort string
		want           int32
	}{{"", 0}, {effortHigh, 1}} {
		lister := &fakeLister{models: []modelEntry{{ID: "listed"}}}
		d := discoveryFixture(t, map[string]providerEntry{"custom": {lister: lister, defaultEffort: tc.operatorEffort}})
		awaitRunEffortLevels(context.Background(), d.reg, "custom", "listed")
		if got := lister.calls.Load(); got != tc.want {
			t.Fatalf("operator default %q: listing calls = %d, want %d", tc.operatorEffort, got, tc.want)
		}
	}
}

// TestEngineDepsReportAppliedEffort: an engine over an effort-wrapped provider
// reports what each model is sent, for its request manifests; an engine on the
// Auto adapter (housekeeping) or the mock reports none.
func TestEngineDepsReportAppliedEffort(t *testing.T) {
	reg, _ := twoProviderEffortRegistry("")
	sessionProvider := sessionEffortProvider(reg, providerAnthropic, "claude-opus-4-7", effortMax)
	deps := engineDepsForProvider(Config{}, sessionProvider, session.ProviderModelID{ProviderID: providerAnthropic, ModelID: "claude-opus-4-7"}, func() int { return 128000 },
		memstore.New(), permpolicy.NewPolicy(nil, nil), hookexec.New(nil), nil, prompt.NewMultiAssembler())
	if deps.AppliedReasoningEffort == nil {
		t.Fatal("an effort-wrapped engine reports no applied effort")
	}
	if got := deps.AppliedReasoningEffort("claude-opus-4-7"); got != effortMax {
		t.Errorf("claude-opus-4-7 applied = %q, want max", got)
	}
	if got := deps.AppliedReasoningEffort("claude-opus-4-5"); got != effortHigh {
		t.Errorf("claude-opus-4-5 applied = %q, want high, the highest level it lists", got)
	}
	if got := deps.AppliedReasoningEffort("claude-haiku-4-5"); got != "" {
		t.Errorf("claude-haiku-4-5 applied = %q, want none: the catalog lists no effort", got)
	}
	for name, provider := range map[string]port.LLMProvider{"housekeeping": atAuto(sessionProvider), "mock": mockllm.New()} {
		deps := engineDepsForProvider(Config{}, provider, session.ProviderModelID{ProviderID: providerAnthropic, ModelID: "claude-opus-4-7"}, func() int { return 128000 },
			memstore.New(), permpolicy.NewPolicy(nil, nil), hookexec.New(nil), nil, prompt.NewMultiAssembler())
		if deps.AppliedReasoningEffort == nil {
			t.Fatalf("%s: the engine reports no applied effort, so its manifests omit it", name)
		}
		if got := deps.AppliedReasoningEffort("claude-opus-4-7"); got != "" {
			t.Errorf("%s: applied = %q, want none", name, got)
		}
	}
}
