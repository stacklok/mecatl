// Package openai implements port.LLMProvider over the OpenAI Responses API
// (POST /v1/responses) using github.com/openai/openai-go/v3. It names the
// PROTOCOL, not a single vendor: this one adapter serves the composition
// registry's "openai", "openrouter", AND (issue #262) the intent-driven
// ToolHive LLM gateway registry entries — each is the same Responses-API
// wire protocol with a different base URL + credential, so registering one
// more OpenAI-compatible endpoint here never touches the OpenAI/Anthropic SDK
// boundary.
//
// The harness owns its own conversation state (the brief's "strategy B"): every
// request is stateless (Store:false, no previous_response_id) and resends the
// full input item slice, with reasoning items carried forward verbatim and
// reasoning.encrypted_content requested via Include so reasoning survives across
// turns. Tools are sent as function tools with their JSON schemas. The two-layer
// system prompt is rendered into Instructions.
//
// The streaming SSE events are translated into provider-neutral port.Chunk
// values by the pure translate function, which is exercised directly from
// recorded fixtures in tests; no network is required to test the translation.
package openai

import (
	"context"
	"errors"
	"iter"
	"net/http"
	"slices"
	"strings"
	"sync/atomic"

	oai "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/responses"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/provider/ssefilter"
)

// Provider is a port.LLMProvider backed by the OpenAI Responses API. Construct
// it with New.
type Provider struct {
	client responses.ResponseService
	// effort is the reasoning-effort token stamped on every request's
	// reasoning.effort field (ADR 0055). Empty (and "auto") means OMIT the field
	// entirely — the provider's own default applies, so a non-reasoning endpoint is
	// never sent an effort it would reject. Composition supplies an ALREADY-CLAMPED
	// neutral token (the openai xhigh/max→high clamp + its diagnostic live in
	// composition, which has port.Diagnostics — this adapter does not); the adapter
	// maps a recognised value verbatim and OMITS on anything else (fail-soft).
	effort string
	// caps is the per-SESSION input-capability intersection (catalog ∩ adapter)
	// the request builder consults when projecting a tool result's typed Parts
	// (T7): port.RouteToolResultParts drops image/audio blocks the (provider, model)
	// cannot receive. nil (the Option unset) DEGRADES to the adapter's own static
	// Capabilities() — so a provider constructed without the Option (tests, the
	// byte-identical default path) behaves exactly as before. It is DISTINCT from
	// the static Capabilities() port method (the adapter transmit authority
	// modelCapability ANDs with the catalog): a pointer so a deliberately text-only
	// (zero-value) intersection is distinguishable from "unset". A tool result with
	// no Parts always takes the legacy string path regardless.
	caps *port.ProviderCapabilities
	// cacheDialect selects which provider-side prompt-cache wire dialect
	// (ADR 0100) buildParams (method) emits. "" (CacheDialectNone, the zero
	// value) emits no cache hints at all — the byte-identical pre-ADR-0100
	// wire.
	cacheDialect CacheDialect
	// cacheMemo memoises the last-seen (StablePrefix, hash) pair for
	// promptCacheKey — see cachekey.go. A pointer (not embedded by value) so
	// the zero-value Provider needs no initialisation.
	cacheMemo atomic.Pointer[prefixMemo]
	// providerPrefs resolves the OpenRouter downstream-provider routing object
	// for a request's model (issue #480); nil for every non-openrouter entry, so
	// the `provider` body key is only ever stamped for OpenRouter.
	providerPrefs func(model string) *OpenRouterProviderPreferences
	// metadataHeader arms the X-OpenRouter-Metadata: enabled header so OpenRouter
	// returns the routed-downstream metadata block; gated to the openrouter entry.
	metadataHeader bool
}

// Option configures a Provider.
type Option func(*config)

type config struct {
	apiKey         string
	baseURL        string
	effort         string
	caps           *port.ProviderCapabilities
	extra          []option.RequestOption
	httpClient     *http.Client
	cacheDialect   CacheDialect
	providerPrefs  func(model string) *OpenRouterProviderPreferences
	metadataHeader bool
}

// WithAPIKey sets the API key used to authenticate requests.
func WithAPIKey(key string) Option {
	return func(c *config) { c.apiKey = key }
}

