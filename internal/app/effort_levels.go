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
// bounded chance to, before a request goes out, when nothing else knows the
// model's effort levels. A gateway that lists models only on demand otherwise
// decides a session's first requests on "unknown". A failed or slow listing
// leaves the levels unknown; once the provider has answered, it never waits.
func awaitEffortLevels(ctx context.Context, reg *providerRegistry, pid, model string) {
	if reg == nil || reg.discovery == nil || reg.effortLevelsFor(pid, model).known {
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

// catalogEffortLevels reads a catalogued model's levels. A catalogued model is
// always known: no effort option means it takes no effort.
func catalogEffortLevels(catalogPID, model string) (effortLevels, bool) {
	p, ok := providercatalog.Default().Provider(catalogPID)
	if !ok {
		return effortLevels{}, false
	}
	m, ok := p.Model(model)
	if !ok {
		return effortLevels{}, false
	}
	return knownEffortLevels(m.EffortLevels()), true
}
