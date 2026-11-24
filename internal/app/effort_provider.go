package app

import (
	"context"
	"iter"
	"slices"
	"strings"
	"sync"

	"github.com/stacklok/mecatl/engine/port"
)

// Reasoning effort is an adapter construction option, never a port.LLMRequest
// field, and one provider adapter serves requests for many models: subagents,
// slots and plan mode reuse a session's provider with another model. So the
// effort is decided per request, here in composition: effortProvider resolves the
// request's own model's effort levels and delegates to an adapter built for the
// level that model accepts.

// effortAdapters builds and keeps one adapter per applied effort ("" is Auto)
// for a provider and capability set. Every wrapper over the same provider and
// capabilities shares one set, so a level is built once.
type effortAdapters struct {
	remint  func(effort string) port.LLMProvider
	mu      sync.Mutex
	byLevel map[string]port.LLMProvider
}

func newEffortAdapters(auto port.LLMProvider, remint func(effort string) port.LLMProvider) *effortAdapters {
	return &effortAdapters{remint: remint, byLevel: map[string]port.LLMProvider{"": auto}}
}

// at returns the adapter for an applied effort, building it on first use.
func (a *effortAdapters) at(level string) port.LLMProvider {
	a.mu.Lock()
	defer a.mu.Unlock()
	if adapter, ok := a.byLevel[level]; ok {
		return adapter
	}
	adapter := a.remint(level)
	a.byLevel[level] = adapter
	return adapter
}

// effortProvider sends each request at the effort its own model accepts: the
// requested level when the model lists it or its levels are unknown, otherwise
// the highest level it lists below the request, and Auto when it lists none.
// The levels come with the provider's rules applied (effortLevelsFor), so on
// Chat Completions an unknown model gets Auto, and on a capped provider at most
// high.
type effortProvider struct {
	adapters   *effortAdapters
	requested  string // a level from effortVocabulary, or "" for Auto
	providerID string
	model      string // the model the provider was resolved for; used when a request names none
	levels     func(model string) effortLevelsResolution
	diag       port.Diagnostics
	// warned holds the changed levels already narrated (effortNoticeKey →
	// struct{}). A session's wrapper and the children that inherit its level
	// share one set, so a change is narrated once per session and model,
	// whichever engine sends the request.
	warned *sync.Map
}

type effortNoticeKey struct{ providerID, model, level string }

var _ port.LLMProvider = (*effortProvider)(nil)

func (p *effortProvider) Stream(ctx context.Context, req port.LLMRequest) (iter.Seq2[port.Chunk, error], error) {
	return p.adapters.at(p.appliedFor(ctx, req.Model)).Stream(ctx, req)
}

func (p *effortProvider) Capabilities() port.ProviderCapabilities {
	return p.adapters.at("").Capabilities()
}

// resolve returns the effort a request for model is sent at, with the levels
// that decided it.
func (p *effortProvider) resolve(model string) (string, string, effortLevelsResolution) {
	if strings.TrimSpace(model) == "" {
		model = p.model
	}
	if p.requested == "" {
		return "", model, effortLevelsResolution{}
	}
	resolution := p.levels(model)
	return appliedEffort(p.requested, resolution.effortLevels), model, resolution
}

// appliedFor returns the effort a request for model is sent at, and narrates a
// changed level once per session and model.
func (p *effortProvider) appliedFor(ctx context.Context, model string) string {
	applied, model, resolution := p.resolve(model)
	if applied != p.requested {
		p.narrateChange(ctx, model, applied, resolution)
	}
	return applied
}

// narrateChange warns, once per session and model, that requests aren't sent at
// the requested level. A provider that never sends effort (the mock) is not
// narrated.
func (p *effortProvider) narrateChange(ctx context.Context, model, applied string, resolution effortLevelsResolution) {
	if p.diag == nil || resolution.source == effortLevelsNotSent {
		return
	}
	if _, seen := p.warned.LoadOrStore(effortNoticeKey{p.providerID, model, p.requested}, struct{}{}); seen {
		return
	}
	p.diag.Log(ctx, port.LevelWarn, effortChangeMessage("the model", applied, resolution.source),
		"provider", p.providerID, "model", model, "requested", p.requested, "applied", applied,
		"levels", strings.Join(resolution.levels, ","), "source", string(resolution.source))
}

