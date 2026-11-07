package session

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
)

var interactionTraversalSink uint64

func BenchmarkModelInteractionCompleteTraversal(b *testing.B) {
	// Count and payload are separate axes, not a single growing fixture.
	for _, axis := range []struct{ count, payload int }{{1, 1}, {8, 1}, {32, 1}, {1, 1024}, {1, 16384}} {
		b.Run(fmt.Sprintf("count=%d/payload=%d", axis.count, axis.payload), func(b *testing.B) {
			s, _ := internalFixture(b)
			limits := s.policy.PayloadLimits
			limits.MaxBytes = 4 << 20
			limits.MaxStringBytes = 1 << 20
			limits.MaxByteStringBytes = 1 << 20
			limits.MaxArrayElements = 1024
			limits.MaxCollectionElements = 1 << 16
			limits.MaxNodes = 1 << 16
			limits.MaxDepth = 32
			s.policy.PayloadLimits = limits
			s.policy.CommitLimits = limits
			s.history = internalTestHistory(b, limits)
			interactionSubmit(b, s, func(c *SessionCommit) {
				interactionCheck(b, c.Create())
				_, err := c.StartRun()
				interactionCheck(b, err)
				interactionCheck(b, c.ReplaceModelBinding(ModelBinding{HistoryID: s.Snapshot().HistoryID(), Route: "r", API: "a", Model: "m", Settings: []byte("{}")}))
			})
			req := interactionRequest(s, 1)
			req.Recipe.Parameters = bytes.Repeat([]byte{0xff}, axis.payload)
			interactionSubmit(b, s, func(c *SessionCommit) { interactionCheck(b, c.add(makeRequestRecord(req, limits))) })
			usage := &InvocationUsage{Input: new(int64(0)), Raw: &OpaqueProviderData{Producer: "p", Format: "u", Version: 1, Data: bytes.Repeat([]byte{0xff}, axis.payload)}}
			interactionSubmit(b, s, func(c *SessionCommit) {
				interactionCheck(b, c.AddInvocationFailure(InvocationFailure{RequestID: req.ID, Class: InvocationFailureTransport, Explanation: new(""), Usage: usage, Evidence: &ObservedEvidence{Correlation: new("")}}))
			})
			next := interactionRequest(s, 2)
			next.RetryOf = &req.ID
			interactionSubmit(b, s, func(c *SessionCommit) { interactionCheck(b, c.add(makeRequestRecord(next, limits))) })
			response := ModelResponse{RequestID: next.ID, Terminal: ResponseTerminalOther, ProviderStopReason: new(""), Usage: usage, Private: usage.Raw, Evidence: &ObservedEvidence{Detail: new("")}}
			for range axis.count {
				response.Items = append(response.Items, ResponseItem{Content: &ResponseContent{Kind: ResponseContentReasoning, Parts: []ContentPart{{Text: new(strings.Repeat("x", axis.payload))}, {Media: &Media{Kind: MediaKindImage, MIMEType: "image/x", Source: MediaSourceInline, InlineData: bytes.Repeat([]byte{0xff}, axis.payload)}}}}}, ResponseItem{Call: &RequestedCall{Name: "", Arguments: bytes.Repeat([]byte{0xff}, axis.payload)}})
			}
			interactionSubmit(b, s, func(c *SessionCommit) {
				interactionCheck(b, c.AddModelResponse(response))
				interactionCheck(b, c.EndRun(RunEndedInterrupted))
			})
			interactionSubmit(b, s, func(c *SessionCommit) { _, err := c.StartRun(); interactionCheck(b, err) })
			pending := interactionRequest(s, 1)
			pending.ID.runID = s.Snapshot().ActiveRun()
			interactionSubmit(b, s, func(c *SessionCommit) { interactionCheck(b, c.add(makeRequestRecord(pending, limits))) })
			interactionSubmit(b, s, func(c *SessionCommit) {
				interactionCheck(b, c.AddRunEnded(RunEnded{RunID: pending.ID.runID, Reason: RunEndedCaptureFailed, Explanation: new(""), Capture: &CaptureFailureEvidence{RequestID: pending.ID, Usage: usage, Evidence: &ObservedEvidence{Correlation: new("")}}}))
			})
			snap := s.Snapshot()
			for _, exports := range []bool{false, true} {
				b.Run(fmt.Sprintf("exports=%t", exports), func(b *testing.B) {
					b.ReportAllocs()
					for b.Loop() {
						var sum uint64
						for record := range snap.Records() {
							if v, ok := record.ModelRequest(); ok {
								sum += v.ID().requestOrdinal + uint64(v.Source().Sequence)
								if retry, ok := v.RetryOf(); ok {
									sum += retry.requestOrdinal
								}
								if exports {
									recipe := v.Recipe()
									sum += uint64(len(recipe.Parameters) + len(recipe.Check))
								}
							}
							if v, ok := record.InvocationFailure(); ok {
								sum += v.RequestID().requestOrdinal + uint64(len(v.Class()))
								detail, _ := v.Explanation()
								sum += uint64(len(detail))
								if exports {
									u, _ := v.Usage()
									e, _ := v.Evidence()
									sum += uint64(len(u.Raw.Data))
									if e.Correlation != nil {
										sum += uint64(len(*e.Correlation))
									}
								}
							}
							if v, ok := record.ModelResponse(); ok {
								sum += v.RequestID().requestOrdinal + uint64(len(v.Terminal())+v.ItemCount()+v.CallCount())
								reason, _ := v.ProviderStopReason()
								sum += uint64(len(reason))
								for item := range v.Items() {
									if c, ok := item.Content(); ok {
										sum += uint64(len(c.Kind()) + c.PartCount())
										for part := range c.Parts() {
											if text, ok := part.Text(); ok {
												sum += uint64(len(text))
											}
											if media, ok := part.Media(); ok {
												sum += uint64(len(media.Kind()) + len(media.MIMEType()) + len(media.Source()))
												if exports {
													data, _ := media.InlineData()
													sum += uint64(len(data))
												}
											}
										}
									} else if exports {
										call, _ := item.Call()
										sum += uint64(len(call.Name) + len(call.Arguments))
									}
								}
								for call := range v.Calls() {
									sum += call.ID().ordinal + uint64(len(call.Name()))
									if exports {
										sum += uint64(len(call.Arguments()))
									}
								}
								if exports {
									u, _ := v.Usage()
									p, _ := v.Private()
									e, _ := v.Evidence()
									sum += uint64(len(u.Raw.Data) + len(p.Data))
									if e.Detail != nil {
										sum += uint64(len(*e.Detail))
									}
								}
							}
							if v, ok := record.RunEnded(); ok {
								sum += v.RunID().ordinal + uint64(len(v.Reason()))
								detail, _ := v.Explanation()
								sum += uint64(len(detail))
								if exports {
									if capture, ok := v.Capture(); ok {
										sum += capture.RequestID.requestOrdinal + uint64(len(capture.Usage.Raw.Data))
										if capture.Evidence.Correlation != nil {
											sum += uint64(len(*capture.Evidence.Correlation))
										}
									}
								}
							}
						}
						interactionTraversalSink = sum
					}
				})
			}
		})
	}
}
