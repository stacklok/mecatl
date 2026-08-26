package app

import (
	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
)

// modelSnapshot joins the provider registry's AVAILABLE providers to the embedded
// models.dev catalog and projects each model into the proto ModelInfo the server's
// ListModels RPC returns (multi-provider Phase 0, S3). It is the SYNCHRONOUS SEED:
// Build captures it (no network) so ModelSelection is honest from t=0, before the
// background live refresh swaps in the live catalog. It mirrors
// agentdefs.go:agentSnapshot — a pure, I/O-free, nil-safe projection.
//
// It shares the SAME per-model projection (embeddedModels → projectModelEntry) and
// sort (sortModelInfos) the live refresh's embedded fallback uses, so the seed and
// the refresh-floor CANNOT drift (the unified projection — they are byte-identical
// for a provider with no lister or a failed fetch). Neither the registry nor the
// catalog leaks into the server adapter (which holds only the projected slice).
//
// It carries PUBLIC metadata only — never a key, env-var name, or base URL
// (CWE-200). A provider with no resolved credentials is NOT in reg.Available(), so
// it is omitted entirely — its very availability is concealed. The synthetic mock
// provider advertises no selectable models, so it is skipped (a UseMock run returns
// nil here and ServerCapabilities.model_selection is false). An available-but-
// uncatalogued provider contributes no models (an honest catalog miss).
//
// Output is sorted by (provider_id, id). A nil registry or zero available providers
// yields nil.
func modelSnapshot(reg *providerRegistry) []*mecatlv1.ModelInfo {
	if reg == nil {
		return nil
	}
	var out []*mecatlv1.ModelInfo
	for _, pid := range reg.Available() { // available providers ONLY
		if pid == providerMock {
			continue // the mock never advertises selectable models
		}
		// The seed is the configured inventory floor for custom providers and the
		// embedded floor for built-ins (no live fetch at Build).
		for _, m := range providerInventoryFloor(reg, pid) {
			out = append(out, projectModelEntry(reg, pid, m))
		}
	}
	sortModelInfos(out)
	return out
}
