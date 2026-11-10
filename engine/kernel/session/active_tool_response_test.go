package session

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"testing"

	"github.com/stacklok/mecatl/engine/kernel/codec"
	"github.com/stacklok/mecatl/engine/kernel/history"
	"github.com/stacklok/mecatl/engine/kernel/history/format"
	historyid "github.com/stacklok/mecatl/engine/kernel/history/id"
	sessionformat "github.com/stacklok/mecatl/engine/kernel/session/format"
)

func toolAdmissionFixture(t testing.TB) (*Session, []RequestedCallID) {
	t.Helper()
	s := interactionFixture(t, "pending")
	request := interactionRequest(s, 1).ID
	interactionSubmit(t, s, func(b *SessionCommit) {
		interactionCheck(t, b.AddModelResponse(request, ModelResponse{Terminal: ResponseTerminalNormal, Items: []ResponseItem{
			{Call: &RequestedCall{Name: "A"}},
			{Call: &RequestedCall{Name: "B"}},
			{Call: &RequestedCall{Name: "C"}},
		}}))
	})
	return s, []RequestedCallID{{requestID: request, ordinal: 1}, {requestID: request, ordinal: 2}, {requestID: request, ordinal: 3}}
}

func toolOutcome(category string) ToolDisposition {
	switch category {
	case "returned", "error":
		status := ToolReturnSuccess
		if category == "error" {
			status = ToolReturnError
		}
		return ToolDisposition{Returned: &ToolReturned{Status: status, Output: ToolOutput{Capture: ToolCaptureComplete}}}
	case "indeterminate":
		return ToolDisposition{Indeterminate: &ToolIndeterminate{Reason: ToolIndeterminateOther, Output: ToolOutput{Capture: ToolCaptureOmitted}}}
	default:
		return ToolDisposition{NotInvoked: &ToolNotInvoked{Reason: ToolNotInvokedOther}}
	}
}

// detachedToolAdmission is an independent negative control for mutable call flags,
// not just another shallow copy of the context being tested.
func detachedToolAdmission(a admissionContext) admissionContext {
	for _, s := range []*lifecycleState{&a.source, &a.candidate} {
		if s.tools != nil {
			s.tools = &activeToolResponse{request: s.tools.request, calls: slices.Clone(s.tools.calls)}
		}
	}
	return a
}

func TestToolAdmissionCopiedCandidates(t *testing.T) {
	s, calls := toolAdmissionFixture(t)
	pinned := s.Snapshot()
	interactionSubmit(t, s, func(b *SessionCommit) {
		interactionCheck(t, b.AddToolInvocation(calls[1], ToolInvocation{Name: "B", Prerequisites: []uint64{1}}))
	})
	a := newAdmission(s.Snapshot())
	before := detachedToolAdmission(a)
	invocation := interactionRecord(t)(newToolInvocationRecord(ToolInvocation{Name: "A"}, calls[0], s.policy.PayloadLimits))
	disposition := interactionRecord(t)(newToolDispositionRecord(toolOutcome("not invoked"), calls[0], s.policy.PayloadLimits))
	left, right := a, a
	interactionCheck(t, left.admit(s.Snapshot().HistoryID(), invocation))
	interactionCheck(t, right.admit(s.Snapshot().HistoryID(), disposition))
	if !reflect.DeepEqual(a, before) || !reflect.DeepEqual(left.source, before.source) || !reflect.DeepEqual(right.source, before.source) {
		t.Fatal("independent candidate mutated shared source/original")
	}
	if left.candidate.tools.calls[0].settled || right.candidate.tools.calls[0].invocation != nil {
		t.Fatal("independent candidates contaminated one another")
	}
	// A candidate settlement must not satisfy B's accepted prerequisite. Failure
	// after a successful earlier mutation must preserve even the copied candidate.
	prior := detachedToolAdmission(right)
	returned := interactionRecord(t)(newToolDispositionRecord(toolOutcome("returned"), calls[1], s.policy.PayloadLimits))
	if err := right.admit(s.Snapshot().HistoryID(), returned); !errors.Is(err, ErrTransition) || !reflect.DeepEqual(right, prior) {
		t.Fatalf("failed prerequisite admission mutated candidate: %v", err)
	}
	if pinned.lifecycle.tools.calls[1].invocation != nil || s.Snapshot().lifecycle.tools.calls[0].settled {
		t.Fatal("candidate mutated pinned snapshots")
	}
}

