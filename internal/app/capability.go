package app

import (
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/internal/adapter/providercatalog"
)

// modelCapability is the SINGLE SOURCE of a (provider, model)'s true input
// capability: the INTERSECTION of the catalog's per-model modalities and the
// wired adapter's port.ProviderCapabilities. The adapter is the AUTHORITY on what
// it can actually TRANSMIT; the catalog is the authority on what the model
// ACCEPTS. The AND of the two is the honest truth, and the only honest value to
// advertise. It lives HERE, in composition (internal/app) — the only layer that
// holds BOTH inputs (the catalog via providercatalog.Default() and the per-provider
// adapter via reg.Lookup(id).provider.Capabilities()).
//
// The result is a NEUTRAL port.ProviderCapabilities: neither the catalog nor the
// registry type crosses into the server/acp adapters — they receive this computed
// value only. This ONE value feeds three sinks so they CANNOT disagree:
//
//	(a) ModelInfo.Image in the provider discovery projection (ListModels),
//	(b) the CreateSessionResponse.session_capabilities echo (per-session), and
//	(c) the ACP gate via Service.ProviderCapabilities() (default caps).
//
// P1's per-model divergence (e.g. native Anthropic image vs no-image, per model)
// is a PURE DATA change here: a new registry entry whose adapter Capabilities()
// reports its real transmit ability, plus catalog rows whose inputModalities differ
// per model. The intersection formula is unchanged; no server/acp/proto edit. That
// is the whole point of locating the AND in composition.
//
// Modality precedence (LIVE-FIRST → catalog floor → adapter-only passthrough; all
// fail-safe toward text-only):
//   - adapterCaps = reg.Lookup(providerID).provider.Capabilities(). An
//     unknown/unavailable provider (or a nil registry) yields the zero value
//     (text-only) — a provider we cannot reach transmits nothing. The adapter is ALWAYS
//     the transmit-authority ceiling: every modality below is AND'd with adapterCaps.
//   - (1) LIVE modalities (reg.meta.modalitiesFor): when a live source declares
//     input modalities, that non-nil list is AUTHORITATIVE for the exact model —
//     including an explicitly empty list (text-only). Omitted metadata remains nil
//     and falls through to the exact catalog row, then adapter capabilities.
//   - (2) CATALOG floor: no live entry but the embedded catalog knows the model ⇒
//     Image = adapterCaps.Image AND catalog-image; Audio = adapterCaps.Audio AND
//     catalog-audio. The catalog carries NO audio field today, so catalog-audio is
//     false and the AND is false regardless — Audio stays effectively adapter-driven.
//   - (3) PASSTHROUGH: UNCATALOGUED + no live entry falls back to ADAPTER-ONLY caps
//     (trust the adapter, both catalog and live are silent — do NOT zero it, or every
//     passthrough model on openai-direct/anthropic would lose image). Only providers
//     WITH a live lister get honest per-model gating; the global default is unchanged.
//   - EmbeddedContext is adapter-driven throughout (neither catalog nor live opines).
//
// Reasoning is intentionally NOT intersected here: it is a ModelInfo field, not a
// port.ProviderCapabilities bit, and there is no adapter "can replay reasoning"
// authority bit in P0. ModelInfo.reasoning stays catalog-sourced. If P1 wants
// reasoning intersected, add the adapter bit then (anti-speculative).
func modelCapability(reg *providerRegistry, providerID, modelID string) port.ProviderCapabilities {
	var view *discoverySnapshot
	if reg != nil {
		view = reg.meta.current()
	}
	return modelCapabilityCandidate(reg, view, providerID, modelID)
}

