package app

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/internal/adapter/llmresilience"
	"github.com/stacklok/mecatl/internal/adapter/openaicodex"
	"github.com/stacklok/mecatl/internal/adapter/openaicompat"
	"github.com/stacklok/mecatl/internal/adapter/openrouter"
	"github.com/stacklok/mecatl/internal/adapter/providercatalog"
	"github.com/stacklok/mecatl/internal/adapter/toolhivellm"
	"github.com/stacklok/mecatl/provider/anthropic"
	"github.com/stacklok/mecatl/provider/openai"
	"github.com/stacklok/mecatl/provider/openaichat"
)

// Provider id strings. These are WIRE-STABLE once they reach the wire (S3's
// CreateSession provider_id field). Public catalog providers match models.dev's
// ids so the S2 join is direct; mecatl-specific routes (openai-codex, toolhive,
// mock) are explicit exceptions. Lowercase, never localized.
const (
	providerOpenAI = "openai"
	// providerOpenAICodex is the manual ChatGPT-subscription credential route.
	// It speaks the same Responses protocol as providerOpenAI but is a distinct
	// billing identity with a fixed, policy-enforced backend endpoint.
	providerOpenAICodex = "openai-codex"
	providerOpenRouter  = "openrouter"
	providerAnthropic   = "anthropic"
	// providerOpenCode is OpenCode Go (https://opencode.ai/zen/go/v1), an
	// OpenAI-compatible subscription gateway that speaks the Chat Completions wire
	// protocol uniformly. Unlike openai/openrouter (Responses API) it rides the
	// native Chat Completions adapter (openaichat). Key-driven (OPENCODE_API_KEY).
	providerOpenCode = "opencode"
	// providerToolhive (issue #262) is the intent-driven ToolHive LLM gateway
	// proxy entry: unlike the three above, it is registered by CONFIG-DETECTED
	// INTENT (or an explicit base-url override), never a credential.
	providerToolhive = "toolhive"
	// providerMock is the synthetic offline provider id used only when UseMock is
	// set. It never reaches the wire as a selectable provider; it exists so the
	// registry has exactly one entry in offline/smoke-test runs.
	providerMock = "mock"
)

// builtinDefaultModel is the per-provider default model used when the operator did
// NOT pass an explicit --model (cfg.Model == ""). The default must be a VALID id for
// that provider's endpoint: OpenAI's Responses API takes the bare "gpt-5", but
// OpenRouter namespaces every model, so the same model is "openai/gpt-5" there
// (catalogued in providercatalog, so it resolves cleanly through modelCapability). A
// provider absent from this table resolves to "" — the adapter/endpoint default —
// which is the safe, non-presumptuous fallback for a future provider.
var builtinDefaultModel = map[string]string{
	providerOpenAI:     "gpt-5",
	providerOpenRouter: "openai/gpt-5",
	// anthropic: the current GA Sonnet (verified against the live Anthropic models
	// overview, 2026-06-06): the best speed/intelligence balance and a cheaper
	// default than Opus. It is catalogued in providercatalog (resolves cleanly
	// through modelCapability) and supports extended thinking (adaptive).
	providerAnthropic: "claude-sonnet-4-6",
	// opencode (OpenCode Go): a confirmed live model id (GET /zen/go/v1/models,
	// 2026-07-17). Bare id — OpenCode Go does not namespace. Not catalogued in
	// providercatalog, so this default cannot fall back to the catalog.
	providerOpenCode: "glm-5.2",
}

// openRouterDefaultBaseURL is the OpenRouter Responses-compatible API base URL.
// OpenRouter rides the SAME stateless openai adapter (it speaks the Responses
// API) with this base URL substituted — there is NO separate wire adapter in P0.
const openRouterDefaultBaseURL = "https://openrouter.ai/api/v1"

// openCodeDefaultBaseURL is the OpenCode Go Chat-Completions API base URL
// (verified live 2026-07-17). The SDK appends "/chat/completions"; the generic
// openaicompat lister appends "/models".
const openCodeDefaultBaseURL = "https://opencode.ai/zen/go/v1"

// openaiStaticCaps / anthropicStaticCaps are the adapters' STATIC transmit
// capabilities — the authority modelCapability ANDs with the catalog. The shared
// .provider is initially built with these (before the registry is assembled and
// the default-model intersection can be computed); buildProviderRegistry's
// post-assembly fixup re-mints the default entry with the real
// modelCapability(default) caps and stamps entry.defaultCaps, so the live
// shared .provider carries the honest default-model intersection.
var (
	openaiStaticCaps    = (&openai.Provider{}).Capabilities()
	anthropicStaticCaps = (&anthropic.Provider{}).Capabilities()
	opencodeStaticCaps  = (&openaichat.Provider{}).Capabilities()
)

// envDetector resolves an environment variable to its value. It is the injectable
// seam (defaults to os.Getenv, set in Build) that keeps the registry's
// credential-availability detection OFFLINE-testable, mirroring xdgconfig.OSEnv's
// env-injection idiom already used in this package. A test passes a fake map-backed
// lookup so registry construction never touches the real process environment.
type envDetector func(name string) string

// providerEnvVars returns the ordered list of environment variables whose
// non-empty value makes a provider AVAILABLE (any one suffices). The var NAMES
// come from the embedded models.dev catalog's per-provider env[] (S2 replaced
// S1's inline map with this catalog read, leaving the registry's interface and
// behaviour unchanged).
//
// One mecatl-specific augmentation lives HERE in composition, never in the
// vendored catalog data (which stays honest to upstream — openrouter's env[] is
// ["OPENROUTER_API_KEY"] only): OpenRouter rides the same Responses-speaking
// openai adapter, so by mecatl convention it ALSO accepts an OpenAI key. We
// append OPENAI_API_KEY for the openrouter provider so an operator who set only
// OPENAI_API_KEY (pointing the base URL at OpenRouter) still resolves — exactly
// S1's behaviour. The first non-empty value wins.
//
// A provider id absent from the catalog returns nil (unavailable), exactly like
// an unset env var.
func providerEnvVars(providerID string) []string {
	// opencode (OpenCode Go) is NOT in the vendored models.dev subset, so the
	// catalog lookup below would return nil. Its credential env var is fixed by
	// convention (OPENCODE_API_KEY) — supply it directly so env auto-detection
	// works like every other provider.
	if providerID == providerOpenCode {
		return []string{"OPENCODE_API_KEY"}
	}
	p, ok := providercatalog.Default().Provider(providerID)
	if !ok {
		return nil
	}
	vars := p.EnvVars()
	if providerID == providerOpenRouter {
		// mecatl convention: OpenRouter also accepts an OpenAI key.
		if !slices.Contains(vars, "OPENAI_API_KEY") {
			vars = append(vars, "OPENAI_API_KEY")
		}
	}
	return vars
}

// providerEntry is one configured provider in the registry: its stable id, the
// constructed (resilience-wrapped) port.LLMProvider, whether its credentials
// resolved from the environment (availability), and its base URL for logging
// only. Composition-only — the agent/server never see this type.
type providerEntry struct {
	id        string           // wire-stable provider id
	provider  port.LLMProvider // resilience-wrapped, ready to hand to an engine
	available bool             // ≥1 of the provider's env[] keys resolved
	baseURL   string           // for logging/diagnostics ONLY; never wired
	// lister is the OPTIONAL live-catalog capability for this provider (nil =>
	// embedded-catalog only). Setting it (at registry build, per provider id) is the
	// ENTIRE opt-in for live model listing — no merge/snapshot plumbing change. It is
	// kept on the entry, NOT type-asserted from .provider, because openrouter and
	// openai share the SAME openai.Provider adapter and only openrouter opts in.
	lister modelLister
	// remint RE-MINTS this entry's provider adapter with a different reasoning-
	// effort token (ADR 0055) AND/OR a different per-session capability
	// intersection (T7), returning a fresh resilience-wrapped port.LLMProvider.
	// It captures the construction inputs (key/baseURL/resolvers/resilience
	// config) so the per-session engine factory can build a same-provider adapter
	// that carries the SESSION's effort + caps when EITHER differs from the
	// operator-default model the entry's .provider was built with — the SAME
	// factory discipline as the per-call model override (the factory owns adapter
	// construction; effort and caps are never port.LLMRequest fields). The DEFAULT
	// path never calls it (the shared .provider is reused byte-for-byte). effort is
	// an ALREADY-CLAMPED neutral token ("" = unset, the provider default); caps is
	// the composition-computed catalog ∩ adapter intersection for the session's
	// resolved (provider, model). nil for the mock entry (it ignores effort/caps)
	// and for a providerConstructor test seam that did not wire one.
	remint func(effort string, caps port.ProviderCapabilities) port.LLMProvider
	// defaultCaps is the capability intersection the entry's shared .provider was
	// built with (modelCapability over the operator-default provider+model), so the
	// per-session factory can compare against the session's resolved intersection
	// and re-mint ONLY when they differ (the byte-identical default path). The
	// zero value for a hand-built/test registry that did not set it — the factory
	// treats zero as "match anything" (no caps-driven re-mint) so a test registry
	// without defaultCaps behaves as before.
	defaultCaps port.ProviderCapabilities
	// intentDriven marks a registry entry that exists by CONFIG-DETECTED INTENT
	// (issue #262: the ToolHive LLM gateway) rather than a resolved credential.
	// It tiers preferredDefaultProvider (below every credential-driven provider)
	// and drives the client's "org" classification. provider_status is broader:
	// it also carries manually configured Codex entitlement outcomes.
	intentDriven bool
	// intentGatewayURL is the UPSTREAM the proxy forwards to, captured for
	// DIAGNOSTIC DISPLAY ONLY (R5.1/R5.2 — never used to build a request) when
	// intentDriven came from config-file auto-detection. Empty when
	// intentDriven came from an EXPLICIT base-url override (there is no
	// config-file gateway_url to show).
	intentGatewayURL string
	// intentExplicit is true when the intent-driven entry came from an
	// EXPLICIT operator override (--toolhive-llm-base-url) rather than
	// config-file auto-detection: the Build-time probe upgrades an
	// unreachable diagnostic from INFO to WARN on this path (the operator
	// asked for this endpoint directly).
	intentExplicit bool
	// defaultEffort is the OPERATOR-DEFAULT reasoning-effort token (ADR 0055,
	// already normalised + per-provider clamped by operatorDefaultEffortFor) this
	// entry was built with. Stamped by buildProviderRegistry's fixup loop
	// BEFORE the T7 re-mint so remintEntry (the shared re-mint helper, issue
	// #262 review finding 4) can re-derive an entry's caps WITHOUT threading
	// cfg through to the runtime heal path (healDefaultModel runs long after
	// Build, off the request path, and has no cfg in scope).
	defaultEffort string
}

