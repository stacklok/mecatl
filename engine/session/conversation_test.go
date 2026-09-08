package session

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// TestValidateToolPairing pins the bidirectional pairing contract: a clean,
// fully-paired history passes; a history that opens on an orphaned tool result
// fails; a history with a dangling assistant tool call (no following result)
// fails; an empty history is trivially valid.
func TestValidateToolPairing(t *testing.T) {
	call := func(id ToolCallID) Message {
		return NewAssistantMessage("", "", []ToolCall{NewToolCall(id, "Read", json.RawMessage(`{}`))})
	}
	multiCall := func(ids ...ToolCallID) Message {
		calls := make([]ToolCall, 0, len(ids))
		for _, id := range ids {
			calls = append(calls, NewToolCall(id, "Read", json.RawMessage(`{}`)))
		}
		return NewAssistantMessage("", "", calls)
	}
	result := func(id ToolCallID) Message { return NewToolMessage(NewToolResult(id, "ok")) }

	tests := []struct {
		name    string
		msgs    []Message
		wantErr bool
	}{
		{
			name:    "empty passes",
			msgs:    nil,
			wantErr: false,
		},
		{
			name: "clean paired history passes",
			msgs: []Message{
				NewSystemMessage("sys"),
				NewUserMessage("goal"),
				call("c1"),
				result("c1"),
				NewAssistantMessage("done", "", nil),
			},
			wantErr: false,
		},
		{
			name: "leading orphan tool result fails",
			msgs: []Message{
				NewUserMessage("goal"),
				result("c1"), // no preceding assistant call for c1
			},
			wantErr: true,
		},
		{
			name: "dangling tool call fails",
			msgs: []Message{
				NewUserMessage("goal"),
				call("c1"), // no following result for c1
			},
			wantErr: true,
		},
		{
			name: "result before its call fails (order matters)",
			msgs: []Message{
				result("c1"),
				call("c1"),
			},
			wantErr: true,
		},
		{
			name: "tool message with nil result fails",
			msgs: []Message{
				{Role: RoleTool},
			},
			wantErr: true,
		},
		{
			name: "multi-call assistant, all answered, passes",
			msgs: []Message{
				NewUserMessage("goal"),
				multiCall("c1", "c2", "c3"),
				result("c1"),
				result("c2"),
				result("c3"),
			},
			wantErr: false,
		},
		{
			name: "multi-call assistant, one unanswered, fails",
			msgs: []Message{
				NewUserMessage("goal"),
				multiCall("c1", "c2", "c3"),
				result("c1"),
				result("c3"), // c2 dangles
			},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateToolPairing(tt.msgs)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ValidateToolPairing err = %v, wantErr = %v", err, tt.wantErr)
			}
		})
	}

	// The dangling-ID report is deterministic: with multiple unanswered calls it
	// names the lexicographically-smallest id (sorted), so the message is stable.
	t.Run("dangling report is deterministic", func(t *testing.T) {
		err := ValidateToolPairing([]Message{multiCall("c9", "c2", "c5")})
		if err == nil {
			t.Fatalf("expected error for all-dangling multi-call")
		}
		if !strings.Contains(err.Error(), `"c2"`) {
			t.Fatalf("dangling report = %q, want it to name the smallest id c2", err.Error())
		}
	})
}

// forkCall builds an assistant message carrying a single (unanswered) tool call —
// the shape of a parent's trailing message at fork-dispatch time.
func forkCall(id ToolCallID) Message {
	return NewAssistantMessage("", "", []ToolCall{NewToolCall(id, "Subagent", json.RawMessage(`{}`))})
}