// effortChangeMessage says why subject's requests aren't sent at the requested
// level, and what they carry instead.
func effortChangeMessage(subject, applied string, source effortLevelsSource) string {
	switch {
	case source == effortLevelsNotConfigured:
		return "reasoning-effort: " + subject + " is on Chat Completions and models.effort_levels doesn't list it; sending no effort"
	case source == effortLevelsCapped:
		return "reasoning-effort: " + subject + "'s effort levels are unknown and its provider may not accept levels above high; sending high"
	case applied == "":
		return "reasoning-effort: " + subject + " lists no effort level at or below the requested one; sending no effort"
	}
	return "reasoning-effort: " + subject + " doesn't accept the requested level; sending the highest level it lists below it"
}

// appliedEffortReporter returns the engine's AppliedReasoningEffort for a
// provider: what an effort-wrapped provider sends each model. Composition wraps
// every provider that can send effort, so any other provider it hands an engine
// (the Auto adapter housekeeping runs on, the mock) sends none.
func appliedEffortReporter(provider port.LLMProvider) func(model string) string {
	wrapped, ok := provider.(*effortProvider)
	if !ok {
		return func(string) string { return "" }
	}
	return func(model string) string {
		applied, _, _ := wrapped.resolve(model)
		return applied
	}
}

// requesting returns a wrapper over the same adapters that requests level and
// narrates its changed levels into warned.
func (p *effortProvider) requesting(level string, warned *sync.Map) *effortProvider {
	return &effortProvider{adapters: p.adapters, requested: level, providerID: p.providerID, model: p.model,
		levels: p.levels, diag: p.diag, warned: warned}
}

// appliedEffort decides the effort a request carries: the requested level when
// the model lists it or its levels are unknown, otherwise the highest level the
// model lists below it, and none when it lists none below it.
func appliedEffort(requested string, levels effortLevels) string {
	if requested == "" || !levels.known || slices.Contains(levels.levels, requested) {
		return requested
	}
	ceiling := slices.Index(effortVocabulary, requested)
	applied := ""
	for _, level := range levels.levels { // in vocabulary order
		if slices.Index(effortVocabulary, level) < ceiling {
			applied = level
		}
	}
	return applied
}

// atAuto returns the adapter of an effort-wrapped provider that sends no effort.
// Housekeeping calls (compaction, titles, reviewers, the router classifier,
// learning) use it so a session's effort never raises their spend. Any other
// provider is returned unchanged.
func atAuto(provider port.LLMProvider) port.LLMProvider {
	if wrapped, ok := provider.(*effortProvider); ok {
		return wrapped.adapters.at("")
	}
	return provider
}

// inheritEffort returns child requesting the level parent requests, so a
// subagent, team member or routed child on another provider's adapter keeps the
// session's effort. Each request is still checked against its own model, and its
// changed levels are narrated with the session's. A child or parent that isn't
// effort-wrapped is returned unchanged.
func inheritEffort(parent, child port.LLMProvider) port.LLMProvider {
	from, ok := parent.(*effortProvider)
	to, wraps := child.(*effortProvider)
	if !ok || !wraps {
		return child
	}
	return to.requesting(from.requested, from.warned)
}

// modelEffortLevels resolves a provider's model's own effort levels against the
// operator setting, the current live snapshot and the catalog.
func (r *providerRegistry) modelEffortLevels(pid, model string) effortLevelsResolution {
	return resolveModelEffortLevels(Config{effortLevels: r.effortLevels}, r.meta.current(), pid, model)
}

// effortLevelsFor returns the levels a request for pid's model is decided
// against: the model's own levels under the provider's rules. The request path,
// the session echo and the startup warning all read it, so they agree.
func (r *providerRegistry) effortLevelsFor(pid, model string) effortLevelsResolution {
	entry, ok := r.Lookup(pid)
	return providerEffortLevels(pid, entry, ok, r.modelEffortLevels(pid, model))
}

