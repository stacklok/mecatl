package session_test

import (
	"bytes"
	"fmt"
	"iter"
	"testing"

	"github.com/stacklok/mecatl/engine/kernel/contentid"
	"github.com/stacklok/mecatl/engine/kernel/session"
	"github.com/stacklok/mecatl/engine/kernel/views"
)

var interactionTraversalSink interactionTraversal
var interactionItemsSink iter.Seq2[int, session.ResponseItemView]
var interactionItemSink session.ResponseItemView

//go:noinline
func consumeInteractionItems(seq iter.Seq2[int, session.ResponseItemView]) {
	for _, item := range seq {
		interactionItemSink = item
	}
}

// The same public authoring and storage replay fixture serves correctness tests
// and benchmarks. Count, requested byte length, and unrelated siblings vary alone.
func completeInteractionFixture(t testing.TB, count, field, sibling int) map[string]*session.Session {
	t.Helper()
	h, s, p := newIteratorSession(t)
	b := beginBatch(t, s)
	mustBatch(t, b.Create())
	run := batchRun(t, b)
	mustBatch(t, b.ReplaceModelBinding(run.HistoryID(), session.ModelBinding{Route: "r", API: "a", Model: "m", Settings: []byte("{}")}))
	submitBatch(t, s, b)
	parameters := bytes.Repeat([]byte{0xa5}, field)
	check := bytes.Repeat([]byte{0x5a}, sibling)
	// Capture actual accepted heads independently of authored DTOs and views.
	sources := make(map[session.ModelRequestID]session.SourceHead)
	request := func(retry *session.ModelRequestID) session.ModelRequestID {
		head := s.Snapshot().Head()
		b := beginBatch(t, s)
		var relativeRetry *uint64
		if retry != nil {
			relativeRetry = new(retry.RunRelative())
		}
		id, err := b.AddModelRequest(run, session.ModelRequest{Source: session.SourceHead{Sequence: head.Sequence, CID: head.CID}, RetryOf: relativeRetry,
			Recipe: session.ConstructionRecipe{Algorithm: "algorithm", Version: 17, Parameters: parameters, Check: check}})
		mustBatch(t, err)
		sources[id] = session.SourceHead{Sequence: head.Sequence, CID: head.CID}
		submitBatch(t, s, b)
		return id
	}
	usage := &session.InvocationUsage{Input: new(int64(0)), Output: new(int64(1)), CacheRead: new(int64(2)), CacheWrite: new(int64(3)), Reasoning: new(int64(4)),
		Raw: &session.OpaqueProviderData{Producer: "producer", Format: "usage", Version: 19, Data: parameters}}
	evidence := &session.ObservedEvidence{Detail: new(string(check)), Correlation: new("correlation")}
	first := request(nil)
	b = beginBatch(t, s)
	mustBatch(t, b.AddInvocationFailure(first, session.InvocationFailure{Class: session.InvocationFailureTransport, Explanation: new("failure"), Usage: usage, Evidence: evidence}))
	submitBatch(t, s, b)
	second := request(&first)
	response := session.ModelResponse{Terminal: session.ResponseTerminalOther, ProviderStopReason: new("stop"), Usage: usage, Evidence: evidence,
		Private: &session.OpaqueProviderData{Producer: "producer", Format: "private", Version: 19, Data: parameters}}
	for range count {
		response.Items = append(response.Items,
			session.ResponseItem{Content: &session.ResponseContent{Kind: session.ResponseContentReasoning, Parts: []session.ContentPart{
				{Text: new("text")}, {Media: &session.Media{Kind: session.MediaKindDocument, MIMEType: "image/example", Source: session.MediaSourceInline,
					FormatHint: "raw", Filename: "image.bin", Title: "title", InlineData: parameters}},
				{Media: &session.Media{Kind: session.MediaKindDocument, MIMEType: "image/example", Source: session.MediaSourceRemote,
					FormatHint: "raw", Filename: "image.bin", Title: "title", RemoteURI: "https://example.test/media"}},
				{Media: &session.Media{Kind: session.MediaKindDocument, MIMEType: "image/example", Source: session.MediaSourceProvider,
					FormatHint: "raw", Filename: "image.bin", Title: "title", ProviderName: "media-provider", ProviderScope: "media-scope", ProviderReferenceID: "media-reference"}}}}},
			session.ResponseItem{Call: &session.RequestedCall{Name: "call", Arguments: parameters}},
			session.ResponseItem{Content: &session.ResponseContent{Kind: session.ResponseContentOutput, Parts: []session.ContentPart{{Text: new("output")}}}})
	}
	b = beginBatch(t, s)
	mustBatch(t, b.AddModelResponse(second, response))
	mustBatch(t, b.EndRun(session.RunEndedInterrupted))
	submitBatch(t, s, b)
	b = beginBatch(t, s)
	run = batchRun(t, b)
	submitBatch(t, s, b)
	pending := request(nil)
	b = beginBatch(t, s)
	mustBatch(t, b.AddRunEnded(run, session.RunEnded{Reason: session.RunEndedCaptureFailed, Explanation: new("capture"),
		Capture: &session.CaptureFailureEvidence{RequestID: pending.RunRelative(), Usage: usage, Evidence: evidence}}))
	submitBatch(t, s, b)
	loaded, err := session.Load(t.Context(), h, s.Snapshot().HistoryID(), p)
	mustBatch(t, err)
	sessions := map[string]*session.Session{"Submit": s, "Load": loaded}
	for seam, inspected := range sessions {
		seen := 0
		snap := inspected.Snapshot()
		for i := 0; i < snap.RecordCount(); i++ {
			if request, ok := snap.RecordAt(i).ModelRequest(); ok {
				want, found := sources[request.ID()]
				if !found || request.Source().Sequence() != want.Sequence || request.Source().CID() != want.CID {
					t.Fatalf("%s request %v source = (%d, %v), want %+v", seam, request.ID(), request.Source().Sequence(), request.Source().CID(), want)
				}
				seen++
			}
		}
		if seen != len(sources) || seen != 3 {
			t.Fatalf("%s inspected %d request sources, want 3", seam, seen)
		}
	}
	return sessions
}

