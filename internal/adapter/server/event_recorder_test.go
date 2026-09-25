package server

import (
	"context"
	"errors"
	"iter"
	"strings"
	"testing"
	"testing/synctest"
	"time"
	"unicode/utf8"

	"github.com/stacklok/mecatl/engine/adapter/eventsource"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
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

func TestRunEventRecorderPersistsNetworkAttemptPayload(t *testing.T) {
	log := &countingEventLog{}
	recorder := NewRunEventRecorder(context.Background(), recorderService(log, port.NopDiagnostics{}), "s1")
	payload := session.NetworkAttemptPayload{SessionID: "s1", RunSerial: 3, Turn: 2, Attempt: 1, MaxAttempts: 2, RetryDisposition: "retryable", StreamProgress: "precommit", Decision: "retry", FailureClass: "connect"}
	recorder.Observe(session.Event{Type: session.EvNetworkAttempt, Turn: 2, Seq: 4, NetworkAttempt: &payload})
	recorder.Close()

	if len(log.recorded) != 1 || log.recorded[0].Type != session.EvNetworkAttempt || log.recorded[0].NetworkAttempt == nil || log.recorded[0].NetworkAttempt.SessionID != "s1" {
		t.Fatalf("recorded network attempt = %+v", log.recorded)
	}
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

func TestRunEventRecorderCoalescingRetainsFirstDeltaSequenceAcrossLaterRecordGap(t *testing.T) {
	log := &countingEventLog{}
	recorder := NewRunEventRecorder(context.Background(), recorderService(log, port.NopDiagnostics{}), "s1")

	recorder.Observe(session.Event{Type: session.EvTurnStart, Turn: 0, Seq: 1})
	recorder.Observe(session.Event{Type: session.EvMessageDelta, Turn: 0, Seq: 5, Text: "coalesced"})
	recorder.Observe(session.Event{Type: session.EvProviderRoute, Turn: 0, Seq: 28, Text: "OpenRouter"})
	recorder.Observe(session.Event{Type: session.EvResult, Turn: 0, Seq: 29, Result: &session.ResultPayload{Stop: session.StopEndTurn}})
	recorder.Close()

	if got, want := len(log.recorded), 4; got != want {
		t.Fatalf("recorded events = %d, want %d: %+v", got, want, log.recorded)
	}
	for i, want := range []int64{1, 5, 28, 29} {
		if got := log.recorded[i].Seq; got != want {
			t.Fatalf("recorded[%d].Seq = %d, want %d", i, got, want)
		}
	}
	if got := log.recorded[1]; got.Type != session.EvMessageDelta || got.Text != "coalesced" {
		t.Fatalf("coalesced delta = %+v", got)
	}
	if got := log.recorded[2]; got.Type != session.EvProviderRoute || got.Text != "OpenRouter" {
		t.Fatalf("later provider route = %+v", got)
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
				ID: "s1", Mode: session.ModeDefault, EnvironmentRef: session.EnvironmentRef{Kind: session.EnvKindLocal, ID: ".", Revision: "in-tree-v1"}, CreatedAt: time.Unix(1, 0),
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
		ID: "s1", Mode: session.ModeDefault, EnvironmentRef: session.EnvironmentRef{Kind: session.EnvKindLocal, ID: ".", Revision: "in-tree-v1"}, CreatedAt: time.Unix(1, 0),
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

type blockedAdmissionEventLog struct {
	countingEventLog
	entered chan struct{}
	release chan struct{}
}

func (l *blockedAdmissionEventLog) Append(ctx context.Context, id session.SessionID, ev session.Event) error {
	close(l.entered)
	<-l.release
	return l.countingEventLog.Append(ctx, id, ev)
}

func TestRunEventRecorderBlockedAppendDoesNotBlockPersistOrDrain(t *testing.T) {
	for _, tc := range []struct {
		name         string
		event        session.Event
		wantPreserve bool
	}{
		{name: "cancelled terminal", event: session.Event{Type: session.EvResult, Result: &session.ResultPayload{Stop: session.StopCancelled}}},
		{name: "plan terminal", event: session.Event{Type: session.EvResult, Result: &session.ResultPayload{Stop: session.StopPlanApproved}}},
		{name: "matching retraction", event: session.Event{Type: session.EvPermissionRetract, Ask: &session.PendingAsk{AskID: "matching"}}},
		{name: "unrelated retraction", event: session.Event{Type: session.EvPermissionRetract, Ask: &session.PendingAsk{AskID: "other"}}, wantPreserve: true},
		{name: "other run terminal", event: session.Event{Type: session.EvResult, RunID: "other", Result: &session.ResultPayload{Stop: session.StopCancelled}}, wantPreserve: true},
		{name: "unrelated event", event: session.Event{Type: session.EvHook}, wantPreserve: true},
	} {
		for _, postWriteError := range []bool{false, true} {
			name := tc.name + "/success"
			if postWriteError {
				name = tc.name + "/ambiguous append failure"
			}
			t.Run(name, func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					id := session.SessionID("blocked-append")
					sess, run, askID := liveAwaitingControlRun(t, id)
					defer func() {
						run.Cancel()
						for range run.Events() {
						}
					}()
					store := memstore.New()
					if err := store.Save(t.Context(), sess); err != nil {
						t.Fatal(err)
					}
					log := &blockedAdmissionEventLog{
						countingEventLog: countingEventLog{postWriteFailCalls: map[int]bool{1: postWriteError}},
						entered:          make(chan struct{}), release: make(chan struct{}),
					}
					diag := &countingDiagnostics{}
					st := &runState{run: run, sess: sess, settled: make(chan struct{}), titleRevision: sess.TitleRevision}
					svc := &Service{
						cfg:  Config{Store: store, EventLog: log, Diagnostics: diag, MutationCapability: NewSessionMutationCapability(false)},
						runs: map[session.SessionID]*runState{id: st},
					}
					svc.Persist(t.Context(), id)
					if !st.awaiting.Load() || st.awaitingAskID != askID {
						t.Fatal("fixture did not persist the exact awaiting handoff")
					}
					ev := tc.event
					if ev.RunID == "" {
						ev.RunID = run.RunID()
					}
					if ev.Ask != nil && ev.Ask.AskID == "matching" {
						ev.Ask = &session.PendingAsk{AskID: askID}
					}
					ctx := session.WithPrincipal(t.Context(), &session.Principal{Subject: "caller", Issuer: "test"})
					recorder := NewRunEventRecorder(ctx, svc, id)
					appended := make(chan struct{})
					go func() {
						recorder.Observe(ev)
						recorder.Close()
						close(appended)
					}()
					defer func() {
						close(log.release)
						<-appended
						if len(log.attempts) != 1 || len(log.recorded) != 1 {
							t.Errorf("attempts/records = %d/%d, want 1/1 even on ambiguous failure", len(log.attempts), len(log.recorded))
						} else if actor := log.recorded[0].Actor; actor == nil || actor.Subject != "caller" || actor.Issuer != "test" {
							t.Errorf("actor = %+v, want verified caller", actor)
						}
						wantWarnings := 0
						if postWriteError {
							wantWarnings = 1
						}
						if diag.warnings != wantWarnings {
							t.Errorf("warnings = %d, want %d; append failure must not be hidden by freeze", diag.warnings, wantWarnings)
						}
					}()
					<-log.entered
					synctest.Wait()
					if st.cancelSignaled {
						t.Fatal("event admission fabricated an owner cancellation")
					}
					persistedAndFrozen := make(chan struct{})
					go func() {
						svc.Persist(t.Context(), id)
						svc.snapshotDrainState()
						close(persistedAndFrozen)
					}()
					synctest.Wait()
					select {
					case <-persistedAndFrozen:
					default:
						t.Fatal("blocked EventLog.Append holds the Persist/drain lifecycle barrier")
					}
					if got := st.preserveDurable.Load(); got != tc.wantPreserve {
						t.Fatalf("preserved handoff = %t, want %t after event admission", got, tc.wantPreserve)
					}
				})
			})
		}
	}
}

func TestRunEventRecorderShutdownProjectionIsLifecycleScoped(t *testing.T) {
	t.Run("owner cancellation remains durable", func(t *testing.T) {
		id := session.SessionID("owner-cancel")
		sess, run, askID := liveAwaitingControlRun(t, id)
		log := memstore.NewEventLog()
		st := &runState{run: run, sess: sess, settled: make(chan struct{}), awaitingAskID: askID}
		st.awaiting.Store(true)
		svc := &Service{
			cfg:  Config{EventLog: log, Diagnostics: port.NopDiagnostics{}},
			runs: map[session.SessionID]*runState{id: st},
		}
		recorder := NewRunEventRecorder(context.Background(), svc, id)
		recorder.Observe(session.Event{Type: session.EvPermissionAsk, RunID: run.RunID(), Ask: &session.PendingAsk{AskID: askID}})
		if err := svc.cancelLiveRun(id, run, run.RunID()); err != nil {
			t.Fatal(err)
		}
		svc.snapshotDrainState()
		for ev := range run.Events() {
			recorder.Observe(ev)
		}
		recorder.Close()

		var cancelled bool
		for ev, err := range log.Read(context.Background(), id) {
			if err != nil {
				t.Fatal(err)
			}
			if ev.Type == session.EvResult && ev.Result != nil && ev.Result.Stop == session.StopCancelled {
				cancelled = true
			}
		}
		if !cancelled {
			t.Fatal("owner cancellation was suppressed by later shutdown freeze")
		}
	})

	t.Run("preserved shutdown omits only matching cancellation records", func(t *testing.T) {
		id := session.SessionID("shutdown-preserved")
		sess, run, askID := liveAwaitingControlRun(t, id)
		defer func() {
			run.Cancel()
			for range run.Events() {
			}
		}()
		log := memstore.NewEventLog()
		st := &runState{run: run, sess: sess, settled: make(chan struct{}), awaitingAskID: askID}
		st.awaiting.Store(true)
		svc := &Service{
			cfg:  Config{EventLog: log, Diagnostics: port.NopDiagnostics{}},
			runs: map[session.SessionID]*runState{id: st},
		}
		svc.snapshotDrainState()
		recorder := NewRunEventRecorder(context.Background(), svc, id)
		recorder.Observe(session.Event{Type: session.EvHook, RunID: run.RunID(), Text: "unrelated"})
		recorder.Observe(session.Event{Type: session.EvPermissionRetract, RunID: run.RunID(), Ask: &session.PendingAsk{AskID: "other-ask"}})
		recorder.Observe(session.Event{Type: session.EvPermissionRetract, RunID: run.RunID(), Ask: &session.PendingAsk{AskID: askID}})
		recorder.Observe(session.Event{Type: session.EvResult, RunID: run.RunID(), Result: &session.ResultPayload{Stop: session.StopCancelled}})
		recorder.Close()

		var kinds []session.EventType
		for ev, err := range log.Read(context.Background(), id) {
			if err != nil {
				t.Fatal(err)
			}
			kinds = append(kinds, ev.Type)
			if ev.Type == session.EvPermissionRetract && (ev.Ask == nil || ev.Ask.AskID != "other-ask") {
				t.Fatalf("recorded retraction = %+v, want only unrelated ask", ev.Ask)
			}
		}
		if len(kinds) != 2 || kinds[0] != session.EvHook || kinds[1] != session.EvPermissionRetract {
			t.Fatalf("durable kinds = %v, want unrelated hook and retraction", kinds)
		}
	})

	t.Run("failed awaiting save does not suppress cancellation", func(t *testing.T) {
		id := session.SessionID("awaiting-save-failed")
		sess, run, _ := liveAwaitingControlRun(t, id)
		defer func() {
			run.Cancel()
			for range run.Events() {
			}
		}()
		log := memstore.NewEventLog()
		svc := &Service{
			cfg:  Config{EventLog: log, Diagnostics: port.NopDiagnostics{}},
			runs: map[session.SessionID]*runState{id: {run: run, sess: sess, settled: make(chan struct{})}},
		}
		svc.snapshotDrainState()
		recorder := NewRunEventRecorder(context.Background(), svc, id)
		recorder.Observe(session.Event{Type: session.EvResult, RunID: run.RunID(), Result: &session.ResultPayload{Stop: session.StopCancelled}})
		recorder.Close()

		for ev, err := range log.Read(context.Background(), id) {
			if err != nil {
				t.Fatal(err)
			}
			if ev.Type == session.EvResult && ev.Result != nil && ev.Result.Stop == session.StopCancelled {
				return
			}
		}
		t.Fatal("cancellation after failed awaiting save was suppressed")
	})
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
		ID: "s1", Mode: session.ModeDefault, EnvironmentRef: session.EnvironmentRef{Kind: session.EnvKindLocal, ID: ".", Revision: "in-tree-v1"}, CreatedAt: time.Unix(1, 0),
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
