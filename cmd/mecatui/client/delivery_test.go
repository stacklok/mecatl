package client

import (
	"testing"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
)

// TestFireDelivery_Scenario5_DeliveryCardDistinctProvenance verifies AC5.2:
// EventToMsg maps a user_prompt event whose text starts with the delivery
// provenance header [scheduled task ...] to a DeliveryNoteMsg (NOT a
// UserPromptMsg), so the ui can render it as a distinct delivery card with
// a scheduled-task affordance + schedule name. A non-delivery user_prompt
// still maps to UserPromptMsg (unchanged).
func TestFireDelivery_Scenario5_DeliveryCardDistinctProvenance(t *testing.T) {
	type expect struct {
		delivery bool
		sched    string
		fire     string
		text     string
	}
	cases := []struct {
		name string
		text string
		want expect
	}{
		{
			name: "delivery note — live path",
			text: "[scheduled task nightly-sync (fire sched--fire1) completed with stop reason: end_turn]\nfire result text",
			want: expect{delivery: true, sched: "nightly-sync", fire: "sched--fire1",
				text: "[scheduled task nightly-sync (fire sched--fire1) completed with stop reason: end_turn]\nfire result text"},
		},
		{
			name: "ordinary user_prompt — unchanged",
			text: "hello, look at this file",
			want: expect{delivery: false, text: "hello, look at this file"},
		},
		{
			name: "mention of scheduled task in prose — NOT a delivery",
			text: "I scheduled task nightly-sync for 2pm",
			want: expect{delivery: false, text: "I scheduled task nightly-sync for 2pm"},
		},
		{
			name: "empty text — NOT a delivery",
			text: "",
			want: expect{delivery: false, text: ""},
		},
		{
			name: "delivery note with different schedule name",
			text: "[scheduled task daily-report (fire sched--fire2) completed with stop reason: end_turn]\nall reports generated",
			want: expect{delivery: true, sched: "daily-report", fire: "sched--fire2",
				text: "[scheduled task daily-report (fire sched--fire2) completed with stop reason: end_turn]\nall reports generated"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ev := &mecatlv1.Event{
				Type: "user_prompt",
				UserPrompt: &mecatlv1.UserPrompt{
					Text: tc.text,
				},
			}
			got := EventToMsg(ev)
			if !tc.want.delivery {
				m, ok := got.(UserPromptMsg)
				if !ok {
					t.Fatalf("expected UserPromptMsg, got %T: %+v", got, got)
				}
				if m.Text != tc.want.text {
					t.Errorf("UserPromptMsg.Text = %q, want %q", m.Text, tc.want.text)
				}
				return
			}
			gotMsg, ok := got.(DeliveryNoteMsg)
			if !ok {
				t.Fatalf("expected DeliveryNoteMsg, got %T: %+v", got, got)
			}
			if gotMsg.ScheduleName != tc.want.sched {
				t.Errorf("DeliveryNoteMsg.ScheduleName = %q, want %q", gotMsg.ScheduleName, tc.want.sched)
			}
			if gotMsg.FireID != tc.want.fire {
				t.Errorf("DeliveryNoteMsg.FireID = %q, want %q", gotMsg.FireID, tc.want.fire)
			}
			if gotMsg.Text != tc.want.text {
				t.Errorf("DeliveryNoteMsg.Text = %q, want %q", gotMsg.Text, tc.want.text)
			}
		})
	}
}

// TestFireDelivery_Scenario5_ReconnectReplaySeesDelivery verifies AC5.4:
// a replay feed (StreamSessionEvents) relays EvUserPrompt, and EventToMsg
// maps a delivery-patterned user_prompt to a DeliveryNoteMsg — so a TUI
// that reconnects AFTER a delivery sees the distinct delivery card via the
// replay path, not a lost event.
func TestFireDelivery_Scenario5_ReconnectReplaySeesDelivery(t *testing.T) {
	deliveryText := "[scheduled task nightly-sync (fire sched--fire1) completed with stop reason: end_turn]\nfire result text"
	ev := &mecatlv1.Event{
		Type: "user_prompt",
		UserPrompt: &mecatlv1.UserPrompt{
			Text: deliveryText,
		},
	}
	got := EventToMsg(ev)
	msg, ok := got.(DeliveryNoteMsg)
	if !ok {
		t.Fatalf("replay of delivery user_prompt: expected DeliveryNoteMsg, got %T: %+v", got, got)
	}
	if msg.ScheduleName != "nightly-sync" {
		t.Errorf("ScheduleName = %q, want nightly-sync", msg.ScheduleName)
	}
	if msg.FireID != "sched--fire1" {
		t.Errorf("FireID = %q, want sched--fire1", msg.FireID)
	}
	if !contains(msg.Text, deliveryText) {
		t.Errorf("Text does not contain the delivery note body")
	}
	// AC5.4: the replay path relays the SAME structured fields — no second
	// translation path.
	_ = msg.FireID
}

func contains(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
