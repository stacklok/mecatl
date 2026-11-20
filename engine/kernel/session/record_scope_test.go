package session

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/stacklok/mecatl/engine/kernel/codec"
	"github.com/stacklok/mecatl/engine/kernel/contentid"
	"github.com/stacklok/mecatl/engine/kernel/history/format"
	sessionid "github.com/stacklok/mecatl/engine/kernel/session/id"
)

func TestDecodedRecordUsesEnclosingScope(t *testing.T) {
	foreign, err := sessionid.ParseSessionID("SES2222222222222222222222")
	mustBatch(t, err)
	for _, id := range []sessionid.SessionID{testSessionID(t), foreign} {
		s, err := New(newSessionHistory(t), id, testConfigFor(sessionPolicy()))
		mustBatch(t, err)
		for _, r := range []*materializedRecord{createdRecord(t), startRecord(t, 1), inputRecord(t, 1), closeoutRecord(t, 1)} {
			decoded, err := decodeMaterialized(r.format(), testLimits())
			mustBatch(t, err)
			applyRecords(t, s, decoded)
		}
		v := s.Snapshot()
		if v.RunAt(0).ID().SessionID() != id || lookupInput(t, v, v.RunAt(0).ID(), 1).ID().RunID().SessionID() != id {
			t.Fatal("decoded scope lost")
		}
	}
}

func TestAllocatedAuthoringScopeBeforePersistenceConversion(t *testing.T) {
	s, limits := internalFixture(t)
	run := RunID{sessionID: s.Snapshot().SessionID(), ordinal: 1}
	foreign, err := sessionid.ParseSessionID("SES2222222222222222222222")
	mustBatch(t, err)
	for _, owner := range []RunID{{}, {sessionID: run.sessionID, ordinal: 2}, {sessionID: foreign, ordinal: 1}} {
		want := error(nil)
		if owner.Validate() != nil {
			want = codec.ErrInvalidData
		}
		inputID := UserInputID{runID: owner, ordinal: 1}
		if r, err := newUserInputRecord(UserInput{}, inputID, limits); !errors.Is(err, want) || (r == nil) != (want != nil) {
			t.Fatalf("input allocation identity: %v; want %v", err, want)
		}
		requestID := ModelRequestID{runID: owner, requestOrdinal: 1}
		v := ModelRequest{Source: &SourceHead{Sequence: 1, CID: contentid.Sum([]byte("source"))}, Recipe: ConstructionRecipe{Algorithm: "a", Version: 1, Check: []byte{1}}}
		if r, err := newModelRequestRecord(v, requestID, limits); !errors.Is(err, want) || (r == nil) != (want != nil) {
			t.Fatalf("request allocation identity: %v; want %v", err, want)
		}
	}
}

