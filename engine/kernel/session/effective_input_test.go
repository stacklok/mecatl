package session_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/history/mem"
	"github.com/stacklok/mecatl/engine/kernel/codec"
	"github.com/stacklok/mecatl/engine/kernel/history"
	historyid "github.com/stacklok/mecatl/engine/kernel/history/id"
	"github.com/stacklok/mecatl/engine/kernel/session"
	sessionformat "github.com/stacklok/mecatl/engine/kernel/session/format"
)

func TestEffectiveInputRelativeReferences(t *testing.T) {
	run := allocatedRun(t, testHistoryID(t), 1)
	foreign, err := historyid.ParseHistoryID("HST2222222222222222222222")
	mustBatch(t, err)
	for _, id := range []session.UserInputID{
		allocatedInput(t, allocatedRun(t, foreign, 1), 1),
		allocatedInput(t, allocatedRun(t, run.HistoryID(), 2), 1), {},
	} {
		v := session.EffectiveInput{Inputs: []session.InputItem{{UserInputID: new(id.RunRelative())}}}
		stored := v
		stored.RunID = run.HistoryRelative()
		if (stored.Validate() == nil) != (id.RunRelative() != 0) {
			t.Fatal("relative reference validation")
		}
		clone := v.Clone()
		*clone.Inputs[0].UserInputID++
		if clone.Equal(v) || *v.Inputs[0].UserInputID != id.RunRelative() {
			t.Fatal("relative reference clone aliases or equality lost component")
		}
		_, b := allocationBatch(t, run)
		batchInput(t, b, run, nil)
		err := b.AddEffectiveInput(run, v)
		if id.RunRelative() == 0 {
			if !errors.Is(err, codec.ErrInvalidData) {
				t.Fatalf("zero reference: %v", err)
			}
		} else {
			mustBatch(t, err)
		}
		mustBatch(t, b.Discard())
	}
}

func TestEffectiveInputExternalRunAdmission(t *testing.T) {
	for _, state := range []string{"uncreated", "idle", "active", "interrupted", "successor"} {
		t.Run(state, func(t *testing.T) {
			backend := &batchBackend{Backend: mem.New()}
			h := newLifecycleHistory(t, backend)
			s := boundSession(t, h, sessionPolicy())
			run := allocatedRun(t, s.Snapshot().HistoryID(), 1)
			if state != "uncreated" {
				b := beginBatch(t, s)
				mustBatch(t, b.Create())
				if state != "idle" {
					if got := batchRun(t, b); got != run {
						t.Fatal("run identity")
					}
					if state == "interrupted" || state == "successor" {
						mustBatch(t, b.EndRun(session.RunEndedInterrupted))
					}
					if state == "successor" {
						batchRun(t, b)
					}
				}
				submitBatch(t, s, b)
			}
			v := session.EffectiveInput{Inputs: []session.InputItem{{Inline: &session.AttributedInput{Actor: "arbitrary origin", Purpose: session.PromptContext, Source: session.ContentSource{Description: "explicit empty contribution"}}}}}
			r := encodeFixture(t, sessionformat.KindEffectiveInput, sessionformat.EffectiveInput{RunID: 1, Inputs: []sessionformat.InputItem{{Inline: &sessionformat.AttributedInput{Actor: "arbitrary origin", Purpose: "context", Source: sessionformat.ContentSource{Description: "explicit empty contribution"}}}}})
			c := proposedCommit(t, s, r)
			before := captureRejectedSnapshot(s.Snapshot())
			attempts := len(backend.attempts)
			independent, err := loadFixtureCommit(t, h, s, c, sessionPolicy())
			if state == "active" {
				mustBatch(t, err)
				got, ok := independent.Snapshot().RecordAt(2).EffectiveInput()
				if !ok || !effectiveMatches(got, v) {
					t.Fatal("inline-only Load changed contribution")
				}
			} else if independent != nil || !errors.Is(err, session.ErrTransition) {
				t.Fatalf("invalid run Load returned partial state: %v", err)
			}
			if len(backend.attempts) != attempts || !reflect.DeepEqual(before, captureRejectedSnapshot(s.Snapshot())) {
				t.Fatal("independent Load changed source session or storage")
			}
			result, err := h.Append(context.Background(), run.HistoryID(), c.Commit)
			mustBatch(t, err)
			if result.Status != history.StatusAccepted {
				t.Fatal("external append")
			}
			loaded, err := session.Load(context.Background(), h, run.HistoryID(), sessionPolicy())
			if state == "active" {
				mustBatch(t, err)
				if !reflect.DeepEqual(independent.Snapshot(), loaded.Snapshot()) {
					t.Fatal("inline-only replay changed history")
				}
			} else if loaded != nil || !errors.Is(err, session.ErrTransition) {
				t.Fatalf("invalid run Load: %v, %v", loaded, err)
			}
			if len(backend.attempts) != attempts+1 {
				t.Fatal("Load appended")
			}
		})
	}
}
