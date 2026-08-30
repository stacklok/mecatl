package agent_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/session"
)

// TestHeuristicTokenCounterDeterministic checks Count is deterministic and scales
// with byte length per the chars-per-token ratio.
func TestHeuristicTokenCounterDeterministic(t *testing.T) {
	c := agent.HeuristicTokenCounter{}
	const s = "the quick brown fox jumps over the lazy dog"
	first := c.Count(s)
	for i := 0; i < 5; i++ {
		if got := c.Count(s); got != first {
			t.Fatalf("Count not deterministic: %d vs %d", got, first)
		}
	}
	// ~4 chars/token: 43 chars / 4 = 10.
	if want := len(s) / 4; first != want {
		t.Fatalf("Count(%q) = %d, want %d", s, first, want)
	}
}

// TestHeuristicTokenCounterCharsPerTokenOverride checks the CharsPerToken knob.
func TestHeuristicTokenCounterCharsPerTokenOverride(t *testing.T) {
	c := agent.HeuristicTokenCounter{CharsPerToken: 2}
	s := "abcdefgh" // 8 chars
	if got := c.Count(s); got != 4 {
		t.Fatalf("Count with cpt=2 = %d, want 4", got)
	}
}

// TestHeuristicTokenCounterMessagesOverhead checks CountMessages adds per-message
// and per-tool-call framing overhead on top of the body estimate, so a history of
// many short messages is not undercounted to zero.
func TestHeuristicTokenCounterMessagesOverhead(t *testing.T) {
	c := agent.HeuristicTokenCounter{}
	msgs := []session.Message{
		session.NewSystemMessage(""),
		session.NewUserMessage(""),
		session.NewAssistantMessage("", "", []session.ToolCall{
			session.NewToolCall("c1", "Read", json.RawMessage(`{}`)),
		}),
		session.NewToolMessage(session.NewToolResult("c1", "")),
	}
	// Every message contributes its per-message overhead even with empty bodies,
	// and the tool call adds its own overhead, so the total must be positive.
	if got := c.CountMessages(msgs); got <= 0 {
		t.Fatalf("CountMessages with empty bodies = %d, want > 0 (framing overhead)", got)
	}
	// Deterministic across calls.
	if a, b := c.CountMessages(msgs), c.CountMessages(msgs); a != b {
		t.Fatalf("CountMessages not deterministic: %d vs %d", a, b)
	}
}

// TestCountMessagesCountsMediaBytes asserts a multimodal message's inline media
// bytes are counted, so a multimodal message is not undercounted vs the same
// text alone.
func TestCountMessagesCountsMediaBytes(t *testing.T) {
	c := agent.HeuristicTokenCounter{}
	textOnly := []session.Message{session.NewUserMessage("hello")}
	withMedia := []session.Message{session.NewUserMessageWithParts("hello", []session.Content{
		{Kind: session.MediaImage, MIMEType: "image/png", Data: make([]byte, 8000)},
	})}
	base := c.CountMessages(textOnly)
	media := c.CountMessages(withMedia)
	if media <= base {
		t.Fatalf("media count %d not greater than text-only %d — media not counted", media, base)
	}
	// 8000 bytes at 4 chars/token ~ 2000 tokens.
	if media-base < 1500 {
		t.Fatalf("media delta %d too small; image bytes undercounted", media-base)
	}
}

func TestCountMessagesToolResultPartsUsesLargerRepresentation(t *testing.T) {
	c := agent.HeuristicTokenCounter{CharsPerToken: 1}
	part := session.Content{
		BlockKind:    session.BlockResourceLink,
		Kind:         session.MediaImage,
		MIMEType:     "application/example",
		Data:         []byte("data"),
		URL:          "https://example.test/resource",
		Text:         "text",
		Name:         "name",
		Title:        "title",
		Description:  "description",
		Size:         123,
		Audience:     []string{"user", "assistant"},
		Priority:     1.25,
		LastModified: "yesterday",
	}
	partsBytes := 4 + len(string(part.BlockKind)) + len(string(part.Kind)) + len(part.MIMEType) +
		((len(part.Data)+2)/3)*4 + len(part.URL) + len(part.Text) + len(part.Name) + len(part.Title) +
		len(part.Description) + len("123") + len("user") + len("assistant") + len("1.25") +
		len(part.LastModified)
	content := strings.Repeat("c", partsBytes+20)
	result := session.NewToolResultWithParts("call", content, []session.Content{part})
	got := c.CountMessages([]session.Message{session.NewToolMessage(result)})
	if want := 4 + len(result.CallID) + len(content); got != want {
		t.Fatalf("CountMessages = %d, want %d (max(Content, Parts), not their sum)", got, want)
	}

	result.Content = "short"
	got = c.CountMessages([]session.Message{session.NewToolMessage(result)})
	if want := 4 + len(result.CallID) + partsBytes; got != want {
		t.Fatalf("CountMessages = %d, want %d (all typed part data and metadata)", got, want)
	}
}

func TestHeuristicCounterCountsReplayIdentifiersAndBase64Projection(t *testing.T) {
	c := agent.HeuristicTokenCounter{CharsPerToken: 1}
	call := session.NewToolCall("call-identifier", "T", json.RawMessage(`{}`))
	call.ItemID = "provider-item"
	assistant := session.NewAssistantMessage("", "reasoning", []session.ToolCall{call})
	assistant.ProviderPhase = "commentary"
	assistant.ReasoningItemID = "reasoning-item"
	result := session.NewToolResultWithParts(call.ID, "", []session.Content{{Kind: session.MediaImage, MIMEType: "image/png", Data: []byte{1, 2, 3, 4}}})
	withFields := c.CountMessages([]session.Message{assistant, session.NewToolMessage(result)})

	call.ID, call.ItemID = "", ""
	assistant = session.NewAssistantMessage("", "reasoning", []session.ToolCall{call})
	withoutFields := c.CountMessages([]session.Message{assistant, session.NewToolMessage(session.NewToolResultWithParts("", "", []session.Content{{Kind: session.MediaImage, MIMEType: "image/png", Data: []byte{1, 2, 3, 4}}}))})
	wantDelta := len("call-identifier")*2 + len("provider-item") + len("commentary") + len("reasoning-item")
	if got := withFields - withoutFields; got != wantDelta {
		t.Fatalf("replay identifier delta = %d, want %d", got, wantDelta)
	}
	media := c.CountMessages([]session.Message{session.NewUserMessageWithParts("", []session.Content{{Kind: session.MediaImage, Data: []byte{1, 2, 3, 4}}})})
	rawSized := c.CountMessages([]session.Message{session.NewUserMessageWithParts("", []session.Content{{Kind: session.MediaImage, Data: []byte{1, 2, 3}}})})
	if media-rawSized != 4 { // base64 lengths: 8 - 4
		t.Fatalf("base64 projection delta = %d, want 4", media-rawSized)
	}
}
