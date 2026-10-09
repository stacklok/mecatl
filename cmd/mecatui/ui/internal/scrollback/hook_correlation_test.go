package scrollback

import "testing"

func TestHookSourceUpdateKeepsLiveReceipt(t *testing.T) {
	var c Conversation
	id := c.EnsureHookReview("review")
	hook := HookSnapshot{CallID: "call", RunID: "run", Seq: 1, Review: &HookReview{ReviewID: "review", Disposition: "ask_action"}}
	c.Tools().Add(ToolCall{ID: "call"})
	if got := c.Tools().RecordHookEvent(hook); got.ID != id || got.Attachment != HookAttached {
		t.Fatalf("placeholder binding: %+v", got)
	}
	current, _ := c.Hook(id)
	current.Detail = HookLiveDetail{State: HookDetailReceipt, Concern: "validated", Receipt: "allowed"}
	if !c.ReviseHook(id, current) {
		t.Fatal("receipt revision rejected")
	}
	final := cloneHook(hook)
	final.Seq = 2
	final.Review.Disposition = "execute"
	if got := c.Tools().RecordHookEvent(final); got.ID != id || got.Status != HookUpdated {
		t.Fatalf("final outcome: %+v", got)
	}
	if got, _ := c.Hook(id); got.Detail != current.Detail || got.Review.Disposition != "execute" {
		t.Fatalf("source update lost receipt: %+v", got)
	}
	if got := c.Tools().RecordHookEvent(hook); got.Status != HookDuplicate {
		t.Fatalf("old source no longer recognized: %+v", got)
	}
}

func TestReviewUpsertAcquiresButDoesNotEraseCallID(t *testing.T) {
	var c Conversation
	record := c.Tools().RecordHookEvent
	withoutCall := HookSnapshot{RunID: "run", Seq: 1, Review: &HookReview{ReviewID: "review"}}
	if got := record(withoutCall); got.Status != HookRecorded {
		t.Fatalf("source without call: %+v", got)
	}
	c.Tools().Add(ToolCall{ID: "call"})
	withCall := cloneHook(withoutCall)
	withCall.CallID, withCall.Seq = "call", 2
	acquired := record(withCall)
	if acquired.Status != HookUpdated || acquired.Attachment != HookAttached {
		t.Fatalf("call acquisition: %+v", acquired)
	}
	if hook, _ := c.Hook(acquired.ID); hook.CallID != "call" {
		t.Fatalf("acquired call ID lost: %+v", hook)
	}
	withoutCall.Seq = 3
	if got := record(withoutCall); got.Status != HookConflict || got.Attachment != HookRejected {
		t.Fatalf("established call ID erased: %+v", got)
	}
}

func TestOlderReviewSourceIsStandaloneConflict(t *testing.T) {
	var c Conversation
	c.Tools().Add(ToolCall{ID: "call"})
	record := c.Tools().RecordHookEvent
	newer := HookSnapshot{CallID: "call", RunID: "run", Seq: 12, Text: "new", Review: &HookReview{ReviewID: "review", Disposition: "blocked"}}
	accepted := record(newer)
	if accepted.Status != HookRecorded || accepted.Attachment != HookAttached {
		t.Fatalf("newer source: %+v", accepted)
	}
	canonical, _ := c.Hook(accepted.ID)
	canonical.Detail = HookLiveDetail{State: HookDetailReceipt, Receipt: "approved"}
	if !c.ReviseHook(accepted.ID, canonical) {
		t.Fatal("receipt revision rejected")
	}
	older := cloneHook(newer)
	older.Seq, older.Text, older.Review.Disposition = 11, "old", "pending"
	conflict := record(older)
	if conflict.Status != HookConflict || conflict.Attachment != HookRejected || conflict.ID == accepted.ID {
		t.Fatalf("older source: %+v", conflict)
	}
	if got, _ := c.Hook(accepted.ID); got.Text != "new" || got.Review.Disposition != "blocked" || got.Detail.Receipt != "approved" {
		t.Fatalf("older source changed canonical record: %+v", got)
	}
	if got, _ := c.Hook(conflict.ID); got.Text != "old" || got.Review.Disposition != "pending" {
		t.Fatalf("older evidence was not standalone: %+v", got)
	}
	if got := record(older); got.Status != HookDuplicate || got.ID != conflict.ID || got.Attachment != HookRejected {
		t.Fatalf("older conflict replay: %+v", got)
	}
}

