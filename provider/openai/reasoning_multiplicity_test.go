package openai

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/openai/openai-go/v3/responses"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

// TestMultipleReasoningItemsReplayUnderTheirOwnIDs is the cold reproducer for
// the invalid_encrypted_content poisoning.
//
// A turn that interleaves reasoning with tool calls emits SEVERAL reasoning
// items, each carrying encrypted_content cryptographically bound to that item's
// own id. The pre-fix adapter emitted one chunk per item and the loop folded
// them by CONCATENATING the blobs while keeping only the LAST id — so replay
// sent three blobs' worth of ciphertext under rs_ccc alone, and the provider
// refused it:
//
//	invalid_encrypted_content: The encrypted content for item rs_… could not be
//	verified. Reason: Encrypted content item_id did not match the target item id.
//
// Because replay resends the whole history, that one message failed every
// subsequent turn of the session — the session went `failed` and stayed there.
//
// This test drives the recorded fixture through the real translate path and
// asserts the COMPLETE wire sequence, because two distinct corruptions live here
// and asserting reasoning and calls separately would hide the second:
//
//   - each blob must ride its OWN id (the fused-blob bug above); and
//   - the items must go back in the model's ORIGINAL interleaved order. The
//     fixture records rs_aaa → fc_1 → rs_bbb → fc_2 → rs_ccc, and the API's
//     stateless-replay rule is to pass prior output items back untouched.
//     Grouping all reasoning ahead of all calls — which is what this emitted when
//     a turn could only have one reasoning item — rewrites the trajectory.
func TestMultipleReasoningItemsReplayUnderTheirOwnIDs(t *testing.T) {
	msg := aggregateAssistantMessage(decodeFixture(t, "multi_reasoning_item_turn.sse"))

	want := []string{
		"reasoning rs_aaa ENCRYPTED_AAA",
		"function_call fc_1 call_one",
		"reasoning rs_bbb ENCRYPTED_BBB",
		"function_call fc_2 call_two",
		"reasoning rs_ccc ENCRYPTED_CCC",
	}
	if got := describeItems(assistantItems(msg)); !reflect.DeepEqual(got, want) {
		t.Errorf("replayed wire items =\n  %s\nwant\n  %s",
			strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
}

// describeItems renders the replayed input items as one comparable line each, so
// a failure shows the whole sequence rather than a struct diff. A reasoning item
// prints its id and blob together — the pairing under test.
func describeItems(items []responses.ResponseInputItemUnionParam) []string {
	out := make([]string, 0, len(items))
	for _, it := range items {
		switch {
		case it.OfReasoning != nil:
			out = append(out, "reasoning "+it.OfReasoning.ID+" "+it.OfReasoning.EncryptedContent.Value)
		case it.OfFunctionCall != nil:
			out = append(out, "function_call "+it.OfFunctionCall.ID.Value+" "+it.OfFunctionCall.CallID)
		case it.OfMessage != nil:
			out = append(out, "message "+it.OfMessage.Content.OfString.Value)
		default:
			out = append(out, "other")
		}
	}
	return out
}

// TestReasoningInterleavingPlacement walks the placements the After cursor has to
// get right, including the ones the fixture does not reach: reasoning trailing the
// last call, a turn with no calls at all, and a stored After that overruns the
// calls a message actually has (a truncated or hand-edited snapshot). Nothing may
// be dropped in any of them — a silently swallowed reasoning item is the failure
// mode the whole envelope exists to prevent.
func TestReasoningInterleavingPlacement(t *testing.T) {
	calls := []session.ToolCall{
		{ID: "c0", ItemID: "fc_0", Name: "Read", Args: json.RawMessage(`{}`)},
		{ID: "c1", ItemID: "fc_1", Name: "Read", Args: json.RawMessage(`{}`)},
	}
	tests := []struct {
		name  string
		items []reasoningItem
		calls []session.ToolCall
		want  []string
	}{
		{
			name:  "all reasoning before the first call (the legacy shape)",
			items: []reasoningItem{{ID: "r1", Blob: "A"}, {ID: "r2", Blob: "B"}},
			calls: calls,
			want: []string{"reasoning r1 A", "reasoning r2 B",
				"function_call fc_0 c0", "function_call fc_1 c1"},
		},
		{
			name:  "reasoning trailing the last call",
			items: []reasoningItem{{ID: "r1", Blob: "A", After: 2}},
			calls: calls,
			want:  []string{"function_call fc_0 c0", "function_call fc_1 c1", "reasoning r1 A"},
		},
		{
			name:  "two reasoning items in the same slot keep their order",
			items: []reasoningItem{{ID: "r1", Blob: "A", After: 1}, {ID: "r2", Blob: "B", After: 1}},
			calls: calls,
			want: []string{"function_call fc_0 c0", "reasoning r1 A", "reasoning r2 B",
				"function_call fc_1 c1"},
		},
		{
			name:  "no tool calls at all",
			items: []reasoningItem{{ID: "r1", Blob: "A"}, {ID: "r2", Blob: "B", After: 3}},
			calls: nil,
			want:  []string{"reasoning r1 A", "reasoning r2 B"},
		},
		{
			name:  "After overruns the calls present: emitted last, never dropped",
			items: []reasoningItem{{ID: "r1", Blob: "A", After: 99}},
			calls: calls,
			want:  []string{"function_call fc_0 c0", "function_call fc_1 c1", "reasoning r1 A"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			msg := session.NewAssistantMessage("", packReasoningItems(tc.items), tc.calls)
			if got := describeItems(assistantItems(msg)); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("wire items =\n  %s\nwant\n  %s",
					strings.Join(got, "\n  "), strings.Join(tc.want, "\n  "))
			}
		})
	}
}

// TestLegacyReasoningBlobReplaysUnchanged pins the migration contract: a session
// recorded BEFORE the adapter packed anything stores a bare encrypted_content
// string on Message.Reasoning plus its id on Message.ReasoningItemID. Those
// sessions are on disk today and must keep replaying exactly as they did — one
// reasoning item, same id, same blob, byte for byte.
//
// The id-less case is the D1a degrade: replaying it would serialise `"id":""`,
// which strict gateways reject outright, so the item is dropped instead (lose
// that turn's reasoning continuity, never 400).
func TestLegacyReasoningBlobReplaysUnchanged(t *testing.T) {
	tests := []struct {
		name      string
		reasoning string
		itemID    string
		want      []reasoningItem
	}{
		{
			name:      "legacy blob with id replays as one item",
			reasoning: "LEGACY_ENCRYPTED",
			itemID:    "rs_legacy",
			want:      []reasoningItem{{ID: "rs_legacy", Blob: "LEGACY_ENCRYPTED"}},
		},
		{
			name:      "legacy blob without id is dropped (D1a)",
			reasoning: "LEGACY_ENCRYPTED",
			itemID:    "",
			want:      nil,
		},
		{
			name:      "no reasoning at all is a replay no-op",
			reasoning: "",
			itemID:    "rs_orphan",
			want:      nil,
		},
		{
			name:      "packed envelope ignores the vestigial legacy id",
			reasoning: packReasoningItems([]reasoningItem{{ID: "rs_1", Blob: "A"}, {ID: "rs_2", Blob: "B"}}),
			itemID:    "rs_stale",
			want:      []reasoningItem{{ID: "rs_1", Blob: "A"}, {ID: "rs_2", Blob: "B"}},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			msg := session.NewAssistantMessage("", tc.reasoning, nil)
			msg.ReasoningItemID = tc.itemID

			var replayed []reasoningItem
			for _, it := range assistantItems(msg) {
				if it.OfReasoning != nil {
					replayed = append(replayed, reasoningItem{ID: it.OfReasoning.ID, Blob: it.OfReasoning.EncryptedContent.Value})
				}
			}
			if !reflect.DeepEqual(replayed, tc.want) {
				t.Errorf("replayed = %+v, want %+v", replayed, tc.want)
			}
		})
	}
}

