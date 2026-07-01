package app

import (
	"context"
	"errors"
	"slices"
	"sort"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/internal/adapter/anthropic"
	"github.com/stacklok/mecatl/internal/adapter/llmresilience"
	"github.com/stacklok/mecatl/internal/adapter/openai"
	"github.com/stacklok/mecatl/internal/adapter/openrouter"
	"github.com/stacklok/mecatl/internal/adapter/providercatalog"
)

// Provider id strings. These are WIRE-STABLE once they reach the wire (S3's
// CreateSession provider_id field): they MUST match models.dev's provider ids
// so the S2 catalog join is a direct key lookup. Lowercase, never localized.
const (
	providerOpenAI     = "openai"
	providerOpenRouter = "openrouter"
	providerAnthropic  = "anthropic"
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
}

// openRouterDefaultBaseURL is the OpenRouter Responses-compatible API base URL.
// OpenRouter rides the SAME stateless openai adapter (it speaks the Responses
// API) with this base URL substituted — there is NO separate wire adapter in P0.
const openRouterDefaultBaseURL = "https://openrouter.ai/api/v1"

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
	id        string           // "openai", "openrouter", or "mock"
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
}

// Lookup returns the entry for id and whether it exists (and is therefore
// available — the registry only holds available entries).
func (r *providerRegistry) Lookup(id string) (providerEntry, bool) {
	e, ok := r.entries[id]
	return e, ok
}