// providerRegistry holds the N configured providers. It is built once in Build
// from Config + the environment, lives entirely in the composition layer, and is
// the single source of "which provider backs model X / session Y". The agent and
// server never see it — they receive a bare port.LLMProvider selected here.
//
// In S1 the registry's only consumer is buildProvider (which returns the default
// provider so the Build call site is unchanged); per-session multi-provider
// routing is S3.
type providerRegistry struct {
	entries      map[string]providerEntry // keyed by provider id; only AVAILABLE entries
	defaultID    string                   // resolved default provider (precedence: resolveDefaultModel)
	defaultModel string                   // resolved default model for defaultID ("" => adapter/endpoint default)
	// meta is the composition-owned live-metadata store the request-path resolvers
	// read (output ceiling / context window / modalities / thinking). It is seeded
	// from the catalog at build BEFORE any network call and atomically swapped by the
	// background live refresh, so a resolver is always live-first with a catalog
	// floor. Never nil for a registry built by buildProviderRegistry; nil-tolerant
	// reads (liveMetaStore.lookup) keep a hand-built test registry safe.
	meta *liveMetaStore
	// contextWindows is the operator-tier exact provider/model override map. It is
	// immutable after Build and read by the picker projection as well as resolvers.
	contextWindows map[string]map[string]int
	// contextWindowOverride is the process-wide CLI escape hatch mirrored from Config.
	// Keeping it beside contextWindows lets model-list projection use the same resolver
	// as engines and echoes instead of growing a second precedence implementation.
	contextWindowOverride int
	// outcomes is the composition-owned live-LISTING outcome store (issue #262,
	// D3 + surfacing): last-known-good snapshots (process-lifetime) plus the
	// per-provider status (ok/unreachable/unauthorized/empty) the v1
	// provider_status wire message projects. Seeded by the Build-time
	// probeToolhive call and kept current by every subsequent
	// resolveProviderModels call (the background refresh + the on-demand
	// /models-open refresh). nil-tolerant like meta (a hand-built test
	// registry that never sets it behaves as a permanently-empty store).
	outcomes *liveOutcomeStore
	// bootstrapModels carries a synchronous default-discovery result into the
	// one-shot initial live publish. It is immutable after construction and is
	// consumed only by liveModelSnapshot; on-demand refreshes still hit the
	// entitlement endpoint normally.
	bootstrapModels map[string][]modelEntry
	// defaultModelAutoSelected is true when defaultModel was AUTO-SELECTED (a
	// first-listed heal/probe pick, issue #262 review finding 7/R2.4) rather
	// than operator-configured (--model/--default-model). Set ONLY at the two
	// sites that fill an EMPTY defaultModel from a live listing —
	// probeToolhive's Build-time auto-pick and healDefaultModel's runtime
	// heal — both already gated on `defaultModel == ""`, which a configured
	// model precludes (resolveDefaultModel would have taken tier (1) or (2)
	// instead), so this can never be true alongside an operator choice. Guarded
	// by defaultModelMu like defaultModel itself; read via the locked
	// DefaultModelAutoSelected accessor.
	defaultModelAutoSelected bool
	// defaultModelMu guards EVERY post-construction access to defaultModel:
	// healDefaultModel's check-and-set (the async live-refresh goroutine and
	// the on-demand refreshStaleModels — reached off the ListModels request
	// path — can both run concurrently after Build returns) AND
	// ResolvedDefaultModel's read (any request-handling goroutine may call it
	// at any time post-Build). Construction-time reads/writes
	// (resolveDefaultModel, the T7 caps-fixup loop, probeToolhive, all inside
	// buildProviderRegistry) run BEFORE any goroutine or request handler holds
	// a reference to the registry, so they stay unlocked — this mutex exists
	// only for the concurrent post-Build window.
	defaultModelMu sync.Mutex
	// publishMu serializes every post-Build snapshot publish (publishSnapshot,
	// issue #262 review finding 2): the merge-then-swap-then-heal-then-project
	// sequence must not interleave between the one-shot background refresh and
	// the on-demand refreshStaleModels, or two concurrent merges could each read
	// the same pre-merge snapshot and one publish's result would be lost. Fetches
	// (the network calls) stay OUTSIDE this mutex — only the cheap, no-network
	// publish tail is serialized. Lock order: publishMu may acquire
	// defaultModelMu/entriesMu (inside healDefaultModel/remintEntry); NEVER the
	// reverse — nothing holding defaultModelMu or entriesMu may acquire publishMu.
	publishMu sync.Mutex
	// entriesMu guards EVERY post-construction access to entries (issue #262
	// review finding 4): healDefaultModel's re-mint (remintEntry) mutates a
	// SINGLE entry's .provider/.defaultCaps post-Build, concurrently with
	// Lookup/Available reads from any request-handling goroutine. Lookup and
	// Available take an RLock (cheap, concurrent-reader-friendly); remintEntry's
	// write takes the write lock ONLY around the map mutation itself (the
	// re-mint construction runs BEFORE the lock — see remintEntry). Construction-
	// time reads/writes (buildProviderRegistry, probeToolhive) run BEFORE any
	// goroutine or request handler holds a reference to the registry, so they
	// stay unlocked — mirroring defaultModelMu's discipline. Lock order: this
	// mutex is a LEAF (never acquires publishMu/defaultModelMu while held).
	entriesMu sync.RWMutex
}

// Lookup returns the entry for id and whether it exists (and is therefore
// available — the registry only holds available entries). RLock-guarded
// (entriesMu): a concurrent remintEntry write must never race a reader.
func (r *providerRegistry) Lookup(id string) (providerEntry, bool) {
	r.entriesMu.RLock()
	defer r.entriesMu.RUnlock()
	e, ok := r.entries[id]
	return e, ok
}

