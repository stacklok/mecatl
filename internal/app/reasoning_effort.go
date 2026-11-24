package app

import (
	"context"
	"strings"

	"github.com/stacklok/mecatl/engine/port"
)

// The NEUTRAL reasoning-effort vocabulary. It is a COMPOSITION-level
// string vocabulary, deliberately NOT a port enum: the loop never branches on it,
// the engine/api surface stays clean, and the per-provider adapter maps the
// neutral token to its own SDK enum. "auto" / "" mean UNSET — do not send a
// reasoning-effort field at all (the provider's own default applies).
const (
	effortAuto   = "auto"
	effortLow    = "low"
	effortMedium = "medium"
	effortHigh   = "high"
	effortXHigh  = "xhigh"
	effortMax    = "max"
)

// validReasoningEfforts is the closed neutral set. "auto" is a valid INPUT
// (meaning unset) but normalises to "" so downstream code has ONE unset sentinel.
var validReasoningEfforts = map[string]struct{}{
	effortAuto:   {},
	effortLow:    {},
	effortMedium: {},
	effortHigh:   {},
	effortXHigh:  {},
	effortMax:    {},
}

// NormalizeReasoningEffort validates and canonicalises a reasoning-effort token
// against the neutral vocabulary. It trims + lowercases, treats "auto"
// and "" as UNSET (returns "", true), and returns ("", false) for any token
// outside the closed set — the caller treats false as "unset + WARN" (fail-soft:
// an unknown token must never produce a 400-causing wire param). It is the SINGLE
// validator for the neutral effort vocabulary, shared by composition + CLI folds.
func NormalizeReasoningEffort(token string) (string, bool) {
	t := strings.ToLower(strings.TrimSpace(token))
	if t == "" || t == effortAuto {
		return "", true // unset
	}
	if _, ok := validReasoningEfforts[t]; !ok {
		return "", false // unknown → caller treats as unset + WARN
	}
	return t, true
}

// sessionEffort is a session's reasoning effort: the level it asks for, which is
// what it keeps, and the level its own model is sent, which is what it echoes.
// Other models the session's provider serves (subagents, slots, plan mode) are
// checked per request.
type sessionEffort struct {
	requested string
	applied   string
}

// sessionProviderOptions wraps a session's provider so each request sends the
// session's requested effort only when its model accepts it, and returns the
// capability intersection for the session's model. Ordinary and agent-bound
// roots share it. A provider that can't re-mint (the mock, test seams) is
// returned unchanged: it can't apply an effort, and its echo follows the same
// per-model rule.
func sessionProviderOptions(ctx context.Context, cfg Config, reg *providerRegistry, provider port.LLMProvider, providerID, model, requestedEffort string) (port.LLMProvider, sessionEffort, port.ProviderCapabilities) {
	requested := resolveSessionEffort(ctx, cfg, requestedEffort)
	if requested != "" {
		// Learn the model's levels before echoing them, so the echo and the
		// first request agree on a gateway that lists models on demand.
		awaitEffortLevels(ctx, reg, providerID, model)
	}
	caps := modelCapability(reg, providerID, model)
	entry, ok := reg.Lookup(providerID)
	if !ok || entry.remint == nil {
		return provider, sessionEffort{requested: requested, applied: reg.appliedEffortFor(providerID, model, requested)}, caps
	}
	wrapped := reg.newEffortProvider(entry, model, requested, capsAdapters(entry, caps))
	return wrapped, sessionEffort{requested: requested, applied: wrapped.appliedFor(ctx, model)}, caps
}

// resolveSessionEffort resolves the REQUESTED reasoning effort for a session:
// per-session selector value (when non-empty) OUT-RANKS the operator default
// (cfg.ReasoningEffort). Both are normalised; an invalid per-session token falls
// back to the operator default with a WARN, and an invalid operator default
// normalises to unset. Whether a request carries it is decided per request by
// effortProvider against that request's model. nil-safe on diag (cfg.diag() is
// never nil).
func resolveSessionEffort(ctx context.Context, cfg Config, sessionEffort string) string {
	diag := cfg.diag()
	if strings.TrimSpace(sessionEffort) != "" {
		norm, ok := NormalizeReasoningEffort(sessionEffort)
		if !ok {
			diag.Log(ctx, port.LevelWarn,
				"reasoning-effort: ignoring unknown per-session value; using the operator default",
				"value", strings.TrimSpace(sessionEffort))
			// fall through to the operator default
		} else {
			return norm
		}
	}
	norm, ok := NormalizeReasoningEffort(cfg.ReasoningEffort)
	if !ok {
		diag.Log(ctx, port.LevelWarn,
			"reasoning-effort: ignoring unknown operator default; using provider default (unset)",
			"value", strings.TrimSpace(cfg.ReasoningEffort))
		return ""
	}
	return norm
}
