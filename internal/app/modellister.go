package app

import (
	"context"
	"os"
	"slices"
	"sort"
	"sync"
	"time"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/internal/adapter/openaicodex"
	"github.com/stacklok/mecatl/internal/adapter/openaicompat"
	"github.com/stacklok/mecatl/internal/adapter/openrouter"
	"github.com/stacklok/mecatl/internal/adapter/providercatalog"
	"github.com/stacklok/mecatl/internal/adapter/server"
	"github.com/stacklok/mecatl/internal/syscaller"
	"github.com/stacklok/mecatl/provider/anthropic"
)

// liveModelRefreshTimeout bounds the whole background refresh (all providers'
// fetches combined). A slow/hung provider endpoint must not keep the refresh
// goroutine — or, in the synchronous test path, Build — alive indefinitely.
const liveModelRefreshTimeout = 10 * time.Second

// modelSwapper is the narrow seam the live-model refresh writes through: the
// service's SetModels. Keeping it an interface (satisfied by *server.Service)
// keeps the refresh testable without a full service.
type modelSwapper interface {
	SetModels([]*mecatlv1.ModelInfo)
}

// publishSnapshot is the SHARED post-fetch publish tail every live-model
// refresh path ends with (startLiveModelRefresh's sync AND async branches,
// and the on-demand refreshStaleModels) — extracted so the sequence (merge
// fresh into the resolver-feeding meta store, project + swap the picker proto
// slice, heal a still-empty intent-driven default model, re-project
// provider_status when the swapper supports it) can never drift between the
// three call sites. fresh is ONLY the providers this call actually re-fetched
// — the FULL available set for the background refresh, or a PARTIAL
// stale-only set for refreshStaleModels — never a whole-map snapshot of
// EVERYTHING, so a partial refresh can only ever touch the providers it
// fetched (mergeSwap, issue #262 review finding 2: the lost-update race a
// whole-map replace caused when this call interleaved with the other
// refresh path). The whole sequence runs under reg.publishMu so the two
// refresh paths cannot interleave their merge-then-swap.
func publishSnapshot(d port.Diagnostics, reg *providerRegistry, swap modelSwapper, fresh map[string][]modelEntry) {
	reg.publishMu.Lock()
	defer reg.publishMu.Unlock()
	merged := reg.meta.mergeSwap(fresh)
	swap.SetModels(projectAll(reg, merged))
	// Issue #262 §1 deviation: heals a still-empty intent-driven default model
	// (toolhive sole+probe-down at Build) the moment a live snapshot lands —
	// a no-op when defaultModel is already set or the default isn't intent-driven.
	// FRESH lists only — "first-listed" must come from a REAL lister result for
	// THIS call, never a carried-over provider from a previous refresh.
	reg.healDefaultModel(d, fresh)
	if setter, ok := swap.(providerStatusSetter); ok {
		setter.SetProviderStatus(providerStatusProto(reg))
	}
}

// projectAll projects EVERY available provider's []modelEntry (excluding the
// mock, which never advertises selectable models) from byProvider into the
// sorted proto slice the picker consumes. Extracted from liveModelSnapshot so
// publishSnapshot can re-run the SAME projection over mergeSwap's merged view
// without duplicating the loop at each of the three call sites.
func projectAll(reg *providerRegistry, byProvider map[string][]modelEntry) []*mecatlv1.ModelInfo {
	var out []*mecatlv1.ModelInfo
	for _, pid := range reg.Available() {
		if pid == providerMock {
			continue
		}
		for _, m := range byProvider[pid] {
			out = append(out, projectModelEntry(reg, pid, m))
		}
	}
	sortModelInfos(out)
	return out
}

