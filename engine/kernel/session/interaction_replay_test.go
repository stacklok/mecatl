package session

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/stacklok/mecatl/engine/kernel/codec"
	"github.com/stacklok/mecatl/engine/kernel/history"
	historyformat "github.com/stacklok/mecatl/engine/kernel/history/format"
	historyid "github.com/stacklok/mecatl/engine/kernel/history/id"
	sessionformat "github.com/stacklok/mecatl/engine/kernel/session/format"
)

// interactionReplay compares independent replay from genesis, not a clone of the
// submitting reducer's state. Each page is read from the real memory History.
func interactionReplay(t testing.TB, s *Session) {
	t.Helper()
	loaded, err := Load(context.Background(), s.history, s.Snapshot().HistoryID(), s.policy)
	interactionCheck(t, err)
	applied, err := New(s.history, s.Snapshot().HistoryID(), s.policy)
	interactionCheck(t, err)
	request := history.ReadRequest{Through: s.Snapshot().Head(), MaxCommits: 1}
	for request.After != request.Through {
		page, err := s.history.Read(context.Background(), s.Snapshot().HistoryID(), request)
		interactionCheck(t, err)
		for _, c := range page.Commits {
			interactionCheck(t, applied.Apply(c))
		}
		request.After = page.Next
	}
	for _, other := range []*Session{loaded, applied} {
		if !reflect.DeepEqual(s.Snapshot(), other.Snapshot()) {
			t.Fatal("Submit/Apply/Load differ")
		}
	}
}

func TestInteractionSuccessorScopeAndFutureReferences(t *testing.T) {
	for _, fact := range []string{"response", "failure", "retry", "capture"} {
		for _, reference := range []string{"old run", "future run", "future request", "current"} {
			for _, path := range []string{"builder", "apply", "load"} {
				t.Run(fact+"/"+reference+"/"+path, func(t *testing.T) {
					s := interactionFixture(t, "ended") // Run 1 still has its pending request.
					old := s.Snapshot()
					interactionSubmit(t, s, func(b *SessionCommit) { _, err := b.StartRun(); interactionCheck(t, err) })
					run := s.Snapshot().ActiveRun()
					first := interactionRequest(s, 1)
					first.ID.RunID = run
					interactionSubmit(t, s, func(b *SessionCommit) {
						interactionCheck(t, b.add(NewModelRequestRecord(first, s.policy.PayloadLimits)))
					})
					if fact == "retry" {
						interactionSubmit(t, s, func(b *SessionCommit) {
							interactionCheck(t, b.AddInvocationFailure(InvocationFailure{RequestID: first.ID, Class: InvocationFailureTransport}))
						})
					}
					id := first.ID
					switch reference {
					case "old run":
						id.RunID.Ordinal--
					case "future run":
						id.RunID.Ordinal++
					case "future request":
						id.Ordinal += 2
					}
					// Forge locally valid DTOs directly: these do not obtain the builder's
					// accepted-membership guarantees. Retry DTOs are implicitly same-run.
					var dto historyformat.Record
					var add func(*SessionCommit) error
					switch fact {
					case "response":
						v := sessionformat.ModelResponse{RunID: id.RunID.Ordinal, RequestOrdinal: id.Ordinal, Terminal: sessionformat.ResponseTerminalNormal}
						wire, err := codec.Encode(v, s.policy.PayloadLimits)
						interactionCheck(t, err)
						dto = historyformat.Record{Kind: sessionformat.KindModelResponse, KindVersion: 1, Payload: wire}
						add = func(b *SessionCommit) error {
							return b.AddModelResponse(ModelResponse{RequestID: id, Terminal: ResponseTerminalNormal})
						}
					case "failure":
						v := sessionformat.InvocationFailure{RunID: id.RunID.Ordinal, RequestOrdinal: id.Ordinal, Class: sessionformat.InvocationFailureTransport}
						wire, err := codec.Encode(v, s.policy.PayloadLimits)
						interactionCheck(t, err)
						dto = historyformat.Record{Kind: sessionformat.KindInvocationFailure, KindVersion: 1, Payload: wire}
						add = func(b *SessionCommit) error {
							return b.AddInvocationFailure(InvocationFailure{RequestID: id, Class: InvocationFailureTransport})
						}
					case "capture":
						v := sessionformat.RunEnded{RunID: id.RunID.Ordinal, Reason: sessionformat.RunEndedCaptureFailed, Capture: &sessionformat.CaptureFailureEvidence{RequestOrdinal: id.Ordinal}}
						wire, err := codec.Encode(v, s.policy.PayloadLimits)
						interactionCheck(t, err)
						dto = historyformat.Record{Kind: sessionformat.KindRunEnded, KindVersion: 1, Payload: wire}
						add = func(b *SessionCommit) error {
							return b.AddRunEnded(RunEnded{RunID: id.RunID, Reason: RunEndedCaptureFailed, Capture: &CaptureFailureEvidence{RequestID: id}})
						}
					case "retry":
						next := interactionRequest(s, 2)
						next.ID.RunID = run
						next.RetryOf = &id
						v := next.toFormat()
						if reference == "old run" || reference == "future run" {
							v.RunID = id.RunID.Ordinal
						}
						wire, err := codec.Encode(v, s.policy.PayloadLimits)
						interactionCheck(t, err)
						dto = historyformat.Record{Kind: sessionformat.KindModelRequest, KindVersion: 1, Payload: wire}
						add = func(b *SessionCommit) error {
							_, err := b.AddModelRequest(AddModelRequestRequest{RunID: run, Source: next.Source, Recipe: next.Recipe, RetryOf: &id})
							return err
						}
					}
					before := detachedSessionState(s.state)
					var err error
					if path == "builder" {
						b, e := s.BeginCommit()
						interactionCheck(t, e)
						prior := b.admission
						err = add(b)
						if err != nil && (b.admission != prior || len(b.records) != 0) {
							t.Fatal("rejection consumed ordinal/state")
						}
						interactionCheck(t, b.Discard())
					} else {
						head := s.Snapshot().Head()
						c := historyformat.Commit{FormatVersion: 1, Sequence: head.Sequence + 1, Parent: new(head.CID), Records: []historyformat.Record{dto}}
						ref, e := s.history.Identify(run.HistoryID, c)
						interactionCheck(t, e)
						if path == "apply" {
							err = s.Apply(history.Commit{Ref: ref, Commit: c})
						} else {
							result, e := s.history.Append(context.Background(), run.HistoryID, c)
							interactionCheck(t, e)
							if result.Status != history.StatusAccepted {
								t.Fatal("History rejected locally valid DTO")
							}
							loaded, e := Load(context.Background(), s.history, run.HistoryID, s.policy)
							err = e
							if err != nil && loaded != nil {
								t.Fatal("failed Load published partial state")
							}
						}
					}
					if reference == "current" {
						interactionCheck(t, err)
					} else if err == nil {
						t.Fatal("foreign/future reference admitted")
					}
					if (reference != "current" || path != "apply") && !reflect.DeepEqual(before, detachedSessionState(s.state)) {
						t.Fatal("rejection changed accepted state")
					}
					if old.RecordCount() != 5 {
						t.Fatal("successor rewrote historical pending run")
					}
				})
			}
		}
	}
	// A foreign history cannot be represented by an unscoped DTO; reject it at
	// the public conversion boundary instead of silently omitting its scope.
	s := interactionFixture(t, "responded")
	id := interactionRequest(s, 1).ID
	id.RunID.HistoryID = historyid.NewHistoryID()
	b, err := s.BeginCommit()
	interactionCheck(t, err)
	if _, err = b.AddModelRequest(AddModelRequestRequest{RunID: s.Snapshot().ActiveRun(), Source: interactionRequest(s, 2).Source, Recipe: interactionRequest(s, 2).Recipe, RetryOf: &id}); !errors.Is(err, codec.ErrInvalidData) {
		t.Fatalf("foreign retry: %v", err)
	}
	if err = b.AddModelResponse(ModelResponse{RequestID: id, Terminal: ResponseTerminalNormal}); !errors.Is(err, ErrIdentity) {
		t.Fatalf("foreign outcome: %v", err)
	}
	interactionCheck(t, b.Discard())
}

