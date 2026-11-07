package session_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/history/mem"
	"github.com/stacklok/mecatl/engine/kernel/codec"
	"github.com/stacklok/mecatl/engine/kernel/history"
	"github.com/stacklok/mecatl/engine/kernel/session"
	sessionformat "github.com/stacklok/mecatl/engine/kernel/session/format"
)

func TestContextActualReserveAndSoftBounds(t *testing.T) {
	scope := testHistoryID(t)
	instructions := session.Instructions{HistoryID: scope, Blocks: []session.InstructionBlock{{Parts: []session.ContentPart{{Text: new("retained context payload")}}, Source: session.ContentSource{Description: "caller prepared"}}}}
	contextRecord := encodeFixture(t, sessionformat.KindInstructions, sessionformat.Instructions{Blocks: []sessionformat.InstructionBlock{{Parts: []sessionformat.ContentPart{{Text: new("retained context payload")}}, Source: sessionformat.ContentSource{Description: "caller prepared"}}}})
	initial := []fixtureRecord{createdRecord(t), startRecord(t, 1), contextRecord}
	largestEnding := encodeFixture(t, sessionformat.KindRunEnded, sessionformat.RunEnded{RunID: 1, Reason: sessionformat.RunEndedCaptureFailed})
	closed := append(append([]fixtureRecord{}, initial...), largestEnding)
	for _, dimension := range []string{"records", "bytes", "work"} {
		for _, mode := range []string{"exact", "one-short", "soft-ordinary", "soft-mixed"} {
			t.Run(fmt.Sprintf("%s/%s", dimension, mode), func(t *testing.T) {
				p := limitPolicy(t, dimension, initial, closed)
				if mode == "soft-ordinary" || mode == "soft-mixed" {
					// Only soft may reject; both mixed closeout and future reserve fit hard.
					p = limitPolicy(t, dimension, initial, append(append([]fixtureRecord{}, closed...), contextRecord))
				}
				if mode == "one-short" {
					switch dimension {
					case "records":
						p.MaxRetainedRecords--
					case "bytes":
						p.MaxPayloadBytes--
					case "work":
						p.MaxDecodedWork--
					}
				}
				backend := &batchBackend{Backend: mem.New()}
				h := newLifecycleHistory(t, backend)
				s := boundSession(t, h, p)
				b := beginBatch(t, s)
				mustBatch(t, b.Create())
				batchRun(t, b)
				mustBatch(t, b.ReplaceInstructions(instructions))
				if mode == "one-short" {
					if ref, err := s.SubmitCommit(context.Background(), b); ref != (history.CommitRef{}) || !errors.Is(err, codec.ErrLimitExceeded) || errors.Is(err, session.ErrAcceptanceUnknown) || len(backend.attempts) != 0 || s.Snapshot().Created() {
						t.Fatalf("actual context reserve one short: %v, %v", ref, err)
					}
					mustBatch(t, beginBatch(t, s).Discard())
					return
				}
				submitBatch(t, s, b)
				if mode != "exact" {
					before := captureRejectedSnapshot(s.Snapshot())
					b = beginBatch(t, s)
					mustBatch(t, b.ReplaceInstructions(instructions))
					if mode == "soft-mixed" {
						mustBatch(t, b.EndRun(session.RunEndedInterrupted))
					}
					if ref, err := s.SubmitCommit(context.Background(), b); ref != (history.CommitRef{}) || !errors.Is(err, codec.ErrLimitExceeded) || errors.Is(err, session.ErrAcceptanceUnknown) || len(backend.attempts) != 1 || !reflect.DeepEqual(before, captureRejectedSnapshot(s.Snapshot())) {
						t.Fatalf("context soft rejection: %v, %v", ref, err)
					}
				}
				b = beginBatch(t, s)
				mustBatch(t, b.EndRun(session.RunEndedInterrupted))
				submitBatch(t, s, b)
				if s.Snapshot().ActiveRun() != (session.RunID{}) || len(backend.attempts) != 2 {
					t.Fatal("actual reserved interruption did not fit")
				}
			})
		}
	}
}