// startLiveModelRefresh kicks the ONE-SHOT background live-catalog refresh and
// returns a close func that cancels it (wired into Build's closeAll). When NO
// available provider has a lister it is a NO-OP (returns a no-op closer; no
// goroutine, no ctx) — so a mock/openai-only deployment spawns nothing. When sync
// is true (a test seam) the refresh runs INLINE before returning, so an offline
// e2e can assert the swapped snapshot deterministically without sleeps.
//
// delay (a DIAGNOSTIC/TEST seam, default 0) artificially holds the ASYNC goroutine
// BEFORE the fetch/swap, so the live-catalog swap lands `delay` after startup. It
// forces the create-races-the-swap window (issue #66 footer heal) open WIDE so the
// race is deterministically reproducible; it is INERT in the sync path (which runs
// inline, no window to widen) and a no-op at 0. The delay sleep is cancellable, so a
// shutdown during the delay still joins promptly without swapping a stale snapshot.
func startLiveModelRefresh(d port.Diagnostics, reg *providerRegistry, swap modelSwapper, runSync bool, delay time.Duration) func() {
	if reg == nil || !anyProviderHasLister(reg) {
		// No lister anywhere: there is nothing to refresh, but the refresh is SETTLED
		// (it will never run) — mark completed BEFORE the early return so an
		// openai/anthropic/mock-only deployment does not leave the echo resolver stuck
		// on provisional-0 forever for an uncatalogued model. (nil-reg ⇒ no store to
		// mark; markRefreshCompleted is nil-safe on a nil store anyway.)
		if reg != nil {
			reg.meta.markRefreshCompleted()
		}
		return func() {} // nothing to refresh
	}
	rootCtx := syscaller.Context(context.Background(), syscaller.RootModelCatalogRefresh)
	if runSync {
		ctx, cancel := context.WithTimeout(rootCtx, liveModelRefreshTimeout)
		defer cancel()
		byProvider := liveModelSnapshot(ctx, d, reg)
		publishSnapshot(d, reg, swap, byProvider)
		reg.meta.markRefreshCompleted() // sync path SETTLES after the swap.
		return func() {}
	}
	ctx, cancel := context.WithCancel(rootCtx)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		// DIAGNOSTIC/TEST delay: hold off the fetch/swap so the pre-swap window is
		// observable. Cancellable so a shutdown during the delay joins without swapping.
		if delay > 0 {
			t := time.NewTimer(delay)
			defer t.Stop()
			select {
			case <-t.C:
			case <-ctx.Done():
				return
			}
		}
		fetchCtx, fetchCancel := context.WithTimeout(ctx, liveModelRefreshTimeout)
		defer fetchCancel()
		byProvider := liveModelSnapshot(fetchCtx, d, reg)
		// If the refresh ctx was cancelled (the closer ran — a shutdown — before the
		// fetch finished), the fetch was interrupted and its result is untrustworthy
		// (the embedded floor at best), so DO NOT overwrite the seed. Only swap when the
		// fetch ran to completion uninterrupted. On a normal run the goroutine reaches
		// here, ctx.Err() is nil, and the swap lands; a later closer just wg.Wait()s.
		if ctx.Err() != nil {
			return
		}
		// Both sinks are fed from the ONE modelEntry list per provider, so the picker
		// proto slice and the resolver-feeding meta store cannot drift.
		publishSnapshot(d, reg, swap, byProvider)
		// SETTLED: the live answer is in (success OR a fetch-fail/empty that fell back
		// to the embedded floor inside resolveProviderModels — either way the Swap above
		// is the authoritative result). Flip the flag so the echo resolver stops
		// returning provisional-0 for an uncatalogued model and floors it instead. NOT
		// reached on the shutdown-cancel returns above (a shutdown is not a settle).
		reg.meta.markRefreshCompleted()
	}()
	return func() {
		cancel()
		wg.Wait()
	}
}

// liveRefreshDelay resolves the ASYNC live-model refresh delay (issue #66 footer-heal
// repro seam). A directly-set Config field (tests) wins; otherwise the undocumented
// MECATL_LIVE_MODEL_REFRESH_DELAY env var is parsed. DIAGNOSTIC/TEST ONLY: default 0
// (unset / unparseable / non-positive ⇒ 0 ⇒ today's behaviour, swap lands sub-second).
// Read in composition so it pollutes no operator flag surface and stays an internal
// detail; never recommended for normal use.
func liveRefreshDelay(cfg Config) time.Duration {
	if cfg.liveModelRefreshDelay > 0 {
		return cfg.liveModelRefreshDelay
	}
	if d, err := time.ParseDuration(os.Getenv("MECATL_LIVE_MODEL_REFRESH_DELAY")); err == nil && d > 0 {
		return d
	}
	return 0
}

// anyProviderHasLister reports whether at least one AVAILABLE provider carries a
// live lister — the gate that decides whether the background refresh runs at all.
func anyProviderHasLister(reg *providerRegistry) bool {
	for _, pid := range reg.Available() {
		if entry, ok := reg.Lookup(pid); ok && entry.lister != nil && !entry.nativeEndpoint {
			return true
		}
	}
	return false
}

// (compile-time) *server.Service satisfies modelSwapper.
var _ modelSwapper = (*server.Service)(nil)

// modelEntry is the NEUTRAL, composition-local, SOURCE-AGNOSTIC description of one
// selectable model — produced EITHER by a LIVE fetch (modelLister) OR by projecting
// the embedded providercatalog (embeddedModels). Both sources fold into this one
// type so the seed and the refresh-floor share ONE projection (projectModelEntry)
// and cannot drift. It carries exactly what the proto projection + the capability
// intersection consume: nothing provider-private, no key, no URL. It NEVER leaves
// internal/app (the server adapter receives only []*mecatlv1.ModelInfo).
//
// InputModalities is the authoritative modality list for THIS model — the single
// source the image capability is derived from (via hasImageModality), so a live
// model and an embedded model are tested by the SAME predicate.
type modelEntry struct {
	ID              string
	DisplayName     string
	ContextLimit    int
	OutputLimit     int // max_tokens output ceiling (0 = unknown ⇒ catalog/default floor)
	InputModalities []string
	Reasoning       bool
	ToolCall        bool
	Thinking        thinkingDescriptor // Anthropic-only; zero value = unknown ⇒ adapter prefix floor
}

