package app

import (
	"sort"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

func projectModelEntry(reg *providerRegistry, cfg Config, view *discoverySnapshot, pid string, m modelEntry) *mecatlv1.ModelInfo {
	name := m.DisplayName
	if name == "" {
		name = m.ID
	}
	cached := reg.promptCached != nil && reg.promptCached(pid, m.ID)
	effort := publishedEffortLevels(reg, cfg, view, pid, m.ID)
	return &mecatlv1.ModelInfo{Id: m.ID, ProviderId: pid, DisplayName: name,
		ContextLimit: int64(resolveModelWindow(cfg, view, pid, m.ID).tokens),
		Image:        modelCapabilityCandidate(reg, view, pid, m.ID).Image,
		Reasoning:    m.Reasoning, PromptCached: cached,
		EffortLevels: effort.levels, EffortLevelsKnown: effort.known}
}

// publishedEffortLevels is what ListModels reports for a model: the levels a
// session on it sends. Chat Completions sends effort only for models the
// operator configured, so there a model of unknown levels is reported as taking
// none.
func publishedEffortLevels(reg *providerRegistry, cfg Config, view *discoverySnapshot, pid, model string) effortLevels {
	resolution := resolveModelEffortLevels(cfg, view, pid, model)
	if !resolution.known {
		if entry, ok := reg.Lookup(pid); ok && entry.protocol == protocolOpenAIChatCompletions {
			return knownEffortLevels(nil)
		}
	}
	return resolution.effortLevels
}

func modelCapabilityCandidate(reg *providerRegistry, view *discoverySnapshot, pid, model string) port.ProviderCapabilities {
	caps := modelAdapterCaps(reg, pid)
	if m, ok := view.lookup(pid, model); ok && m.InputModalities != nil {
		caps.Image = caps.Image && hasImageModality(m.InputModalities)
		caps.Audio = caps.Audio && hasAudioModality(m.InputModalities)
		return caps
	}
	image, audio, known := catalogModalities(pid, model)
	if known {
		caps.Image = caps.Image && image
		caps.Audio = caps.Audio && audio
	}
	return caps
}

func providerStatusProto(reg *providerRegistry, view *discoverySnapshot) []*mecatlv1.ProviderStatus {
	var out []*mecatlv1.ProviderStatus
	for pid, state := range view.providers {
		entry, available := reg.Lookup(pid)
		_, unavailableNative := reg.unavailableNative[pid]
		if !unavailableNative && (!available || (!entry.intentDriven && pid != providerOpenAICodex && entry.defaultModel == "")) {
			continue
		}
		if state.outcome.State == "" {
			continue
		}
		other := entry.intentDriven && state.outcome.State == statusOK && pid != view.defaults.provider
		if isToolhiveProvider(pid) && isToolhiveProvider(view.defaults.provider) {
			other = false
		}
		out = append(out, &mecatlv1.ProviderStatus{ProviderId: pid, State: state.outcome.State, Hint: state.outcome.Hint,
			ModelCount: server.ClampInt32(len(state.observations)), AvailableNotDefault: other,
			DefaultModelAutoSelected: pid == view.defaults.provider && view.defaults.autoSelected})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ProviderId < out[j].ProviderId })
	return out
}

func (r *providerRegistry) healDefaultModelCandidate(pid string, view *discoverySnapshot) *diagFact {
	if pid != r.Default() {
		return nil
	}
	entry, ok := r.Lookup(pid)
	if !ok || (!entry.intentDriven && pid != providerOpenAICodex) {
		return nil
	}
	models := view.providers[pid].observations
	if len(models) == 0 {
		return nil
	}
	r.defaultModelMu.Lock()
	if r.defaultModel != "" {
		r.defaultModelMu.Unlock()
		return nil
	}
	model := models[0].ID
	r.defaultModel = model
	r.defaultModelAutoSelected = true
	r.defaultModelMu.Unlock()
	r.remintEntryCandidate(pid, model, view)
	return &diagFact{level: port.LevelInfo, msg: "provider default model (auto-selected) after live refresh",
		args: []any{"provider", pid, "model", model, "base_url", entry.baseURL, "gateway_url", entry.intentGatewayURL}}
}

func (r *providerRegistry) remintEntryCandidate(pid, model string, view *discoverySnapshot) {
	entry, ok := r.Lookup(pid)
	if !ok || entry.remint == nil {
		return
	}
	caps := modelCapabilityCandidate(r, view, pid, model)
	var provider port.LLMProvider
	if auto := entry.remint("", caps); auto != nil {
		adapters := newEffortAdapters(auto, func(effort string) port.LLMProvider { return entry.remint(effort, caps) })
		provider = r.newEffortProvider(entry, model, entry.defaultEffort, adapters)
	}
	r.entriesMu.Lock()
	entry = r.entries[pid]
	entry.provider, entry.defaultCaps = provider, caps
	r.entries[pid] = entry
	r.entriesMu.Unlock()
}
