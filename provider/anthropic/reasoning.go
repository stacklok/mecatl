package anthropic

import "encoding/json"

// reasoningEnvelopeVersion is the schema version of the packed reasoning blob.
// It lets the envelope evolve without misreading an older blob round-tripped
// from a stored session.
const reasoningEnvelopeVersion = 1

// reasoning block discriminators.
const (
	reasoningKindThinking = "thinking"
	reasoningKindRedacted = "redacted"
)

// reasoningEnvelope is the JSON shape packed INTO the opaque
// session.Message.Reasoning string. Anthropic's reasoning-replay unit is a LIST
// of thinking blocks ({thinking,signature}) plus possibly redacted_thinking
// blocks ({data}); a single opaque string cannot hold a list directly, so the
// adapter serializes the ordered list as this envelope. The domain value object
// stays a bare string — only this adapter knows the blob is a JSON list.
//
// Order in Blocks is the SSE index order = the model's original emission order;
// the unpacker reconstructs blocks in that order, BEFORE the tool_use blocks, so
// the §extended-thinking sequence rule (consecutive thinking blocks must match
// the original outputs, unrearranged) is preserved by construction.
type reasoningEnvelope struct {
	V      int              `json:"v"`
	Blocks []reasoningBlock `json:"blocks"`
}

// reasoningBlock is one entry in the envelope. For a thinking block, Kind is
// "thinking" and Thinking+Signature carry the (possibly empty) thinking text and
// its opaque signature. For a redacted block, Kind is "redacted" and Data
// carries the opaque redacted_thinking payload.
type reasoningBlock struct {
	Kind      string `json:"t"`
	Thinking  string `json:"x,omitempty"`
	Signature string `json:"s,omitempty"`
	Data      string `json:"d,omitempty"`
}

// packReasoning serializes the ordered list of reasoning blocks into the opaque
// envelope string stored on session.Message.Reasoning. An empty list packs to
// "" (replay no-op, exactly like the openai empty-blob case) so a turn with no
// thinking stores no reasoning. The marshal cannot fail for these scalar fields.
func packReasoning(blocks []reasoningBlock) string {
	if len(blocks) == 0 {
		return ""
	}
	b, err := json.Marshal(reasoningEnvelope{V: reasoningEnvelopeVersion, Blocks: blocks})
	if err != nil {
		// Defensive: scalar string fields never fail to marshal. Fail soft to a
		// replay no-op rather than panic.
		return ""
	}
	return string(b)
}

// unpackReasoning parses the opaque envelope string back into the ordered list
// of reasoning blocks. It is FAIL-SOFT: an empty string yields no blocks (replay
// no-op), and a malformed/unknown-version blob also yields no blocks rather than
// an error — the turn still replays, just without thinking. (When thinking is
// active and a tool_use is present, an empty result means the assistant turn
// carries no thinking blocks, which the caller must already tolerate for the
// no-thinking case.) It never panics on arbitrary input (fuzzed).
func unpackReasoning(blob string) []reasoningBlock {
	if blob == "" {
		return nil
	}
	var env reasoningEnvelope
	if err := json.Unmarshal([]byte(blob), &env); err != nil {
		return nil
	}
	if env.V != reasoningEnvelopeVersion {
		return nil
	}
	// Keep only well-formed blocks; drop anything with an unknown discriminator.
	out := make([]reasoningBlock, 0, len(env.Blocks))
	for _, blk := range env.Blocks {
		switch blk.Kind {
		case reasoningKindThinking, reasoningKindRedacted:
			out = append(out, blk)
		default:
			// skip unknown kinds (forward-compatible)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