// thinkingDescriptor is a NEUTRAL, source-agnostic projection of a model's
// extended-thinking capability — the live replacement for the adapter's hardcoded
// adaptive/enabled/none prefix lists. The zero value (Known=false) means "unknown",
// so the anthropic adapter falls back to its embedded prefix matrix (the offline
// floor). Only the live Anthropic lister populates it (Capabilities.Thinking.Types);
// every other source leaves it zero, which costs nothing and changes no behaviour.
type thinkingDescriptor struct {
	Known    bool // true only when a live source populated it
	Adaptive bool // Capabilities.Thinking.Types.adaptive.supported
	Enabled  bool // Capabilities.Thinking.Types.enabled.supported (manual)
}

// modelLister is the OPTIONAL live-catalog capability a provider may expose. It is
// a COMPOSITION-LOCAL interface (NOT a port) for the same reason providerRegistry
// is composition-only: it has a SINGLE consumer (liveModelSnapshot), and the
// agent/domain/server never enumerate a catalog. A provider opts in by having its
// adapter satisfy this interface and by composition setting providerEntry.lister
// at registry-build time — that one assignment is the ENTIRE opt-in; the
// merge/snapshot/registry plumbing is unchanged.
//
// ListModels is read-only, takes a ctx for timeout/cancel, and is FAIL-SAFE to the
// caller: an error means the caller falls back to the embedded catalog for that
// provider (never empty, never a crash).
type modelLister interface {
	ListModels(ctx context.Context) ([]modelEntry, error)
}

// openRouterLister adapts the *openrouter.Lister (which returns its OWN package
// type, []openrouter.Model — no import cycle) to the composition modelLister
// interface by mapping each openrouter.Model → modelEntry. This is the one place the
// adapter's type is mapped to the composition-neutral type; the adapter never
// imports internal/app.
type openRouterLister struct {
	inner *openrouter.Lister
}

// openAICodexLister adapts the account-entitlement response into the one
// composition-local modelEntry stream. The live list is inventory-authoritative:
// this wrapper can enrich ONLY ids already returned by Codex. A matching OpenAI
// catalog row supplies fields the entitlement endpoint omitted; an unknown id
// remains selectable with the adapter-static modality ceiling and conservative
// context-window floor.
type openAICodexLister struct {
	inner *openaicodex.Lister
}

func (l openAICodexLister) ListModels(ctx context.Context) ([]modelEntry, error) {
	raw, err := l.inner.ListModels(ctx)
	if err != nil {
		return nil, err
	}
	metadata := make(map[string]modelEntry)
	for _, m := range embeddedModels(providerOpenAI) {
		metadata[m.ID] = m
	}
	out := make([]modelEntry, 0, len(raw))
	for _, m := range raw {
		entry := modelEntry{
			ID:              m.ID,
			DisplayName:     m.DisplayName,
			ContextLimit:    m.ContextLimit,
			InputModalities: append([]string(nil), m.InputModalities...),
			Reasoning:       m.Reasoning,
			ToolCall:        m.ToolCall,
		}
		catalog, catalogued := metadata[m.ID]
		if entry.DisplayName == "" && catalogued {
			entry.DisplayName = catalog.DisplayName
		}
		if entry.ContextLimit <= 0 && catalogued {
			entry.ContextLimit = catalog.ContextLimit
		}
		if !m.InputModalitiesKnown {
			if catalogued && len(catalog.InputModalities) > 0 {
				entry.InputModalities = append([]string(nil), catalog.InputModalities...)
			} else {
				entry.InputModalities = []string{"text"}
				if openaiStaticCaps.Image {
					entry.InputModalities = append(entry.InputModalities, "image")
				}
				if openaiStaticCaps.Audio {
					entry.InputModalities = append(entry.InputModalities, "audio")
				}
			}
		}
		if !m.ReasoningKnown && catalogued {
			entry.Reasoning = catalog.Reasoning
		}
		out = append(out, entry)
	}
	return out, nil
}