func TestToolAdmissionSourceCandidateMatrix(t *testing.T) {
	for _, category := range []string{"returned", "error", "indeterminate", "not invoked"} {
		for _, acceptedInvocation := range []bool{false, true} {
			for _, candidateInvocation := range []bool{false, true} {
				t.Run(category+"/accepted="+boolLabel(acceptedInvocation)+"/candidate="+boolLabel(candidateInvocation), func(t *testing.T) {
					s, calls := toolAdmissionFixture(t)
					if acceptedInvocation {
						interactionSubmit(t, s, func(b *SessionCommit) { interactionCheck(t, b.AddToolInvocation(calls[0], ToolInvocation{Name: "A"})) })
					}
					b, err := s.BeginCommit()
					interactionCheck(t, err)
					if candidateInvocation {
						err := b.AddToolInvocation(calls[0], ToolInvocation{Name: "A"})
						if acceptedInvocation {
							if !errors.Is(err, ErrTransition) {
								t.Fatalf("duplicate invocation: %v", err)
							}
						} else {
							interactionCheck(t, err)
						}
					}
					before := detachedToolAdmission(b.admission)
					err = b.AddToolDisposition(calls[0], toolOutcome(category))
					if acceptedInvocation || category == "not invoked" {
						interactionCheck(t, err)
						if b.admission.candidate.calls != 2 {
							t.Fatal("category did not settle")
						}
						prior := detachedToolAdmission(b.admission)
						for _, err := range []error{b.AddToolDisposition(calls[0], toolOutcome(category)), b.AddToolInvocation(calls[0], ToolInvocation{Name: "late"})} {
							if !errors.Is(err, ErrTransition) {
								t.Fatalf("post-settlement fact: %v", err)
							}
						}
						if !reflect.DeepEqual(b.admission, prior) {
							t.Fatal("bad add changed candidate")
						}
						_, err = s.SubmitCommit(context.Background(), b)
						interactionCheck(t, err)
						interactionReplay(t, s)
					} else {
						if !errors.Is(err, ErrTransition) || !reflect.DeepEqual(b.admission, before) {
							t.Fatalf("unaccepted invocation outcome: %v", err)
						}
						interactionCheck(t, b.Discard())
					}
				})
			}
		}
	}
}

func boolLabel(v bool) string {
	if v {
		return "yes"
	}
	return "no"
}

func TestToolAdmissionPrerequisiteSettlement(t *testing.T) {
	for _, category := range []string{"returned", "error", "indeterminate", "not invoked"} {
		for _, returned := range []string{"returned", "error"} {
			for _, accepted := range []bool{false, true} {
				t.Run(category+"/B="+returned+"/accepted="+boolLabel(accepted), func(t *testing.T) {
					s, calls := toolAdmissionFixture(t)
					// Requested A precedes B, but B's invocation may be committed first.
					interactionSubmit(t, s, func(b *SessionCommit) {
						interactionCheck(t, b.AddToolInvocation(calls[1], ToolInvocation{Name: "B", Prerequisites: []uint64{1}}))
						interactionCheck(t, b.AddToolInvocation(calls[2], ToolInvocation{Name: "C", Prerequisites: []uint64{2, 1}}))
					})
					interactionSubmit(t, s, func(b *SessionCommit) {
						interactionCheck(t, b.AddToolInvocation(calls[0], ToolInvocation{Name: "A"}))
					})
					if accepted {
						interactionSubmit(t, s, func(b *SessionCommit) { interactionCheck(t, b.AddToolDisposition(calls[0], toolOutcome(category))) })
					}
					b, err := s.BeginCommit()
					interactionCheck(t, err)
					if !accepted {
						interactionCheck(t, b.AddToolDisposition(calls[0], toolOutcome(category)))
					}
					before := detachedToolAdmission(b.admission)
					err = b.AddToolDisposition(calls[1], toolOutcome(returned))
					if accepted {
						interactionCheck(t, err)
						// Even with A accepted and B now candidate-settled, C must wait.
						before = detachedToolAdmission(b.admission)
						if err := b.AddToolDisposition(calls[2], toolOutcome(returned)); !errors.Is(err, ErrTransition) || !reflect.DeepEqual(before, b.admission) {
							t.Fatalf("candidate B unblocked C: %v", err)
						}
					} else if !errors.Is(err, ErrTransition) || !reflect.DeepEqual(before, b.admission) {
						t.Fatalf("candidate prerequisite unblocked return: %v", err)
					}
					// Indeterminate can settle despite unaccepted/unsettled prerequisites.
					interactionCheck(t, b.AddToolDisposition(calls[2], toolOutcome("indeterminate")))
					_, err = s.SubmitCommit(context.Background(), b)
					interactionCheck(t, err)
					interactionReplay(t, s)
				})
			}
		}
	}
}