// modelReasoningSupport reports whether the (provider, model) is known to support
// reasoning-effort, and whether that fact is KNOWN at all (ADR 0055 capability
// gate). It mirrors modelCapability's precedence: (1) LIVE-FIRST — a live meta
// entry's Reasoning bit is authoritative when present; (2) CATALOG floor — the
// embedded catalog's SupportsReasoning; (3) UNKNOWN — neither source describes the
// model (a passthrough/uncatalogued model), so known=false and the caller
// FAILS-OPEN (sends effort anyway; the provider 400s honestly if it really cannot
// — the same unknown=capable posture the thinking path takes). The mock provider
// is treated as known-incapable so an offline test never sends effort to it.
func modelReasoningSupport(reg *providerRegistry, providerID, modelID string) (supported, known bool) {
	if providerID == providerMock {
		return false, true
	}
	// (1) Live-first.
	if reg != nil && reg.meta != nil {
		if entry, ok := reg.meta.lookup(providerID, modelID); ok {
			return entry.Reasoning, true
		}
	}
	// Preserve the configured default's existing inventory-floor presence without
	// storing that floor as a live observation.
	if reg != nil && modelID != "" {
		if entry, ok := reg.Lookup(providerID); ok && entry.defaultModel == modelID {
			return false, true
		}
	}
	// (2) Catalog floor.
	if providerID != "" && modelID != "" {
		if p, ok := providercatalog.Default().Provider(metadataCatalogProviderID(providerID)); ok {
			for _, m := range p.Models() {
				if m.ID() == modelID {
					return m.SupportsReasoning(), true
				}
			}
		}
	}
	// (3) Unknown → caller fails open.
	return false, false
}

// modelAdapterCaps returns the wired adapter's transmit capabilities for a
// provider (the AUTHORITY on what it can actually send), or the zero value
// (text-only) for an unknown/unavailable provider or a nil registry — a provider
// we cannot reach transmits nothing. It is the shared adapter-side input of BOTH
// the catalog-keyed modelCapability (session echo / ACP gate) and the live-or-
// embedded liveModelSnapshot (the picker), so the two intersect against the SAME
// adapter authority.
func modelAdapterCaps(reg *providerRegistry, providerID string) port.ProviderCapabilities {
	var caps port.ProviderCapabilities
	if reg != nil {
		if entry, ok := reg.Lookup(providerID); ok && entry.provider != nil {
			caps = entry.provider.Capabilities()
		}
	}
	return caps
}

// hasImageModality reports whether "image" is among a model's input modalities. It
// is the ONE predicate both the embedded path (via providercatalog's
// SupportsImageInput, which tests the same list) and the LIVE path
// (liveModelSnapshot) use to derive image-ness, so a live model and an embedded
// model can never disagree on how image is computed. The image capability the
// picker advertises is always adapterCaps.Image AND hasImageModality(modalities).
func hasImageModality(modalities []string) bool {
	for _, mod := range modalities {
		if mod == "image" {
			return true
		}
	}
	return false
}

// hasAudioModality reports whether "audio" is among a model's input modalities. It is
// the audio twin of hasImageModality so the live modality path computes audio-ness the
// SAME way the catalog path does (catalogModalities also scans the raw list for "audio").
// AND'd with adapterCaps.Audio in modelCapability; the P0 adapters are Audio:false, so
// this is forward-compatible until an audio-capable adapter ships.
func hasAudioModality(modalities []string) bool {
	for _, mod := range modalities {
		if mod == "audio" {
			return true
		}
	}
	return false
}

// catalogModalities reports the catalog's per-model input modalities (image,
// audio) for (providerID, modelID) and whether the (provider, model) pair was
// found in the catalog at all. A miss (unknown provider, uncatalogued model, or an
// empty modelID) returns (false, false, false) so the caller falls back to
// adapter-only caps — a passthrough model is NOT a capability denial. The catalog
// carries no explicit audio modality today, so catAudio derives from the raw
// input-modality list ("audio") for forward-compatibility (false in the P0 data).
func catalogModalities(providerID, modelID string) (image, audio, found bool) {
	if providerID == "" || modelID == "" {
		return false, false, false
	}
	p, ok := providercatalog.Default().Provider(metadataCatalogProviderID(providerID))
	if !ok {
		return false, false, false
	}
	m, ok := p.Model(modelID)
	if !ok {
		return false, false, false
	}
	// Image derives from the SHARED hasImageModality predicate over the catalog's
	// raw modality list — the SAME function liveModelSnapshot (the picker) uses —
	// so the session echo and ListModels provably agree on a model's image-ness
	// (the anti-divergence guarantee rests on ONE function, not two copies of the
	// "is image among inputModalities" test, for BOTH the live and embedded paths).
	// Audio derives from the same raw list for forward-compatibility; it is false
	// in the P0 data, so the AND in modelCapability is false regardless.
	mods := m.InputModalities()
	image = hasImageModality(mods)
	for _, mod := range mods {
		if mod == "audio" {
			audio = true
		}
	}
	return image, audio, true
}
