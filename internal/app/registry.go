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

	anthropicoption "github.com/anthropics/anthropic-sdk-go/option"
	openaichatoption "github.com/openai/openai-go/v3/option"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/internal/adapter/llmendpoint"
	"github.com/stacklok/mecatl/internal/adapter/llmresilience"
	"github.com/stacklok/mecatl/internal/adapter/openaibearer"
	"github.com/stacklok/mecatl/internal/adapter/openaicodex"
	"github.com/stacklok/mecatl/internal/adapter/openaicompat"
	"github.com/stacklok/mecatl/internal/adapter/openrouter"
	"github.com/stacklok/mecatl/internal/adapter/permconfig"
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
	// providerOpenRouterAnthropic is the native Anthropic Messages surface exposed
	// by the same OpenRouter credential (ADR 0346), the sibling of
	// providerToolhiveAnthropic. It is deliberately a distinct provider: models
	// listed here execute through OpenRouter's Anthropic endpoint, never the
	// OpenAI Responses adapter used by providerOpenRouter. It exists because
	// Anthropic caches only on an explicit ask, and the Messages surface carries
	// the breakpoints (and a TTL) that the Responses path cannot express.
	providerOpenRouterAnthropic = "openrouter-anthropic"
	providerAnthropic           = "anthropic"
	// providerOpenCode is OpenCode Go (https://opencode.ai/zen/go/v1), an
	// OpenAI-compatible subscription gateway that speaks the Chat Completions wire
	// protocol uniformly. Unlike openai/openrouter (Responses API) it rides the
	// native Chat Completions adapter (openaichat). Key-driven (OPENCODE_API_KEY).
	providerOpenCode = "opencode"
	// providerToolhive (issue #262) is the intent-driven ToolHive LLM gateway
	// proxy entry: unlike the three above, it is registered by CONFIG-DETECTED
	// INTENT (or an explicit base-url override), never a credential.
	providerToolhive = "toolhive"
	// providerToolhiveAnthropic is the native Anthropic Messages surface exposed
	// by the same configured ToolHive gateway identity. It is deliberately a
	// distinct provider: models listed here must execute through /anthropic/v1/messages,
	// never the OpenAI Responses adapter used by providerToolhive.
	providerToolhiveAnthropic = "toolhive-anthropic"
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
	// openrouter-anthropic serves ONLY Anthropic-family ids, so its default must
	// be one; the OpenRouter-namespaced form of the anthropic entry's default.
	providerOpenRouterAnthropic: "anthropic/claude-sonnet-4-6",
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