// TestForkSnapshotStripsTrailingForkCall proves ForkSnapshot drops the trailing
// UNANSWERED tool call (the fork-call itself) so the result is tool-pairing-valid,
// while preserving every prior fully-paired turn. It also covers the no-orphan
// case (a clean conversation is returned unchanged) and the nil receiver.
func TestForkSnapshotStripsTrailingForkCall(t *testing.T) {
	result := func(id ToolCallID) Message { return NewToolMessage(NewToolResult(id, "ok")) }

	conv := &Conversation{Messages: []Message{
		NewUserMessage("investigate the SENTINEL"),
		NewAssistantMessage("looking", "", []ToolCall{NewToolCall("c1", "Read", json.RawMessage(`{}`))}),
		result("c1"),
		NewAssistantMessage("here is what I found", "", nil),
		forkCall("fork1"), // trailing unanswered fork call — must be stripped
	}}

	snap := ForkSnapshot(conv)
	if err := ValidateToolPairing(snap); err != nil {
		t.Fatalf("ForkSnapshot result is not pairing-valid: %v", err)
	}
	// Prior turns preserved: the SENTINEL user message and the c1 pair survive.
	var sawSentinel, sawC1Call, sawC1Result, sawForkCall bool
	for _, m := range snap {
		if m.Role == RoleUser && strings.Contains(m.Text, "SENTINEL") {
			sawSentinel = true
		}
		for _, c := range m.ToolCalls {
			if c.ID == "c1" {
				sawC1Call = true
			}
			if c.ID == "fork1" {
				sawForkCall = true
			}
		}
		if m.ToolResult != nil && m.ToolResult.CallID == "c1" {
			sawC1Result = true
		}
	}
	if !sawSentinel || !sawC1Call || !sawC1Result {
		t.Fatalf("prior turns not preserved: sentinel=%v c1call=%v c1result=%v", sawSentinel, sawC1Call, sawC1Result)
	}
	if sawForkCall {
		t.Fatalf("trailing fork call was NOT stripped")
	}

	// No trailing orphan: a clean conversation is returned cloned-but-unchanged.
	clean := &Conversation{Messages: []Message{
		NewUserMessage("goal"),
		NewAssistantMessage("done", "", nil),
	}}
	cleanSnap := ForkSnapshot(clean)
	if len(cleanSnap) != 2 {
		t.Fatalf("clean snapshot len = %d, want 2 (unchanged)", len(cleanSnap))
	}
	if err := ValidateToolPairing(cleanSnap); err != nil {
		t.Fatalf("clean snapshot not pairing-valid: %v", err)
	}

	// Nil receiver is safe.
	if ForkSnapshot(nil) != nil {
		t.Fatalf("ForkSnapshot(nil) should return nil")
	}

	// Empty (turn-0) conversation yields an empty, trivially-valid snapshot.
	if got := ForkSnapshot(&Conversation{}); len(got) != 0 {
		t.Fatalf("empty conversation snapshot len = %d, want 0", len(got))
	}
}

// TestForkDeepCopyIsIndependent proves CloneMessages (and thus ForkSnapshot) gives
// the child a FRESH backing array: appending to the copy never mutates the parent
// conversation — neither its length nor its content.
func TestForkDeepCopyIsIndependent(t *testing.T) {
	parent := &Conversation{Messages: []Message{
		NewUserMessage("parent turn one"),
		NewAssistantMessage("parent answer one", "", nil),
	}}
	parentLenBefore := len(parent.Messages)

	child := CloneMessages(parent.Messages)
	// Append several messages to the child copy.
	child = append(child, NewUserMessage("child only one"))
	child = append(child, NewAssistantMessage("child only two", "", nil))

	if len(parent.Messages) != parentLenBefore {
		t.Fatalf("parent length grew to %d (was %d) — copy aliased the backing array",
			len(parent.Messages), parentLenBefore)
	}
	for _, m := range parent.Messages {
		if strings.Contains(m.Text, "child only") {
			t.Fatalf("parent message was overwritten by a child append: %q", m.Text)
		}
	}
	// And the child genuinely carries both the inherited and the new messages.
	if len(child) != parentLenBefore+2 {
		t.Fatalf("child length = %d, want %d", len(child), parentLenBefore+2)
	}
}