func TestInteractionUnknownKindAndVersionFailClosed(t *testing.T) {
	for _, kind := range []string{sessionformat.KindModelRequest, sessionformat.KindModelResponse, sessionformat.KindInvocationFailure, sessionformat.KindRunEnded, "session.future"} {
		for _, path := range []string{"decode", "apply", "load"} {
			t.Run(kind+"/"+path, func(t *testing.T) {
				s := interactionFixture(t, "pending")
				dto := historyformat.Record{Kind: kind, KindVersion: 2, Payload: []byte{0xa0}}
				if kind == "session.future" {
					dto.KindVersion = 1
				}
				before := detachedSessionState(s.state)
				var err error
				if path == "decode" {
					var r Record
					r, err = DecodeRecord(s.Snapshot().HistoryID(), dto, s.policy.PayloadLimits)
					if !reflect.DeepEqual(r, Record{}) {
						t.Fatal("failed decode returned record")
					}
				} else {
					head := s.Snapshot().Head()
					c := historyformat.Commit{FormatVersion: 1, Sequence: head.Sequence + 1, Parent: new(head.CID), Records: []historyformat.Record{dto}}
					ref, e := s.history.Identify(s.Snapshot().HistoryID(), c)
					interactionCheck(t, e)
					if path == "apply" {
						err = s.Apply(history.Commit{Ref: ref, Commit: c})
					} else {
						_, e = s.history.Append(context.Background(), s.Snapshot().HistoryID(), c)
						interactionCheck(t, e)
						_, err = Load(context.Background(), s.history, s.Snapshot().HistoryID(), s.policy)
					}
				}
				if !errors.Is(err, ErrUnsupportedRecord) || !reflect.DeepEqual(before, detachedSessionState(s.state)) {
					t.Fatalf("unsupported dispatch: %v", err)
				}
			})
		}
	}
}
