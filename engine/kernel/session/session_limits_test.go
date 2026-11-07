package session_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/history/mem"
	"github.com/stacklok/mecatl/engine/kernel/codec"
	"github.com/stacklok/mecatl/engine/kernel/history"
	"github.com/stacklok/mecatl/engine/kernel/session"
)

// limitPolicy sets one cumulative dimension from actual encoded facts. The other
// dimensions remain wide, so each boundary has an independent rejection oracle.
func limitPolicy(t *testing.T, dimension string, softRecords, hardRecords []session.Record) session.Policy {
	t.Helper()
	p := sessionPolicy()
	sum := func(records []session.Record) uint64 {
		var n uint64
		for _, r := range records {
			n += uint64(len(r.Format().Payload))
		}
		return n
	}
	switch dimension {
	case "records":
		p.SoftMaxRetainedRecords, p.MaxRetainedRecords = len(softRecords), len(hardRecords)
	case "bytes":
		p.SoftMaxPayloadBytes, p.MaxPayloadBytes = sum(softRecords), sum(hardRecords)
	case "work":
		units := uint64(p.PayloadLimits.MaxBytes + p.PayloadLimits.MaxNodes + p.PayloadLimits.MaxCollectionElements)
		p.SoftMaxDecodedWork, p.MaxDecodedWork = uint64(len(softRecords))*units, uint64(len(hardRecords))*units
	default:
		t.Fatal("unknown dimension")
	}
	return p
}

func TestSessionSoftAdmissionAndCleanupBoundaries(t *testing.T) {
	initial := []session.Record{createdRecord(t), startRecord(t, 1), inputRecord(t, 1)}
	closed := append(append([]session.Record{}, initial...), closeoutRecord(t, 1))
	for _, dimension := range []string{"records", "bytes", "work"} {
		for _, mixed := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/mixed=%t", dimension, mixed), func(t *testing.T) {
				// Both the ordinary input plus a subsequent interruption, and the
				// entire idle mixed result fit hard. Only soft can reject either.
				p := limitPolicy(t, dimension, initial, append(append([]session.Record{}, closed...), inputRecord(t, 2)))
				backend := &batchBackend{Backend: mem.New()}
				h := newLifecycleHistory(t, backend)
				s := boundSession(t, h, p)
				b := beginBatch(t, s)
				mustBatch(t, b.Create())
				run := batchRun(t, b)
				batchInput(t, b, run, []byte{0xff, 0, 'x'})
				submitBatch(t, s, b) // Exactly soft, with actual hard closeout room.
				old := s.Snapshot()
				before := captureRejectedSnapshot(old)
				b = beginBatch(t, s)
				if id := batchInput(t, b, run, nil); id != allocatedInput(t, run, 2) {
					t.Fatal("provisional ordinal")
				}
				if mixed {
					mustBatch(t, b.EndRun(session.RunEndedInterrupted)) // Ending idle must not bypass soft.
				}
				if ref, err := s.SubmitCommit(context.Background(), b); ref != (history.CommitRef{}) || !errors.Is(err, codec.ErrLimitExceeded) || errors.Is(err, session.ErrAcceptanceUnknown) {
					t.Fatalf("ordinary soft rejection: %v, %v", ref, err)
				}
				if len(backend.attempts) != 1 || !reflect.DeepEqual(before, captureRejectedSnapshot(s.Snapshot())) || !reflect.DeepEqual(before, captureRejectedSnapshot(old)) {
					t.Fatal("soft rejection appended or mutated accepted views")
				}
				b = beginBatch(t, s)
				mustBatch(t, b.EndRun(session.RunEndedInterrupted))
				submitBatch(t, s, b) // Above soft, within hard.
				if s.Snapshot().ActiveRun() != (session.RunID{}) || s.Snapshot().RecordCount() != 4 || len(backend.attempts) != 2 {
					t.Fatal("cleanup did not consume only its own reserved fact")
				}
				loaded, err := session.Load(context.Background(), h, testHistoryID(t), p)
				mustBatch(t, err)
				if !reflect.DeepEqual(captureRejectedSnapshot(loaded.Snapshot()), captureRejectedSnapshot(s.Snapshot())) || len(backend.attempts) != 2 {
					t.Fatal("above-soft cleanup did not replay without writes")
				}
			})
		}
	}
}

