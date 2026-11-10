package session

import (
	"context"
	"errors"
	"fmt"
	"math"
	"reflect"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/kernel/codec"
	"github.com/stacklok/mecatl/engine/kernel/history"
)

func TestToolOrdinaryEndAfterFinalDispositions(t *testing.T) {
	for _, reason := range []RunEndedReason{RunEndedCompleted, RunEndedError, RunEndedInterrupted, RunEndedLimit, RunEndedCaptureFailed} {
		t.Run(string(reason), func(t *testing.T) {
			s, calls := toolAdmissionFixture(t)
			interactionSubmit(t, s, func(b *SessionCommit) {
				interactionCheck(t, b.AddToolInvocation(calls[0], ToolInvocation{Name: "A"}))
				interactionCheck(t, b.AddToolInvocation(calls[1], ToolInvocation{Name: "B"}))
			})
			interactionSubmit(t, s, func(b *SessionCommit) {
				interactionCheck(t, b.AddToolDisposition(calls[0], toolOutcome("error")))
				interactionCheck(t, b.AddToolDisposition(calls[1], toolOutcome("indeterminate")))
				interactionCheck(t, b.AddToolDisposition(calls[2], toolOutcome("not invoked")))
				interactionCheck(t, b.EndRun(reason))
			})
			ending, ok := s.Snapshot().RecordAt(s.Snapshot().RecordCount() - 1).RunEnded()
			if !ok || ending.BulkToolCloseout() || s.state.view.lifecycle.calls != 0 {
				t.Fatal("ordinary end did not retain individual settlement")
			}
			interactionReplay(t, s)
		})
	}
}

func TestToolBulkCloseoutCandidateFactsAndSuccessor(t *testing.T) {
	for _, reason := range []RunEndedReason{RunEndedLimit, RunEndedCaptureFailed} {
		for _, successor := range []bool{false, true} {
			for _, path := range []string{"builder", "preflight", "apply", "load"} {
				t.Run(fmt.Sprintf("%s/successor=%t/%s", reason, successor, path), func(t *testing.T) {
					s, calls := toolAdmissionFixture(t)
					run := s.Snapshot().ActiveRun()
					pinned := s.Snapshot()
					explicit := toolOutcome("not invoked")
					beforeCount := pinned.RecordCount()
					interactionSubmit(t, s, func(b *SessionCommit) {
						interactionCheck(t, b.AddToolDisposition(calls[0], explicit))
						interactionCheck(t, b.AddToolInvocation(calls[1], ToolInvocation{Name: "B"}))
						interactionCheck(t, b.AddRunEnded(run, RunEnded{Reason: reason, BulkToolCloseout: true}))
						terminal := detachedToolAdmission(b.admission)
						if err := b.AddToolDisposition(calls[2], toolOutcome("not invoked")); !errors.Is(err, ErrTransition) {
							t.Fatalf("same-batch late disposition: %v", err)
						}
						if err := b.AddToolInvocation(calls[2], ToolInvocation{Name: "late"}); !errors.Is(err, ErrTransition) || !reflect.DeepEqual(terminal, b.admission) {
							t.Fatalf("same-batch late invocation mutated ending: %v", err)
						}
					})
					if s.Snapshot().RecordCount() != beforeCount+3 || pinned.lifecycle.tools.calls[0].settled || pinned.lifecycle.tools.calls[1].invocation != nil {
						t.Fatal("bulk fabricated records or changed pinned calls")
					}
					explicit.RunID, explicit.RequestID, explicit.CallID = run.ordinal, calls[0].requestID.requestOrdinal, calls[0].ordinal
					v, ok := s.Snapshot().RecordAt(beforeCount).ToolDisposition()
					if !ok || !v.value.Equal(explicit) {
						t.Fatal("bulk replaced explicit disposition")
					}
					interactionReplay(t, s)
					if successor {
						interactionSubmit(t, s, func(b *SessionCommit) {
							next, err := b.StartRun()
							interactionCheck(t, err)
							_, err = b.AddUserInput(next, UserInput{})
							interactionCheck(t, err)
						})
						if s.state.view.lifecycle.tools != nil || s.state.view.lifecycle.calls != 0 || s.state.view.lifecycle.pending != (ModelRequestID{}) {
							t.Fatal("successor inherited obligations")
						}
						interactionReplay(t, s)
					}
					// Both facts reject for both kinds of unresolved bulk-derived call.
					for _, call := range calls[1:] {
						for _, invocation := range []bool{false, true} {
							var r *materializedRecord
							if invocation {
								r = interactionRecord(t)(newToolInvocationRecord(ToolInvocation{Name: "late"}, call, s.policy.PayloadLimits))
							} else {
								r = interactionRecord(t)(newToolDispositionRecord(toolOutcome("not invoked"), call, s.policy.PayloadLimits))
							}
							before := detachedToolAdmission(newAdmission(s.Snapshot()))
							c := interactionCommit(t, s, []*materializedRecord{r})
							var err error
							switch path {
							case "builder":
								b, e := s.BeginCommit()
								interactionCheck(t, e)
								err = b.add(r, nil)
								interactionCheck(t, b.Discard())
							case "preflight":
								err = s.preflight([]*materializedRecord{r}, c.Ref, uint64(len(r.value.Payload)))
							case "apply":
								err = s.apply(c)
							case "load":
								// Isolate each malformed suffix so a previous rejection cannot mask it.
								isolated, _ := internalFixture(t)
								h := isolated.history
								request := history.ReadRequest{Through: s.Snapshot().Head(), MaxCommits: 1}
								for request.After != request.Through {
									page, e := s.history.Read(context.Background(), run.historyID, request)
									interactionCheck(t, e)
									for _, accepted := range page.Commits {
										_, e := h.Append(context.Background(), run.historyID, accepted.Commit)
										interactionCheck(t, e)
									}
									request.After = page.Next
								}
								_, e := h.Append(context.Background(), run.historyID, c.Commit)
								interactionCheck(t, e)
								var loaded *Session
								loaded, err = Load(context.Background(), h, run.historyID, s.policy)
								if loaded != nil {
									t.Fatal("failed load published a partial session")
								}
							}
							if !errors.Is(err, ErrTransition) || !reflect.DeepEqual(before, newAdmission(s.Snapshot())) {
								t.Fatalf("late fact admitted or mutated source: %v", err)
							}
						}
					}
				})
			}
		}
	}
}

