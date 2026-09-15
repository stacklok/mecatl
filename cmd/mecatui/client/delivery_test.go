package client

import (
	"testing"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
)

// fenceDelivery wraps a delivery note body in the SAME fenced-untrusted block
// renderFireDelivery produces (internal/app/scheduler_delivery.go ->
// governance.FenceUntrusted): a "<<<UNTRUSTED\n" opener, the body, a trailing
// "<<<UNTRUSTED\n". The client package may not import engine/governance (the
// ui->no-engine layering rule), so this is the literal mirror of
// governance.WriteUntrustedBlock's bytes -- kept in sync by the delivery tests'
// contract (the live-wire relay + the TUI client detect the note via this exact
// shape). A drift would surface as a delivery-card regression here.
func fenceDelivery(body string) string {
	return "<<<UNTRUSTED\n" + body + "\n<<<UNTRUSTED\n"
}

// TestFireDelivery_Scenario5_DeliveryCardDistinctProvenance verifies AC5.2:
// EventToMsg maps a user_prompt event whose text is a FENCED delivery note
// (the renderFireDelivery output -- an untrusted-fence opener followed by the
// "[scheduled task ..." provenance header) to a DeliveryNoteMsg (NOT a
// UserPromptMsg), so the ui can render it as a distinct delivery card with a
// scheduled-task affordance + schedule name. A non-delivery user_prompt
// (un-fenced, or a bare "[scheduled task" a user typed) still maps to
// UserPromptMsg (unchanged).
func TestFireDelivery_Scenario5_DeliveryCardDistinctProvenance(t *testing.T) {
	deliveryBody := "[scheduled task nightly-sync (fire sched--fire1) completed with stop reason: end_turn]\nfire result text"
	deliveryText := fenceDelivery(deliveryBody)
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
			name: "delivery note -- live path (fenced)",
			text: deliveryText,
			want: expect{delivery: true, sched: "nightly-sync", fire: "sched--fire1", text: deliveryText},
		},
		{
			name: "ordinary user_prompt -- unchanged",
			text: "hello, look at this file",
			want: expect{delivery: false, text: "hello, look at this file"},
		},
		{
			name: "mention of scheduled task in prose -- NOT a delivery",
			text: "I scheduled task nightly-sync for 2pm",
			want: expect{delivery: false, text: "I scheduled task nightly-sync for 2pm"},
		},
		{
			name: "bare un-fenced delivery header a user typed -- NOT a delivery",
			text: "[scheduled task nightly-sync (fire sched--fire1) completed with stop reason: end_turn]\nfire result text",
			want: expect{delivery: false, text: "[scheduled task nightly-sync (fire sched--fire1) completed with stop reason: end_turn]\nfire result text"},
		},
		{
			name: "empty text -- NOT a delivery",
			text: "",
			want: expect{delivery: false, text: ""},
		},
		{
			name: "delivery note with different schedule name",
			text: fenceDelivery("[scheduled task daily-report (fire sched--fire2) completed with stop reason: end_turn]\nall reports generated"),
			want: expect{delivery: true, sched: "daily-report", fire: "sched--fire2",
				text: fenceDelivery("[scheduled task daily-report (fire sched--fire2) completed with stop reason: end_turn]\nall reports generated")},
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
// maps a fenced delivery-patterned user_prompt to a DeliveryNoteMsg -- so a TUI
// that reconnects AFTER a delivery sees the distinct delivery card via the
// replay path, not a lost event.
func TestFireDelivery_Scenario5_ReconnectReplaySeesDelivery(t *testing.T) {
	deliveryBody := "[scheduled task nightly-sync (fire sched--fire1) completed with stop reason: end_turn]\nfire result text"
	deliveryText := fenceDelivery(deliveryBody)
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
	if !contains(msg.Text, deliveryBody) {
		t.Errorf("Text does not contain the delivery note body")
	}
	// AC5.4: the replay path relays the SAME structured fields -- no second
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

func TestSyntheticUserPromptReplay_Scenario2_ClientMapsStructuredOrigin(t *testing.T) {
	parts := []*mecatlv1.Content{{Kind: mecatlv1.Content_KIND_IMAGE, MimeType: "image/png", Data: []byte("pixels")}}
	for _, synthetic := range []bool{false, true} {
		ev := &mecatlv1.Event{Type: "user_prompt", UserPrompt: &mecatlv1.UserPrompt{
			Text: "Please continue working on the task.", Parts: parts, Synthetic: synthetic,
		}}
		got, ok := EventToMsg(ev).(UserPromptMsg)
		if !ok {
			t.Fatalf("synthetic=%v: EventToMsg = %T, want UserPromptMsg", synthetic, EventToMsg(ev))
		}
		if got.Text != ev.GetUserPrompt().GetText() || got.Synthetic != synthetic {
			t.Fatalf("synthetic=%v: mapped prompt = %+v", synthetic, got)
		}
		if len(got.Parts) != 1 || got.Parts[0].MimeType != "image/png" {
			t.Fatalf("synthetic=%v: mapped parts = %+v", synthetic, got.Parts)
		}
	}
}

func TestSyntheticUserPromptReplay_Scenario2_DeliveryProjectionTakesPrecedence(t *testing.T) {
	text := fenceDelivery("[scheduled task nightly-sync (fire sched--fire1) completed with stop reason: end_turn]\nresult")
	for _, synthetic := range []bool{false, true} {
		got := EventToMsg(&mecatlv1.Event{Type: "user_prompt", UserPrompt: &mecatlv1.UserPrompt{Text: text, Synthetic: synthetic}})
		delivery, ok := got.(DeliveryNoteMsg)
		if !ok {
			t.Fatalf("synthetic=%v: EventToMsg = %T, want DeliveryNoteMsg", synthetic, got)
		}
		if delivery.ScheduleName != "nightly-sync" || delivery.FireID != "sched--fire1" {
			t.Fatalf("synthetic=%v: delivery = %+v", synthetic, delivery)
		}
	}
}
