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
// Auto. The levels come with the provider's rules applied (effortLevelsFor), so
// on Chat Completions an unknown model gets Auto too.
type effortProvider struct {
	adapters   *effortAdapters
	requested  string // a level from effortVocabulary, or "" for Auto
	providerID string
	model      string // the model the provider was resolved for; used when a request names none
	levels     func(model string) effortLevelsResolution
	diag       port.Diagnostics
	// warned holds the drops already narrated (dropKey → struct{}). A session's
	// wrapper and the children that inherit its level share one set, so a drop is
	// narrated once per session and model, whichever engine sends the request.
	warned *sync.Map
}

type dropKey struct{ providerID, model, level string }

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
	applied := appliedEffort(p.requested, resolution.effortLevels)
	if applied == "" {
		p.narrateDrop(ctx, model, resolution)
	}
	return applied
}

// narrateDrop warns, once per session and model, that the requested level isn't
// sent. A provider that never sends effort (the mock) is not narrated.
func (p *effortProvider) narrateDrop(ctx context.Context, model string, resolution effortLevelsResolution) {
	if p.diag == nil || resolution.source == effortLevelsNotSent {
		return
	}
	if _, seen := p.warned.LoadOrStore(dropKey{p.providerID, model, p.requested}, struct{}{}); seen {
		return
	}
	msg := "reasoning-effort: the model doesn't accept the requested level; sending no effort"
	if resolution.source == effortLevelsNotConfigured {
		msg = "reasoning-effort: a Chat Completions model gets effort only when models.effort_levels lists it; sending no effort"
	}
	p.diag.Log(ctx, port.LevelWarn, msg,
		"provider", p.providerID, "model", model, "requested", p.requested,
		"levels", strings.Join(resolution.levels, ","), "source", string(resolution.source))
}

// requesting returns a wrapper over the same adapters that requests level and
// narrates its drops into warned.
func (p *effortProvider) requesting(level string, warned *sync.Map) *effortProvider {
	return &effortProvider{adapters: p.adapters, requested: level, providerID: p.providerID, model: p.model,
		levels: p.levels, diag: p.diag, warned: warned}
}

// appliedEffort decides the effort a request carries: the requested level when
// the model lists it or its levels are unknown, otherwise none.
func appliedEffort(requested string, levels effortLevels) string {
	if requested == "" || (levels.known && !slices.Contains(levels.levels, requested)) {
		return ""
	}
	return requested
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
// drops are narrated with the session's. A child or parent that isn't
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
// models.effort_levels lists it. Any other unknown model fails open: it is sent
// the requested level.
func providerEffortLevels(pid string, entry providerEntry, found bool, own effortLevelsResolution) effortLevelsResolution {
	switch {
	case pid == providerMock:
		return effortLevelsResolution{knownEffortLevels(nil), effortLevelsNotSent}
	case !own.known && found && entry.protocol == protocolOpenAIChatCompletions:
		return effortLevelsResolution{knownEffortLevels(nil), effortLevelsNotConfigured}
	}
	return own
}

// appliedEffortFor returns the effort a request for pid's model is sent at when
// requested is asked for.
func (r *providerRegistry) appliedEffortFor(pid, model, requested string) string {
	return appliedEffort(requested, r.effortLevelsFor(pid, model).effortLevels)
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
		diag:       r.diag,
		warned:     &sync.Map{},
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
	if resolution.source == effortLevelsNotSent || appliedEffort(requested, resolution.effortLevels) != "" {
		return
	}
	msg := "reasoning-effort: the default model doesn't accept the operator default; its requests carry no effort"
	if resolution.source == effortLevelsNotConfigured {
		msg = "reasoning-effort: the default model is on Chat Completions and models.effort_levels doesn't list it; its requests carry no effort"
	}
	reg.diag.Log(ctx, port.LevelWarn, msg,
		"provider", reg.defaultID, "model", reg.defaultModel, "requested", requested,
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