// WithBaseURL overrides the API host so OpenAI-compatible endpoints (vLLM,
// LiteLLM, a local proxy, ...) can be targeted. The SDK appends "/responses".
func WithBaseURL(url string) Option {
	return func(c *config) { c.baseURL = url }
}

// WithReasoningEffort sets the reasoning-effort token stamped on every request's
// reasoning.effort field (ADR 0055). The value is a NEUTRAL composition token,
// ALREADY CLAMPED for OpenAI (xhigh/max are clamped to high in composition, with a
// diagnostic, because this adapter has no port.Diagnostics). Empty (and "auto")
// OMITS the field — the provider default applies. It is an adapter-CONSTRUCTION
// Option, not a port.LLMRequest field, so the provider stays neutral; the
// per-session engine factory re-mints the adapter when the session's effort
// differs from the operator default (the same factory discipline as the per-call
// model override).
func WithReasoningEffort(effort string) Option {
	return func(c *config) { c.effort = effort }
}

// WithProviderCapabilities sets the per-SESSION input-capability intersection
// (the catalog ∩ adapter value composition computes via modelCapability) the
// request builder consults when projecting a tool result's typed Parts (T7). It
// is an adapter-CONSTRUCTION Option, not a port.LLMRequest field — the per-
// session engine factory re-mints the adapter (alongside reasoning effort) when
// the session's resolved (provider, model) carries a DIFFERENT intersection than
// the operator-default model the shared provider was built with; the default
// path (same model) reuses the shared provider byte-for-byte. When unset, the
// builder degrades to the adapter's own static Capabilities() — byte-identical
// to the pre-T7 path, and a tool result with no Parts always takes the legacy
// single-string function_call_output regardless. A deliberately text-only
// (zero-value) caps is distinct from unset (nil).
func WithProviderCapabilities(caps port.ProviderCapabilities) Option {
	return func(c *config) {
		cc := caps
		c.caps = &cc
	}
}

// WithHTTPClient sets the final *http.Client the SDK issues requests through
// (e.g. a redirect-refusing or policy-guarded client). It is deliberately
// applied after arbitrary WithRequestOption values, so a late generic SDK
// option cannot replace and bypass a security transport. nil is ignored (SDK
// default). NOTE for callers: do NOT set Client.Timeout here — a streaming turn
// runs for minutes; establishment/idle bounds live in llmresilience, not the
// transport's blanket deadline.
func WithHTTPClient(c *http.Client) Option {
	return func(cfg *config) {
		if c != nil {
			cfg.httpClient = c
		}
	}
}

// WithMaxRetries configures the openai-go retry loop. Composition uses zero
// when llmresilience is the sole retry owner or when a dynamic credential
// source must not be called multiple times inside one outer attempt.
func WithMaxRetries(retries int) Option {
	return func(c *config) { c.extra = append(c.extra, option.WithMaxRetries(retries)) }
}

// WithRequestOption threads an arbitrary openai-go request option through to the
// client (e.g. option.WithHeader, option.WithMaxRetries). Multiple are applied
// in order, after the API key and base URL.
func WithRequestOption(opts ...option.RequestOption) Option {
	return func(c *config) { c.extra = append(c.extra, opts...) }
}

// OpenRouterProviderPreferences is the OpenRouter DOWNSTREAM-provider routing object
// (issue #480) stamped onto the request body's `provider` key. It is the
// adapter-local, provider-private mirror of OpenRouter's ProviderPreferences
// schema — v1 carries only Order + AllowFallbacks. It is NOT a port.LLMRequest
// field: the request stays provider-neutral and the knob is minted per model at
// adapter construction (the remint discipline).
//
// Order lists downstream provider slugs (lowercase-kebab, e.g. "anthropic",
// "google-vertex", "deepinfra/turbo") tried in order; setting it disables
// OpenRouter's default price load-balancing. AllowFallbacks is a POINTER so
// "absent" (OpenRouter default true) is distinguishable from an explicit false
// (pin hard to Order, no fallback). Base-slug matching applies: "google-vertex"
// matches all its regions/variants (service tiers excepted).
type OpenRouterProviderPreferences struct {
	Order          []string
	AllowFallbacks *bool
}

