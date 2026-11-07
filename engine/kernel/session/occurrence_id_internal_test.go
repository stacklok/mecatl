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
)

func TestOccurrenceIDLocalValidation(t *testing.T) {
	s, _ := internalFixture(t)
	run := RunID{historyID: s.Snapshot().HistoryID(), ordinal: 1}
	request := ModelRequestID{runID: run, requestOrdinal: 1}
	for name, value := range map[string]interface{ Validate() error }{
		"zero run":            RunID{},
		"unallocated run":     RunID{historyID: run.historyID},
		"unscoped run":        RunID{ordinal: 1},
		"zero input":          UserInputID{},
		"unallocated input":   UserInputID{runID: run},
		"unscoped input":      UserInputID{ordinal: 1},
		"invalid input run":   UserInputID{runID: RunID{historyID: run.historyID}, ordinal: 1},
		"zero request":        ModelRequestID{},
		"unallocated request": ModelRequestID{runID: run},
		"unscoped request":    ModelRequestID{requestOrdinal: 1},
		"zero call":           RequestedCallID{},
		"unallocated call":    RequestedCallID{requestID: request},
		"unscoped call":       RequestedCallID{ordinal: 1},
	} {
		t.Run(name, func(t *testing.T) {
			if value.Validate() == nil {
				t.Fatal("invalid identity validated")
			}
		})
	}
	for _, n := range []uint64{1, 2, math.MaxUint64} {
		run.ordinal = n
		request = ModelRequestID{runID: run, requestOrdinal: n}
		input := UserInputID{runID: run, ordinal: n}
		call := RequestedCallID{requestID: request, ordinal: n}
		if run.Validate() != nil || input.Validate() != nil || request.Validate() != nil || call.Validate() != nil {
			t.Fatal("positive scoped identity rejected")
		}
		runCopy, inputCopy, requestCopy, callCopy := run, input, request, call
		if !run.Equal(runCopy) || !input.Equal(inputCopy) || !request.Equal(requestCopy) || !call.Equal(callCopy) {
			t.Fatal("identity copy changed equality")
		}
	}
}

func TestSnapshotUserInputByIDMalformedComponents(t *testing.T) {
	s, _ := internalFixture(t)
	var run RunID
	interactionSubmit(t, s, func(b *SessionCommit) {
		interactionCheck(t, b.Create())
		var err error
		run, err = b.StartRun()
		interactionCheck(t, err)
		_, err = b.AddUserInput(AddUserInputRequest{RunID: run})
		interactionCheck(t, err)
	})
	for _, n := range []uint64{0, 1<<32 + 1, 1<<32 + 2, 1<<63 + 1, math.MaxUint64} {
		badRun := run
		badRun.ordinal = n
		for _, id := range []UserInputID{{runID: run, ordinal: n}, {runID: badRun, ordinal: 1}} {
			if got, ok := s.Snapshot().UserInputByID(id); ok || got != (UserInputView{}) {
				t.Fatalf("malformed lookup returned a view: %v", id)
			}
		}
	}
	for _, id := range []UserInputID{{ordinal: 1}, {runID: RunID{ordinal: 1}, ordinal: 1}} {
		if got, ok := s.Snapshot().UserInputByID(id); ok || got != (UserInputView{}) {
			t.Fatal("unscoped lookup returned a view")
		}
	}

}

// Extreme component values belong in internal tests, not public ID constructors.
func TestEffectiveInputExtremeReferenceAllPaths(t *testing.T) {
	for _, accepted := range []bool{false, true} {
		for _, reverse := range []bool{false, true} {
			for _, path := range []string{"builder", "apply", "load"} {
				t.Run(fmt.Sprintf("accepted=%t/reverse=%t/%s", accepted, reverse, path), func(t *testing.T) {
					s := interactionFixture(t, "active")
					run := s.Snapshot().ActiveRun()
					b, err := s.BeginCommit()
					interactionCheck(t, err)
					first, err := b.AddUserInput(AddUserInputRequest{RunID: run})
					interactionCheck(t, err)
					if accepted {
						_, err = s.SubmitCommit(context.Background(), b)
						interactionCheck(t, err)
						b, err = s.BeginCommit()
						interactionCheck(t, err)
					}
					missing := UserInputID{runID: run, ordinal: math.MaxUint64}
					refs := []InputItem{{UserInputID: &first}, {UserInputID: &missing}}
					if reverse {
						refs[0], refs[1] = refs[1], refs[0]
					}
					v := EffectiveInput{RunID: run, Inputs: refs}
					before := detachedSessionState(s.state)
					if path == "builder" {
						prior, count := b.admission, len(b.records)
						err = b.AddEffectiveInput(v)
						if b.admission != prior || len(b.records) != count {
							t.Fatal("extreme reference addition changed batch")
						}
						interactionCheck(t, b.Discard())
					} else {
						records := append([]Record(nil), b.records...)
						interactionCheck(t, b.Discard())
						r := interactionRecord(t)(NewEffectiveInputRecord(v, s.policy.PayloadLimits))
						r, err = DecodeRecord(run.historyID, r.Format(), s.policy.PayloadLimits)
						interactionCheck(t, err)
						c := interactionCommit(t, s, append(records, r))
						if path == "apply" {
							err = s.Apply(c)
						} else {
							result, appendErr := s.history.Append(context.Background(), run.historyID, c.Commit)
							interactionCheck(t, appendErr)
							if result.Status != history.StatusAccepted {
								t.Fatal("history rejected locally valid extreme reference")
							}
							loaded, loadErr := Load(context.Background(), s.history, run.historyID, s.policy)
							err = loadErr
							if loaded != nil {
								t.Fatal("failed replay returned a session")
							}
						}
					}
					if !errors.Is(err, ErrTransition) || !reflect.DeepEqual(before, detachedSessionState(s.state)) {
						t.Fatalf("extreme reference replay published state: %v", err)
					}
				})
			}
		}
	}
}

func TestOccurrenceAllocationExhaustion(t *testing.T) {
	if next, err := nextOrdinal(math.MaxUint64); next != 0 || !errors.Is(err, codec.ErrLimitExceeded) {
		t.Fatalf("exhaustion must return zero and a resource-limit error: %d, %v", next, err)
	}
	if next, err := nextOrdinal(math.MaxUint64 - 1); next != math.MaxUint64 || err != nil {
		t.Fatalf("last representable occurrence: %d, %v", next, err)
	}
}