// Available returns the available provider ids, sorted, for ListModels (S3) and
// the zero-keys diagnostic. RLock-guarded (entriesMu), matching Lookup.
func (r *providerRegistry) Available() []string {
	r.entriesMu.RLock()
	defer r.entriesMu.RUnlock()
	ids := make([]string, 0, len(r.entries))
	for id := range r.entries {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// Default returns the default provider id, or "" when zero providers are
// available (the zero-keys case). Deliberately LOCK-FREE, unlike its siblings
// (ResolvedDefaultModel/DefaultModelAutoSelected use defaultModelMu, Lookup/
// Available use entriesMu): defaultID is set ONCE in buildProviderRegistry and
// is IMMUTABLE for the life of the registry — nothing post-Build ever
// reassigns it (the healed/auto-selected MODEL can change; the default
// PROVIDER identity never does). If a future change ever makes defaultID
// mutable after Build, it MUST add locking here too — this comment is the
// trip-wire.
func (r *providerRegistry) Default() string { return r.defaultID }

// ResolvedDefaultModel returns the resolved EFFECTIVE default model for the
// default provider, or "" when no model resolved (the adapter/endpoint
// default): the explicit cfg.Model when set, else the server-configured
// cfg.DefaultModel, else the per-provider builtin table value (see
// resolveDefaultModel). Deliberately NOT named after Config.DefaultModel —
// that field is one TIER of this resolution (and under --model a value it
// lost to), not the same concept. Lock-guarded (defaultModelMu): a
// zero-selector session or diagnostic can call this from any request-handling
// goroutine while healDefaultModel is concurrently healing it post-Build.
func (r *providerRegistry) ResolvedDefaultModel() string {
	r.defaultModelMu.Lock()
	defer r.defaultModelMu.Unlock()
	return r.defaultModel
}

// DefaultModelAutoSelected reports whether the default provider's resolved
// model was AUTO-SELECTED (issue #262 review finding 7) rather than
// operator-configured. Lock-guarded (defaultModelMu), matching
// ResolvedDefaultModel.
func (r *providerRegistry) DefaultModelAutoSelected() bool {
	r.defaultModelMu.Lock()
	defer r.defaultModelMu.Unlock()
	return r.defaultModelAutoSelected
}

// DefaultModelFor returns the builtin default model for a given provider id (the
// id the model string is VALID for), or "" when the provider has no table entry
// (the adapter/endpoint default). It is the single source the per-sub-agent
// provider resolver uses when a def switches provider but pins no model — a parent
// model string is for the PARENT's provider and may be invalid on the child's, so
// the child rebases off this provider-appropriate default rather than inheriting
// the parent model. Composition-only, like the rest of the registry.
func (*providerRegistry) DefaultModelFor(id string) string { return builtinDefaultModel[id] }

// healDefaultModel fills a still-empty defaultModel for an INTENT-DRIVEN
// default provider (issue #262 §1 accepted deviation: toolhive registered
// sole+probe-down boots with defaultModel="" so Build never bricks) from a
// freshly-swapped live snapshot. It is called after EVERY live-model swap —
// startLiveModelRefresh's sync AND async paths, and refreshStaleModels'
// on-demand /models-open path — so the model heals the instant the proxy
// comes up, with NO restart (R1.4).
//
// It is a no-op when defaultModel is already non-empty (NEVER overwrites an
// operator/session choice), when the default provider isn't intentDriven, or
// when byProvider has nothing for it. The check-and-set itself runs under
// defaultModelMu (issue found by review: an unlocked fast-path read raced
// against a concurrent writer once this could be reached from BOTH the async
// live-refresh goroutine AND refreshStaleModels, itself reachable off the
// ListModels request path) — no unlocked pre-check. The re-mint (issue #262
// review finding 4: the runtime auto-select was skipping the caps/effort
// re-mint the Build-time auto-select performs, leaving the per-session
// factory comparing against a stale defaultCaps baseline) runs via the
// SHARED remintEntry helper AFTER defaultModelMu is released — remintEntry
// takes its own entriesMu write lock for the map mutation, and nesting that
// under defaultModelMu would add a lock-ordering constraint nothing else
// needs (only the ONE winning filler ever reaches this branch, since a
// subsequent call sees defaultModel already set and returns above). Logs ONE
// "(auto-selected)" INFO the first time it fills.
func (r *providerRegistry) healDefaultModel(d port.Diagnostics, byProvider map[string][]modelEntry) {
	if r == nil {
		return
	}
	entry, ok := r.Lookup(r.defaultID)
	if !ok || !entry.intentDriven {
		return
	}
	live := byProvider[r.defaultID]
	if len(live) == 0 {
		return
	}
	r.defaultModelMu.Lock()
	if r.defaultModel != "" {
		r.defaultModelMu.Unlock()
		return // already set (a prior heal, or an operator/session pin)
	}
	model := live[0].ID
	r.defaultModel = model
	r.defaultModelAutoSelected = true // issue #262 review finding 7
	r.defaultModelMu.Unlock()

	r.remintEntry(r.defaultID, model)
	d.Log(context.Background(), port.LevelInfo,
		"provider default model (auto-selected) after live refresh",
		// R2.6: name the proxy base URL + upstream gateway_url on the SAME
		// event that makes toolhive the default model, not only via the
		// separate "registered and reachable" line (which may have logged an
		// unreachable/empty outcome at Build time, before this heal fires).
		"provider", r.defaultID, "model", model,
		"base_url", entry.baseURL, "gateway_url", entry.intentGatewayURL)
}

// remintEntry RE-MINTS pid's shared .provider/.defaultCaps for model — the ONE
// re-mint path shared by the build-time T7 fixup, probeToolhive's auto-pick,
// and healDefaultModel (issue #262 review finding 4: the third re-mint site,
// extracted so the three cannot drift on the caps/effort computation). It
// reads entry.defaultEffort (stamped by buildProviderRegistry BEFORE the
// registry is handed out) rather than taking a cfg parameter, so a caller
// reached long after Build (healDefaultModel, off the request path, with no
// cfg in scope) can share it byte-for-byte with the two build-time sites. A
// missing entry or one with no remint closure (mock / a providerConstructor
// test seam) is a silent no-op. The write is entriesMu-guarded (a concurrent
// Lookup/Available must never observe a torn entry); the (Lookup + caps
// compute) that PRECEDE the write happen WITHOUT the write lock held, so a
// concurrent reader is never blocked behind a live-listing round-trip — there
// is none here, but the discipline matters because modelCapability may itself
// Lookup.
func (r *providerRegistry) remintEntry(pid, model string) {
	entry, ok := r.Lookup(pid) // RLock — never nested under the write lock below
	if !ok || entry.remint == nil {
		return
	}
	defCaps := modelCapability(r, pid, model) // may Lookup internally — compute BEFORE the write lock
	p := entry.remint(entry.defaultEffort, defCaps)
	r.entriesMu.Lock()
	e := r.entries[pid]
	e.provider = p
	e.defaultCaps = defCaps
	r.entries[pid] = e
	r.entriesMu.Unlock()
}

// errNoProvider is the named, actionable zero-keys error: when no provider's
// credentials resolved AND the mock is not selected, Build cannot serve a useful
// engine. The copy enumerates every accepted credential env var (per adapter), the
// compatible/proxy base-URL overrides (the "I have an endpoint but no public key"
// case), the offline --mock escape hatch, and a docs pointer, so first-run is
// self-explanatory. Keep it lowercase with NO trailing punctuation (ST1005);
// TestRegistryZeroKeys pins the load-bearing substrings.
var errNoProvider = errors.New(
	"no LLM provider available: set one of ANTHROPIC_API_KEY (Claude), " +
		"OPENAI_API_KEY (OpenAI), OPENROUTER_API_KEY (one key, many models — a good first choice), " +
		"or OPENCODE_API_KEY (OpenCode Go) " +
		"in the environment; for an OpenAI- or Anthropic-compatible/proxy endpoint pass the matching key " +
		"plus --openai-base-url / --anthropic-base-url / --openrouter-base-url / --opencode-base-url; to try mecatl offline with " +
		"no key run with --mock; see docs/usage.md for provider setup")

// buildProviderRegistry constructs the registry from cfg and the injected env
// detector. It builds (and resilience-wraps) ONLY the available providers — there
// is no point holding an unkeyed provider — keyed by their stable id. When UseMock
// is set it short-circuits to a single synthetic "mock" entry (preserving the
// offline mockllm test/smoke path) regardless of the environment.
//
// It returns errNoProvider when no provider resolves credentials and the mock is
// not selected. Startup logging emits one line per available provider with the
// provider id and base URL ONLY — NEVER the key (CWE-200; S5 verifies).
func buildProviderRegistry(cfg Config, detect envDetector) (*providerRegistry, error) {
	return buildProviderRegistryContext(context.Background(), cfg, detect)
}

func buildProviderRegistryContext(ctx context.Context, cfg Config, detect envDetector) (*providerRegistry, error) {
	ctx = providerRegistryContext(ctx)
	detect = providerRegistryDetector(detect)

	// UseMock short-circuit: a single synthetic entry, offline, regardless of env.
	if cfg.UseMock || cfg.MockProvider != nil {
		cfg.diag().Log(ctx, port.LevelWarn, "LLM provider: mock (canned, offline) — for smoke tests only")
		mock := port.LLMProvider(mockllm.New(
			mockllm.TextTurn("Mock provider: no real model is configured. Set OPENAI_API_KEY for live use."),
		))
		if cfg.MockProvider != nil {
			// The test-only scripted seam (Config.MockProvider): the caller's
			// scripted provider replaces the canned single text turn.
			mock = cfg.MockProvider
		}
		// The mock is intentionally left UNWRAPPED by resilience: it never fails over
		// the network, so retries/breaker would be inert.
		return &providerRegistry{
			entries:   map[string]providerEntry{providerMock: {id: providerMock, provider: mock, available: true}},
			defaultID: providerMock,
			// The mock ignores the model entirely; carry cfg.Model so an explicit
			// --model is still echoed (snapshots/capabilities) without inventing one.
			defaultModel: cfg.Model,
			// An EMPTY (non-nil) meta store: the mock has no lister and no catalog rows,
			// so it stays empty, but an explicit store keeps the resolver helpers'
			// invariant uniform (every production registry carries one) and future-proofs
			// a mock-with-metadata path. The helpers are nil-tolerant regardless.
			meta: newLiveMetaStore(),
			// An empty outcomes store mirrors meta: the mock has no lister, so it
			// never records anything, but every production registry carries one.
			outcomes: newLiveOutcomeStore(),
		}, nil
	}

	entries := make(map[string]providerEntry)

	// The live-metadata store the request-path resolvers read. Construct it FIRST so
	// the per-provider entries' resolver closures (notably anthropic's max-tokens +
	// thinking resolvers) capture it; it is seeded from the catalog (seedFromCatalog)
	// AFTER the entries are built (we need reg.Available()) and BEFORE any network
	// call, then atomically swapped by the background refresh. Live-first, catalog
	// floor — see liveMetaStore.
	meta := newLiveMetaStore()

	// openai: AVAILABLE iff a key resolves — from cfg.OpenAIKey (which the cmd layer
	// reads from OPENAI_API_KEY) or, failing that, the OPENAI_API_KEY env var via
	// detect. Availability is purely KEY-DRIVEN; the legacy cfg.UseOpenAI flag is a
	// back-compat selector the cmd layer still sets when a key is present, but it does
	// NOT gate the registry (a key alone suffices — env auto-detection is the S1 model).
	if key := providerKey(cfg.OpenAIKey, providerOpenAI, detect); key != "" {
		entries[providerOpenAI] = newOpenAICompatEntry(cfg, providerOpenAI, key, cfg.OpenAIBaseURL)
	}

	if entry, err := newOpenAICodexEntry(cfg); err != nil {
		return nil, err
	} else if entry.available {
		entries[providerOpenAICodex] = entry
	}

	// openrouter: same stateless openai adapter, OpenRouter base URL, keyed by
	// OPENROUTER_API_KEY (falling back to OPENAI_API_KEY by convention).
	if key := providerKey(cfg.OpenRouterKey, providerOpenRouter, detect); key != "" {
		baseURL := cfg.OpenRouterBaseURL
		if baseURL == "" {
			baseURL = openRouterDefaultBaseURL
		}
		// Downstream-provider routing (issue #480): the openrouter entry ALONE arms
		// the two OpenRouter-private knobs. WithOpenRouterProviderPreferences resolves the
		// per-model `provider` body object from cfg.openRouterRoutes (nil for an
		// unconfigured model → no body key); WithOpenRouterMetadata(true) arms the
		// X-OpenRouter-Metadata header so the routed downstream echoes back as
		// ChunkProviderRoute → EvProviderRoute. Both ride the `extra` channel, so
		// EVERY mint (the default build AND every per-session/heal remint) carries
		// them. Every OTHER entry passes no such option, so the body key + header
		// can never leak to a non-OpenRouter endpoint.
		entry := newOpenAICompatEntry(cfg, providerOpenRouter, key, baseURL,
			openai.WithOpenRouterProviderPreferences(cfg.openRouterRouteFor),
			openai.WithOpenRouterMetadata(true))
		// OpenRouter opts into LIVE model listing: its public /models endpoint
		// enumerates the real catalog (336 models) vs the curated embedded subset.
		// The lister rides on the entry (NOT the shared openai.Provider) so openai —
		// which uses the same adapter — does NOT advertise live listing. The HTTP
		// client is the composition test seam (nil => default timeout client; tests
		// inject a mock transport). KEYLESS: the lister never receives the key.
		entry.lister = openRouterLister{inner: openrouter.NewLister(cfg.liveModelHTTPClient)}
		entries[providerOpenRouter] = entry
	}

	// opencode (OpenCode Go): the native Chat Completions adapter (openaichat, NOT
	// the openai Responses adapter), keyed by OPENCODE_API_KEY. Its /models endpoint
	// is OpenAI-shaped, so it opts into LIVE model listing via the generic
	// openaicompat lister (bare ids, no display name). The lister IS keyed here —
	// unlike openrouter's keyless public catalog, OpenCode Go requires the bearer.
	if key := providerKey(cfg.OpenCodeKey, providerOpenCode, detect); key != "" {
		baseURL := cfg.OpenCodeBaseURL
		if baseURL == "" {
			baseURL = openCodeDefaultBaseURL
		}
		entry := newOpenCodeEntry(cfg, providerOpenCode, key, baseURL)
		entry.lister = openCodeLister{inner: openaicompat.NewLister(baseURL, key, cfg.liveModelHTTPClient)}
		entries[providerOpenCode] = entry
	}

	// anthropic: the native Messages-API adapter (NOT the openai adapter). AVAILABLE
	// iff a key resolves — from cfg.AnthropicKey (the cmd layer reads ANTHROPIC_API_KEY)
	// or the catalog-driven env vars via detect. Per-session routing, sub-agent
	// provider switch, and the capability intersection treat it as data (no change).
	if key := providerKey(cfg.AnthropicKey, providerAnthropic, detect); key != "" {
		entries[providerAnthropic] = newAnthropicEntry(cfg, key, meta)
	}

	// toolhive (issue #262, D1): registered by CONFIG-DETECTED INTENT alone —
	// resolveToolhiveIntent NEVER runs a network probe, so registration never
	// blocks on (or is gated by) reachability (R1.1). With toolhive registered,
	// len(entries)>0 even with ZERO provider keys, so errNoProvider no longer
	// fires for a ToolHive-only operator — intended (zero-API-key onboarding).
	// Issue #265: the intent now carries a routing mode — proxy (loopback,
	// today's behaviour) or direct (gateway_url + in-process OIDC token).
	if intent, ok := resolveToolhiveIntent(cfg); ok {
		switch intent.mode {
		case toolhiveModeDirect:
			entries[providerToolhive] = newDirectGatewayEntry(cfg, providerToolhive, intent, cfg.toolhiveConfigPath)
		default: // toolhiveModeProxy (the zero value + the explicit-override path)
			lister := gatewayLister{inner: openaicompat.NewLister(intent.baseURL, toolhivellm.PlaceholderToken, cfg.liveModelHTTPClient)}
			entries[providerToolhive] = newGatewayEntry(cfg, providerToolhive, intent.baseURL, intent.gatewayURL, intent.explicit, lister)
		}
	}

	if len(entries) == 0 {
		return nil, errNoProvider
	}

	// Stamp each entry's OPERATOR-DEFAULT effort BEFORE the registry is handed
	// out (issue #262 review finding 4): remintEntry (below) reads
	// entry.defaultEffort rather than re-deriving it from cfg, so the runtime
	// heal path (healDefaultModel, reached long after Build with no cfg in
	// scope) can share the exact same re-mint helper as the two build-time
	// call sites.
	for id, entry := range entries {
		entry.defaultEffort = operatorDefaultEffortFor(cfg, id)
		entries[id] = entry
	}

	reg := &providerRegistry{entries: entries, meta: meta, outcomes: newLiveOutcomeStore()}
	reg.defaultID, reg.defaultModel = resolveDefaultModel(cfg, reg)
	// Seed the live-metadata store from the embedded catalog for every available
	// provider — the t=0 floor every resolver reads before the background live swap.
	meta.seedFromCatalog(reg.Available())
	if err := bootstrapOpenAICodexDefault(ctx, reg, cfg); err != nil {
		return nil, err
	}
	// T7 post-assembly fixup: stamp each real adapter entry's shared .provider
	// with the DEFAULT model's capability intersection (catalog ∩ adapter) and
	// record it as defaultCaps so the per-session factory can re-mint ONLY when a
	// session's resolved intersection differs (the byte-identical default path).
	// The entries were initially built with the adapter's STATIC transmit caps
	// (the registry wasn't assembled yet, so modelCapability couldn't run); this
	// re-mint replaces that placeholder with the honest default-model intersection.
	// Mock / providerConstructor-seam entries have no remint closure and are
	// skipped by remintEntry itself (they ignore caps; defaultCaps stays zero =
	// "match anything").
	for id := range entries {
		// The shared .provider serves the operator-default model for the DEFAULT
		// provider (reg.defaultModel — the resolved cfg.Model), and the provider's
		// builtin default for a non-default provider (its .provider is only ever a
		// re-mint base for a session that selects it, so the placeholder model's
		// caps are immediately replaced by the session's).
		model := reg.DefaultModelFor(id)
		if id == reg.defaultID {
			model = reg.defaultModel
		}
		reg.remintEntry(id, model)
	}
	// Build-time probe (issue #262, R1.2): only runs when a toolhive entry
	// exists (a single map lookup otherwise). It NEVER gates registration
	// (already done above) — it drives the startup diagnostic, the initial
	// provider_status + last-known-good seed, and default-model eligibility.
	if err := probeToolhive(reg, cfg); err != nil {
		return nil, err
	}
	return reg, nil
}

func providerRegistryContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

func providerRegistryDetector(detect envDetector) envDetector {
	if detect == nil {
		return osGetenv
	}
	return detect
}

// newOpenAICodexEntry returns an unavailable zero entry when no manual token is
// configured. A configured token is revalidated at registry construction because
// it can expire after the command root's one-time snapshot resolution. The request
// policy is captured as an extra adapter option so initial construction,
// capability/default fixups, and per-session re-mints retain the same fixed
// endpoint, headers, expiry check, redirect refusal, and retry setting.
//
// Deliberately do not route this through providerKey/providerEnvVars: a ChatGPT
// subscription and an OpenAI API key are separate billing identities.
func newOpenAICodexEntry(cfg Config) (providerEntry, error) {
	if !cfg.OpenAICodexCredential.Configured() {
		return providerEntry{}, nil
	}
	now := cfg.openAICodexNow
	if now == nil {
		now = time.Now
	}
	if err := cfg.OpenAICodexCredential.Validate(now()); err != nil {
		return providerEntry{}, err
	}
	policy, err := openaicodex.NewRequestPolicy(
		cfg.OpenAICodexCredential,
		now,
		cfg.openAICodexTransport,
	)
	if err != nil {
		return providerEntry{}, err
	}
	entry := newOpenAICompatEntry(
		cfg,
		providerOpenAICodex,
		"policy-owned",
		openaicodex.BaseURL,
		openai.WithHTTPClient(policy.HTTPClient()),
		openai.WithMaxRetries(0),
	)
	entry.lister = openAICodexLister{inner: openaicodex.NewLister(policy)}
	return entry, nil
}

const openAICodexBootstrapTimeout = 5 * time.Second

// bootstrapOpenAICodexDefault performs the one synchronous entitlement lookup
// required when Codex is the resolved default and the operator supplied no model.
// Explicit models and a different preferred provider bypass it entirely. The
// selected row is written through the same outcome/meta facts later background
// refreshes use, before the registry remints the default provider.
func bootstrapOpenAICodexDefault(parent context.Context, reg *providerRegistry, cfg Config) error {
	if reg == nil || reg.defaultID != providerOpenAICodex || reg.defaultModel != "" {
		return nil
	}
	entry, ok := reg.Lookup(providerOpenAICodex)
	if !ok || entry.lister == nil {
		return errors.New("openai-codex: default model discovery is unavailable")
	}
	ctx, cancel := context.WithTimeout(parent, openAICodexBootstrapTimeout)
	defer cancel()
	models, err := entry.lister.ListModels(ctx)
	if err != nil {
		state := classifyLiveListError(err)
		reg.outcomes.recordFailure(providerOpenAICodex, state, "")
		if state == statusUnauthorized {
			return fmt.Errorf("openai-codex: default model discovery unauthorized: %w", err)
		}
		return fmt.Errorf("openai-codex: default model discovery unreachable: %w", err)
	}
	reg.outcomes.recordSuccess(providerOpenAICodex, models)
	if len(models) == 0 {
		return errors.New("openai-codex: account returned no picker-visible models; replace the manual token or choose an explicit model")
	}
	reg.defaultModel = models[0].ID
	reg.defaultModelAutoSelected = true
	reg.bootstrapModels = map[string][]modelEntry{providerOpenAICodex: models}
	reg.meta.mergeSwap(map[string][]modelEntry{providerOpenAICodex: models})
	cfg.diag().Log(ctx, port.LevelInfo, "openai-codex default model auto-selected from account entitlements",
		"provider", providerOpenAICodex, "model", reg.defaultModel)
	return nil
}

// providerKey resolves the credential for a provider: an explicit cfg-supplied key
// wins (it was read from the env by the cmd layer), otherwise the first non-empty
// value among the provider's catalog-driven env vars (providerEnvVars — the
// multi-env-var slice, any one suffices). Returns "" when nothing resolves
// (provider unavailable).
func providerKey(cfgKey, providerID string, detect envDetector) string {
	if cfgKey != "" {
		return cfgKey
	}
	for _, v := range providerEnvVars(providerID) {
		if val := detect(v); val != "" {
			return val
		}
	}
	return ""
}

// newOpenAICompatEntry constructs a resilience-wrapped openai-adapter provider
// entry. It is shared by openai, openrouter, openai-codex, and (issue #262)
// toolhive-gateway (each is the SAME adapter with a different base URL + key/token),
// so they cannot drift on resilience wrapping. It logs the provider id and base URL
// ONLY — never the key. extra carries additional openai.Options appended to EVERY
// construct() call (default AND per-session/heal re-mints), so a caller-supplied
// option rides every re-mint too, never just the initial build. openai passes none —
// byte-identical; the gateway entry passes its redirect-refusing WithHTTPClient (F3);
// openrouter passes its downstream-provider WithOpenRouterProviderPreferences + WithOpenRouterMetadata
// (issue #480).
func newOpenAICompatEntry(cfg Config, id, key, baseURL string, extra ...openai.Option) providerEntry {
	cfg.diag().Log(context.Background(), port.LevelInfo, "LLM provider available", "provider", id, "model", cfg.Model, "base_url", baseURL)
	// Composition-only test seam (S3 e2e): when a providerConstructor is injected,
	// it builds the provider (e.g. a distinct mock per id) instead of the real
	// openai adapter, so the offline multi-provider e2e can hold TWO real provider
	// ids backed by mocks. Production leaves it nil and uses the openai adapter.
	if cfg.providerConstructor != nil {
		return providerEntry{id: id, provider: cfg.providerConstructor(cfg, id, key, baseURL), available: true, baseURL: baseURL}
	}
	// construct mints a resilience-wrapped openai adapter carrying the given
	// reasoning-effort token (ADR 0055) and per-session capability intersection
	// (T7). It is the SINGLE construction path: the default .provider is
	// construct(defaultEffort, defaultCaps) and the per-session re-mint is
	// construct(sessionEffort, sessionCaps), so the two cannot drift on resilience
	// wrapping. It also closes over `extra` (this func's variadic parameter, F3) —
	// so EVERY mint (the default build AND every per-session/heal re-mint via
	// remintEntry) carries whatever options the caller passed newOpenAICompatEntry
	// (e.g. the gateway entry's redirect-refusing WithHTTPClient), never just the
	// initial one.
	construct := func(effort string, caps port.ProviderCapabilities) port.LLMProvider {
		opts := []openai.Option{openai.WithAPIKey(key)}
		if baseURL != "" {
			opts = append(opts, openai.WithBaseURL(baseURL))
		}
		if effort != "" {
			opts = append(opts, openai.WithReasoningEffort(effort))
		}
		opts = append(opts, openai.WithProviderCapabilities(caps))
		// Prompt caching (ADR 0100): the dialect is a PURE (id, baseURL) gate,
		// never id alone — an operator can point "openai" at a non-canonical
		// compatible endpoint (vLLM/LiteLLM via --openai-base-url) that would
		// 400 on prompt_cache_retention or an unrecognised field. INSIDE the
		// closure (not the outer call site) so every per-session/heal re-mint
		// carries it, not just the initial build.
		opts = append(opts, openai.WithCacheDialect(cacheDialectFor(id, baseURL, cfg)))
		opts = append(opts, extra...)
		var llm port.LLMProvider = openai.New(opts...)
		return llmresilience.Wrap(llm, llmresilience.Config{
			MaxAttempts:       cfg.LLMMaxAttempts,
			BaseBackoff:       llmBaseBackoff,
			MaxBackoff:        llmMaxBackoff,
			PerAttemptTimeout: cfg.LLMPerAttemptTimeout,
			StreamIdleTimeout: cfg.LLMStreamIdleTimeout,
			BreakerThreshold:  cfg.LLMBreakerThreshold,
			BreakerCooldown:   cfg.LLMBreakerCooldown,
			// Provider-tag every resilience line so a multi-provider operator can tell
			// WHICH provider stalled/opened its breaker (the lines themselves carry no
			// provider identity otherwise).
			Diagnostics: cfg.diag().With("provider", id),
		})
	}
	// The OPERATOR-DEFAULT effort baked into the shared .provider: normalise +
	// per-provider clamp (xhigh/max→high for openai), narrating a clamp at startup so
	// an operator who set --reasoning-effort max against OpenAI sees the promised WARN.
	// A per-session selector that resolves to a DIFFERENT effort OR capability
	// intersection re-mints via remint; the default path reuses .provider byte-for-byte.
	// defaultCaps is computed AFTER the registry is assembled (it needs the live-meta
	// store + catalog) — see buildProviderRegistry's post-assembly fixup. Until then
	// the shared .provider is built with the adapter's static transmit caps (no Parts
	// path fires pre-fixup since no tool produces Parts at build time).
	llm := construct(operatorDefaultEffortFor(cfg, id), openaiStaticCaps)
	cfg.diag().Log(context.Background(), port.LevelInfo, "LLM resilience enabled",
		"provider", id,
		"max_attempts", cfg.LLMMaxAttempts,
		"per_attempt_timeout", cfg.LLMPerAttemptTimeout,
		"stream_idle_timeout", cfg.LLMStreamIdleTimeout,
		"breaker_threshold", cfg.LLMBreakerThreshold,
		"breaker_cooldown", cfg.LLMBreakerCooldown)
	return providerEntry{id: id, provider: llm, available: true, baseURL: baseURL, remint: construct}
}

// newOpenCodeEntry constructs a resilience-wrapped OpenCode Go provider entry
// over the native Chat Completions adapter (openaichat). It is the Chat
// Completions sibling of newOpenAICompatEntry (Responses): SAME construct/remint
// discipline (the shared .provider is construct(defaultEffort), the per-session
// re-mint is construct(sessionEffort), so resilience wrapping cannot drift), SAME
// providerConstructor test seam. It differs in two ways: the adapter is
// openaichat.New (Chat Completions), and the remint's caps argument is IGNORED —
// openaichat has no per-model modality gating (all its models are text+image via
// static caps), so a caps-driven re-mint would rebuild an identical adapter; only
// the reasoning-effort axis re-mints meaningfully.
func newOpenCodeEntry(cfg Config, id, key, baseURL string) providerEntry {
	cfg.diag().Log(context.Background(), port.LevelInfo, "LLM provider available", "provider", id, "model", cfg.Model, "base_url", baseURL)
	if cfg.providerConstructor != nil {
		return providerEntry{id: id, provider: cfg.providerConstructor(cfg, id, key, baseURL), available: true, baseURL: baseURL}
	}
	construct := func(effort string, _ port.ProviderCapabilities) port.LLMProvider {
		opts := []openaichat.Option{openaichat.WithAPIKey(key)}
		if baseURL != "" {
			opts = append(opts, openaichat.WithBaseURL(baseURL))
		}
		if effort != "" {
			opts = append(opts, openaichat.WithReasoningEffort(effort))
		}
		// Prompt caching (ADR 0100): dormant today (id is always providerOpenCode
		// here, which never matches the OpenAI gate — see
		// openaichatCacheDialectFor), but wired inside the closure so a future
		// OpenAI-over-Chat-Completions entry gets it for free on every
		// per-session/heal re-mint.
		opts = append(opts, openaichat.WithCacheDialect(openaichatCacheDialectFor(id, baseURL, cfg)))
		var llm port.LLMProvider = openaichat.New(opts...)
		return llmresilience.Wrap(llm, llmresilience.Config{
			MaxAttempts:       cfg.LLMMaxAttempts,
			BaseBackoff:       llmBaseBackoff,
			MaxBackoff:        llmMaxBackoff,
			PerAttemptTimeout: cfg.LLMPerAttemptTimeout,
			StreamIdleTimeout: cfg.LLMStreamIdleTimeout,
			BreakerThreshold:  cfg.LLMBreakerThreshold,
			BreakerCooldown:   cfg.LLMBreakerCooldown,
			Diagnostics:       cfg.diag().With("provider", id),
		})
	}
	llm := construct(operatorDefaultEffortFor(cfg, id), opencodeStaticCaps)
	cfg.diag().Log(context.Background(), port.LevelInfo, "LLM resilience enabled",
		"provider", id,
		"max_attempts", cfg.LLMMaxAttempts,
		"per_attempt_timeout", cfg.LLMPerAttemptTimeout,
		"stream_idle_timeout", cfg.LLMStreamIdleTimeout,
		"breaker_threshold", cfg.LLMBreakerThreshold,
		"breaker_cooldown", cfg.LLMBreakerCooldown)
	return providerEntry{id: id, provider: llm, available: true, baseURL: baseURL, remint: construct}
}

// entry. It honors the SAME composition-only providerConstructor test seam first
// (so the offline multi-provider e2e can back "anthropic" with a mock), else
// constructs the anthropic adapter with WithMaxTokens set to the default model's
// catalogued output limit (Anthropic REQUIRES max_tokens and rejects a value
// above the model's ceiling; a conservative fallback applies when uncatalogued).
// It logs the provider id, default model, and base URL ONLY — never the key. No
// lister in P1 (live Anthropic listing is deferred).
func newAnthropicEntry(cfg Config, key string, meta *liveMetaStore) providerEntry {
	baseURL := cfg.AnthropicBaseURL
	cfg.diag().Log(context.Background(), port.LevelInfo, "LLM provider available", "provider", providerAnthropic, "model", cfg.Model, "base_url", baseURL)
	if cfg.providerConstructor != nil {
		entry := providerEntry{
			id:        providerAnthropic,
			provider:  cfg.providerConstructor(cfg, providerAnthropic, key, baseURL),
			available: true,
			baseURL:   baseURL,
		}
		entry.lister = anthropicLister{inner: anthropic.NewLister(key, baseURL, cfg.liveModelHTTPClient)}
		return entry
	}
	// normaliseAnthropicCacheTTL is called ONCE here (a build-time, not a
	// per-remint, call site — mirrors the operatorDefaultEffortFor discipline
	// above) so an unrecognised --anthropic-cache-ttl value WARNs at most once
	// per process, never once per session/heal re-mint.
	cacheTTL := normaliseAnthropicCacheTTL(cfg)
	// construct mints a resilience-wrapped anthropic adapter carrying the given
	// reasoning-effort token (ADR 0055) and per-session capability intersection
	// (T7), over the SAME max-tokens + thinking resolvers. It is the SINGLE
	// construction path: the default .provider is construct(defaultEffort,
	// defaultCaps) and the per-session re-mint is construct(sessionEffort,
	// sessionCaps), so the two cannot drift on resolvers or resilience wrapping.
	construct := func(effort string, caps port.ProviderCapabilities) port.LLMProvider {
		opts := []anthropic.Option{
			anthropic.WithAPIKey(key),
			// PER-MODEL max_tokens: each request's max_tokens is resolved LIVE-FIRST from
			// the live-metadata store (the live output ceiling when present), else the
			// catalogued ceiling, else the adapter's conservative default. So a per-session/
			// sub-agent route to a smaller-ceiling model (e.g. claude-3-5-haiku=8192) never
			// sends the default model's larger value and 400s, and a newly-released model the
			// catalog doesn't know gets its true ceiling from the live API. The adapter stays
			// catalog-/store-free — composition owns the closure over meta.
			anthropic.WithMaxTokensResolver(func(model string) int {
				return meta.outputLimitFor(providerAnthropic, model)
			}),
			// THINKING-FROM-LIVE: the adapter's extended-thinking mode (adaptive / manual /
			// none) reads the LIVE descriptor (Capabilities.Thinking.Types) when the model is
			// KNOWN, falling back to the adapter's hardcoded prefix matrix as the OFFLINE
			// floor (known=false). This retires the stale-prefix guesswork for live runs
			// while keeping the deterministic matrix for offline/uncatalogued models.
			anthropic.WithThinkingResolver(func(model string) (adaptive, enabled, known bool) {
				return meta.thinkingFor(providerAnthropic, model)
			}),
			anthropic.WithProviderCapabilities(caps),
			// Prompt caching (ADR 0100): --no-prompt-cache disables the three NEW
			// conversation breakpoints (the pre-existing StablePrefix breakpoint is
			// unaffected); --anthropic-cache-ttl (normalised once, above) stamps a
			// uniform TTL across every marker the adapter emits. INSIDE the closure
			// so every per-session/heal re-mint carries both.
			anthropic.WithConversationCaching(!cfg.PromptCacheDisabled),
		}
		if cacheTTL != "" {
			opts = append(opts, anthropic.WithCacheTTL(cacheTTL))
		}
		// Reasoning effort (ADR 0055) is INDEPENDENT of the thinking config above; both
		// coexist on the request. Anthropic identity-maps the neutral vocabulary.
		if effort != "" {
			opts = append(opts, anthropic.WithReasoningEffort(effort))
		}
		if baseURL != "" {
			opts = append(opts, anthropic.WithBaseURL(baseURL))
		}
		var llm port.LLMProvider = anthropic.New(opts...)
		return llmresilience.Wrap(llm, llmresilience.Config{
			MaxAttempts:       cfg.LLMMaxAttempts,
			BaseBackoff:       llmBaseBackoff,
			MaxBackoff:        llmMaxBackoff,
			PerAttemptTimeout: cfg.LLMPerAttemptTimeout,
			StreamIdleTimeout: cfg.LLMStreamIdleTimeout,
			BreakerThreshold:  cfg.LLMBreakerThreshold,
			BreakerCooldown:   cfg.LLMBreakerCooldown,
			// Provider-tag every resilience line (see the openai entry).
			Diagnostics: cfg.diag().With("provider", providerAnthropic),
		})
	}
	// The OPERATOR-DEFAULT effort baked into the shared .provider (anthropic
	// identity-maps all five tiers, so this never clamps — but it shares the one
	// startup-clamp-narration helper for uniformity with the openai entry). The
	// default-model capability intersection is stamped by buildProviderRegistry's
	// post-assembly fixup (it needs the assembled registry + meta); until then the
	// shared .provider carries the adapter's static transmit caps.
	llm := construct(operatorDefaultEffortFor(cfg, providerAnthropic), anthropicStaticCaps)
	cfg.diag().Log(context.Background(), port.LevelInfo, "LLM resilience enabled",
		"provider", providerAnthropic,
		"max_attempts", cfg.LLMMaxAttempts,
		"per_attempt_timeout", cfg.LLMPerAttemptTimeout,
		"stream_idle_timeout", cfg.LLMStreamIdleTimeout,
		"breaker_threshold", cfg.LLMBreakerThreshold,
		"breaker_cooldown", cfg.LLMBreakerCooldown)
	entry := providerEntry{id: providerAnthropic, provider: llm, available: true, baseURL: baseURL, remint: construct}
	// Anthropic opts into LIVE model listing: its keyed /v1/models endpoint
	// self-describes the rich per-model metadata (output ceiling, context window,
	// image, thinking types). The lister rides on the entry (so only anthropic
	// advertises it) and carries the key for a READ-ONLY metadata GET — it is
	// availability-gated by construction (this code runs only when the key resolved)
	// and never logs the key. The HTTP client is the composition test seam (nil ⇒
	// default client; tests inject a mock transport).
	entry.lister = anthropicLister{inner: anthropic.NewLister(key, baseURL, cfg.liveModelHTTPClient)}
	return entry
}

// anthropicOutputLimit returns the catalogued output ceiling for a SPECIFIC
// anthropic model id (the per-request max_tokens resolver the adapter calls with
// req.Model). Returns 0 for an uncatalogued model, so the adapter falls back to
// its conservative constant (the lowest common Claude ceiling) and never 400s on
// a too-high value. Composition-only catalog read; keeps the adapter catalog-free.
func anthropicOutputLimit(model string) int {
	p, ok := providercatalog.Default().Provider(providerAnthropic)
	if !ok {
		return 0
	}
	for _, m := range p.Models() {
		if m.ID() == model {
			return m.OutputLimit()
		}
	}
	return 0
}

// resolveDefaultModel resolves the default (providerID, modelID) pair from cfg and
// the registry. Precedence (per the Phase-0 brief):
//
//  1. --model flag (cfg.Model) — an EXPLICIT operator override (non-empty): a bare
//     string paired with the default provider id; the catalog-aware "which provider
//     owns this model" resolution is deferred (S-later).
//  2. server-configured deployment-wide default (cfg.DefaultModel, --default-model;
//     issue #21) — the default model for the resolved default provider, shared by
//     every client; validated FAIL-FAST at Build (validateDefaultModel), so here
//     it is taken verbatim.
//  3. client last-used state — S4 (client-side), not the server.
//  4. per-provider default model — when neither (1) nor (2) is set, the
//     builtinDefaultModel table entry for the default provider (e.g. openai =>
//     "gpt-5", openrouter => "openai/gpt-5"). A provider absent from the table
//     yields "" — the adapter/endpoint default — the safe fallback for a future
//     provider.
//
// The provider preference among available providers is openai first (back-compat
// with the single-provider default), then the established keyed-provider order,
// then openai-codex, then intent-driven gateways — overridden by a
// configured cfg.DefaultProvider (--default-provider) when that provider is
// AVAILABLE. The unavailable case is caught fail-fast by validateDefaultModel at
// Build; this resolver stays total/non-erroring (a hand-built registry or a
// pre-validation call simply keeps the preference). When cfg.DefaultModel is set
// WITHOUT cfg.DefaultProvider, the configured model applies to the preferred
// default provider — the validator confirmed it is catalogued for THAT provider,
// keeping the pair coherent.
func resolveDefaultModel(cfg Config, reg *providerRegistry) (providerID, modelID string) {
	defID := preferredDefaultProvider(reg)
	if cfg.DefaultProvider != "" {
		if _, ok := reg.Lookup(cfg.DefaultProvider); ok {
			defID = cfg.DefaultProvider
		}
	}
	// (1) --model flag: an EXPLICIT override wins, paired with the default provider.
	if cfg.Model != "" {
		return defID, cfg.Model
	}
	// (2) server-configured deployment-wide default (--default-model).
	if cfg.DefaultModel != "" {
		return defID, cfg.DefaultModel
	}
	// (3) client last-used: S4.
	// (4) per-provider default model from the table (no entry => "" => endpoint default).
	return defID, builtinDefaultModel[defID]
}

// preferredDefaultProvider picks the default provider id from explicit tiers.
// OpenAI remains first. The providers that pre-date the manual subscription route
// retain their former sorted order (anthropic, opencode, openrouter), followed by
// openai-codex. Any future/hand-built key-driven entry remains above an
// intent-driven gateway. Intent-driven providers stay the lowest tier, so a
// ToolHive-only, zero-API-key operator still gets a usable default.
func preferredDefaultProvider(reg *providerRegistry) string {
	if _, ok := reg.Lookup(providerOpenAI); ok {
		return providerOpenAI
	}
	for _, id := range []string{providerAnthropic, providerOpenCode, providerOpenRouter} {
		if entry, ok := reg.Lookup(id); ok && !entry.intentDriven {
			return id
		}
	}
	if entry, ok := reg.Lookup(providerOpenAICodex); ok && !entry.intentDriven {
		return providerOpenAICodex
	}
	var firstIntentDriven string
	for _, id := range reg.Available() { // sorted
		if id == providerOpenAI || id == providerAnthropic || id == providerOpenCode ||
			id == providerOpenRouter || id == providerOpenAICodex {
			continue
		}
		entry, ok := reg.Lookup(id)
		if !ok {
			continue
		}
		if !entry.intentDriven {
			return id // first key-driven provider, sorted order
		}
		if firstIntentDriven == "" {
			firstIntentDriven = id
		}
	}
	return firstIntentDriven // "" when reg.Available() is empty too
}

// toolhiveRoutingMode is the resolved routing mode for a toolhive registry
// entry (issue #265): proxy routes through the LOCAL loopback reverse proxy
// (today's behaviour); direct talks to the real gateway_url with an in-process
// OIDC token. The zero value is proxy (the byte-identical default for every
// config that predates direct mode).
type toolhiveRoutingMode int

const (
	toolhiveModeProxy  toolhiveRoutingMode = 0 // loopback reverse proxy (today's behaviour)
	toolhiveModeDirect toolhiveRoutingMode = 1 // gateway_url + in-process OIDC token
)

// toolhiveIntent is the resolved toolhive registration intent: whether to
// register, and — when registering — the routing mode + the URLs
// newGatewayEntry/newDirectGatewayEntry consume. baseURL is the request URL
// (loopback for proxy, gateway_url+"/v1" for direct); gatewayURL is the
// upstream the proxy forwards to (DIAGNOSTIC ONLY for proxy, the SAME as
// baseURL's origin for direct); explicit marks an --toolhive-llm-base-url
// override (proxy-only — direct is config-driven).
type toolhiveIntent struct {
	mode           toolhiveRoutingMode
	baseURL        string
	gatewayURL     string
	explicit       bool
	oidcConfigured bool // F3: loaded once after DetectConfig, used by newDirectGatewayEntry
}

// resolveToolhiveIntent decides whether a "toolhive" registry entry should be
// registered (issue #262, D1) and, if so, what routing mode + URLs to serve it
// on (issue #265). It NEVER runs a network probe — registration is intent-only,
// and the intent it returns feeds newGatewayEntry/newDirectGatewayEntry, which
// the LATER Build-time probe (probeToolhive) reads off the constructed entry.
//
// Precedence: an EXPLICIT cfg.ToolhiveLLMBaseURL (already loopback-validated
// by validateToolhiveBaseURL at Build) wins outright and SKIPS the config-file
// read entirely (gatewayURL="" — there is no upstream to show; explicit=true
// so the probe upgrades an unreachable diagnostic to WARN, since the operator
// asked for this exact endpoint). The explicit override is ALWAYS proxy mode:
// it is a loopback proxy address, and direct mode derives its base URL from
// the config file's gateway_url (there is no gateway_url to derive from an
// explicit loopback override), so --toolhive-llm-mode is IGNORED on this path
// (the override is the documented proxy escape hatch). Otherwise, when
// cfg.ToolhiveLLM is set (the default), toolhivellm.DetectConfig reads
// ToolHive's own config file; a miss logs ONE DEBUG diagnostic and returns
// ok=false — never a WARN/ERROR (most operators simply do not run ToolHive,
// and detection failure is always fail-soft, R1.1). cfg.ToolhiveLLM=false
// skips detection entirely (zero file stats), the explicit shared-host opt-out
// (R4.1).
//
// Routing mode (when the config-file path is taken): cfg.ToolhiveLLMMode drives
// the discriminator — auto (the default) selects direct when the OIDC trio
// (gateway_url + issuer + client_id) is configured (toolhivellm.OIDCConfigured)
// and falls back to proxy otherwise (today's byte-identical behaviour when
// OIDC is absent); proxy forces the loopback path regardless of OIDC; direct
// forces the gateway_url path. direct + !IsConfigured() is NOT a fail-soft
// miss here — it is a loud Build-fail (resolveDirectIntentError) so an
// operator who asked for direct against an unconfigured gateway sees the
// exact missing fields, not a silent fallback to proxy.
func resolveToolhiveIntent(cfg Config) (toolhiveIntent, bool) {
	if cfg.ToolhiveLLMBaseURL != "" {
		// Explicit loopback override: always proxy. Direct mode is
		// config-driven (it needs gateway_url); the override is the proxy
		// escape hatch, so --toolhive-llm-mode does not apply here.
		return toolhiveIntent{mode: toolhiveModeProxy, baseURL: cfg.ToolhiveLLMBaseURL, explicit: true}, true
	}
	if !cfg.ToolhiveLLM {
		return toolhiveIntent{}, false
	}
	path := cfg.toolhiveConfigPath
	if path == "" {
		// os.UserConfigDir(), not xdgconfig.UserConfigDir(): ToolHive's own
		// config.go resolves its path via github.com/adrg/xdg, which (like
		// stdlib os.UserConfigDir) maps XDG concepts to native per-OS
		// locations (~/Library/Application Support on macOS, %AppData% on
		// Windows, $XDG_CONFIG_HOME/~/.config on Unix) rather than the
		// literal Linux XDG spec every other mecatl adapter wants via
		// xdgconfig. Using xdgconfig here silently misses ToolHive's real
		// config file on macOS/Windows.
		if dir, err := os.UserConfigDir(); err == nil && dir != "" {
			path = filepath.Join(dir, toolhivellm.DefaultConfigRelPath)
		}
	}
	detected, found := toolhivellm.DetectConfig(path)
	if !found {
		cfg.diag().Log(context.Background(), port.LevelDebug,
			"toolhive LLM gateway: no config detected, skipping auto-registration", "path", path)
		return toolhiveIntent{}, false
	}
	// F3: load the OIDC config ONCE after DetectConfig validates the file, so
	// newDirectGatewayEntry consumes the validated result rather than re-reading.
	oidcOK := toolhivellm.OIDCConfigured(path)

	// Mode discriminator (issue #265). auto upgrades to direct when the OIDC
	// trio is configured; proxy is the byte-identical fallback. direct is the
	// explicit opt-in; a direct ask against an unconfigured gateway is a loud
	// Build-fail (NOT a silent proxy fallback — the operator asked for direct
	// and would be surprised by a loopback that has no token to inject).
	switch cfg.ToolhiveLLMMode {
	case "proxy":
		return toolhiveIntent{mode: toolhiveModeProxy, baseURL: detected.BaseURL(), gatewayURL: detected.GatewayURL}, true
	case "direct":
		if !oidcOK {
			// Not a fail-soft miss: validateToolhiveLLMMode (Build) already
			// fail-closed on direct + !IsConfigured() with the actionable
			// error naming the missing fields. Reaching here means the
			// validator was bypassed (e.g. a test calling
			// resolveToolhiveIntent directly); fall back to no registration
			// rather than a silent proxy.
			return toolhiveIntent{}, false
		}
		if !gatewayURLIsHTTPS(detected.GatewayURL) {
			cfg.diag().Log(context.Background(), port.LevelWarn,
				"toolhive direct mode: gateway_url is not HTTPS, falling back to proxy mode",
				"gateway_url", detected.GatewayURL)
			return toolhiveIntent{mode: toolhiveModeProxy, baseURL: detected.BaseURL(), gatewayURL: detected.GatewayURL}, true
		}
		return toolhiveIntent{mode: toolhiveModeDirect, baseURL: directBaseURL(detected.GatewayURL), gatewayURL: detected.GatewayURL, oidcConfigured: true}, true
	default: // "auto" (and any unknown, treated as the default)
		if oidcOK {
			if !gatewayURLIsHTTPS(detected.GatewayURL) {
				cfg.diag().Log(context.Background(), port.LevelWarn,
					"toolhive direct mode: gateway_url is not HTTPS, falling back to proxy mode",
					"gateway_url", detected.GatewayURL)
				return toolhiveIntent{mode: toolhiveModeProxy, baseURL: detected.BaseURL(), gatewayURL: detected.GatewayURL}, true
			}
			return toolhiveIntent{mode: toolhiveModeDirect, baseURL: directBaseURL(detected.GatewayURL), gatewayURL: detected.GatewayURL, oidcConfigured: true}, true
		}
		return toolhiveIntent{mode: toolhiveModeProxy, baseURL: detected.BaseURL(), gatewayURL: detected.GatewayURL}, true
	}
}

// gatewayURLIsHTTPS reports whether raw is safe for direct-mode token injection:
// the scheme must be https unless the HOST itself is loopback (localhost,
// 127.0.0.1 or ::1, any port) — the explicit local-development carve-out. A
// non-HTTPS gateway_url would send the OIDC bearer token over cleartext
// (CWE-319).
//
// It compares the parsed HOST, never a string prefix: "http://localhost" is a
// prefix of the publicly-resolvable "http://localhost.attacker.com", so a
// prefix test would approve cleartext bearer traffic to an attacker-controlled
// host — the exact thing this gate exists to forbid. The scheme is lowercased
// before comparison because a URI scheme is case-insensitive (RFC 3986 §3.1),
// so "HTTPS://gw" must not silently downgrade to proxy mode. url.Hostname()
// strips the port and the IPv6 brackets, so "[::1]:8080" needs no special case.
// An unparseable gateway_url fails CLOSED (proxy fallback).
func gatewayURLIsHTTPS(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	switch strings.ToLower(u.Scheme) {
	case "https":
		return true
	case "http":
		switch u.Hostname() {
		case "localhost", "127.0.0.1", "::1":
			return true
		}
	}
	return false
}

// directBaseURL derives the request base URL for direct mode from the config's
// gateway_url: gateway_url + "/v1". It mirrors what the ToolHive proxy forwards
// (pkg/llm/proxy/proxy.go sets the upstream URL verbatim and forwards every
// path, so the gateway serves /v1/models at gateway_url + /v1/models). A
// trailing slash on gateway_url is normalized so "https://gw/v1" never becomes
// "https://gw//v1". It is the ONE derivation of a request URL from gateway_url
// (the proxy mode NEVER does this — its base URL is the hardcoded loopback).
//
// It joins through net/url rather than concatenating strings: concatenation
// appends the segment to whatever the string ends with, so a gateway_url
// carrying a query or fragment ("https://gw?x=1") produced the nonsense
// "https://gw?x=1/v1", with the segment swallowed into the query value. JoinPath
// appends to the PATH and leaves the query where it belongs. An unparseable
// gateway_url yields "" (the same contract as the empty input); in practice
// gatewayURLIsHTTPS has already rejected it and forced proxy mode.
func directBaseURL(gatewayURL string) string {
	if gatewayURL == "" {
		return ""
	}
	joined, err := url.JoinPath(gatewayURL, "v1")
	if err != nil {
		return ""
	}
	return joined
}

// ToolhiveAvailable reports whether a ToolHive LLM gateway would be registered
// for this Config — an explicit --toolhive-llm-base-url, or a detected
// locally-running proxy. It runs the SAME resolveToolhiveIntent detection Build
// uses (one source of truth), so a client-side provider pre-check (mecatui's
// config.validate) agrees with what Build will actually resolve.
func ToolhiveAvailable(cfg Config) bool {
	_, ok := resolveToolhiveIntent(cfg)
	return ok
}

// newGatewayEntry mints the toolhive registry entry: it delegates to
// newOpenAICompatEntry (the SAME construction/resilience path as openai and
// openrouter — the two cannot drift) using toolhivellm.PlaceholderToken as
// the credential (the ToolHive LLM gateway proxy's documented inbound-auth
// convention), then stamps the intent-driven metadata (R2.1/R2.6/R6.2) and
// the live lister. It ALSO wires a redirect-refusing HTTP client onto the
// INFERENCE request path (F3/CWE-918): the listing probe already refuses
// redirects via openaicompat.RefuseRedirects, but the request that carries
// the actual conversation body + Authorization header rides the openai
// adapter's own client, which by SDK default follows up to 10 redirects — a
// hostile/misconfigured listener squatting the loopback port could otherwise
// bounce that body off-loopback. Only the gateway entry gets this: openai
// and openrouter talk to a real, TLS-terminated, non-loopback endpoint where
// following a redirect is ordinary and expected.
func newGatewayEntry(cfg Config, id, baseURL, gatewayURL string, explicit bool, lister modelLister) providerEntry {
	entry := newOpenAICompatEntry(cfg, id, toolhivellm.PlaceholderToken, baseURL,
		openai.WithHTTPClient(&http.Client{CheckRedirect: openaicompat.RefuseRedirects}))
	entry.lister = lister
	entry.intentDriven = true
	entry.intentGatewayURL = gatewayURL
	entry.intentExplicit = explicit
	return entry
}

// newDirectGatewayEntry mints the DIRECT-mode toolhive registry entry (issue
// #265): it talks to the real gateway_url (no local proxy hop) with an
// in-process OIDC access token injected by a bearer RoundTripper. It reuses
// newOpenAICompatEntry (the SAME construction/resilience path as the proxy
// entry) with a placeholder key — the openai-go SDK stamps
// `Authorization: Bearer <placeholder>` on every request BEFORE the
// *http.Client.Transport fires, so the bearerRoundTripper STRIPS that header
// and sets `Bearer <real-token>` per request (mirroring the ToolHive proxy's
// own Rewrite: pkg/llm/proxy/proxy.go Del + Set). The placeholder is never sent
// on the wire.
//
// The HTTP client composes TWO policies: the bearerRoundTripper (token
// injection) over the SDK's default transport, AND RefuseRedirects
// (CWE-918 — the gateway_url is an operator-configured HTTPS endpoint, but a
// redirect must never bounce the conversation body + bearer off the intended
// host). Both ride every per-session/heal re-mint: newOpenAICompatEntry closes
// `extra` into the construct() closure so the WithHTTPClient option is appended
// to every mint, not just the initial build (zero drift, the same property the
// proxy entry relies on).
//
// The token source is built ONCE here (toolhivellm.DirectTokenSource) and
// captured by the RoundTripper; a per-request Token(ctx) call handles refresh
// internally, so the RoundTripper is stateless across requests. A construction
// failure (config unreadable, secrets provider unavailable) does NOT fail Build:
// it logs ERROR once and installs a token source that returns the cause on every
// request, so the operator learns the reason at the first request instead of a
// startup crash (the proxy-mode §1 deviation — a down gateway must never brick
// Build — applies here too). The live lister is
// wired too (direct mode serves /v1/models the same way), authenticated by the
// SAME bearer RoundTripper so the probe and inference paths share one
// credential.
func newDirectGatewayEntry(cfg Config, id string, intent toolhiveIntent, configPath string) providerEntry {
	tokenSource, err := toolhivellm.DirectTokenSource(configPath, cfg.diag())
	if err != nil {
		// Deliberately NOT a Build failure. The caller
		// (buildProviderRegistry) cannot see this error anyway
		// (newOpenAICompatEntry has no error return), and the proxy-mode §1
		// deviation applies here too: a down or unconfigured gateway must never
		// brick Build. So log the cause ONCE at ERROR and install a token source
		// that returns it on every call — the operator reads "no cached
		// credential" at the first request rather than eating a startup crash,
		// and every other provider in the registry stays usable.
		cfg.diag().Log(context.Background(), port.LevelError,
			"toolhive direct-mode token source unavailable — requests will fail until the gateway is configured",
			"provider", id, "base_url", intent.baseURL, "error", err.Error())
		tokenSource = func(context.Context) (string, error) { return "", err }
	}
	rt := &bearerRoundTripper{base: http.DefaultTransport, token: tokenSource}
	client := &http.Client{
		Transport:     rt,
		CheckRedirect: openaicompat.RefuseRedirects,
	}
	entry := newOpenAICompatEntry(cfg, id, toolhivellm.PlaceholderToken, intent.baseURL,
		openai.WithHTTPClient(client),
		openai.WithMaxRetries(0))
	// The direct-mode lister shares the SAME bearer-authenticated client so
	// the Build-time probe (probeToolhive) and the live refresh authenticate
	// against the gateway with the real token, not the placeholder. The lister
	// stamps `Bearer <placeholder>` itself; the bearerRoundTripper strips it
	// and sets the real token, exactly as it does for inference requests.
	entry.lister = gatewayLister{inner: openaicompat.NewLister(intent.baseURL, toolhivellm.PlaceholderToken, client)}
	entry.intentDriven = true
	entry.intentGatewayURL = intent.gatewayURL
	entry.intentExplicit = intent.explicit
	return entry
}

// bearerRoundTripper injects a fresh OIDC access token onto every request as
// `Authorization: Bearer <token>`, stripping any Authorization header the SDK
// stamped before the transport fires (the openai-go SDK sets a placeholder
// `Bearer <key>` via SetAPIKey before the *http.Client.Transport RoundTripper
// sees the request — so this MUST Del then Set, exactly like the ToolHive
// proxy's Rewrite). It NEVER logs the request, the Authorization header, or
// the token value; an error from the token source is returned as a terminal
// transport error (no retry — the token source handles refresh internally,
// and a genuine failure like ErrTokenRequired is not retryable at the
// transport layer).
//
// SECURITY: the token lives in the OS keyring (pkg/secrets), accessed
// in-process; it NEVER enters a log, an error string, or an environment
// variable. Errors are already sanitised by toolhivellm.DirectTokenSource
// (llm.SanitizeTokenError strips any bearer material an IdP echoes back).
type bearerRoundTripper struct {
	base  http.RoundTripper
	token toolhivellm.TokenSourceFunc
}

// RoundTrip implements http.RoundTripper. It is the single point the token
// touches the wire. On a token-source error it returns the error WITHOUT
// forwarding the request (no partial credentials on the wire); the
// llmresilience wrapper surfaces it as a failed attempt. It must never log.
func (b *bearerRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	tok, err := b.token(req.Context())
	if err != nil {
		// The error is already sanitised (no bearer material). Do NOT include
		// the token, the URL's query, or req.Header — return the error as-is.
		return nil, err
	}
	// Clone the request per RoundTripper contract (the caller may reuse it);
	// mutate ONLY the Authorization header on the clone.
	clone := req.Clone(req.Context())
	clone.Header.Del("Authorization")
	clone.Header.Set("Authorization", "Bearer "+tok)
	return b.base.RoundTrip(clone)
}

// toolhiveProbeTimeout bounds the Build-time probe (R1.2): a down/hanging
// proxy must never slow down Build's own startup beyond a bounded window.
const toolhiveProbeTimeout = 1500 * time.Millisecond

// The v1 provider_status states (STRING passthroughs on the wire, no proto
// enum — the EvNoProgress/StopBudget discipline).
const (
	statusOK           = "ok"
	statusUnreachable  = "unreachable"
	statusUnauthorized = "unauthorized"
	statusEmpty        = "empty"
)

// toolhiveStatusHints is the ONE place the remediation-hint copy lives,
// shared by the Build-time probe diagnostics, the v1 provider_status
// projection (providerStatusProto), and (verbatim) docs/usage.md's
// troubleshooting table — so the three surfaces cannot drift on wording.
var toolhiveStatusHints = map[string]string{
	statusUnreachable:  "start it with `thv llm proxy start`",
	statusUnauthorized: "re-auth with `thv llm setup`",
	statusEmpty:        "your ToolHive gateway credential lists no models — ask your platform admin or re-run `thv llm setup`",
}

// openAICodexStatusHints keeps manual-token remediation distinct from the
// ToolHive gateway. Codex is the only non-intent-driven provider whose live
// inventory is also the account entitlement boundary, so its listing outcome
// is operator-actionable and is projected through provider_status.
var openAICodexStatusHints = map[string]string{
	statusUnreachable:  "check connectivity to chatgpt.com and retry",
	statusUnauthorized: "replace the manual token in auth.yaml and restart mecatl",
	statusEmpty:        "the ChatGPT account lists no selectable Codex models; replace the manual token or check the subscription",
}

// statusHintFor returns provider-specific remediation only for providers whose
// listing outcome is operator-actionable on provider_status. Ordinary provider
// outages (for example OpenRouter) get "". Keep each vendor's copy in its own
// table so gateway and manual-token remedies cannot cross-contaminate.
func statusHintFor(pid, state string) string {
	switch pid {
	case providerToolhive:
		return toolhiveStatusHints[state]
	case providerOpenAICodex:
		return openAICodexStatusHints[state]
	default:
		return ""
	}
}

// errToolhiveNoModels is the actionable Build-fail error (D2 R2.3): toolhive
// is the registry's SOLE/DEFAULT provider, the probe succeeded, but the
// credential lists ZERO models — there is nothing sensible to default to, and
// staying silent would leave the operator stuck with an empty picker and no
// explanation. Kept lowercase with no trailing punctuation (ST1005), mirroring
// errNoProvider.
var errToolhiveNoModels = errors.New(
	"your ToolHive gateway credential lists no models — ask your platform admin or re-run `thv llm setup`")

// classifyLiveListError maps ANY provider's live-listing failure to a v1
// provider_status state (it classifies every resolveProviderModels failure,
// not just toolhive's — the name used to say "Toolhive" back when it was
// probeToolhive-only, but it is now the SHARED classifier resolveProviderModels
// calls for every provider with a lister): a 401/403 *openaicompat.StatusError
// is "unauthorized" (a stale/rejected credential); everything else (connection
// refused, timeout, malformed response, a 5xx) is "unreachable" (the endpoint
// is not up / not listening yet) — the common case for a ToolHive user who
// simply hasn't started the proxy.
func classifyLiveListError(err error) string {
	var statusErr interface{ StatusCode() int }
	if errors.As(err, &statusErr) && (statusErr.StatusCode() == http.StatusUnauthorized || statusErr.StatusCode() == http.StatusForbidden) {
		return statusUnauthorized
	}
	return statusUnreachable
}

// probeToolhive runs the BOUNDED (issue #262 R1.2, ≤1.5s) Build-time probe
// against a registered toolhive entry. Registration itself NEVER depends on
// this (resolveToolhiveIntent already ran unconditionally) — the probe drives
// ONLY: the startup diagnostic, the initial provider_status + last-known-good
// seed (via reg.outcomes), and default-model eligibility (D2). A nil/missing
// toolhive entry is a no-op (every non-ToolHive Build pays one map lookup).
//
// Returns errToolhiveNoModels ONLY when the probe succeeded, returned zero
// models, AND toolhive is the registry's resolved DEFAULT provider (R2.3) —
// every other outcome is diagnosed, never fatal (the §1 accepted deviation: a
// down/unauthorized proxy must never brick Build when toolhive is sole).
func probeToolhive(reg *providerRegistry, cfg Config) error {
	entry, ok := reg.Lookup(providerToolhive)
	if !ok || entry.lister == nil {
		return nil
	}
	diag := cfg.diag()
	ctx, cancel := context.WithTimeout(context.Background(), toolhiveProbeTimeout)
	defer cancel()

	models, err := entry.lister.ListModels(ctx)
	switch {
	case err != nil:
		state := classifyLiveListError(err)
		hint := toolhiveStatusHints[state]
		reg.outcomes.recordFailure(providerToolhive, state, hint)
		// An explicit --toolhive-llm-base-url is the operator asking directly for
		// THIS endpoint, so an unreachable probe is upgraded to WARN (never
		// silent on a deliberate ask); an unauthorized credential is always a
		// WARN (a stale/rejected credential is actionable right now) regardless
		// of source.
		level := port.LevelInfo
		if entry.intentExplicit || state == statusUnauthorized {
			level = port.LevelWarn
		}
		diag.Log(ctx, level, "toolhive LLM gateway: probe failed — "+hint,
			"provider", providerToolhive, "base_url", entry.baseURL, "state", state)
	case len(models) == 0:
		reg.outcomes.recordSuccess(providerToolhive, nil)
		diag.Log(ctx, port.LevelWarn, "toolhive LLM gateway: "+toolhiveStatusHints[statusEmpty],
			"provider", providerToolhive, "base_url", entry.baseURL)
		if reg.defaultID == providerToolhive {
			return errToolhiveNoModels
		}
	default:
		// models is ALREADY []modelEntry (entry.lister is the composition
		// modelLister interface; the concrete gatewayLister already stamped
		// ToolCall:true per entry) — use it directly, never re-map it (that
		// would duplicate the ToolCall business rule in a second place).
		reg.outcomes.recordSuccess(providerToolhive, models)
		diag.Log(ctx, port.LevelInfo, "toolhive LLM gateway: registered and reachable",
			"provider", providerToolhive, "base_url", entry.baseURL, "gateway_url", entry.intentGatewayURL, "models", len(models))
		if reg.defaultID == providerToolhive && reg.defaultModel == "" {
			reg.defaultModel = models[0].ID
			reg.defaultModelAutoSelected = true // issue #262 review finding 7
			// Re-run the T7 caps fixup for the toolhive entry now that a real
			// default model is known — via the SAME shared remintEntry helper
			// buildProviderRegistry's post-assembly fixup loop and
			// healDefaultModel use (issue #262 review finding 4: the three
			// re-mint sites cannot drift on the caps/effort computation).
			reg.remintEntry(providerToolhive, reg.defaultModel)
			diag.Log(ctx, port.LevelInfo, "toolhive LLM gateway: default model (auto-selected)",
				"provider", providerToolhive, "model", reg.defaultModel,
				"base_url", entry.baseURL, "gateway_url", entry.intentGatewayURL)
		}
	}
	return nil
}
