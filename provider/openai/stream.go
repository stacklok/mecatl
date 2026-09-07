package openai

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	oai "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/responses"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

// streamState carries the small amount of state the translation needs across
// events. The OpenAI Responses SSE stream is semantic and mostly self-describing
// per event, so the only carried state is the assembled final response (for the
// terminal usage/stop), threaded via the events themselves.
type streamState struct {
	// providerRoute permits parsing OpenRouter's private routing metadata. It is
	// armed only by WithOpenRouterMetadata, so a compatible non-OpenRouter endpoint
	// cannot manufacture a provider-neutral route chunk by returning the same key.
	providerRoute bool

	// done is set when a TERMINAL Responses event is observed
	// (response.completed / response.incomplete / response.failed / a top-level
	// error). It guards against emitting a second ChunkDone if both
	// response.completed and a later terminal event arrive, AND it is the
	// truncation signal: a clean EOF with done==false means the stream ended
	// without a terminal event (e.g. a dropped connection), which Stream fails closed as
	// errTruncatedStream rather than letting the engine promote partial text to a
	// successful StopEndTurn.
	done bool

	// responseID is learned only from a typed Responses event and lets a later
	// top-level error correlate with the response it terminated.
	responseID string

	// reasoning accumulates the turn's (id, encrypted_content) reasoning items in
	// arrival order, packed into ONE ChunkReasoningItem at the terminal event.
	// A turn may emit several — each blob is bound to its own item id and must be
	// replayed under that id, which one chunk per item cannot express (the port
	// carries a single id, and the loop folds the chunks into one Message field).
	// See reasoning.go. This is the adapter's only payload buffering; it is
	// bounded by the turn's reasoning output and released with the stream.
	reasoning []reasoningItem

	// callsSeen counts the function_call items emitted so far this turn. It stamps
	// each buffered reasoning item's After, which is what lets replay put the item
	// back BETWEEN the right two tool calls instead of hoisting every reasoning
	// item to the front of the turn.
	callsSeen int
}

// errTruncatedStream is surfaced when the Responses stream ends cleanly (no error
// frame) but WITHOUT a terminal event (no response.completed/incomplete/failed).
// The SDK's ssestream returns Err()==nil on a plain mid-stream EOF — a dropped
// connection is indistinguishable from a normal close at that layer, and unlike
// Chat Completions there is no [DONE] sentinel. Failing closed here stops a
// truncated turn from being promoted to a successful StopEndTurn by the engine
// (which turns partial text + StopNone into StopEndTurn). It wraps
// io.ErrUnexpectedEOF so the resilience classifier treats a PRE-commit truncation
// as retryable (a post-commit one is terminal by the no-replay rule) — the same
// posture as the openaichat adapter's identically-named error.
var errTruncatedStream = fmt.Errorf("openai: responses stream ended without a terminal event: %w", io.ErrUnexpectedEOF)