// Available returns the available provider ids, sorted, for ListModels (S3) and
// the zero-keys diagnostic.
func (r *providerRegistry) Available() []string {
	ids := make([]string, 0, len(r.entries))
	for id := range r.entries {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// Default returns the default provider id, or "" when zero providers are
// available (the zero-keys case).
func (r *providerRegistry) Default() string { return r.defaultID }

// ResolvedDefaultModel returns the resolved EFFECTIVE default model for the
// default provider, or "" when no model resolved (the adapter/endpoint
// default): the explicit cfg.Model when set, else the server-configured
// cfg.DefaultModel, else the per-provider builtin table value (see
// resolveDefaultModel). Deliberately NOT named after Config.DefaultModel —
// that field is one TIER of this resolution (and under --model a value it
// lost to), not the same concept.
func (r *providerRegistry) ResolvedDefaultModel() string { return r.defaultModel }

// DefaultModelFor returns the builtin default model for a given provider id (the
// id the model string is VALID for), or "" when the provider has no table entry
// (the adapter/endpoint default). It is the single source the per-sub-agent
// provider resolver uses when a def switches provider but pins no model — a parent
// model string is for the PARENT's provider and may be invalid on the child's, so
// the child rebases off this provider-appropriate default rather than inheriting
// the parent model. Composition-only, like the rest of the registry.
func (*providerRegistry) DefaultModelFor(id string) string { return builtinDefaultModel[id] }

// errNoProvider is the named, actionable zero-keys error: when no provider's
// credentials resolved AND the mock is not selected, Build cannot serve a useful
// engine. The copy enumerates every accepted credential env var (per adapter), the
// compatible/proxy base-URL overrides (the "I have an endpoint but no public key"
// case), the offline --mock escape hatch, and a docs pointer, so first-run is
// self-explanatory. Keep it lowercase with NO trailing punctuation (ST1005);
// TestRegistryZeroKeys pins the load-bearing substrings.
var errNoProvider = errors.New(
	"no LLM provider available: set one of ANTHROPIC_API_KEY (Claude), " +
		"OPENAI_API_KEY (OpenAI), or OPENROUTER_API_KEY (one key, many models — a good first choice) " +
		"in the environment; for an OpenAI- or Anthropic-compatible/proxy endpoint pass the matching key " +
		"plus --openai-base-url / --anthropic-base-url / --openrouter-base-url; to try mecatl offline with " +
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
	if detect == nil {
		detect = osGetenv
	}

	// UseMock short-circuit: a single synthetic entry, offline, regardless of env.
	if cfg.UseMock {
		cfg.diag().Log(context.Background(), port.LevelWarn, "LLM provider: mock (canned, offline) — for smoke tests only")
		mock := mockllm.New(
			mockllm.TextTurn("Mock provider: no real model is configured. Set OPENAI_API_KEY for live use."),
		)
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
		entries[providerOpenAI] = newOpenAIEntry(cfg, providerOpenAI, key, cfg.OpenAIBaseURL)
	}

	// openrouter: same stateless openai adapter, OpenRouter base URL, keyed by
	// OPENROUTER_API_KEY (falling back to OPENAI_API_KEY by convention).
	if key := providerKey(cfg.OpenRouterKey, providerOpenRouter, detect); key != "" {
		baseURL := cfg.OpenRouterBaseURL
		if baseURL == "" {
			baseURL = openRouterDefaultBaseURL
		}
		entry := newOpenAIEntry(cfg, providerOpenRouter, key, baseURL)
		// OpenRouter opts into LIVE model listing: its public /models endpoint
		// enumerates the real catalog (336 models) vs the curated embedded subset.
		// The lister rides on the entry (NOT the shared openai.Provider) so openai —
		// which uses the same adapter — does NOT advertise live listing. The HTTP
		// client is the composition test seam (nil => default timeout client; tests
		// inject a mock transport). KEYLESS: the lister never receives the key.
		entry.lister = openRouterLister{inner: openrouter.NewLister(cfg.liveModelHTTPClient)}
		entries[providerOpenRouter] = entry
	}

	// anthropic: the native Messages-API adapter (NOT the openai adapter). AVAILABLE
	// iff a key resolves — from cfg.AnthropicKey (the cmd layer reads ANTHROPIC_API_KEY)
	// or the catalog-driven env vars via detect. Per-session routing, sub-agent
	// provider switch, and the capability intersection treat it as data (no change).
	if key := providerKey(cfg.AnthropicKey, providerAnthropic, detect); key != "" {
		entries[providerAnthropic] = newAnthropicEntry(cfg, key, meta)
	}

	if len(entries) == 0 {
		return nil, errNoProvider
	}

	reg := &providerRegistry{entries: entries, meta: meta}
	reg.defaultID, reg.defaultModel = resolveDefaultModel(cfg, reg)
	// Seed the live-metadata store from the embedded catalog for every available
	// provider — the t=0 floor every resolver reads before the background live swap.
	meta.seedFromCatalog(reg.Available())
	// T7 post-assembly fixup: stamp each real adapter entry's shared .provider
	// with the DEFAULT model's capability intersection (catalog ∩ adapter) and
	// record it as defaultCaps so the per-session factory can re-mint ONLY when a
	// session's resolved intersection differs (the byte-identical default path).
	// The entries were initially built with the adapter's STATIC transmit caps
	// (the registry wasn't assembled yet, so modelCapability couldn't run); this
	// re-mint replaces that placeholder with the honest default-model intersection.
	// Mock / providerConstructor-seam entries have no remint closure and are
	// skipped (they ignore caps; defaultCaps stays zero = "match anything").
	for id, entry := range entries {
		if entry.remint == nil {
			continue
		}
		// The shared .provider serves the operator-default model for the DEFAULT
		// provider (reg.defaultModel — the resolved cfg.Model), and the provider's
		// builtin default for a non-default provider (its .provider is only ever a
		// re-mint base for a session that selects it, so the placeholder model's
		// caps are immediately replaced by the session's).
		model := reg.DefaultModelFor(id)
		if id == reg.defaultID {
			model = reg.defaultModel
		}
		defCaps := modelCapability(reg, id, model)
		entry.provider = entry.remint(operatorDefaultEffortFor(cfg, id), defCaps)
		entry.defaultCaps = defCaps
		entries[id] = entry
	}
	return reg, nil
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

// newOpenAIEntry constructs a resilience-wrapped openai-adapter provider entry. It
// is shared by the openai and openrouter ids (OpenRouter is the SAME adapter with a
// different base URL + key), so the two cannot drift on resilience wrapping. It logs
// the provider id and base URL ONLY — never the key.
func newOpenAIEntry(cfg Config, id, key, baseURL string) providerEntry {
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
	// wrapping.
	construct := func(effort string, caps port.ProviderCapabilities) port.LLMProvider {
		opts := []openai.Option{openai.WithAPIKey(key)}
		if baseURL != "" {
			opts = append(opts, openai.WithBaseURL(baseURL))
		}
		if effort != "" {
			opts = append(opts, openai.WithReasoningEffort(effort))
		}
		opts = append(opts, openai.WithProviderCapabilities(caps))
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

// newAnthropicEntry constructs a resilience-wrapped native-Anthropic provider
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
// with the single-provider default), then sorted order — overridden by a
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

// preferredDefaultProvider picks the default provider id from the available
// entries: openai when present (preserving the single-provider default), else the
// first id in sorted order, else "" (zero available).
func preferredDefaultProvider(reg *providerRegistry) string {
	if _, ok := reg.entries[providerOpenAI]; ok {
		return providerOpenAI
	}
	avail := reg.Available()
	if len(avail) == 0 {
		return ""
	}
	return avail[0]
}
