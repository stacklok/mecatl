package session

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/stacklok/mecatl/engine/kernel/codec"
	"github.com/stacklok/mecatl/engine/kernel/contentid"
	"github.com/stacklok/mecatl/engine/kernel/history/format"
	historyid "github.com/stacklok/mecatl/engine/kernel/history/id"
)

func TestDecodedRecordUsesEnclosingScope(t *testing.T) {
	foreign, err := historyid.ParseHistoryID("HST2222222222222222222222")
	mustBatch(t, err)
	for _, id := range []historyid.HistoryID{testHistoryID(t), foreign} {
		s, err := New(newSessionHistory(t), id, sessionPolicy())
		mustBatch(t, err)
		for _, r := range []*materializedRecord{createdRecord(t), startRecord(t, 1), inputRecord(t, 1), closeoutRecord(t, 1)} {
			decoded, err := decodeMaterialized(r.format(), testLimits())
			mustBatch(t, err)
			applyRecords(t, s, decoded)
		}
		v := s.Snapshot()
		if v.RunAt(0).ID().HistoryID() != id || lookupInput(t, v, v.RunAt(0).ID(), 1).ID().RunID().HistoryID() != id {
			t.Fatal("decoded scope lost")
		}
	}
}

func TestAllocatedAuthoringScopeBeforePersistenceConversion(t *testing.T) {
	s, limits := internalFixture(t)
	run := RunID{historyID: s.Snapshot().HistoryID(), ordinal: 1}
	foreign, err := historyid.ParseHistoryID("HST2222222222222222222222")
	mustBatch(t, err)
	for _, owner := range []RunID{{}, {historyID: run.historyID, ordinal: 2}, {historyID: foreign, ordinal: 1}} {
		inputID := UserInputID{runID: owner, ordinal: 1}
		if r, err := newUserInputRecord(run.historyID, UserInput{RunID: run}, inputID, limits); !errors.Is(err, codec.ErrInvalidData) || r != nil {
			t.Fatalf("input identity lost scope before conversion: %v", err)
		}
		requestID := ModelRequestID{runID: owner, requestOrdinal: 1}
		v := ModelRequest{RunID: run, Source: SourceHead{Sequence: 1, CID: contentid.Sum([]byte("source"))}, Recipe: ConstructionRecipe{Algorithm: "a", Version: 1, Check: []byte{1}}}
		if r, err := newModelRequestRecord(run.historyID, v, requestID, limits); !errors.Is(err, codec.ErrInvalidData) || r != nil {
			t.Fatalf("request identity lost scope before conversion: %v", err)
		}
	}
}

func TestIdleContextReplacementHistoryScope(t *testing.T) {
	foreign, err := historyid.ParseHistoryID("HST2222222222222222222222")
	interactionCheck(t, err)
	for _, stage := range []string{"created", "same-batch creation", "ended", "same-batch ending"} {
		t.Run(stage, func(t *testing.T) {
			s, _ := internalFixture(t)
			if stage != "same-batch creation" {
				interactionSubmit(t, s, func(b *SessionCommit) {
					interactionCheck(t, b.Create())
					if stage == "ended" || stage == "same-batch ending" {
						_, err := b.StartRun()
						interactionCheck(t, err)
					}
					if stage == "ended" {
						interactionCheck(t, b.EndRun(RunEndedInterrupted))
					}
				})
			}
			before := detachedSessionState(s.state)
			b, err := s.BeginCommit()
			interactionCheck(t, err)
			if stage == "same-batch creation" {
				interactionCheck(t, b.Create())
			}
			if stage == "same-batch ending" {
				interactionCheck(t, b.EndRun(RunEndedInterrupted))
			}
			for _, replace := range []func(historyid.HistoryID) error{
				func(scope historyid.HistoryID) error { return b.ReplaceInstructions(Instructions{HistoryID: scope}) },
				func(scope historyid.HistoryID) error { return b.ReplaceToolCatalog(ToolCatalog{HistoryID: scope}) },
				func(scope historyid.HistoryID) error {
					return b.ReplaceModelBinding(ModelBinding{HistoryID: scope, Route: "r", API: "a", Model: "m", Settings: []byte("{}")})
				},
			} {
				count, admission := len(b.records), b.admission
				if err := replace(foreign); !errors.Is(err, ErrIdentity) {
					t.Fatalf("foreign idle replacement: %v", err)
				}
				if len(b.records) != count || b.admission != admission || !reflect.DeepEqual(before, detachedSessionState(s.state)) {
					t.Fatal("foreign replacement changed batch or accepted state")
				}
				interactionCheck(t, replace(s.Snapshot().HistoryID()))
			}
			count := len(b.records)
			_, err = s.SubmitCommit(context.Background(), b)
			interactionCheck(t, err)
			if s.Snapshot().RecordCount() != before.records+count {
				t.Fatal("failed replacement was retained")
			}
			interactionReplay(t, s)
		})
	}
}