// translate converts a single Responses SSE event into zero or more
// provider-neutral chunks. It is a pure function (apart from the small carried
// streamState) so it can be driven directly from recorded fixtures in tests,
// with no real client.
//
// A terminal failure event (the top-level "error" event, or a "response.failed"
// status) is reported as a non-nil error carrying the provider's human-readable
// message rather than as a bare StopError chunk: the loop surfaces a stream
// error verbatim, so the real reason ("rate_limit_exceeded: ...", "<model> is
// not a valid model ID", ...) reaches the result instead of an opaque "error".
//
// Unknown / unhandled event types (the long tail of audio, image, web/file
// search, MCP, reasoning-part, content-part, *.added / in_progress, etc.) are
// ignored: the Responses stream is forward-compatible, and treating an
// unrecognised event as fatal would break against any spec-compliant endpoint
// that emits events we do not consume.
//
// Mapping:
//   - response.output_text.delta            -> ChunkText (event.Delta)
//   - response.reasoning_summary_text.delta -> ChunkReasoning (event.Delta, DISPLAY summary)
//   - response.reasoning_text.delta         -> ChunkReasoning (event.Delta, DISPLAY summary)
//   - response.output_item.done (reasoning)     -> BUFFERED (encrypted_content + item id)
//   - response.output_item.done (message)       -> ChunkPhase (opaque phase marker, REPLAYED)
//   - response.output_item.done (function_call) -> ChunkToolCall
//   - response.completed                    -> ChunkReasoningItem (packed)? then ChunkUsage, ChunkDone(end_turn)
//   - response.incomplete                   -> ChunkUsage then ChunkDone(error)
//   - response.failed / error               -> non-nil error (provider message)
//
// The reasoning summary deltas (ChunkReasoning) and the reasoning replay blob
// (ChunkReasoningItem) are deliberately distinct: the summary is human-readable
// prose for display, whereas the replay blob is OpenAI's opaque encrypted_content
// token that must be sent back verbatim (request.go) for stateless multi-turn
// reasoning continuity. They MUST NOT be conflated.
//
// Reasoning replay items are BUFFERED across the turn and packed into a single
// ChunkReasoningItem at response.completed, because each blob only verifies
// under its own reasoning-item id and the port carries one id per chunk. See
// streamState.reasoning and reasoning.go. A turn that ends WITHOUT
// response.completed (incomplete/failed/error/truncation) drops them, which
// costs nothing: the engine discards the whole assistant message on those paths.
//
// Visible output text is projected through the provider-neutral one-string seam:
// every non-empty delta is emitted in serial SSE arrival order and provider item,
// output, and content identities are deliberately discarded at this boundary.
// Reasoning summary deltas remain display-only and independently concatenate.
func translate(event responses.ResponseStreamEventUnion, st *streamState) ([]port.Chunk, error) {
	// Response IDs only enter state from a known Responses lifecycle event's typed
	// Response field; raw SSE fields and arbitrary metadata are never inspected.
	if responseID := observedResponseID(event); responseID != "" {
		st.responseID = responseID
	}

	switch event.Type {
	case "response.output_text.delta":
		if event.Delta == "" {
			return nil, nil
		}
		return []port.Chunk{{Kind: port.ChunkText, Text: event.Delta}}, nil

	case "response.reasoning_summary_text.delta", "response.reasoning_text.delta":
		// Human-readable reasoning summary deltas: DISPLAY-only. They are NOT the
		// blob replayed to the provider (that arrives on the reasoning output item;
		// see response.output_item.done below).
		if event.Delta == "" {
			return nil, nil
		}
		return []port.Chunk{{Kind: port.ChunkReasoning, Text: event.Delta}}, nil

	case "response.output_item.done":
		// Act on the .done payload, not concatenated deltas (per the brief's
		// assembly rule). Two assembled item types matter here:
		item := event.Item
		switch item.Type {
		case "reasoning":
			// The reasoning item carries encrypted_content (requested via
			// Include: reasoning.encrypted_content) — the opaque REPLAY blob that
			// must be sent back verbatim for stateless reasoning continuity, bound
			// to THIS item's id. An empty blob (non-reasoning models, or encryption
			// not honoured) is skipped, so replay is a no-op. An id-less item is
			// skipped too: it could only be replayed as `"id":""`, which strict
			// gateways reject (the D1a degrade).
			//
			// It is BUFFERED rather than emitted here: a turn may produce several,
			// and each blob only verifies under its own id, so they ride out
			// together as one packed envelope at the terminal event (see the
			// "response.completed" case below, and reasoning.go).
			if item.EncryptedContent == "" || item.ID == "" {
				return nil, nil
			}
			st.reasoning = append(st.reasoning, reasoningItem{
				ID: item.ID, Blob: item.EncryptedContent, After: st.callsSeen,
			})
			return nil, nil
		case "message":
			// The assembled assistant message item carries an opaque PHASE marker
			// ("commentary" / "final_answer") that store:false manual-replay apps must
			// preserve and resend on the assistant message item, or GPT-5.x models
			// treat preambles as final answers / stop early (see request.go's
			// assistantItems). It is cast through as an OPAQUE string — never validated
			// against the enum, so unknown future values pass through unchanged
			// (forward-compat). The visible text still arrives via output_text.delta;
			// this case only lifts the phase off the assembled item (an empty phase —
			// non-tagging models — yields no chunk, so replay is a no-op).
			if item.Phase == "" {
				return nil, nil
			}
			return []port.Chunk{{Kind: port.ChunkPhase, Text: string(item.Phase)}}, nil
		case "function_call":
			// The assembled function_call carries call_id, name, and the final
			// arguments JSON string. item.ID is the provider's opaque item-level
			// identifier (e.g. "fc_1" from the OpenAI Responses API), distinct from
			// call_id. It is stored in ItemID for verbatim replay on subsequent
			// stateless turns so the provider can de-duplicate items (prevents the
			// "Duplicate item found with id fc_N" error on store:false multi-turn
			// sessions with Azure GPT-5.x).
			call := session.ToolCall{
				ID:     session.ToolCallID(item.CallID),
				ItemID: item.ID,
				Name:   item.Name,
				Args:   json.RawMessage(item.Arguments.OfString),
			}
			// Advance the cursor a later reasoning item stamps itself against, so
			// replay can rebuild the original interleaving (see reasoningItem.After).
			st.callsSeen++
			return []port.Chunk{{Kind: port.ChunkToolCall, ToolCall: &call}}, nil
		default:
			return nil, nil
		}

	case "response.completed":
		return translateCompleted(event, st)

	case "response.incomplete":
		// An incomplete response still carries usage and a reason (e.g.
		// max_output_tokens, content_filter); emit usage, then stop as an error
		// with a human-readable message keyed on the reason so the run is
		// diagnosable but accounted for. The reason is provider-driven and NOT
		// retried (replaying the identical prompt just trips the same condition).
		if st.done {
			return nil, nil
		}
		st.done = true
		usage := mapUsage(event.Response.Usage)
		return []port.Chunk{
			{Kind: port.ChunkUsage, Usage: &usage},
		}, fmt.Errorf("%s", incompleteMessage(event.Response))

	case "response.failed":
		if st.done {
			return nil, nil
		}
		st.done = true
		return nil, &responseStreamError{
			msg:      "response failed: " + responseErrorString(event.Response.Error),
			status:   providerErrorStatus(string(event.Response.Error.Code), event.Response.Error.Message),
			metadata: responseErrorMetadata(event.Response.Error, event.Response.ID),
		}

	case "error":
		// Top-level transport/protocol error event. The message/code/param live
		// directly on the event union (variant ResponseErrorEvent).
		if st.done {
			return nil, nil
		}
		st.done = true
		return nil, &responseStreamError{
			msg:      "stream error: " + streamErrorString(event),
			status:   providerErrorStatus(event.Code, event.Message),
			metadata: streamErrorMetadata(event.Code, event.Message, st.responseID),
		}

	default:
		return nil, nil
	}
}

