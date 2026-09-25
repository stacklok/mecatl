package anthropic

import (
	"context"
	"encoding/json"
	"errors"
	"iter"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	sdk "github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

// defaultMaxTokens is the CONSERVATIVE flat fallback for the REQUIRED max_tokens
// param: the LOWEST common Claude output ceiling (claude-3-opus / claude-3-haiku
// = 4096), so a request for an UNCATALOGUED model never 400s on a too-high value.
// Anthropic requires max_tokens and rejects a value above the model's real output
// limit, so the per-REQUEST value is resolved per req.Model (maxTokensFor), not
// baked in — see WithMaxTokensResolver.
const (
	defaultMaxTokens    int64 = 4096
	sessionIDHeaderName       = "X-Mecatl-Session-ID"
)

// defaultThinkingBudget is the default budget_tokens used for the manual
// (type:"enabled") thinking config on older thinking-capable model families. It
// must be ≥1024 and strictly less than max_tokens; the request builder clamps it
// below max_tokens. Adaptive-thinking models ignore it entirely (no budget
// field). Override with WithThinkingBudget.
const defaultThinkingBudget int64 = 4096

// maxTokensResolver maps a model id to its real max output ceiling. Composition
// builds it from the catalog's per-model output limit (the adapter stays
// catalog-free); a nil resolver, or a model the resolver does not know (returns
// ≤0), falls back to the construction default. This is the per-REQUEST
// max_tokens source — critical so a per-session/sub-agent route to a
// smaller-ceiling model (e.g. claude-3-5-haiku=8192) does not send the DEFAULT
// model's larger ceiling and 400 every turn.
type maxTokensResolver func(model string) int

// thinkingResolver maps a model id to its LIVE extended-thinking descriptor:
// (adaptive, enabled, known). Composition builds it over the live-metadata store
// (the adapter stays catalog-/store-free). A nil resolver — or known=false for a
// model the live source does not describe — falls the adapter back to its embedded
// prefix matrix (usesAdaptiveThinking/thinkingCapable), the OFFLINE floor. When
// known=true the adapter TRUSTS the live bits: adaptive ⇒ {type:"adaptive"},
// else enabled ⇒ manual {type:"enabled"}, else NEITHER ⇒ omit thinking (NONE).
type thinkingResolver func(model string) (adaptive, enabled, known bool)

// Provider is a port.LLMProvider backed by the native Anthropic Messages API.
// Construct it with New.
type Provider struct {
	client         sdk.MessageService
	maxTokens      int64             // construction fallback (WithMaxTokens / default)
	maxTokensFor   maxTokensResolver // per-request resolver (WithMaxTokensResolver)
	thinkingFor    thinkingResolver  // per-request LIVE thinking descriptor (WithThinkingResolver)
	thinkingBudget int64
	// effort is the reasoning-effort token stamped on every request's
	// output_config.effort field (ADR 0055). Empty (and "auto") means OMIT the
	// field entirely (the model default applies). Anthropic's output_config.effort
	// is INDEPENDENT of the extended-thinking config (both coexist); identity-maps
	// low/medium/high/xhigh/max. It is an adapter-CONSTRUCTION knob, not a
	// port.LLMRequest field; the per-session engine factory re-mints the adapter
	// when a session's effort differs from the operator default.
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
	// no Parts always takes the legacy single-string path regardless.
	caps *port.ProviderCapabilities
	// conversationCaching gates the three NEW conversation cache_control
	// breakpoints (ADR 0100): the two conditional conversation anchors (the
	// leading-turn-0-fragment boundary and the previous-turn boundary) plus the
	// top-level automatic marker. It does NOT gate the pre-existing StablePrefix
	// breakpoint in buildSystem, which shipped before this feature and stays
	// unconditional. Default true (WithConversationCaching unset) — disabling via
	// WithConversationCaching(false) (wired from --no-prompt-cache) reproduces the
	// pre-change wire exactly.
	conversationCaching bool
	// cacheTTL is the raw TTL token (mirrors effort) stamped on EVERY breakpoint
	// the adapter emits — the StablePrefix marker, the two conditional
	// conversation anchors, and the top-level automatic marker all carry the SAME
	// ttl (the uniform-TTL rule, ADR 0100). "" (the default) omits the ttl field
	// everywhere (the API's own 5m default applies), byte-identical to today.
	// Mapped per-request via cacheTTLFor; an unrecognised token degrades to ""
	// fail-soft, mirroring outputConfigEffortFor's omit-on-unknown arm.
	cacheTTL string
}

// Option configures a Provider.
type Option func(*config)

type config struct {
	apiKey                     string
	baseURL                    string
	maxTokens                  int64
	maxTokensFor               maxTokensResolver
	thinkingFor                thinkingResolver
	thinkingBudget             int64
	effort                     string
	caps                       *port.ProviderCapabilities
	extra                      []option.RequestOption
	disableConversationCaching bool
	cacheTTL                   string
}

// WithAPIKey sets the API key used to authenticate requests (the x-api-key
// header). The SDK sets anthropic-version automatically.
func WithAPIKey(key string) Option {
	return func(c *config) { c.apiKey = key }
}

// WithBaseURL overrides the API host so compatible/proxy endpoints (a gateway,
// Bedrock/Vertex-style fronting) can be targeted. The SDK appends "/v1/messages".
func WithBaseURL(url string) Option {
	return func(c *config) { c.baseURL = url }
}

// WithMaxTokens sets the construction-FALLBACK max_tokens used only when the
// per-request resolver (WithMaxTokensResolver) is absent or does not know the
// request model. An unset/zero value falls back to defaultMaxTokens (the lowest
// common Claude ceiling). Prefer WithMaxTokensResolver so each request's
// max_tokens reflects ITS model's real output ceiling.
func WithMaxTokens(n int64) Option {
	return func(c *config) { c.maxTokens = n }
}

// WithMaxTokensResolver injects the per-model max-output-ceiling resolver.
// Composition builds it from the catalog so each request sends a max_tokens that
// matches req.Model's real ceiling (never the default model's larger value).
// A nil resolver — or a model it returns ≤0 for — falls back to WithMaxTokens /
// defaultMaxTokens. Keeps the adapter catalog-free (the catalog stays in
// composition).
func WithMaxTokensResolver(resolve maxTokensResolver) Option {
	return func(c *config) { c.maxTokensFor = resolve }
}

// WithThinkingResolver injects the per-model LIVE extended-thinking descriptor
// resolver. Composition builds it over the live-metadata store (which carries
// Anthropic's Capabilities.Thinking.Types), so a request's thinking mode reflects
// the model's TRUE capability rather than a stale id-prefix guess. A nil resolver —
// or known=false for a model the live source does not describe — falls back to the
// adapter's embedded prefix matrix (the OFFLINE floor); the prefix lists are NOT
// removed. Keeps the adapter catalog-/store-free (the store stays in composition).
func WithThinkingResolver(resolve thinkingResolver) Option {
	return func(c *config) { c.thinkingFor = resolve }
}

// WithThinkingBudget sets budget_tokens for the manual (type:"enabled") thinking
// config used on older model families (Sonnet 4.5, Opus 4.5, Haiku 4.5 and
// earlier). It is ignored by adaptive-thinking models (Opus 4.8/4.7/4.6, Sonnet
// 4.6). The builder clamps it to ≥1024 and strictly below max_tokens.
func WithThinkingBudget(n int64) Option {
	return func(c *config) { c.thinkingBudget = n }
}

// WithReasoningEffort sets the reasoning-effort token stamped on every request's
// output_config.effort field (ADR 0055). The value is a NEUTRAL composition token;
// Anthropic identity-maps all five tiers (low/medium/high/xhigh/max). Empty (and
// "auto") OMITS the field — the model default applies. It is INDEPENDENT of the
// extended-thinking config (WithThinkingBudget / WithThinkingResolver) — both
// coexist on the request. It is an adapter-CONSTRUCTION Option, not a
// port.LLMRequest field, so the provider stays neutral; the per-session engine
// factory re-mints the adapter when a session's effort differs from the operator
// default (the same factory discipline as the per-call model override).
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
// single-string tool_result block regardless. A deliberately text-only
// (zero-value) caps is distinct from unset (nil).
func WithProviderCapabilities(caps port.ProviderCapabilities) Option {
	return func(c *config) {
		cc := caps
		c.caps = &cc
	}
}

// WithConversationCaching toggles the three NEW conversation cache_control
// breakpoints (ADR 0100): the two conditional conversation anchors — the
// leading-turn-0-fragment boundary and the previous-turn boundary — plus the
// top-level automatic marker (MessageNewParams.CacheControl, which self-
// advances to the last cacheable block on every turn). It does NOT gate the
// pre-existing StablePrefix breakpoint in buildSystem, which shipped before
// this feature and stays unconditional. Default (Option unset) is enabled;
// pass false (wired from --no-prompt-cache) to reproduce the pre-change wire
// exactly — the byte-identical escape hatch.
func WithConversationCaching(enabled bool) Option {
	return func(c *config) { c.disableConversationCaching = !enabled }
}

// WithCacheTTL sets the raw TTL token stamped on EVERY breakpoint the adapter
// emits — the StablePrefix marker, the two conditional conversation anchors,
// and the top-level automatic marker all carry the SAME ttl (the uniform-TTL
// rule, ADR 0100: it makes every documented TTL-ordering 400 unreachable).
// Accepts "5m" or "1h"; "" (the default) omits the ttl field everywhere (the
// API's own 5m default applies), byte-identical to today. An unrecognised
// token degrades to "" fail-soft — mirrors outputConfigEffortFor's
// omit-on-unknown arm, so a stray/forward value can never 400 the request.
func WithCacheTTL(ttl string) Option {
	return func(c *config) { c.cacheTTL = ttl }
}

// WithRequestOption threads an arbitrary anthropic-sdk-go request option through
// to the client (e.g. option.WithHeader, option.WithMaxRetries,
// option.WithHTTPClient for a mock transport in tests). Multiple are applied in
// order, after the API key and base URL.
func WithRequestOption(opts ...option.RequestOption) Option {
	return func(c *config) { c.extra = append(c.extra, opts...) }
}

// New constructs a Provider. At minimum supply WithAPIKey; add WithBaseURL for
// compatible endpoints and WithMaxTokens for the model's real output limit.
func New(opts ...Option) *Provider {
	var c config
	for _, o := range opts {
		o(&c)
	}
	// WithoutEnvironmentDefaults FIRST: suppress the SDK's ambient autoload of
	// ANTHROPIC_BASE_URL / ANTHROPIC_AUTH_TOKEN / WIF profiles from the process env.
	// The adapter contributes ONLY what the harness resolved (the explicit key + an
	// optional flag base URL), so the documented single-knob credential/base-URL
	// custody and the availability gate are not bypassed by an ambient env var.
	reqOpts := make([]option.RequestOption, 0, len(c.extra)+3)
	reqOpts = append(reqOpts, option.WithoutEnvironmentDefaults())
	if c.apiKey != "" {
		reqOpts = append(reqOpts, option.WithAPIKey(c.apiKey))
	}
	if c.baseURL != "" {
		reqOpts = append(reqOpts, option.WithBaseURL(c.baseURL))
	}
	reqOpts = append(reqOpts, c.extra...)

	maxTokens := c.maxTokens
	if maxTokens <= 0 {
		maxTokens = defaultMaxTokens
	}
	budget := c.thinkingBudget
	if budget <= 0 {
		budget = defaultThinkingBudget
	}

	client := sdk.NewClient(reqOpts...)
	return &Provider{
		client:              client.Messages,
		maxTokens:           maxTokens,
		maxTokensFor:        c.maxTokensFor,
		thinkingFor:         c.thinkingFor,
		thinkingBudget:      budget,
		effort:              c.effort,
		caps:                c.caps,
		conversationCaching: !c.disableConversationCaching,
		cacheTTL:            c.cacheTTL,
	}
}

func sessionHeaderOptions(ctx context.Context) []option.RequestOption {
	id, ok := port.SessionIDFromContext(ctx)
	if !ok || !validHTTPHeaderValue(string(id)) {
		return nil
	}
	return []option.RequestOption{option.WithHeader(sessionIDHeaderName, string(id))}
}

func validHTTPHeaderValue(value string) bool {
	if value == "" || value[0] == ' ' || value[0] == '\t' || value[len(value)-1] == ' ' || value[len(value)-1] == '\t' {
		return false
	}
	for i := range len(value) {
		c := value[i]
		if (c < ' ' && c != '\t') || c == 0x7f {
			return false
		}
	}
	return true
}

// maxTokensForModel resolves the REQUIRED max_tokens for a given request model:
// the per-model resolver's value when it knows the model (>0), else the
// construction fallback (WithMaxTokens / defaultMaxTokens). Never returns a value
// above the model's real ceiling for a catalogued model, so a per-session route
// to a smaller-ceiling model does not 400.
func (p *Provider) maxTokensForModel(model string) int64 {
	if p.maxTokensFor != nil {
		if n := p.maxTokensFor(model); n > 0 {
			return int64(n)
		}
	}
	return p.maxTokens
}

// Stream issues a streaming Messages request and yields provider-neutral chunks.
// The returned iterator translates each SSE event via translate; it stops
// (abandoning the underlying stream) when ctx is cancelled, and surfaces a
// terminal transport error as the iterator's error. The outer error is reserved
// for a failure to construct the request parameters.
func (p *Provider) Stream(ctx context.Context, req port.LLMRequest) (iter.Seq2[port.Chunk, error], error) {
	params, err := p.buildParams(req)
	if err != nil {
		return nil, err
	}

	reqOpts := sessionHeaderOptions(ctx)
	stream := p.client.NewStreaming(ctx, params, reqOpts...)

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
			yield(port.Chunk{}, anthropicStreamErr(err, err.Error()))
		}
	}, nil
}