type interactionTraversal struct {
	requests, retries, failures, responses, endings, captures int
	items, contents, parts, itemCalls, calls                  int
	inlineMedia, remoteMedia, providerMedia                   int
	usages, counters, evidence, providers, recipes            int
	bytes                                                     int
}

// copyInteractionBytes keeps deliberate owned exports observable across the
// benchmark boundary, including small leaves the compiler could otherwise stack-copy.
//
//go:noinline
func copyInteractionBytes(v views.Bytes) []byte {
	return v.Copy()
}

// inspectInteractionBytes always visits the borrowed leaf; only explicit exports copy.
func inspectInteractionBytes(v views.Bytes, expected byte, exports bool) int {
	if v.Len() == 0 {
		panic("empty byte leaf")
	}
	for i, value := range v.All() {
		if value != expected || v.At(i) != value {
			panic("byte leaf")
		}
	}
	if exports {
		return len(copyInteractionBytes(v))
	}
	return 0
}

func inspectInteractionProvider(v session.OpaqueProviderDataView, format string, exports bool, visits *interactionTraversal) {
	if v.Producer() != "producer" || v.Format() != format || v.Version() != 19 {
		panic("provider metadata")
	}
	visits.providers++
	visits.bytes += inspectInteractionBytes(v.Data(), 0xa5, exports)
}

func inspectInteractionUsage(v session.InvocationUsageView, present, exports bool, visits *interactionTraversal) {
	if !present {
		panic("usage presence")
	}
	input, ip := v.Input()
	output, op := v.Output()
	read, rp := v.CacheRead()
	write, wp := v.CacheWrite()
	reasoning, ep := v.Reasoning()
	if !ip || !op || !rp || !wp || !ep || input != 0 || output != 1 || read != 2 || write != 3 || reasoning != 4 {
		panic("usage counters")
	}
	visits.usages++
	visits.counters += 5
	raw, ok := v.Raw()
	if !ok {
		panic("raw presence")
	}
	inspectInteractionProvider(raw, "usage", exports, visits)
}

