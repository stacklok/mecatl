package app

import "testing"

// TestResolveContextWindowClosureLiveFirst exercises the exact mechanism issue #66
// fixes: the ResolveContextWindow closure Build injects into server.Config closes
// over reg.meta (a liveMetaStore). A DEFAULT-session model that the LIVE listing
// carries but the curated catalog does NOT (e.g. a brand-new model not yet
// re-pinned into the embed) has a catalog floor of 0, so before the background
// live swap lands the closure returns 0 (the t=0 seed) and the resolved_model
// echo degrades to no footer bar; AFTER the swap the closure returns the live
// window, so a subsequent GetSession self-heals.
//
// The closure under test is byte-for-byte the one in build.go's svcCfg literal:
//
//	func(p, m string) int64 { return int64(reg.meta.contextWindowFor(p, m)) }
func TestResolveContextWindowClosureLiveFirst(t *testing.T) {
	const liveOnlyModel = "openai/gpt-99-future"
	const liveCtx = 1_050_000 // below maxLiveContextLimit (2_000_000), so unclamped

	s := newMetadataFixture()
	// Seed from the curated catalog for OpenRouter. The live-only model is NOT in the
	// curated catalog, so its catalog floor is 0.
	s.setEligibilityFixture([]string{providerOpenRouter})

	// The injected closure, identical in shape to the one in build.go.
	resolve := func(p, m string) int64 { return int64(s.contextWindowFor(p, m)) }

	// Pre-swap (seed only): a live-only model has a catalog floor of 0.
	if got := resolve(providerOpenRouter, liveOnlyModel); got != 0 {
		t.Fatalf("pre-swap resolve(%q,%q) = %d, want 0 (curated catalog has no row → footer would have no bar)",
			providerOpenRouter, liveOnlyModel, got)
	}

	// The background live refresh lands: the live listing carries the model + its window.
	s.setMetadataFixture(map[string][]modelEntry{
		providerOpenRouter: {{ID: liveOnlyModel, ContextLimit: liveCtx}},
	})

	// Post-swap: the closure returns the live window — the echo is now honest.
	if got := resolve(providerOpenRouter, liveOnlyModel); got != int64(liveCtx) {
		t.Fatalf("post-swap resolve(%q,%q) = %d, want live %d", providerOpenRouter, liveOnlyModel, got, liveCtx)
	}
}

// TestResolveContextWindowClosureNeverLowersCatalogued: a CATALOGUED default model
// (OpenRouter openai/gpt-5, present in the curated catalog) resolves to its catalog
// window pre-swap and is NOT lowered when no live entry exists — the fix only ever
// RAISES a missing window, never drops a known one (contextWindowFor floors to the
// catalog).
func TestResolveContextWindowClosureNeverLowersCatalogued(t *testing.T) {
	const cataloguedModel = "openai/gpt-5"

	s := newMetadataFixture()
	s.setEligibilityFixture([]string{providerOpenRouter})

	resolve := func(p, m string) int64 { return int64(s.contextWindowFor(p, m)) }

	floor := resolve(providerOpenRouter, cataloguedModel)
	if floor <= 0 {
		t.Fatalf("expected a non-zero catalog window for catalogued %q, got %d", cataloguedModel, floor)
	}

	// A live swap that DROPS the catalogued model (only an unrelated id present) must
	// not erase the catalog floor.
	s.setMetadataFixture(map[string][]modelEntry{
		providerOpenRouter: {{ID: "some-other-live-model", ContextLimit: 12345}},
	})
	if got := resolve(providerOpenRouter, cataloguedModel); got != floor {
		t.Fatalf("post-swap (model dropped) resolve = %d, want catalog floor %d (fix must never lower)", got, floor)
	}
}