func (l openRouterLister) ListModels(ctx context.Context) ([]modelEntry, error) {
	raw, err := l.inner.ListModels(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]modelEntry, 0, len(raw))
	for _, m := range raw {
		out = append(out, modelEntry{
			ID:           m.ID,
			DisplayName:  m.DisplayName,
			ContextLimit: m.ContextLimit,
			// top_provider.max_completion_tokens (Slice C). CAPTURED into the meta store,
			// but currently OFF the OpenRouter request path: OpenRouter rides the openai
			// Responses adapter, which has no per-model max_tokens resolver today (only the
			// native anthropic adapter does). The composition UPPER clamp (clampLive in
			// livemeta.go) gates this value, so a future OpenRouter max_tokens consumer
			// cannot reintroduce the unbounded-live risk.
			OutputLimit:     m.OutputLimit,
			InputModalities: m.InputModalities,
			Reasoning:       m.Reasoning,
			ToolCall:        m.ToolCall,
			// Thinking stays zero: OpenRouter exposes only a coarse `reasoning` flag, not
			// the adaptive/enabled thinking-types matrix — so a model routed via OpenRouter
			// defers to the adapter's prefix floor (it is not the native anthropic provider).
		})
	}
	return out, nil
}

// anthropicLister adapts the *anthropic.Lister (which returns its OWN package type,
// []anthropic.Model — no import cycle) to the composition modelLister interface by
// mapping each anthropic.Model → modelEntry, INCLUDING the live thinking descriptor
// (the live replacement for the adapter's prefix matrix), the output ceiling, the
// context window, and image. This is the one place the adapter's type is mapped to
// the composition-neutral type; the adapter never imports internal/app.
type anthropicLister struct {
	inner *anthropic.Lister
}

func (l anthropicLister) ListModels(ctx context.Context) ([]modelEntry, error) {
	raw, err := l.inner.ListModels(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]modelEntry, 0, len(raw))
	for _, m := range raw {
		var mods []string
		if m.Image {
			mods = []string{"image"} // feed the SHARED hasImageModality predicate
		}
		out = append(out, modelEntry{
			ID:              m.ID,
			DisplayName:     m.DisplayName,
			ContextLimit:    m.ContextLimit,
			OutputLimit:     m.OutputLimit,
			InputModalities: mods,
			Reasoning:       m.Thinking.Adaptive || m.Thinking.Enabled,
			ToolCall:        true, // every current Claude model supports tool use
			Thinking: thinkingDescriptor{
				Known:    true, // a live anthropic source populated this
				Adaptive: m.Thinking.Adaptive,
				Enabled:  m.Thinking.Enabled,
			},
		})
	}
	return out, nil
}

// gatewayLister adapts the *openaicompat.Lister (which returns its OWN
// package type, []openaicompat.Model — no import cycle) to the composition
// modelLister interface by mapping each openaicompat.Model → modelEntry. It
// serves the ToolHive LLM gateway registry entry (issue #262); the SAME
// adapter type could serve any future OpenAI-compatible gateway (the package
// is protocol-generic, not ToolHive-specific).
type gatewayLister struct {
	inner *openaicompat.Lister
}

func (l gatewayLister) ListModels(ctx context.Context) ([]modelEntry, error) {
	raw, err := l.inner.ListModels(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]modelEntry, 0, len(raw))
	for _, m := range raw {
		out = append(out, modelEntry{
			ID:           m.ID,
			DisplayName:  m.DisplayName,
			ContextLimit: m.ContextLimit,
			// ToolCall:true — a coding-agent gateway fronts tool-capable models;
			// the local gate is not authoritative (the openrouter-image-caps
			// lesson: an over-permissive local flag fails safe via the
			// provider's own 4xx, never a silent wrong local guess).
			ToolCall: true,
			// Reasoning stays false and InputModalities stays nil (image=false,
			// conservative): the generic OpenAI-shaped /v1/models envelope
			// carries no modality/reasoning metadata. An absent context_window
			// decodes to 0, so the resolver falls back to the catalog then 128k.
		})
	}
	return out, nil
}

// openCodeLister wraps the generic openaicompat lister for OpenCode Go. It is the
// sibling of gatewayLister with ONE deliberate difference: it stamps the ADAPTER's
// static input modalities (text+image) rather than leaving them nil.
//
// Why: OpenCode Go's /models envelope carries no modality metadata, so a raw
// gatewayLister row has InputModalities==nil. modelCapability treats a PRESENT
// live row as authoritative, so nil would FLIP an uncatalogued model's Image from
// the adapter-static default (true) to false the instant a live refresh lands —
// contradicting the documented "uncatalogued → adapter static caps" fallback and
// making the picker/session echo diverge before vs after the refresh. Stamping the
// adapter modalities resolves the unknown-metadata case to that documented
// fallback STABLY (no flip), and the picker (projectModelEntry) + the session echo
// (modelCapability) agree for free since both read this same modelEntry. (ToolHive
// keeps the conservative nil-modality gatewayLister — a separate, deliberate
// choice, unchanged.)
type openCodeLister struct {
	inner *openaicompat.Lister
}

