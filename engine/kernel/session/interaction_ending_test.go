package session

import (
	"context"
	"errors"
	"fmt"
	"math"
	"reflect"
	"testing"

	"github.com/stacklok/mecatl/engine/kernel/codec"
	"github.com/stacklok/mecatl/engine/kernel/history"
	"github.com/stacklok/mecatl/engine/kernel/history/format"
)

func TestInteractionEveryMinimalEndingReservation(t *testing.T) {
	reasons := []RunEndedReason{RunEndedError, RunEndedLimit, RunEndedCancelled, RunEndedInterrupted, RunEndedCaptureFailed}
	for _, stage := range []string{"pending", "calls"} {
		for _, reason := range reasons {
			for _, dimension := range []string{"records", "bytes", "work"} {
				for _, short := range []bool{false, true} {
					t.Run(fmt.Sprintf("%s/%d/%s/short=%t", stage, reason, dimension, short), func(t *testing.T) {
						s := interactionFixture(t, stage)
						run := s.Snapshot().ActiveRun()
						contextRecord := interactionRecord(t)(NewInstructionsRecord(Instructions{HistoryID: run.HistoryID}, s.policy.PayloadLimits))
						ending := interactionRecord(t)(NewRunEndedRecord(RunEnded{RunID: run, Reason: reason}, s.policy.PayloadLimits))
						p := s.policy
						switch dimension {
						case "records":
							p.SoftMaxRetainedRecords = s.state.records + 1
							p.MaxRetainedRecords = s.state.records + 2
							if short {
								p.MaxRetainedRecords--
							}
						case "bytes":
							p.SoftMaxPayloadBytes = s.state.payloadBytes + uint64(len(contextRecord.value.Payload))
							p.MaxPayloadBytes = p.SoftMaxPayloadBytes + uint64(len(ending.value.Payload))
							if short {
								p.MaxPayloadBytes--
							}
						case "work":
							units := uint64(p.PayloadLimits.MaxBytes + p.PayloadLimits.MaxNodes + p.PayloadLimits.MaxCollectionElements)
							p.SoftMaxDecodedWork = uint64(s.state.records+1) * units
							p.MaxDecodedWork = uint64(s.state.records+2) * units
							if short {
								p.MaxDecodedWork--
							}
						}
						s.policy = p
						before := detachedSessionState(s.state)
						b, err := s.BeginCommit()
						interactionCheck(t, err)
						interactionCheck(t, b.ReplaceInstructions(Instructions{HistoryID: run.HistoryID}))
						_, err = s.SubmitCommit(context.Background(), b)
						if short {
							if !errors.Is(err, codec.ErrLimitExceeded) || errors.Is(err, ErrAcceptanceUnknown) || !reflect.DeepEqual(before, detachedSessionState(s.state)) {
								t.Fatalf("reservation one short: %v", err)
							}
							return
						}
						interactionCheck(t, err)
						active := s.state.view.lifecycle
						prepared, err := s.prepareEnding(reason)
						interactionCheck(t, err)
						if len(prepared.commit.Records) != 1 || !prepared.commit.Records[0].Equal(ending.Format()) {
							t.Fatal("reservation not actual minimal ending")
						}
						b, err = s.BeginCommit()
						interactionCheck(t, err)
						interactionCheck(t, b.EndRun(reason))
						_, err = s.SubmitCommit(context.Background(), b)
						interactionCheck(t, err)
						if s.state.view.lifecycle.pending != active.pending || s.state.view.lifecycle.calls != active.calls || s.state.view.lifecycle.frontier != active.frontier || s.state.view.lifecycle.accounted != active.accounted {
							t.Fatal("ending erased uncertainty")
						}
						loaded, err := Load(context.Background(), s.history, run.HistoryID, p)
						interactionCheck(t, err)
						if !reflect.DeepEqual(s.Snapshot(), loaded.Snapshot()) {
							t.Fatal("hard-only ending replay")
						}
					})
				}
			}
		}
	}
	// Every closed reason has the same bounded payload shape, including maximum
	// run widths. No count of unresolved calls can enlarge it.
	for _, ordinal := range []uint64{1, 23, 24, 255, 256, math.MaxUint64} {
		var size int
		for _, reason := range append([]RunEndedReason{RunEndedCompleted}, reasons...) {
			s, _ := internalFixture(t)
			r := interactionRecord(t)(NewRunEndedRecord(RunEnded{RunID: RunID{HistoryID: s.Snapshot().HistoryID(), Ordinal: ordinal}, Reason: reason}, s.policy.PayloadLimits))
			if size != 0 && len(r.value.Payload) != size {
				t.Fatal("minimal reason sizes differ")
			}
			size = len(r.value.Payload)
		}
	}
}

func TestInteractionDetailedAndMixedEndingsStaySoftBounded(t *testing.T) {
	for _, mode := range []string{"detail", "empty detail", "capture", "mixed"} {
		t.Run(mode, func(t *testing.T) {
			s := interactionFixture(t, "pending")
			run := s.Snapshot().ActiveRun()
			before := detachedSessionState(s.state)
			s.policy.SoftMaxRetainedRecords = s.state.records
			b, err := s.BeginCommit()
			interactionCheck(t, err)
			ending := RunEnded{RunID: run, Reason: RunEndedCaptureFailed}
			switch mode {
			case "detail":
				ending.Explanation = new("safe explanation")
			case "empty detail":
				ending.Explanation = new("")
			case "capture":
				ending.Capture = &CaptureFailureEvidence{RequestID: s.state.view.lifecycle.pending}
			case "mixed":
				interactionCheck(t, b.ReplaceInstructions(Instructions{HistoryID: run.HistoryID}))
			}
			interactionCheck(t, b.AddRunEnded(ending))
			_, err = s.SubmitCommit(context.Background(), b)
			if !errors.Is(err, codec.ErrLimitExceeded) || errors.Is(err, ErrAcceptanceUnknown) || !reflect.DeepEqual(before, detachedSessionState(s.state)) {
				t.Fatalf("soft bypass: %v", err)
			}
			interactionSubmit(t, s, func(b *SessionCommit) { interactionCheck(t, b.EndRun(RunEndedCaptureFailed)) })
		})
	}
}

