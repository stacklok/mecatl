package session

import (
	"context"
	"errors"
	"math"
	"reflect"
	"testing"

	"github.com/stacklok/mecatl/engine/kernel/codec"
	"github.com/stacklok/mecatl/engine/kernel/history"
	"github.com/stacklok/mecatl/engine/kernel/history/format"
	sessionformat "github.com/stacklok/mecatl/engine/kernel/session/format"
)

func interactionSubmit(t testing.TB, s *Session, add func(*SessionCommit)) {
	t.Helper()
	b, err := s.BeginCommit()
	if err != nil {
		t.Fatal(err)
	}
	add(b)
	if _, err := s.SubmitCommit(context.Background(), b); err != nil {
		t.Fatal(err)
	}
}
func interactionCheck(t testing.TB, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
func interactionRecord(t testing.TB) func(*materializedRecord, error) *materializedRecord {
	return func(r *materializedRecord, err error) *materializedRecord {
		t.Helper()
		interactionCheck(t, err)
		return r
	}
}
func interactionRequest(s *Session, ordinal uint64) identifiedRequest {
	head := s.Snapshot().Head()
	return identifiedRequest{ID: ModelRequestID{runID: RunID{historyID: s.Snapshot().HistoryID(), ordinal: 1}, requestOrdinal: ordinal}, Source: SourceHead{Sequence: head.Sequence, CID: head.CID}, Recipe: ConstructionRecipe{Algorithm: "unknown/construction", Version: 9, Parameters: []byte{0xff, 0}, Check: []byte{1}}}
}
func interactionFixture(t testing.TB, stage string) *Session {
	t.Helper()
	s, _ := internalFixture(t)
	interactionSubmit(t, s, func(b *SessionCommit) {
		interactionCheck(t, b.Create())
		if stage != "idle" {
			_, err := b.StartRun()
			interactionCheck(t, err)
			if stage != "unbound" {
				interactionCheck(t, b.ReplaceModelBinding(ModelBinding{HistoryID: s.Snapshot().HistoryID(), Route: "r", API: "a", Model: "m", Settings: []byte("{}")}))
			}
		}
	})
	if stage == "pending" || stage == "responded" || stage == "calls" || stage == "ended" {
		req := interactionRequest(s, 1)
		interactionSubmit(t, s, func(b *SessionCommit) { interactionCheck(t, b.add(makeRequestRecord(req, s.policy.PayloadLimits))) })
		if stage == "responded" || stage == "calls" {
			response := ModelResponse{RequestID: req.ID, Terminal: ResponseTerminalError}
			if stage == "calls" {
				response.Items = []ResponseItem{{Call: &RequestedCall{Name: "", Arguments: []byte("not JSON")}}}
			}
			interactionSubmit(t, s, func(b *SessionCommit) { interactionCheck(t, b.AddModelResponse(response)) })
		}
		if stage == "ended" {
			interactionSubmit(t, s, func(b *SessionCommit) { interactionCheck(t, b.EndRun(RunEndedInterrupted)) })
		}
	}
	return s
}

// This oracle bypasses the builder and session preparation when asserting replay
// acceptance. History deliberately knows nothing about session lifecycle gates.
func interactionCommit(t testing.TB, s *Session, records []*materializedRecord) history.Commit {
	t.Helper()
	head := s.Snapshot().Head()
	c := format.Commit{FormatVersion: 1, Sequence: head.Sequence + 1, Parent: new(head.CID)}
	for _, r := range records {
		c.Records = append(c.Records, r.format())
	}
	ref, err := s.history.Identify(s.Snapshot().HistoryID(), c)
	interactionCheck(t, err)
	return history.Commit{Ref: ref, Commit: c}
}

// interactionForgedEffectiveReferenceRecord constructs an independently valid DTO
// without the typed record constructor or commit builder. Replay must enforce the
// same contribution frontier when it decodes these bytes.
func interactionForgedEffectiveReferenceRecord(t testing.TB, s *Session, run RunID, input UserInputID) *materializedRecord {
	t.Helper()
	payload, err := codec.Encode(sessionformat.EffectiveInput{
		RunID:  run.ordinal,
		Inputs: []sessionformat.InputItem{{UserInputID: new(input.ordinal)}},
	}, s.policy.PayloadLimits)
	interactionCheck(t, err)
	r, err := decodeMaterialized(format.Record{
		Kind:        sessionformat.KindEffectiveInput,
		KindVersion: sessionformat.Version,
		Payload:     payload,
	}, s.policy.PayloadLimits)
	interactionCheck(t, err)
	return r
}

// interactionPendingAfterEffectiveReference accepts an empty authored submission
// and a nonempty reference contribution before requesting an invocation.
func interactionPendingAfterEffectiveReference(t testing.TB) (*Session, RunID, UserInputID, identifiedRequest) {
	t.Helper()
	s := interactionFixture(t, "active")
	run := s.Snapshot().ActiveRun()
	var input UserInputID
	interactionSubmit(t, s, func(b *SessionCommit) {
		id, err := b.AddUserInput(UserInput{RunID: run})
		interactionCheck(t, err)
		input = id
	})
	contribution := EffectiveInput{RunID: run, Inputs: []InputItem{{UserInputID: &input}}}
	interactionSubmit(t, s, func(b *SessionCommit) { interactionCheck(t, b.AddEffectiveInput(contribution)) })

	request := interactionRequest(s, 1)
	interactionSubmit(t, s, func(b *SessionCommit) {
		interactionCheck(t, b.add(makeRequestRecord(request, s.policy.PayloadLimits)))
	})
	return s, run, input, request
}

func TestAdmissionFailureAtomicity(t *testing.T) {
	cases := []struct {
		name  string
		stage string
		make  func(*Session) *materializedRecord
		want  error
	}{
		{
			name:  "request source",
			stage: "active",
			make: func(s *Session) *materializedRecord {
				v := interactionRequest(s, 1)
				v.Source.Sequence++
				return interactionRecord(t)(makeRequestRecord(v, s.policy.PayloadLimits))
			},
			want: ErrIdentity,
		},
		{
			name:  "completed pending ending",
			stage: "pending",
			make: func(s *Session) *materializedRecord {
				return interactionRecord(t)(newRunEndedRecord(RunEnded{RunID: s.Snapshot().ActiveRun(), Reason: RunEndedCompleted}, s.policy.PayloadLimits))
			},
			want: ErrTransition,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := interactionFixture(t, tc.stage)
			a := newAdmission(s.state.view)
			candidate, source, head := a.candidate, a.source, a.head

			if err := a.admit(s.Snapshot().HistoryID(), tc.make(s)); !errors.Is(err, tc.want) {
				t.Fatalf("admit: %v; want %v", err, tc.want)
			}
			if a.candidate != candidate {
				t.Fatal("rejected admission changed candidate")
			}
			if a.source != source {
				t.Fatal("rejected admission changed pinned source")
			}
			if a.head != head {
				t.Fatal("rejected admission changed pinned head")
			}
		})
	}
}