// WithOpenRouterProviderPreferences sets a resolve-at-request closure keyed on the
// request's model id, returning the downstream-provider routing object for that
// model (nil = send nothing). The per-model closure shape exists because the
// registry entry is shared across models while the config is per-model. It is
// gated to the openrouter registry entry in COMPOSITION — every other entry
// passes no Option, so the `provider` key can never leak to a non-OpenRouter
// endpoint. The body key is injected via option.WithJSONSet at Stream time, so
// buildParams and the byte-stable prompt-cache prefix are untouched.
func WithOpenRouterProviderPreferences(resolve func(model string) *OpenRouterProviderPreferences) Option {
	return func(c *config) { c.providerPrefs = resolve }
}

// WithOpenRouterMetadata arms the `X-OpenRouter-Metadata: enabled` request header,
// which makes OpenRouter return the openrouter_metadata block (naming the routed
// downstream provider) on the terminal streaming event. It is a SEPARATE Option
// from WithOpenRouterProviderPreferences so the routing echo works even with no order
// configured. Gated to the openrouter registry entry in composition. The adapter
// never logs; the value reaches the loop as ChunkProviderRoute.
func WithOpenRouterMetadata(enabled bool) Option {
	return func(c *config) { c.metadataHeader = enabled }
}

// New constructs a Provider. At minimum supply WithAPIKey; add WithBaseURL for
// compatible endpoints.
func New(opts ...Option) *Provider {
	var c config
	for _, o := range opts {
		o(&c)
	}
	reqOpts := make([]option.RequestOption, 0, len(c.extra)+3)
	// FIRST, so it is the OUTERMOST middleware and therefore filters the body that
	// the SDK's ssestream decoder ultimately reads. See provider/ssefilter
	// for why an SSE keepalive would otherwise kill a streaming turn outright. The
	// same guard the openaichat (Chat Completions) adapter installs applies here:
	// responses.NewStreaming drives the plain ssestream decoder, so it hits the
	// identical empty-payload json.Unmarshal defect.
	reqOpts = append(reqOpts, option.WithMiddleware(ssefilter.NewKeepaliveFilter()))
	if c.apiKey != "" {
		reqOpts = append(reqOpts, option.WithAPIKey(c.apiKey))
	}
	if c.baseURL != "" {
		reqOpts = append(reqOpts, option.WithBaseURL(c.baseURL))
	}
	reqOpts = append(reqOpts, c.extra...)
	if c.httpClient != nil {
		// LAST: a generic request option must not bypass a guarded transport.
		reqOpts = append(reqOpts, option.WithHTTPClient(c.httpClient))
	}

	client := oai.NewClient(reqOpts...)
	return &Provider{
		client:         client.Responses,
		effort:         c.effort,
		caps:           c.caps,
		cacheDialect:   c.cacheDialect,
		providerPrefs:  c.providerPrefs,
		metadataHeader: c.metadataHeader,
	}
}

// Stream issues a streaming Responses request and yields provider-neutral
// chunks. The returned iterator translates each SSE event via translate; it
// stops (abandoning the underlying stream) when ctx is cancelled, and surfaces a
// terminal transport error as the iterator's error. The outer error is reserved
// for a failure to construct the request parameters.
//
// A rejection of the replayed encrypted reasoning is REPAIRED once, before any
// chunk has gone out: see withoutEncryptedReasoning. Everything else — an
// ordinary 4xx, a post-commit failure, a cancellation — stays terminal.
func (p *Provider) Stream(ctx context.Context, req port.LLMRequest) (iter.Seq2[port.Chunk, error], error) {
	params, err := p.buildParams(req)
	if err != nil {
		return nil, err
	}

	// Per-REQUEST OpenRouter routing options (issue #480). These are request
	// options, NOT buildParams output: the `provider` key is routing metadata, so
	// the params struct and the byte-stable prompt-cache prefix stay untouched.
	// Both the initial attempt and the encrypted-reasoning fallback carry them.
	reqOpts := p.routingRequestOptions(req.Model)

	return func(yield func(port.Chunk, error) bool) {
		emitted, stopped, streamErr := p.streamAttempt(ctx, params, reqOpts, yield)
		if stopped || streamErr == nil || ctx.Err() != nil {
			return
		}

		// Encrypted reasoning is intentionally opaque and normally replayed verbatim.
		// A provider may nevertheless reject a blob it previously returned. Recovery is
		// safe only before the attempt emitted any neutral chunk: retry once with just
		// the encrypted reasoning items removed, retaining visible history, tool
		// calls/results, provider phases, and provider-assigned tool item IDs. Ordinary
		// 4xx errors and post-commit failures remain terminal.
		fallbackReq, hasEncryptedReasoning := withoutEncryptedReasoning(req)
		if emitted || !hasEncryptedReasoning || !isInvalidEncryptedContent(streamErr) {
			yield(port.Chunk{}, streamErr)
			return
		}
		fallbackParams, buildErr := p.buildParams(fallbackReq)
		if buildErr != nil {
			yield(port.Chunk{}, buildErr)
			return
		}
		_, stopped, streamErr = p.streamAttempt(ctx, fallbackParams, reqOpts, yield)
		if !stopped && streamErr != nil && ctx.Err() == nil {
			yield(port.Chunk{}, &encryptedReasoningFallbackError{err: streamErr})
		}
	}, nil
}

