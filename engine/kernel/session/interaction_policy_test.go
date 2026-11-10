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

func TestInteractionResponsePayloadPolicyBoundaries(t *testing.T) {
	for field := 0; field < reflect.TypeFor[codec.Limits]().NumField(); field++ {
		name := reflect.TypeFor[codec.Limits]().Field(field).Name
		for _, short := range []bool{false, true} {
			for _, path := range []string{"submit", "apply", "load"} {
				t.Run(fmt.Sprintf("%s/short=%t/%s", name, short, path), func(t *testing.T) {
					s := interactionFixture(t, "pending")
					id := s.state.view.lifecycle.pending
					response := ModelResponse{Terminal: ResponseTerminalOther, ProviderStopReason: new(string(make([]byte, 120))), Evidence: &ObservedEvidence{Detail: new(""), Correlation: new("")}, Usage: &InvocationUsage{Input: new(int64(0)), Output: new(int64(0)), CacheRead: new(int64(0)), CacheWrite: new(int64(0)), Reasoning: new(int64(0)), Raw: &OpaqueProviderData{Producer: "p", Format: "u", Version: 1, Data: make([]byte, 128)}}, Private: &OpaqueProviderData{Producer: "p", Format: "private", Version: 1, Data: make([]byte, 128)}, Items: []ResponseItem{{Content: &ResponseContent{Kind: ResponseContentOutput, Parts: []ContentPart{{Text: new("")}, {Media: &Media{Kind: MediaKindImage, MIMEType: "image/x", Source: MediaSourceInline, InlineData: make([]byte, 128)}}}}}}}
					for range 8 {
						response.Items = append(response.Items, ResponseItem{Call: &RequestedCall{Arguments: []byte{0xff}}})
					}
					stored := response
					stored.RunID, stored.RequestID = id.runID.ordinal, id.requestOrdinal
					r := interactionRecord(t)(newModelResponseRecord(response, id, s.policy.PayloadLimits))
					c := interactionCommit(t, s, []*materializedRecord{r})
					limits := s.policy.PayloadLimits
					lo, hi := 1, int(reflect.ValueOf(limits).Field(field).Int())
					for lo < hi {
						mid := lo + (hi-lo)/2
						l := limits
						reflect.ValueOf(&l).Elem().Field(field).SetInt(int64(mid))
						if _, err := codec.Encode(stored, l); err == nil {
							hi = mid
						} else {
							lo = mid + 1
						}
					}
					if short {
						lo--
					}
					reflect.ValueOf(&limits).Elem().Field(field).SetInt(int64(lo))
					s.policy.PayloadLimits = limits
					// Prove a Load rejection is caused by this response, not an earlier
					// fact made inadmissible by the deliberately tighter payload policy.
					_, err := Load(context.Background(), s.history, id.runID.historyID, s.policy)
					interactionCheck(t, err)
					before := detachedSessionState(s.state)
					switch path {
					case "submit":
						b, e := s.BeginCommit()
						interactionCheck(t, e)
						err = b.AddModelResponse(id, response)
						if err == nil {
							_, err = s.SubmitCommit(context.Background(), b)
						} else {
							interactionCheck(t, b.Discard())
						}
					case "apply":
						err = s.apply(c)
					case "load":
						result, e := s.history.Append(context.Background(), id.runID.historyID, c.Commit)
						interactionCheck(t, e)
						if result.Status != history.StatusAccepted {
							t.Fatal("History response admission")
						}
						_, err = Load(context.Background(), s.history, id.runID.historyID, s.policy)
					}
					if short {
						if !errors.Is(err, codec.ErrLimitExceeded) || !reflect.DeepEqual(before, detachedSessionState(s.state)) {
							t.Fatalf("one-short payload policy: %v", err)
						}
					} else {
						interactionCheck(t, err)
					}
				})
			}
		}
	}
}

