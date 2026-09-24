// Package openaichat implements port.LLMProvider over the OpenAI Chat
// Completions API (POST /v1/chat/completions) using
// github.com/openai/openai-go/v3. It names the PROTOCOL, not a single vendor:
// any endpoint speaking the OpenAI Chat Completions wire protocol (OpenCode Go,
// Groq, Together, DeepSeek, vLLM, ...) is served by this one adapter with a
// different base URL + credential. Its first consumer is the composition
// registry's "opencode" provider (OpenCode Go, https://opencode.ai/zen/go/v1).
//
// It is the Chat Completions sibling of the openai package (Responses API): the
// harness owns its own conversation state, so every request is stateless and
// resends the full message slice. Display-only reasoning (a provider's streamed
// reasoning_content "thinking") IS surfaced as ChunkReasoning — both for client
// display and so llmresilience's idle watchdog observes progress during a
// thinking phase — but there is NO reasoning-REPLAY blob (ChunkReasoningItem) and
// no phase marker: Chat Completions does not carry reasoning across turns, so
// those replay facilities are simply unused here (a deliberate simplification,
// not a gap).
//
// The streaming chat.completion.chunk events are translated into
// provider-neutral port.Chunk values by the pure translate function, exercised
// directly from recorded SSE fixtures in tests; no network is required to test
// the translation.
package openaichat

import (
	"context"
	"errors"
	"iter"
	"net/http"
	"strings"
	"sync/atomic"

	oai "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/provider/ssefilter"
)

const (
	sessionIDHeaderName         = "X-Mecatl-Session-ID"
	openCodeSessionIDHeaderName = "X-OpenCode-Session"
)

// Provider is a port.LLMProvider backed by the OpenAI Chat Completions API.
// Construct it with New.
type Provider struct {
	client oai.ChatCompletionService
	// effort is the reasoning-effort token stamped on every request's
	// reasoning_effort field (ADR 0055). Empty (and "auto"/unknown) OMITS the
	// field — the provider default applies. Composition supplies an already-
	// normalised neutral token; unlike the openai (Responses) adapter this
	// endpoint accepts xhigh/max, so they are NOT clamped to high.
	effort string
	// cacheDialect selects which provider-side prompt-cache wire dialect
	// (ADR 0100) buildParams (method) emits. "" (CacheDialectNone, the zero
	// value) emits no cache hints at all — the byte-identical pre-ADR-0100
	// wire.
	cacheDialect CacheDialect
	// openCodeSessionHeader enables the OpenCode-specific session header without
	// changing the generic adapter's wire behavior.
	openCodeSessionHeader bool
	// cacheMemo memoises the last-seen (StablePrefix, hash) pair for
	// promptCacheKey — see cachekey.go.
	cacheMemo atomic.Pointer[prefixMemo]
}

// Option configures a Provider.
type Option func(*config)

type config struct {
	apiKey                string
	baseURL               string
	effort                string
	extra                 []option.RequestOption
	cacheDialect          CacheDialect
	openCodeSessionHeader bool
}

// WithAPIKey sets the API key used to authenticate requests.
func WithAPIKey(key string) Option {
	return func(c *config) { c.apiKey = key }
}

// WithBaseURL overrides the API host so OpenAI-compatible endpoints can be
// targeted. The SDK appends "/chat/completions"; baseURL should end in "/v1".
func WithBaseURL(url string) Option {
	return func(c *config) { c.baseURL = url }
}

// WithReasoningEffort sets the reasoning-effort token stamped on every request's
// reasoning_effort field (ADR 0055). Empty/"auto"/unknown OMITS the field. It is
// an adapter-CONSTRUCTION Option, not a port.LLMRequest field — the per-session
// engine factory re-mints the adapter when a session's effort differs from the
// operator default.
func WithReasoningEffort(effort string) Option {
	return func(c *config) { c.effort = effort }
}

// WithHTTPClient sets the *http.Client the SDK issues requests through. nil is
// ignored (SDK default). NOTE: do NOT set Client.Timeout here — a streaming turn
// runs for minutes; establishment/idle bounds live in llmresilience.
func WithHTTPClient(c *http.Client) Option {
	return func(cfg *config) {
		if c != nil {
			cfg.extra = append(cfg.extra, option.WithHTTPClient(c))
		}
	}
}