// TestForkSnapshotIsSliceNotClosure proves the design's load-bearing claim that a
// fork seeds from the SLICE taken at ForkSnapshot time, NOT a live view of the
// parent conversation: a parent append AFTER the snapshot is taken must NOT appear
// in the captured slice. (Chosen at the SESSION level: the slice-independence is a
// property of ForkSnapshot/CloneMessages itself — the cheapest, most direct place
// to pin it. The agent loop's synchronous-capture-before-detach discipline that
// USES this property is separately guarded end-to-end by
// TestSubagentForkBackgroundChildSeesParentHistory, whose mutation-verify shows a
// post-detach live read is a data race.)
func TestForkSnapshotIsSliceNotClosure(t *testing.T) {
	parent := &Conversation{Messages: []Message{
		NewUserMessage("investigate the SENTINEL"),
		NewAssistantMessage("here is what I found", "", nil),
	}}

	snap := ForkSnapshot(parent)
	snapLen := len(snap)

	// The parent KEEPS GOING after the snapshot was taken (exactly what a detached
	// background fork's parent does): several new turns are appended.
	parent.Append(NewUserMessage("LATE_APPEND_after_fork one"))
	parent.Append(NewAssistantMessage("LATE_APPEND_after_fork two", "", nil))

	if len(snap) != snapLen {
		t.Fatalf("snapshot length changed from %d to %d after a parent append — it is a live view, not a slice", snapLen, len(snap))
	}
	for _, m := range snap {
		if strings.Contains(m.Text, "LATE_APPEND") {
			t.Fatalf("a parent append AFTER the snapshot leaked into the captured slice: %q", m.Text)
		}
	}
	// And the pre-fork sentinel the snapshot DID capture is still there (the slice is
	// the frozen prefix, not empty).
	var sawSentinel bool
	for _, m := range snap {
		if strings.Contains(m.Text, "SENTINEL") {
			sawSentinel = true
		}
	}
	if !sawSentinel {
		t.Fatalf("snapshot lost the pre-fork sentinel it should have frozen: %+v", snap)
	}
}

