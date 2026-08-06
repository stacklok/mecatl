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
	"iter"
	"net/http"
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
}

// Option configures a Provider.
type Option func(*config)

type config struct {
	apiKey       string
	baseURL      string
	effort       string
	caps         *port.ProviderCapabilities
	extra        []option.RequestOption
	cacheDialect CacheDialect
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

// WithHTTPClient sets the *http.Client the SDK issues requests through (e.g. a
// redirect-refusing client for a loopback gateway endpoint, CWE-918). nil is
// ignored (SDK default). NOTE for callers: do NOT set Client.Timeout here — a
// streaming turn runs for minutes; establishment/idle bounds live in
// llmresilience, not the transport's blanket deadline.
func WithHTTPClient(c *http.Client) Option {
	return func(cfg *config) {
		if c != nil {
			cfg.extra = append(cfg.extra, option.WithHTTPClient(c))
		}
	}
}

// WithRequestOption threads an arbitrary openai-go request option through to the
// client (e.g. option.WithHeader, option.WithMaxRetries). Multiple are applied
// in order, after the API key and base URL.
func WithRequestOption(opts ...option.RequestOption) Option {
	return func(c *config) { c.extra = append(c.extra, opts...) }
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

	client := oai.NewClient(reqOpts...)
	return &Provider{client: client.Responses, effort: c.effort, caps: c.caps, cacheDialect: c.cacheDialect}
}

// Stream issues a streaming Responses request and yields provider-neutral
// chunks. The returned iterator translates each SSE event via translate; it
// stops (abandoning the underlying stream) when ctx is cancelled, and surfaces a
// terminal transport error as the iterator's error. The outer error is reserved
// for a failure to construct the request parameters.
func (p *Provider) Stream(ctx context.Context, req port.LLMRequest) (iter.Seq2[port.Chunk, error], error) {
	params, err := p.buildParams(req)
	if err != nil {
		return nil, err
	}

	stream := p.client.NewStreaming(ctx, params)

	return func(yield func(port.Chunk, error) bool) {
		defer func() { _ = stream.Close() }()
		var st streamState
		for stream.Next() {
			select {
			case <-ctx.Done():
				return
			default:
			}
			event := stream.Current()
			chunks, terr := translate(event, &st)
			for _, c := range chunks {
				if !yield(c, nil) {
					return
				}
			}
			if terr != nil {
				// A terminal failure event (response.failed / error / incomplete)
				// carries the provider's real message; surface it as the stream's
				// error so the loop reports the reason rather than a bare stop.
				yield(port.Chunk{}, terr)
				return
			}
		}
		if err := stream.Err(); err != nil {
			// Don't report a plain context cancellation as a stream error; the
			// caller cancelled deliberately.
			if ctx.Err() != nil {
				return
			}
			yield(port.Chunk{}, err)
			return
		}
		if !st.done {
			// Clean EOF but NO terminal Responses event: the SDK's ssestream
			// returns Err()==nil on a plain mid-stream EOF, so a dropped connection
			// is indistinguishable from a normal close here. FAIL CLOSED — do NOT let
			// this fall through as a benign end, or the engine promotes the partial
			// text to a successful StopEndTurn (loop.go finishTurnNoTools). Surface
			// a truncation error instead (retryable pre-commit; terminal once a
			// committing chunk has gone out, by the no-replay rule).
			if ctx.Err() != nil {
				return
			}
			yield(port.Chunk{}, errTruncatedStream)
		}
	}, nil
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