func TestUnidentifiedReviewSourceCannotReplaceIdentifiedOutcome(t *testing.T) {
	for _, identity := range []HookSnapshot{{}, {RunID: "run"}, {Seq: 11}} {
		var c Conversation
		c.Tools().Add(ToolCall{ID: "call"})
		newer := HookSnapshot{CallID: "call", RunID: "run", Seq: 12, Review: &HookReview{ReviewID: "review", Disposition: "execute"}}
		accepted := c.Tools().RecordHookEvent(newer)
		unknown := cloneHook(newer)
		unknown.RunID, unknown.Seq, unknown.Review.Disposition = identity.RunID, identity.Seq, "ask_action"
		if got := c.Tools().RecordHookEvent(unknown); got.Status != HookConflict || got.Attachment != HookRejected {
			t.Fatalf("unidentified source replaced accepted outcome: %+v", got)
		}
		if got, _ := c.Hook(accepted.ID); got.RunID != "run" || got.Seq != 12 || got.Review.Disposition != "execute" {
			t.Fatalf("accepted outcome changed: %+v", got)
		}
	}
}

func TestHookEventIdentityAndReviewUpsert(t *testing.T) {
	var c Conversation
	c.Tools().Add(ToolCall{ID: "call", Name: "Read"})
	record := c.Tools().RecordHookEvent
	generic := HookSnapshot{CallID: "call", RunID: "run", Seq: 1, Text: "same"}
	first := record(generic)
	if first.Status != HookRecorded || first.Attachment != HookAttached {
		t.Fatalf("first: %+v", first)
	}
	if got := record(generic); got.Status != HookDuplicate || got.ID != first.ID {
		t.Fatalf("replay: %+v", got)
	}
	for _, h := range []HookSnapshot{
		{CallID: "call", RunID: "other", Seq: 1, Text: "same"},
		{CallID: "call", RunID: "run", Seq: 2, Text: "same"},
		{CallID: "call", RunID: "", Seq: 1, Text: "same"},
		{CallID: "call", RunID: "", Seq: 1, Text: "same"},
		{CallID: "call", RunID: "run", Seq: 0, Text: "same"},
		{CallID: "call", RunID: "run", Seq: 0, Text: "same"},
	} {
		if got := record(h); got.Status != HookRecorded || got.ID == first.ID || got.Attachment != HookAttached {
			t.Fatalf("distinct source: %+v", got)
		}
	}
	conflict := generic
	conflict.Text = "contradiction"
	contradiction := record(conflict)
	if contradiction.Status != HookConflict || contradiction.Attachment != HookRejected || contradiction.ID == first.ID {
		t.Fatalf("event conflict: %+v", contradiction)
	}
	if again := record(conflict); again.Status != HookDuplicate || again.ID != contradiction.ID || again.Attachment != HookRejected {
		t.Fatalf("conflicting source replay: %+v", again)
	}
	if got, _ := c.Hook(first.ID); got.Text != "same" {
		t.Fatalf("accepted record overwritten: %+v", got)
	}
	if got := record(generic); got.Status != HookDuplicate || got.ID != first.ID {
		t.Fatalf("conflict changed identity index: %+v", got)
	}
	if hooks := c.SnapshotAt(0).Payload.(ToolCardSnapshot).Hooks; len(hooks) != 7 {
		t.Fatalf("attached hooks = %d, want 7", len(hooks))
	}
}