// WithOpenCodeSessionHeader enables OpenCode Go's required per-conversation
// x-opencode-session header. It is opt-in so generic OpenAI-compatible endpoints
// continue to receive only mecatl's correlation header.
func WithOpenCodeSessionHeader() Option {
	return func(c *config) { c.openCodeSessionHeader = true }
}

// WithRequestOption threads an arbitrary openai-go request option through to the
// client. Multiple are applied in order, after the API key and base URL.
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
	// for why an SSE keepalive would otherwise kill a streaming turn outright.
	reqOpts = append(reqOpts, option.WithMiddleware(ssefilter.NewKeepaliveFilter()))
	// Always install the resolved value, including an empty value. Otherwise the
	// SDK autoloads OPENAI_API_KEY, which violates a custom auth.method=none.
	reqOpts = append(reqOpts, option.WithAPIKey(c.apiKey))
	if c.baseURL != "" {
		reqOpts = append(reqOpts, option.WithBaseURL(c.baseURL))
	}
	reqOpts = append(reqOpts, c.extra...)

	client := oai.NewClient(reqOpts...)
	return &Provider{
		client:                client.Chat.Completions,
		effort:                c.effort,
		cacheDialect:          c.cacheDialect,
		openCodeSessionHeader: c.openCodeSessionHeader,
	}
}

