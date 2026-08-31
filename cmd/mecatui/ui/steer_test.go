package ui

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/theme"
)

// Scenario 6 of docs/acceptance/steer-while-running.md (the mecatui half): the
// capability-driven flip. When the server advertises Capabilities.Steer, `enter`
// mid-run sends a `steer` frame on the Converse stream (not a local stage) and the
// queue card reflects the AUTHORITATIVE echoed/acked state; when the capability is
// absent, the #228 client-side terminal merge-queue owns mid-run input,
// byte-identical.

// newSteerModel builds a connected, idle Model whose fakeConv advertises the given
// Steer capability on CreateSession (mirrors newQueueModel, with caps).
func newSteerModel(t *testing.T, steerCap bool) (Model, *fakeConv) {
	t.Helper()
	recv := &fakeRecver{}
	send := &fakeSender{}
	conv := &fakeConv{recv: recv, send: send, caps: client.Capabilities{Steer: steerCap}}
	m := newTestModelFromDeps(Deps{
		Session:     conv,
		Conv:        conv,
		Theme:       theme.New("aztec", theme.AztecPalette()),
		Ctx:         context.Background(),
		NoAltScreen: true,
	})
	m = applyAll(m,
		tea.WindowSizeMsg{Width: 100, Height: 30},
		client.SessionReadyMsg{SessionID: "sess-test-0001", Capabilities: conv.caps},
	)
	return m, conv
}

// enqueueSteer types text and presses enter while running, then RUNS the returned
// command so the steer frame's Send executes (sendSteer's send is a synchronous
// tea.Cmd). Returns the updated model.
func enqueueSteer(t *testing.T, m Model, text string) Model {
	t.Helper()
	m = typeText(t, m, text)
	mm, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	runBatchLeaves(cmd)
	return mm.(Model)
}

// steerFrame pairs one sent `steer` frame's text with its message_id.
type steerFrame struct {
	text string
	id   string
}

// steerFrames returns the text+message_id of every `steer` frame sent on the
// stream, in order.
func steerFrames(send *fakeSender) []steerFrame {
	var out []steerFrame
	for _, fr := range send.frames() {
		if s := fr.GetSteer(); s != nil {
			out = append(out, steerFrame{text: s.GetText(), id: s.GetMessageId()})
		}
	}
	return out
}

// steerTexts returns only the text-half of steerFrames (legacy callers).
func steerTexts(send *fakeSender) []string {
	var out []string
	for _, sf := range steerFrames(send) {
		out = append(out, sf.text)
	}
	return out
}

// steerCancelCount returns how many `steer_cancel` frames were sent.
func steerCancelCount(send *fakeSender) int {
	n := 0
	for _, fr := range send.frames() {
		if fr.GetSteerCancel() != nil {
			n++
		}
	}
	return n
}

func TestSteer_MultimodalSendAndEditBack(t *testing.T) {
	m, conv := newSteerModel(t, true)
	m.caps.Image = true
	m = startRunning(t, m, "first")
	m.prompt.Rewrite("inspect [Image #1]")
	m.stagedMedia = map[string]stagedAttachment{
		"[Image #1]": {mime: "image/png", data: []byte("pixels")},
		"[Image #2]": {mime: "image/png", data: []byte("deleted")},
	}
	mm, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = mm.(Model)
	runBatchLeaves(cmd)
	frames := conv.send.frames()
	steer := frames[len(frames)-1].GetSteer()
	part := steer.GetParts()[0]
	if steer.GetText() != "inspect" || len(steer.GetParts()) != 1 || part.GetKind().String() != "KIND_IMAGE" || part.GetMimeType() != "image/png" || string(part.GetData()) != "pixels" {
		t.Fatalf("steer = text %q parts %#v", steer.GetText(), steer.GetParts())
	}
	if strings.Contains(steer.GetText(), "[Image #1]") {
		t.Fatal("attachment marker reached the wire")
	}
	mm, cmd = m.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	m = mm.(Model)
	runBatchLeaves(cmd)
	if m.prompt.Value() != "inspect [Image #1]" || string(m.stagedMedia["[Image #1]"].data) != "pixels" {
		t.Fatalf("edit-back lost draft/media: %q %#v", m.prompt.Value(), m.stagedMedia)
	}
	if _, retained := m.stagedMedia["[Image #2]"]; retained {
		t.Fatal("deleted attachment marker was retained by queued send")
	}
	m.stagedMedia["[Image #1]"] = stagedAttachment{mime: "image/png", data: []byte("replacement-pixels")}
	mm, cmd = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = mm.(Model)
	runBatchLeaves(cmd)
	replacement := conv.send.frames()[len(conv.send.frames())-1].GetSteer()
	if replacement.GetText() != "inspect" || len(replacement.GetParts()) != 1 || replacement.GetParts()[0].GetMimeType() != "image/png" || string(replacement.GetParts()[0].GetData()) != "replacement-pixels" {
		t.Fatalf("replacement steer = text %q parts %#v", replacement.GetText(), replacement.GetParts())
	}
	mm, cmd = m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	m = mm.(Model)
	runBatchLeaves(cmd)
	mm, _ = m.Update(client.SteerOutcomeMsg{Outcome: client.SteerRetracted, MessageID: "steer-0002"})
	m = mm.(Model)
	if m.steer == nil || m.steer.Phase != steerRetracted || len(m.steer.Sends) != 1 || len(m.steer.Sends[0].Media.Parts) != 0 || len(m.steer.Sends[0].Staged) != 0 || len(m.stagedMedia) != 0 {
		t.Fatalf("cancel retained steer attachments: steer=%#v staged=%#v", m.steer, m.stagedMedia)
	}
}

