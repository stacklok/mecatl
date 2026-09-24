package scrollback

import "testing"

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