func sessionHeaderOptions(ctx context.Context, openCode bool) []option.RequestOption {
	id, ok := port.SessionIDFromContext(ctx)
	if !ok || !validHTTPHeaderValue(string(id)) {
		return nil
	}
	opts := []option.RequestOption{option.WithHeader(sessionIDHeaderName, string(id))}
	if openCode {
		opts = append(opts, option.WithHeader(openCodeSessionIDHeaderName, string(id)))
	}
	return opts
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

// Stream issues a streaming Chat Completions request and yields provider-neutral
// chunks. The returned iterator translates each SSE chunk via translate; it stops
// (abandoning the underlying stream) when ctx is cancelled, and surfaces a
// terminal transport/stream error as the iterator's error. The outer error is
// reserved for a failure to construct the request parameters.
func (p *Provider) Stream(ctx context.Context, req port.LLMRequest) (iter.Seq2[port.Chunk, error], error) {
	params, err := p.buildParams(req)
	if err != nil {
		return nil, err
	}

	reqOpts := sessionHeaderOptions(ctx, p.openCodeSessionHeader)
	stream := p.client.NewStreaming(ctx, params, reqOpts...)

	return func(yield func(port.Chunk, error) bool) {
		defer func() { _ = stream.Close() }()
		var st streamState
		observe := port.ObserveAttemptOnce(ctx)
		for stream.Next() {
			select {
			case <-ctx.Done():
				observe(false, session.StreamOutcomeCancelled)
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
				// A translation-layer failure (e.g. tool-args over the size cap) is
				// terminal — surface it and stop.
				observe(st.finished, session.StreamOutcomeStreamError)
				yield(port.Chunk{}, terr)
				return
			}
		}
		if err := stream.Err(); err != nil {
			// Don't report a plain context cancellation as a stream error; the
			// caller cancelled deliberately.
			if ctx.Err() != nil {
				observe(false, session.StreamOutcomeCancelled)
				return
			}
			observe(st.finished, session.StreamOutcomeStreamError)
			yield(port.Chunk{}, openaichatStreamErr(err, err.Error(), st.completionID))
			return
		}
		if !st.finished {
			// Clean EOF but NO finish_reason: the SDK's ssestream returns Err()==nil
			// on a plain mid-stream EOF, so a dropped connection is indistinguishable
			// from a normal close here. FAIL CLOSED — do NOT flush buffered tool calls
			// or fabricate a terminal (that would turn a truncated stream into a
			// successful tool-executing turn). Surface a truncation error instead
			// (retryable pre-commit; terminal once a committing chunk has gone out).
			if ctx.Err() != nil {
				observe(false, session.StreamOutcomeCancelled)
				return
			}
			observe(false, session.StreamOutcomeIncomplete)
			yield(port.Chunk{}, errTruncatedStream)
			return
		}
		outcome := session.StreamOutcomeComplete
		if st.stop == session.StopError {
			outcome = session.StreamOutcomeIncomplete
		}
		observe(true, outcome)
		// Finished cleanly: flush the buffered terminal (tool calls, usage, done).
		for _, c := range finalize(&st) {
			if !yield(c, nil) {
				return
			}
		}
	}, nil
}

// Capabilities reports the provider's multimodal input support. STATIC: the Chat
// Completions user-message content union supports text + image_url (so Image is
// true, EmbeddedContext is true because inline text flattens into a text part);
// audio (input_audio) is wired in the protocol but no target model consumes it,
// so Audio is false.
func (*Provider) Capabilities() port.ProviderCapabilities {
	return port.ProviderCapabilities{Image: true, Audio: false, EmbeddedContext: true}
}

// Compile-time assertion that Provider satisfies the port.
var _ port.LLMProvider = (*Provider)(nil)

// openaichatStreamError carries typed retry disposition for terminal stream errors so
// the llmresilience layer can distinguish permanent client-side rejections (4xx
// other than 408/429) from transient failures. It carries the SDK error for
// Unwrap and a human-readable message for Error().
type openaichatStreamError struct {
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
}

func (e *openaichatStreamError) Error() string             { return e.msg }
func (e *openaichatStreamError) Unwrap() error             { return e.err }
func (e *openaichatStreamError) StatusCode() int           { return e.status }
func (e *openaichatStreamError) ProviderHTTPStatus() int   { return e.metadata.httpStatus }
func (e *openaichatStreamError) ProviderInBandStatus() int { return e.metadata.inBandStatus }
func (e *openaichatStreamError) ProviderErrorCode() string { return e.metadata.providerCode }
func (e *openaichatStreamError) ProviderErrorCorrelationKind() string {
	return e.metadata.correlationKind
}
func (e *openaichatStreamError) ProviderErrorCorrelationID() string { return e.metadata.correlationID }

// RetryDisposition implements session.RetryDispositionError.
func (e *openaichatStreamError) RetryDisposition() session.RetryDisposition {
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
// context window — a PERMANENT client error that must NOT be retried.
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

func structuredHTTPErrorText(code, kind, message string) string {
	label := strings.TrimSpace(code)
	if label == "" {
		label = strings.TrimSpace(kind)
	}
	message = strings.TrimSpace(message)
	switch {
	case label != "" && message != "":
		return label + ": " + message
	case label != "":
		return label
	case message != "":
		return message
	default:
		return "provider request failed"
	}
}

// openaichatStreamErr wraps the given error as an openaichatStreamError while
// retaining the SDK error in the chain and projecting only typed provider fields.
func openaichatStreamErr(err error, msg, completionID string) *openaichatStreamError {
	var sdkErr *oai.Error
	status := 0
	metadata := providerErrorMetadata{}
	if errors.As(err, &sdkErr) {
		metadata.providerCode = sdkErr.Code
		if sdkErr.StatusCode != 0 {
			status = sdkErr.StatusCode
			metadata.httpStatus = sdkErr.StatusCode
		} else {
			status = openaichatErrorCodeToStatus(sdkErr.Code)
			metadata.inBandStatus = status
		}
		requestID := ""
		if sdkErr.Response != nil {
			requestID = sdkErr.Response.Header.Get("X-Request-ID")
			if requestID != "" {
				metadata.correlationKind = "request"
				metadata.correlationID = requestID
			}
		}
		msg = port.AppendHTTPErrorDisplay(structuredHTTPErrorText(sdkErr.Code, sdkErr.Type, sdkErr.Message), sdkErr.Request, requestID)
	}
	if metadata.correlationID == "" && completionID != "" {
		metadata.correlationKind = "completion"
		metadata.correlationID = completionID
	}
	return &openaichatStreamError{err: err, msg: msg, status: status, metadata: metadata}
}

func openaichatErrorCodeToStatus(code string) int {
	switch code {
	case "rate_limit_exceeded":
		return http.StatusTooManyRequests
	case "server_error", "engine_overloaded", "service_unavailable":
		return http.StatusServiceUnavailable
	case "gateway_timeout", "timeout":
		return http.StatusGatewayTimeout
	default:
		return 0
	}
}