func TestSteer_RejectsCompletePendingMediaAggregateAtomically(t *testing.T) {
	m, conv := newSteerModel(t, true)
	m.caps.Image = true
	m = startRunning(t, m, "first")
	m.steer = &steerState{Phase: steerSent, Text: "kept"}
	for i := 0; i < 16; i++ {
		part, desc, err := client.StageClipboardImage("image/png", []byte{byte(i)}, m.caps)
		if err != nil {
			t.Fatal(err)
		}
		media := client.MediaResult{Descriptors: []string{desc}}
		media.Parts = append(media.Parts, part)
		m.steer.Sends = append(m.steer.Sends, steerQueuedSend{ID: "old", Text: "kept", Media: media})
	}
	m.prompt.Rewrite("new [Image #1]")
	m.stagedMedia = map[string]stagedAttachment{"[Image #1]": {mime: "image/png", data: []byte("new")}}
	before := m.steer
	frames := len(conv.send.frames())
	mm, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = mm.(Model)
	runBatchLeaves(cmd)
	if m.steer != before || m.prompt.Value() != "new [Image #1]" || string(m.stagedMedia["[Image #1]"].data) != "new" || len(conv.send.frames()) != frames {
		t.Fatalf("aggregate rejection mutated state: steer=%p/%p draft=%q staged=%#v frames=%d/%d", m.steer, before, m.prompt.Value(), m.stagedMedia, len(conv.send.frames()), frames)
	}
}

func TestSteer_MediaOnlySendAndEcho(t *testing.T) {
	m, conv := newSteerModel(t, true)
	m.caps.Image = true
	m = startRunning(t, m, "first")
	m.prompt.Rewrite("[Image #1]")
	m.stagedMedia = map[string]stagedAttachment{"[Image #1]": {mime: "image/png", data: []byte("pixels")}}
	mm, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = mm.(Model)
	runBatchLeaves(cmd)
	steer := conv.send.frames()[len(conv.send.frames())-1].GetSteer()
	if steer.GetText() != "" || len(steer.GetParts()) != 1 {
		t.Fatalf("media-only steer = text %q parts %d", steer.GetText(), len(steer.GetParts()))
	}
	mm, _ = m.Update(client.SteerEchoMsg{Parts: []client.ContentBlock{{Kind: client.ContentBlockImage, MimeType: "image/png", Data: []byte("pixels")}}, MessageID: "steer-0001"})
	m = mm.(Model)
	last := m.conv.blocks[len(m.conv.blocks)-1]
	if len(last.media) != 1 || last.media[0] != "image/png (inline)" {
		t.Fatalf("echo media projection = %#v", last)
	}
}

func TestSteer_FailedAckRestoresCorrelatedAttachment(t *testing.T) {
	m, conv := newSteerModel(t, true)
	m.caps.Image = true
	m = startRunning(t, m, "first")
	m.prompt.Rewrite("retry [Image #1]")
	m.stagedMedia = map[string]stagedAttachment{"[Image #1]": {mime: "image/png", data: []byte("owned-once")}}
	mm, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = mm.(Model)
	runBatchLeaves(cmd)
	mm, _ = m.Update(client.SteerOutcomeMsg{Outcome: client.SteerTooLate, Promoted: false, MessageID: "steer-0001"})
	m = mm.(Model)
	if m.steer != nil || m.prompt.Value() != "retry [Image #1]" || string(m.stagedMedia["[Image #1]"].data) != "owned-once" {
		t.Fatalf("failed send not restored exactly: steer=%#v draft=%q staged=%#v", m.steer, m.prompt.Value(), m.stagedMedia)
	}
	mm, cmd = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = mm.(Model)
	runBatchLeaves(cmd)
	frames := conv.send.frames()
	last := frames[len(frames)-1].GetSteer()
	if last.GetMessageId() != "steer-0002" || len(last.GetParts()) != 1 || string(last.GetParts()[0].GetData()) != "owned-once" || len(m.stagedMedia) != 0 {
		t.Fatalf("retry reused/lost attachment: frame=%#v staged=%#v", last, m.stagedMedia)
	}
}