func (l openCodeLister) ListModels(ctx context.Context) ([]modelEntry, error) {
	raw, err := l.inner.ListModels(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]modelEntry, 0, len(raw))
	for _, m := range raw {
		out = append(out, modelEntry{
			ID:           m.ID,
			DisplayName:  m.DisplayName,
			ContextLimit: m.ContextLimit,
			ToolCall:     true, // same rationale as gatewayLister: fail-safe via provider 4xx
			// Adapter-static modalities (openaichat is text+image). Not per-model
			// truth (the endpoint gives none), but the documented stable fallback.
			InputModalities: []string{"text", "image"},
		})
	}
	return out, nil
}

// providerInventoryFloor returns the embedded catalog for built-ins and the one
// configured default model for custom providers. Custom providers deliberately
// have no static catalog: their default is enough to keep zero-selectors,
// validation, picker seeds, and listing failures deterministic.
func providerInventoryFloor(reg *providerRegistry, providerID string) []modelEntry {
	if embedded := embeddedModels(providerID); len(embedded) > 0 {
		return embedded
	}
	if reg != nil {
		if entry, ok := reg.Lookup(providerID); ok && entry.defaultModel != "" {
			return []modelEntry{{ID: entry.defaultModel}}
		}
	}
	return nil
}

// customProviderInventoryFloor returns the configured custom-provider default model
// as its synchronous inventory floor.
func customProviderInventoryFloor(reg *providerRegistry, providerID string) []modelEntry {
	if reg != nil {
		if entry, ok := reg.Lookup(providerID); ok && entry.defaultModel != "" {
			return []modelEntry{{ID: entry.defaultModel}}
		}
	}
	return nil
}

// embeddedModels projects the embedded catalog for one provider into the internal
// []modelEntry — the FALLBACK FLOOR used by BOTH the synchronous seed
// (modelSnapshot) and the live refresh when a provider has no lister or its fetch
// fails/empties. Returns nil for an uncatalogued provider (an honest miss). Since
// both the seed and the floor go through THIS one projection + projectModelEntry,
// they cannot drift.
func embeddedModels(providerID string) []modelEntry {
	p, ok := providercatalog.Default().Provider(providerID)
	if !ok {
		return nil
	}
	out := make([]modelEntry, 0, len(p.Models()))
	for _, m := range p.Models() {
		out = append(out, modelEntry{
			ID:              m.ID(),
			DisplayName:     m.Name(),
			ContextLimit:    m.ContextLimit(),
			OutputLimit:     m.OutputLimit(),
			InputModalities: m.InputModalities(),
			Reasoning:       m.SupportsReasoning(),
			ToolCall:        m.SupportsToolCall(),
			// Thinking stays zero (Known=false): the embedded catalog has no thinking-
			// types bit, so an embedded/seed model defers to the adapter's prefix floor.
		})
	}
	return out
}

// metadataCatalogProviderID selects a metadata-only catalog namespace. Codex
// uses OpenAI metadata for an already-entitled or explicitly selected matching
// id, but embeddedModels intentionally does not call this helper: OpenAI's API
// inventory must never become Codex subscription inventory.
func metadataCatalogProviderID(providerID string) string {
	if providerID == providerOpenAICodex {
		return providerOpenAI
	}
	return providerID
}

// projectModelEntry is the SINGLE projection of a (provider, modelEntry) into the
// proto ModelInfo — the ONE place the image intersection, display-name fallback,
// and field mapping live, shared by the seed (modelSnapshot) and the live refresh
// (liveModelSnapshot) so the two cannot hand-sync-drift. Image is adapterCaps.Image
// AND hasImageModality(modalities) — the adapter authority on transmit ∩ THIS
// model's modalities, via the shared predicate, whether the modalities came from
// live metadata or the embedded catalog.
func projectModelEntry(reg *providerRegistry, providerID string, m modelEntry) *mecatlv1.ModelInfo {
	name := m.DisplayName
	if name == "" {
		name = m.ID // display falls back to the id
	}
	contextLimit := m.ContextLimit
	if reg != nil {
		// The picker uses the SAME precedence core as engines and session echoes.
		// At projection time meta already contains this seed/live row; a genuinely
		// unknown value takes the same conservative floor as the engine.
		if resolved, known := reg.resolveWindowCore(Config{
			ContextWindowOverride: reg.contextWindowOverride,
			contextWindows:        reg.contextWindows,
		}, providerID, m.ID); known {
			contextLimit = resolved
		} else {
			contextLimit = defaultContextWindowTokens
		}
	}
	return &mecatlv1.ModelInfo{
		Id:           m.ID,
		ProviderId:   providerID,
		DisplayName:  name,
		Image:        modelAdapterCaps(reg, providerID).Image && hasImageModality(m.InputModalities),
		Reasoning:    m.Reasoning,
		ContextLimit: int64(contextLimit),
	}
}

