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
	reasons := []RunEndedReason{RunEndedCompleted, RunEndedError, RunEndedLimit, RunEndedInterrupted, RunEndedCaptureFailed}
	for _, stage := range []string{"bound", "pending", "calls"} {
		for _, reason := range reasons {
			if reason == RunEndedCompleted && stage != "bound" {
				continue // Completion cannot discard pending outcomes or calls.
			}
			for _, dimension := range []string{"records", "bytes", "work"} {
				for _, short := range []bool{false, true} {
					t.Run(fmt.Sprintf("%s/%s/%s/short=%t", stage, reason, dimension, short), func(t *testing.T) {
						s := interactionFixture(t, stage)
						run := s.Snapshot().ActiveRun()
						contextRecord := interactionRecord(t)(newInstructionsRecord(Instructions{}, s.policy.PayloadLimits))
						ending := interactionRecord(t)(newRunEndedRecord(RunEnded{Reason: reason}, run, s.policy.PayloadLimits))
						largest := interactionRecord(t)(newRunEndedRecord(RunEnded{Reason: RunEndedCaptureFailed}, run, s.policy.PayloadLimits))
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
							p.MaxPayloadBytes = p.SoftMaxPayloadBytes + uint64(len(largest.value.Payload))
							if short {
								p.MaxPayloadBytes--
							}
						case "work":
							units := uint64(p.PayloadLimits.MaxBytes) + uint64(p.PayloadLimits.MaxNodes) + uint64(p.PayloadLimits.MaxCollectionElements)
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
						interactionCheck(t, b.ReplaceInstructions(run.historyID, Instructions{}))
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
						if len(prepared.commit.Records) != 1 || !prepared.commit.Records[0].Equal(ending.format()) {
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
						loaded, err := Load(context.Background(), s.history, run.historyID, p)
						interactionCheck(t, err)
						if !reflect.DeepEqual(s.Snapshot(), loaded.Snapshot()) {
							t.Fatal("hard-only ending replay")
						}
					})
				}
			}
		}
	}
	// Labels have unequal sizes. The actual largest supported minimal ending
	// bounds every reason, including at maximum run widths.
	for _, ordinal := range []uint64{1, 23, 24, 255, 256, math.MaxUint64} {
		s, _ := internalFixture(t)
		run := RunID{historyID: s.Snapshot().HistoryID(), ordinal: ordinal}
		largest := interactionRecord(t)(newRunEndedRecord(RunEnded{Reason: RunEndedCaptureFailed}, run, s.policy.PayloadLimits))
		for _, reason := range reasons {
			r := interactionRecord(t)(newRunEndedRecord(RunEnded{Reason: reason}, run, s.policy.PayloadLimits))
			if len(r.value.Payload) > len(largest.value.Payload) {
				t.Fatalf("%s exceeds reserved minimal shape", reason)
			}
			if reason == RunEndedError && len(r.value.Payload) == len(largest.value.Payload) {
				t.Fatal("fixture must exercise unequal label lengths")
			}
		}
	}
}