// TestSteer_RuntimeDisabledFallsBackToLocalQueue verifies that runtime feature
// disabling preserves the client-side queue: `enter` mid-run sends no steer
// frame, the queue holds merged text, and a clean end drains it through the
// ordinary prompt path.
func TestSteer_RuntimeDisabledFallsBackToLocalQueue(t *testing.T) {
	m, conv := newSteerModel(t, false) // runtime feature disabled
	m = startRunning(t, m, "first")

	m = enqueue(t, m, "second")
	m = enqueue(t, m, "third")

	// The local merge-queue owns mid-run input: no steer frame, the queue holds both.
	if got := steerFrames(conv.send); len(got) != 0 {
		t.Fatalf("steer-disabled must send NO steer frame; sent %v", got)
	}
	if len(m.queued) != 2 || m.queued[0] != "second" || m.queued[1] != "third" {
		t.Fatalf("queued = %v, want [second third] (the #228 local merge-queue)", m.queued)
	}
	if m.steer != nil {
		t.Fatalf("steer state must stay nil when the capability is absent, got %+v", m.steer)
	}

	// A clean end MERGES the queue into one prompt and drains it through the
	// ordinary prompt path (byte-identical to #228's merge-always).
	mm, cmd := m.Update(client.ResultMsg{Stop: "end_turn"})
	m = mm.(Model)
	runBatchLeaves(cmd)
	if len(m.queued) != 0 {
		t.Fatalf("the merged queue must drain on a clean end, got %v", m.queued)
	}
	got := promptTexts(conv.send)
	want := []string{"first", "second" + queueMergeSep + "third"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("prompt frames = %v, want %v (merged local-queue drain)", got, want)
	}
}

