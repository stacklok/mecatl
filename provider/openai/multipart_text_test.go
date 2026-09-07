package openai

import (
	"encoding/json"
	"testing"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

func TestADR_0302_DistinctIdentitiesEmitOrderedChunkText(t *testing.T) {
	got := decodeFixture(t, "distinct_text_identities_turn.sse")
	want := []port.Chunk{
		{Kind: port.ChunkText, Text: "item"},
		{Kind: port.ChunkText, Text: "-output"},
		{Kind: port.ChunkText, Text: "-content"},
		{Kind: port.ChunkText, Text: "-ordered"},
		{Kind: port.ChunkUsage, Usage: &session.Usage{InputTokens: 7, OutputTokens: 4, CacheReadTokens: 2}},
		{Kind: port.ChunkDone, Stop: session.StopEndTurn},
	}
	assertChunks(t, got, want)
}

func TestADR_0302_InterleavedPhaseReasoningAndToolCallPreserveChunkSemantics(t *testing.T) {
	got := decodeFixture(t, "interleaved_multipart_turn.sse")
	if len(got) != 8 {
		t.Fatalf("got %d chunks, want 8: %+v", len(got), got)
	}
	wantKinds := []port.ChunkKind{
		port.ChunkReasoning,
		port.ChunkText,
		port.ChunkToolCall,
		port.ChunkText,
		port.ChunkPhase,
		port.ChunkReasoningItem,
		port.ChunkUsage,
		port.ChunkDone,
	}
	for i, want := range wantKinds {
		if got[i].Kind != want {
			t.Fatalf("chunk %d kind = %v, want %v; chunks=%+v", i, got[i].Kind, want, got)
		}
	}
	if got[0].Text != "inspect" || got[1].Text != "first" || got[3].Text != "second" {
		t.Fatalf("reasoning/text projection changed: %+v", got)
	}
	call := got[2].ToolCall
	if call == nil || call.ID != "call_1" || call.ItemID != "fc_1" || call.Name != "Read" || string(call.Args) != `{"path":"a.go"}` {
		t.Fatalf("tool call = %+v, want assembled fc_1/call_1 Read", call)
	}
	if got[4].Text != "future_phase" {
		t.Fatalf("phase = %q, want opaque future_phase", got[4].Text)
	}
	items := unpackReasoningItems(got[5].Text, got[5].ReasoningItemID)
	if len(items) != 1 || items[0].ID != "rs_1" || items[0].Blob != "OPAQUE_REASONING" || items[0].After != 0 {
		t.Fatalf("reasoning replay = %q (%+v), want rs_1 opaque item before call", got[5].Text, items)
	}
	wantUsage := session.Usage{InputTokens: 13, OutputTokens: 8, CacheReadTokens: 3, ReasoningTokens: 2}
	if got[6].Usage == nil || *got[6].Usage != wantUsage {
		t.Fatalf("usage = %+v, want %+v", got[6].Usage, wantUsage)
	}
	if got[7].Stop != session.StopEndTurn {
		t.Fatalf("stop = %q, want %q", got[7].Stop, session.StopEndTurn)
	}
	if !json.Valid(call.Args) {
		t.Fatalf("tool args are not valid JSON: %q", call.Args)
	}
}