// Capabilities reports the provider's multimodal input support — the ADAPTER
// TRANSMIT authority composition's modelCapability intersects with the catalog
// (catalog ∩ adapter). It is STATIC (Anthropic vision is base64/url image
// blocks, so Image is true; the Messages content union has NO audio member, so
// Audio is false; EmbeddedContext is true because inline text flattens into a
// text block). It does NOT reflect the per-session intersection — that lives on
// p.caps (set via WithProviderCapabilities) and is consulted ONLY by the request
// builder's tool-result projection; the port method stays the static transmit
// authority so modelCapability's AND stays honest (the composition-layer
// intersection then yields Image:false for any text-only Claude model with no
// adapter change).
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

// anthropicStreamError carries typed retry disposition for terminal stream errors so
// the llmresilience layer can distinguish permanent client-side rejections (4xx
// other than 408/429) from transient failures (5xx, rate limits, unknown). It
// carries the SDK error for Unwrap and a human-readable message for Error().
type anthropicStreamError struct {
	err      error  // original SDK/transport error (for Unwrap)
	msg      string // human-readable Error() string
	status   int    // HTTP-status equivalent; 0 = unknown
	metadata providerErrorMetadata
}

type providerErrorMetadata struct {
	httpStatus      int
	inBandStatus    int
	providerCode    string
	correlationKind string
	correlationID   string
	retryNotBefore  time.Time
	hasRetryAfter   bool
}