// TestSteer_TUIRendersAuthoritativeState is AC6.3: with the steer capability
// PRESENT, `enter` mid-run sends ONE merged steer frame, the card reflects the
// authoritative lifecycle (pending → sent → promoted-after-race → landed),
// and `↑` edits the whole in-flight message (the task-10 queue model: send
// collapses staged lines into ONE bundle with a fresh message_id; ↑ cancels the
// outstanding bundle and pulls it back as ONE editable blob; the drain echo
// renders the landed line IN CONTEXT at its stream position).
func TestSteer_TUIRendersAuthoritativeState(t *testing.T) {
	t.Run("send merges into one bundle and shows pending", func(t *testing.T) {
		m, conv := newSteerModel(t, true)
		m = startRunning(t, m, "first")

		m = enqueueSteer(t, m, "second")

		// The ONE bundle is on the wire with a fresh session-scoped id; the local
		// queue is untouched.
		got := steerFrames(conv.send)
		if len(got) != 1 || got[0].text != "second" || got[0].id != "steer-0001" {
			t.Fatalf("steer frames = %+v, want one frame text=second id=steer-0001", got)
		}
		if len(m.queued) != 0 {
			t.Fatalf("steer mode must not touch the local queue, got %v", m.queued)
		}
		if m.steer == nil || m.steer.Phase != steerPending || m.steer.watermarkID() != "steer-0001" {
			t.Fatalf("steer state = %+v, want the pending bundle id=steer-0001", m.steer)
		}
		// The card renders the merged message as ONE item in the pending state.
		card := stripANSIstr(m.renderSteer())
		if !strings.Contains(card, "sending") {
			t.Fatalf("pending card must show the sending state, got:\n%s", card)
		}
		if !strings.Contains(card, "second") {
			t.Fatalf("pending card must preview the combined message, got:\n%s", card)
		}
	})

	t.Run("accepted ack moves the card to sent", func(t *testing.T) {
		m, _ := newSteerModel(t, true)
		m = startRunning(t, m, "first")
		m = enqueueSteer(t, m, "second")

		mm, _ := m.Update(client.SteerOutcomeMsg{Outcome: client.SteerAccepted, Text: "second", MessageID: "steer-0001"})
		m = mm.(Model)

		if m.steer == nil || m.steer.Phase != steerSent {
			t.Fatalf("steer state = %+v, want sent (acked)", m.steer)
		}
		card := stripANSIstr(m.renderSteer())
		if !strings.Contains(card, "queued") {
			t.Fatalf("sent card must show the queued-for-boundary state, got:\n%s", card)
		}
	})

	t.Run("drain echo clears the card and lands the queued text in context", func(t *testing.T) {
		m, _ := newSteerModel(t, true)
		m = startRunning(t, m, "first")
		m = enqueueSteer(t, m, "second")
		mm, _ := m.Update(client.SteerOutcomeMsg{Outcome: client.SteerAccepted, Text: "second", MessageID: "steer-0001"})
		m = mm.(Model)

		mm2, _ := m.Update(client.SteerEchoMsg{Text: "second", MessageID: "steer-0001"})
		m = mm2.(Model)

		if m.steer != nil {
			t.Fatalf("the drain echo must clear the in-flight card, got %+v", m.steer)
		}
		if got := stripANSIstr(m.renderSteer()); got != "" {
			t.Fatalf("no card after the drain echo, got:\n%s", got)
		}
		// The landed line appears IN CONTEXT (appended to the transcript at the
		// echo's stream position — after the settling tool activity, before the
		// next turn).
		view := stripANSIstr(m.View().Content)
		if !strings.Contains(view, "second") {
			t.Fatalf("the landed steer must appear in the transcript, got:\n%s", view)
		}
	})

	t.Run("too_late ack renders the promoted-after-race state", func(t *testing.T) {
		m, _ := newSteerModel(t, true)
		m = startRunning(t, m, "first")
		m = enqueueSteer(t, m, "second")

		mm, _ := m.Update(client.SteerOutcomeMsg{Outcome: client.SteerTooLate, Text: "second", Promoted: true, MessageID: "steer-0001"})
		m = mm.(Model)

		if m.steer == nil || m.steer.Phase != steerPromoted {
			t.Fatalf("steer state = %+v, want promoted", m.steer)
		}
		card := stripANSIstr(m.renderSteer())
		if !strings.Contains(card, "follow-up") {
			t.Fatalf("promoted card must honestly show the promoted-after-race state, got:\n%s", card)
		}
	})

	t.Run("too_late ack with Promoted=false renders not-sent, not a follow-up", func(t *testing.T) {
		m, _ := newSteerModel(t, true)
		m = startRunning(t, m, "first")
		m = enqueueSteer(t, m, "second")

		mm, _ := m.Update(client.SteerOutcomeMsg{Outcome: client.SteerTooLate, Text: "second", Promoted: false, MessageID: "steer-0001"})
		m = mm.(Model)

		if m.steer != nil || m.prompt.Value() != "second" {
			t.Fatalf("failed undelivered send was not restored for editing: steer=%+v draft=%q", m.steer, m.prompt.Value())
		}
		status := stripANSIstr(m.statusMsg)
		if strings.Contains(status, "follow-up") || !strings.Contains(status, "restored for editing") {
			t.Fatalf("failed status must be honest and retryable, got %q", status)
		}
	})

	t.Run("up edits the combined message and resend replaces it", func(t *testing.T) {
		m, conv := newSteerModel(t, true)
		m = startRunning(t, m, "first")
		m = enqueueSteer(t, m, "second")
		mm, _ := m.Update(client.SteerOutcomeMsg{Outcome: client.SteerAccepted, Text: "second", MessageID: "steer-0001"})
		m = mm.(Model)

		// ↑ on an empty input with an in-flight steer CANCELS the outstanding
		// bundle and pulls the whole not-yet-drained message back into the
		// textarea as ONE editable blob (the non-destructive-edit old behaviour
		// that left the old frame live to be merged on resend is GONE).
		mm2, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyUp})
		m = mm2.(Model)
		runBatchLeaves(cmd)
		if got := m.prompt.Value(); got != "second" {
			t.Fatalf("↑ must pull the in-flight message into the textarea, got %q", got)
		}
		if got := steerCancelCount(conv.send); got != 1 {
			t.Fatalf("↑ must send exactly ONE steer_cancel for the outstanding bundle, got %d", got)
		}

		// Edit and resend: it collapses to ONE new bundle with a NEW id.
		m = typeText(t, m, " CHANGED") // appends to the pulled-back draft
		mm3, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
		m = mm3.(Model)
		runBatchLeaves(cmd)
		got := steerFrames(conv.send)
		want := []steerFrame{
			{text: "second", id: "steer-0001"},
			{text: "second CHANGED", id: "steer-0002"},
		}
		if len(got) != len(want) {
			t.Fatalf("steer frames = %+v, want %+v", got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("steer frame %d = %+v, want %+v (resend collapses to ONE new bundle with a NEW id)", i, got[i], want[i])
			}
		}
	})

	t.Run("esc cancels without retracting a pending steer", func(t *testing.T) {
		m, conv := newSteerModel(t, true)
		m = startRunning(t, m, "first")
		m = enqueueSteer(t, m, "second")

		mm, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
		m = mm.(Model)
		runBatchLeaves(cmd)

		if got := steerCancelCount(conv.send); got != 0 {
			t.Fatalf("esc must not retract a pending steer, got %d steer_cancel frames", got)
		}
		cancelFrames := 0
		for _, frame := range conv.send.frames() {
			if frame.GetCancel() != nil {
				cancelFrames++
			}
		}
		if cancelFrames != 1 {
			t.Fatalf("esc sent %d Cancel frames, want 1", cancelFrames)
		}
		if m.steer == nil || m.steer.Phase != steerPending {
			t.Fatalf("esc changed pending steer state: %+v", m.steer)
		}
	})

	t.Run("stale ack for a burned id is ignored", func(t *testing.T) {
		m, _ := newSteerModel(t, true)
		m = startRunning(t, m, "first")
		m = enqueueSteer(t, m, "second")
		mm, _ := m.Update(client.SteerEchoMsg{Text: "second", MessageID: "steer-0001"})
		m = mm.(Model)

		// The id is burned (the echo landed); a late accepted ack carrying that
		// STALE id must not resurrect a card.
		mm2, _ := m.Update(client.SteerOutcomeMsg{Outcome: client.SteerAccepted, Text: "second", MessageID: "steer-0001"})
		m = mm2.(Model)
		if m.steer != nil {
			t.Fatalf("a stale ack for a drained id must be ignored, got %+v", m.steer)
		}
		if got := stripANSIstr(m.renderSteer()); got != "" {
			t.Fatalf("no card from a stale ack, got:\n%s", got)
		}
	})
}