// routingRequestOptions builds the OpenRouter per-request options for model: the
// `provider` body key (when providerPrefs resolves one) and the
// X-OpenRouter-Metadata header (when armed). It returns nil for every
// non-openrouter entry (both knobs unset), so the openai/anthropic/toolhive wire
// is byte-identical to before — the routing object can never leak to a
// non-OpenRouter endpoint. WithJSONSet mutates the marshalled body buffer (an
// SDK-sanctioned escape hatch), leaving buildParams' params struct clean.
func (p *Provider) routingRequestOptions(model string) []option.RequestOption {
	var opts []option.RequestOption
	if p.providerPrefs != nil {
		if prefs := p.providerPrefs(model); prefs != nil {
			body := map[string]any{}
			if len(prefs.Order) > 0 {
				body["order"] = prefs.Order
			}
			if prefs.AllowFallbacks != nil {
				body["allow_fallbacks"] = *prefs.AllowFallbacks
			}
			if len(body) > 0 {
				opts = append(opts, option.WithJSONSet("provider", body))
			}
		}
	}
	if p.metadataHeader {
		opts = append(opts, option.WithHeader("X-OpenRouter-Metadata", "enabled"))
	}
	return opts
}

// encryptedReasoningFallbackError marks a failed cleaned fallback as terminal for
// an outer resilience decorator. The original failure remains unwrap-visible for
// diagnostics and errors.As, but another whole-request retry would repeat both the
// rejected replay and its already-spent repair attempt.
type encryptedReasoningFallbackError struct {
	err error
}

func (e *encryptedReasoningFallbackError) Error() string { return e.err.Error() }
func (e *encryptedReasoningFallbackError) Unwrap() error { return e.err }
func (*encryptedReasoningFallbackError) Retryable() bool { return false }

// streamAttempt performs one Responses streaming attempt. emitted means at least one
// provider-neutral chunk was handed to the caller; stopped means the caller declined a
// chunk. An HTTP/SSE/clean-EOF failure is returned rather than yielded so Stream can make
// the single pre-commit encrypted-reasoning recovery decision in one place.
func (p *Provider) streamAttempt(ctx context.Context, params responses.ResponseNewParams, reqOpts []option.RequestOption, yield func(port.Chunk, error) bool) (emitted, stopped bool, err error) {
	stream := p.client.NewStreaming(ctx, params, reqOpts...)
	defer func() { _ = stream.Close() }()

	st := streamState{providerRoute: p.metadataHeader}
	for stream.Next() {
		if ctx.Err() != nil {
			return emitted, false, nil
		}
		chunks, terr := translate(stream.Current(), &st)
		for _, c := range chunks {
			emitted = true
			if !yield(c, nil) {
				return emitted, true, nil
			}
		}
		if terr != nil {
			// A terminal failure event (response.failed / error / incomplete)
			// carries the provider's real message; surface it as the stream's
			// error so the loop reports the reason rather than a bare stop.
			return emitted, false, terr
		}
	}
	if streamErr := stream.Err(); streamErr != nil {
		// Don't report a plain context cancellation as a stream error; the
		// caller cancelled deliberately.
		if ctx.Err() != nil {
			return emitted, false, nil
		}
		return emitted, false, streamErr
	}
	if !st.done && ctx.Err() == nil {
		// Clean EOF but NO terminal Responses event: the SDK's ssestream
		// returns Err()==nil on a plain mid-stream EOF, so a dropped connection
		// is indistinguishable from a normal close here. FAIL CLOSED — do NOT let
		// this fall through as a benign end, or the engine promotes the partial
		// text to a successful StopEndTurn (loop.go finishTurnNoTools). Surface
		// a truncation error instead (retryable pre-commit; terminal once a
		// committing chunk has gone out, by the no-replay rule).
		return emitted, false, errTruncatedStream
	}
	return emitted, false, nil
}