func TestReviewReplaysCannotRollBack(t *testing.T) {
	var c Conversation
	c.Tools().Add(ToolCall{ID: "call"})
	record := c.Tools().RecordHookEvent
	old := HookSnapshot{CallID: "call", RunID: "run", Seq: 11, Text: "old", Review: &HookReview{ReviewID: "review", Disposition: "pending"}}
	first := record(old)
	newer := cloneHook(old)
	newer.Seq = 12
	newer.Text = "new"
	newer.Review.Disposition = "blocked"
	if got := record(newer); got.Status != HookUpdated || got.ID != first.ID || got.Attachment != HookAttached {
		t.Fatalf("review update: %+v", got)
	}
	before := c.SnapshotAt(0).Revision
	if got := record(old); got.Status != HookDuplicate || got.ID != first.ID {
		t.Fatalf("stale replay: %+v", got)
	}
	if hook, _ := c.Hook(first.ID); hook.Text != "new" || hook.Review.Disposition != "blocked" || c.SnapshotAt(0).Revision != before {
		t.Fatalf("rolled back: %+v", hook)
	}
	wrong := cloneHook(old)
	wrong.Review.Disposition = "allowed"
	if got := record(wrong); got.Status != HookConflict || got.Attachment != HookRejected {
		t.Fatalf("conflicting replay: %+v", got)
	}
	wrong = cloneHook(newer)
	wrong.CallID = "other"
	wrong.Seq = 13
	callConflict := record(wrong)
	if callConflict.Status != HookConflict || callConflict.Attachment != HookRejected {
		t.Fatalf("review/call conflict: %+v", callConflict)
	}
	other := HookSnapshot{CallID: "call", RunID: "run", Seq: 13, Review: &HookReview{ReviewID: "another"}}
	if got := record(other); got.Status != HookRecorded || got.ID == first.ID {
		t.Fatalf("distinct review: %+v", got)
	}
	if got := record(wrong); got.Status != HookDuplicate || got.ID != callConflict.ID {
		t.Fatalf("event/review collision replay: %+v", got)
	}
	updated, _ := c.Hook(first.ID)
	updated.Detail = HookLiveDetail{State: HookDetailReceipt, Receipt: "receipt"}
	if !c.ReviseHook(first.ID, updated) {
		t.Fatal("live detail revision")
	}
	changed := cloneHook(updated)
	changed.CallID = "other"
	if c.ReviseHook(first.ID, changed) {
		t.Fatal("changed call identity")
	}
	changed = cloneHook(updated)
	changed.Review.ReviewID = "another"
	if c.ReviseHook(first.ID, changed) {
		t.Fatal("changed review identity")
	}
	if got := record(newer); got.Status != HookDuplicate || got.ID != first.ID {
		t.Fatalf("replay after live revision: %+v", got)
	}
	if hook, _ := c.Hook(first.ID); hook.Detail.Receipt != "receipt" {
		t.Fatalf("lost live detail: %+v", hook)
	}
	c.Tools().Add(ToolCall{ID: "call"})
	later := cloneHook(newer)
	later.Seq = 14
	later.Review.Disposition = "approved"
	if got := record(later); got.Status != HookConflict || got.Attachment != HookRejected {
		t.Fatalf("ambiguous bound review update: %+v", got)
	}
	if hook, _ := c.Hook(first.ID); hook.Review.Disposition != "blocked" {
		t.Fatalf("bound review overwritten: %+v", hook)
	}
}

func TestHookAssociationUniqueAndStable(t *testing.T) {
	var c Conversation
	record := c.Tools().RecordHookEvent
	pending := record(HookSnapshot{CallID: "late", RunID: "r", Seq: 1})
	if pending.Attachment != HookPending || pending.ID == 0 || c.AttachHook("", pending.ID) || c.AttachHook("wrong", pending.ID) {
		t.Fatalf("pending or mismatched: %+v", pending)
	}
	if got := record(HookSnapshot{RunID: "r", Seq: 2}); got.Attachment != HookRejected {
		t.Fatalf("empty call: %+v", got)
	}
	c.Tools().Add(ToolCall{ID: "late"})
	if !c.AttachHook("late", pending.ID) {
		t.Fatal("pending attachment")
	}
	if !c.AttachHook("late", pending.ID) {
		t.Fatal("idempotent pending attachment")
	}
	original := c.SnapshotAt(0)
	c.Tools().Add(ToolCall{ID: "late"})
	if c.AttachHook("late", pending.ID) || c.AttachHook("late", pending.ID+100) {
		t.Fatal("ambiguous attachment")
	}
	if got := record(HookSnapshot{CallID: "late", RunID: "r", Seq: 3}); got.Attachment != HookRejected {
		t.Fatalf("ambiguous record: %+v", got)
	}
	if got := record(HookSnapshot{CallID: "late", RunID: "r", Seq: 1}); got.Attachment != HookRejected || got.ID != pending.ID {
		t.Fatalf("ambiguous replay: %+v", got)
	}
	review := HookSnapshot{CallID: "late", RunID: "r", Seq: 4, Review: &HookReview{ReviewID: "rv", Disposition: "pending"}}
	reviewID := record(review).ID
	review.Seq = 5
	review.Review.Disposition = "blocked"
	if got := record(review); got.Status != HookConflict || got.Attachment != HookRejected {
		t.Fatalf("ambiguous review update: %+v", got)
	}
	if hook, _ := c.Hook(reviewID); hook.Review.Disposition != "pending" {
		t.Fatalf("ambiguous update overwrote review: %+v", hook)
	}
	if len(c.SnapshotAt(0).Payload.(ToolCardSnapshot).Hooks) != 1 || len(c.SnapshotAt(1).Payload.(ToolCardSnapshot).Hooks) != 0 || c.SnapshotAt(0).Revision != original.Revision {
		t.Fatal("binding moved or changed on reuse")
	}
	// Lifecycle results still target the latest index, not the hook binding.
	if !c.Tools().Resolve("late", ToolResult{Body: "latest"}) || c.SnapshotAt(0).Payload.(ToolCardSnapshot).Resolved || !c.SnapshotAt(1).Payload.(ToolCardSnapshot).Resolved {
		t.Fatal("latest result behavior changed")
	}
}