func TestNestedAuthoringScopeBeforeConversion(t *testing.T) {
	foreign, err := historyid.ParseHistoryID("HST2222222222222222222222")
	interactionCheck(t, err)
	for _, kind := range []string{"effective", "retry", "capture"} {
		for _, mismatch := range []string{"history", "run"} {
			t.Run(kind+"/"+mismatch, func(t *testing.T) {
				stage := map[string]string{"effective": "active", "retry": "responded", "capture": "pending"}[kind]
				s := interactionFixture(t, stage)
				run := s.Snapshot().ActiveRun()
				if kind == "effective" {
					interactionSubmit(t, s, func(b *SessionCommit) {
						_, err := b.AddUserInput(UserInput{RunID: run})
						interactionCheck(t, err)
					})
				}
				other := run
				if mismatch == "history" {
					other.historyID = foreign // Equal local ordinals must not hide scope.
				} else {
					other.ordinal++
				}
				b, err := s.BeginCommit()
				interactionCheck(t, err)
				add := func(referenceRun RunID) error {
					switch kind {
					case "effective":
						id := UserInputID{runID: referenceRun, ordinal: 1}
						return b.AddEffectiveInput(EffectiveInput{RunID: run, Inputs: []InputItem{{UserInputID: &id}}})
					case "retry":
						v := interactionRequest(s, 2).authoring()
						v.RetryOf = new(requestID(referenceRun.historyID, referenceRun.ordinal, 1))
						id, err := b.AddModelRequest(v)
						if err != nil && id != (ModelRequestID{}) {
							t.Fatal("failed retry returned an identity")
						}
						return err
					default:
						return b.AddRunEnded(RunEnded{RunID: run, Reason: RunEndedCaptureFailed, Capture: &CaptureFailureEvidence{RequestID: requestID(referenceRun.historyID, referenceRun.ordinal, 1)}})
					}
				}
				before, admission := detachedSessionState(s.state), b.admission
				if err := add(other); !errors.Is(err, codec.ErrInvalidData) {
					t.Fatalf("nested scope mismatch: %v", err)
				}
				if len(b.records) != 0 || b.admission != admission || !reflect.DeepEqual(before, detachedSessionState(s.state)) {
					t.Fatal("failed nested reference changed batch or accepted state")
				}
				interactionCheck(t, add(run))
				_, err = s.SubmitCommit(context.Background(), b)
				interactionCheck(t, err)
				if s.Snapshot().RecordCount() != before.records+1 {
					t.Fatal("failed nested reference retained or consumed identity")
				}
				interactionReplay(t, s)
			})
		}
	}
}

func TestForeignOutcomeAndEndingHistoryScope(t *testing.T) {
	foreign, err := historyid.ParseHistoryID("HST2222222222222222222222")
	interactionCheck(t, err)
	for _, kind := range []string{"response", "failure", "ending"} {
		t.Run(kind, func(t *testing.T) {
			s := interactionFixture(t, "pending")
			b, err := s.BeginCommit()
			interactionCheck(t, err)
			add := func(scope historyid.HistoryID) error {
				id := requestID(scope, 1, 1)
				switch kind {
				case "response":
					return b.AddModelResponse(ModelResponse{RequestID: id, Terminal: ResponseTerminalNormal})
				case "failure":
					return b.AddInvocationFailure(InvocationFailure{RequestID: id, Class: InvocationFailureTransport})
				default:
					return b.AddRunEnded(RunEnded{RunID: id.runID, Reason: RunEndedInterrupted})
				}
			}
			before, admission := detachedSessionState(s.state), b.admission
			if err := add(foreign); !errors.Is(err, ErrIdentity) {
				t.Fatalf("foreign history with equal ordinals: %v", err)
			}
			if len(b.records) != 0 || b.admission != admission || !reflect.DeepEqual(before, detachedSessionState(s.state)) {
				t.Fatal("foreign outcome/ending changed batch or accepted state")
			}
			interactionCheck(t, add(s.Snapshot().HistoryID()))
			_, err = s.SubmitCommit(context.Background(), b)
			interactionCheck(t, err)
			if s.Snapshot().RecordCount() != before.records+1 {
				t.Fatal("failed outcome/ending retained")
			}
			interactionReplay(t, s)
		})
	}
}

func TestSessionLocalOrdinalsBindToVerifiedChain(t *testing.T) {
	local := newSession(t, sessionPolicy())
	c := proposedCommit(t, local, createdRecord(t), startRecord(t, 1), inputRecord(t, 1))
	foreign, err := historyid.ParseHistoryID("HST2222222222222222222222")
	mustBatch(t, err)
	dto := c.Commit.Clone()
	dto.HistoryID = new(foreign)
	accepted := identify(t, dto)
	other, err := New(newSessionHistory(t), foreign, sessionPolicy())
	mustBatch(t, err)
	mustBatch(t, other.apply(accepted))
	got := other.Snapshot()
	run := allocatedRun(t, foreign, 1)
	if got.HistoryID() != foreign || got.ActiveRun() != run || got.RunAt(0).ID() != run || lookupInput(t, got, run, 1).ID() != allocatedInput(t, run, 1) {
		t.Fatal("local ordinals not scoped to verified chain")
	}
	assertRejected(t, local, accepted, ErrIdentity)
	// A successor binds through the exact parent, without a repeated history field.
	applyRecords(t, other, closeoutRecord(t, 1))
	if other.Snapshot().ActiveRun() != (RunID{}) {
		t.Fatal("scoped closeout")
	}
	if !c.Commit.Records[0].Equal(format.Record{Kind: "session.created", KindVersion: 1, Payload: []byte{0xa0}}) {
		t.Fatal("creation repeats scope")
	}
}