// translateCompleted handles the terminal response.completed event: it packs the
// turn's buffered reasoning items into one replay blob, emits the routed
// downstream provider (when present), and closes with usage + done. Split out of
// translate only to keep the dispatcher under the cyclomatic-complexity bound.
func translateCompleted(event responses.ResponseStreamEventUnion, st *streamState) ([]port.Chunk, error) {
	if st.done {
		return nil, nil
	}
	st.done = true
	usage := mapUsage(event.Response.Usage)
	chunks := make([]port.Chunk, 0, 4)
	// The routed DOWNSTREAM provider (issue #480): the openrouter_metadata
	// block rides the terminal event's raw JSON when the request armed
	// X-OpenRouter-Metadata. Parsing is gated by that same adapter option so a
	// compatible non-OpenRouter endpoint cannot inject a route echo. Emitted FIRST
	// so ordering is deterministic. "" on a cache hit → no chunk.
	if st.providerRoute {
		if label := selectedDownstreamProvider(event.Response.RawJSON()); label != "" {
			chunks = append(chunks, port.Chunk{Kind: port.ChunkProviderRoute, Text: label})
		}
	}
	// The turn's buffered reasoning items, packed into one replay blob. It
	// rides out here — the only point at which the full ordered list is known.
	// ReasoningItemID stays EMPTY: with several ids in play the port's single
	// id cannot name them, and each id now travels inside the envelope beside
	// the blob it belongs to (provider/anthropic does the same).
	if packed := packReasoningItems(st.reasoning); packed != "" {
		chunks = append(chunks, port.Chunk{Kind: port.ChunkReasoningItem, Text: packed})
	}
	return append(chunks,
		port.Chunk{Kind: port.ChunkUsage, Usage: &usage},
		port.Chunk{Kind: port.ChunkDone, Stop: mapStop(event.Response.Status)},
	), nil
}

