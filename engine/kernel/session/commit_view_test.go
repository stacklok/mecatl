package session_test

import (
	"bytes"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/stacklok/mecatl/engine/kernel/codec"
	"github.com/stacklok/mecatl/engine/kernel/session"
)

func captureCommit(t testing.TB, b *session.SessionCommit) session.CommitView {
	t.Helper()
	v, err := b.View()
	mustBatch(t, err)
	return v
}

func TestCommitViewPinnedSegmentsAndLifetime(t *testing.T) {
	for _, closeBatch := range []string{"Submit", "Discard"} {
		t.Run(closeBatch, func(t *testing.T) {
			h, s, p := newIteratorSession(t)
			b := beginBatch(t, s)
			empty := captureCommit(t, b)
			if empty.HistoryID() != s.Snapshot().HistoryID() || empty.RecordCount() != 0 || empty.ActiveRun() != (session.RunID{}) {
				t.Fatal("uncreated capture")
			}
			mustBatch(t, b.Create())
			run := batchRun(t, b)
			// Accepted and proposed segments both have nonempty sibling payloads.
			first, err := b.AddUserInput(run, session.UserInput{Parts: []session.ContentPart{{Text: new("accepted")}}})
			mustBatch(t, err)
			submitBatch(t, s, b)
			b = beginBatch(t, s)
			data := bytes.Repeat([]byte{0xa5}, 4096)
			second, err := b.AddUserInput(run, session.UserInput{Parts: []session.ContentPart{
				{Text: new("proposed")}, {Media: &session.Media{Kind: session.MediaKindDocument, Source: session.MediaSourceInline, MIMEType: "application/octet-stream", InlineData: data}},
			}})
			mustBatch(t, err)
			v := captureCommit(t, b)
			clear(data)
			if v.HistoryID() != run.HistoryID() || v.ActiveRun() != run || v.RecordCount() != 4 || empty.RecordCount() != 0 {
				t.Fatal("capture scope, active run or bounds")
			}
			accepted, _ := v.RecordAt(2).UserInput()
			proposed, _ := v.RecordAt(3).UserInput()
			media, ok := proposed.Parts().At(1).Media()
			if !ok || accepted.ID() != first || proposed.ID() != second || media.InlineData().At(0) != 0xa5 {
				t.Fatal("scoped two-segment traversal borrowed caller data")
			}
			leaf := media.InlineData().Copy()
			clear(leaf)
			wire := v.RecordAt(3).MutableClone()
			clear(wire.Payload)
			assertIteratorNoAllocation(t, func() {
				navigatedMedia, present := proposed.Parts().At(1).Media()
				if v.RecordCount() != 4 || v.ActiveRun() != run || !present || navigatedMedia != media || navigatedMedia.InlineData().Len() != 4096 || navigatedMedia.InlineData().At(0) != 0xa5 {
					panic("borrowed scalar navigation changed")
				}
			})
			before := make([][]byte, v.RecordCount())
			for i := range before {
				before[i] = v.RecordAt(i).MutableClone().Payload
			}
			mustBatch(t, b.ReplaceInstructions(run.HistoryID(), session.Instructions{}))
			mustBatch(t, b.EndRun(session.RunEndedInterrupted))
			if captureCommit(t, b).ActiveRun() != (session.RunID{}) || v.ActiveRun() != run {
				t.Fatal("active run not pinned")
			}
			if closeBatch == "Submit" {
				submitBatch(t, s, b)
				loaded, err := session.Load(t.Context(), h, run.HistoryID(), p)
				mustBatch(t, err)
				for i := range before {
					if !bytes.Equal(before[i], loaded.Snapshot().RecordAt(i).MutableClone().Payload) {
						t.Fatal("load changed view payload")
					}
				}
			} else {
				mustBatch(t, b.Discard())
			}
			for range 2 {
				count := 0
				for r := range v.Records() {
					if !bytes.Equal(before[count], r.MutableClone().Payload) {
						t.Fatal("closure changed pinned payload")
					}
					count++
				}
				if count != len(before) {
					t.Fatal("closure changed pinned length")
				}
			}
			visits := 0
			v.Records()(func(session.RecordView) bool { visits++; return false })
			if visits != 1 {
				t.Fatal("iteration ignored early stop")
			}
			if got, err := b.View(); !errors.Is(err, session.ErrBatch) || !reflect.DeepEqual(got, session.CommitView{}) {
				t.Fatal("closed capture succeeded")
			}
		})
	}
}