// TestForkMidToolCallRepairedNotOrphaned is the adversarial case: a parent captured
// MID-TURN — its trailing assistant message carries the unanswered fork call AND a
// prior unanswered call — must produce a snapshot that SeedHistory accepts (the
// orphan-strip repairs it), never a dangling tool_use. It composes ForkSnapshot
// with SeedHistory exactly as the fork child build does.
func TestForkMidToolCallRepairedNotOrphaned(t *testing.T) {
	conv := &Conversation{Messages: []Message{
		NewUserMessage("investigate"),
		// A trailing assistant turn carrying a parallel batch: a sibling read call
		// ("other") that IS answered (its result follows) alongside the unanswered
		// fork call. The strip is WHOLE-TRAILING-TURN (msgs[:lastAssistant]), so it
		// drops the entire assistant message — both the fork call AND the answered
		// sibling's call — plus the trailing tool results that belong to that turn.
		NewAssistantMessage("delegating", "", []ToolCall{
			NewToolCall("other", "Read", json.RawMessage(`{}`)),
			NewToolCall("fork1", "Subagent", json.RawMessage(`{}`)),
		}),
		// The sibling's answer (the fork call's result is recorded only AFTER dispatch,
		// so it is absent here — that is the trailing orphan being repaired).
		NewToolMessage(NewToolResult("other", "sibling read result")),
	}}

	snap := ForkSnapshot(conv)
	if err := ValidateToolPairing(snap); err != nil {
		t.Fatalf("mid-tool-call snapshot not pairing-valid: %v", err)
	}
	// WHOLE-TURN-STRIP semantics (pin against a future "strip only the fork call"
	// refactor): the answered sibling's call AND its result must BOTH be gone from
	// the snapshot. Leaving the sibling's call while dropping the assistant turn — or
	// keeping the sibling's result while dropping its call — would orphan a pair. The
	// only pairing-safe repair of a mid-turn capture is dropping the whole turn.
	for _, m := range snap {
		for _, c := range m.ToolCalls {
			if c.ID == "other" || c.ID == "fork1" {
				t.Fatalf("whole-turn strip must drop the sibling call %q too, not just the fork call", c.ID)
			}
		}
		if m.ToolResult != nil && m.ToolResult.CallID == "other" {
			t.Fatalf("the answered sibling's RESULT must also be stripped (its call is gone) — left it orphaned")
		}
	}
	// SeedHistory (idle) must accept it — the real child-build path.
	child := New("fork-child", ModeDefault, EnvironmentRef{Kind: EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"}, Limits{}, time.Unix(0, 0))
	if err := child.SeedHistory(snap); err != nil {
		t.Fatalf("SeedHistory(mid-tool-call snapshot) rejected a repaired history: %v", err)
	}
}

// TestStripProviderState pins the CROSS-provider carryover contract: the four
// provider-private replay blobs (Message.Reasoning, Message.ReasoningItemID,
// Message.ProviderPhase, ToolCall.ItemID) are cleared, every provider-neutral
// field (Role, Text, ToolCall ID/Name/Args, ToolResult incl. its Parts,
// Message.Parts) survives verbatim, and the input — including the shared
// ToolCalls backing arrays — is NEVER mutated.
func TestStripProviderState(t *testing.T) {
	media, err := NewImageContent("image/png", []byte{0x89, 0x50})
	if err != nil {
		t.Fatalf("NewImageContent: %v", err)
	}

	tests := []struct {
		name string
		msgs []Message
	}{
		{
			name: "nil input passes through",
			msgs: nil,
		},
		{
			name: "empty input passes through",
			msgs: []Message{},
		},
		{
			name: "assistant blobs cleared, text/role/calls preserved",
			msgs: []Message{
				NewUserMessage("goal"),
				{
					Role:            RoleAssistant,
					Text:            "working on it",
					Reasoning:       "openai-encrypted-blob",
					ReasoningItemID: "rs_item_1",
					ProviderPhase:   "commentary",
					ToolCalls: []ToolCall{
						{ID: "c1", Name: "Read", Args: json.RawMessage(`{"path":"a.go"}`), ItemID: "fc_item_1"},
					},
				},
			},
		},
		{
			name: "every ToolCall ItemID cleared, ID/Name/Args preserved",
			msgs: []Message{
				{
					Role: RoleAssistant,
					ToolCalls: []ToolCall{
						{ID: "c1", Name: "Read", Args: json.RawMessage(`{"path":"a.go"}`), ItemID: "fc_item_1"},
						{ID: "c2", Name: "Grep", Args: json.RawMessage(`{"pattern":"foo"}`), ItemID: "fc_item_2"},
					},
				},
				NewToolMessage(NewToolResult("c1", "file body")),
				NewToolMessage(NewToolResult("c2", "3 matches")),
			},
		},
		{
			name: "tool results and parts untouched",
			msgs: []Message{
				NewUserMessageWithParts("look at this", []Content{media}),
				{
					Role:      RoleAssistant,
					Reasoning: "anthropic-thinking-signature",
					ToolCalls: []ToolCall{
						{ID: "c1", Name: "Read", Args: json.RawMessage(`{}`), ItemID: "fc_item_1"},
					},
				},
				NewToolMessage(NewToolResultWithParts("c1", "summary", []Content{NewTextBlock("block text")})),
			},
		},
		{
			name: "message with no blobs passes through",
			msgs: []Message{
				NewSystemMessage("sys"),
				NewUserMessage("plain question"),
				NewAssistantMessage("plain answer", "", nil),
			},
		},
		{
			name: "mixed blob-bearing and blob-free messages",
			msgs: []Message{
				NewUserMessage("goal"),
				{
					Role:          RoleAssistant,
					Text:          "blob turn",
					Reasoning:     "blob",
					ProviderPhase: "final_answer",
					ToolCalls: []ToolCall{
						{ID: "c1", Name: "Read", Args: json.RawMessage(`{}`), ItemID: "fc_item_1"},
					},
				},
				NewToolMessage(NewToolResult("c1", "ok")),
				NewAssistantMessage("blob-free turn", "", nil),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Snapshot the input's pre-strip blob state for the mutate-verify.
			type blobState struct {
				reasoning       string
				reasoningItemID string
				phase           string
				itemIDs         []string
			}
			before := make([]blobState, len(tt.msgs))
			for i, m := range tt.msgs {
				before[i].reasoning = m.Reasoning
				before[i].reasoningItemID = m.ReasoningItemID
				before[i].phase = m.ProviderPhase
				before[i].itemIDs = make([]string, len(m.ToolCalls))
				for j, c := range m.ToolCalls {
					before[i].itemIDs[j] = c.ItemID
				}
			}

			stripped := StripProviderState(tt.msgs)

			if len(stripped) != len(tt.msgs) {
				t.Fatalf("stripped len = %d, want %d", len(stripped), len(tt.msgs))
			}
			if tt.msgs == nil && stripped != nil {
				t.Fatalf("StripProviderState(nil) = %v, want nil", stripped)
			}

			for i, m := range stripped {
				if m.Reasoning != "" {
					t.Fatalf("msg %d: Reasoning = %q, want cleared", i, m.Reasoning)
				}
				if m.ReasoningItemID != "" {
					t.Fatalf("msg %d: ReasoningItemID = %q, want cleared", i, m.ReasoningItemID)
				}
				if m.ProviderPhase != "" {
					t.Fatalf("msg %d: ProviderPhase = %q, want cleared", i, m.ProviderPhase)
				}
				for j, c := range m.ToolCalls {
					if c.ItemID != "" {
						t.Fatalf("msg %d call %d: ItemID = %q, want cleared", i, j, c.ItemID)
					}
				}

				// Provider-neutral fields preserved verbatim.
				orig := tt.msgs[i]
				if m.Role != orig.Role || m.Text != orig.Text {
					t.Fatalf("msg %d: Role/Text drifted: got (%q,%q), want (%q,%q)",
						i, m.Role, m.Text, orig.Role, orig.Text)
				}
				if len(m.ToolCalls) != len(orig.ToolCalls) {
					t.Fatalf("msg %d: ToolCalls len = %d, want %d", i, len(m.ToolCalls), len(orig.ToolCalls))
				}
				for j, c := range m.ToolCalls {
					o := orig.ToolCalls[j]
					if c.ID != o.ID || c.Name != o.Name || string(c.Args) != string(o.Args) {
						t.Fatalf("msg %d call %d: neutral fields drifted: got (%q,%q,%s), want (%q,%q,%s)",
							i, j, c.ID, c.Name, c.Args, o.ID, o.Name, o.Args)
					}
				}
				// ToolResult is carried by pointer and StripProviderState never
				// rewrites it, so the SAME immutable pointer must survive.
				if m.ToolResult != orig.ToolResult {
					t.Fatalf("msg %d: ToolResult pointer drifted: got %+v, want %+v", i, m.ToolResult, orig.ToolResult)
				}
				if len(m.Parts) != len(orig.Parts) {
					t.Fatalf("msg %d: Parts len = %d, want %d", i, len(m.Parts), len(orig.Parts))
				}
			}

			// Stripping orphans nothing: a history that was pairing-valid on input
			// stays pairing-valid (call IDs are untouched). Rows that are
			// deliberately mid-conversation (a dangling trailing call) are excluded.
			if err := ValidateToolPairing(tt.msgs); err == nil {
				if err := ValidateToolPairing(stripped); err != nil {
					t.Fatalf("stripped history not pairing-valid (input was): %v", err)
				}
			}

			// MUTATE-VERIFY: the input slice AND its ToolCalls backing arrays are
			// untouched — every blob/ItemID the input carried is still there.
			for i, m := range tt.msgs {
				if m.Reasoning != before[i].reasoning || m.ProviderPhase != before[i].phase || m.ReasoningItemID != before[i].reasoningItemID {
					t.Fatalf("msg %d: input mutated: Reasoning/ReasoningItemID/ProviderPhase now (%q,%q,%q), were (%q,%q,%q)",
						i, m.Reasoning, m.ReasoningItemID, m.ProviderPhase, before[i].reasoning, before[i].reasoningItemID, before[i].phase)
				}
				for j, c := range m.ToolCalls {
					if c.ItemID != before[i].itemIDs[j] {
						t.Fatalf("msg %d call %d: input ToolCalls backing array mutated: ItemID now %q, was %q",
							i, j, c.ItemID, before[i].itemIDs[j])
					}
				}
			}
		})
	}
}

// TestStripProviderStateToolCallsBackingArrayNotAliased pins the load-bearing
// half of the no-mutation contract: when the input carries tool calls, the
// result's ToolCalls slice must be a COPY — zeroing ItemID on the result must
// never write through a shared backing array into the caller's history.
func TestStripProviderStateToolCallsBackingArrayNotAliased(t *testing.T) {
	orig := []Message{
		{
			Role: RoleAssistant,
			ToolCalls: []ToolCall{
				{ID: "c1", Name: "Read", Args: json.RawMessage(`{}`), ItemID: "fc_item_1"},
			},
		},
	}
	stripped := StripProviderState(orig)
	if stripped[0].ToolCalls[0].ItemID != "" {
		t.Fatalf("stripped ItemID = %q, want cleared", stripped[0].ToolCalls[0].ItemID)
	}
	if orig[0].ToolCalls[0].ItemID != "fc_item_1" {
		t.Fatalf("input ToolCalls backing array was mutated: ItemID = %q, want fc_item_1",
			orig[0].ToolCalls[0].ItemID)
	}
	// And the arrays really are distinct storage, not two headers on one array.
	stripped[0].ToolCalls[0].ItemID = "rewritten"
	if orig[0].ToolCalls[0].ItemID != "fc_item_1" {
		t.Fatalf("result aliases the input backing array: a write to the copy reached the original")
	}
}

// TestForkEmptyParentConversation proves a turn-0 fork (the parent has produced no
// real history yet, only the just-the-fork-call assistant turn) yields an empty,
// trivially-valid snapshot — the fork degrades to a fresh-context child, NOT an
// error.
func TestForkEmptyParentConversation(t *testing.T) {
	// A conversation consisting ONLY of the trailing (unanswered) fork call.
	conv := &Conversation{Messages: []Message{
		NewAssistantMessage("", "", []ToolCall{NewToolCall("fork1", "Subagent", json.RawMessage(`{}`))}),
	}}
	snap := ForkSnapshot(conv)
	if len(snap) != 0 {
		t.Fatalf("turn-0 fork snapshot len = %d, want 0 (degrades to fresh context)", len(snap))
	}
	if err := ValidateToolPairing(snap); err != nil {
		t.Fatalf("empty snapshot not pairing-valid: %v", err)
	}
	child := New("fork-child", ModeDefault, EnvironmentRef{Kind: EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"}, Limits{}, time.Unix(0, 0))
	if err := child.SeedHistory(snap); err != nil {
		t.Fatalf("SeedHistory(empty snapshot) should succeed: %v", err)
	}
	if len(child.Conversation.Messages) != 0 {
		t.Fatalf("fresh-context child should have empty history, got %d", len(child.Conversation.Messages))
	}
}
