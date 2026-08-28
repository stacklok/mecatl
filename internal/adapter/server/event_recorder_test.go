package server

import (
	"context"
	"errors"
	"iter"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/stacklok/mecatl/engine/adapter/eventsource"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/store/jsonlstore"
)

type countingEventLog struct {
	attempts           []session.Event
	recorded           []session.Event
	failCalls          map[int]bool
	postWriteFailCalls map[int]bool
}

func (l *countingEventLog) Append(_ context.Context, _ session.SessionID, ev session.Event) error {
	l.attempts = append(l.attempts, ev)
	if l.failCalls[len(l.attempts)] {
		return errors.New("append failed")
	}
	l.recorded = append(l.recorded, ev)
	if l.postWriteFailCalls[len(l.attempts)] {
		return errors.New("sync failed after durable write")
	}
	return nil
}

func (l *countingEventLog) Read(_ context.Context, _ session.SessionID) iter.Seq2[session.Event, error] {
	return func(yield func(session.Event, error) bool) {
		for _, ev := range l.recorded {
			if !yield(ev, nil) {
				return
			}
		}
	}
}

type countingDiagnostics struct{ warnings int }

func (d *countingDiagnostics) Log(_ context.Context, level port.Level, _ string, _ ...any) {
	if level == port.LevelWarn {
		d.warnings++
	}
}

func (d *countingDiagnostics) With(...any) port.Diagnostics { return d }

func recorderService(log port.EventLog, diag port.Diagnostics) *Service {
	return &Service{cfg: Config{EventLog: log, Diagnostics: diag}}
}

func TestRunEventRecorderCoalescesDeltasInFirstObservedOrder(t *testing.T) {
	log := &countingEventLog{}
	recorder := NewRunEventRecorder(context.Background(), recorderService(log, port.NopDiagnostics{}), "s1")

	recorder.Observe(session.Event{Type: session.EvTurnStart, Turn: 3, Seq: 1})
	recorder.Observe(session.Event{Type: session.EvReasoningDelta, Turn: 3, Seq: 2, Text: "reason "})
	for i := 0; i < 100; i++ {
		recorder.Observe(session.Event{Type: session.EvMessageDelta, Turn: 3, Seq: int64(3 + i), Text: "x"})
	}
	recorder.Observe(session.Event{Type: session.EvReasoningDelta, Turn: 3, Seq: 103, Text: "done"})
	recorder.Observe(session.Event{Type: session.EvTurnEnd, Turn: 3, Seq: 104})
	recorder.Close()

	if got := len(log.recorded); got != 4 {
		t.Fatalf("Append calls = %d, want 4", got)
	}
	if log.recorded[1].Type != session.EvReasoningDelta || log.recorded[1].Text != "reason done" {
		t.Fatalf("first coalesced delta = %+v, want reasoning %q", log.recorded[1], "reason done")
	}
	if log.recorded[2].Type != session.EvMessageDelta || log.recorded[2].Text != strings.Repeat("x", 100) {
		t.Fatalf("second coalesced delta = (%s, %d bytes), want message delta with 100 bytes", log.recorded[2].Type, len(log.recorded[2].Text))
	}
	if log.recorded[3].Type != session.EvTurnEnd {
		t.Fatalf("boundary = %s, want %s", log.recorded[3].Type, session.EvTurnEnd)
	}
}