func inspectInteractionEvidence(v session.ObservedEvidenceView, present bool, visits *interactionTraversal) {
	detail, dp := v.Detail()
	correlation, cp := v.Correlation()
	if !present || !dp || !cp || len(detail) == 0 || correlation != "correlation" {
		panic("evidence strings/presence")
	}
	// Full sibling string values are asserted outside the timed traversal; do
	// not turn scalar navigation into a payload-sized validation scan.
	if detail[0] != 0x5a || detail[len(detail)-1] != 0x5a {
		panic("evidence detail value")
	}
	visits.evidence++
}

// Concrete indexed record navigation and compiler-visible Items/Parts/Calls
// iteration measure the direct path, not opaque iter.Seq escape overhead. Both
// modes inspect every scalar, presence bit and borrowed byte leaf; only Copy is conditional.
func traverseCompleteInteraction(snap session.Snapshot, exports bool) interactionTraversal {
	var visits interactionTraversal
	for i := 0; i < snap.RecordCount(); i++ {
		record := snap.RecordAt(i)
		if v, ok := record.ModelRequest(); ok {
			visits.requests++
			if v.ID().RunID().HistoryID() != snap.HistoryID() || v.Source().Sequence() == 0 || v.Source().CID() == (contentid.CID{}) {
				panic("request scope/source")
			}
			if retry, ok := v.RetryOf(); ok {
				first, _ := snap.RecordAt(3).ModelRequest()
				if retry != first.ID() {
					panic("retry identity")
				}
				visits.retries++
			}
			recipe := v.Recipe()
			if recipe.Algorithm() != "algorithm" || recipe.Version() != 17 {
				panic("recipe metadata")
			}
			visits.recipes++
			visits.bytes += inspectInteractionBytes(recipe.Parameters(), 0xa5, exports)
			visits.bytes += inspectInteractionBytes(recipe.Check(), 0x5a, exports)
		}
		if v, ok := record.InvocationFailure(); ok {
			visits.failures++
			request, _ := snap.RecordAt(3).ModelRequest()
			text, present := v.Explanation()
			if v.RequestID() != request.ID() || v.Class() != session.InvocationFailureTransport || !present || text != "failure" {
				panic("failure scalars")
			}
			u, up := v.Usage()
			e, ep := v.Evidence()
			inspectInteractionUsage(u, up, exports, &visits)
			inspectInteractionEvidence(e, ep, &visits)
		}
		if v, ok := record.ModelResponse(); ok {
			visits.responses++
			request, _ := snap.RecordAt(5).ModelRequest()
			text, present := v.ProviderStopReason()
			if v.RequestID() != request.ID() || v.Terminal() != session.ResponseTerminalOther || !present || text != "stop" || v.Items().Len() != 3*v.CallCount() {
				panic("response scalars")
			}
			u, up := v.Usage()
			e, ep := v.Evidence()
			inspectInteractionUsage(u, up, exports, &visits)
			inspectInteractionEvidence(e, ep, &visits)
			private, ok := v.Private()
			if !ok {
				panic("private presence")
			}
			inspectInteractionProvider(private, "private", exports, &visits)
			for _, item := range v.Items().All() {
				visits.items++
				content, contentOK := item.Content()
				call, callOK := item.Call()
				if contentOK == callOK {
					panic("item union")
				}
				if contentOK {
					visits.contents++
					if (content.Kind() != session.ResponseContentReasoning && content.Kind() != session.ResponseContentOutput) || content.Parts().Len() == 0 {
						panic("content scalars")
					}
					for _, part := range content.Parts().All() {
						visits.parts++
						text, textOK := part.Text()
						media, mediaOK := part.Media()
						if textOK == mediaOK || (textOK && text == "") {
							panic("part union/text")
						}
						if textOK {
							want := "text"
							if content.Kind() == session.ResponseContentOutput {
								want = "output"
							}
							if text != want {
								panic("text value")
							}
						}
						if mediaOK {
							uri, uriOK := media.RemoteURI()
							name, nameOK := media.ProviderName()
							scope, scopeOK := media.ProviderScope()
							ref, refOK := media.ProviderReferenceID()
							if media.Kind() != session.MediaKindDocument || media.MIMEType() != "image/example" || media.FormatHint() != "raw" || media.Filename() != "image.bin" || media.Title() != "title" {
								panic("media metadata")
							}
							data, inline := media.InlineData()
							switch media.Source() {
							case session.MediaSourceInline:
								if !inline || uriOK || nameOK || scopeOK || refOK || uri != "" || name != "" || scope != "" || ref != "" {
									panic("inline media source")
								}
								visits.inlineMedia++
								visits.bytes += inspectInteractionBytes(data, 0xa5, exports)
							case session.MediaSourceRemote:
								if inline || data.Len() != 0 || !uriOK || uri != "https://example.test/media" || nameOK || scopeOK || refOK || name != "" || scope != "" || ref != "" {
									panic("remote media source")
								}
								visits.remoteMedia++
							case session.MediaSourceProvider:
								if inline || data.Len() != 0 || uriOK || uri != "" || !nameOK || !scopeOK || !refOK || name != "media-provider" || scope != "media-scope" || ref != "media-reference" {
									panic("provider media source")
								}
								visits.providerMedia++
							default:
								panic("unknown media source")
							}
						}
					}
				} else {
					visits.itemCalls++
					if call.Name() != "call" {
						panic("identity-free name")
					}
					visits.bytes += inspectInteractionBytes(call.Arguments(), 0xa5, exports)
				}
			}
			var previous session.RequestedCallID
			for call := range v.Calls() {
				if call.ID().RequestID() != v.RequestID() || call.ID() == previous || call.Name() != "call" {
					panic("identity-bearing call")
				}
				previous = call.ID()
				visits.calls++
				visits.bytes += inspectInteractionBytes(call.Arguments(), 0xa5, exports)
			}
		}
		if v, ok := record.RunEnded(); ok {
			visits.endings++
			if v.RunID().HistoryID() != snap.HistoryID() {
				panic("ending scope")
			}
			text, textOK := v.Explanation()
			capture, present := v.Capture()
			if present {
				visits.captures++
				request, _ := snap.RecordAt(9).ModelRequest()
				if v.Reason() != session.RunEndedCaptureFailed || !textOK || text != "capture" || capture.RequestID() != request.ID() || capture.RequestID().RunID() != v.RunID() {
					panic("capture scope/scalars")
				}
				u, up := capture.Usage()
				e, ep := capture.Evidence()
				inspectInteractionUsage(u, up, exports, &visits)
				inspectInteractionEvidence(e, ep, &visits)
			} else if v.Reason() != session.RunEndedInterrupted || textOK {
				panic("minimal ending")
			}
		}
	}
	return visits
}