// BuiltinDefaultModelFor returns the runtime fallback model for a stock provider,
// or "" when the provider has no fixed fallback.
func BuiltinDefaultModelFor(id string) string {
	return builtinDefaultModel[id]
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

type nativeCredentialLoaderFunc func(context.Context, permconfig.ProviderDefinition) (llmendpoint.BearerSource, error)

func (f nativeCredentialLoaderFunc) Load(ctx context.Context, definition permconfig.ProviderDefinition) (llmendpoint.BearerSource, error) {
	return f(ctx, definition)
}

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

// providerProtocol is the wire protocol a providerEntry's adapter speaks. It
// mirrors permconfig's existing api_flavor vocabulary one-for-one, so a custom
// provider definition and a built-in entry classify through the same three
// values rather than two overlapping notions of "is it Anthropic".
//
// It replaced an anthropicProtocol bool once a THIRD state had to be
// distinguished: Chat Completions has no breakpoint mechanism at all, so it
// answers the prompt-cache question differently from BOTH Responses and
// Messages. Three mutually-exclusive states is where a second bool starts
// encoding an impossible fourth.
//
// The ZERO VALUE is protocolOpenAIResponses deliberately: every hand-built
// providerEntry literal (the mock entry, the test seams, the fixtures) keeps
// the behaviour it had when this was a bool that defaulted false.
//
// Every real CONSTRUCTOR still sets it explicitly rather than leaning on that
// zero value, so a reader of any one constructor can see which protocol it
// builds without first knowing the enum's declaration order.
type providerProtocol int

const (
	// protocolOpenAIResponses is POST /v1/responses via provider/openai — the
	// zero value, and the protocol most entries speak.
	protocolOpenAIResponses providerProtocol = iota
	// protocolAnthropicMessages is POST /v1/messages via provider/anthropic:
	// anthropic, toolhive-anthropic, openrouter-anthropic, and a custom
	// api_flavor: anthropic-messages definition.
	protocolAnthropicMessages
	// protocolOpenAIChatCompletions is POST /v1/chat/completions via
	// provider/openaichat: opencode, and a custom
	// api_flavor: openai-chat-completions definition. It has NO prompt-cache
	// breakpoint mechanism, which is the whole reason this value exists.
	protocolOpenAIChatCompletions
)

// providerEntry is one configured provider in the registry: its stable id, the
// constructed (resilience-wrapped) port.LLMProvider, whether its credentials
// resolved from the environment (availability), and its base URL for logging
// only. Composition-only — the agent/server never see this type.
type providerEntry struct {
	id        string           // wire-stable provider id
	provider  port.LLMProvider // resilience-wrapped, ready to hand to an engine
	available bool             // ≥1 of the provider's env[] keys resolved
	baseURL   string           // for logging/diagnostics ONLY; never wired
	// protocol is the WIRE PROTOCOL this entry's adapter speaks. Set at
	// construction by whichever constructor built the entry — never inferred from
	// the id, which cannot see a custom provider's api_flavor. Read by
	// promptCachedFor and promptCacheSource (ADR 0346), because the three
	// protocols ask for a prompt cache in three different ways, and one of them
	// has no way to ask at all.
	protocol providerProtocol
	// nativeEndpoint marks a deployment-wide native gateway. Its credentialed
	// live listing is on-demand only; Build publishes the configured model floor.
	nativeEndpoint bool
	// defaultModel is the composition-owned floor for a custom provider. Built-in
	// entries leave it empty and resolve through builtinDefaultModel.
	defaultModel string
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
	// toolhiveMode records the family entry's resolved route so status hints can
	// distinguish a local proxy failure from a direct gateway/OIDC failure.
	// It is meaningful only when intentDriven is true for a ToolHive provider.
	toolhiveMode toolhiveRoutingMode
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
	entries map[string]providerEntry // keyed by provider id; only AVAILABLE entries
	// unavailableNative retains configured endpoint identities that are valid but
	// not enrolled. They remain status-visible and explicit selection can return
	// an actionable error without falling back.
	unavailableNative map[string]struct{}
	defaultID         string // resolved default provider (precedence: resolveDefaultModel)
	defaultModel      string // resolved default model for defaultID ("" => adapter/endpoint default)
	// meta is a read-only view bound to discovery before bootstrap. Catalog
	// fallback is computed at read time, never stored as a live observation.
	meta *liveMetaStore
	// contextWindows is the operator-tier exact provider/model override map. It is
	// immutable after Build and read by the picker projection as well as resolvers.
	contextWindows map[string]map[string]int
	// promptCached answers ModelInfo.prompt_cached for a (provider, model) pair
	// (ADR 0346). A CLOSURE over the build's Config so the picker projection
	// reads the SAME resolution the adapters were constructed with, instead of
	// growing a second copy of the dialect precedence. nil on a hand-built test
	// registry, which projects false.
	promptCached func(providerID, modelID string) bool
	// contextWindowOverride is the process-wide CLI escape hatch mirrored from Config.
	// Keeping it beside contextWindows lets model-list projection use the same resolver
	// as engines and echoes instead of growing a second precedence implementation.
	contextWindowOverride int
	// discovery is the sole Build-local owner of attempts and observations.
	discovery *providerDiscovery
	// defaultModelAutoSelected distinguishes first-listed healing from an
	// operator pin. All reads and writes use defaultModelMu.
	defaultModelAutoSelected bool
	// defaultModelMu protects selection facts; discovery's completion tail
	// serializes healing and captures both facts before public projection.
	defaultModelMu sync.Mutex
	// entriesMu guards reminted provider entries. It is a leaf lock: remint
	// construction and capability resolution run before acquiring it.
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
func (r *providerRegistry) DefaultModelFor(id string) string {
	if entry, ok := r.Lookup(id); ok && entry.defaultModel != "" {
		return entry.defaultModel
	}
	return BuiltinDefaultModelFor(id)
}

// remintEntry applies the initial capability fixup from one accepted snapshot.
// Discovery healing calls remintEntryCandidate with its unpublished candidate;
// both paths use the same remint implementation outside the publication lock.
func (r *providerRegistry) remintEntry(pid, model string) {
	r.remintEntryCandidate(pid, model, r.meta.current())
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
		"no key run with --mock; see https://mecatl.dev/docs/features/choose-models for provider setup")

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

func mockDefaultModel(configured string) string {
	if configured != "" {
		return configured
	}
	return providerMock
}

//nolint:gocyclo // Registry assembly branches once per independently configured provider and auth mode.
func buildProviderRegistryContext(ctx context.Context, cfg Config, detect envDetector) (*providerRegistry, error) {
	ctx = providerRegistryContext(ctx)
	detect = providerRegistryDetector(detect)

	// UseMock short-circuit: a single synthetic entry, offline, regardless of env.
	if cfg.UseMock || cfg.MockProvider != nil {
		logMockProvider(ctx, cfg)
		mock := port.LLMProvider(mockllm.New(
			mockllm.TextTurn("Mock provider: no real model is configured. Set OPENAI_API_KEY for live use."),
		))
		if cfg.MockProvider != nil {
			// The test-only scripted seam (Config.MockProvider): the caller's
			// scripted provider replaces the canned single text turn.
			mock = cfg.MockProvider
		}
		defaultModel := mockDefaultModel(cfg.Model)

		// The mock is intentionally left UNWRAPPED by resilience: it never fails over
		// the network, so retries/breaker would be inert.
		reg := &providerRegistry{
			entries:      map[string]providerEntry{providerMock: {id: providerMock, provider: mock, available: true}},
			defaultID:    providerMock,
			defaultModel: defaultModel,
			meta:         newLiveMetaStore(),
		}
		reg.discovery = newProviderDiscovery(reg, cfg)
		reg.meta.owner = reg.discovery
		return reg, nil
	}

	openAIKey := providerKey(cfg.OpenAIKey, providerOpenAI, detect)
	if openAIKey != "" && cfg.OpenAIBearerTokenFile != "" {
		return nil, errors.New("OpenAI API key and --openai-bearer-token-file are mutually exclusive; remove the OpenAI API-key credential or remove --openai-bearer-token-file")
	}
	if cfg.OpenAIBearerTokenFile != "" && builtinBaseURL(cfg, providerOpenAI) == "" {
		return nil, errors.New("--openai-bearer-token-file requires an explicit nonempty --openai-base-url")
	}

	entries := make(map[string]providerEntry)

	// Resolver closures capture this read view before entries exist. Bind it
	// to the owner after registration and before any bootstrap request.
	meta := newLiveMetaStore()

	// openai: AVAILABLE iff an API key resolves or a bearer-token file is
	// configured. The file is read by the transport for every request so projected
	// Kubernetes ServiceAccount token rotation is observed without a restart.
	if openAIKey != "" {
		entries[providerOpenAI] = newOpenAICompatEntry(cfg, providerOpenAI, openAIKey, builtinBaseURL(cfg, providerOpenAI))
	} else if cfg.OpenAIBearerTokenFile != "" {
		baseURL := builtinBaseURL(cfg, providerOpenAI)
		client, err := openaibearer.NewHTTPClient(cfg.OpenAIBearerTokenFile, baseURL, nil)
		if err != nil {
			return nil, fmt.Errorf("configure OpenAI bearer token file: %w", err)
		}
		entries[providerOpenAI] = newOpenAICompatEntry(cfg, providerOpenAI, "", baseURL,
			openai.WithHTTPClient(client), openai.WithMaxRetries(0))
	}

	if entry, err := newOpenAICodexEntry(cfg); err != nil {
		return nil, err
	} else if entry.available {
		entries[providerOpenAICodex] = entry
	}

	// openrouter: same stateless openai adapter, OpenRouter base URL, keyed by
	// OPENROUTER_API_KEY (falling back to OPENAI_API_KEY by convention).
	if key := providerKey(cfg.OpenRouterKey, providerOpenRouter, detect); key != "" {
		baseURL := builtinBaseURL(cfg, providerOpenRouter)
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
		//
		// ONE value, shared with the openrouter-anthropic entry below: two
		// constructions of the same keyless, stateless leaf over the same client are
		// two things to keep in step for no gain. The residual is honest and
		// deliberate — the pair still makes TWO GETs per refresh pass, because
		// openrouter.Lister is a documented stateless leaf with no cache, and the
		// ToolHive two-surface family already behaves the same way. A shared cached
		// fetch was considered and declined: a cache that outlives a call is an
		// outlives-a-call resource needing an ADR 0027 List 1 row, which is a real
		// cost to pay for deduplicating one background HTTP GET.
		orLister := openRouterLister{inner: openrouter.NewLister(cfg.liveModelHTTPClient)}
		entry.lister = orLister
		entries[providerOpenRouter] = entry

		// openrouter-anthropic (ADR 0346): the SAME credential also reaches
		// OpenRouter's native Anthropic Messages surface, and Anthropic caches
		// ONLY on an explicit ask. Routing Claude there gets the ADR 0100
		// breakpoint budget plus a TTL — neither expressible over Responses —
		// because newAnthropicEntryFor applies conversation caching on EVERY
		// endpoint. Registered on credential presence (no opt-in), mirroring
		// ADR 0334's register-on-intent, so an existing OpenRouter user's Claude
		// default starts caching on upgrade with no config change.
		if anthropicBase := openRouterAnthropicBaseURL(baseURL); anthropicBase != "" {
			arEntry := newAnthropicEntryFor(cfg, providerOpenRouterAnthropic, key, anthropicBase, meta, false)
			// Anthropic-family ids ONLY: the skin rejects non-Anthropic models.
			// Reuses OpenRouter's own /models rather than probing the skin's
			// undocumented /v1/models.
			arEntry.lister = openRouterAnthropicLister{inner: orLister}
			entries[providerOpenRouterAnthropic] = arEntry
		}
	}

	// opencode (OpenCode Go): the native Chat Completions adapter (openaichat, NOT
	// the openai Responses adapter), keyed by OPENCODE_API_KEY. Its /models endpoint
	// is OpenAI-shaped, so it opts into LIVE model listing via the generic
	// openaicompat lister (bare ids, no display name). The lister IS keyed here —
	// unlike openrouter's keyless public catalog, OpenCode Go requires the bearer.
	if key := providerKey(cfg.OpenCodeKey, providerOpenCode, detect); key != "" {
		baseURL := builtinBaseURL(cfg, providerOpenCode)
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
		entries[providerAnthropic] = newAnthropicEntryFor(cfg, providerAnthropic, key, builtinBaseURL(cfg, providerAnthropic), meta, true)
	}

	unavailableNative, err := addCustomProviderEntries(ctx, entries, cfg, meta)
	if err != nil {
		return nil, err
	}

	// ToolHive (issue #262, D1): both protocol-specific entries are registered
	// by CONFIG-DETECTED INTENT alone —
	// resolveToolhiveIntent NEVER runs a network probe, so registration never
	// blocks on (or is gated by) reachability (R1.1). With the family registered,
	// len(entries)>0 even with ZERO provider keys, so errNoProvider no longer
	// fires for a ToolHive-only operator — intended (zero-API-key onboarding).
	// Issue #265: the intent now carries a routing mode — proxy (loopback,
	// today's behaviour) or direct (gateway_url + in-process OIDC token).
	if intent, ok := resolveToolhiveIntent(cfg); ok {
		openAIEntry, anthropicEntry := newToolhiveEntries(cfg, intent, cfg.toolhiveConfigPath, meta)
		entries[providerToolhive] = openAIEntry
		entries[providerToolhiveAnthropic] = anthropicEntry
	}

	if len(entries) == 0 {
		if len(unavailableNative) > 0 {
			return nil, fmt.Errorf("OIDC provider is not enrolled: run `mecatui providers` to inspect configured providers, then `mecatui providers login PROVIDER`")
		}
		return nil, errNoProvider
	}
	if cfg.DefaultProvider != "" {
		if _, unavailable := unavailableNative[cfg.DefaultProvider]; unavailable {
			return nil, fmt.Errorf("provider %q is not enrolled: run `mecatui providers login %s`", cfg.DefaultProvider, cfg.DefaultProvider)
		}
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

	reg := &providerRegistry{entries: entries, unavailableNative: unavailableNative, meta: meta,
		contextWindows: cfg.contextWindows, contextWindowOverride: cfg.ContextWindowOverride}
	// Bind the prompt-cache projection AFTER entries exist (it looks an entry up)
	// and over THIS build's cfg, so --no-prompt-cache and the ADR 0100 dialect
	// precedence are read once, in one place.
	reg.promptCached = func(providerID, modelID string) bool {
		return promptCachedFor(reg, cfg, providerID, modelID)
	}
	// BUILD-ONCE posture line (ADR 0346 decision 7). Logged here, where the
	// registry is assembled, and NEVER from the per-engine deps builders — the
	// same rule that keeps normaliseAnthropicCacheTTL's WARN off every
	// per-session and heal re-mint.
	if line := promptCachePostureLine(reg, cfg); line != "" {
		cfg.diag().Log(context.Background(), port.LevelInfo, line)
	}
	reg.defaultID, reg.defaultModel = resolveDefaultModel(cfg, reg)
	reg.discovery = newProviderDiscovery(reg, cfg)
	meta.owner = reg.discovery
	ready := false
	defer func() {
		if !ready {
			reg.discovery.Close()
		}
	}()
	if !cfg.skipProviderNetworkDiscovery {
		if err := bootstrapOpenAICodexDefault(ctx, reg, cfg); err != nil {
			return nil, err
		}
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
	// Build-time probe (issue #262, R1.2): only runs when the ToolHive family
	// exists. It NEVER gates registration
	// (already done above) — it drives the startup diagnostic, the initial
	// provider_status + last-known-good seed, and default-model eligibility.
	if !cfg.skipProviderNetworkDiscovery {
		if err := probeToolhive(reg, cfg); err != nil {
			return nil, err
		}
	}
	ready = true
	return reg, nil
}

func logMockProvider(ctx context.Context, cfg Config) {
	if cfg.MockProvider == nil {
		cfg.diag().Log(ctx, port.LevelWarn, "LLM provider: mock (canned, offline) — for smoke tests only")
		return
	}
	cfg.diag().Log(ctx, port.LevelWarn, "LLM provider: mock (scripted, offline) — for testing only")
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

func builtinBaseURL(cfg Config, id string) string {
	return cfg.ProviderOverrides[id].BaseURL
}

func mergeProviderOverrides(settings, command permconfig.ProviderOverrides) permconfig.ProviderOverrides {
	if len(settings) == 0 && len(command) == 0 {
		return nil
	}
	merged := make(permconfig.ProviderOverrides, len(settings)+len(command))
	for id, override := range settings {
		merged[id] = override
	}
	for id, override := range command {
		if override.BaseURL != "" {
			merged[id] = override
		}
	}
	return merged
}

func sortedProviderDefinitions(definitions permconfig.ProviderDefinitions) []permconfig.ProviderDefinition {
	out := make([]permconfig.ProviderDefinition, 0, len(definitions))
	for _, definition := range definitions {
		out = append(out, definition)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func addCustomProviderEntries(ctx context.Context, entries map[string]providerEntry, cfg Config, meta *liveMetaStore) (map[string]struct{}, error) {
	unavailableNative := make(map[string]struct{})
	for _, definition := range sortedProviderDefinitions(cfg.ProviderDefinitions) {
		if definition.Auth.Method == "oidc" {
			if cfg.NativeEndpointCredentialLoader == nil {
				unavailableNative[definition.ID] = struct{}{}
				continue
			}
			source, err := cfg.NativeEndpointCredentialLoader.Load(ctx, definition)
			if err != nil || source == nil {
				unavailableNative[definition.ID] = struct{}{}
				continue
			}
			entry, err := newNativeProviderEntry(cfg, definition, source)
			if err != nil {
				return nil, err
			}
			entries[definition.ID] = entry
			continue
		}
		key := cfg.CustomProviderAPIKeys[definition.ID]
		if definition.Auth.Method == "api_key" && key == "" {
			continue
		}
		entries[definition.ID] = newCustomProviderEntry(cfg, definition, key, meta)
	}
	return unavailableNative, nil
}

func newNativeProviderEntry(cfg Config, definition permconfig.ProviderDefinition, source llmendpoint.BearerSource) (providerEntry, error) {
	transport := cfg.nativeEndpointTransport
	if transport == nil {
		if bound, ok := source.(interface{ GatewayTransport() http.RoundTripper }); ok {
			transport = bound.GatewayTransport()
		}
	}
	client, err := llmendpoint.NewGatewayHTTPClient(definition.BaseURL, source, transport)
	if err != nil {
		return providerEntry{}, fmt.Errorf("configure OIDC provider %q: %w", definition.ID, err)
	}
	entry := newOpenAICompatEntry(cfg, definition.ID, "transport-owned", definition.BaseURL,
		openai.WithHTTPClient(client), openai.WithMaxRetries(0))
	entry.nativeEndpoint = true
	entry.defaultModel = definition.DefaultModel
	entry.lister = gatewayLister{inner: openaicompat.NewLister(definition.BaseURL, "", client)}
	return entry, nil
}

func newCustomProviderEntry(cfg Config, definition permconfig.ProviderDefinition, key string, meta *liveMetaStore) providerEntry {
	inferenceClient := customProviderInferenceHTTPClient()
	var entry providerEntry
	switch definition.APIFlavor {
	case "openai-responses":
		entry = newOpenAICompatEntry(cfg, definition.ID, key, definition.BaseURL, openai.WithHTTPClient(inferenceClient))
	case "openai-chat-completions":
		entry = newOpenCodeEntry(cfg, definition.ID, key, definition.BaseURL, openaichat.WithHTTPClient(inferenceClient))
	case "anthropic-messages":
		entry = newAnthropicEntryFor(cfg, definition.ID, key, definition.BaseURL, meta, false,
			anthropic.WithRequestOption(anthropicoption.WithHTTPClient(inferenceClient)))
	default:
		// Definitions are validated before composition. Keep this fail-closed for
		// hand-built Config values used by embedding callers and tests.
		return providerEntry{}
	}
	entry.defaultModel = definition.DefaultModel
	listingClient := customProviderListingHTTPClient(cfg.liveModelHTTPClient)
	switch definition.APIFlavor {
	case "openai-responses", "openai-chat-completions":
		entry.lister = gatewayLister{inner: openaicompat.NewLister(definition.BaseURL, key, listingClient)}
	case "anthropic-messages":
		entry.lister = anthropicLister{inner: anthropic.NewLister(key, definition.BaseURL, listingClient)}
	}
	return entry
}

func customProviderInferenceHTTPClient() *http.Client {
	return &http.Client{CheckRedirect: openaicompat.RefuseRedirects}
}

func customProviderListingHTTPClient(client *http.Client) *http.Client {
	if client == nil {
		client = &http.Client{}
	}
	clone := *client
	if clone.Timeout <= 0 {
		clone.Timeout = 5 * time.Second
	}
	clone.CheckRedirect = openaicompat.RefuseRedirects
	return &clone
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
func bootstrapOpenAICodexDefault(parent context.Context, reg *providerRegistry, _ Config) error {
	if reg == nil || reg.Default() != providerOpenAICodex || reg.ResolvedDefaultModel() != "" {
		return nil
	}
	entry, ok := reg.Lookup(providerOpenAICodex)
	if !ok || entry.lister == nil {
		return errors.New("openai-codex: default model discovery is unavailable")
	}
	view, err := reg.discovery.request(parent, providerOpenAICodex, discoveryBootstrap)
	if err != nil {
		return err
	}
	state := view.providers[providerOpenAICodex]
	switch state.outcome.State {
	case statusOK:
		return nil
	case statusEmpty:
		return errors.New("openai-codex: account returned no picker-visible models; replace the manual token or choose an explicit model")
	case statusUnauthorized:
		return errors.New("openai-codex: default model discovery unauthorized")
	default:
		return errors.New("openai-codex: default model discovery unreachable")
	}
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
		return providerEntry{id: id, provider: cfg.providerConstructor(cfg, id, key, baseURL), available: true,
			baseURL: baseURL, protocol: protocolOpenAIResponses}
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
		opts := make([]openai.Option, 0, 4+len(extra))
		if key != "" {
			opts = append(opts, openai.WithAPIKey(key))
		}
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
		// Prompt-cache key salt (ADR 0346): the SAME per-process value on every
		// entry. Inside the closure for the same reason the dialect is — a
		// per-session/heal re-mint must not silently drop it and start emitting
		// the unsalted, cross-principal-stable key.
		opts = append(opts, openai.WithCacheKeySalt(cfg.promptCacheKeySalt))
		// Protocol-native breakpoint (ADR 0346 decision 1): armed for EVERY
		// endpoint, governed only by --no-prompt-cache. Deliberately not tied
		// to the dialect above — an endpoint the dialect cannot classify is
		// exactly where an explicit-ask upstream needs the ask.
		opts = append(opts, openai.WithPromptCacheBreakpoints(!cfg.PromptCacheDisabled))
		// Redirect refusal (ADR 0346 Scenario 5, applying ADR 0334's existing
		// gateway decision consistently): the SDK's default client follows up to
		// 10 redirects and re-sends the body on a 307/308. Go strips
		// Authorization cross-domain so the key does not travel, but the system
		// prompt, file contents and tool results do. Prepended BEFORE extra, so a
		// caller that passes its own WithHTTPClient (the gateway entries, which
		// already refuse redirects and additionally inject a bearer) still wins.
		opts = append(opts, openai.WithHTTPClient(&http.Client{CheckRedirect: openaicompat.RefuseRedirects}))
		opts = append(opts, extra...)
		opts = append(opts, openai.WithMaxRetries(0))
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
	return providerEntry{id: id, provider: llm, available: true, baseURL: baseURL,
		remint: construct, protocol: protocolOpenAIResponses}
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
func newOpenCodeEntry(cfg Config, id, key, baseURL string, extra ...openaichat.Option) providerEntry {
	cfg.diag().Log(context.Background(), port.LevelInfo, "LLM provider available", "provider", id, "model", cfg.Model, "base_url", baseURL)
	if cfg.providerConstructor != nil {
		return providerEntry{id: id, provider: cfg.providerConstructor(cfg, id, key, baseURL), available: true,
			baseURL: baseURL, protocol: protocolOpenAIChatCompletions}
	}
	construct := func(effort string, _ port.ProviderCapabilities) port.LLMProvider {
		opts := make([]openaichat.Option, 0, 3)
		if key != "" {
			opts = append(opts, openaichat.WithAPIKey(key))
		}
		if baseURL != "" {
			opts = append(opts, openaichat.WithBaseURL(baseURL))
		}
		if effort != "" {
			opts = append(opts, openaichat.WithReasoningEffort(effort))
		}
		if id == providerOpenCode {
			opts = append(opts, openaichat.WithOpenCodeSessionHeader())
		}
		// Prompt caching (ADR 0100): dormant today (id is always providerOpenCode
		// here, which never matches the OpenAI gate — see
		// openaichatCacheDialectFor), but wired inside the closure so a future
		// OpenAI-over-Chat-Completions entry gets it for free on every
		// per-session/heal re-mint.
		opts = append(opts, openaichat.WithCacheDialect(openaichatCacheDialectFor(id, baseURL, cfg)))
		opts = append(opts, extra...)
		opts = append(opts, openaichat.WithRequestOption(openaichatoption.WithMaxRetries(0)))
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
	return providerEntry{id: id, provider: llm, available: true, baseURL: baseURL,
		remint: construct, protocol: protocolOpenAIChatCompletions}
}

// newAnthropicEntryFor constructs an Anthropic Messages entry. It honors the SAME composition-only providerConstructor test seam first
// (so the offline multi-provider e2e can back "anthropic" with a mock), else
// constructs the anthropic adapter with WithMaxTokens set to the default model's
// catalogued output limit (Anthropic REQUIRES max_tokens and rejects a value
// above the model's ceiling; a conservative fallback applies when uncatalogued).
// It logs the provider id, default model, and base URL ONLY — never the key. No
// lister in P1 (live Anthropic listing is deferred).
func newAnthropicEntryFor(cfg Config, id, key, baseURL string, meta *liveMetaStore, liveListing bool, extra ...anthropic.Option) providerEntry {
	cfg.diag().Log(context.Background(), port.LevelInfo, "LLM provider available", "provider", id, "model", cfg.Model, "base_url", baseURL)
	if cfg.providerConstructor != nil {
		entry := providerEntry{
			id:        id,
			provider:  cfg.providerConstructor(cfg, id, key, baseURL),
			available: true,
			baseURL:   baseURL,
		}
		entry.protocol = protocolAnthropicMessages
		if liveListing {
			entry.lister = anthropicLister{inner: anthropic.NewLister(key, baseURL, cfg.liveModelHTTPClient)}
		}
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
			// PER-MODEL max_tokens: each request's max_tokens is resolved LIVE-FIRST from
			// the live-metadata store (the live output ceiling when present), else the
			// catalogued ceiling, else the adapter's conservative default. So a per-session/
			// sub-agent route to a smaller-ceiling model (e.g. claude-3-5-haiku=8192) never
			// sends the default model's larger value and 400s, and a newly-released model the
			// catalog doesn't know gets its true ceiling from the live API. The adapter stays
			// catalog-/store-free — composition owns the closure over meta.
			anthropic.WithMaxTokensResolver(func(model string) int {
				return meta.outputLimitFor(id, model)
			}),
			// THINKING-FROM-LIVE: the adapter's extended-thinking mode (adaptive / manual /
			// none) reads the LIVE descriptor (Capabilities.Thinking.Types) when the model is
			// KNOWN, falling back to the adapter's hardcoded prefix matrix as the OFFLINE
			// floor (known=false). This retires the stale-prefix guesswork for live runs
			// while keeping the deterministic matrix for offline/uncatalogued models.
			anthropic.WithThinkingResolver(func(model string) (adaptive, enabled, known bool) {
				return meta.thinkingFor(id, model)
			}),
			anthropic.WithProviderCapabilities(caps),
			// Prompt caching (ADR 0100): --no-prompt-cache disables the three NEW
			// conversation breakpoints (the pre-existing StablePrefix breakpoint is
			// unaffected); --anthropic-cache-ttl (normalised once, above) stamps a
			// uniform TTL across every marker the adapter emits. INSIDE the closure
			// so every per-session/heal re-mint carries both.
			anthropic.WithConversationCaching(!cfg.PromptCacheDisabled),
		}
		if key != "" {
			opts = append([]anthropic.Option{anthropic.WithAPIKey(key)}, opts...)
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
		// Redirect refusal (ADR 0346 Scenario 5), the Messages-protocol sibling of
		// newOpenAICompatEntry's client. The SDK's default client follows up to 10
		// redirects and re-sends the body on a 307/308. Go strips Authorization
		// cross-origin — but NOT x-api-key, the header this protocol authenticates
		// with — and never the body: system prompt, file contents, tool results.
		//
		// Unlike the openai path this is LOAD-BEARING, not belt-and-braces:
		// openai-go carries its own cross-origin guard, anthropic-sdk-go ships
		// none, so composition is the ONLY layer refusing here.
		//
		// It lives in the CONSTRUCTOR, not at the openrouter-anthropic call site,
		// because every Anthropic-protocol entry derives its base from
		// operator-overridable config (--anthropic-base-url, --openrouter-base-url,
		// a custom provider's base_url), so every one of them needs it and a future
		// caller cannot forget it. Prepended BEFORE extra, so a caller passing its
		// own client (the gateway entries, which already refuse redirects and
		// additionally inject a bearer) still wins.
		opts = append(opts, anthropic.WithRequestOption(
			anthropicoption.WithHTTPClient(&http.Client{CheckRedirect: openaicompat.RefuseRedirects})))
		opts = append(opts, extra...)
		opts = append(opts, anthropic.WithRequestOption(anthropicoption.WithMaxRetries(0)))
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
			Diagnostics: cfg.diag().With("provider", id),
		})
	}
	// The OPERATOR-DEFAULT effort baked into the shared .provider (anthropic
	// identity-maps all five tiers, so this never clamps — but it shares the one
	// startup-clamp-narration helper for uniformity with the openai entry). The
	// default-model capability intersection is stamped by buildProviderRegistry's
	// post-assembly fixup (it needs the assembled registry + meta); until then the
	// shared .provider carries the adapter's static transmit caps.
	llm := construct(operatorDefaultEffortFor(cfg, id), anthropicStaticCaps)
	cfg.diag().Log(context.Background(), port.LevelInfo, "LLM resilience enabled",
		"provider", id,
		"max_attempts", cfg.LLMMaxAttempts,
		"per_attempt_timeout", cfg.LLMPerAttemptTimeout,
		"stream_idle_timeout", cfg.LLMStreamIdleTimeout,
		"breaker_threshold", cfg.LLMBreakerThreshold,
		"breaker_cooldown", cfg.LLMBreakerCooldown)
	entry := providerEntry{id: id, provider: llm, available: true, baseURL: baseURL, remint: construct, protocol: protocolAnthropicMessages}
	if liveListing {
		entry.lister = anthropicLister{inner: anthropic.NewLister(key, baseURL, cfg.liveModelHTTPClient)}
	}
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
	explicitProvider := false
	if cfg.DefaultProvider != "" {
		if _, ok := reg.Lookup(cfg.DefaultProvider); ok {
			defID = cfg.DefaultProvider
			explicitProvider = true
		}
	}
	var model string
	switch {
	case cfg.Model != "":
		// (1) --model flag: an EXPLICIT override wins, paired with the default provider.
		model = cfg.Model
	case cfg.DefaultModel != "":
		// (2) server-configured deployment-wide default (--default-model).
		model = cfg.DefaultModel
	default:
		// (3) client last-used: S4.
		// (4) per-provider default model from the table (no entry => "" => endpoint default).
		model = reg.DefaultModelFor(defID)
	}
	// ADR 0346 decision 3: never DEFAULT onto a protocol sibling that cannot cache
	// the default model. An explicitly configured provider still wins — the
	// operator named it.
	if !explicitProvider {
		defID = preferAnthropicProtocolSibling(reg, defID, model)
	}
	return defID, model
}

// anthropicProtocolSibling maps an OpenAI-Responses provider id to the
// Anthropic-Messages entry registered from the SAME credential, for providers
// whose two surfaces SHARE a model-id namespace.
//
// Only the OpenRouter pair qualifies (ADR 0346 decision 6). The ToolHive pair
// is deliberately ABSENT: measured on staging, that gateway exposes one Claude
// Opus 4.8 as `anthropic/claude-opus-4.8` on its OpenRouter downstream,
// `claude-opus-4-8` on its Anthropic downstream and
// `us.anthropic.claude-opus-4-8` on Bedrock. Switching the provider while
// carrying the id verbatim would therefore produce a pair that cannot resolve.
// That fails safe rather than misrouting, but it delivers nothing, so claiming
// it would be false comfort. The gateway case is covered by the protocol-native
// breakpoint instead, which needs no id translation at all.
var anthropicProtocolSibling = map[string]string{
	providerOpenRouter: providerOpenRouterAnthropic,
}

// preferAnthropicProtocolSibling redirects a DEFAULT provider selection to its
// Anthropic-Messages sibling when the default model is an Anthropic-family id
// (ADR 0346 decision 3).
//
// Why this exists: Anthropic caches only on an explicit ask, and the Responses
// entry emits no breakpoints, so defaulting a Claude model onto it silently
// re-pays full uncached input every turn. That is exactly how the reported
// incident happened — preferredDefaultProvider ranks intent-driven entries by
// SORTED ORDER, and "toolhive" sorts before "toolhive-anthropic".
//
// The model id is carried VERBATIM, which is why anthropicProtocolSibling only
// lists pairs whose surfaces share an id namespace. For OpenRouter that is
// exact: its Anthropic surface takes the same namespaced id as its Responses
// API. A pair that does not share one is not listed at all rather than being
// switched and hoped for.
//
// Returns id unchanged when the model is not Anthropic-family, when there is no
// sibling for id, or when the sibling is not registered.
func preferAnthropicProtocolSibling(reg *providerRegistry, id, model string) string {
	if !isAnthropicFamilyModel(model) {
		return id
	}
	sibling, ok := anthropicProtocolSibling[id]
	if !ok {
		return id
	}
	if _, registered := reg.Lookup(sibling); !registered {
		return id
	}
	return sibling
}

// isAnthropicFamilyModel reports whether a model id names an Anthropic model, in
// either the OpenRouter-namespaced form ("anthropic/claude-...") or the bare
// vendor form ("claude-..."). Deliberately narrow: it matches the vendor
// namespace and the product name, never a substring anywhere in the id.
func isAnthropicFamilyModel(model string) bool {
	m := strings.ToLower(strings.TrimSpace(model))
	return strings.HasPrefix(m, openRouterAnthropicSegment) || strings.HasPrefix(m, "claude")
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

// toolhiveIntent is the resolved ToolHive-family registration intent and its
// routing mode. baseURL is the OpenAI-compatible request URL (loopback for
// proxy, gateway_url+"/v1" for direct); the native Anthropic URL is derived
// from it without losing a gateway path prefix. gatewayURL is the
// upstream the proxy forwards to (DIAGNOSTIC ONLY for proxy, the SAME as
// baseURL's origin for direct); explicit marks an --toolhive-llm-base-url
// override (proxy-only — direct is config-driven).
type toolhiveIntent struct {
	mode       toolhiveRoutingMode
	baseURL    string
	gatewayURL string
	explicit   bool
}

// resolveToolhiveIntent decides whether the ToolHive provider family should be
// registered (issue #262, D1) and, if so, what routing mode + URLs serve both
// protocol entries (issue #265). It NEVER runs a network probe — registration
// is intent-only; the LATER Build-time probe reads the constructed entries.
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
	diagnosticGatewayURL := sanitizeGatewayURL(detected.GatewayURL)

	// Mode discriminator (issue #265). auto upgrades to direct when the OIDC
	// trio is configured; proxy is the byte-identical fallback. direct is the
	// explicit opt-in; a direct ask against an unconfigured gateway is a loud
	// Build-fail (NOT a silent proxy fallback — the operator asked for direct
	// and would be surprised by a loopback that has no token to inject).
	switch cfg.ToolhiveLLMMode {
	case "proxy":
		return toolhiveIntent{mode: toolhiveModeProxy, baseURL: detected.BaseURL(), gatewayURL: diagnosticGatewayURL}, true
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
				"gateway_url", diagnosticGatewayURL)
			return toolhiveIntent{mode: toolhiveModeProxy, baseURL: detected.BaseURL(), gatewayURL: diagnosticGatewayURL}, true
		}
		return toolhiveIntent{mode: toolhiveModeDirect, baseURL: directBaseURL(detected.GatewayURL), gatewayURL: diagnosticGatewayURL}, true
	default: // "auto" (and any unknown, treated as the default)
		if oidcOK {
			if !gatewayURLIsHTTPS(detected.GatewayURL) {
				cfg.diag().Log(context.Background(), port.LevelWarn,
					"toolhive direct mode: gateway_url is not HTTPS, falling back to proxy mode",
					"gateway_url", diagnosticGatewayURL)
				return toolhiveIntent{mode: toolhiveModeProxy, baseURL: detected.BaseURL(), gatewayURL: diagnosticGatewayURL}, true
			}
			return toolhiveIntent{mode: toolhiveModeDirect, baseURL: directBaseURL(detected.GatewayURL), gatewayURL: diagnosticGatewayURL}, true
		}
		return toolhiveIntent{mode: toolhiveModeProxy, baseURL: detected.BaseURL(), gatewayURL: diagnosticGatewayURL}, true
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
	return deriveGatewayBaseURL(gatewayURL, "v1", false)
}

// toolhiveAnthropicBaseURL derives the base expected by anthropic-sdk-go. A
// normal ToolHive OpenAI base ends in /v1; native Anthropic is its sibling
// /anthropic, because the SDK appends /v1/models or /v1/messages itself. An
// explicit proxy override that does not end in /v1 instead gains a trailing
// /anthropic. In both cases any legitimate path prefix is retained.
func toolhiveAnthropicBaseURL(openAIBaseURL string) string {
	return deriveGatewayBaseURL(openAIBaseURL, "anthropic", true)
}

// deriveGatewayBaseURL joins a protocol segment onto a gateway URL without
// carrying credential-shaped userinfo, a query, or a fragment into request or
// diagnostic URLs. When replaceV1 is true, only a terminal path segment named
// exactly "v1" is replaced; an interior segment is preserved.
func deriveGatewayBaseURL(raw, segment string, replaceV1 bool) string {
	sanitized := sanitizeGatewayURL(raw)
	if sanitized == "" {
		return ""
	}
	u, _ := url.Parse(sanitized)
	u.RawPath = ""
	if replaceV1 {
		clean := strings.TrimRight(u.Path, "/")
		if clean == "/v1" {
			u.Path = ""
		} else if strings.HasSuffix(clean, "/v1") {
			u.Path = strings.TrimSuffix(clean, "/v1")
		} else {
			u.Path = clean
		}
	}
	joined, err := url.JoinPath(u.String(), segment)
	if err != nil {
		return ""
	}
	return joined
}

func sanitizeGatewayURL(raw string) string {
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return ""
	}
	u.User = nil
	u.RawQuery = ""
	u.ForceQuery = false
	u.Fragment = ""
	u.RawFragment = ""
	return u.String()
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
	entry.toolhiveMode = toolhiveModeProxy
	return entry
}

// toolhiveTokenSourceFactory constructs the one OIDC token source shared by
// both protocol-specific ToolHive entries in direct mode.
type toolhiveTokenSourceFactory func(string, port.Diagnostics) (toolhivellm.TokenSourceFunc, error)

// newToolhiveEntries constructs the two protocol-specific providers backed by
// one detected ToolHive gateway identity. The OpenAI entry remains the legacy
// provider/default; the Anthropic entry always uses the native Messages adapter.
func newToolhiveEntries(cfg Config, intent toolhiveIntent, configPath string, meta *liveMetaStore) (providerEntry, providerEntry) {
	if intent.mode == toolhiveModeDirect {
		client := newDirectGatewayClient(cfg, intent, configPath)
		return newDirectGatewayEntry(cfg, providerToolhive, intent, client),
			newToolhiveAnthropicEntry(cfg, intent, meta, client)
	}

	openAILister := gatewayLister{inner: openaicompat.NewLister(
		intent.baseURL, toolhivellm.PlaceholderToken, cfg.liveModelHTTPClient)}
	openAIEntry := newGatewayEntry(cfg, providerToolhive, intent.baseURL,
		intent.gatewayURL, intent.explicit, openAILister)

	// The native SDK must never forward an x-api-key placeholder through the
	// proxy. This transport strips both authentication schemes and adds only the
	// proxy's documented loopback bearer on the cloned request.
	client := newToolhiveBearerClient(cfg.liveModelHTTPClient,
		func(context.Context) (string, error) { return toolhivellm.PlaceholderToken, nil })
	return openAIEntry, newToolhiveAnthropicEntry(cfg, intent, meta, client)
}

// newToolhiveAnthropicEntry wires native Anthropic discovery and inference to
// the same ToolHive route. No API key is supplied to the SDK: the authoritative
// bearer transport owns authentication and strips any conflicting header.
func newToolhiveAnthropicEntry(cfg Config, intent toolhiveIntent, meta *liveMetaStore, client *http.Client) providerEntry {
	baseURL := toolhiveAnthropicBaseURL(intent.baseURL)
	entry := newAnthropicEntryFor(cfg, providerToolhiveAnthropic, "", baseURL, meta, false,
		anthropic.WithRequestOption(
			anthropicoption.WithHTTPClient(client),
			anthropicoption.WithMaxRetries(0),
		))
	entry.lister = anthropicLister{inner: anthropic.NewLister("", baseURL, client)}
	entry.intentDriven = true
	entry.intentGatewayURL = intent.gatewayURL
	entry.intentExplicit = intent.explicit
	entry.toolhiveMode = intent.mode
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
// newDirectGatewayClient builds one token source and one bearer client shared
// by both protocol entries. A per-request Token(ctx) call handles refresh
// internally, so the RoundTripper is stateless across requests. A construction
// failure (config unreadable, secrets provider unavailable) does NOT fail Build:
// it logs ERROR once and installs a token source that returns the cause on every
// request, so the operator learns the reason at the first request instead of a
// startup crash (the proxy-mode §1 deviation — a down gateway must never brick
// Build — applies here too). The live lister is
// paths for both protocols are authenticated by the SAME bearer RoundTripper,
// so discovery and inference share one credential flow.
func newDirectGatewayClient(cfg Config, intent toolhiveIntent, configPath string) *http.Client {
	factory := cfg.toolhiveTokenSourceFactory
	if factory == nil {
		factory = toolhivellm.DirectTokenSource
	}
	tokenSource, err := factory(configPath, cfg.diag())
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
			"provider", providerToolhive, "base_url", intent.baseURL, "error", err.Error())
		tokenSource = func(context.Context) (string, error) { return "", err }
	}
	return newToolhiveBearerClient(cfg.liveModelHTTPClient, tokenSource)
}

func newDirectGatewayEntry(cfg Config, id string, intent toolhiveIntent, client *http.Client) providerEntry {
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
	entry.toolhiveMode = toolhiveModeDirect
	return entry
}

// newToolhiveBearerClient shallow-clones the supplied client so lister tests
// retain their injected transport and timeout while ToolHive always owns the
// redirect and authentication policies. Production supplies nil and receives a
// standard client over http.DefaultTransport.
func newToolhiveBearerClient(baseClient *http.Client, token toolhivellm.TokenSourceFunc) *http.Client {
	client := &http.Client{}
	if baseClient != nil {
		*client = *baseClient
	}
	base := client.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	client.Transport = &bearerRoundTripper{base: base, token: token}
	client.CheckRedirect = openaicompat.RefuseRedirects
	return client
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
	// Clone and remove both SDK authentication schemes before consulting the
	// authoritative source. If token resolution fails, no request is forwarded.
	clone := req.Clone(req.Context())
	clone.Header.Del("Authorization")
	clone.Header.Del("X-Api-Key")
	tok, err := b.token(req.Context())
	if err != nil {
		// The error is already sanitised (no bearer material). Do NOT include
		// the token, the URL's query, or req.Header.
		//
		// Wrap it with llmresilience.ErrCredentials so the resilience layer can
		// tell a credential failure from a provider failure. It cannot otherwise:
		// net/http wraps whatever a RoundTripper returns in *url.Error, which
		// satisfies net.Error, so an unmintable token would be retried
		// MaxAttempts times, counted toward the shared circuit breaker, and then
		// replaced by the breaker's own cooldown error — burying the one message
		// that names the fix (re-run the login) behind an opaque "provider
		// unhealthy" verdict for as long as the credential stays broken.
		return nil, fmt.Errorf("%w: %w", llmresilience.ErrCredentials, err)
	}
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
	statusNotEnrolled  = "not-enrolled"
	statusEmpty        = "empty"
)

// toolhiveStatusHints is the ONE place the remediation-hint copy lives,
// shared by the Build-time probe diagnostics, the v1 provider_status
// projection (providerStatusProto), and the public model-selection guide's
// troubleshooting table — so the three surfaces cannot drift on wording.
var toolhiveStatusHints = map[string]string{
	statusUnreachable:  "start it with `thv llm proxy start`",
	statusUnauthorized: "re-auth with `thv llm setup`",
	statusEmpty:        "your ToolHive gateway credential lists no models — ask your platform admin or re-run `thv llm setup`",
}

var toolhiveDirectStatusHints = map[string]string{
	statusUnreachable:  "check gateway connectivity or use `--toolhive-llm-mode proxy`",
	statusUnauthorized: "re-auth with `mecatui providers login toolhive` or `thv llm setup`",
	statusEmpty:        toolhiveStatusHints[statusEmpty],
}

func isToolhiveProvider(pid string) bool {
	return pid == providerToolhive || pid == providerToolhiveAnthropic
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

// customProviderStatusHints is deliberately GENERIC — mecatl knows nothing
// vendor-specific about an operator-defined custom provider's gateway, so
// unlike the ToolHive/Codex tables above this names no specific remedy
// command. It exists because a custom provider's live-listing failure is the
// one case where the operator's own credential can be entirely correct (it
// works for inference) while listing still 401s: many OpenAI-compatible
// gateways authorize or even implement the listing endpoint differently from
// the completion endpoint. The hint names that possibility plus the concrete
// escape hatch (models.context_windows) rather than leaving the operator with
// only the bare state string.
var customProviderStatusHints = map[string]string{
	statusUnreachable:  "could not reach this provider's model-listing endpoint; check the base URL, or set an exact models.context_windows override if listing is not supported",
	statusUnauthorized: "this provider rejected the API key for model listing (it may still be valid for inference — some gateways authorize listing separately); verify the key or set an exact models.context_windows override",
	statusEmpty:        "this provider's model-listing endpoint returned no models; set an exact models.context_windows override if listing is not expected to work",
}

// statusHintFor returns provider-specific remediation for ToolHive, Codex, and
// operator-defined custom providers (entry.defaultModel != "" — see
// providerEntry.defaultModel). It consumes the whole entry so ToolHive routing
// mode is structural data, not something callers infer from the resulting
// prose. Ordinary built-in provider outages (for example OpenRouter) get "".
// Keep each vendor's copy in its own table so gateway and manual-token
// remedies cannot cross-contaminate.
//
// Case order matters: the ToolHive/Codex identity cases MUST stay ahead of the
// entry.defaultModel != "" case. That is safe today only because no ToolHive
// or Codex construction path (newToolhiveEntry* / the Codex entry builder)
// ever sets providerEntry.defaultModel — it is populated solely by
// newNativeProviderEntry/newCustomProviderEntry. If a future change ever set
// defaultModel on a built-in entry, it would silently fall through to the
// generic custom-provider hint instead of its vendor-specific one.
func statusHintFor(entry providerEntry, state string) string {
	switch {
	case entry.id == providerToolhive || entry.id == providerToolhiveAnthropic:
		if entry.toolhiveMode == toolhiveModeDirect {
			return toolhiveDirectStatusHints[state]
		}
		return toolhiveStatusHints[state]
	case entry.id == providerOpenAICodex:
		return openAICodexStatusHints[state]
	case entry.defaultModel != "":
		return customProviderStatusHints[state]
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
// snapshot (via the discovery owner), and default-model eligibility (D2). A nil/missing
// toolhive entry is a no-op (every non-ToolHive Build pays one map lookup).
//
// Returns errToolhiveNoModels ONLY when the probe succeeded, returned zero
// models, AND toolhive is the registry's resolved DEFAULT provider (R2.3) —
// every other outcome is diagnosed, never fatal (the §1 accepted deviation: a
// down/unauthorized proxy must never brick Build when toolhive is sole).
func probeToolhive(reg *providerRegistry, cfg Config) error {
	var waits sync.WaitGroup
	for _, pid := range []string{providerToolhive, providerToolhiveAnthropic} {
		if entry, ok := reg.Lookup(pid); ok && entry.lister != nil {
			waits.Go(func() { _, _ = reg.discovery.request(context.Background(), pid, discoveryBootstrap) })
		}
	}
	waits.Wait()
	view := reg.discovery.snapshot()
	for _, pid := range []string{providerToolhive, providerToolhiveAnthropic} {
		state := view.providers[pid]
		if state.outcome.State == "" {
			continue
		}
		entry, _ := reg.Lookup(pid)
		if state.outcome.State == statusOK {
			cfg.diag().Log(context.Background(), port.LevelInfo, "toolhive LLM gateway: registered and reachable",
				"provider", pid, "base_url", entry.baseURL, "gateway_url", entry.intentGatewayURL, "models", len(state.observations))
		} else {
			level := port.LevelInfo
			if entry.intentExplicit || state.outcome.State == statusUnauthorized || state.outcome.State == statusEmpty {
				level = port.LevelWarn
			}
			hint := state.outcome.Hint
			if state.outcome.State != statusEmpty {
				hint = "probe failed — " + hint
			}
			cfg.diag().Log(context.Background(), level, "toolhive LLM gateway: "+hint,
				"provider", pid, "base_url", entry.baseURL, "state", state.outcome.State)
		}
		if pid == reg.Default() && state.outcome.State == statusEmpty {
			return errToolhiveNoModels
		}
	}
	return nil
}