func TestRunEventRecorderThresholdFlushPreservesFirstObservedKindOrder(t *testing.T) {
	large := strings.Repeat("x", maxCoalescedTextBytes+1)
	for _, tc := range []struct {
		name        string
		first       session.Event
		crossing    session.Event
		wantKinds   []session.EventType
		wantMessage string
		wantReason  string
	}{
		{
			name:        "reasoning before oversized message",
			first:       session.Event{Type: session.EvReasoningDelta, Turn: 1, Text: "older reasoning"},
			crossing:    session.Event{Type: session.EvMessageDelta, Turn: 1, Text: large},
			wantKinds:   []session.EventType{session.EvReasoningDelta, session.EvMessageDelta, session.EvMessageDelta, session.EvResult},
			wantMessage: large,
			wantReason:  "older reasoning",
		},
		{
			name:        "message before oversized reasoning",
			first:       session.Event{Type: session.EvMessageDelta, Turn: 1, Text: "older message"},
			crossing:    session.Event{Type: session.EvReasoningDelta, Turn: 1, Text: large},
			wantKinds:   []session.EventType{session.EvMessageDelta, session.EvReasoningDelta, session.EvReasoningDelta, session.EvResult},
			wantMessage: "older message",
			wantReason:  large,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			log := &countingEventLog{}
			recorder := NewRunEventRecorder(context.Background(), recorderService(log, port.NopDiagnostics{}), "s1")
			recorder.Observe(tc.first)
			recorder.Observe(tc.crossing)
			recorder.Observe(session.Event{Type: session.EvResult, Turn: 1, Result: &session.ResultPayload{Stop: session.StopEndTurn}})
			recorder.Close()

			if len(log.recorded) != len(tc.wantKinds) {
				t.Fatalf("recorded %d events, want %d: %+v", len(log.recorded), len(tc.wantKinds), log.recorded)
			}
			var message, reasoning string
			for i, ev := range log.recorded {
				if ev.Type != tc.wantKinds[i] {
					t.Fatalf("event[%d] kind = %s, want %s", i, ev.Type, tc.wantKinds[i])
				}
				switch ev.Type {
				case session.EvMessageDelta:
					message += ev.Text
				case session.EvReasoningDelta:
					reasoning += ev.Text
				}
			}
			if message != tc.wantMessage || reasoning != tc.wantReason {
				t.Fatalf("fold inputs = message %d bytes, reasoning %d bytes; want %d and %d", len(message), len(reasoning), len(tc.wantMessage), len(tc.wantReason))
			}

			folded, err := eventsource.Fold(eventsource.SessionMeta{
				ID: "s1", Mode: session.ModeDefault, Workspace: ".", CreatedAt: time.Unix(1, 0),
			}, log.Read(context.Background(), "s1"))
			if err != nil {
				t.Fatalf("Fold: %v", err)
			}
			if len(folded.Conversation.Messages) != 1 || folded.Conversation.Messages[0].Text != tc.wantMessage {
				t.Fatalf("folded messages = %+v, want exact message text of %d bytes", folded.Conversation.Messages, len(tc.wantMessage))
			}
		})
	}
}

func TestRunEventRecorderFlushesOnTurnChangeAndClose(t *testing.T) {
	log := &countingEventLog{}
	recorder := NewRunEventRecorder(context.Background(), recorderService(log, port.NopDiagnostics{}), "s1")
	recorder.Observe(session.Event{Type: session.EvMessageDelta, Turn: 1, Text: "one"})
	recorder.Observe(session.Event{Type: session.EvMessageDelta, Turn: 2, Text: "two"})
	if len(log.recorded) != 1 || log.recorded[0].Text != "one" {
		t.Fatalf("turn change recorded = %+v, want flushed turn-one delta", log.recorded)
	}
	recorder.Close()
	if len(log.recorded) != 2 || log.recorded[1].Text != "two" {
		t.Fatalf("close recorded = %+v, want final turn-two delta", log.recorded)
	}
}

func TestRunEventRecorderPostWriteErrorIsNotRetriedAndLaterEventsContinue(t *testing.T) {
	log := &countingEventLog{postWriteFailCalls: map[int]bool{1: true}}
	diag := &countingDiagnostics{}
	ctx := session.WithPrincipal(context.Background(), &session.Principal{Subject: "alice", Issuer: "test"})
	recorder := NewRunEventRecorder(ctx, recorderService(log, diag), "s1")

	recorder.Observe(session.Event{Type: session.EvMessageDelta, Text: "partial"})
	recorder.Observe(session.Event{Type: session.EvTurnEnd})
	recorder.Observe(session.Event{Type: session.EvResult, Result: &session.ResultPayload{Stop: session.StopEndTurn, Text: "partial"}})
	recorder.Close()

	if len(log.attempts) != 3 {
		t.Fatalf("Append attempts = %d, want delta, boundary, and result once each", len(log.attempts))
	}
	if diag.warnings != 1 {
		t.Fatalf("warnings = %d, want sticky one-per-recorder warning", diag.warnings)
	}
	if len(log.recorded) != 3 {
		t.Fatalf("durable records = %+v, want three non-duplicated events", log.recorded)
	}
	for i, want := range []session.EventType{session.EvMessageDelta, session.EvTurnEnd, session.EvResult} {
		if log.attempts[i].Type != want || log.recorded[i].Type != want {
			t.Fatalf("event[%d] attempted=%s recorded=%s, want %s", i, log.attempts[i].Type, log.recorded[i].Type, want)
		}
		if log.attempts[i].Actor == nil || log.attempts[i].Actor.Subject != "alice" || log.attempts[i].Actor.Issuer != "test" {
			t.Fatalf("attempt[%d] actor = %+v, want test:alice", i, log.attempts[i].Actor)
		}
	}
	folded, err := eventsource.Fold(eventsource.SessionMeta{
		ID: "s1", Mode: session.ModeDefault, Workspace: ".", CreatedAt: time.Unix(1, 0),
	}, log.Read(context.Background(), "s1"))
	if err != nil {
		t.Fatalf("Fold: %v", err)
	}
	if len(folded.Conversation.Messages) != 1 || folded.Conversation.Messages[0].Text != "partial" {
		t.Fatalf("folded conversation = %+v, want one non-duplicated partial message", folded.Conversation.Messages)
	}
}