func TestCommitViewZeroAndOriginalHandle(t *testing.T) {
	var zero session.CommitView
	if zero.RecordCount() != 0 || zero.ActiveRun() != (session.RunID{}) || zero.HistoryID() != (session.Snapshot{}).HistoryID() {
		t.Fatal("zero view")
	}
	for range zero.Records() {
		t.Fatal("zero iteration")
	}
	for _, i := range []int{-1, 0, 1} {
		func() {
			defer func() {
				if recover() == nil {
					t.Fatal("invalid index did not panic")
				}
			}()
			zero.RecordAt(i)
		}()
	}
	var nilBatch *session.SessionCommit
	if _, err := nilBatch.View(); !errors.Is(err, session.ErrBatch) {
		t.Fatal("nil batch capture")
	}
	s := boundSession(t, newSessionHistory(t), sessionPolicy())
	b := beginBatch(t, s)
	batchCopy := *b
	if _, err := batchCopy.View(); !errors.Is(err, session.ErrBatch) {
		t.Fatal("copied batch capture")
	}
	mustBatch(t, b.Discard())
}

func TestCommitViewPreflightRejectionLifetime(t *testing.T) {
	p := sessionPolicy()
	p.MaxRecordsPerCommit = 1
	s := boundSession(t, newSessionHistory(t), p)
	b := beginBatch(t, s)
	mustBatch(t, b.Create())
	run := batchRun(t, b)
	v := captureCommit(t, b)
	if _, err := s.SubmitCommit(t.Context(), b); !errors.Is(err, codec.ErrLimitExceeded) {
		t.Fatalf("preflight: %v", err)
	}
	if v.RecordCount() != 2 || v.ActiveRun() != run {
		t.Fatal("preflight rejection changed captured scalars")
	}
	start, ok := v.RecordAt(1).RunStarted()
	if !ok || start.ID() != run {
		t.Fatal("preflight rejection changed captured identity")
	}
	if _, err := b.View(); !errors.Is(err, session.ErrBatch) {
		t.Fatal("rejected batch allowed capture")
	}
}

var commitViewSink session.CommitView
var commitRecordSink session.RecordView

func BenchmarkCommitView(b *testing.B) {
	for _, count := range []int{1, 128} {
		b.Run(fmt.Sprintf("RecordsPerSegment=%d", count), func(b *testing.B) {
			_, s, _ := newIteratorSession(b)
			batch := beginBatch(b, s)
			mustBatch(b, batch.Create())
			run := batchRun(b, batch)
			for range count {
				_, err := batch.AddUserInput(run, session.UserInput{Parts: []session.ContentPart{{Text: new(string(bytes.Repeat([]byte{'s'}, 4096)))}}})
				mustBatch(b, err)
			}
			submitBatch(b, s, batch)
			batch = beginBatch(b, s)
			for range count {
				_, err := batch.AddUserInput(run, session.UserInput{Parts: []session.ContentPart{{Text: new("proposed")}}})
				mustBatch(b, err)
			}
			defer batch.Discard() //nolint:errcheck // Benchmark cleanup.
			b.Run("Capture", func(b *testing.B) {
				b.ReportAllocs()
				for b.Loop() {
					var err error
					commitViewSink, err = batch.View()
					if err != nil {
						b.Fatal(err)
					}
				}
			})
			v := captureCommit(b, batch)
			b.Run("Traversal", func(b *testing.B) {
				b.ReportAllocs()
				for b.Loop() {
					for r := range v.Records() {
						commitRecordSink = r
					}
				}
			})
		})
	}
}
