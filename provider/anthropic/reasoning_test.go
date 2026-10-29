package anthropic

import (
	"bytes"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

func TestReplayRoundTrip(t *testing.T) {
	in := []reasoningBlock{
		{Kind: reasoningKindThinking, Thinking: "first thought\n<&雪>", Signature: "SIG-1=="},
		{Kind: reasoningKindRedacted, Data: "REDACTED=="},
		{Kind: reasoningKindThinking, Thinking: "", Signature: "SIG-OMITTED=="},
	}
	packed := packReplay(in)
	if packed == nil || packed.Format != "anthropic.messages" || packed.Version != 1 {
		t.Fatal("wrong replay namespace/version")
	}
	out := replayBlocks(t, packed)
	if !reflect.DeepEqual(in, out) {
		t.Fatal("ordered thinking, signatures or redacted data changed")
	}
	if !bytes.Equal(packed.Data, packReplay(out).Data) {
		t.Fatal("serialized replay bytes changed")
	}
}

func TestReplayEmptyIsNoOp(t *testing.T) {
	if packReplay(nil) != nil || replayBlocks(t, nil) != nil {
		t.Fatal("empty replay must be a no-op")
	}
}

func TestReplayCompatibilityAndMalformedSafety(t *testing.T) {
	const canary = "PRIVATE-REPLAY-CANARY"
	for _, tc := range []struct {
		name    string
		replay  *session.ProviderReplay
		wantErr bool
	}{
		{"other namespace", &session.ProviderReplay{Format: "openai.responses", Version: 1, Data: []byte(canary)}, false},
		{"other version", &session.ProviderReplay{Format: anthropicReplayFormat, Version: 2, Data: []byte(canary)}, false},
		{"bad JSON", &session.ProviderReplay{Format: anthropicReplayFormat, Version: 1, Data: []byte(canary)}, true},
		{"invalid UTF8", &session.ProviderReplay{Format: anthropicReplayFormat, Version: 1, Data: []byte{'\xff'}}, true},
		{"null", &session.ProviderReplay{Format: anthropicReplayFormat, Version: 1, Data: []byte(`null`)}, true},
		{"missing schema", &session.ProviderReplay{Format: anthropicReplayFormat, Version: 1, Data: []byte(`{}`)}, true},
		{"wrong schema", &session.ProviderReplay{Format: anthropicReplayFormat, Version: 1, Data: []byte(`{"v":2,"blocks":[]}`)}, true},
		{"bad block type", &session.ProviderReplay{Format: anthropicReplayFormat, Version: 1, Data: []byte(`{"v":1,"blocks":[{"t":"PRIVATE-REPLAY-CANARY"}]}`)}, true},
		{"missing signature", &session.ProviderReplay{Format: anthropicReplayFormat, Version: 1, Data: []byte(`{"v":1,"blocks":[{"t":"thinking","x":"PRIVATE-REPLAY-CANARY"}]}`)}, true},
		{"missing redacted data", &session.ProviderReplay{Format: anthropicReplayFormat, Version: 1, Data: []byte(`{"v":1,"blocks":[{"t":"redacted"}]}`)}, true},
		{"mixed fields", &session.ProviderReplay{Format: anthropicReplayFormat, Version: 1, Data: []byte(`{"v":1,"blocks":[{"t":"redacted","d":"PRIVATE-REPLAY-CANARY","s":"sig"}]}`)}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			blocks, err := unpackReplay(tc.replay)
			if (err != nil) != tc.wantErr || len(blocks) != 0 {
				t.Fatal("unexpected replay decoding result")
			}
			p := testProvider()
			params, buildErr := p.buildParams(port.LLMRequest{Model: "claude-sonnet-4-6", Messages: []port.LLMMessage{
				assistantMessage("safe text", tc.replay, []port.LLMToolCall{toolCall("toolu_safe", "Read", nil)}),
			}})
			if (buildErr != nil) != tc.wantErr {
				t.Fatal("request did not honor replay compatibility/error")
			}
			if buildErr != nil {
				if len(params.Messages) != 0 || strings.Contains(fmt.Sprintf("%+v", buildErr), canary) {
					t.Fatal("malformed replay leaked private data or partial request")
				}
			} else if blocks := messageContent(params.Messages[0]); len(blocks) != 2 || blocks[0].OfText.Text != "safe text" {
				t.Fatal("incompatible replay affected ordinary text/tool history")
			}
		})
	}
}

// The migration owner can wrap valid old Message.Reasoning bytes unchanged.
func TestLegacyReplayEncoding(t *testing.T) {
	const legacy = `{"v":1,"blocks":[{"t":"thinking","x":"thought","s":"SIG=="},{"t":"redacted","d":"DATA=="}]}`
	replay := &session.ProviderReplay{Format: "anthropic.messages", Version: 1, Data: []byte(legacy)}
	want := []reasoningBlock{{Kind: reasoningKindThinking, Thinking: "thought", Signature: "SIG=="}, {Kind: reasoningKindRedacted, Data: "DATA=="}}
	if !reflect.DeepEqual(replayBlocks(t, replay), want) || !bytes.Equal(packReplay(want).Data, replay.Data) {
		t.Fatal("legacy serialized envelope cannot be wrapped byte-exactly")
	}
}