func TestIdleContextReplacementHistoryScope(t *testing.T) {
	foreign, err := sessionid.ParseSessionID("SES2222222222222222222222")
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
			for _, replace := range []func(sessionid.SessionID) error{
				func(scope sessionid.SessionID) error { return b.ReplaceUserInstructions(scope, UserInstructions{}) },
				func(scope sessionid.SessionID) error { return b.ReplaceRootInstructions(scope, RootInstructions{}) },
				func(scope sessionid.SessionID) error { return b.ReplaceToolCatalog(scope, ToolCatalog{}) },
				func(scope sessionid.SessionID) error {
					return b.ReplaceModelBinding(scope, ModelBinding{Route: "r", API: "a", Model: "m"})
				},
			} {
				count, admission := len(b.records), b.admission
				for _, invalid := range []struct {
					scope sessionid.SessionID
					want  error
				}{{foreign, ErrIdentity}, {sessionid.SessionID{}, codec.ErrInvalidData}} {
					if err := replace(invalid.scope); !errors.Is(err, invalid.want) {
						t.Fatalf("invalid idle replacement: %v; want %v", err, invalid.want)
					}
					if len(b.records) != count || !reflect.DeepEqual(b.admission, admission) || !reflect.DeepEqual(before, detachedSessionState(s.state)) {
						t.Fatal("invalid replacement changed batch or accepted state")
					}
				}
				interactionCheck(t, replace(s.Snapshot().SessionID()))
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

func TestRelativeReferencesUseDestinationRun(t *testing.T) {
	foreign, err := sessionid.ParseSessionID("SES2222222222222222222222")
	interactionCheck(t, err)
	for _, kind := range []string{"effective", "retry", "capture"} {
		for _, mismatch := range []string{"history", "run"} {
			t.Run(kind+"/"+mismatch, func(t *testing.T) {
				stage := map[string]string{"effective": "active", "retry": "responded", "capture": "pending"}[kind]
				s := interactionFixture(t, stage)
				run := s.Snapshot().ActiveRun()
				if kind == "effective" {
					interactionSubmit(t, s, func(b *SessionCommit) {
						_, err := b.AddUserInput(run, UserInput{})
						interactionCheck(t, err)
					})
				}
				other := run
				if mismatch == "history" {
					other.sessionID = foreign // Only the extracted component is retained.
				} else {
					other.ordinal++
				}
				b, err := s.BeginCommit()
				interactionCheck(t, err)
				add := func(referenceRun RunID) error {
					switch kind {
					case "effective":
						id := UserInputID{runID: referenceRun, ordinal: 1}
						return b.AddEffectiveInput(run, EffectiveInput{Inputs: []InputItem{{UserInputID: new(id.RunRelative())}}})
					case "retry":
						v := interactionRequest(s, 2).ModelRequest
						v.Source = nil
						v.RetryOf = new(requestID(referenceRun.sessionID, referenceRun.ordinal, 1).RunRelative())
						id, err := b.AddModelRequest(run, captureCommit(t, b), v)
						if err != nil && id != (ModelRequestID{}) {
							t.Fatal("failed retry returned an identity")
						}
						return err
					default:
						return b.AddRunEnded(run, RunEnded{Reason: RunEndedCaptureFailed, Capture: &CaptureFailureEvidence{RequestID: requestID(referenceRun.sessionID, referenceRun.ordinal, 1).RunRelative()}})
					}
				}
				before := detachedSessionState(s.state)
				interactionCheck(t, add(other))
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
	foreign, err := sessionid.ParseSessionID("SES2222222222222222222222")
	interactionCheck(t, err)
	for _, kind := range []string{"response", "failure", "ending"} {
		t.Run(kind, func(t *testing.T) {
			s := interactionFixture(t, "pending")
			b, err := s.BeginCommit()
			interactionCheck(t, err)
			add := func(id ModelRequestID) error {
				switch kind {
				case "response":
					return b.AddModelResponse(id, ModelResponse{Terminal: ResponseTerminalNormal})
				case "failure":
					return b.AddInvocationFailure(id, InvocationFailure{Class: InvocationFailureTransport})
				default:
					return b.AddRunEnded(id.runID, RunEnded{Reason: RunEndedInterrupted})
				}
			}
			before, admission := detachedSessionState(s.state), b.admission
			for _, invalid := range []struct {
				id   ModelRequestID
				want error
			}{
				{requestID(foreign, 1, 1), ErrIdentity},
				{ModelRequestID{}, codec.ErrInvalidData},
				{requestID(sessionid.SessionID{}, 1, 1), codec.ErrInvalidData},
				{requestID(foreign, 0, 1), codec.ErrInvalidData},
			} {
				if err := add(invalid.id); !errors.Is(err, invalid.want) {
					t.Fatalf("invalid outcome/ending identity: %v; want %v", err, invalid.want)
				}
				if len(b.records) != 0 || !reflect.DeepEqual(b.admission, admission) || !reflect.DeepEqual(before, detachedSessionState(s.state)) {
					t.Fatal("invalid outcome/ending changed batch or accepted state")
				}
			}
			if kind != "ending" {
				if err := add(requestID(foreign, 1, 0)); !errors.Is(err, codec.ErrInvalidData) {
					t.Fatalf("invalid request ordinal in foreign history: %v", err)
				}
				if len(b.records) != 0 || !reflect.DeepEqual(b.admission, admission) || !reflect.DeepEqual(before, detachedSessionState(s.state)) {
					t.Fatal("invalid request ordinal changed batch or accepted state")
				}
			}
			interactionCheck(t, add(requestID(s.Snapshot().SessionID(), 1, 1)))
			_, err = s.SubmitCommit(context.Background(), b)
			interactionCheck(t, err)
			if s.Snapshot().RecordCount() != before.records+1 {
				t.Fatal("failed outcome/ending retained")
			}
			interactionReplay(t, s)
		})
	}
}

func TestPublicRunAuthoringScope(t *testing.T) {
	foreign, err := sessionid.ParseSessionID("SES2222222222222222222222")
	interactionCheck(t, err)
	for _, kind := range []string{"effective", "input", "request"} {
		t.Run(kind, func(t *testing.T) {
			s := interactionFixture(t, "active")
			run := s.Snapshot().ActiveRun()
			b, err := s.BeginCommit()
			interactionCheck(t, err)
			add := func(owner RunID) error {
				switch kind {
				case "effective":
					return b.AddEffectiveInput(owner, EffectiveInput{})
				case "input":
					id, err := b.AddUserInput(owner, UserInput{})
					if err != nil && id != (UserInputID{}) {
						t.Fatal("failed input returned identity")
					}
					if err == nil && id != (UserInputID{runID: run, ordinal: 1}) {
						t.Fatal("failed input consumed identity")
					}
					return err
				default:
					v := interactionRequest(s, 1).ModelRequest
					v.Source = nil
					id, err := b.AddModelRequest(owner, captureCommit(t, b), v)
					if err != nil && id != (ModelRequestID{}) {
						t.Fatal("failed request returned identity")
					}
					if err == nil && id != requestID(run.sessionID, run.ordinal, 1) {
						t.Fatal("failed request consumed identity")
					}
					return err
				}
			}
			before, admission := detachedSessionState(s.state), b.admission
			for _, owner := range []RunID{{}, {ordinal: 1}, {sessionID: run.sessionID}, {sessionID: foreign, ordinal: 1}, {sessionID: foreign}, {sessionID: run.sessionID, ordinal: 2}} {
				want := ErrTransition
				if kind == "effective" {
					if owner.Validate() != nil {
						want = codec.ErrInvalidData
					} else if owner.sessionID != run.sessionID {
						want = ErrIdentity
					}
				}
				if err := add(owner); !errors.Is(err, want) {
					t.Fatalf("invalid run ownership: %v; want %v", err, want)
				}
				if len(b.records) != 0 || !reflect.DeepEqual(b.admission, admission) || !reflect.DeepEqual(before, detachedSessionState(s.state)) {
					t.Fatal("failed authoring changed batch or accepted state")
				}
			}
			interactionCheck(t, add(run))
			_, err = s.SubmitCommit(context.Background(), b)
			interactionCheck(t, err)
			if s.Snapshot().RecordCount() != before.records+1 {
				t.Fatal("failed authoring retained facts")
			}
			interactionReplay(t, s)
		})
	}
}

func TestPublicOwnerCheckPrecedesOversizedPayload(t *testing.T) {
	foreign, err := sessionid.ParseSessionID("SES2222222222222222222222")
	interactionCheck(t, err)
	for name, add := range map[string]func(*SessionCommit, sessionid.SessionID) error{
		"user context prefix": func(b *SessionCommit, scope sessionid.SessionID) error {
			return b.ReplaceUserInstructions(scope, UserInstructions{})
		},
		"instructions": func(b *SessionCommit, scope sessionid.SessionID) error {
			return b.ReplaceRootInstructions(scope, RootInstructions{})
		},
		"catalog": func(b *SessionCommit, scope sessionid.SessionID) error {
			return b.ReplaceToolCatalog(scope, ToolCatalog{})
		},
		"binding": func(b *SessionCommit, scope sessionid.SessionID) error {
			return b.ReplaceModelBinding(scope, ModelBinding{Route: "r", API: "a", Model: "m"})
		},
		"effective": func(b *SessionCommit, scope sessionid.SessionID) error {
			return b.AddEffectiveInput(RunID{sessionID: scope, ordinal: 1}, EffectiveInput{})
		},
		"response": func(b *SessionCommit, scope sessionid.SessionID) error {
			return b.AddModelResponse(requestID(scope, 1, 1), ModelResponse{Terminal: ResponseTerminalError})
		},
		"failure": func(b *SessionCommit, scope sessionid.SessionID) error {
			return b.AddInvocationFailure(requestID(scope, 1, 1), InvocationFailure{Class: InvocationFailureLocal})
		},
		"ending": func(b *SessionCommit, scope sessionid.SessionID) error {
			return b.AddRunEnded(RunID{sessionID: scope, ordinal: 1}, RunEnded{Reason: RunEndedInterrupted})
		},
	} {
		t.Run(name, func(t *testing.T) {
			stage := "pending"
			if name == "effective" {
				stage = "active"
			}
			s := interactionFixture(t, stage)
			control, err := s.BeginCommit()
			interactionCheck(t, err)
			if name == "effective" {
				_, err := control.AddUserInput(s.Snapshot().ActiveRun(), UserInput{})
				interactionCheck(t, err)
			}
			interactionCheck(t, add(control, s.Snapshot().SessionID()))
			interactionCheck(t, control.Discard())

			s.policy.PayloadLimits.MaxBytes = 1
			b, err := s.BeginCommit()
			interactionCheck(t, err)
			before, admission := detachedSessionState(s.state), b.admission
			for _, invalid := range []struct {
				scope sessionid.SessionID
				want  error
			}{{foreign, ErrIdentity}, {sessionid.SessionID{}, codec.ErrInvalidData}, {s.Snapshot().SessionID(), codec.ErrLimitExceeded}} {
				if err := add(b, invalid.scope); !errors.Is(err, invalid.want) {
					t.Fatalf("owner/payload precedence: %v; want %v", err, invalid.want)
				}
				if len(b.records) != 0 || !reflect.DeepEqual(b.admission, admission) || !reflect.DeepEqual(before, detachedSessionState(s.state)) {
					t.Fatal("failed owner/payload validation changed batch or accepted state")
				}
			}
			interactionCheck(t, b.Discard())
		})
	}
}

func TestPublicOwnerCheckPrecedesPayloadValidation(t *testing.T) {
	foreign, err := sessionid.ParseSessionID("SES2222222222222222222222")
	interactionCheck(t, err)
	for name, add := range map[string]func(*SessionCommit, sessionid.SessionID) error{
		"user context prefix": func(b *SessionCommit, scope sessionid.SessionID) error {
			return b.ReplaceUserInstructions(scope, UserInstructions{Blocks: []InstructionBlock{{Key: "harness/core"}}})
		},
		"instructions": func(b *SessionCommit, scope sessionid.SessionID) error {
			return b.ReplaceRootInstructions(scope, RootInstructions{Blocks: []InstructionBlock{{Key: "harness/core"}}})
		},
		"catalog": func(b *SessionCommit, scope sessionid.SessionID) error {
			return b.ReplaceToolCatalog(scope, ToolCatalog{Tools: []ToolDefinition{{}}})
		},
		"binding": func(b *SessionCommit, scope sessionid.SessionID) error {
			return b.ReplaceModelBinding(scope, ModelBinding{})
		},
		"effective": func(b *SessionCommit, scope sessionid.SessionID) error {
			return b.AddEffectiveInput(RunID{sessionID: scope, ordinal: 1}, EffectiveInput{Inputs: []InputItem{{}}})
		},
		"response": func(b *SessionCommit, scope sessionid.SessionID) error {
			return b.AddModelResponse(requestID(scope, 1, 1), ModelResponse{})
		},
		"failure": func(b *SessionCommit, scope sessionid.SessionID) error {
			return b.AddInvocationFailure(requestID(scope, 1, 1), InvocationFailure{})
		},
		"ending": func(b *SessionCommit, scope sessionid.SessionID) error {
			return b.AddRunEnded(RunID{sessionID: scope, ordinal: 1}, RunEnded{})
		},
	} {
		t.Run(name, func(t *testing.T) {
			s := interactionFixture(t, "pending")
			b, err := s.BeginCommit()
			interactionCheck(t, err)
			before, admission := detachedSessionState(s.state), b.admission
			for _, invalid := range []struct {
				scope sessionid.SessionID
				want  error
			}{{foreign, ErrIdentity}, {sessionid.SessionID{}, codec.ErrInvalidData}, {s.Snapshot().SessionID(), codec.ErrInvalidData}} {
				if err := add(b, invalid.scope); !errors.Is(err, invalid.want) {
					t.Fatalf("owner/payload precedence: %v; want %v", err, invalid.want)
				}
				if len(b.records) != 0 || !reflect.DeepEqual(b.admission, admission) || !reflect.DeepEqual(before, detachedSessionState(s.state)) {
					t.Fatal("failed owner/payload validation changed batch or accepted state")
				}
			}
			interactionCheck(t, b.Discard())
		})
	}
}

func TestSessionLocalOrdinalsBindToVerifiedChain(t *testing.T) {
	local := newSession(t, sessionPolicy())
	c := proposedCommit(t, local, createdRecord(t), startRecord(t, 1), inputRecord(t, 1))
	foreign, err := sessionid.ParseSessionID("SES2222222222222222222222")
	mustBatch(t, err)
	dto := c.Commit.Clone()
	dto.HistoryID = new(foreign.ResourceID())
	accepted := identify(t, dto)
	other, err := New(newSessionHistory(t), foreign, testConfigFor(sessionPolicy()))
	mustBatch(t, err)
	mustBatch(t, other.apply(accepted))
	got := other.Snapshot()
	run := allocatedRun(t, foreign, 1)
	if got.SessionID() != foreign || got.ActiveRun() != run || got.RunAt(0).ID() != run || lookupInput(t, got, run, 1).ID() != allocatedInput(t, run, 1) {
		t.Fatal("local ordinals not scoped to verified chain")
	}
	assertRejected(t, local, accepted, ErrIdentity)
	// A successor binds through the exact parent, without a repeated history field.
	applyRecords(t, other, closeoutRecord(t, 1))
	if other.Snapshot().ActiveRun() != (RunID{}) {
		t.Fatal("scoped closeout")
	}
	if !c.Commit.Records[0].Equal(format.Record{Timestamp: 0, Kind: "session.created", KindVersion: 1, Payload: []byte{0xa0}}) {
		t.Fatal("creation repeats scope")
	}
}