// sortModelInfos orders a snapshot by (provider_id, id) for a deterministic picker.
// Both the seed and the live refresh sort through this one function.
func sortModelInfos(out []*mecatlv1.ModelInfo) {
	sort.Slice(out, func(i, j int) bool {
		if out[i].GetProviderId() != out[j].GetProviderId() {
			return out[i].GetProviderId() < out[j].GetProviderId()
		}
		return out[i].GetId() < out[j].GetId()
	})
}

// liveModelSnapshot builds the selectable-model inventory the way modelSnapshot
// does (availability-gated to reg.Available(), secret-free, sorted by
// provider_id,id), but consults each available provider's LIVE catalog when it has
// a lister. Merge semantics: live REPLACES the embedded subset for a provider on
// SUCCESS+NON-EMPTY; on ANY error/timeout/empty (or no lister) it falls back to the
// embedded subset (the fallback floor). It never returns a fabricated entry and
// never crashes.
//
// The fetch is attempted at most once per provider here; the CALLER (the
// background refresh in Build) owns concurrency/lifecycle. This function is pure
// w.r.t. composition state — it reads the registry and the network (through the
// listers) and returns the per-provider []modelEntry map (the resolver-feeding
// meta-store sink AND, via projectAll in publishSnapshot, the picker proto sink) —
// the ONE modelEntry list per provider both project from, so they cannot drift.
func liveModelSnapshot(ctx context.Context, d port.Diagnostics, reg *providerRegistry) map[string][]modelEntry {
	if reg == nil {
		return nil
	}
	byProvider := make(map[string][]modelEntry)
	for _, pid := range reg.Available() { // available (keyed) providers ONLY
		if pid == providerMock {
			continue // the mock never advertises selectable models
		}
		if entry, ok := reg.Lookup(pid); ok && entry.nativeEndpoint {
			byProvider[pid] = providerInventoryFloor(reg, pid)
			continue // native authenticated listing is on-demand, never during Build
		}
		if models, ok := reg.bootstrapModels[pid]; ok {
			// Default discovery already fetched this exact live entitlement snapshot
			// synchronously. Publish it without a duplicate back-to-back request.
			byProvider[pid] = models
			continue
		}
		byProvider[pid] = resolveProviderModels(ctx, d, reg, pid)
	}
	return byProvider
}

// resolveProviderModels returns the per-provider model list applying the merge
// + fail-safe rules, now OUTCOME-AWARE (issue #262, D3): live REPLACES
// embedded on success (even an honest empty — see below); on a lister error,
// a provider with a NON-EMPTY embedded catalog (openrouter, anthropic) falls
// back to it EXACTLY as before (byte-identical regression pin); a provider
// with an EMPTY embedded catalog (toolhive — no per-credential catalog to
// embed) instead falls back to the last-known-good live snapshot from a
// PRIOR successful fetch this process's lifetime, so a transient outage never
// blanks a picker that was populated moments ago; only a provider that has
// NEVER listed successfully yields a genuinely empty list.
//
// "Honest empty still replaces" (R3.1) is scoped to that same
// empty-embedded-catalog case: a KEYED provider with a non-empty embedded
// floor keeps the LEGACY behaviour of falling back to embedded on a live
// empty response (a transient blip must not blank an otherwise-rich picker).
func resolveProviderModels(ctx context.Context, d port.Diagnostics, reg *providerRegistry, pid string) []modelEntry {
	entry, ok := reg.Lookup(pid)
	if !ok || entry.lister == nil {
		return providerInventoryFloor(reg, pid)
	}
	embedded := providerInventoryFloor(reg, pid)
	live, err := entry.lister.ListModels(ctx)
	if err != nil {
		state := classifyLiveListError(err)
		hint := statusHintFor(pid, state)
		reg.outcomes.recordFailure(pid, state, hint)
		d.Log(ctx, port.LevelWarn, "live model fetch failed", "provider", pid, "err", err, "state", state)
		if len(embedded) > 0 {
			return embedded // byte-identical to pre-#262
		}
		if lastGood, ok := reg.outcomes.getLastGood(pid); ok {
			d.Log(ctx, port.LevelInfo, "live model fetch failed, using last-known-good snapshot",
				"provider", pid, "models", len(lastGood))
			return lastGood
		}
		return nil // never listed successfully: an honest empty, not a fabrication
	}
	// Success: record ALWAYS, even an empty list — an honest empty IS a
	// successful list (R3.1) and must be available as a future last-known-good
	// fallback for a provider with no embedded floor.
	reg.outcomes.recordSuccess(pid, live)
	if len(live) == 0 && len(embedded) > 0 {
		// Legacy behaviour for a KEYED provider with a non-empty embedded floor: a
		// transient empty response must not blank an otherwise-rich picker.
		d.Log(ctx, port.LevelWarn, "live model fetch returned no models, using embedded catalog", "provider", pid)
		return embedded
	}
	return mergeCustomProviderFloor(reg, pid, live)
}