func BenchmarkModelInteractionCompleteTraversal(b *testing.B) {
	for _, axis := range []struct{ count, field, sibling int }{{1, 32, 32}, {8, 32, 32}, {32, 32, 32}, {1, 16384, 32}, {1, 32, 16384}} {
		b.Run(fmt.Sprintf("count=%d/field=%d/sibling=%d", axis.count, axis.field, axis.sibling), func(b *testing.B) {
			for seam, s := range completeInteractionFixture(b, axis.count, axis.field, axis.sibling) {
				response, _ := s.Snapshot().RecordAt(6).ModelResponse()
				stored := response.Items().All()
				for _, mode := range []string{"escaping-create", "opaque-consume", "escaping-reuse"} {
					b.Run(seam+"/"+mode, func(b *testing.B) {
						b.ReportAllocs()
						for b.Loop() {
							switch mode {
							case "escaping-create":
								interactionItemsSink = response.Items().All()
							case "opaque-consume":
								consumeInteractionItems(response.Items().All())
							case "escaping-reuse":
								consumeInteractionItems(stored)
							}
						}
					})
				}
				for _, exports := range []bool{false, true} {
					b.Run(fmt.Sprintf("%s/direct/exports=%t", seam, exports), func(b *testing.B) {
						snap := s.Snapshot()
						b.ReportAllocs()
						for b.Loop() {
							interactionTraversalSink = traverseCompleteInteraction(snap, exports)
						}
					})
				}
			}
		})
	}
}