func TestToolAdmissionMultiplePrerequisiteSettlement(t *testing.T) {
	for _, returned := range []string{"returned", "error"} {
		t.Run(returned, func(t *testing.T) {
			s, calls := toolAdmissionFixture(t)
			interactionSubmit(t, s, func(b *SessionCommit) {
				interactionCheck(t, b.AddToolInvocation(calls[1], ToolInvocation{Name: "B", Prerequisites: []uint64{1}}))
				interactionCheck(t, b.AddToolInvocation(calls[2], ToolInvocation{Name: "C", Prerequisites: []uint64{2, 1}}))
			})
			// B may settle indeterminately without A. It is C's first prerequisite;
			// its second prerequisite A remains unsettled.
			interactionSubmit(t, s, func(b *SessionCommit) {
				interactionCheck(t, b.AddToolDisposition(calls[1], toolOutcome("indeterminate")))
			})
			b, err := s.BeginCommit()
			interactionCheck(t, err)
			before := detachedToolAdmission(b.admission)
			if err := b.AddToolDisposition(calls[2], toolOutcome(returned)); !errors.Is(err, ErrTransition) || !reflect.DeepEqual(before, b.admission) {
				t.Fatalf("accepted first prerequisite bypassed unsettled second: %v", err)
			}
			interactionCheck(t, b.Discard())

			// A and B are both accepted before C's returned result, so every
			// Returned status must admit and replay through real history.
			s, calls = toolAdmissionFixture(t)
			interactionSubmit(t, s, func(b *SessionCommit) {
				interactionCheck(t, b.AddToolInvocation(calls[1], ToolInvocation{Name: "B", Prerequisites: []uint64{1}}))
				interactionCheck(t, b.AddToolInvocation(calls[2], ToolInvocation{Name: "C", Prerequisites: []uint64{2, 1}}))
				interactionCheck(t, b.AddToolInvocation(calls[0], ToolInvocation{Name: "A"}))
			})
			interactionSubmit(t, s, func(b *SessionCommit) {
				interactionCheck(t, b.AddToolDisposition(calls[0], toolOutcome("not invoked")))
			})
			interactionSubmit(t, s, func(b *SessionCommit) {
				interactionCheck(t, b.AddToolDisposition(calls[1], toolOutcome(returned)))
			})
			interactionSubmit(t, s, func(b *SessionCommit) {
				interactionCheck(t, b.AddToolDisposition(calls[2], toolOutcome(returned)))
			})
			interactionReplay(t, s)
		})
	}
}

