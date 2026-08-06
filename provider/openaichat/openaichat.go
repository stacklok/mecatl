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

	oai "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/provider/ssefilter"
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
}

// Option configures a Provider.
type Option func(*config)

type config struct {
	apiKey  string
	baseURL string
	effort  string
	extra   []option.RequestOption
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
	if c.apiKey != "" {
		reqOpts = append(reqOpts, option.WithAPIKey(c.apiKey))
	}
	if c.baseURL != "" {
		reqOpts = append(reqOpts, option.WithBaseURL(c.baseURL))
	}
	reqOpts = append(reqOpts, c.extra...)

	client := oai.NewClient(reqOpts...)
	return &Provider{client: client.Chat.Completions, effort: c.effort}
}

// Stream issues a streaming Chat Completions request and yields provider-neutral
// chunks. The returned iterator translates each SSE chunk via translate; it stops
// (abandoning the underlying stream) when ctx is cancelled, and surfaces a
// terminal transport/stream error as the iterator's error. The outer error is
// reserved for a failure to construct the request parameters.
func (p *Provider) Stream(ctx context.Context, req port.LLMRequest) (iter.Seq2[port.Chunk, error], error) {
	params, err := buildParams(req, p.effort)
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
				// A translation-layer failure (e.g. tool-args over the size cap) is
				// terminal — surface it and stop.
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
			yield(port.Chunk{}, openaichatStreamErr(err, err.Error()))
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
				return
			}
			yield(port.Chunk{}, errTruncatedStream)
			return
		}
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

// openaichatStreamError wraps a terminal stream error as port.PermanentError so
// the llmresilience layer can distinguish permanent client-side rejections (4xx
// other than 408/429) from transient failures. It carries the SDK error for
// Unwrap and a human-readable message for Error().
type openaichatStreamError struct {
	err    error  // original SDK/transport error (for Unwrap)
	msg    string // human-readable Error() string
	status int    // HTTP-status equivalent; 0 = unknown
}

func (e *openaichatStreamError) Error() string { return e.msg }
func (e *openaichatStreamError) Unwrap() error { return e.err }

// Permanent implements port.PermanentError. The error is permanent when the
// message signals a context-window overflow, or when the status is a known
// non-retryable 4xx. Status 0 and retryable codes (408, 429, 5xx) are NOT
// permanent — fail-open.
func (e *openaichatStreamError) Permanent() bool {
	if isContextOverflowMessage(e.msg) {
		return true
	}
	if e.status != 0 && !retryableStatus(e.status) {
		return true
	}
	return false
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

// openaichatStreamErr wraps the given error as an openaichatStreamError, probing
// the error chain for an OpenAI SDK Error to extract an HTTP-status equivalent.
// If no SDK error is found, status is 0 (unknown / fail-open).
func openaichatStreamErr(err error, msg string) *openaichatStreamError {
	var sdkErr *oai.Error
	status := 0
	if errors.As(err, &sdkErr) {
		status = sdkErr.StatusCode
	}
	return &openaichatStreamError{err: err, msg: msg, status: status}
}