// TestPackedReasoningNeverEmitsAnIDLessItem is the invariant that keeps the
// D1a degrade honest through the packing layer: whatever an envelope contains,
// nothing missing an id or a blob may reach the wire, because the SDK
// serialises an unset id as `"id":""` and strict gateways 400 on it.
func TestPackedReasoningNeverEmitsAnIDLessItem(t *testing.T) {
	packed := packReasoningItems([]reasoningItem{
		{ID: "rs_ok", Blob: "GOOD"},
		{ID: "", Blob: "ORPHAN_BLOB"},
		{ID: "rs_empty", Blob: ""},
	})
	msg := session.NewAssistantMessage("", packed, nil)

	for _, it := range assistantItems(msg) {
		if it.OfReasoning == nil {
			continue
		}
		if it.OfReasoning.ID == "" {
			t.Error(`replayed a reasoning item with id "" — strict gateways reject it; drop the item instead`)
		}
		if it.OfReasoning.EncryptedContent.Value == "" {
			t.Error("replayed a reasoning item with no encrypted content — it carries nothing to verify")
		}
	}
}

// TestReasoningItemsSurviveAPackRoundTrip pins the property the whole fix rests
// on: the (id, blob) pairing is preserved through storage. The envelope is what
// gets written to a session snapshot and read back a process later, so a pairing
// that survives pack→unpack survives a restart.
func TestReasoningItemsSurviveAPackRoundTrip(t *testing.T) {
	want := []reasoningItem{
		{ID: "rs_1", Blob: "AAA"},
		{ID: "rs_2", Blob: "BBB"},
		{ID: "rs_3", Blob: `{"looks":"like json"}`},
	}
	if got := unpackReasoningItems(packReasoningItems(want), ""); !reflect.DeepEqual(got, want) {
		t.Errorf("round trip = %+v, want %+v", got, want)
	}
	if got := packReasoningItems(nil); got != "" {
		t.Errorf("packing no items = %q, want \"\" (a replay no-op)", got)
	}
}

