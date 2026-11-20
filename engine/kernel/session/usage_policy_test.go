package session

import (
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/stacklok/mecatl/engine/kernel/codec"
	"github.com/stacklok/mecatl/engine/kernel/history"
	sessionformat "github.com/stacklok/mecatl/engine/kernel/session/format"
)

func TestUsagePayloadPolicyBoundaries(t *testing.T) {
	for field := 0; field < reflect.TypeFor[codec.Limits]().NumField(); field++ {
		for _, short := range []bool{false, true} {
			for _, path := range []string{"submit", "apply", "load"} {
				t.Run(fmt.Sprintf("%s/short=%t/%s", reflect.TypeFor[codec.Limits]().Field(field).Name, short, path), func(t *testing.T) {
					s := interactionFixture(t, "idle")
					v := sessionformat.Usage{ID: 1, Definition: &UsageDefinition{Purpose: UsagePurposeMain}, Report: usageReportFixture()}
					r := interactionRecord(t)(newUsageRecord(v, s.policy.PayloadLimits))
					c := interactionCommit(t, s, []*materializedRecord{r})
					limits := s.policy.PayloadLimits
					lo, hi := 1, int(reflect.ValueOf(limits).Field(field).Int())
					for lo < hi {
						mid := lo + (hi-lo)/2
						l := limits
						reflect.ValueOf(&l).Elem().Field(field).SetInt(int64(mid))
						if _, err := codec.Encode(v, l); err == nil {
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
					// Only the accounting record, not the preceding creation, violates the bound.
					_, err := Load(t.Context(), s.history, s.Snapshot().SessionID(), testConfigFor(s.policy))
					mustBatch(t, err)
					before := detachedSessionState(s.state)
					switch path {
					case "submit":
						b := beginBatch(t, s)
						_, err = b.AddUsage(nil, nil, UsagePurposeMain, v.Report)
						if err == nil {
							_, err = s.SubmitCommit(t.Context(), b)
						} else {
							mustBatch(t, b.Discard())
						}
					case "apply":
						err = s.apply(c)
					case "load":
						result, e := s.history.Append(t.Context(), s.Snapshot().SessionID().ResourceID(), c.Commit)
						mustBatch(t, e)
						if result.Status != history.StatusAccepted {
							t.Fatal("history rejected payload")
						}
						var loaded *Session
						loaded, err = Load(t.Context(), s.history, s.Snapshot().SessionID(), testConfigFor(s.policy))
						if err != nil && loaded != nil {
							t.Fatal("failed load returned partial session")
						}
					}
					if short {
						if !errors.Is(err, codec.ErrLimitExceeded) || !reflect.DeepEqual(before, detachedSessionState(s.state)) {
							t.Fatalf("one-short usage policy: %v", err)
						}
					} else {
						mustBatch(t, err)
					}
				})
			}
		}
	}
}

func TestUsageMixedCommitPolicyAtomicity(t *testing.T) {
	for _, dimension := range []string{"commit count", "retained count", "payload bytes", "decoded work", "commit bytes"} {
		for _, short := range []bool{false, true} {
			for _, path := range []string{"submit", "apply", "load"} {
				t.Run(fmt.Sprintf("%s/short=%t/%s", dimension, short, path), func(t *testing.T) {
					s := interactionFixture(t, "pending")
					request := s.state.view.lifecycle.pending
					b := beginBatch(t, s)
					id, err := b.AddUsage(nil, &request, UsagePurposeMain, usageReportFixture())
					mustBatch(t, err)
					submitBatch(t, s, b)
					records := []*materializedRecord{
						interactionRecord(t)(newModelResponseRecord(ModelResponse{Terminal: ResponseTerminalNormal}, request, s.policy.PayloadLimits)),
						interactionRecord(t)(newUsageRecord(sessionformat.Usage{ID: 1, Report: UsageReport{State: UsageReportWithdrawn}}, s.policy.PayloadLimits)),
						interactionRecord(t)(newUsageRecord(sessionformat.Usage{ID: 2, Definition: &UsageDefinition{Purpose: "aux"}, Report: usageReportFixture()}, s.policy.PayloadLimits)),
						interactionRecord(t)(newRunEndedRecord(RunEnded{Reason: RunEndedCompleted}, request.runID, s.policy.PayloadLimits)),
					}
					c := interactionCommit(t, s, records)
					original := s.policy
					p := original
					delta := 0
					if short {
						delta = 1
					}
					var total uint64
					for _, r := range records {
						total += uint64(len(r.value.Payload))
					}
					switch dimension {
					case "commit count":
						p.MaxRecordsPerCommit = len(records) - delta
					case "retained count":
						p.MaxRetainedRecords = s.state.records + len(records) - delta
						p.SoftMaxRetainedRecords = p.MaxRetainedRecords
					case "payload bytes":
						p.MaxPayloadBytes = s.state.payloadBytes + total - uint64(delta)
						p.SoftMaxPayloadBytes = p.MaxPayloadBytes
					case "decoded work":
						p.MaxDecodedWork = uint64(s.state.records+len(records))*uint64(p.PayloadLimits.MaxBytes+p.PayloadLimits.MaxNodes+p.PayloadLimits.MaxCollectionElements) - uint64(delta)
						p.SoftMaxDecodedWork = p.MaxDecodedWork
					case "commit bytes":
						lo, hi := 1, p.CommitLimits.MaxBytes
						for lo < hi {
							mid := lo + (hi-lo)/2
							l := p.CommitLimits
							l.MaxBytes = mid
							if _, e := codec.Encode(c.Commit, l); e == nil {
								hi = mid
							} else {
								lo = mid + 1
							}
						}
						p.CommitLimits.MaxBytes = lo - delta
					}
					s.policy = p
					before := detachedSessionState(s.state)
					switch path {
					case "submit":
						b = beginBatch(t, s)
						mustBatch(t, b.AddModelResponse(request, ModelResponse{Terminal: ResponseTerminalNormal}))
						mustBatch(t, b.ReplaceUsage(id, UsageReport{State: UsageReportWithdrawn}))
						_, err = b.AddUsage(nil, nil, "aux", usageReportFixture())
						mustBatch(t, err)
						mustBatch(t, b.EndRun(RunEndedCompleted))
						_, err = s.SubmitCommit(t.Context(), b)
					case "apply":
						err = s.apply(c)
					case "load":
						_, e := s.history.Append(t.Context(), s.Snapshot().SessionID().ResourceID(), c.Commit)
						mustBatch(t, e)
						var loaded *Session
						loaded, err = Load(t.Context(), s.history, s.Snapshot().SessionID(), testConfigFor(p))
						if err != nil && loaded != nil {
							t.Fatal("partial load")
						}
					}
					if short {
						if !errors.Is(err, codec.ErrLimitExceeded) || !reflect.DeepEqual(before, detachedSessionState(s.state)) {
							t.Fatalf("mixed commit not atomic: %v", err)
						}
						if path == "submit" {
							s.policy = original
							b = beginBatch(t, s)
							next, e := b.AddUsage(nil, nil, UsagePurposeMain, UsageReport{State: UsageReportActive})
							mustBatch(t, e)
							if next.SessionRelative() != 2 {
								t.Fatal("failed commit consumed usage identity")
							}
							mustBatch(t, b.Discard())
						}
					} else {
						mustBatch(t, err)
						if path == "submit" {
							interactionReplay(t, s)
						}
					}
				})
			}
		}
	}
}

func TestUsageOrdinaryLimitsAndEndingReserve(t *testing.T) {
	for _, mode := range []string{"soft", "reserve"} {
		for _, mixed := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/mixed=%t", mode, mixed), func(t *testing.T) {
				s := interactionFixture(t, "pending")
				original := s.policy
				before := detachedSessionState(s.state)
				if mode == "soft" {
					s.policy.SoftMaxRetainedRecords = s.state.records
				} else {
					s.policy.MaxRetainedRecords = s.state.records + 1
					s.policy.SoftMaxRetainedRecords = s.policy.MaxRetainedRecords
				}
				b := beginBatch(t, s)
				id, err := b.AddUsage(nil, nil, UsagePurposeMain, UsageReport{State: UsageReportActive})
				mustBatch(t, err)
				if mixed {
					mustBatch(t, b.AddRunEnded(s.Snapshot().ActiveRun(), RunEnded{Reason: RunEndedLimit, BulkToolCloseout: true}))
				}
				if _, err := s.SubmitCommit(t.Context(), b); !errors.Is(err, codec.ErrLimitExceeded) || !reflect.DeepEqual(before, detachedSessionState(s.state)) {
					t.Fatalf("accounting bypassed ordinary policy/reserve: %v", err)
				}
				// Only the lone minimal ending may use exceptional admission.
				b = beginBatch(t, s)
				mustBatch(t, b.AddRunEnded(s.Snapshot().ActiveRun(), RunEnded{Reason: RunEndedLimit, BulkToolCloseout: true}))
				submitBatch(t, s, b)
				s.policy = original
				b = beginBatch(t, s)
				again, err := b.AddUsage(nil, nil, UsagePurposeMain, UsageReport{State: UsageReportActive})
				mustBatch(t, err)
				if again != id {
					t.Fatal("rejected accounting consumed allocation")
				}
				submitBatch(t, s, b)
				interactionReplay(t, s)
			})
		}
	}
}
