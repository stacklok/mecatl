package app

import (
	"context"
	"slices"
	"strings"

	"github.com/stacklok/mecatl/internal/adapter/providercatalog"
)

// effortLevels is what mecatl knows about the effort levels one model accepts.
// The zero value is unknown. A known value with no levels means the model takes
// no effort.
type effortLevels struct {
	known  bool
	levels []string // a subset of effortVocabulary, in vocabulary order
}

// effortVocabulary is the order effort levels are kept and offered in. Auto is
// not a level: it sends no effort.
var effortVocabulary = []string{effortLow, effortMedium, effortHigh, effortXHigh, effortMax}

// knownEffortLevels builds known levels from upstream tokens. Tokens outside the
// vocabulary (none, minimal, anything else) are dropped and the rest are put in
// vocabulary order, so a source listing none of the five declares no effort.
func knownEffortLevels(tokens []string) effortLevels {
	normalized := make([]string, 0, len(tokens))
	for _, token := range tokens {
		normalized = append(normalized, strings.ToLower(strings.TrimSpace(token)))
	}
	levels := make([]string, 0, len(effortVocabulary))
	for _, level := range effortVocabulary {
		if slices.Contains(normalized, level) {
			levels = append(levels, level)
		}
	}
	return effortLevels{known: true, levels: levels}
}

// effortLevelsSource names where a model's effort levels came from.
type effortLevelsSource string

const (
	effortLevelsFromConfig  effortLevelsSource = "config"
	effortLevelsFromLive    effortLevelsSource = "live"
	effortLevelsFromCatalog effortLevelsSource = "catalog"
	effortLevelsUnknown     effortLevelsSource = "unknown"
	// effortLevelsNotSent: the provider never sends effort (the mock).
	effortLevelsNotSent effortLevelsSource = "provider"
	// effortLevelsNotConfigured: a Chat Completions model whose levels nothing
	// knows; models.effort_levels opts it in.
	effortLevelsNotConfigured effortLevelsSource = "chat-completions"
	// effortLevelsCapped: a model whose levels nothing knows, on a provider that
	// can front models without xhigh or max; it is sent at most high.
	effortLevelsCapped effortLevelsSource = "capped"
)

type effortLevelsResolution struct {
	effortLevels
	source effortLevelsSource
}

// resolveModelEffortLevels answers which effort levels a provider's model
// accepts, from the first source that knows: the operator's
// models.effort_levels, the live listing, then the embedded catalog. A live
// listing that knows the model takes no effort is final; a listing that doesn't
// know falls through to the catalog. It mirrors resolveModelWindow's order.
func resolveModelEffortLevels(cfg Config, view *discoverySnapshot, pid, model string) effortLevelsResolution {
	if configured, ok := cfg.effortLevels[pid][model]; ok {
		return effortLevelsResolution{knownEffortLevels(configured), effortLevelsFromConfig}
	}
	if m, ok := view.lookup(pid, model); ok && m.Effort.known {
		return effortLevelsResolution{m.Effort, effortLevelsFromLive}
	}
	if levels, ok := catalogEffortLevels(metadataCatalogProviderID(pid), model); ok {
		return effortLevelsResolution{levels, effortLevelsFromCatalog}
	}
	return effortLevelsResolution{source: effortLevelsUnknown}
}

// awaitEffortLevels gives a provider whose live listing hasn't answered yet one
// bounded chance to, before a request goes out, when neither the operator
// setting nor the listing knows the model's effort levels. The catalog doesn't
// settle it: a gateway's listing can differ from the catalog, and one that lists
// models only on demand would otherwise decide a session's first requests on the
// catalog or on "unknown". A failed or slow listing leaves the other sources to
// decide; once the provider has answered, it never waits.
func awaitEffortLevels(ctx context.Context, reg *providerRegistry, pid, model string) {
	if reg == nil || reg.discovery == nil {
		return
	}
	if source := reg.modelEffortLevels(pid, model).source; source == effortLevelsFromConfig || source == effortLevelsFromLive {
		return
	}
	entry, ok := reg.Lookup(pid)
	view := reg.discovery.snapshot()
	if !ok || entry.lister == nil || view == nil {
		return
	}
	if state := view.providers[pid]; !state.eligible || !state.completedAt.IsZero() {
		return
	}
	bounded, cancel := context.WithTimeout(ctx, liveModelRefreshTimeout)
	defer cancel()
	_, _ = reg.discovery.request(bounded, pid, discoveryAdmission)
}

// awaitRunEffortLevels is the effort half of the wait before a run. A session
// with its own engine learned its model's levels when the engine was built;
// this wait is for the shared engine, which asks for the operator default. At
// Auto no request carries effort, so there is nothing to wait for.
func awaitRunEffortLevels(ctx context.Context, reg *providerRegistry, pid, model string) {
	if reg == nil {
		return
	}
	if entry, ok := reg.Lookup(pid); !ok || entry.defaultEffort == "" {
		return
	}
	awaitEffortLevels(ctx, reg, pid, model)
}

// catalogEffortLevels reads a catalogued model's levels. A catalogued model is
// known: no effort option means it takes no effort. OpenRouter is the
// exception: it translates an effort level into the control a reasoning model
// does take (a thinking budget, an on/off switch), so a reasoning model it
// catalogues without an effort option hasn't said which levels it takes.
func catalogEffortLevels(catalogPID, model string) (effortLevels, bool) {
	p, ok := providercatalog.Default().Provider(catalogPID)
	if !ok {
		return effortLevels{}, false
	}
	m, ok := p.Model(model)
	if !ok {
		return effortLevels{}, false
	}
	levels := m.EffortLevels()
	if catalogPID == providerOpenRouter && m.SupportsReasoning() && len(levels) == 0 {
		return effortLevels{}, false
	}
	return knownEffortLevels(levels), true
}
