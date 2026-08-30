package agent

import (
	"strconv"

	"github.com/stacklok/mecatl/engine/prompt"
	"github.com/stacklok/mecatl/engine/session"
)

// TokenCounter estimates how many model tokens a piece of text or a slice of
// conversation messages occupies. It is a seam (ARCHITECTURE.md §8, gauntlet
// #12): the loop and the compaction cascade consume it to decide when to compact
// and which segments to drop, while the concrete tokenizer (a dependency-free
// heuristic by default, an optional tiktoken-backed adapter in production) is
// injected from the composition root.
//
// Implementations MUST be deterministic and SHOULD be cheap: Count/CountMessages
// run on every turn. A counter is never required to be exact — the loop only
// needs the estimate to be in the right ballpark — but it must be stable, since
// an unstable estimate would make compaction non-reproducible.
type TokenCounter interface {
	// Count returns the estimated token count of a single string.
	Count(text string) int
	// CountMessages returns the estimated token count of a conversation slice,
	// summing each message's text/reasoning/tool bodies plus the per-message and
	// per-role framing overhead the provider adds on the wire.
	CountMessages(msgs []session.Message) int
}

// byteTokenCounter and layeredTokenCounter let in-package counters avoid temporary
// strings while keeping TokenCounter's public compatibility surface unchanged.
type byteTokenCounter interface {
	countBytes([]byte) int
}

type layeredTokenCounter interface {
	countLayered(prompt.Layered) int
}

func countBytes(counter TokenCounter, text []byte) int {
	if c, ok := counter.(byteTokenCounter); ok {
		return c.countBytes(text)
	}
	return counter.Count(string(text))
}

func countLayered(counter TokenCounter, system prompt.Layered) int {
	if c, ok := counter.(layeredTokenCounter); ok {
		return c.countLayered(system)
	}
	return counter.Count(system.Render())
}

// estimateZeroUsageInput implements the issue-#82 DISPLAY-ONLY zero-usage input
// fallback. When a turn reported a real input-token figure (reported != 0) it returns
// (0, false): no estimate. When the turn reported NO input usage (a stalled / usage-less
// turn) AND the conversation is non-empty, it returns the TokenCounter's estimate over
// the conversation and (est, true) so the caller knows the value is an estimate. The
// non-empty guard is load-bearing: it ensures a genuinely empty conversation NEVER gets
// a phantom estimate even if the counter would attribute framing overhead to an empty
// slice — it is the boundary the fallback must not cross. The estimate feeds ONLY the
// display meter (turn.end), never the cumulative usage or any token budget.
func estimateZeroUsageInput(reported int, msgs []session.Message, tc TokenCounter) (int, bool) {
	if reported != 0 || len(msgs) == 0 {
		return 0, false
	}
	return tc.CountMessages(msgs), true
}

// charsPerToken is the bytes→tokens ratio the heuristic counter uses. English
// prose and code both sit near ~4 characters per token for the common
// byte-pair-encoding tokenizers, so this is a serviceable offline estimate.
const charsPerToken = 4

// perMessageOverhead is the fixed token cost the heuristic attributes to every
// message for the role tag and message framing the provider wraps each turn in
// (OpenAI documents ~3–4 framing tokens per message). Accounting for it keeps the
// estimate from undercounting many-small-message histories.
const perMessageOverhead = 4

// perToolCallOverhead is the fixed token cost the heuristic attributes to each
// tool-call envelope. IDs, names, arguments, and provider item IDs are counted
// separately as transmitted payload.
const perToolCallOverhead = 4

// perToolSpecOverhead covers the advertised tool-definition envelope.
const perToolSpecOverhead = 4

// perContentPartOverhead covers the provider's typed-block envelope and discriminator.
const perContentPartOverhead = 4

// HeuristicTokenCounter is the default, dependency-free TokenCounter. It divides
// byte length by charsPerToken and adds a small fixed overhead per message and
// per tool call so a history of many short messages is not undercounted. It
// performs no network or tokenizer-table lookup, so it is fully deterministic and
// testable offline. It replaces the inline 4-chars/token estimate the loop used
// before the TokenCounter seam existed.
type HeuristicTokenCounter struct {
	// CharsPerToken overrides charsPerToken when > 0.
	CharsPerToken int
}

// Count implements TokenCounter for a single string.
func (h HeuristicTokenCounter) Count(text string) int {
	return len(text) / h.charsPerToken()
}

func (h HeuristicTokenCounter) countBytes(text []byte) int {
	return len(text) / h.charsPerToken()
}

func (h HeuristicTokenCounter) countLayered(system prompt.Layered) int {
	length := len(system.StablePrefix) + len(system.VolatileSuffix)
	if system.StablePrefix != "" && system.VolatileSuffix != "" {
		length += len("\n\n")
	}
	return length / h.charsPerToken()
}

func (h HeuristicTokenCounter) charsPerToken() int {
	if h.CharsPerToken > 0 {
		return h.CharsPerToken
	}
	return charsPerToken
}

// CountMessages implements TokenCounter for a conversation slice, summing the
// text/reasoning/tool bodies (divided by the chars-per-token ratio) plus the
// fixed per-message and per-tool-call framing overhead.
func (h HeuristicTokenCounter) CountMessages(msgs []session.Message) int {
	cpt := h.charsPerToken()
	total := 0
	for _, m := range msgs {
		total += perMessageOverhead
		total += (len(m.Text) + len(m.Reasoning) + len(m.ProviderPhase) + len(m.ReasoningItemID)) / cpt
		for _, c := range m.ToolCalls {
			total += perToolCallOverhead
			total += (len(c.ID) + len(c.Name) + len(c.Args) + len(c.ItemID)) / cpt
		}
		if m.ToolResult != nil {
			total += len(m.ToolResult.CallID) / cpt
			contentTokens := len(m.ToolResult.Content) / cpt
			partsTokens := heuristicContentParts(m.ToolResult.Parts, cpt)
			total += max(contentTokens, partsTokens)
		}
		total += heuristicContentParts(m.Parts, cpt)
	}
	return total
}

func heuristicContentParts(parts []session.Content, cpt int) int {
	total := 0
	for _, p := range parts {
		dataBytes := len(p.Data)
		if dataBytes > 0 && (p.Kind == session.MediaImage || p.Kind == session.MediaAudio) {
			dataBytes = ((dataBytes + 2) / 3) * 4
		}
		// Resource metadata is conservatively counted because shared provider routing
		// may project it into a model-visible textual block. Inline media bytes use
		// their base64 wire length; fixed overhead covers framing only.
		bytes := len(p.BlockKind) + len(p.Kind) + len(p.MIMEType) + dataBytes +
			len(p.URL) + len(p.Text) + len(p.Name) + len(p.Title) + len(p.Description) +
			len(p.LastModified)
		if p.Size != 0 {
			bytes += len(strconv.FormatInt(p.Size, 10))
		}
		if p.Priority != 0 {
			bytes += len(strconv.FormatFloat(p.Priority, 'g', -1, 64))
		}
		for _, audience := range p.Audience {
			bytes += len(audience)
		}
		total += perContentPartOverhead + bytes/cpt
	}
	return total
}

// Compile-time assertion that the default counter satisfies the seam.
var _ TokenCounter = HeuristicTokenCounter{}
