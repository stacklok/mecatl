// Package tokenizer provides a tiktoken-backed agent.TokenCounter. It wraps
// github.com/tiktoken-go/tokenizer, whose byte-pair-encoding rank tables are
// COMPILED INTO the package as Go source (no go:embed of external files, no
// runtime download), so token counting is fully offline and deterministic — a
// hard requirement for a harness that must run air-gapped and reproducibly.
//
// This adapter is wired only in the composition root (cmd/mecated); the agent
// package depends solely on the agent.TokenCounter interface, never on this
// concrete tokenizer. The default counter remains the dependency-free heuristic
// in engine/agent; this is the opt-in "real tokenizer" tier.
package tokenizer

import (
	"encoding/base64"
	"fmt"
	"strconv"

	"github.com/tiktoken-go/tokenizer"

	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/session"
)

// Encoding identifies a tiktoken byte-pair-encoding table.
type Encoding string

const (
	// O200kBase is the encoding used by the GPT-4o / GPT-4.1 / o-series and
	// later frontier models. It is the sensible default for current OpenAI models.
	O200kBase Encoding = "o200k_base"
	// Cl100kBase is the encoding used by GPT-3.5-turbo and GPT-4 (pre-4o).
	Cl100kBase Encoding = "cl100k_base"
)

// perMessageOverhead is the framing-token cost attributed to each message
// (role tag + message envelope) on top of its encoded body, mirroring OpenAI's
// documented ~3–4 tokens-per-message overhead. It keeps a many-small-message
// history from being undercounted, matching the heuristic counter's intent.
const perMessageOverhead = 4

// perToolCallOverhead is the framing-token cost attributed to each tool-call
// envelope. IDs, names, arguments, and provider item IDs are encoded separately.
const perToolCallOverhead = 4

// perContentPartOverhead covers the provider's typed-block envelope and discriminator.
const perContentPartOverhead = 4

// Counter is a tiktoken-backed agent.TokenCounter. It is safe for concurrent use:
// the underlying codec is read-only after construction.
type Counter struct {
	codec tokenizer.Codec
}

// Compile-time assertion that Counter satisfies the agent seam.
var _ agent.TokenCounter = (*Counter)(nil)

// New constructs a Counter for the given encoding. The rank tables are loaded
// from the compiled-in vocabulary (offline); an unknown encoding returns an
// error.
func New(enc Encoding) (*Counter, error) {
	codec, err := tokenizer.Get(tokenizer.Encoding(enc))
	if err != nil {
		return nil, fmt.Errorf("tokenizer: load encoding %q: %w", enc, err)
	}
	return &Counter{codec: codec}, nil
}

// NewForModel constructs a Counter for the tiktoken encoding associated with the
// given model name, falling back to O200kBase when the model is unrecognised
// (every current OpenAI frontier model uses o200k_base).
func NewForModel(model string) (*Counter, error) {
	if codec, err := tokenizer.ForModel(tokenizer.Model(model)); err == nil {
		return &Counter{codec: codec}, nil
	}
	return New(O200kBase)
}

// Count returns the exact tiktoken token count of text.
func (c *Counter) Count(text string) int {
	if text == "" {
		return 0
	}
	ids, _, err := c.codec.Encode(text)
	if err != nil {
		// Encoding a plain string does not fail for the embedded codecs; fall back
		// to a coarse estimate rather than panicking on the off chance it does.
		return len(text) / 4
	}
	return len(ids)
}

// CountMessages returns the tiktoken token count of a conversation slice, summing
// each message's encoded text/reasoning/tool bodies plus the per-message and
// per-tool-call framing overhead.
func (c *Counter) CountMessages(msgs []session.Message) int {
	total := 0
	for _, m := range msgs {
		total += perMessageOverhead
		total += c.Count(m.Text)
		total += c.Count(m.Reasoning)
		total += c.Count(m.ProviderPhase)
		total += c.Count(m.ReasoningItemID)
		for _, call := range m.ToolCalls {
			total += perToolCallOverhead
			total += c.Count(string(call.ID))
			total += c.Count(call.Name)
			total += c.Count(string(call.Args))
			total += c.Count(call.ItemID)
		}
		if m.ToolResult != nil {
			total += c.Count(string(m.ToolResult.CallID))
			total += max(c.Count(m.ToolResult.Content), c.countContentParts(m.ToolResult.Parts))
		}
		total += c.countContentParts(m.Parts)
	}
	return total
}

func (c *Counter) countContentParts(parts []session.Content) int {
	total := 0
	for _, p := range parts {
		total += perContentPartOverhead
		data := string(p.Data)
		if len(p.Data) > 0 && (p.Kind == session.MediaImage || p.Kind == session.MediaAudio) {
			data = base64.StdEncoding.EncodeToString(p.Data)
		}
		// Resource metadata is conservatively counted because shared provider routing
		// may render it into model-visible text. Fixed overhead covers framing only.
		for _, value := range []string{
			string(p.BlockKind), string(p.Kind), p.MIMEType, data, p.URL,
			p.Text, p.Name, p.Title, p.Description, p.LastModified,
		} {
			total += c.Count(value)
		}
		if p.Size != 0 {
			total += c.Count(strconv.FormatInt(p.Size, 10))
		}
		if p.Priority != 0 {
			total += c.Count(strconv.FormatFloat(p.Priority, 'g', -1, 64))
		}
		for _, audience := range p.Audience {
			total += c.Count(audience)
		}
	}
	return total
}