func mergeCustomProviderFloor(reg *providerRegistry, providerID string, live []modelEntry) []modelEntry {
	floor := customProviderInventoryFloor(reg, providerID)
	if len(floor) == 0 {
		return live
	}
	out := append([]modelEntry(nil), floor...)
	for _, model := range live {
		if model.ID == floor[0].ID {
			out[0] = model
			continue
		}
		out = append(out, model)
	}
	return out
}

// providerStatus is one provider's last live-listing outcome (issue #262,
// D3 + surfacing): the state ("ok"/"unreachable"/"unauthorized"/"empty") plus
// a short human remediation hint, empty for "ok".
type providerStatus struct {
	State string
	Hint  string
}

// liveOutcomeStore is the composition-owned, mutex-guarded store of (a) the
// last-known-good live model snapshot per provider (process-lifetime, D3's
// last-known-good fallback) and (b) the current provider_status per provider
// (the v1 wire projection). It is held on providerRegistry, nil-tolerant like
// liveMetaStore (a hand-built test registry that never sets it behaves as a
// permanently-empty store — every method is a safe no-op/miss on nil).
type liveOutcomeStore struct {
	mu       sync.Mutex
	lastGood map[string][]modelEntry
	status   map[string]providerStatus
}

func newLiveOutcomeStore() *liveOutcomeStore {
	return &liveOutcomeStore{lastGood: map[string][]modelEntry{}, status: map[string]providerStatus{}}
}

// recordSuccess records a successful (possibly empty) live listing: the
// last-known-good snapshot and the derived status. "empty" fires ONLY when
// live is empty AND the provider's embedded catalog is ALSO empty (a
// genuinely nothing-to-show provider like toolhive); a keyed provider whose
// live response is momentarily empty but has a non-empty embedded floor
// still records "ok" (the caller returns the embedded floor to the picker,
// but the provider itself is reachable and authorized).
func (s *liveOutcomeStore) recordSuccess(pid string, live []modelEntry) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastGood[pid] = live
	state, hint := statusOK, ""
	if len(live) == 0 && len(embeddedModels(pid)) == 0 {
		state, hint = statusEmpty, statusHintFor(pid, statusEmpty)
	}
	s.status[pid] = providerStatus{State: state, Hint: hint}
}

// recordFailure records a failed live listing's classified state + hint. It
// does NOT touch lastGood — a failure never overwrites a prior success.
func (s *liveOutcomeStore) recordFailure(pid, state, hint string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.status[pid] = providerStatus{State: state, Hint: hint}
}

// getLastGood returns the last successful live snapshot for pid, if any.
func (s *liveOutcomeStore) getLastGood(pid string) ([]modelEntry, bool) {
	if s == nil {
		return nil, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.lastGood[pid]
	return v, ok
}

// getModelCount returns the count of models in pid's last-known-good live
// snapshot. 0 for an unreachable/unrecorded provider (a hand-built test store
// with a nil outcomes behaves as permanently-empty via the nil guard). It is
// the source of the ProviderStatus.model_count wire field — derived from the
// SAME lastGood map recordSuccess writes, so the count and the last-known-good
// fallback never drift.
func (s *liveOutcomeStore) getModelCount(pid string) int {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.lastGood[pid])
}