// TestSteer_TUIIgnoresStaleAck is R4-1: a sent steer carries a fresh client-minted
// message_id and the ui resolves acks/echoes BY ID — a stale ack (drained id) or
// an ack for one bundle while a NEW bundle is in flight must not touch the live
// steer lifecycle (text echo matching would resurrect old frames on duplicate
// texts; the id is the only safe correlation key).
func TestSteer_TUIIgnoresStaleAck(t *testing.T) {
	m, _ := newSteerModel(t, true)
	m = startRunning(t, m, "first")
	m = enqueueSteer(t, m, "first bundle")

	// Burn the first bundle: accepted ack, then the drain echo.
	mm, _ := m.Update(client.SteerOutcomeMsg{Outcome: client.SteerAccepted, Text: "first bundle", MessageID: "steer-0001"})
	m = mm.(Model)
	mm, _ = m.Update(client.SteerEchoMsg{Text: "first bundle", MessageID: "steer-0001"})
	m = mm.(Model)
	if m.steer != nil {
		t.Fatalf("the drain echo must clear the in-flight card, got %+v", m.steer)
	}

	// Send a SECOND bundle (same text shape, NEW id). A late ack carrying the STALE
	// (drained) id must NOT overwrite the live bundle's text or phase; an ack for
	// the live id advances it.
	m = enqueueSteer(t, m, "second bundle")
	mm2, _ := m.Update(client.SteerOutcomeMsg{Outcome: client.SteerAccepted, Text: "POISONED", MessageID: "steer-0001"})
	m = mm2.(Model)
	if m.steer == nil || m.steer.watermarkID() != "steer-0002" || m.steer.Text != "second bundle" || m.steer.Phase != steerPending {
		t.Fatalf("the stale-id ack must not touch the live bundle, got %+v", m.steer)
	}
	mm3, _ := m.Update(client.SteerOutcomeMsg{Outcome: client.SteerAccepted, Text: "second bundle", MessageID: "steer-0002"})
	m = mm3.(Model)
	if m.steer == nil || m.steer.Phase != steerSent {
		t.Fatalf("the live-id ack must advance the live bundle to sent, got %+v", m.steer)
	}

	// A stale drān echo for the OLD id likewise must not clear the live card.
	mm4, _ := m.Update(client.SteerEchoMsg{Text: "first bundle", MessageID: "steer-0001"})
	m = mm4.(Model)
	if m.steer == nil || m.steer.Phase != steerSent {
		t.Fatalf("the stale-id echo must not clear the live card, got %+v", m.steer)
	}
}

// TestSteer_TUIEditCancelThenRecompose is R4-2: ↑ on an in-flight steer CANCELS
// the outstanding bundle (exactly ONE steer_cancel carrying the bundle's
// message_id, fired WITHOUT waiting for the ack) and pulls the whole
// not-yet-drained text into the textarea as ONE editable blob; resend collapses
// to ONE new bundle with a NEW message_id. (Previously ↑ left the old frame live
// server-side and resend merged onto it — the edit-back duplication bug.)
func TestSteer_TUIEditCancelThenRecompose(t *testing.T) {
	m, conv := newSteerModel(t, true)
	m = startRunning(t, m, "first")
	m = enqueueSteer(t, m, "second")
	m = enqueueSteer(t, m, "third") // batch-newer-line: stages fresh, inbox full
	mm, _ := m.Update(client.SteerOutcomeMsg{Outcome: client.SteerAccepted, Text: "second", MessageID: "steer-0001"})
	m = mm.(Model)

	// ↑ with ONE staged line pending: it cancels the ACKED bundle and pulls the
	// whole not-yet-drained set into ONE editable blob (acked text + staged lines).
	mm2, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	m = mm2.(Model)
	runBatchLeaves(cmd)

	wantDraft := "second" + queueMergeSep + "third"
	if got := m.prompt.Value(); got != wantDraft {
		t.Fatalf("↑ must pull the whole not-yet-drained set into ONE editable blob, got %q, want %q", got, wantDraft)
	}
	// Exactly ONE steer_cancel fired; the outstanding (cancelled) bundle is the
	// SECOND — the merged re-collapse of the first frame plus the typed-while-batched
	// line — so the cancel carries its NEW id (steer-0002), the whole reason the
	// correlation is by id, not text.
	var cancels []string
	for _, fr := range conv.send.frames() {
		if sc := fr.GetSteerCancel(); sc != nil {
			cancels = append(cancels, sc.GetMessageId())
		}
	}
	if len(cancels) != 1 || cancels[0] != "steer-0002" {
		t.Fatalf("↑ must fire exactly ONE steer_cancel for the outstanding bundle's id, got %v", cancels)
	}
	// The old bundle is GONE client-side as a sendable frame: the card is empty
	// while the draft is being edited (the bundle is being rebuilt).
	if got := stripANSIstr(m.renderSteer()); got != "" {
		t.Fatalf("no sendable steer while the recomposed bundle is drafted, got:\n%s", got)
	}

	// Edit and resend: it collapses to ONE new bundle with a NEW id.
	m = typeText(t, m, " CHANGED")
	mm3, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = mm3.(Model)
	runBatchLeaves(cmd)
	got := steerFrames(conv.send)
	// Each send carries ONLY its own fragment under the ordered-queue model (the
	// engine appends fragments to the pending bundle; re-sending merged text
	// would re-append drained text — the duplication bug). The ↑ edit recomposed
	// the whole draft into ONE new fragment with a fresh id.
	want := []steerFrame{
		{text: "second", id: "steer-0001"},
		{text: "third", id: "steer-0002"},
		{text: "second" + queueMergeSep + "third CHANGED", id: "steer-0003"},
	}
	if len(got) != len(want) {
		t.Fatalf("steer frames = %+v, want the original fragments + ONE recomposed replacement", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("steer frame %d = %+v, want %+v (fragment sends; recompose collapses into ONE new fragment)", i, got[i], want[i])
		}
	}
	if m.steer == nil || m.steer.watermarkID() != "steer-0003" || m.steer.Phase != steerPending {
		t.Fatalf("the resent replacement is the ONE in-flight bundle with the new id, got %+v", m.steer)
	}
}