// responseErrorString renders a Responses ResponseError (on a failed response)
// as "code: message", tolerating either part being absent.
func responseErrorString(e responses.ResponseError) string {
	switch {
	case e.Message != "" && e.Code != "":
		return fmt.Sprintf("%s: %s", e.Code, e.Message)
	case e.Message != "":
		return e.Message
	case e.Code != "":
		return string(e.Code)
	default:
		return "unknown error"
	}
}

func observedResponseID(event responses.ResponseStreamEventUnion) string {
	switch event.Type {
	case "response.created", "response.in_progress", "response.completed", "response.incomplete", "response.failed", "response.queued":
		return event.Response.ID
	default:
		return ""
	}
}

func responseErrorMetadata(e responses.ResponseError, responseID string) providerErrorMetadata {
	return streamErrorMetadata(string(e.Code), e.Message, responseID)
}

func streamErrorMetadata(code, message, responseID string) providerErrorMetadata {
	metadata := providerErrorMetadata{
		inBandStatus: providerErrorStatus(code, message),
		providerCode: code,
	}
	if responseID != "" {
		metadata.correlationKind = "response"
		metadata.correlationID = responseID
	}
	return metadata
}

// streamErrorString renders a top-level "error" event union as "code: message",
// optionally appending the offending param, tolerating absent parts.
func streamErrorString(event responses.ResponseStreamEventUnion) string {
	msg := event.Message
	switch {
	case msg != "" && event.Code != "":
		msg = fmt.Sprintf("%s: %s", event.Code, msg)
	case msg == "" && event.Code != "":
		msg = event.Code
	case msg == "":
		msg = "unknown error"
	}
	if event.Param != "" {
		msg = fmt.Sprintf("%s (param: %s)", msg, event.Param)
	}
	return msg
}

// responseStreamError is a typed error returned by the stream translator for
// response.failed and top-level error events. It preserves the original
// human-readable message AND carries an HTTP-status equivalent so the
// llmresilience DefaultClassifier (which checks for interface{ StatusCode() int })
// can classify transient codes (e.g. 429 rate-limit) as retryable.
//
// The human-readable Error() string retains the in-band event text. Structured
// HTTP rejections add only the safe target/ID projection in httpMetadataError.
type providerErrorMetadata struct {
	httpStatus      int
	inBandStatus    int
	providerCode    string
	correlationKind string
	correlationID   string
}

type responseStreamError struct {
	msg      string // human-readable, e.g. "response failed: rate_limit_exceeded: Too Many Requests"
	status   int    // HTTP-status equivalent; 0 means unknown/non-retryable
	metadata providerErrorMetadata
}

func (e *responseStreamError) Error() string             { return e.msg }
func (e *responseStreamError) ProviderHTTPStatus() int   { return e.metadata.httpStatus }
func (e *responseStreamError) ProviderInBandStatus() int { return e.metadata.inBandStatus }
func (e *responseStreamError) ProviderErrorCode() string { return e.metadata.providerCode }
func (e *responseStreamError) ProviderErrorCorrelationKind() string {
	return e.metadata.correlationKind
}
func (e *responseStreamError) ProviderErrorCorrelationID() string { return e.metadata.correlationID }