// getStatus returns the current recorded status for pid, if any (a provider
// never probed/listed has no recorded status).
func (s *liveOutcomeStore) getStatus(pid string) (providerStatus, bool) {
	if s == nil {
		return providerStatus{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.status[pid]
	return v, ok
}

// providerStatusProto projects operator-actionable live-inventory outcomes into
// the v1 wire message. That includes intent-driven gateways, openai-codex, and
// operator-defined custom providers (identified by their custom-only configured
// default model), whose live list is the account entitlement boundary; ordinary
// OpenRouter and Anthropic listing blips remain unprojected. Sorted by provider
// id for a deterministic wire shape.
func providerStatusProto(reg *providerRegistry) []*mecatlv1.ProviderStatus {
	if reg == nil {
		return nil
	}
	ids := reg.Available()
	for id := range reg.unavailableNative {
		if !slices.Contains(ids, id) {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	var out []*mecatlv1.ProviderStatus
	for _, pid := range ids {
		entry, available := reg.Lookup(pid)
		_, unavailableNative := reg.unavailableNative[pid]
		if !unavailableNative && (!available || (!entry.intentDriven && pid != providerOpenAICodex && entry.defaultModel == "")) {
			continue
		}
		status, ok := reg.outcomes.getStatus(pid)
		if !ok {
			continue
		}
		// default_model_auto_selected (issue #262 review finding 7) is set ONLY
		// for the DEFAULT provider whose model was auto-selected — never a
		// non-default intent-driven provider, and never an operator-configured
		// default.
		autoSelected := pid == reg.Default() && reg.DefaultModelAutoSelected()
		// available_not_default (this wave) is true ONLY when this intent-driven
		// provider is reachable (state == "ok") AND is NOT the active default.
		// reg.Default() is lock-free and immutable post-Build (see Default()).
		availableNotDefault := entry.intentDriven && status.State == statusOK && pid != reg.Default()
		// model_count is the live listing length (a slice len); a provider
		// never lists >2B models, so this reuses server.ClampInt32 (the same
		// overflow-safe int32 narrowing already used 15+ times in that
		// package) rather than hand-rolling the clamp again here.
		out = append(out, &mecatlv1.ProviderStatus{
			ProviderId:               pid,
			State:                    status.State,
			Hint:                     status.Hint,
			DefaultModelAutoSelected: autoSelected,
			ModelCount:               server.ClampInt32(reg.outcomes.getModelCount(pid)),
			AvailableNotDefault:      availableNotDefault,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].GetProviderId() < out[j].GetProviderId() })
	return out
}

// providerStatusSetter is the narrow seam the on-demand /models-open refresh
// writes provider_status through: the service's SetProviderStatus. Mirrors
// modelSwapper (SetModels) so the refresh path can update both wire sinks
// from one merged result.
type providerStatusSetter interface {
	SetProviderStatus([]*mecatlv1.ProviderStatus)
}

// (compile-time) *server.Service satisfies providerStatusSetter.
var _ providerStatusSetter = (*server.Service)(nil)

// refreshStaleModelsCooldown bounds how often the on-demand /models-open
// refresh (R1.4) re-fetches: a burst of picker opens against a persistently
// down gateway must not hammer it.
const refreshStaleModelsCooldown = 10 * time.Second

// refreshStaleModelsState is the mutex + last-refresh-timestamp the Service
// closure (internal/adapter/server wiring) captures, so concurrent /models
// opens serialize and the cooldown is enforced across calls. Composition
// constructs exactly one per Build (buildRefreshStaleModels).
type refreshStaleModelsState struct {
	mu   sync.Mutex
	last time.Time
}

// refreshStaleModels re-fetches ONLY INTENT-DRIVEN providers (issue #262:
// v1 scope is the ToolHive LLM gateway) whose last-recorded status is NOT
// "ok" (R1.4: "proxy started after boot ⇒ models appear on next refresh /
// /models open, no restart"). It is deliberately scoped to intentDriven
// entries — a KEY-driven provider (openrouter/anthropic) already has its own
// async background refresh (startLiveModelRefresh) and no "start it later"
// story; re-fetching one here on every /models open whose status happens to
// be unrecorded yet (e.g. immediately after Build, before the background
// refresh has settled) would add a REQUEST-PATH network call that never
// existed before #262 — a behaviour change this feature must not cause. A
// healthy (or non-intent-driven) provider is NEVER re-fetched here, so a
// deployment with no intent-driven provider costs nothing beyond the map
// scan. Each stale provider's fetch is bounded to 2s; the whole call is
// cooldown-gated via st so a burst of /models opens against a persistently-
// down gateway doesn't hammer it. It builds `fresh` from ONLY the re-fetched
// stale providers (never a pre-read whole-map snapshot — that was the lost-
// update race, issue #262 review finding 2) and hands it to publishSnapshot,
// which MERGES it into the current per-provider meta snapshot, rebuilds the
// picker proto slice, re-projects provider_status, and heals a still-empty
// intent-driven default model.
func refreshStaleModels(ctx context.Context, d port.Diagnostics, reg *providerRegistry, swap modelSwapper, st *refreshStaleModelsState) {
	if reg == nil || st == nil {
		return
	}
	st.mu.Lock()
	if !st.last.IsZero() && time.Since(st.last) < refreshStaleModelsCooldown {
		st.mu.Unlock()
		return
	}
	st.last = time.Now()
	st.mu.Unlock()

	var stale []string
	for _, pid := range reg.Available() {
		entry, ok := reg.Lookup(pid)
		if !ok || entry.lister == nil || (!entry.intentDriven && !entry.nativeEndpoint) {
			continue
		}
		if status, ok := reg.outcomes.getStatus(pid); ok && status.State == statusOK {
			continue // healthy providers are never re-fetched here
		}
		stale = append(stale, pid)
	}
	if len(stale) == 0 {
		return
	}

	fresh := make(map[string][]modelEntry, len(stale))
	for _, pid := range stale {
		fetchCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		fresh[pid] = resolveProviderModels(fetchCtx, d, reg, pid)
		cancel()
	}

	publishSnapshot(d, reg, swap, fresh)
}