// providerEffortLevels applies a provider's rules to a model's own levels. The
// mock never sends effort. On Chat Completions, where many models reject the
// parameter, a model whose levels nothing knows takes none until
// models.effort_levels lists it. On a capped provider such a model takes low,
// medium and high. Any other unknown model fails open: it is sent the requested
// level.
func providerEffortLevels(pid string, entry providerEntry, found bool, own effortLevelsResolution) effortLevelsResolution {
	switch {
	case pid == providerMock:
		return effortLevelsResolution{knownEffortLevels(nil), effortLevelsNotSent}
	case !own.known && found && entry.protocol == protocolOpenAIChatCompletions:
		return effortLevelsResolution{knownEffortLevels(nil), effortLevelsNotConfigured}
	case !own.known && slices.Contains(cappedEffortProviders, pid):
		return effortLevelsResolution{knownEffortLevels([]string{effortLow, effortMedium, effortHigh}), effortLevelsCapped}
	}
	return own
}

// cappedEffortProviders can front models that don't take xhigh or max: OpenAI's
// reasoning models list up to high, and a gateway can't tell which upstream
// serves a model. A model on them whose levels nothing knows is sent at most
// high, so an operator default of max doesn't fail every request.
var cappedEffortProviders = []string{providerOpenAI, providerOpenAICodex, providerOpenRouter, providerToolhive}

// appliedEffortFor returns the effort a request for pid's model is sent at when
// requested is asked for.
func (r *providerRegistry) appliedEffortFor(pid, model, requested string) string {
	return appliedEffort(requested, r.effortLevelsFor(pid, model).effortLevels)
}

// newEffortProvider wraps adapters for pid so each request sends requested, or
// the level its model accepts in its place.
func (r *providerRegistry) newEffortProvider(entry providerEntry, model, requested string, adapters *effortAdapters) *effortProvider {
	pid := entry.id
	return &effortProvider{
		adapters:   adapters,
		requested:  requested,
		providerID: pid,
		model:      model,
		levels:     func(model string) effortLevelsResolution { return r.effortLevelsFor(pid, model) },
		diag:       r.diag,
		warned:     &sync.Map{},
	}
}

// warnOperatorEffortNotAccepted narrates, once at startup, an operator default
// the default model is known not to accept: requests to that model carry a lower
// level or none. Levels learned later from a live listing are narrated per
// request.
func warnOperatorEffortNotAccepted(ctx context.Context, reg *providerRegistry, requested string) {
	if requested == "" || reg.defaultID == "" || reg.diag == nil {
		return
	}
	resolution := reg.effortLevelsFor(reg.defaultID, reg.defaultModel)
	applied := appliedEffort(requested, resolution.effortLevels)
	if resolution.source == effortLevelsNotSent || applied == requested {
		return
	}
	reg.diag.Log(ctx, port.LevelWarn, effortChangeMessage("the default model", applied, resolution.source),
		"provider", reg.defaultID, "model", reg.defaultModel, "requested", requested, "applied", applied,
		"levels", strings.Join(resolution.levels, ","), "source", string(resolution.source))
}

// capsAdapters returns adapters for entry built with caps. When the entry's
// shared provider was built for the same capabilities, its Auto adapter is
// reused (the shared set, or the entry's own provider when it isn't wrapped);
// otherwise a new set is built for caps.
func capsAdapters(entry providerEntry, caps port.ProviderCapabilities) *effortAdapters {
	remint := func(effort string) port.LLMProvider { return entry.remint(effort, caps) }
	if entry.defaultCaps == caps || entry.defaultCaps == (port.ProviderCapabilities{}) {
		if shared, ok := entry.provider.(*effortProvider); ok {
			return shared.adapters
		}
		if entry.provider != nil {
			return newEffortAdapters(entry.provider, remint)
		}
	}
	return newEffortAdapters(entry.remint("", caps), remint)
}