// httpMetadataError keeps the SDK error unwrap-visible while exposing a safe
// display projection plus typed metadata for diagnostics.
type httpMetadataError struct {
	err      error
	message  string
	metadata providerErrorMetadata
}

func (e *httpMetadataError) Error() string                        { return e.message }
func (e *httpMetadataError) Unwrap() error                        { return e.err }
func (e *httpMetadataError) ProviderHTTPStatus() int              { return e.metadata.httpStatus }
func (e *httpMetadataError) ProviderInBandStatus() int            { return e.metadata.inBandStatus }
func (e *httpMetadataError) ProviderErrorCode() string            { return e.metadata.providerCode }
func (e *httpMetadataError) ProviderErrorCorrelationKind() string { return e.metadata.correlationKind }
func (e *httpMetadataError) ProviderErrorCorrelationID() string   { return e.metadata.correlationID }

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

func withHTTPErrorMetadata(err error) error {
	var apiErr *oai.Error
	if !errors.As(err, &apiErr) {
		return err
	}
	metadata := providerErrorMetadata{
		httpStatus:   apiErr.StatusCode,
		providerCode: apiErr.Code,
	}
	requestID := ""
	if apiErr.Response != nil {
		requestID = apiErr.Response.Header.Get("X-Request-ID")
		if requestID != "" {
			metadata.correlationKind = "request"
			metadata.correlationID = requestID
		}
	}
	return &httpMetadataError{
		err:      err,
		message:  port.AppendHTTPErrorDisplay(structuredHTTPErrorText(apiErr.Code, apiErr.Type, apiErr.Message), apiErr.Request, requestID),
		metadata: metadata,
	}
}

// StatusCode returns the HTTP-status equivalent of the provider error code. The
// llmresilience DefaultClassifier recognises this interface and routes retryable
// statuses (408, 429, 5xx) through its retry logic.
func (e *responseStreamError) StatusCode() int { return e.status }

// Permanent implements port.PermanentError. A responseStreamError is permanent
// when the message signals a context-window overflow (replaying the identical
// over-context prompt cannot succeed), or when the status code is a known
// non-retryable 4xx rejection. Status 0 and retryable codes (408, 429, 5xx) are
// NOT permanent (fail-open: an unclassifiable error may succeed on retry).
func (e *responseStreamError) Permanent() bool {
	if isContextOverflowMessage(e.msg) {
		return true
	}
	// status==0 means "unknown" — fail-open, not permanent. Context-overflow
	// messages are already demoted to status 0 by providerErrorStatus, so the
	// context-overflow check above is the discriminator that separates the two
	// cases of status==0.
	if e.status != 0 && !retryableStatus(e.status) {
		return true
	}
	return false
}

// retryableStatus reports whether an HTTP status code is transient (worthy of
// retry). Must stay consistent with the llmresilience classifier's set: 408
// request-timeout, 429 rate-limit, and all 5xx server errors are transient and
// should be retried; everything else — including unknown (0) — is not. This is
// the mirror of providerCodeToHTTPStatus: the codes that function maps INTO the
// retryable set are the same ones this function recognises.
func retryableStatus(code int) bool {
	return code == 408 || code == 429 || code >= 500
}

// isContextOverflowMessage reports whether a provider error message indicates
// the request was rejected because it exceeded the model's context window. This
// is a PERMANENT client error: replaying the identical over-context prompt
// cannot succeed, so it must NOT be retried and must NOT count toward the
// circuit breaker. OpenRouter (and the OpenAI Responses API) reuse the
// `server_error` code for both genuine transient server faults AND these
// input-too-large rejections, and provide no structured field to tell them
// apart, so the message is the only discriminator. The signatures are matched
// case-insensitively as substrings. There are five: three anchored to the
// context-window domain ("context window", "context length", "maximum
// context") and two anchored to a token-limit overflow phrasing
// ("exceeds the token limit", "exceeded the token limit"). Each is specific
// enough that a genuine transient rate-limit / capacity / overload message
// cannot false-positive and strip retry + breaker protection.
func isContextOverflowMessage(msg string) bool {
	m := strings.ToLower(msg)
	// Each signature is anchored to the context-window domain so a genuine
	// transient rate-limit / capacity message cannot false-positive and strip
	// retry + breaker protection. OpenRouter reuses `server_error` for both
	// transient faults and input-too-large rejections, and provides no
	// structured field to tell them apart; the message is the only
	// discriminator, so the signatures must be specific.
	return strings.Contains(m, "context window") ||
		strings.Contains(m, "context length") ||
		strings.Contains(m, "maximum context") ||
		strings.Contains(m, "exceeds the token limit") ||
		strings.Contains(m, "exceeded the token limit")
}

