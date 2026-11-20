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
// Auto. On Chat Completions an unknown model gets Auto too (failOpen is false),
// so only models.effort_levels opts a model in there.
type effortProvider struct {
	adapters   *effortAdapters
	requested  string // a level from effortVocabulary, or "" for Auto
	providerID string
	model      string // the model the provider was resolved for; used when a request names none
	levels     func(model string) effortLevelsResolution
	failOpen   bool
	diag       port.Diagnostics
	warned     sync.Map // dropKey → struct{}: each drop is narrated once per wrapper
}

type dropKey struct{ model, level string }

var _ port.LLMProvider = (*effortProvider)(nil)

func (p *effortProvider) Stream(ctx context.Context, req port.LLMRequest) (iter.Seq2[port.Chunk, error], error) {
	return p.adapters.at(p.appliedFor(ctx, req.Model)).Stream(ctx, req)
}

func (p *effortProvider) Capabilities() port.ProviderCapabilities {
	return p.adapters.at("").Capabilities()
}

// appliedFor returns the effort a request for model is sent at, and narrates a
// dropped level once.
func (p *effortProvider) appliedFor(ctx context.Context, model string) string {
	if p.requested == "" {
		return ""
	}
	if strings.TrimSpace(model) == "" {
		model = p.model
	}
	resolution := p.levels(model)
	applied := appliedEffort(p.requested, resolution.effortLevels, p.failOpen)
	if applied == "" {
		if _, seen := p.warned.LoadOrStore(dropKey{model, p.requested}, struct{}{}); !seen && p.diag != nil {
			p.diag.Log(ctx, port.LevelWarn,
				"reasoning-effort: the model doesn't accept the requested level; sending no effort",
				"provider", p.providerID, "model", model, "requested", p.requested,
				"levels", strings.Join(resolution.levels, ","), "levels_known", resolution.known, "source", string(resolution.source))
		}
	}
	return applied
}

// withRequested returns a wrapper over the same adapters that requests another
// level, with its own drop narration.
func (p *effortProvider) withRequested(requested string) *effortProvider {
	return &effortProvider{adapters: p.adapters, requested: requested, providerID: p.providerID, model: p.model,
		levels: p.levels, failOpen: p.failOpen, diag: p.diag}
}

// appliedEffort decides the effort a request carries. A known list sends the
// requested level only when it includes it; unknown levels send it when the
// protocol fails open.
func appliedEffort(requested string, levels effortLevels, failOpen bool) string {
	switch {
	case requested == "":
		return ""
	case levels.known:
		if slices.Contains(levels.levels, requested) {
			return requested
		}
		return ""
	case failOpen:
		return requested
	}
	return ""
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
// session's effort. Each request is still checked against its own model. A
// child or parent that isn't effort-wrapped is returned unchanged.
func inheritEffort(parent, child port.LLMProvider) port.LLMProvider {
	from, ok := parent.(*effortProvider)
	to, wraps := child.(*effortProvider)
	if !ok || !wraps || from.requested == to.requested {
		return child
	}
	return to.withRequested(from.requested)
}

// effortLevelsFor resolves a provider's model's effort levels against the
// operator setting, the current live snapshot and the catalog.
func (r *providerRegistry) effortLevelsFor(pid, model string) effortLevelsResolution {
	return resolveModelEffortLevels(Config{effortLevels: r.effortLevels}, r.meta.current(), pid, model)
}

// appliedEffortFor returns the effort a request for pid's model is sent at when
// requested is asked for. The mock ignores effort, so offline runs never get one.
func (r *providerRegistry) appliedEffortFor(pid, model, requested string) string {
	if pid == providerMock {
		return ""
	}
	entry, ok := r.Lookup(pid)
	failOpen := !ok || entry.protocol != protocolOpenAIChatCompletions
	return appliedEffort(requested, r.effortLevelsFor(pid, model).effortLevels, failOpen)
}

// newEffortProvider wraps adapters for pid so each request sends requested only
// when its model accepts it.
func (r *providerRegistry) newEffortProvider(entry providerEntry, model, requested string, adapters *effortAdapters) *effortProvider {
	pid := entry.id
	return &effortProvider{
		adapters:   adapters,
		requested:  requested,
		providerID: pid,
		model:      model,
		levels:     func(model string) effortLevelsResolution { return r.effortLevelsFor(pid, model) },
		failOpen:   entry.protocol != protocolOpenAIChatCompletions,
		diag:       r.diag,
	}
}

// warnOperatorEffortNotAccepted narrates, once at startup, an operator default
// the default model is known not to accept: requests to that model carry no
// effort. Levels learned later from a live listing are narrated per request.
func warnOperatorEffortNotAccepted(ctx context.Context, reg *providerRegistry, requested string) {
	if requested == "" || reg.defaultID == "" || reg.diag == nil {
		return
	}
	resolution := reg.effortLevelsFor(reg.defaultID, reg.defaultModel)
	if resolution.known && !slices.Contains(resolution.levels, requested) {
		reg.diag.Log(ctx, port.LevelWarn,
			"reasoning-effort: the default model doesn't accept the operator default; its requests carry no effort",
			"provider", reg.defaultID, "model", reg.defaultModel, "requested", requested,
			"levels", strings.Join(resolution.levels, ","), "source", string(resolution.source))
	}
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