func TestToolBulkPreservesEveryExplicitDisposition(t *testing.T) {
	for _, category := range []string{"returned", "error", "indeterminate", "not invoked"} {
		for _, accepted := range []bool{false, true} {
			t.Run(category+"/accepted="+boolLabel(accepted), func(t *testing.T) {
				s, calls := toolAdmissionFixture(t)
				interactionSubmit(t, s, func(b *SessionCommit) {
					interactionCheck(t, b.AddToolInvocation(calls[0], ToolInvocation{Name: "A"}))
				})
				explicit := toolOutcome(category)
				explicit.Diagnostic = new("retained diagnostic")
				position := s.Snapshot().RecordCount()
				if accepted {
					interactionSubmit(t, s, func(b *SessionCommit) {
						interactionCheck(t, b.AddToolDisposition(calls[0], explicit))
					})
				}
				interactionSubmit(t, s, func(b *SessionCommit) {
					if !accepted {
						interactionCheck(t, b.AddToolDisposition(calls[0], explicit))
					}
					interactionCheck(t, b.AddRunEnded(s.Snapshot().ActiveRun(), RunEnded{Reason: RunEndedLimit, BulkToolCloseout: true}))
				})
				explicit.RunID = calls[0].requestID.runID.ordinal
				explicit.RequestID, explicit.CallID = calls[0].requestID.requestOrdinal, calls[0].ordinal
				loaded, err := Load(context.Background(), s.history, s.Snapshot().HistoryID(), s.policy)
				interactionCheck(t, err)
				for _, snap := range []Snapshot{s.Snapshot(), loaded.Snapshot()} {
					v, ok := snap.RecordAt(position).ToolDisposition()
					if !ok || !v.value.Equal(explicit) {
						t.Fatal("bulk replaced explicit category, diagnostic or output")
					}
				}
			})
		}
	}
}