func TestSessionHardReplayWithoutFutureWriteCapacity(t *testing.T) {
	initial := []session.Record{createdRecord(t), startRecord(t, 1), inputRecord(t, 1)}
	closed := append(append([]session.Record{}, initial...), closeoutRecord(t, 1))
	for _, dimension := range []string{"records", "bytes", "work"} {
		for _, headroom := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/headroom=%t", dimension, headroom), func(t *testing.T) {
				backend := &batchBackend{Backend: mem.New()}
				h := newLifecycleHistory(t, backend)
				wide := boundSession(t, h, sessionPolicy())
				b := beginBatch(t, wide)
				mustBatch(t, b.Create())
				run := batchRun(t, b)
				batchInput(t, b, run, []byte{0xff, 0, 'x'})
				submitBatch(t, wide, b)
				hard := initial
				if headroom {
					hard = closed
				}
				p := limitPolicy(t, dimension, initial[:2], hard)
				loaded, err := session.Load(context.Background(), h, testHistoryID(t), p)
				mustBatch(t, err)
				asserted := boundSession(t, h, p)
				accepted := proposedCommit(t, asserted, initial...)
				mustBatch(t, asserted.Apply(accepted))
				if !reflect.DeepEqual(captureRejectedSnapshot(loaded.Snapshot()), captureRejectedSnapshot(wide.Snapshot())) || !reflect.DeepEqual(captureRejectedSnapshot(asserted.Snapshot()), captureRejectedSnapshot(wide.Snapshot())) || len(backend.attempts) != 1 {
					t.Fatal("hard-valid above-soft active replay changed state or wrote")
				}
				before := captureRejectedSnapshot(loaded.Snapshot())
				b = beginBatch(t, loaded)
				batchInput(t, b, run, nil)
				if _, err := loaded.SubmitCommit(context.Background(), b); !errors.Is(err, codec.ErrLimitExceeded) {
					t.Fatalf("above-soft ordinary write: %v", err)
				}
				b = beginBatch(t, loaded)
				mustBatch(t, b.EndRun(session.RunEndedInterrupted))
				ref, err := loaded.SubmitCommit(context.Background(), b)
				if headroom {
					mustBatch(t, err)
					if ref == (history.CommitRef{}) || loaded.Snapshot().ActiveRun() != (session.RunID{}) {
						t.Fatal("cleanup with headroom failed")
					}
				} else if !errors.Is(err, codec.ErrLimitExceeded) || errors.Is(err, session.ErrAcceptanceUnknown) || ref != (history.CommitRef{}) || len(backend.attempts) != 1 || !reflect.DeepEqual(before, captureRejectedSnapshot(loaded.Snapshot())) {
					t.Fatalf("hard-bound cleanup rejection: %v, %v", ref, err)
				}
			})
		}
	}
}

func TestSessionWriteReservesActualHardCloseout(t *testing.T) {
	initial := []session.Record{createdRecord(t), startRecord(t, 1), inputRecord(t, 1)}
	closed := append(append([]session.Record{}, initial...), closeoutRecord(t, 1))
	for _, dimension := range []string{"records", "bytes", "work"} {
		t.Run(dimension, func(t *testing.T) {
			p := limitPolicy(t, dimension, initial, closed)
			switch dimension {
			case "records":
				p.MaxRetainedRecords--
			case "bytes":
				p.MaxPayloadBytes--
			case "work":
				p.MaxDecodedWork--
			}
			backend := &batchBackend{Backend: mem.New()}
			s := boundSession(t, newLifecycleHistory(t, backend), p)
			b := beginBatch(t, s)
			mustBatch(t, b.Create())
			run := batchRun(t, b)
			batchInput(t, b, run, []byte{0xff, 0, 'x'})
			if ref, err := s.SubmitCommit(context.Background(), b); ref != (history.CommitRef{}) || !errors.Is(err, codec.ErrLimitExceeded) || errors.Is(err, session.ErrAcceptanceUnknown) || len(backend.attempts) != 0 || s.Snapshot().Created() {
				t.Fatalf("one-short closeout capacity: %v, %v", ref, err)
			}
			mustBatch(t, beginBatch(t, s).Discard())
		})
	}
}

// limitBackendError proves typed storage causes survive the redacted wrapper.
type limitBackendError struct{}

func (*limitBackendError) Error() string { return "private backend detail" }
func (*limitBackendError) Unwrap() error { return codec.ErrLimitExceeded }

