package scrollback

import "testing"

func TestEngineHookIdentityRevisionAndOrigin(t *testing.T) {
	var c Conversation
	c.SetOriginSession("root")
	c.Tools().Add(ToolCall{ID: "tc_root", Name: "Read"})
	record := c.Tools().RecordHookEvent
	first := HookSnapshot{SourceID: "review_tc_root_action", Revision: 8, OriginSessionID: "root", CallID: "tc_root", RunID: "run-1", Seq: 1, Review: &HookReview{ReviewID: "review_tc_root_action", Disposition: "ask_action"}}
	accepted := record(first)
	if accepted.ID != HookID(first.SourceID) || accepted.Status != HookRecorded || accepted.Attachment != HookAttached {
		t.Fatalf("first: %+v", accepted)
	}
	current, _ := c.Hook(accepted.ID)
	current.Detail = HookLiveDetail{State: HookDetailReceipt, Receipt: "approved", Concern: "explanation"}
	if !c.ReviseHook(accepted.ID, current) {
		t.Fatal("failed to preserve live detail")
	}
	final := cloneHook(first)
	final.Revision, final.RunID, final.Seq, final.Review.Disposition = 12, "run-2", 1, "execute"
	if got := record(final); got.ID != accepted.ID || got.Status != HookUpdated {
		t.Fatalf("new version: %+v", got)
	}
	if got := record(final); got.Status != HookDuplicate {
		t.Fatalf("identical update: %+v", got)
	}
	for _, conflict := range []HookSnapshot{
		first,
		func() HookSnapshot { x := cloneHook(final); x.Text = "different"; return x }(),
		func() HookSnapshot { x := cloneHook(final); x.OriginSessionID = "child"; return x }(),
		func() HookSnapshot { x := cloneHook(final); x.CallID = "different"; return x }(),
		func() HookSnapshot { x := cloneHook(final); x.Review.ReviewID = "mismatch"; return x }(),
		func() HookSnapshot { x := cloneHook(final); x.Revision = 0; return x }(),
	} {
		got := record(conflict)
		if got.Status != HookConflict || got.Attachment != HookRejected || got.ID == accepted.ID {
			t.Fatalf("conflict accepted: %+v", got)
		}
		if replay := record(conflict); replay.Status != HookDuplicate || replay.ID != got.ID {
			t.Fatalf("conflict replay: %+v", replay)
		}
	}
	stored, _ := c.Hook(accepted.ID)
	if stored.Revision != 12 || stored.Review.Disposition != "execute" || stored.Detail != current.Detail || len(c.SnapshotAt(0).Payload.(ToolCardSnapshot).Hooks) != 1 {
		t.Fatalf("canonical rolled back or detail lost: %+v", stored)
	}
}

func TestDuplicateCallIdentityRejectsHookAttachment(t *testing.T) {
	var c Conversation
	c.SetOriginSession("root")
	c.Tools().Add(ToolCall{ID: "tc_collision", Name: "Read"})
	c.Tools().Add(ToolCall{ID: "tc_collision", Name: "Write"})
	outcome := c.Tools().RecordHookEvent(HookSnapshot{SourceID: "hook_collision", Revision: 1, OriginSessionID: "root", CallID: "tc_collision"})
	if outcome.Attachment != HookRejected || outcome.ID != "hook_collision" {
		t.Fatalf("ambiguous card association: %+v", outcome)
	}
	for i := 0; i < c.Len(); i++ {
		if len(c.SnapshotAt(i).Payload.(ToolCardSnapshot).Hooks) != 0 {
			t.Fatal("hook attached to ambiguous card")
		}
	}
}

func TestChildHookNeverAttachesToRootCard(t *testing.T) {
	var c Conversation
	c.SetOriginSession("root")
	c.Tools().Add(ToolCall{ID: "tc_root"})
	child := HookSnapshot{SourceID: "review_child", Revision: 1, OriginSessionID: "child", CallID: "tc_root", Review: &HookReview{ReviewID: "review_child"}}
	if got := c.Tools().RecordHookEvent(child); got.Attachment != HookRejected || got.ID != "review_child" {
		t.Fatalf("child attachment: %+v", got)
	}
	if got := c.SnapshotAt(0).Payload.(ToolCardSnapshot); len(got.Hooks) != 0 {
		t.Fatalf("child leaked to root: %+v", got)
	}
}