func TestInteractionEffectiveContributionFrontier(t *testing.T) {
	t.Run("builder", func(t *testing.T) {
		s, run, input, first := interactionPendingAfterEffectiveReference(t)
		contribution := EffectiveInput{RunID: run, Inputs: []InputItem{{UserInputID: &input}}}

		// An old outcome cannot account for a newly contributed duplicate in the
		// same candidate; the rejected ending publishes neither fact.
		before := s.Snapshot()
		b, err := s.BeginCommit()
		interactionCheck(t, err)
		interactionCheck(t, b.AddModelResponse(ModelResponse{RequestID: first.ID, Terminal: ResponseTerminalNormal}))
		interactionCheck(t, b.AddEffectiveInput(contribution))
		if err := b.EndRun(RunEndedCompleted); !errors.Is(err, ErrTransition) {
			t.Fatalf("old outcome completed newer contribution: %v", err)
		}
		if !reflect.DeepEqual(before, s.Snapshot()) {
			t.Fatal("rejected outcome/contribution ending published state")
		}
		interactionCheck(t, b.Discard())

		interactionSubmit(t, s, func(b *SessionCommit) {
			interactionCheck(t, b.AddModelResponse(ModelResponse{RequestID: first.ID, Terminal: ResponseTerminalNormal}))
		})
		before = s.Snapshot()
		b, err = s.BeginCommit()
		interactionCheck(t, err)
		interactionCheck(t, b.AddEffectiveInput(contribution))
		second, err := b.AddModelRequest(ModelRequest{RunID: run, Source: interactionRequest(s, 2).Source, Recipe: interactionRequest(s, 2).Recipe})
		if second != (ModelRequestID{}) || !errors.Is(err, ErrTransition) {
			t.Fatalf("same-batch contribution/request: %v, %v", second, err)
		}
		if !reflect.DeepEqual(before, s.Snapshot()) {
			t.Fatal("rejected same-batch request published state")
		}
		interactionCheck(t, b.Discard())

		// Repeating the exact accepted effective fact remains a contribution even
		// though its referenced authored submission was empty.
		interactionSubmit(t, s, func(b *SessionCommit) { interactionCheck(t, b.AddEffectiveInput(contribution)) })
		interactionSubmit(t, s, func(b *SessionCommit) {
			id, err := b.AddModelRequest(ModelRequest{RunID: run, Source: interactionRequest(s, 2).Source, Recipe: interactionRequest(s, 2).Recipe})
			interactionCheck(t, err)
			second = id
		})
		interactionSubmit(t, s, func(b *SessionCommit) {
			interactionCheck(t, b.AddModelResponse(ModelResponse{RequestID: second, Terminal: ResponseTerminalNormal}))
		})
		interactionSubmit(t, s, func(b *SessionCommit) { interactionCheck(t, b.EndRun(RunEndedCompleted)) })
		if s.Snapshot().ActiveRun() != (RunID{}) || s.Snapshot().RecordCount() != 11 {
			t.Fatal("new request/outcome did not complete after duplicate contribution")
		}
	})

	for _, path := range []string{"preflight", "apply", "load"} {
		t.Run(path, func(t *testing.T) {
			s, run, input, first := interactionPendingAfterEffectiveReference(t)
			interactionSubmit(t, s, func(b *SessionCommit) {
				interactionCheck(t, b.AddModelResponse(ModelResponse{RequestID: first.ID, Terminal: ResponseTerminalNormal}))
			})
			contribution := interactionForgedEffectiveReferenceRecord(t, s, run, input)
			request := interactionRecord(t)(makeRequestRecord(interactionRequest(s, 2), s.policy.PayloadLimits))
			records := []*materializedRecord{contribution, request}
			before := s.Snapshot()

			var err error
			switch path {
			case "preflight":
				_, err = s.prepare(records)
			case "apply":
				err = s.apply(interactionCommit(t, s, records))
			case "load":
				commit := interactionCommit(t, s, records)
				result, appendErr := s.history.Append(context.Background(), s.Snapshot().HistoryID(), commit.Commit)
				interactionCheck(t, appendErr)
				if result.Status != history.StatusAccepted {
					t.Fatal("history rejected locally valid forged contribution")
				}
				loaded, loadErr := Load(context.Background(), s.history, s.Snapshot().HistoryID(), s.policy)
				if loaded != nil {
					t.Fatal("load published a request after forged same-batch contribution")
				}
				err = loadErr
			}
			if !errors.Is(err, ErrTransition) {
				t.Fatalf("forged contribution/request: %v", err)
			}
			if !reflect.DeepEqual(before, s.Snapshot()) || s.Snapshot().RecordCount() != before.RecordCount() {
				t.Fatal("rejected contribution/request published state or an ordinal")
			}
		})
	}
}