// TestSteer_TUIEditBackNoDuplicate is R4-3: resend after ↑ does NOT duplicate the
// old text. ↑ CANCELS the outstanding bundle, so the old text can never be merged
// onto the resend — the replacement frame is EXACTLY the edited blob (the old
// Contains-masked-duplication assertion is gone: this is exact equality over the
// full frame list). Pre-rework the second send merged the pulled-back text onto
// the still-live old frame, producing "first\n\nfirst edited".
func TestSteer_TUIEditBackNoDuplicate(t *testing.T) {
	m, conv := newSteerModel(t, true)
	m = startRunning(t, m, "first")
	m = enqueueSteer(t, m, "first")
	mm, _ := m.Update(client.SteerOutcomeMsg{Outcome: client.SteerAccepted, Text: "first", MessageID: "steer-0001"})
	m = mm.(Model)

	// ↑ pulls it back for editing (cancels the outstanding bundle).
	mm2, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	m = mm2.(Model)
	runBatchLeaves(cmd)
	if got := m.prompt.Value(); got != "first" {
		t.Fatalf("↑ must pull the in-flight steer back for editing, got %q", got)
	}

	// Edit + resend: THE replacement frame is exactly the edited blob — no merged
	// old+new duplication, and the old text does not ride the resend twice.
	m = typeText(t, m, " edited")
	mm3, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = mm3.(Model)
	runBatchLeaves(cmd)
	got := steerTexts(conv.send)
	want := []string{"first", "first edited"}
	if len(got) != len(want) {
		t.Fatalf("steer frames = %v, want exactly the original + the edited replacement", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("steer frame %d = %q, want %q EXACTLY (no edit-back duplication)", i, got[i], want[i])
		}
	}
}

// TestSteer_TUIBurnedIdFreshDraft is R4-4: a drained message_id is BURNED — ↑
// after the drain echo does NOT cancel (there is no pending bundle to retract) and
// does NOT pull the landed text; editing a shipped steer means typing a FRESH
// draft (resend is a NEW submission with its own new id). The landed line stays
// rendered in context and the "shipped" (landed-in-context) state is what the
// echo-implied transcript shows — the pending card has cleared because the drain
// echo rendered the line.
func TestSteer_TUIBurnedIdFreshDraft(t *testing.T) {
	m, conv := newSteerModel(t, true)
	m = startRunning(t, m, "first")
	m = enqueueSteer(t, m, "second")
	mm, _ := m.Update(client.SteerOutcomeMsg{Outcome: client.SteerAccepted, Text: "second", MessageID: "steer-0001"})
	m = mm.(Model)
	mm2, _ := m.Update(client.SteerEchoMsg{Text: "second", MessageID: "steer-0001"})
	m = mm2.(Model)
	if m.steer != nil {
		t.Fatalf("the drain echo clears the in-flight card, got %+v", m.steer)
	}

	// The "shipped" state: the queued text now lives IN CONTEXT in the transcript
	// (the landed line at the boundary), with no pending card.
	view := stripANSIstr(m.View().Content)
	if !strings.Contains(view, "second") {
		t.Fatalf("the shipped steer must be visible in context, got:\n%s", view)
	}

	// ↑ on an empty input AFTER the drain: no cancel frame (nothing pending) and
	// no pull-back (the landed text is burned — the shipped line is not re-editable).
	mm3, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	m = mm3.(Model)
	runBatchLeaves(cmd)
	if got := steerCancelCount(conv.send); got != 0 {
		t.Fatalf("↑ after drain must send NO steer_cancel (the id is burned), got %d", got)
	}
	if got := m.prompt.Value(); got != "" {
		t.Fatalf("↑ after drain must NOT pull the shipped text back (burned id), got %q", got)
	}

	// Typing a new prompt and resending opens a FRESH submission with its OWN id —
	// it is not a re-edit of the shipped one.
	m = typeText(t, m, "replacement")
	mm4, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = mm4.(Model)
	runBatchLeaves(cmd)
	got := steerFrames(conv.send)
	want := []steerFrame{
		{text: "second", id: "steer-0001"},
		{text: "replacement", id: "steer-0002"},
	}
	if len(got) != len(want) {
		t.Fatalf("steer frames = %+v, want the burned original + a fresh submission", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("steer frame %d = %+v, want %+v (post-drain resend = new submission, new id)", i, got[i], want[i])
		}
	}
}

