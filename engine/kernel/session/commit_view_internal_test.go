package session

import (
	"errors"
	"reflect"
	"testing"

	"github.com/stacklok/mecatl/engine/kernel/codec"
	"github.com/stacklok/mecatl/engine/kernel/history"
)

func captureCommit(t testing.TB, b *SessionCommit) CommitView {
	t.Helper()
	v, err := b.View()
	interactionCheck(t, err)
	return v
}

func TestModelRequestCaptureFreshness(t *testing.T) {
	for _, scenario := range []string{"zero", "foreign", "discarded", "copied batch", "stale empty context", "stale input", "failed addition", "current"} {
		t.Run(scenario, func(t *testing.T) {
			s := interactionFixture(t, "active")
			run := s.Snapshot().ActiveRun()
			b, err := s.BeginCommit()
			interactionCheck(t, err)
			view := captureCommit(t, b)
			destination := b
			want := ErrIdentity
			switch scenario {
			case "zero":
				view = CommitView{}
			case "foreign":
				other := interactionFixture(t, "active") // Identical scope, head and prefix length.
				foreign, err := other.BeginCommit()
				interactionCheck(t, err)
				view = captureCommit(t, foreign)
				interactionCheck(t, foreign.Discard())
			case "discarded":
				interactionCheck(t, b.Discard())
				b, err = s.BeginCommit()
				interactionCheck(t, err)
				destination = b
			case "copied batch":
				copied := *b
				destination = &copied
				want = ErrBatch
			case "stale empty context":
				interactionCheck(t, b.ReplaceRootInstructions(run.historyID, RootInstructions{}))
			case "stale input":
				_, err := b.AddUserInput(run, UserInput{})
				interactionCheck(t, err)
			case "failed addition":
				if err := b.Create(); !errors.Is(err, ErrTransition) {
					t.Fatal("invalid add succeeded")
				}
				want = nil
			case "current":
				want = nil
			}
			before, count := b.admission, len(b.records)
			payload := ModelRequest{Recipe: ConstructionRecipe{Algorithm: "a", Version: 1, Check: []byte{1}}}
			id, err := destination.AddModelRequest(run, view, payload)
			if !errors.Is(err, want) {
				t.Fatalf("attachment = %v; want %v", err, want)
			}
			if want != nil {
				if id != (ModelRequestID{}) || b.admission != before || len(b.records) != count {
					t.Fatal("failed capture attachment changed batch")
				}
				id, err = b.AddModelRequest(run, captureCommit(t, b), payload)
				interactionCheck(t, err)
			}
			if id.requestOrdinal != 1 {
				t.Fatal("capture failure consumed identity")
			}
			_, err = s.SubmitCommit(t.Context(), b)
			interactionCheck(t, err)
			interactionReplay(t, s)
			if _, err := b.AddModelRequest(run, view, payload); !errors.Is(err, ErrBatch) {
				t.Fatal("closed attachment")
			}
		})
	}
}

func TestModelRequestGenesisCaptureSubmitLoad(t *testing.T) {
	s, _ := internalFixture(t)
	b, err := s.BeginCommit()
	interactionCheck(t, err)
	interactionCheck(t, b.Create())
	run, err := b.StartRun()
	interactionCheck(t, err)
	interactionCheck(t, b.ReplaceModelBinding(run.historyID, ModelBinding{Route: "r", API: "a", Model: "m"}))
	interactionCheck(t, b.ReplaceRootInstructions(run.historyID, RootInstructions{}))
	interactionCheck(t, b.ReplaceUserInstructions(run.historyID, UserInstructions{}))
	input, err := b.AddUserInput(run, UserInput{Parts: []ContentPart{{Text: new("hello")}}})
	interactionCheck(t, err)
	interactionCheck(t, b.AddEffectiveInput(run, EffectiveInput{Inputs: []InputItem{{UserInputID: new(input.ordinal)}}}))
	view := captureCommit(t, b)
	id, err := b.AddModelRequest(run, view, ModelRequest{Recipe: ConstructionRecipe{Algorithm: "a", Version: 1, Check: []byte{1}}})
	interactionCheck(t, err)
	if view.RecordCount() != 7 || b.admission.candidate.captured != 2 {
		t.Fatal("genesis candidate coverage")
	}
	if err := b.AddModelResponse(id, ModelResponse{Terminal: ResponseTerminalNormal}); !errors.Is(err, ErrTransition) {
		t.Fatal("same-batch outcome admitted")
	}
	if err := b.AddInvocationFailure(id, InvocationFailure{Class: InvocationFailureTimeout}); !errors.Is(err, ErrTransition) {
		t.Fatal("same-batch failure admitted")
	}
	if err := b.AddRunEnded(run, RunEnded{Reason: RunEndedCaptureFailed, Capture: &CaptureFailureEvidence{RequestID: id.requestOrdinal}}); !errors.Is(err, ErrTransition) {
		t.Fatal("same-batch capture admitted")
	}
	_, err = s.SubmitCommit(t.Context(), b)
	interactionCheck(t, err)
	interactionReplay(t, s)
	loaded, err := Load(t.Context(), s.history, run.historyID, s.policy)
	interactionCheck(t, err)
	for _, snapshot := range []Snapshot{s.Snapshot(), loaded.Snapshot()} {
		request, ok := snapshot.RecordAt(7).ModelRequest()
		if !ok || request.ID() != id {
			t.Fatal("genesis request lost")
		}
		if source, present := request.Source(); present || source != (SourceHeadView{}) {
			t.Fatal("genesis anchor became present")
		}
		value, err := codec.Decode[ModelRequest](snapshot.RecordAt(7).MutableClone().Payload, s.policy.PayloadLimits)
		interactionCheck(t, err)
		if value.Source != nil {
			t.Fatal("genesis encoded zero CID link")
		}
	}
	interactionSubmit(t, s, func(b *SessionCommit) {
		interactionCheck(t, b.AddModelResponse(id, ModelResponse{Terminal: ResponseTerminalNormal}))
		interactionCheck(t, b.EndRun(RunEndedCompleted))
	})
	interactionReplay(t, s)
}

