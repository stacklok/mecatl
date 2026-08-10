package openai

import "encoding/json"

// reasoningEnvelopeVersion is the schema version of the packed reasoning blob.
// It lets the envelope evolve without misreading an older blob round-tripped
// from a stored session.
const reasoningEnvelopeVersion = 1

// reasoningEnvelope is the JSON shape packed INTO the opaque
// session.Message.Reasoning string. The Responses API's reasoning-replay unit is
// a LIST of reasoning items, each pairing its own opaque encrypted_content with
// the reasoning-item id ("rs_…") that content is cryptographically bound to; a
// turn that interleaves reasoning with tool calls routinely emits several. A
// single opaque string cannot hold a list directly, so the adapter serializes the
// ordered list as this envelope. The domain value object stays a bare string —
// only this adapter knows the blob is a JSON list.
//
// Concatenating the blobs and keeping one id (the shape this replaced) sent
// content bound to earlier ids under the final id, which the provider rejects:
//
//	invalid_encrypted_content: … Encrypted content item_id did not match the
//	target item id.
//
// Because replay resends the whole history, one such message poisoned every
// later turn of the session. Pairing each blob with its own id by construction
// makes that unrepresentable.
//
// Order in Items is the SSE arrival order = the model's original emission order.
// A turn INTERLEAVES reasoning with tool calls (rs_a, fc_1, rs_b, fc_2, rs_c),
// and the API's stateless-replay rule is to pass the prior output items back
// untouched — so position relative to the function calls has to survive too, not
// just the sequence among the reasoning items. session.Message has no field that
// orders a reasoning blob against a tool call, so each entry records how many
// function calls preceded it (After) and assistantItems weaves the two lists back
// together from that.
//
// This mirrors provider/anthropic's reasoningEnvelope, which packs that API's
// own multi-unit reasoning list into the same single domain string. Structuring
// the blob is the adapter's business: "provider-neutral" means the ENGINE never
// interprets it, not that the adapter may not give it a shape.
type reasoningEnvelope struct {
	V     int             `json:"v"`
	Items []reasoningItem `json:"items"`
}

// reasoningItem is one entry in the envelope: the provider's reasoning-item id
// and the encrypted_content blob bound to it. Both are opaque — never parsed,
// logged, or displayed, only carried back verbatim.
//
// After is the number of function-call items the turn had already emitted when
// this reasoning item arrived, i.e. its slot in session.Message.ToolCalls: 0 means
// "before the first call", 1 means "between call 0 and call 1", and a value >=
// len(ToolCalls) means "after the last call". Zero is the correct default for
// every blob written before this field existed — all reasoning first, then the
// calls, which is exactly what those turns replayed as.
type reasoningItem struct {
	ID    string `json:"i"`
	Blob  string `json:"e"`
	After int    `json:"n,omitempty"`
}

// packReasoningItems serializes the ordered reasoning items into the opaque
// envelope string stored on session.Message.Reasoning. An empty list packs to ""
// (replay no-op), so a turn with no reasoning stores no reasoning. The marshal
// cannot fail for these scalar fields.
func packReasoningItems(items []reasoningItem) string {
	if len(items) == 0 {
		return ""
	}
	b, err := json.Marshal(reasoningEnvelope{V: reasoningEnvelopeVersion, Items: items})
	if err != nil {
		// Defensive: scalar string fields never fail to marshal. Fail soft to a
		// replay no-op rather than panic.
		return ""
	}
	return string(b)
}

// unpackReasoningItems parses the opaque blob stored on session.Message.Reasoning
// back into the ordered reasoning items to replay.
//
// It is FAIL-SOFT in the LEGACY direction, which is the whole point: a blob
// written before this adapter packed anything is a bare encrypted_content string
// (base64-ish ciphertext, never valid envelope JSON), so anything that does not
// parse as a current-version envelope is treated as exactly one item carrying
// that blob under legacyID — the caller's session.Message.ReasoningItemID. That
// reproduces the pre-packing behaviour byte-for-byte, so sessions already on disk
// replay exactly as they did before, including the single-item case that was
// always correct.
//
// An empty blob yields no items (replay no-op). An item missing either half is
// dropped: an id-less item serialises as `"id":""`, which strict gateways reject
// outright, so the D1a degrade applies — lose that item's reasoning continuity,
// never 400. It never panics on arbitrary input (fuzzed).
func unpackReasoningItems(blob, legacyID string) []reasoningItem {
	if blob == "" {
		return nil
	}
	var env reasoningEnvelope
	if err := json.Unmarshal([]byte(blob), &env); err != nil || env.V != reasoningEnvelopeVersion {
		return legacyReasoningItems(blob, legacyID)
	}
	out := make([]reasoningItem, 0, len(env.Items))
	for _, it := range env.Items {
		if it.ID == "" || it.Blob == "" {
			continue
		}
		out = append(out, it)
	}
	if len(out) == 0 {
		// A well-formed envelope carrying nothing replayable is NOT a legacy blob —
		// falling back here would resend the envelope JSON itself as ciphertext.
		return nil
	}
	return out
}

// legacyReasoningItems renders a pre-packing blob — a bare encrypted_content
// string paired with the message's own ReasoningItemID — as the single-item list
// the current replay path consumes. An id-less legacy blob yields nothing (the
// D1a degrade: dropping the item beats emitting `"id":""`).
func legacyReasoningItems(blob, legacyID string) []reasoningItem {
	if legacyID == "" {
		return nil
	}
	return []reasoningItem{{ID: legacyID, Blob: blob}}
}