func TestRunEventRecorderDrainToDiscardStillObserves(t *testing.T) {
	log := &countingEventLog{}
	svc := recorderService(log, port.NopDiagnostics{})
	recorder := NewRunEventRecorder(context.Background(), svc, "s1")
	h := &HarnessServer{svc: svc}
	rl := &runRelay{id: "s1", sendErr: errors.New("client gone"), recorder: recorder}

	h.sendEvent(rl, session.Event{Type: session.EvMessageDelta, Text: "tail"})
	h.sendEvent(rl, session.Event{Type: session.EvResult, Result: &session.ResultPayload{Stop: session.StopCancelled}})
	recorder.Close()

	if len(log.recorded) != 2 || log.recorded[0].Text != "tail" || log.recorded[1].Type != session.EvResult {
		t.Fatalf("drain records = %+v, want coalesced tail then terminal result", log.recorded)
	}
}

func TestRunEventRecorderOversizedEscapedUTF8RoundTripsThroughJSONL(t *testing.T) {
	log, err := jsonlstore.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	recorder := NewRunEventRecorder(context.Background(), recorderService(log, port.NopDiagnostics{}), "s1")
	unit := "\x00\n\t\"\\雪"
	want := strings.Repeat(unit, maxCoalescedTextBytes/len(unit)+100_000)

	// Deliberately split inside neither the logical text nor its UTF-8 runes; the
	// recorder itself must choose bounded, rune-safe record boundaries.
	for offset := 0; offset < len(want); {
		n := min(8191, len(want)-offset)
		for n > 0 && offset+n < len(want) && !utf8.RuneStart(want[offset+n]) {
			n--
		}
		recorder.Observe(session.Event{Type: session.EvMessageDelta, Turn: 0, Text: want[offset : offset+n]})
		offset += n
	}
	recorder.Observe(session.Event{Type: session.EvResult, Turn: 0, Result: &session.ResultPayload{Stop: session.StopEndTurn, Text: want}})
	recorder.Close()

	var chunks []string
	for ev, readErr := range log.Read(context.Background(), "s1") {
		if readErr != nil {
			t.Fatalf("EventLog.Read: %v", readErr)
		}
		if ev.Type == session.EvMessageDelta {
			if len(ev.Text) > maxCoalescedTextBytes || !utf8.ValidString(ev.Text) {
				t.Fatalf("invalid chunk: bytes=%d utf8=%v", len(ev.Text), utf8.ValidString(ev.Text))
			}
			chunks = append(chunks, ev.Text)
		}
	}
	if got := strings.Join(chunks, ""); got != want {
		t.Fatalf("coalesced text bytes = %d, want exact %d-byte text", len(got), len(want))
	}
	if got, minimum := len(chunks), (len(want)+maxCoalescedTextBytes-1)/maxCoalescedTextBytes; got != minimum {
		t.Fatalf("delta records = %d, want minimum bounded count %d", got, minimum)
	}

	folded, err := eventsource.Fold(eventsource.SessionMeta{
		ID: "s1", Mode: session.ModeDefault, Workspace: ".", CreatedAt: time.Unix(1, 0),
	}, log.Read(context.Background(), "s1"))
	if err != nil {
		t.Fatalf("Fold: %v", err)
	}
	if len(folded.Conversation.Messages) != 1 || folded.Conversation.Messages[0].Text != want {
		gotBytes := 0
		if len(folded.Conversation.Messages) > 0 {
			gotBytes = len(folded.Conversation.Messages[0].Text)
		}
		t.Fatalf("folded conversation has %d messages / %d text bytes, want one exact %d-byte message",
			len(folded.Conversation.Messages), gotBytes, len(want))
	}
}