func TestModelRequestAfterAcceptedOutcomeOrSettlement(t *testing.T) {
	for _, outcome := range []string{"response", "failure", "returned", "error", "indeterminate", "not invoked"} {
		t.Run(outcome, func(t *testing.T) {
			var s *Session
			var calls []RequestedCallID
			if outcome == "response" || outcome == "failure" {
				s = interactionFixture(t, "pending")
			} else {
				s, calls = toolAdmissionFixture(t)
				if outcome != "not invoked" {
					interactionSubmit(t, s, func(b *SessionCommit) {
						for _, call := range calls {
							interactionCheck(t, b.AddToolInvocation(call, ToolInvocation{Name: "effective"}))
						}
					})
				}
			}
			previous := interactionRequest(s, 1).ID
			run := s.Snapshot().ActiveRun()
			b, err := s.BeginCommit()
			interactionCheck(t, err)
			switch outcome {
			case "response":
				interactionCheck(t, b.AddModelResponse(previous, ModelResponse{Terminal: ResponseTerminalNormal}))
			case "failure":
				interactionCheck(t, b.AddInvocationFailure(previous, InvocationFailure{Class: InvocationFailureTimeout}))
			default:
				for _, call := range calls {
					interactionCheck(t, b.AddToolDisposition(call, toolOutcome(outcome)))
				}
			}
			v := captureCommit(t, b)
			id, err := b.AddModelRequest(run, v, ModelRequest{Recipe: ConstructionRecipe{Algorithm: "a", Version: 1, Check: []byte{1}}, RetryOf: new(previous.requestOrdinal)})
			interactionCheck(t, err)
			if id.requestOrdinal != 2 {
				t.Fatal("wrong successor identity")
			}
			if err := b.AddModelResponse(id, ModelResponse{Terminal: ResponseTerminalNormal}); !errors.Is(err, ErrTransition) {
				t.Fatal("successor outcome bypassed acceptance")
			}
			_, err = s.SubmitCommit(t.Context(), b)
			interactionCheck(t, err)
			interactionReplay(t, s)
			request, _ := s.Snapshot().RecordAt(v.RecordCount()).ModelRequest()
			if retry, ok := request.RetryOf(); !ok || retry != previous {
				t.Fatal("accepted same-run retry lost")
			}
		})
	}
}

func TestModelRequestRetryCannotUsePrecedingRun(t *testing.T) {
	s := interactionFixture(t, "responded")
	b, err := s.BeginCommit()
	interactionCheck(t, err)
	interactionCheck(t, b.EndRun(RunEndedCompleted))
	run, err := b.StartRun()
	interactionCheck(t, err)
	v := captureCommit(t, b)
	payload := ModelRequest{Recipe: ConstructionRecipe{Algorithm: "a", Version: 1, Check: []byte{1}}, RetryOf: new(uint64(1))}
	before, count := b.admission, len(b.records)
	if id, err := b.AddModelRequest(run, v, payload); id != (ModelRequestID{}) || !errors.Is(err, ErrTransition) || b.admission != before || len(b.records) != count {
		t.Fatalf("old-run retry consumed identity or admitted: %v", err)
	}
	payload.RetryOf = nil
	id, err := b.AddModelRequest(run, v, payload)
	interactionCheck(t, err)
	if id.requestOrdinal != 1 {
		t.Fatal("failed retry consumed identity")
	}
	_, err = s.SubmitCommit(t.Context(), b)
	interactionCheck(t, err)
	interactionReplay(t, s)
}

func TestModelRequestNilAnchorOutsideGenesisAllPaths(t *testing.T) {
	for _, path := range []string{"builder", "preflight", "apply", "load"} {
		t.Run(path, func(t *testing.T) {
			s := interactionFixture(t, "active")
			v := interactionRequest(s, 1)
			v.Source = nil
			r := interactionRecord(t)(makeRequestRecord(v, s.policy.PayloadLimits))
			before := detachedSessionState(s.state)
			var err error
			switch path {
			case "builder":
				b, e := s.BeginCommit()
				interactionCheck(t, e)
				err = b.add(r, nil)
				interactionCheck(t, b.Discard())
			case "preflight":
				_, err = s.prepare([]*materializedRecord{r})
			case "apply":
				err = s.apply(interactionCommit(t, s, []*materializedRecord{r}))
			case "load":
				c := interactionCommit(t, s, []*materializedRecord{r})
				result, e := s.history.Append(t.Context(), s.Snapshot().HistoryID(), c.Commit)
				interactionCheck(t, e)
				if result.Status != history.StatusAccepted {
					t.Fatal("history rejected opaque record")
				}
				var loaded *Session
				loaded, err = Load(t.Context(), s.history, s.Snapshot().HistoryID(), s.policy)
				if loaded != nil {
					t.Fatal("invalid load returned session")
				}
			}
			if !errors.Is(err, ErrIdentity) || !reflect.DeepEqual(before, detachedSessionState(s.state)) {
				t.Fatalf("nil anchor admission: %v", err)
			}
		})
	}
}