func (e *anthropicStreamError) Error() string             { return e.msg }
func (e *anthropicStreamError) Unwrap() error             { return e.err }
func (e *anthropicStreamError) StatusCode() int           { return e.status }
func (e *anthropicStreamError) ProviderHTTPStatus() int   { return e.metadata.httpStatus }
func (e *anthropicStreamError) ProviderInBandStatus() int { return e.metadata.inBandStatus }
func (e *anthropicStreamError) ProviderErrorCode() string { return e.metadata.providerCode }
func (e *anthropicStreamError) ProviderErrorCorrelationKind() string {
	return e.metadata.correlationKind
}
func (e *anthropicStreamError) ProviderErrorCorrelationID() string { return e.metadata.correlationID }
func (e *anthropicStreamError) RetryNotBefore() (time.Time, bool) {
	return e.metadata.retryNotBefore, e.metadata.hasRetryAfter
}

// RetryDisposition implements session.RetryDispositionError.
func (e *anthropicStreamError) RetryDisposition() session.RetryDisposition {
	if isContextOverflowMessage(e.msg) || e.status != 0 && !retryableStatus(e.status) {
		return session.RetryDispositionPermanent
	}
	if retryableStatus(e.status) {
		return session.RetryDispositionRetryable
	}
	return session.RetryDispositionUnknown
}

