package anthropic

import (
	"encoding/json"
	"errors"
	"unicode/utf8"

	"github.com/stacklok/mecatl/engine/session"
)

const anthropicReplayFormat = "anthropic.messages"
const anthropicReplayVersion uint32 = 1
const reasoningEnvelopeVersion = 1

const (
	reasoningKindThinking = "thinking"
	reasoningKindRedacted = "redacted"
)

// reasoningEnvelope serializes signed/redacted blocks in their relative order.
// Replay places these blocks before text and tool_use; it does not preserve an
// exact ordered sequence of all provider content blocks.
type reasoningEnvelope struct {
	V      int              `json:"v"`
	Blocks []reasoningBlock `json:"blocks"`
}

type reasoningBlock struct {
	Kind      string `json:"t"`
	Thinking  string `json:"x,omitempty"`
	Signature string `json:"s,omitempty"`
	Data      string `json:"d,omitempty"`
}

func packReplay(blocks []reasoningBlock) *session.ProviderReplay {
	if len(blocks) == 0 {
		return nil
	}
	// Only scalar strings are encoded, so Marshal cannot fail.
	data, _ := json.Marshal(reasoningEnvelope{V: reasoningEnvelopeVersion, Blocks: blocks})
	return &session.ProviderReplay{Format: anthropicReplayFormat, Version: anthropicReplayVersion, Data: data}
}

// Legacy carriers are fail-soft; compatible anthropic.messages envelopes fail
// closed when malformed. Incompatible namespaces are never inspected.
func unpackReplay(replay *session.ProviderReplay) ([]reasoningBlock, error) {
	if replay != nil && replay.Format == "mecatl.legacy-message" && replay.Version == 1 {
		return unpackLegacyReplay(replay.Data), nil
	}
	if replay == nil || replay.Format != anthropicReplayFormat || replay.Version != anthropicReplayVersion {
		return nil, nil
	}
	return unpackCurrentReplay(replay.Data)
}

// unpackLegacyReplay interprets snapshot Reasoning preserved in the generic
// mecatl.legacy-message wrapper; only the issuing adapter knows its meaning.
// A v=1 reasoning envelope supplies signed/redacted blocks. Unsupported or
// malformed legacy data is ignored; this recovers replay, not executable identity.
func unpackLegacyReplay(data []byte) []reasoningBlock {
	var legacy struct {
		Reasoning string `json:"reasoning"`
	}
	if !utf8.Valid(data) || json.Unmarshal(data, &legacy) != nil || !utf8.ValidString(legacy.Reasoning) {
		return nil
	}
	var env reasoningEnvelope
	if json.Unmarshal([]byte(legacy.Reasoning), &env) != nil || env.V != reasoningEnvelopeVersion {
		return nil
	}
	var blocks []reasoningBlock
	for _, block := range env.Blocks {
		if block.Kind == reasoningKindThinking || block.Kind == reasoningKindRedacted {
			blocks = append(blocks, block)
		}
	}
	return blocks
}

func unpackCurrentReplay(data []byte) ([]reasoningBlock, error) {
	if !utf8.Valid(data) {
		return nil, errors.New("anthropic: malformed Messages replay envelope")
	}
	var env *reasoningEnvelope
	if err := json.Unmarshal(data, &env); err != nil || env == nil || env.V != reasoningEnvelopeVersion {
		return nil, errors.New("anthropic: malformed Messages replay envelope")
	}
	for _, block := range env.Blocks {
		switch block.Kind {
		case reasoningKindThinking:
			if block.Signature == "" || block.Data != "" {
				return nil, errors.New("anthropic: invalid thinking replay block")
			}
		case reasoningKindRedacted:
			if block.Data == "" || block.Thinking != "" || block.Signature != "" {
				return nil, errors.New("anthropic: invalid redacted replay block")
			}
		default:
			return nil, errors.New("anthropic: unknown Messages replay block")
		}
	}
	return env.Blocks, nil
}