// withoutEncryptedReasoning clones the request with every REASONING replay
// envelope removed and nothing else touched. The visible messages, tool calls and
// results, assistant phase markers, and provider-assigned function-call item IDs
// all survive: dropping those would trade one rejection for two other known
// failures (GPT-5.x treats a phase-less preamble as a final answer and stops
// early; a phase-less function_call collides as "Duplicate item found with id
// fc_N"). The caller's request is never mutated.
//
// The bool reports whether anything was actually removed, so a 400 on a request
// that carried no reasoning envelope cannot unlock a hidden retry.
// "Carried one" is decided by the SAME unpack the wire projection uses
// (assistantItems), so the two can never disagree about whether an envelope
// exists — a packed multi-item blob, a legacy (blob, id) pair, and a partial
// state that would already have been omitted are each classified identically
// here and there.
func withoutEncryptedReasoning(req port.LLMRequest) (port.LLMRequest, bool) {
	messages := slices.Clone(req.Messages)
	found := false
	for i := range messages {
		if len(unpackReasoningItems(messages[i].Reasoning, messages[i].ReasoningItemID)) == 0 {
			continue
		}
		found = true
		messages[i].Reasoning = ""
		messages[i].ReasoningItemID = ""
	}
	if !found {
		return req, false
	}
	req.Messages = messages
	return req, true
}

// isInvalidEncryptedContent reports whether a provider error is the specific
// refusal to verify replayed encrypted reasoning. It gates a retry that silently
// discards a turn's reasoning, so it is deliberately narrow: a typed 400 with the
// invalid_encrypted_content code, or a 400 whose message carries the
// verification/decryption phrasing. A neighbouring 400 must not match.
func isInvalidEncryptedContent(err error) bool {
	var apiErr *oai.Error
	if errors.As(err, &apiErr) {
		if apiErr.StatusCode != http.StatusBadRequest {
			return false
		}
		if apiErr.Code == "invalid_encrypted_content" {
			return true
		}
		if hasInvalidEncryptedDetail(apiErr.Message) {
			return true
		}
	}

	// Some Responses-compatible gateways return a non-standard root JSON object,
	// leaving the SDK's typed Message/Code empty while retaining the body in Error().
	msg := err.Error()
	return strings.Contains(strings.ToLower(msg), "400 bad request") && hasInvalidEncryptedDetail(msg)
}

// hasInvalidEncryptedDetail matches the human-readable half of the rejection, for
// an upstream that relays the message without the structured code.
func hasInvalidEncryptedDetail(msg string) bool {
	m := strings.ToLower(msg)
	return strings.Contains(m, "encrypted content") &&
		(strings.Contains(m, "could not be verified") || strings.Contains(m, "could not be decrypted"))
}

// Capabilities reports the provider's multimodal input support — the ADAPTER
// TRANSMIT authority composition's modelCapability intersects with the catalog
// (catalog ∩ adapter). It is STATIC (the OpenAI Responses input-message content
// union supports text + image + file but has NO audio member, openai-go v3.37.0,
// so Audio is false; Image is true; EmbeddedContext is true because inline text
// flattens into the input_text content part). It does NOT reflect the per-
// session intersection — that lives on p.caps (set via WithProviderCapabilities)
// and is consulted ONLY by the request builder's tool-result projection; the
// port method stays the static transmit authority so modelCapability's AND stays
// honest.
func (*Provider) Capabilities() port.ProviderCapabilities {
	return port.ProviderCapabilities{Image: true, Audio: false, EmbeddedContext: true}
}

// sessionCaps returns the per-session capability intersection the request
// builder consults for tool-result Part projection: the composition-set value
// (WithProviderCapabilities) when present, else the adapter's static transmit
// Capabilities() (the byte-identical pre-T7 default).
func (p *Provider) sessionCaps() port.ProviderCapabilities {
	if p.caps != nil {
		return *p.caps
	}
	return p.Capabilities()
}

// Compile-time assertion that Provider satisfies the port.
var _ port.LLMProvider = (*Provider)(nil)