func TestSessionAdmissionAndUncertaintyShareCauseNotOutcome(t *testing.T) {
	for _, backendFailure := range []bool{false, true} {
		for _, accepted := range []bool{false, true} {
			t.Run(fmt.Sprintf("backend=%t/accepted=%t", backendFailure, accepted), func(t *testing.T) {
				p := sessionPolicy()
				if !backendFailure {
					p.SoftMaxRetainedRecords = 2
				}
				backend := &batchBackend{Backend: mem.New()}
				s := boundSession(t, newLifecycleHistory(t, backend), p)
				b := beginBatch(t, s)
				mustBatch(t, b.Create())
				run := batchRun(t, b)
				submitBatch(t, s, b)
				before := captureRejectedSnapshot(s.Snapshot())
				if backendFailure {
					backend.fail = fmt.Errorf("storage error: %w", &limitBackendError{})
					backend.accept = accepted
				}
				b = beginBatch(t, s)
				id := batchInput(t, b, run, []byte("owned"))
				ref, err := s.SubmitCommit(context.Background(), b)
				if ref != (history.CommitRef{}) || !errors.Is(err, codec.ErrLimitExceeded) || errors.Is(err, session.ErrAcceptanceUnknown) != backendFailure || !reflect.DeepEqual(before, captureRejectedSnapshot(s.Snapshot())) {
					t.Fatalf("same cause, distinct outcome: %v, %v", ref, err)
				}
				if backendFailure {
					var cause *limitBackendError
					if !errors.As(err, &cause) || strings.Contains(err.Error(), "private backend detail") {
						t.Fatal("uncertainty wrapper lost typed cause or rendered backend text")
					}
					if _, err := s.BeginCommit(); !errors.Is(err, session.ErrBatch) {
						t.Fatal("uncertain proposal allowed replacement cleanup")
					}
					if err := b.Discard(); !errors.Is(err, session.ErrBatch) {
						t.Fatal("uncertain proposal allowed discard")
					}
					submitBatch(t, s, b)
					if len(backend.attempts) != 3 || !reflect.DeepEqual(backend.attempts[1], backend.attempts[2]) || lookupInput(t, s.Snapshot(), id.RunID(), 1).ID() != id {
						t.Fatal("uncertain retry changed identity or bytes")
					}
				} else {
					if len(backend.attempts) != 1 {
						t.Fatal("local rejection attempted storage")
					}
					b = beginBatch(t, s)
					mustBatch(t, b.EndRun(session.RunEndedInterrupted))
					submitBatch(t, s, b)
					if !userInputsMatch(s.Snapshot(), run, 0) {
						t.Fatal("rejected input leaked into cleanup")
					}
				}
			})
		}
	}
}

func TestSessionPolicySoftBoundsExplicit(t *testing.T) {
	for _, dimension := range []string{"records", "bytes", "work"} {
		for _, invalid := range []string{"zero", "above hard"} {
			t.Run(dimension+"/"+invalid, func(t *testing.T) {
				p := sessionPolicy()
				switch dimension {
				case "records":
					p.SoftMaxRetainedRecords = 0
					if invalid == "above hard" {
						p.SoftMaxRetainedRecords = p.MaxRetainedRecords + 1
					}
				case "bytes":
					p.SoftMaxPayloadBytes = 0
					if invalid == "above hard" {
						p.SoftMaxPayloadBytes = p.MaxPayloadBytes + 1
					}
				case "work":
					p.SoftMaxDecodedWork = 0
					if invalid == "above hard" {
						p.SoftMaxDecodedWork = p.MaxDecodedWork + 1
					}
				}
				if !errors.Is(p.Validate(), session.ErrInvalidPolicy) {
					t.Fatal("invalid soft bound accepted")
				}
				if s, err := session.New(newSessionHistory(t), testHistoryID(t), p); s != nil || !errors.Is(err, session.ErrInvalidPolicy) {
					t.Fatalf("New accepted invalid soft policy: %v", err)
				}
			})
		}
	}
	p := sessionPolicy()
	p.SoftMaxRetainedRecords = -1
	if !errors.Is(p.Validate(), session.ErrInvalidPolicy) {
		t.Fatal("negative soft record bound accepted")
	}
	mustBatch(t, sessionPolicy().Validate()) // Equality is valid; no implicit reserve fraction.
}