func TestToolAdmissionSettlementAdvancement(t *testing.T) {
	for _, input := range []string{"none", "user", "effective"} {
		t.Run(input, func(t *testing.T) {
			s, calls := toolAdmissionFixture(t)
			b, err := s.BeginCommit()
			interactionCheck(t, err)
			run := s.Snapshot().ActiveRun()
			if _, err := b.AddUserInput(run, UserInput{}); !errors.Is(err, ErrTransition) {
				t.Fatalf("unsettled user input: %v", err)
			}
			if err := b.AddEffectiveInput(run, EffectiveInput{}); !errors.Is(err, ErrTransition) {
				t.Fatalf("unsettled effective input: %v", err)
			}
			for _, call := range calls {
				interactionCheck(t, b.AddToolDisposition(call, toolOutcome("not invoked")))
			}
			request := interactionRequest(s, 2).ModelRequest
			if _, err := b.AddModelRequest(run, request); !errors.Is(err, ErrTransition) {
				t.Fatalf("candidate settlement manufactured source eligibility: %v", err)
			}
			switch input {
			case "user":
				_, err := b.AddUserInput(run, UserInput{})
				interactionCheck(t, err)
			case "effective":
				interactionCheck(t, b.AddEffectiveInput(run, EffectiveInput{Inputs: []InputItem{{Inline: &AttributedInput{Actor: "harness", Purpose: PromptContext, Source: ContentSource{Description: "next"}}}}}))
			}
			_, err = s.SubmitCommit(context.Background(), b)
			interactionCheck(t, err)
			interactionSubmit(t, s, func(b *SessionCommit) {
				_, err := b.AddModelRequest(run, interactionRequest(s, 2).ModelRequest)
				interactionCheck(t, err)
			})
			interactionReplay(t, s)
		})
	}
}