func TestInteractionHistoryPolicyReservesExactNextEndingWithoutIO(t *testing.T) {
	for _, short := range []bool{false, true} {
		t.Run(fmt.Sprint(short), func(t *testing.T) {
			s, backend := closeoutWidthPrefix(t)
			run := RunID{HistoryID: s.Snapshot().HistoryID(), Ordinal: uint64(s.Snapshot().RunCount()) + 1}
			start := interactionRecord(t)(NewRunStartedRecord(RunStarted{ID: run}, s.policy.PayloadLimits))
			candidate, err := s.prepare([]Record{start})
			interactionCheck(t, err)
			ending := interactionRecord(t)(NewRunEndedRecord(RunEnded{RunID: run, Reason: RunEndedCaptureFailed}, s.policy.PayloadLimits))
			next := format.Commit{FormatVersion: 1, Sequence: 24, Parent: new(candidate.ref.CID), Records: []format.Record{ending.Format()}}
			budget, err := codec.NewBudget(s.policy.CommitLimits)
			interactionCheck(t, err)
			interactionCheck(t, next.CheckCBOR(&budget, 0, math.MaxInt))
			limits := s.policy.CommitLimits
			limits.MaxBytes = budget.EstimatedBytes()
			if short {
				limits.MaxBytes--
			}
			limits.MaxStringBytes = min(limits.MaxStringBytes, limits.MaxBytes)
			limits.MaxByteStringBytes = min(limits.MaxByteStringBytes, limits.MaxBytes)
			s.history, err = history.New(backend, history.Policy{CommitLimits: limits, RetryWindow: 16, MaxPageBytes: limits.MaxBytes, MaxPageCommits: 2})
			interactionCheck(t, err)
			if _, err := s.history.Identify(run.HistoryID, candidate.commit); err != nil {
				t.Fatalf("candidate itself must fit tighter history: %v", err)
			}
			attempts := backend.appends
			_, err = s.prepare([]Record{start})
			if backend.appends != attempts {
				t.Fatal("Identify reservation did I/O")
			}
			if short {
				if !errors.Is(err, history.ErrInvalid) {
					t.Fatalf("missing tighter History reservation: %v", err)
				}
				return
			}
			interactionCheck(t, err)
			interactionSubmit(t, s, func(b *SessionCommit) {
				got, e := b.StartRun()
				interactionCheck(t, e)
				if got != run {
					t.Fatal("run")
				}
			})
			interactionSubmit(t, s, func(b *SessionCommit) { interactionCheck(t, b.EndRun(RunEndedCaptureFailed)) })
		})
	}
}

func TestInteractionCaptureEvidenceMembershipAndPreservation(t *testing.T) {
	for _, wrong := range []bool{false, true} {
		t.Run(fmt.Sprint(wrong), func(t *testing.T) {
			s := interactionFixture(t, "pending")
			pending := s.state.view.lifecycle.pending
			capture := CaptureFailureEvidence{RequestID: pending, Evidence: &ObservedEvidence{Correlation: new("safe")}, Usage: &InvocationUsage{Input: new(int64(0)), Raw: &OpaqueProviderData{Producer: "p", Format: "usage", Version: 1, Data: []byte{0xff}}}}
			if wrong {
				capture.RequestID.Ordinal++
			}
			ending := RunEnded{RunID: pending.RunID, Reason: RunEndedCaptureFailed, Capture: &capture}
			r := interactionRecord(t)(NewRunEndedRecord(ending, s.policy.PayloadLimits))
			c := interactionCommit(t, s, []Record{r})
			before := detachedSessionState(s.state)
			b, err := s.BeginCommit()
			interactionCheck(t, err)
			err = b.AddRunEnded(ending)
			if wrong {
				if !errors.Is(err, ErrTransition) {
					t.Fatal("foreign pending capture admitted")
				}
				interactionCheck(t, b.Discard())
				if err = s.Apply(c); !errors.Is(err, ErrTransition) || !reflect.DeepEqual(before, detachedSessionState(s.state)) {
					t.Fatalf("decoded capture membership: %v", err)
				}
				return
			}
			interactionCheck(t, err)
			*capture.Usage.Input = 7
			capture.Usage.Raw.Data[0] = 0
			_, err = s.SubmitCommit(context.Background(), b)
			interactionCheck(t, err)
			v, ok := s.Snapshot().RecordAt(s.Snapshot().RecordCount() - 1).RunEnded()
			if !ok {
				t.Fatal("ending")
			}
			evidence, ok := v.Capture()
			if !ok || evidence.RequestID != pending || *evidence.Usage.Input != 0 || evidence.Usage.Raw.Data[0] != 0xff {
				t.Fatal("capture evidence lost/aliased")
			}
			*evidence.Usage.Input = 8
			evidence.Usage.Raw.Data[0] = 1
			again, _ := v.Capture()
			if *again.Usage.Input != 0 || again.Usage.Raw.Data[0] != 0xff || s.state.view.lifecycle.pending != pending {
				t.Fatal("capture export mutated fact or resolved request")
			}
			loaded, err := Load(context.Background(), s.history, pending.RunID.HistoryID, s.policy)
			interactionCheck(t, err)
			if !reflect.DeepEqual(s.Snapshot(), loaded.Snapshot()) {
				t.Fatal("capture replay")
			}
		})
	}
}