func TestInteractionAdmissionAllPaths(t *testing.T) {
	type testCase struct {
		name, stage string
		build       func(*Session) []*materializedRecord
		want        error
	}
	request := func(s *Session) *materializedRecord {
		v := interactionRequest(s, s.state.view.lifecycle.requests+1)
		return interactionRecord(t)(makeRequestRecord(v, s.policy.PayloadLimits))
	}
	response := func(s *Session) *materializedRecord {
		return interactionRecord(t)(newModelResponseRecord(ModelResponse{RequestID: interactionRequest(s, 1).ID, Terminal: ResponseTerminalNormal}, s.policy.PayloadLimits))
	}
	failure := func(s *Session) *materializedRecord {
		return interactionRecord(t)(newInvocationFailureRecord(InvocationFailure{RequestID: interactionRequest(s, 1).ID, Class: InvocationFailureTransport}, s.policy.PayloadLimits))
	}
	end := func(s *Session, reason RunEndedReason) *materializedRecord {
		return interactionRecord(t)(newRunEndedRecord(RunEnded{RunID: interactionRequest(s, 1).ID.runID, Reason: reason}, s.policy.PayloadLimits))
	}
	input := func(s *Session) *materializedRecord {
		return interactionRecord(t)(makeInputRecord(identifiedInput{ID: UserInputID{runID: interactionRequest(s, 1).ID.runID, ordinal: s.state.view.lifecycle.inputs + 1}}, s.policy.PayloadLimits))
	}
	effective := func(s *Session, nonempty bool) *materializedRecord {
		v := EffectiveInput{RunID: interactionRequest(s, 1).ID.runID}
		if nonempty {
			v.Inputs = []InputItem{{Inline: &AttributedInput{Actor: "harness", Purpose: PromptContext, Source: ContentSource{Description: "prepared"}}}}
		}
		return interactionRecord(t)(newEffectiveInputRecord(v, s.policy.PayloadLimits))
	}
	contextRecord := func(s *Session) *materializedRecord {
		return interactionRecord(t)(newModelBindingRecord(ModelBinding{HistoryID: s.Snapshot().HistoryID(), Route: "new", API: "a", Model: "new", Settings: []byte("{}")}, s.policy.PayloadLimits))
	}
	cases := []testCase{
		{"valid request", "active", func(s *Session) []*materializedRecord { return []*materializedRecord{request(s)} }, nil},
		{"source needs active run", "idle", func(s *Session) []*materializedRecord {
			return []*materializedRecord{interactionRecord(t)(newRunStartedRecord(RunStarted{ID: interactionRequest(s, 1).ID.runID}, s.policy.PayloadLimits)), contextRecord(s), request(s)}
		}, ErrTransition},
		{"provisional binding insufficient", "unbound", func(s *Session) []*materializedRecord { return []*materializedRecord{contextRecord(s), request(s)} }, ErrTransition},
		{"context before and after prospective", "active", func(s *Session) []*materializedRecord {
			return []*materializedRecord{contextRecord(s), request(s), contextRecord(s)}
		}, nil},
		{"new authored input even empty", "active", func(s *Session) []*materializedRecord { return []*materializedRecord{input(s), request(s)} }, ErrTransition},
		{"new effective input", "active", func(s *Session) []*materializedRecord { return []*materializedRecord{effective(s, true), request(s)} }, ErrTransition},
		{"empty effective input unchanged", "active", func(s *Session) []*materializedRecord { return []*materializedRecord{effective(s, false), request(s)} }, nil},
		{"request then response", "active", func(s *Session) []*materializedRecord { return []*materializedRecord{request(s), response(s)} }, ErrTransition},
		{"request then failure", "active", func(s *Session) []*materializedRecord { return []*materializedRecord{request(s), failure(s)} }, ErrTransition},
		{"outcome then request", "pending", func(s *Session) []*materializedRecord { return []*materializedRecord{response(s), request(s)} }, ErrTransition},
		{"failure then request", "pending", func(s *Session) []*materializedRecord { return []*materializedRecord{failure(s), request(s)} }, ErrTransition},
		{"response and clean end", "pending", func(s *Session) []*materializedRecord {
			return []*materializedRecord{response(s), end(s, RunEndedCompleted)}
		}, nil},
		{"failure and error end", "pending", func(s *Session) []*materializedRecord {
			return []*materializedRecord{failure(s), end(s, RunEndedError)}
		}, nil},
		{"duplicate response", "pending", func(s *Session) []*materializedRecord { return []*materializedRecord{response(s), response(s)} }, ErrTransition},
		{"response then failure", "pending", func(s *Session) []*materializedRecord { return []*materializedRecord{response(s), failure(s)} }, ErrTransition},
		{"failure then response", "pending", func(s *Session) []*materializedRecord { return []*materializedRecord{failure(s), response(s)} }, ErrTransition},
		{"input while pending", "pending", func(s *Session) []*materializedRecord { return []*materializedRecord{input(s)} }, ErrTransition},
		{"request while pending", "pending", func(s *Session) []*materializedRecord { return []*materializedRecord{request(s)} }, ErrTransition},
		{"clean end while pending", "pending", func(s *Session) []*materializedRecord { return []*materializedRecord{end(s, RunEndedCompleted)} }, ErrTransition},
		{"uncertain end while pending", "pending", func(s *Session) []*materializedRecord { return []*materializedRecord{end(s, RunEndedCaptureFailed)} }, nil},
		{"input while calls unresolved", "calls", func(s *Session) []*materializedRecord { return []*materializedRecord{input(s)} }, ErrTransition},
		{"effective while calls unresolved", "calls", func(s *Session) []*materializedRecord { return []*materializedRecord{effective(s, false)} }, ErrTransition},
		{"request while calls unresolved", "calls", func(s *Session) []*materializedRecord { return []*materializedRecord{request(s)} }, ErrTransition},
		{"clean end while calls unresolved", "calls", func(s *Session) []*materializedRecord { return []*materializedRecord{end(s, RunEndedCompleted)} }, ErrTransition},
		{"uncertain end preserves calls", "calls", func(s *Session) []*materializedRecord { return []*materializedRecord{end(s, RunEndedInterrupted)} }, nil},
		{"late response", "ended", func(s *Session) []*materializedRecord { return []*materializedRecord{response(s)} }, ErrTransition},
		{"duplicate ending", "ended", func(s *Session) []*materializedRecord { return []*materializedRecord{end(s, RunEndedError)} }, ErrTransition},
		{"new input after outcome not covered", "responded", func(s *Session) []*materializedRecord {
			return []*materializedRecord{input(s), end(s, RunEndedCompleted)}
		}, ErrTransition},
		{"new input may remain uncertain", "responded", func(s *Session) []*materializedRecord { return []*materializedRecord{input(s), end(s, RunEndedError)} }, nil},
	}
	for _, field := range []string{"sequence", "CID", "retry", "ordinal"} {
		cases = append(cases, testCase{"invalid " + field, "active", func(s *Session) []*materializedRecord {
			v := interactionRequest(s, 1)
			switch field {
			case "sequence":
				v.Source.Sequence++
			case "CID":
				v.Source.CID = interactionFixture(t, "idle").Snapshot().Head().CID
			case "retry":
				v.RetryOf = new(v.ID)
			case "ordinal":
				v.ID.requestOrdinal++
			}
			return []*materializedRecord{interactionRecord(t)(makeRequestRecord(v, s.policy.PayloadLimits))}
		}, map[string]error{"sequence": ErrIdentity, "CID": ErrIdentity, "retry": ErrTransition, "ordinal": ErrTransition}[field]})
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, path := range []string{"builder", "preflight", "apply", "load"} {
				t.Run(path, func(t *testing.T) {
					s := interactionFixture(t, tc.stage)
					records := tc.build(s)
					before := detachedSessionState(s.state)
					var err error
					switch path {
					case "builder":
						b, e := s.BeginCommit()
						interactionCheck(t, e)
						for _, r := range records {
							prior := b.admission
							n := len(b.records)
							err = b.add(r, nil)
							if err != nil {
								if prior != b.admission || n != len(b.records) {
									t.Fatal("failed addition mutated candidate")
								}
								break
							}
						}
						interactionCheck(t, b.Discard())
					case "preflight":
						_, err = s.prepare(records)
					case "apply":
						err = s.apply(interactionCommit(t, s, records))
					case "load":
						c := interactionCommit(t, s, records)
						result, e := s.history.Append(context.Background(), s.Snapshot().HistoryID(), c.Commit)
						interactionCheck(t, e)
						if result.Status != history.StatusAccepted {
							t.Fatal("history did not accept locally valid commit")
						}
						_, err = Load(context.Background(), s.history, s.Snapshot().HistoryID(), s.policy)
					}
					if !errors.Is(err, tc.want) {
						t.Fatalf("got %v; want %v", err, tc.want)
					}
					if (tc.want != nil || path != "apply") && !reflect.DeepEqual(before, detachedSessionState(s.state)) {
						t.Fatal("rejection/preparation changed accepted state")
					}
				})
			}
		})
	}
}

