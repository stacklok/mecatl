package scrollback

import "testing"

func TestNoticeMutationChecksKindAndMaintainsCallIndex(t *testing.T) {
	var c Conversation
	tool := c.Tools().Add(ToolCall{ID: "read", Name: "Read"})
	notice := c.Notices().AddNotice("before")
	other := c.Tools().Add(ToolCall{ID: "write", Name: "Write"})
	for _, id := range []BlockID{0, 999, tool, other} {
		if c.Notices().UpdateNotice(id, "wrong") || c.Notices().RemoveNotice(id) || c.Messages().RemoveUser(id) {
			t.Fatalf("wrong kind or missing ID %d mutated conversation", id)
		}
	}
	if c.Len() != 3 || !c.Notices().UpdateNotice(notice, "after") || c.SnapshotAt(1).Revision != 1 {
		t.Fatal("notice update did not preserve position and advance revision")
	}
	if !c.Notices().RemoveNotice(notice) || c.Notices().RemoveNotice(notice) || c.Len() != 2 {
		t.Fatal("notice removal failed or matched again")
	}
	for _, call := range []string{"read", "write"} {
		if !c.Tools().Resolve(call, ToolResult{Body: call}) {
			t.Fatalf("call index for %s was lost after notice removal", call)
		}
		snapshot, ok := c.SnapshotForCall(call)
		if !ok || snapshot.Payload.(ToolCardSnapshot).Result.Body != call {
			t.Fatalf("call %s points at wrong card: %+v", call, snapshot)
		}
	}
}

func TestPlainCardsExposeEveryKind(t *testing.T) {
	var c Conversation
	plain := c.Notices()
	plain.AddNotice("notice")
	plain.AddRecoveryNotice("recovery")
	plain.AddTurnStat("stats")
	plain.AddError("error", true)
	plain.AddHook("before", "Read", "allow")
	plain.AddHookText("hook", "after", "Write", "deny")
	plain.AddDelivery("fire", "delivery")
	plain.AddDeliveryWithSchedule("daily", "scheduled", "delivery")

	want := []Kind{KindNotice, KindNotice, KindTurnStat, KindError, KindHook, KindHook, KindDelivery, KindDelivery}
	for i, kind := range want {
		if got := c.SnapshotAt(i).Payload.Kind(); got != kind {
			t.Fatalf("card %d kind = %v, want %v", i, got, kind)
		}
	}
	if got := c.SnapshotAt(1).Payload.(NoticeCardSnapshot); !got.Recover {
		t.Fatalf("recovery notice = %#v", got)
	}
	if got := c.SnapshotAt(3).Payload.(ErrorCardSnapshot); !got.Permanent {
		t.Fatalf("error card = %#v", got)
	}
	if got, want := c.SnapshotAt(4).Payload, (HookCardSnapshot{Phase: "before", Tool: "Read", Decision: "allow"}); got != want {
		t.Fatalf("hook = %#v, want %#v", got, want)
	}
	if got, want := c.SnapshotAt(7).Payload, (DeliveryCardSnapshot{ScheduleName: "daily", FireID: "scheduled", Text: "delivery"}); got != want {
		t.Fatalf("delivery = %#v, want %#v", got, want)
	}
}