// providerErrorStatus maps a provider error code + message to an HTTP-status
// equivalent for retry/breaker classification. It is a thin refinement layer
// over the code-only providerCodeToHTTPStatus: a context-window-overflow
// message is a permanent client error regardless of which code the provider
// glued onto it (OpenRouter reuses `server_error` for both transient faults and
// input-too-large rejections), so it demotes to 0 (non-retryable,
// breaker-neutral) before the code mapping runs. Everything else falls through
// to the code-only mapping unchanged.
func providerErrorStatus(code, msg string) int {
	if isContextOverflowMessage(msg) {
		return 0
	}
	return providerCodeToHTTPStatus(code)
}

// providerCodeToHTTPStatus maps a provider error-code string to an HTTP-status
// equivalent for retry classification. The mapping is a conservative allowlist:
// only KNOWN-transient codes receive a retryable status (429 or 5xx); everything
// else — including permanent client-error codes — maps to 0, which the
// DefaultClassifier treats as non-retryable.
//
// Retryable (transient) codes:
//   - "rate_limit_exceeded"        → 429  (too many requests; canonical retry)
//   - "server_error"               → 503  (provider-side internal error)
//   - "engine_overloaded"          → 503  (provider capacity; retry is correct)
//   - "service_unavailable"        → 503  (provider unavailable)
//   - "gateway_timeout"            → 504  (upstream timeout)
//   - "timeout"                    → 504  (generic timeout)
//
// Non-retryable (permanent) codes map to 0:
//   - "invalid_request_error"      — bad request; replaying won't fix it
//   - "content_filter"             — moderation block; replaying trips the same gate
//   - "model_not_found"            — wrong model; replaying won't fix it
//   - "insufficient_quota"         — billing; replaying won't fix it
//   - "access_denied"              — auth/permission; replaying won't fix it
//   - ""                           — unknown code; fail-safe non-retryable
//   - (all other codes)            — unknown; fail-safe non-retryable
func providerCodeToHTTPStatus(code string) int {
	switch code {
	case "rate_limit_exceeded":
		return 429
	case "server_error", "engine_overloaded", "service_unavailable":
		return 503
	case "gateway_timeout", "timeout":
		return 504
	default:
		// Unknown or permanent codes: return 0 so the classifier treats them as
		// non-retryable. This is the fail-safe path: we never retry something we
		// don't know to be transient.
		return 0
	}
}

// incompleteReason extracts the incomplete_details.reason from a response,
// falling back to the status when no reason was provided.
func incompleteReason(r responses.Response) string {
	if r.IncompleteDetails.Reason != "" {
		return r.IncompleteDetails.Reason
	}
	return string(r.Status)
}

// incompleteMessage renders a human-readable terminal message for a
// response.incomplete event, keyed on the incomplete_details.reason. The loop
// prefixes this with "agent: stream: ", so it reads naturally lowercased after
// that prefix. The raw reason token is kept visible in every branch for
// diagnosability, and an unknown/future reason falls back to the plain
// "response incomplete: <reason>" form (forward-compatible — the reason enum is
// never hard-coded exhaustively). This is a presentation choice only: it does
// NOT affect retry classification (the caller returns the bare, non-retryable
// error verbatim alongside the usage chunk).
func incompleteMessage(r responses.Response) string {
	reason := incompleteReason(r)
	switch reason {
	case "content_filter":
		return "the provider's content filter blocked this response " +
			"(reason: content_filter). This is an UPSTREAM moderation decision, " +
			"not a mecatl error — some routes (e.g. an Azure OpenAI upstream) apply " +
			"aggressive moderation to benign security/credentials wording. " +
			"Retype or resend your message to continue."
	case "max_output_tokens":
		return "the response was cut off at the provider's max output token limit " +
			"(reason: max_output_tokens)."
	default:
		return fmt.Sprintf("response incomplete: %s", reason)
	}
}