func TestToolAdmissionRejectedReplayAndPreflight(t *testing.T) {
	for _, scenario := range []string{"response invocation", "response disposition", "invocation returned", "invocation indeterminate", "candidate prerequisite", "candidate prerequisite error", "duplicate invocation", "duplicate disposition", "invocation after disposition", "candidate next request", "absent call", "absent request", "wrong run", "old run"} {
		for _, path := range []string{"builder", "preflight", "apply", "load"} {
			t.Run(scenario+"/"+path, func(t *testing.T) {
				s, calls := toolAdmissionFixture(t)
				var records []*materializedRecord
				inv := func(call RequestedCallID, prereqs ...uint64) *materializedRecord {
					return interactionRecord(t)(newToolInvocationRecord(ToolInvocation{Name: "tool", Prerequisites: prereqs}, call, s.policy.PayloadLimits))
				}
				disp := func(call RequestedCallID, category string) *materializedRecord {
					return interactionRecord(t)(newToolDispositionRecord(toolOutcome(category), call, s.policy.PayloadLimits))
				}
				switch scenario {
				case "response invocation", "response disposition":
					s = interactionFixture(t, "pending")
					request := interactionRequest(s, 1).ID
					calls[0] = RequestedCallID{requestID: request, ordinal: 1}
					response := interactionRecord(t)(newModelResponseRecord(ModelResponse{Terminal: ResponseTerminalNormal, Items: []ResponseItem{{Call: &RequestedCall{Name: "A"}}}}, request, s.policy.PayloadLimits))
					records = []*materializedRecord{response}
					if scenario == "response invocation" {
						records = append(records, inv(calls[0]))
					} else {
						records = append(records, disp(calls[0], "not invoked"))
					}
				case "invocation returned", "invocation indeterminate":
					category := "returned"
					if scenario == "invocation indeterminate" {
						category = "indeterminate"
					}
					records = []*materializedRecord{inv(calls[0]), disp(calls[0], category)}
				case "candidate prerequisite", "candidate prerequisite error":
					interactionSubmit(t, s, func(b *SessionCommit) {
						interactionCheck(t, b.AddToolInvocation(calls[1], ToolInvocation{Name: "B", Prerequisites: []uint64{1}}))
					})
					returned := "returned"
					if scenario == "candidate prerequisite error" {
						returned = "error"
					}
					records = []*materializedRecord{disp(calls[0], "not invoked"), disp(calls[1], returned)}
				case "candidate next request":
					for _, call := range calls {
						records = append(records, disp(call, "not invoked"))
					}
					records = append(records, interactionRecord(t)(makeRequestRecord(interactionRequest(s, 2), s.policy.PayloadLimits)))
				case "duplicate invocation":
					records = []*materializedRecord{inv(calls[0]), inv(calls[0])}
				case "duplicate disposition":
					records = []*materializedRecord{disp(calls[0], "not invoked"), disp(calls[0], "not invoked")}
				case "invocation after disposition":
					records = []*materializedRecord{disp(calls[0], "not invoked"), inv(calls[0])}
				default:
					call := calls[0]
					switch scenario {
					case "absent call":
						call.ordinal = 4
					case "absent request":
						call.requestID.requestOrdinal++
					case "wrong run":
						call.requestID.runID.ordinal++
					case "old run":
						interactionSubmit(t, s, func(b *SessionCommit) {
							for _, call := range calls {
								interactionCheck(t, b.AddToolDisposition(call, toolOutcome("not invoked")))
							}
							interactionCheck(t, b.EndRun(RunEndedInterrupted))
						})
						interactionSubmit(t, s, func(b *SessionCommit) { _, err := b.StartRun(); interactionCheck(t, err) })
					}
					records = []*materializedRecord{inv(call)}
				}
				before := detachedToolAdmission(newAdmission(s.Snapshot()))
				commit := interactionCommit(t, s, records)
				var err error
				switch path {
				case "builder":
					b, e := s.BeginCommit()
					interactionCheck(t, e)
					for _, r := range records {
						prior := detachedToolAdmission(b.admission)
						err = b.add(r, nil)
						if err != nil {
							if !reflect.DeepEqual(prior, b.admission) {
								t.Fatal("failed add mutated candidate")
							}
							break
						}
					}
					interactionCheck(t, b.Discard())
				case "preflight":
					err = s.preflight(records, commit.Ref, 0)
				case "apply":
					err = s.apply(commit)
				case "load":
					result, e := s.history.Append(context.Background(), s.Snapshot().HistoryID(), commit.Commit)
					interactionCheck(t, e)
					if result.Status != history.StatusAccepted {
						t.Fatal("History rejected opaque facts")
					}
					var loaded *Session
					loaded, err = Load(context.Background(), s.history, s.Snapshot().HistoryID(), s.policy)
					if loaded != nil {
						t.Fatal("failed load published partial session")
					}
				}
				if !errors.Is(err, ErrTransition) {
					t.Fatalf("invalid tool facts: %v", err)
				}
				if !reflect.DeepEqual(before, newAdmission(s.Snapshot())) {
					t.Fatal("rejected batch mutated source")
				}
			})
		}
	}
}

func TestToolDispositionMalformedStorageLoadIsAtomic(t *testing.T) {
	s, _ := toolAdmissionFixture(t)
	before := s.Snapshot()
	malformed := &materializedRecord{value: format.Record{
		Kind:        sessionformat.KindToolDisposition,
		KindVersion: sessionformat.Version,
		Payload:     []byte{0xff},
	}}
	commit := interactionCommit(t, s, []*materializedRecord{malformed})
	result, err := s.history.Append(context.Background(), s.Snapshot().HistoryID(), commit.Commit)
	interactionCheck(t, err)
	if result.Status != history.StatusAccepted {
		t.Fatal("history rejected opaque malformed tool record")
	}
	loaded, err := Load(context.Background(), s.history, s.Snapshot().HistoryID(), s.policy)
	if loaded != nil || !errors.Is(err, codec.ErrInvalidData) {
		t.Fatalf("malformed tool disposition Load = %v, %v", loaded, err)
	}
	if !reflect.DeepEqual(before, s.Snapshot()) {
		t.Fatal("failed storage decode changed submitted session")
	}
}