func TestInteractionOutcomePolicyBoundaries(t *testing.T) {
	for _, dimension := range []string{"commit count", "retained count", "payload bytes", "decoded work"} {
		for _, short := range []bool{false, true} {
			for _, path := range []string{"submit", "apply", "load"} {
				t.Run(fmt.Sprintf("%s/short=%t/%s", dimension, short, path), func(t *testing.T) {
					s, _ := internalFixture(t)
					// Every prefix commit has one record, so the one-short
					// count policy rejects the candidate, not its prefix.
					interactionSubmit(t, s, func(b *SessionCommit) { interactionCheck(t, b.Create()) })
					interactionSubmit(t, s, func(b *SessionCommit) {
						interactionCheck(t, b.ReplaceModelBinding(s.Snapshot().HistoryID(), ModelBinding{Route: "r", API: "a", Model: "m", Settings: []byte("{}")}))
					})
					interactionSubmit(t, s, func(b *SessionCommit) { _, err := b.StartRun(); interactionCheck(t, err) })
					request := interactionRequest(s, 1)
					interactionSubmit(t, s, func(b *SessionCommit) {
						interactionCheck(t, b.add(makeRequestRecord(request, s.policy.PayloadLimits)))
					})
					id := s.state.view.lifecycle.pending
					records := []*materializedRecord{
						interactionRecord(t)(newModelResponseRecord(ModelResponse{Terminal: ResponseTerminalNormal, Private: &OpaqueProviderData{Producer: "p", Format: "f", Version: 1, Data: []byte{0xff}}}, id, s.policy.PayloadLimits)),
						interactionRecord(t)(newRunEndedRecord(RunEnded{Reason: RunEndedCompleted}, id.runID, s.policy.PayloadLimits)),
					}
					c := interactionCommit(t, s, records)
					p := s.policy
					switch dimension {
					case "commit count":
						p.MaxRecordsPerCommit = 2
						if short {
							p.MaxRecordsPerCommit--
						}
					case "retained count":
						p.MaxRetainedRecords = s.state.records + 2
						if short {
							p.MaxRetainedRecords--
						}
						p.SoftMaxRetainedRecords = p.MaxRetainedRecords
					case "payload bytes":
						p.MaxPayloadBytes = s.state.payloadBytes + uint64(len(records[0].value.Payload)+len(records[1].value.Payload))
						if short {
							p.MaxPayloadBytes--
						}
						p.SoftMaxPayloadBytes = p.MaxPayloadBytes
					case "decoded work":
						p.MaxDecodedWork = uint64(s.state.records+2) * uint64(p.PayloadLimits.MaxBytes+p.PayloadLimits.MaxNodes+p.PayloadLimits.MaxCollectionElements)
						if short {
							p.MaxDecodedWork--
						}
						p.SoftMaxDecodedWork = p.MaxDecodedWork
					}
					s.policy = p
					before := detachedSessionState(s.state)
					var err error
					switch path {
					case "submit":
						b, e := s.BeginCommit()
						interactionCheck(t, e)
						for _, r := range records {
							interactionCheck(t, b.add(r, nil))
						}
						_, err = s.SubmitCommit(context.Background(), b)
					case "apply":
						err = s.apply(c)
					case "load":
						result, e := s.history.Append(context.Background(), id.runID.historyID, c.Commit)
						interactionCheck(t, e)
						if result.Status != history.StatusAccepted {
							t.Fatal("memory History acceptance")
						}
						var loaded *Session
						loaded, err = Load(context.Background(), s.history, id.runID.historyID, p)
						if err != nil && loaded != nil {
							t.Fatal("failed load published partial state")
						}
					}
					if short {
						if !errors.Is(err, codec.ErrLimitExceeded) || errors.Is(err, ErrAcceptanceUnknown) || !reflect.DeepEqual(before, detachedSessionState(s.state)) {
							t.Fatalf("one-short policy admission: %v", err)
						}
					} else {
						interactionCheck(t, err)
					}
				})
			}
		}
	}
}

func TestInteractionMinimalEndingEnvelopeWidths(t *testing.T) {
	// Exercise real accepted chains on both sides of the compact and uint8
	// sequence widths. Check actual wire bytes, not the conservative sizing budget.
	for _, next := range []int64{23, 24, 255, 256} {
		t.Run(fmt.Sprint(next), func(t *testing.T) {
			s := interactionFixture(t, "pending")
			for s.Snapshot().Head().Sequence < next-1 {
				interactionSubmit(t, s, func(b *SessionCommit) {
					interactionCheck(t, b.ReplaceInstructions(s.Snapshot().HistoryID(), Instructions{}))
				})
			}
			for _, reason := range []RunEndedReason{RunEndedLimit, RunEndedCaptureFailed} {
				ending, err := s.prepareEnding(reason)
				interactionCheck(t, err)
				if ending.commit.Sequence != next {
					t.Fatal("wrong reserved sequence")
				}
				wire, err := codec.Encode(ending.commit, s.policy.CommitLimits)
				interactionCheck(t, err)
				prior := ending.commit.Clone()
				prior.Sequence--
				previous, err := codec.Encode(prior, s.policy.CommitLimits)
				interactionCheck(t, err)
				delta := 0
				if next == 24 || next == 256 {
					delta = 1
				}
				if len(wire) != len(previous)+delta {
					t.Fatalf("sequence width delta: %d", len(wire)-len(previous))
				}
				budget, err := codec.NewBudget(s.policy.CommitLimits)
				interactionCheck(t, err)
				interactionCheck(t, ending.commit.CheckCBOR(&budget, 0, math.MaxInt))
				for _, short := range []bool{false, true} {
					limits := s.policy.CommitLimits
					limits.MaxBytes = budget.EstimatedBytes()
					if short {
						limits.MaxBytes--
					}
					limits.MaxStringBytes = min(limits.MaxStringBytes, limits.MaxBytes)
					limits.MaxByteStringBytes = min(limits.MaxByteStringBytes, limits.MaxBytes)
					_, err = codec.Encode(ending.commit, limits)
					if short {
						if !errors.Is(err, codec.ErrLimitExceeded) {
							t.Fatalf("envelope one-short: %v", err)
						}
					} else {
						interactionCheck(t, err)
					}
				}
			}
			interactionSubmit(t, s, func(b *SessionCommit) { interactionCheck(t, b.EndRun(RunEndedCaptureFailed)) })
			interactionReplay(t, s)
		})
	}
}
