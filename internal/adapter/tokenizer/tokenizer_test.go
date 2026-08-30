package tokenizer_test

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/tokenizer"
)

// TestCountKnownStrings checks the tiktoken-backed counter returns the canonical
// token counts for known strings, fully offline (the vocab is compiled in). These
// counts match OpenAI's tiktoken reference for the respective encodings.
func TestCountKnownStrings(t *testing.T) {
	cases := []struct {
		enc   tokenizer.Encoding
		text  string
		count int
	}{
		{tokenizer.Cl100kBase, "hello world", 2},
		{tokenizer.O200kBase, "hello world", 2},
		{tokenizer.Cl100kBase, "tiktoken is great!", 6},
		{tokenizer.Cl100kBase, "", 0},
	}
	for _, tc := range cases {
		c, err := tokenizer.New(tc.enc)
		if err != nil {
			t.Fatalf("New(%q): %v", tc.enc, err)
		}
		if got := c.Count(tc.text); got != tc.count {
			t.Fatalf("Count(%q) with %s = %d, want %d", tc.text, tc.enc, got, tc.count)
		}
	}
}

// TestNewForModel resolves a known model to a working counter and falls back to
// o200k_base for an unknown model rather than erroring.
func TestNewForModel(t *testing.T) {
	for _, model := range []string{"gpt-4o", "gpt-5", "some-unknown-model"} {
		c, err := tokenizer.NewForModel(model)
		if err != nil {
			t.Fatalf("NewForModel(%q): %v", model, err)
		}
		if got := c.Count("hello world"); got != 2 {
			t.Fatalf("NewForModel(%q).Count = %d, want 2", model, got)
		}
	}
}

// TestCountMessages sums encoded bodies plus framing overhead and is deterministic.
func TestCountMessages(t *testing.T) {
	c, err := tokenizer.New(tokenizer.O200kBase)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	msgs := []session.Message{
		session.NewSystemMessage("hello world"),
		session.NewUserMessage("hello world"),
	}
	got := c.CountMessages(msgs)
	// Two messages: 2*(perMessageOverhead=4) framing + 2*(2 tokens body) = 12.
	if got != 12 {
		t.Fatalf("CountMessages = %d, want 12", got)
	}
	if a, b := c.CountMessages(msgs), c.CountMessages(msgs); a != b {
		t.Fatalf("CountMessages not deterministic: %d vs %d", a, b)
	}
}

func TestCountMessagesToolResultPartsUsesLargerRepresentation(t *testing.T) {
	c, err := tokenizer.New(tokenizer.O200kBase)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	part := session.Content{
		BlockKind:    session.BlockStructuredContent,
		Kind:         session.MediaAudio,
		MIMEType:     "application/json",
		Data:         []byte("binary payload"),
		URL:          "https://example.test/resource",
		Text:         `{"answer":42}`,
		Name:         "name",
		Title:        "title",
		Description:  "description",
		Size:         123,
		Audience:     []string{"user", "assistant"},
		Priority:     1.25,
		LastModified: "yesterday",
	}
	partTokens := 4
	for _, value := range []string{
		string(part.BlockKind), string(part.Kind), part.MIMEType, base64.StdEncoding.EncodeToString(part.Data),
		part.URL, part.Text, part.Name, part.Title, part.Description, "123", "user",
		"assistant", "1.25", part.LastModified,
	} {
		partTokens += c.Count(value)
	}
	content := strings.Repeat("content ", partTokens+20)
	result := session.NewToolResultWithParts("call", content, []session.Content{part})
	if got, want := c.CountMessages([]session.Message{session.NewToolMessage(result)}), 4+c.Count(string(result.CallID))+c.Count(content); got != want {
		t.Fatalf("CountMessages = %d, want %d (max(Content, Parts), not their sum)", got, want)
	}

	result.Content = "short"
	if got, want := c.CountMessages([]session.Message{session.NewToolMessage(result)}), 4+c.Count(string(result.CallID))+partTokens; got != want {
		t.Fatalf("CountMessages = %d, want %d (all typed part data and metadata)", got, want)
	}
}

func TestCounterCountsReplayIdentifiersAndBase64Projection(t *testing.T) {
	c, err := tokenizer.New(tokenizer.O200kBase)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	call := session.NewToolCall("distinct-call-id", "Read", []byte(`{}`))
	call.ItemID = "fc_provider_item"
	message := session.NewAssistantMessage("", "opaque reasoning", []session.ToolCall{call})
	message.ProviderPhase = "commentary"
	message.ReasoningItemID = "rs_provider_item"
	result := session.NewToolResult(call.ID, "result")
	got := c.CountMessages([]session.Message{message, session.NewToolMessage(result)})

	call.ID, call.ItemID = "", ""
	baseline := session.NewAssistantMessage("", "opaque reasoning", []session.ToolCall{call})
	wantDelta := c.Count("distinct-call-id")*2 + c.Count("fc_provider_item") + c.Count("commentary") + c.Count("rs_provider_item")
	base := c.CountMessages([]session.Message{baseline, session.NewToolMessage(session.NewToolResult("", "result"))})
	if got-base != wantDelta {
		t.Fatalf("replay identifier delta = %d, want %d", got-base, wantDelta)
	}

	data := []byte{0xff, 0x00, 0x7f, 0x01}
	media := c.CountMessages([]session.Message{session.NewUserMessageWithParts("", []session.Content{{Kind: session.MediaImage, Data: data}})})
	want := 4 + c.Count(string(session.MediaImage)) + c.Count(base64.StdEncoding.EncodeToString(data)) + 4
	if media != want {
		t.Fatalf("inline media count = %d, want base64-projected %d", media, want)
	}
}

// TestNewUnknownEncoding returns an error for an unrecognised encoding name.
func TestNewUnknownEncoding(t *testing.T) {
	if _, err := tokenizer.New("not-a-real-encoding"); err == nil {
		t.Fatalf("expected error for unknown encoding")
	}
}