// FuzzUnpackReasoningItems feeds arbitrary bytes to the unpacker, which parses a
// blob read straight back off disk (or produced by an older/newer harness).
// It must never panic, and it must never invent an unreplayable item — every
// item it returns carries both halves, because the caller replays whatever it
// hands back.
func FuzzUnpackReasoningItems(f *testing.F) {
	f.Add("", "")
	f.Add("LEGACY_OPAQUE_BLOB", "rs_legacy")
	f.Add(packReasoningItems([]reasoningItem{{ID: "rs_1", Blob: "A"}}), "")
	f.Add(packReasoningItems([]reasoningItem{{ID: "rs_1", Blob: "A"}, {ID: "rs_2", Blob: "B"}}), "rs_stale")
	f.Add(`{"v":1,"items":[]}`, "rs_x")
	f.Add(`{"v":2,"items":[{"i":"rs_1","e":"A"}]}`, "rs_x")
	f.Add(`{"v":1,"items":[{"i":"","e":"A"}]}`, "rs_x")
	f.Add(`{"v":1,"items":null}`, "")
	f.Add("{not json", "rs_x")
	f.Add("\x00\x00", "\x00")

	f.Fuzz(func(t *testing.T, blob, legacyID string) {
		for _, it := range unpackReasoningItems(blob, legacyID) {
			if it.ID == "" || it.Blob == "" {
				t.Fatalf("unpacked an unreplayable item %+v from blob %q, legacyID %q", it, blob, legacyID)
			}
		}
	})
}