// isContextOverflowMessage is duplicated from provider/openai/stream.go (separate
// Go module; a shared dep is worse than ~10 lines). It reports whether a provider
// error message indicates the request was rejected because it exceeded the model's
// context window — a PERMANENT client error that must NOT be retried and must NOT
// count toward the circuit breaker.
func isContextOverflowMessage(msg string) bool {
	m := strings.ToLower(msg)
	return strings.Contains(m, "context window") ||
		strings.Contains(m, "context length") ||
		strings.Contains(m, "maximum context") ||
		strings.Contains(m, "exceeds the token limit") ||
		strings.Contains(m, "exceeded the token limit")
}

// retryableStatus reports whether an HTTP status code is transient. Must stay
// consistent with the llmresilience classifier's retry set.
func retryableStatus(code int) bool {
	return code == 408 || code == 429 || code >= 500
}

var retryAfterHorizon = time.Date(9999, 12, 31, 23, 59, 59, 999999999, time.UTC)

func parseRetryAfter(header http.Header, received time.Time) (time.Time, bool) {
	values := header.Values("Retry-After")
	if len(values) != 1 {
		return time.Time{}, false
	}
	value := values[0]
	if value != "" && strings.IndexFunc(value, func(r rune) bool { return r < '0' || r > '9' }) == -1 {
		seconds, err := strconv.ParseUint(value, 10, 64)
		if err != nil {
			return time.Time{}, false
		}
		if seconds > uint64(math.MaxInt64/int64(time.Second)) {
			return retryAfterHorizon, true
		}
		at := received.Add(time.Duration(seconds) * time.Second)
		if !at.Before(retryAfterHorizon) {
			return retryAfterHorizon, true
		}
		return at, true
	}
	at, err := http.ParseTime(value)
	if err != nil {
		return time.Time{}, false
	}
	if at.Before(received) {
		return received, true
	}
	if !at.Before(retryAfterHorizon) {
		return retryAfterHorizon, true
	}
	return at, true
}