func TestInteractionMinimalEndingExactHardBounds(t *testing.T) {
	for _, reason := range []RunEndedReason{RunEndedCompleted, RunEndedError, RunEndedLimit, RunEndedInterrupted, RunEndedCaptureFailed} {
		for _, dimension := range []string{"records", "bytes", "work"} {
			for _, short := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/short=%t", reason, dimension, short), func(t *testing.T) {
					s := interactionFixture(t, "bound")
					r := interactionRecord(t)(newRunEndedRecord(RunEnded{Reason: reason}, s.Snapshot().ActiveRun(), s.policy.PayloadLimits))
					switch dimension {
					case "records":
						s.policy.SoftMaxRetainedRecords = s.state.records
						s.policy.MaxRetainedRecords = s.state.records + 1
						if short {
							s.policy.MaxRetainedRecords--
						}
					case "bytes":
						s.policy.SoftMaxPayloadBytes = s.state.payloadBytes
						s.policy.MaxPayloadBytes = s.state.payloadBytes + uint64(len(r.value.Payload))
						if short {
							s.policy.MaxPayloadBytes--
						}
					case "work":
						l := s.policy.PayloadLimits
						units := uint64(l.MaxBytes) + uint64(l.MaxNodes) + uint64(l.MaxCollectionElements)
						s.policy.SoftMaxDecodedWork = uint64(s.state.records) * units
						s.policy.MaxDecodedWork = uint64(s.state.records+1) * units
						if short {
							s.policy.MaxDecodedWork--
						}
					}
					before := detachedSessionState(s.state)
					b, err := s.BeginCommit()
					interactionCheck(t, err)
					interactionCheck(t, b.EndRun(reason))
					_, err = s.SubmitCommit(context.Background(), b)
					if short {
						if !errors.Is(err, codec.ErrLimitExceeded) || errors.Is(err, ErrAcceptanceUnknown) || !reflect.DeepEqual(before, detachedSessionState(s.state)) {
							t.Fatalf("one-short minimal ending: %v", err)
						}
						return
					}
					interactionCheck(t, err)
					interactionReplay(t, s)
				})
			}
		}
	}
}