// TestRecoveryRemovesEveryPackedReasoningItem is the seam between this fix's two
// halves: the encrypted-content repair (Stream's one-shot fallback) must
// understand the PACKED envelope, not just the legacy single (blob, id) pair.
//
// It matters because the two halves disagree by default. The repair decides
// "this message carried a reasoning envelope" and strips it; a check written
// against the legacy fields (`Reasoning != "" && ReasoningItemID != ""`) sees a
// packed message as carrying nothing — `ReasoningItemID` is empty once the ids
// move inside the envelope — so the repair would never fire for a
// freshly-produced session, exactly the ones this fix creates. Deciding through
// unpackReasoningItems, the same call the wire projection makes, keeps them in
// step: N items in, N items gone, nothing else touched.
func TestRecoveryRemovesEveryPackedReasoningItem(t *testing.T) {
	var bodies [][]byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		bodies = append(bodies, body)
		if len(bodies) == 1 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"code":"invalid_encrypted_content",` +
				`"message":"The encrypted content for item rs_2 could not be verified."}}`))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`data: {"type":"response.output_text.delta","sequence_number":0,"delta":"recovered"}` + "\n\n" +
			`data: {"type":"response.completed","sequence_number":1,` +
			`"response":{"status":"completed","usage":{"input_tokens":1,"output_tokens":1}}}` + "\n\n"))
	}))
	defer srv.Close()

	assistant := session.NewAssistantMessage("visible", packReasoningItems([]reasoningItem{
		{ID: "rs_1", Blob: "AAA"}, {ID: "rs_2", Blob: "BBB"}, {ID: "rs_3", Blob: "CCC"},
	}), []session.ToolCall{{ID: "call_1", ItemID: "fc_keep", Name: "Read", Args: json.RawMessage(`{}`)}})
	assistant.ProviderPhase = "commentary"

	p := New(WithAPIKey("test-key"), WithBaseURL(srv.URL+"/v1"))
	seq, err := p.Stream(context.Background(), port.LLMRequest{
		Model:    "gpt-5.6-terra",
		Messages: []session.Message{session.NewUserMessage("go"), assistant},
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	for _, streamErr := range seq {
		if streamErr != nil {
			t.Fatalf("recovery stream: %v", streamErr)
		}
	}
	if len(bodies) != 2 {
		t.Fatalf("request count = %d, want exactly 2 (rejected, then repaired)", len(bodies))
	}

	var first, second struct {
		Input []map[string]any `json:"input"`
	}
	if err := json.Unmarshal(bodies[0], &first); err != nil {
		t.Fatalf("decode first request: %v", err)
	}
	if err := json.Unmarshal(bodies[1], &second); err != nil {
		t.Fatalf("decode repaired request: %v", err)
	}
	if got := countInputType(first.Input, "reasoning"); got != 3 {
		t.Fatalf("first request reasoning items = %d, want 3 (one per packed entry)", got)
	}
	if got := countInputType(second.Input, "reasoning"); got != 0 {
		t.Errorf("repaired request reasoning items = %d, want 0 — every packed entry must be removed, not just one", got)
	}
	// Only the reasoning envelope goes. Dropping the phase marker or the
	// function-call item id would trade this rejection for two other known
	// failures (GPT-5.x stopping early on a phase-less preamble; "Duplicate item
	// found with id fc_N").
	repaired := string(bodies[1])
	for _, keep := range []string{"fc_keep", "commentary", "visible"} {
		if !strings.Contains(repaired, keep) {
			t.Errorf("repaired request dropped %q; only the reasoning envelope may be removed", keep)
		}
	}
}

// TestReasoningItemsDroppedOnNonTerminalStream pins the flush boundary: the
// buffered items ride out at response.completed, so a stream that never gets
// there emits no replay blob. That costs nothing — the engine discards the whole
// assistant message on those paths — but it must not leak a partial envelope.
func TestReasoningItemsDroppedOnNonTerminalStream(t *testing.T) {
	chunks, err := decodeFixtureErr(t, "response_failed.sse")
	if err == nil {
		t.Fatal("expected the failed-response fixture to surface an error")
	}
	for _, c := range chunks {
		if c.Kind == port.ChunkReasoningItem {
			t.Errorf("failed stream emitted a reasoning replay blob %q; it should be dropped", c.Text)
		}
	}
}