func TestInteractionOverflowAndMaterializationAgreement(t *testing.T) {
	for _, field := range []string{"requests", "authored frontier", "effective frontier"} {
		t.Run(field, func(t *testing.T) {
			s := interactionFixture(t, "active")
			run := s.Snapshot().ActiveRun()
			if field == "requests" {
				s.state.view.lifecycle.requests = math.MaxUint64
			} else {
				s.state.view.lifecycle.frontier = math.MaxUint64
			}
			b, err := s.BeginCommit()
			interactionCheck(t, err)
			before := b.admission
			switch field {
			case "requests":
				_, err = b.AddModelRequest(ModelRequest{RunID: run, Source: interactionRequest(s, 1).Source, Recipe: interactionRequest(s, 1).Recipe})
			case "authored frontier":
				_, err = b.AddUserInput(UserInput{RunID: run})
			case "effective frontier":
				err = b.AddEffectiveInput(EffectiveInput{RunID: run, Inputs: []InputItem{{Inline: &AttributedInput{Actor: "harness", Purpose: PromptContext, Source: ContentSource{Description: "prepared"}}}}})
			}
			if !errors.Is(err, codec.ErrLimitExceeded) || b.admission != before || len(b.records) != 0 {
				t.Fatalf("overflow changed candidate: %v", err)
			}
			interactionCheck(t, b.Discard())
		})
	}
	s := interactionFixture(t, "pending")
	req := interactionRequest(s, 2)
	req.RetryOf = new(interactionRequest(s, 1).ID)
	records := []*materializedRecord{
		interactionRecord(t)(makeRequestRecord(req, s.policy.PayloadLimits)),
		interactionRecord(t)(newModelResponseRecord(ModelResponse{RequestID: *req.RetryOf, Terminal: ResponseTerminalNormal, Items: []ResponseItem{{Call: &RequestedCall{}}}}, s.policy.PayloadLimits)),
		interactionRecord(t)(newInvocationFailureRecord(InvocationFailure{RequestID: *req.RetryOf, Class: InvocationFailureTimeout}, s.policy.PayloadLimits)),
		interactionRecord(t)(newRunEndedRecord(RunEnded{RunID: req.ID.runID, Reason: RunEndedCaptureFailed, Capture: &CaptureFailureEvidence{RequestID: *req.RetryOf}}, s.policy.PayloadLimits)),
	}
	for _, r := range records {
		imported, err := decodeMaterialized(r.format(), s.policy.PayloadLimits)
		interactionCheck(t, err)
		a, b := newAdmission(s.state.view), newAdmission(s.state.view)
		x, y := a.admit(s.Snapshot().HistoryID(), r), b.admit(s.Snapshot().HistoryID(), imported)
		if !errors.Is(x, y) || a.candidate != b.candidate {
			t.Fatalf("constructed and decoded gates disagree for %s: %v/%v", r.value.Kind, x, y)
		}
		if !supportedRecord(r.value.Kind, sessionformat.Version) {
			t.Fatal("new kind not in closed dispatch")
		}
	}
}