func TestToolAdmissionOptionalPrerequisiteSettlement(t *testing.T) {
	for _, category := range []string{"indeterminate", "not invoked"} {
		t.Run(category, func(t *testing.T) {
			s, calls := toolAdmissionFixture(t)
			interactionSubmit(t, s, func(b *SessionCommit) {
				interactionCheck(t, b.AddToolInvocation(calls[1], ToolInvocation{Name: "B", Prerequisites: []uint64{1}}))
			})
			interactionSubmit(t, s, func(b *SessionCommit) {
				// A has neither invocation nor disposition, yet these categories
				// can settle B. Return A later, independently of response order.
				interactionCheck(t, b.AddToolDisposition(calls[1], toolOutcome(category)))
				interactionCheck(t, b.AddToolInvocation(calls[0], ToolInvocation{Name: "A"}))
			})
			interactionSubmit(t, s, func(b *SessionCommit) {
				interactionCheck(t, b.AddToolDisposition(calls[2], toolOutcome("not invoked")))
				interactionCheck(t, b.AddToolDisposition(calls[0], toolOutcome("returned")))
				interactionCheck(t, b.EndRun(RunEndedCompleted))
			})
			interactionReplay(t, s)
		})
	}
}

func TestToolAdmissionDestinationMembership(t *testing.T) {
	for _, destination := range []string{"absent call", "absent request", "wrong run", "foreign history", "ended run", "old response"} {
		for _, kind := range []string{"invocation", "returned", "indeterminate", "not invoked"} {
			t.Run(destination+"/"+kind, func(t *testing.T) {
				s, calls := toolAdmissionFixture(t)
				call := calls[0]
				want := ErrTransition
				switch destination {
				case "absent call":
					call.ordinal = 4
				case "absent request":
					call.requestID.requestOrdinal++
				case "wrong run":
					call.requestID.runID.ordinal++
				case "foreign history":
					call.requestID.runID.historyID = historyid.NewHistoryID()
					want = ErrIdentity
				case "ended run", "old response":
					interactionSubmit(t, s, func(b *SessionCommit) {
						for _, call := range calls {
							interactionCheck(t, b.AddToolDisposition(call, toolOutcome("not invoked")))
						}
						if destination == "ended run" {
							interactionCheck(t, b.EndRun(RunEndedCompleted))
						}
					})
					if destination == "old response" {
						var request ModelRequestID
						interactionSubmit(t, s, func(b *SessionCommit) {
							id, err := b.AddModelRequest(s.Snapshot().ActiveRun(), interactionRequest(s, 2).ModelRequest)
							interactionCheck(t, err)
							request = id
						})
						interactionSubmit(t, s, func(b *SessionCommit) {
							interactionCheck(t, b.AddModelResponse(request, ModelResponse{Terminal: ResponseTerminalNormal, Items: []ResponseItem{{Call: &RequestedCall{Name: "new A"}}}}))
						})
					}
				}
				b, err := s.BeginCommit()
				interactionCheck(t, err)
				before := detachedToolAdmission(b.admission)
				if kind == "invocation" {
					err = b.AddToolInvocation(call, ToolInvocation{Name: "tool"})
				} else {
					err = b.AddToolDisposition(call, toolOutcome(kind))
				}
				if !errors.Is(err, want) || !reflect.DeepEqual(before, b.admission) || len(b.records) != 0 {
					t.Fatalf("invalid destination changed candidate: %v", err)
				}
				interactionCheck(t, b.Discard())
			})
		}
	}
}

func TestToolAdmissionInvalidPrerequisitesPreserveCandidate(t *testing.T) {
	s, calls := toolAdmissionFixture(t)
	b, err := s.BeginCommit()
	interactionCheck(t, err)
	defer b.Discard() //nolint:errcheck // Test-only unsubmitted candidate.
	for _, prerequisites := range [][]uint64{{0}, {2}, {3}, {4}, {1, 1}} {
		before := detachedToolAdmission(b.admission)
		if err := b.AddToolInvocation(calls[1], ToolInvocation{Name: "B", Prerequisites: prerequisites}); !errors.Is(err, codec.ErrInvalidData) || !reflect.DeepEqual(before, b.admission) {
			t.Fatalf("invalid prerequisite %v: %v", prerequisites, err)
		}
	}
}