// TestSteer_TUIQueuedUntilLanded is R4-5: the steer shows as a PENDING card at the
// bottom UNTIL the EvSteer drain echo lands, then the landed line appears IN
// CONTEXT at its true position (the echo is stream-positioned: it renders exactly
// where the drain committed it — not at a client-guessed position). The card
// clears the same render pass the echo projects the line; a stale (already-burned
// id) echo never duplicates the landed line.
func TestSteer_TUIQueuedUntilLanded(t *testing.T) {
	m, _ := newSteerModel(t, true)
	m = startRunning(t, m, "first")
	m = enqueueSteer(t, m, "check b.go too")

	// Pre-echo: the card is pending and the transcript does NOT yet carry the steer
	// (the card preview collapses multi-word text onto one line, so the in-context
	// occurrence check keys off the flat preview string, not the raw text).
	cardPreview := truncate(oneLine("check b.go too"), queuePreviewWidth)
	if got := stripANSIstr(m.renderSteer()); !strings.Contains(got, "sending") {
		t.Fatalf("before the echo the card shows the pending state, got:\n%s", got)
	}
	m.refreshView()
	before := stripANSIstr(m.View().Content)
	if n := strings.Count(before, cardPreview); n != 1 { // card preview only
		t.Fatalf("the pending steer must appear ONLY as the bottom card before the echo lands, found %d:\n%s", n, before)
	}

	// The accepted ack keeps the card (sent, still un-landed) — no context line yet.
	mm, _ := m.Update(client.SteerOutcomeMsg{Outcome: client.SteerAccepted, Text: "check b.go too", MessageID: "steer-0001"})
	m = mm.(Model)
	m.refreshView()
	mid := stripANSIstr(m.View().Content)
	if n := strings.Count(mid, cardPreview); n != 1 {
		t.Fatalf("the acked-but-unlanded steer must appear ONLY as the pending card (1 render), found %d:\n%s", n, mid)
	}

	// The drain echo lands: the card clears and the line appears in context (ONCE).
	mm2, _ := m.Update(client.SteerEchoMsg{Text: "check b.go too", MessageID: "steer-0001"})
	m = mm2.(Model)
	m.refreshView()
	after := stripANSIstr(m.View().Content)
	if n := strings.Count(after, "check b.go too"); n != 1 {
		t.Fatalf("after the echo the landed line appears in context EXACTLY once, found %d:\n%s", n, after)
	}
	if got := stripANSIstr(m.renderSteer()); got != "" {
		t.Fatalf("the echo clears the pending card in the same projection, got:\n%s", got)
	}

	// A duplicate echo for the same (burned) id never re-renders the line.
	mm3, _ := m.Update(client.SteerEchoMsg{Text: "check b.go too", MessageID: "steer-0001"})
	m = mm3.(Model)
	m.refreshView()
	again := stripANSIstr(m.View().Content)
	if n := strings.Count(again, "check b.go too"); n != 1 {
		t.Fatalf("a duplicate echo for a burned id must not duplicate the landed line, found %d:\n%s", n, again)
	}
}

// TestSteer_CardGolden locks the steer-mode card's frame (ANSI stripped): a run
// streaming with a sent (acked) steer in flight renders the "steer queued" card
// above the input, previewing the merged message as ONE item. This is the
// pixel-exact half of AC6.3's card (the behavioural half is
// TestSteer_TUIRendersAuthoritativeState).
func TestSteer_CardGolden(t *testing.T) {
	m, _ := newSteerModel(t, true)
	m = startRunning(t, m, "first")
	m = enqueueSteer(t, m, "also check b.go")
	mm, _ := m.Update(client.SteerOutcomeMsg{Outcome: client.SteerAccepted, Text: "also check b.go"})
	m = mm.(Model)
	m.refreshView()
	got := stripANSI([]byte(m.View().Content))
	compareGolden(t, "steer_card.golden", got)
}