// anthropicHTTPErrorText projects only the structured API envelope for display.
// sdk.Error.Error includes the request URL, request ID, and raw response body, so it
// must remain unwrap-only.
func anthropicHTTPErrorText(err *sdk.Error) string {
	var envelope struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	_ = json.Unmarshal([]byte(err.RawJSON()), &envelope)
	return structuredHTTPErrorText(string(err.Type()), envelope.Error.Message)
}

func structuredHTTPErrorText(kind, message string) string {
	kind = strings.TrimSpace(kind)
	message = strings.TrimSpace(message)
	switch {
	case kind != "" && message != "":
		return kind + ": " + message
	case kind != "":
		return kind
	case message != "":
		return message
	default:
		return "provider request failed"
	}
}

// anthropicStreamErr wraps the given error while retaining the SDK error in the
// chain and projecting only typed provider metadata.
func anthropicStreamErr(err error, msg string) *anthropicStreamError {
	var sdkErr *sdk.Error
	status := 0
	metadata := providerErrorMetadata{}
	if errors.As(err, &sdkErr) {
		msg = port.AppendHTTPErrorDisplay(anthropicHTTPErrorText(sdkErr), sdkErr.Request, sdkErr.RequestID)
		status = sdkErr.StatusCode
		metadata.httpStatus = status
		if sdkErr.Response != nil {
			metadata.retryNotBefore, metadata.hasRetryAfter = parseRetryAfter(sdkErr.Response.Header, time.Now())
		}
		metadata.providerCode = string(sdkErr.Type())
		if sdkErr.RequestID != "" {
			metadata.correlationKind = "request"
			metadata.correlationID = sdkErr.RequestID
		}
	}
	return &anthropicStreamError{err: err, msg: msg, status: status, metadata: metadata}
}

// Compile-time assertion that Provider satisfies the port.
var _ port.LLMProvider = (*Provider)(nil)
