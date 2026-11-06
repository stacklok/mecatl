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
)

func TestEffectiveInputScopeConversion(t *testing.T) {
	run := session.RunID{HistoryID: testHistoryID(t), Ordinal: 1}
	foreign, err := historyid.ParseHistoryID("HST2222222222222222222222")
	mustBatch(t, err)
	for _, id := range []session.UserInputID{
		{RunID: session.RunID{HistoryID: foreign, Ordinal: 1}, Ordinal: 1},
		{RunID: session.RunID{HistoryID: run.HistoryID, Ordinal: 2}, Ordinal: 1}, {},
	} {
		v := session.EffectiveInput{RunID: run, Inputs: []session.InputItem{{UserInput: &id}}}
		if v.Validate() == nil {
			t.Fatal("conflicting full scope validated")
		}
		clone := v.Clone()
		if !clone.Equal(v) {
			t.Fatal("clone silently discarded scope")
		}
		other := v.Clone()
		other.Inputs[0].UserInput.RunID = run
		if v.Equal(other) {
			t.Fatal("equality discarded reference scope")
		}
		if r, err := session.NewEffectiveInputRecord(v, testLimits()); !errors.Is(err, codec.ErrInvalidData) || !reflect.DeepEqual(r, session.Record{}) {
			t.Fatalf("conversion discarded scope: %v", err)
		}
	}
}

func TestEffectiveInputExternalRunAdmission(t *testing.T) {
	for _, state := range []string{"uncreated", "idle", "active", "interrupted", "successor"} {
		t.Run(state, func(t *testing.T) {
			backend := &batchBackend{Backend: mem.New()}
			h := newLifecycleHistory(t, backend)
			s := boundSession(t, h, sessionPolicy())
			run := session.RunID{HistoryID: s.Snapshot().HistoryID(), Ordinal: 1}
			if state != "uncreated" {
				b := beginBatch(t, s)
				mustBatch(t, b.Create())
				if state != "idle" {
					if got := batchRun(t, b); got != run {
						t.Fatal("run identity")
					}
					if state == "interrupted" || state == "successor" {
						mustBatch(t, b.Interrupt())
					}
					if state == "successor" {
						batchRun(t, b)
					}
				}
				submitBatch(t, s, b)
			}
			v := session.EffectiveInput{RunID: run, Inputs: []session.InputItem{{Inline: &session.AttributedInput{Actor: "arbitrary origin", Purpose: session.PromptContext, Source: session.ContentSource{Description: "explicit empty contribution"}}}}}
			r, err := session.NewEffectiveInputRecord(v, testLimits())
			mustBatch(t, err)
			r, err = session.DecodeRecord(run.HistoryID, r.Format(), testLimits())
			mustBatch(t, err)
			c := proposedCommit(t, s, r)
			before := captureRejectedSnapshot(s.Snapshot())
			attempts := len(backend.attempts)
			err = s.Apply(c)
			if state == "active" {
				mustBatch(t, err)
				got, ok := s.Snapshot().RecordAt(2).EffectiveInput()
				if !ok || !effectiveMatches(got, v) {
					t.Fatal("inline-only Apply changed contribution")
				}
			} else if !errors.Is(err, session.ErrTransition) || !reflect.DeepEqual(before, captureRejectedSnapshot(s.Snapshot())) {
				t.Fatalf("invalid run Apply published: %v", err)
			}
			if len(backend.attempts) != attempts {
				t.Fatal("Apply appended")
			}
			result, err := h.Append(context.Background(), run.HistoryID, c.Commit)
			mustBatch(t, err)
			if result.Status != history.StatusAccepted {
				t.Fatal("external append")
			}
			loaded, err := session.Load(context.Background(), h, run.HistoryID, sessionPolicy())
			if state == "active" {
				mustBatch(t, err)
				if !reflect.DeepEqual(s.Snapshot(), loaded.Snapshot()) {
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