func TestInteractionMinimalEndingEncodingPolicyDimensions(t *testing.T) {
	for _, reason := range []RunEndedReason{RunEndedCompleted, RunEndedError, RunEndedLimit, RunEndedInterrupted, RunEndedCaptureFailed} {
		for _, owner := range []string{"payload", "session envelope", "history envelope"} {
			for field := range reflect.TypeFor[codec.Limits]().NumField() {
				name := reflect.TypeFor[codec.Limits]().Field(field).Name
				t.Run(fmt.Sprintf("%s/%s/%s", reason, owner, name), func(t *testing.T) {
					s := interactionFixture(t, "bound")
					ending, err := s.prepareEnding(reason)
					interactionCheck(t, err)
					var value codec.Marshaler = ending.commit
					limits := s.policy.CommitLimits
					if owner == "payload" {
						value = RunEnded{RunID: s.Snapshot().ActiveRun().HistoryRelative(), Reason: reason}
						limits = s.policy.PayloadLimits
					}
					lo, hi := 1, int(reflect.ValueOf(limits).Field(field).Int())
					for lo < hi {
						mid := lo + (hi-lo)/2
						l := limits
						reflect.ValueOf(&l).Elem().Field(field).SetInt(int64(mid))
						if _, err := codec.Encode(value, l); err == nil {
							hi = mid
						} else {
							lo = mid + 1
						}
					}
					for _, short := range []bool{false, true} {
						// An unused dimension may already fit its smallest valid policy;
						// zero is invalid policy, not a one-short encoding boundary.
						if short && lo == 1 {
							continue
						}
						l := limits
						n := lo
						if short {
							n--
						}
						reflect.ValueOf(&l).Elem().Field(field).SetInt(int64(n))
						want := codec.ErrLimitExceeded
						switch owner {
						case "payload":
							s.policy.PayloadLimits = l
						case "session envelope":
							s.policy.CommitLimits = l
						case "history envelope":
							// Identify must reject without calling this backend.
							backend := &closeoutEnvelopeBackend{}
							s.history, err = history.New(backend, history.Policy{CommitLimits: l, RetryWindow: 16, MaxPageBytes: l.MaxBytes, MaxPageCommits: 2})
							interactionCheck(t, err)
							want = history.ErrInvalid
						}
						got, err := s.prepareEnding(reason)
						if short {
							if !errors.Is(err, want) || !reflect.DeepEqual(got, prepared{}) {
								t.Fatalf("one-short %s: %v", owner, err)
							}
						} else if err != nil || got.ref != ending.ref || !got.commit.Equal(ending.commit) {
							t.Fatalf("exact %s changed ending: %v", owner, err)
						}
					}
				})
			}
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
			ending := RunEnded{Reason: RunEndedCaptureFailed}
			switch mode {
			case "detail":
				ending.Explanation = new("safe explanation")
			case "empty detail":
				ending.Explanation = new("")
			case "capture":
				ending.Capture = &CaptureFailureEvidence{RequestID: s.state.view.lifecycle.pending.RunRelative()}
			case "mixed":
				interactionCheck(t, b.ReplaceInstructions(run.historyID, Instructions{}))
			}
			interactionCheck(t, b.AddRunEnded(run, ending))
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
			run := RunID{historyID: s.Snapshot().HistoryID(), ordinal: uint64(s.Snapshot().RunCount()) + 1}
			start := interactionRecord(t)(newRunStartedRecord(RunStarted{ID: run.HistoryRelative()}, s.policy.PayloadLimits))
			candidate, err := s.prepare([]*materializedRecord{start})
			interactionCheck(t, err)
			ending := interactionRecord(t)(newRunEndedRecord(RunEnded{Reason: RunEndedCaptureFailed}, run, s.policy.PayloadLimits))
			next := format.Commit{FormatVersion: 1, Sequence: 24, Parent: new(candidate.ref.CID), Records: []format.Record{ending.format()}}
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
			if _, err := s.history.Identify(run.historyID, candidate.commit); err != nil {
				t.Fatalf("candidate itself must fit tighter history: %v", err)
			}
			attempts := backend.appends
			_, err = s.prepare([]*materializedRecord{start})
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
			capture := CaptureFailureEvidence{RequestID: pending.RunRelative(), Evidence: &ObservedEvidence{Correlation: new("safe")}, Usage: &InvocationUsage{Input: new(int64(0)), Raw: &OpaqueProviderData{Producer: "p", Format: "usage", Version: 1, Data: []byte{0xff}}}}
			if wrong {
				capture.RequestID++
			}
			ending := RunEnded{Reason: RunEndedCaptureFailed, Capture: &capture}
			r := interactionRecord(t)(newRunEndedRecord(ending, pending.runID, s.policy.PayloadLimits))
			c := interactionCommit(t, s, []*materializedRecord{r})
			before := detachedSessionState(s.state)
			b, err := s.BeginCommit()
			interactionCheck(t, err)
			err = b.AddRunEnded(pending.runID, ending)
			if wrong {
				if !errors.Is(err, ErrTransition) {
					t.Fatal("foreign pending capture admitted")
				}
				interactionCheck(t, b.Discard())
				if err = s.apply(c); !errors.Is(err, ErrTransition) || !reflect.DeepEqual(before, detachedSessionState(s.state)) {
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
			captureView, present := v.Capture()
			if !present || captureView.RequestID() != pending {
				t.Fatal("capture evidence missing")
			}
			usage, usagePresent := captureView.Usage()
			raw, rawPresent := usage.Raw()
			input, inputPresent := usage.Input()
			if !usagePresent || !rawPresent || !inputPresent || input != 0 || raw.Data().Copy()[0] != 0xff {
				t.Fatal("capture evidence lost/aliased")
			}
			data := raw.Data().Copy()
			data[0] = 1
			again, _ := v.Capture()
			againUsage, _ := again.Usage()
			againInput, _ := againUsage.Input()
			againRaw, _ := againUsage.Raw()
			if againInput != 0 || againRaw.Data().Copy()[0] != 0xff || s.state.view.lifecycle.pending != pending {
				t.Fatal("capture export mutated fact or resolved request")
			}
			loaded, err := Load(context.Background(), s.history, pending.runID.historyID, s.policy)
			interactionCheck(t, err)
			if !reflect.DeepEqual(s.Snapshot(), loaded.Snapshot()) {
				t.Fatal("capture replay")
			}
		})
	}
}
