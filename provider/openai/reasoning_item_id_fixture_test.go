package openai

import (
	"reflect"
	"testing"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

// TestReasoningItemIDMixedItemBoundary is the cold reproducer for the
// reasoning-item id="" Responses-replay fix. It drives a recorded SSE fixture
// end-to-end through decodeSSE (the exact translate path the live adapter uses)
// and asserts the reasoning item's OWN provider id ("rs_123") survives the
// SSE→port.Chunk boundary intact and is NOT confused with the assistant message
// item's id ("msg_abc") that the text deltas ride on.
//
// The fixture mixes a reasoning item rs_123 with a message item msg_abc.
// ChunkReplay keeps each ciphertext bound to its own reasoning item ID inside
// the opaque envelope; readable text chunks carry no private replay. Strict
// gateways require that same item ID when the ciphertext is replayed.
func TestReasoningItemIDMixedItemBoundary(t *testing.T) {
	chunks := decodeFixture(t, "reasoning_item_id_mixed.sse")

	// (a) The reasoning replay chunk pairs the blob with the reasoning item's OWN
	// id "rs_123" — not the message item's "msg_abc", and not empty. Under the
	// pre-fix code item.ID was dropped entirely, so this assertion discriminates.
	var reasoningItemChunk *port.Chunk
	for i := range chunks {
		if chunks[i].Kind == port.ChunkReplay {
			reasoningItemChunk = &chunks[i]
		}
	}
	if reasoningItemChunk == nil {
		t.Fatalf("no ChunkReasoningItem in stream; chunks=%+v", chunks)
	}
	items := replayReasoning(reasoningItemChunk.Replay)
	if len(items) != 1 {
		t.Fatalf("unpacked %d reasoning items, want 1; packed=%q", len(items), reasoningItemChunk.Text)
	}
	if items[0].ID != "rs_123" {
		t.Errorf("reasoning item id = %q, want %q (must be the reasoning item's own id, not the message item's and not empty)",
			items[0].ID, "rs_123")
	}
	if items[0].Blob != "ENCRYPTED_RS_123" {
		t.Errorf("reasoning item blob = %q, want %q (the encrypted_content replay blob)", items[0].Blob, "ENCRYPTED_RS_123")
	}

	// (b) The assistant TEXT chunks must NOT carry the reasoning item id — the
	// reasoning id belongs only to the reasoning item, not the message item.
	// ChunkText carries no replay payload, including reasoning-item identity.
	for i, c := range chunks {
		if c.Kind != port.ChunkText {
			continue
		}
		if c.Replay != nil {
			t.Errorf("text chunk %d leaks private replay", i)
		}
	}

	// (c) The final converted assistant message — as the loop folds it — must
	// replay the blob under "rs_123". Asserting this through assistantItems (the
	// real replay path) rather than the Message field directly keeps the test on
	// the behaviour that matters: what goes on the wire. Under the pre-fix code
	// the blob threaded through but the id was lost, serialising `"id":""`.
	msg := aggregateAssistantMessage(chunks)
	if msg.Text != "The answer is 42." {
		t.Errorf("Message.Text = %q, want %q", msg.Text, "The answer is 42.")
	}
	var replayed []reasoningItem
	for _, it := range assistantItems(msg) {
		if it.OfReasoning != nil {
			replayed = append(replayed, reasoningItem{ID: it.OfReasoning.ID, Blob: it.OfReasoning.EncryptedContent.Value})
		}
	}
	want := []reasoningItem{{ID: "rs_123", Blob: "ENCRYPTED_RS_123"}}
	if !reflect.DeepEqual(replayed, want) {
		t.Errorf("replayed reasoning items = %+v, want %+v (blob replayed under the reasoning item's own id)", replayed, want)
	}
}

// aggregateAssistantMessage folds only the provider DTO seam. Canonical domain
// acceptance belongs to engine tests, not the Responses adapter.
func aggregateAssistantMessage(chunks []port.Chunk) port.LLMMessage {
	msg := port.LLMMessage{Role: session.RoleAssistant}
	for _, c := range chunks {
		switch c.Kind {
		case port.ChunkText:
			msg.Text += c.Text
		case port.ChunkPhase:
			msg.ProviderPhase = c.Text
		case port.ChunkReplay:
			msg.Replay = c.Replay
		case port.ChunkToolCall:
			if c.ToolCall != nil {
				msg.ToolCalls = append(msg.ToolCalls, *c.ToolCall)
			}
		}
	}
	return msg
}