// TestSteer_RunningSlashCommandsInterceptLocalBuiltins verifies that local bare
// built-ins keep their local meaning while a steer-capable run streams. Unknown
// slash commands remain model-facing so workspace commands still work.
func TestSteer_RunningSlashCommandsInterceptLocalBuiltins(t *testing.T) {
	t.Run("help stays local after a steer", func(t *testing.T) {
		m, conv := newSteerModel(t, true)
		m = startRunning(t, m, "first")

		m = typeText(t, m, "normal steer")
		mm, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
		runBatchLeaves(cmd) // execute the actual returned send command
		m = mm.(Model)
		if got := steerTexts(conv.send); len(got) != 1 || got[0] != "normal steer" {
			t.Fatalf("normal input must send one steer, got %v", got)
		}

		m = typeText(t, m, "  /help  ")
		mm, cmd = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
		runBatchLeaves(cmd) // execute the actual returned local-builtin command
		m = mm.(Model)
		if !m.showHelp {
			t.Fatal("bare /help must open local help while the run remains active")
		}
		if m.phase != phaseRunning {
			t.Fatalf("bare /help changed phase to %v, want running", m.phase)
		}
		if got := m.prompt.Value(); got != "" {
			t.Fatalf("bare /help must clear the input, got %q", got)
		}
		if got := steerTexts(conv.send); len(got) != 1 {
			t.Fatalf("bare /help must not send another steer, got %v", got)
		}
		if len(m.queued) != 0 {
			t.Fatalf("bare /help must not add a queued follow-up, got %v", m.queued)
		}
		if m.palette.open {
			t.Fatal("bare /help must close the command palette after consuming its input")
		}

		// Close the local overlay, then prove the original run and its pending steer
		// remain usable without cancelling either one.
		mm, cmd = m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
		runBatchLeaves(cmd)
		m = mm.(Model)
		m = typeText(t, m, "second steer")
		mm, cmd = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
		runBatchLeaves(cmd)
		m = mm.(Model)
		if got := steerTexts(conv.send); len(got) != 2 || got[1] != "second steer" {
			t.Fatalf("run must accept another steer after /help, got %v", got)
		}
		if got := steerCancelCount(conv.send); got != 0 {
			t.Fatalf("bare /help must not cancel the pending steer, sent %d steer_cancel frames", got)
		}
		if m.phase != phaseRunning {
			t.Fatalf("run stopped after /help and another steer: phase=%v", m.phase)
		}
	})

	t.Run("palette clear preserves an active steer", func(t *testing.T) {
		m, conv := newSteerModel(t, true)
		m = startRunning(t, m, "first")

		// Establish the active steer state before selecting the local command.
		m = enqueueSteer(t, m, "ordinary steer")
		if got := steerTexts(conv.send); len(got) != 1 || got[0] != "ordinary steer" {
			t.Fatalf("first ordinary input must send one steer, got %v", got)
		}
		if got := promptTexts(conv.send); len(got) != 1 || got[0] != "first" {
			t.Fatalf("running steers must not open prompts, got %v", got)
		}

		// Select /clear through the palette, rather than submitting a typed command.
		m = typeText(t, m, "/")
		if !m.palette.open || len(m.palette.filtered) == 0 || m.palette.filtered[0].Name != "clear" || !m.palette.filtered[0].Builtin {
			t.Fatalf("/ must select the local clear builtin in the palette, got %+v", m.palette)
		}
		mm, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
		runBatchLeaves(cmd)
		m = mm.(Model)

		if m.phase != phaseRunning || m.conv.isEmpty() {
			t.Fatal("palette /clear must not disrupt a running conversation")
		}
		if !strings.Contains(stripANSIstr(m.statusMsg), "cannot clear while running") {
			t.Fatalf("palette /clear status = %q, want running warning", m.statusMsg)
		}
		if m.palette.open || m.prompt.Value() != "" {
			t.Fatalf("palette /clear must close the palette and clear input, open=%t input=%q", m.palette.open, m.prompt.Value())
		}
		if got := steerTexts(conv.send); len(got) != 1 {
			t.Fatalf("palette /clear must not send another steer, got %v", got)
		}
		if got := promptTexts(conv.send); len(got) != 1 {
			t.Fatalf("palette /clear must not open a prompt, got %v", got)
		}
		if len(m.queued) != 0 {
			t.Fatalf("palette /clear must not add a queued follow-up, got %v", m.queued)
		}
		if got := steerCancelCount(conv.send); got != 0 {
			t.Fatalf("palette /clear must not cancel the active steer, sent %d steer_cancel frames", got)
		}

		m = enqueueSteer(t, m, "second ordinary steer")
		if got := steerTexts(conv.send); len(got) != 2 || got[1] != "second ordinary steer" {
			t.Fatalf("run must accept another steer after palette /clear, got %v", got)
		}
	})

	t.Run("clear is safe and unknown commands fall through", func(t *testing.T) {
		m, conv := newSteerModel(t, true)
		m = startRunning(t, m, "first")

		m = typeText(t, m, "/workspace-command")
		_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
		runBatchLeaves(cmd)
		if got := steerTexts(conv.send); len(got) != 1 || got[0] != "/workspace-command" {
			t.Fatalf("unknown workspace command must fall through to steer, got %v", got)
		}
	})
}