// mapUsage maps Responses usage accounting into the domain Usage, including the
// cached-tokens subset into CacheReadTokens and the reasoning-tokens subset into
// ReasoningTokens (both subsets of their inclusive totals — see session.Usage).
func mapUsage(u responses.ResponseUsage) session.Usage {
	inputTokens := int(u.InputTokens)
	return session.Usage{
		InputTokens:      inputTokens,
		OutputTokens:     int(u.OutputTokens),
		CacheReadTokens:  int(u.InputTokensDetails.CachedTokens),
		CacheWriteTokens: cacheWriteTokensFrom(u.InputTokensDetails, inputTokens),
		ReasoningTokens:  int(u.OutputTokensDetails.ReasoningTokens),
	}
}

// cacheWriteTokensFrom probes InputTokensDetails.RawJSON() for the
// OpenAI/OpenRouter "cache_write_tokens" field (ADR 0100) — there is no typed
// SDK field for it (openai-go v3.37.0's ResponseUsageInputTokensDetails only
// types CachedTokens); OpenRouter's own docs confirm the field lives at
// usage.input_tokens_details.cache_write_tokens on the Responses surface,
// mirroring OpenAI's own GPT-5.6+ naming. Any parse failure or absent key
// yields 0 — fail-soft, never guess. Unlike the anthropic adapter (whose raw
// input_tokens EXCLUDES cache tokens, requiring a fold), OpenAI's InputTokens
// already INCLUDES cache writes, so no fold is needed here — only a clamp:
// write is bounded to [0, inputTokens] so CacheWriteTokens ⊆ InputTokens holds
// regardless of how an upstream (mis)reports it.
func cacheWriteTokensFrom(details responses.ResponseUsageInputTokensDetails, inputTokens int) int {
	raw := details.RawJSON()
	if raw == "" {
		return 0
	}
	var probe struct {
		CacheWriteTokens int64 `json:"cache_write_tokens"`
	}
	if err := json.Unmarshal([]byte(raw), &probe); err != nil {
		return 0
	}
	write := int(probe.CacheWriteTokens)
	switch {
	case write < 0:
		return 0
	case write > inputTokens:
		return inputTokens
	default:
		return write
	}
}

// mapStop maps a terminal Responses status to a domain StopReason. A completed
// response is reported as StopEndTurn; the loop decides whether tool calls in the
// turn mean it should continue.
func mapStop(status responses.ResponseStatus) session.StopReason {
	switch status {
	case responses.ResponseStatusCompleted:
		return session.StopEndTurn
	case responses.ResponseStatusIncomplete, responses.ResponseStatusFailed:
		return session.StopError
	case responses.ResponseStatusCancelled:
		return session.StopCancelled
	default:
		return session.StopEndTurn
	}
}

// decodeSSE reads an SSE byte stream (the wire form of a Responses streaming
// response) and translates it into a flat slice of chunks, applying translate to
// each event in order. It exists so tests can drive the exact translation path
// from a recorded golden fixture without a real client. Lines are parsed as
// "data: <json>" records separated by blank lines; "event:" lines are ignored
// because the event JSON carries its own "type".
func decodeSSE(r io.Reader) ([]port.Chunk, error) {
	var out []port.Chunk
	st := streamState{providerRoute: true}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\r")
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "" || data == "[DONE]" {
			continue
		}
		var event responses.ResponseStreamEventUnion
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			return nil, err
		}
		chunks, terr := translate(event, &st)
		out = append(out, chunks...)
		if terr != nil {
			return out, terr
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return out, nil
}