func TestRunEndingExactEscapePolicy(t *testing.T) {
	for _, reason := range []RunEndedReason{RunEndedCompleted, RunEndedError, RunEndedInterrupted, RunEndedLimit, RunEndedCaptureFailed} {
		for _, bulk := range []bool{false, true} {
			for _, mode := range []string{"minimal", "detail", "empty detail", "mixed"} {
				for _, tight := range []bool{false, true} {
					t.Run(fmt.Sprintf("%s/bulk=%t/%s/tight=%t", reason, bulk, mode, tight), func(t *testing.T) {
						s := interactionFixture(t, "bound")
						run := s.Snapshot().ActiveRun()
						if tight {
							s.policy.SoftMaxRetainedRecords = s.state.records
						}
						b, err := s.BeginCommit()
						interactionCheck(t, err)
						ending := RunEnded{Reason: reason, BulkToolCloseout: bulk}
						switch mode {
						case "detail":
							ending.Explanation = new("safe")
						case "empty detail":
							ending.Explanation = new("")
						case "mixed":
							interactionCheck(t, b.ReplaceInstructions(run.historyID, Instructions{}))
						}
						err = b.AddRunEnded(run, ending)
						allowedBulk := reason == RunEndedLimit || reason == RunEndedCaptureFailed
						if bulk && !allowedBulk {
							if !errors.Is(err, codec.ErrInvalidData) {
								t.Fatalf("invalid reason/flag: %v", err)
							}
							interactionCheck(t, b.Discard())
							return
						}
						interactionCheck(t, err)
						before := detachedSessionState(s.state)
						_, err = s.SubmitCommit(context.Background(), b)
						if tight && (!bulk || mode != "minimal") {
							if !errors.Is(err, codec.ErrLimitExceeded) || !reflect.DeepEqual(before, detachedSessionState(s.state)) {
								t.Fatalf("non-emergency bypassed soft limit: %v", err)
							}
						} else {
							interactionCheck(t, err)
							interactionReplay(t, s)
						}
					})
				}
			}
		}
	}
}

func TestToolOversizedDispositionThenMinimalBulk(t *testing.T) {
	s, calls := toolAdmissionFixture(t)
	interactionSubmit(t, s, func(b *SessionCommit) {
		interactionCheck(t, b.AddToolInvocation(calls[0], ToolInvocation{Name: "A"}))
	})
	before := detachedToolAdmission(newAdmission(s.Snapshot()))
	b, err := s.BeginCommit()
	interactionCheck(t, err)
	outcome := toolOutcome("returned")
	outcome.Returned.Output.Parts = []ToolOutputPart{{Content: &ContentPart{Text: new(strings.Repeat("x", s.policy.PayloadLimits.MaxStringBytes+1))}}}
	if err := b.AddToolDisposition(calls[0], outcome); !errors.Is(err, codec.ErrLimitExceeded) || len(b.records) != 0 || !reflect.DeepEqual(before, b.admission) {
		t.Fatalf("oversized disposition changed outstanding call: %v", err)
	}
	s.policy.SoftMaxRetainedRecords = s.state.records
	interactionCheck(t, b.AddRunEnded(s.Snapshot().ActiveRun(), RunEnded{Reason: RunEndedCaptureFailed, BulkToolCloseout: true}))
	_, err = s.SubmitCommit(context.Background(), b)
	interactionCheck(t, err)
	if s.state.view.lifecycle.tools.calls[0].settled || s.state.view.lifecycle.tools.calls[0].invocation == nil {
		t.Fatal("rejected output was retained or invocation lost")
	}
	interactionReplay(t, s)
}

func TestRunEndingReserveMaximumWidthsCumulativeBounds(t *testing.T) {
	for _, dimension := range []string{"records", "bytes", "work"} {
		for _, short := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/short=%t", dimension, short), func(t *testing.T) {
				s := interactionFixture(t, "bound")
				s.state.view.lifecycle.activeRun.ordinal = math.MaxUint64
				s.state.view.lifecycle.runs = math.MaxUint64
				s.state.view.head.Sequence = math.MaxInt64 - 1
				ending := interactionRecord(t)(newRunEndedRecord(RunEnded{Reason: RunEndedCaptureFailed, BulkToolCloseout: true}, s.Snapshot().ActiveRun(), s.policy.PayloadLimits))
				switch dimension {
				case "records":
					s.policy.SoftMaxRetainedRecords = s.state.records
					s.policy.MaxRetainedRecords = s.state.records + 1
					if short {
						s.policy.MaxRetainedRecords--
					}
				case "bytes":
					s.policy.SoftMaxPayloadBytes = s.state.payloadBytes
					s.policy.MaxPayloadBytes = s.state.payloadBytes + uint64(len(ending.value.Payload))
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
				err := s.reserveEnding(s.Snapshot().Head(), s.state.view.lifecycle, s.state.records, s.state.payloadBytes)
				if short {
					if !errors.Is(err, codec.ErrLimitExceeded) {
						t.Fatalf("one-short maximum reserve: %v", err)
					}
					return
				}
				interactionCheck(t, err)
				p, err := s.prepareEnding(RunEndedCaptureFailed)
				interactionCheck(t, err)
				interactionCheck(t, s.apply(history.Commit{Ref: p.ref, Commit: p.commit}))
			})
		}
	}
}
