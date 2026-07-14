package app

import (
	"context"
	"strings"

	"github.com/stacklok/mecatl/engine/port"
)

// The NEUTRAL reasoning-effort vocabulary (ADR 0055). It is a COMPOSITION-level
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
// against the neutral vocabulary (ADR 0055). It trims + lowercases, treats "auto"
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

// clampEffortForProvider maps a NORMALISED neutral effort token to the value the
// given provider can accept, returning (clamped value, whether a clamp happened).
// OpenAI (and OpenRouter, which shares the openai adapter) is documented to
// support only low/medium/high in this contract, so xhigh/max clamp DOWN to high;
// Anthropic identity-maps all five tiers. An empty token (unset) passes through
// unchanged (no clamp). This is the COMPOSITION clamp — it runs here, not in the
// adapter, because composition holds the port.Diagnostics needed to NARRATE a
// clamp (the openai adapter has none). Per the locked user decision the clamp is
// conservative (xhigh/max→high for openai) even though the current openai-go SDK
// happens to expose an xhigh tier.
func clampEffortForProvider(providerID, effort string) (string, bool) {
	if effort == "" {
		return "", false
	}
	switch providerID {
	case providerOpenAI, providerOpenRouter, providerToolhive:
		// The ToolHive LLM gateway (issue #262) fronts MIXED upstreams over the
		// OpenAI protocol — the same conservative clamp applies since we cannot
		// know which upstream model backs a given selector.
		if effort == effortXHigh || effort == effortMax {
			return effortHigh, true
		}
		return effort, false
	default:
		// anthropic, mock, and any future provider: identity (the neutral
		// vocabulary == anthropic's native tiers; the mock ignores effort entirely).
		return effort, false
	}
}

// operatorDefaultEffortFor normalises + per-provider-clamps the OPERATOR-DEFAULT
// reasoning effort the registry bakes into a provider's shared adapter (ADR 0055),
// and NARRATES a clamp via cfg.diag() naming BOTH the requested and clamped-to
// values — so an operator who runs `--reasoning-effort max` against OpenAI sees the
// promised "(with a WARN)" downgrade at startup, not only silently in the
// per-session path. It is the startup twin of the per-session clamp narration in
// sessionEngineFactory. nil-safe on diag (cfg.diag() is never nil).
func operatorDefaultEffortFor(cfg Config, providerID string) string {
	norm, _ := NormalizeReasoningEffort(cfg.ReasoningEffort)
	clamped, didClamp := clampEffortForProvider(providerID, norm)
	if didClamp {
		cfg.diag().Log(context.Background(), port.LevelWarn,
			"reasoning-effort: clamped operator default for provider (this provider supports low/medium/high only)",
			"provider", providerID, "requested", norm, "clamped_to", clamped)
	}
	return clamped
}

// resolveSessionEffort resolves the EFFECTIVE reasoning effort for a session:
// per-session selector value (when non-empty) OUT-RANKS the operator default
// (cfg.ReasoningEffort). Both are normalised; an invalid per-session token falls
// back to the operator default with a WARN, and an invalid operator default
// normalises to unset. The returned value is the normalised neutral token BEFORE
// the per-provider clamp (clampEffortForProvider is applied at the construction
// site so the clamp diagnostic names the provider). nil-safe on diag (cfg.diag()
// is never nil).
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
