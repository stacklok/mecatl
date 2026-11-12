package session_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"reflect"
	"runtime"
	"slices"
	"sync"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/history/mem"
	"github.com/stacklok/mecatl/engine/kernel/codec"
	"github.com/stacklok/mecatl/engine/kernel/history"
	"github.com/stacklok/mecatl/engine/kernel/session"
	"github.com/stacklok/mecatl/engine/kernel/views"
)

// collectView detaches collection handles for assertions, not traversal benchmarks.
func collectView[T, V any](v views.SliceView[T, V]) []V {
	var result []V
	for _, element := range v.All() {
		result = append(result, element)
	}
	return result
}

func addInteractionRequest(t testing.TB, s *session.Session, run session.RunID, retry *session.ModelRequestID, parameters []byte) session.ModelRequestID {
	t.Helper()
	head := s.Snapshot().Head()
	b := beginBatch(t, s)
	var relativeRetry *uint64
	if retry != nil {
		relativeRetry = new(retry.RunRelative())
	}
	id, err := b.AddModelRequest(run, session.ModelRequest{Source: session.SourceHead{Sequence: head.Sequence, CID: head.CID}, Recipe: session.ConstructionRecipe{Algorithm: "future/algorithm", Version: 17, Parameters: parameters, Check: []byte{0xff, 0, 1}}, RetryOf: relativeRetry})
	mustBatch(t, err)
	submitBatch(t, s, b)
	return id
}

func TestModelInteractionUnknownAcceptanceFreezesExactProposal(t *testing.T) {
	for _, accepted := range []bool{false, true} {
		for _, outcome := range []bool{false, true} {
			backend := &batchBackend{Backend: mem.New()}
			s := boundSession(t, newLifecycleHistory(t, backend), sessionPolicy())
			b := beginBatch(t, s)
			mustBatch(t, b.Create())
			run := batchRun(t, b)
			mustBatch(t, b.ReplaceModelBinding(run.HistoryID(), session.ModelBinding{Route: "r", API: "a", Model: "m", Settings: []byte("{}")}))
			submitBatch(t, s, b)
			head := s.Snapshot().Head()
			b = beginBatch(t, s)
			add := session.ModelRequest{Source: session.SourceHead{Sequence: head.Sequence, CID: head.CID}, Recipe: session.ConstructionRecipe{Algorithm: "a", Version: 1, Parameters: []byte("original"), Check: []byte{1}}}
			stale := add
			stale.Source.Sequence++
			if id, err := b.AddModelRequest(run, stale); id != (session.ModelRequestID{}) || !errors.Is(err, session.ErrIdentity) {
				t.Fatalf("stale request: %v", err)
			}
			id, err := b.AddModelRequest(run, add)
			mustBatch(t, err)
			if id != allocatedRequest(t, run, 1) {
				t.Fatal("failed addition consumed identity")
			}
			if outcome {
				submitBatch(t, s, b)
				b = beginBatch(t, s)
				mustBatch(t, b.AddModelResponse(id, session.ModelResponse{Terminal: session.ResponseTerminalNormal}))
			}
			before := s.Snapshot()
			backend.accept = accepted
			backend.fail = codec.ErrLimitExceeded
			ref, err := s.SubmitCommit(context.Background(), b)
			if ref != (history.CommitRef{}) || !errors.Is(err, session.ErrAcceptanceUnknown) || !errors.Is(err, codec.ErrLimitExceeded) || !reflect.DeepEqual(before, s.Snapshot()) {
				t.Fatalf("unknown limit-shaped result: %v", err)
			}
			attempt := backend.attempts[len(backend.attempts)-1]
			clear(add.Recipe.Parameters)
			if err := b.EndRun(session.RunEndedCaptureFailed); !errors.Is(err, session.ErrBatch) {
				t.Fatal("alternate ending after uncertainty")
			}
			if err := b.AddInvocationFailure(id, session.InvocationFailure{Class: session.InvocationFailureTimeout}); !errors.Is(err, session.ErrBatch) {
				t.Fatal("alternate outcome after uncertainty")
			}
			if _, err := s.BeginCommit(); !errors.Is(err, session.ErrBatch) {
				t.Fatal("uncertain slot released")
			}
			if got := submitBatch(t, s, b); got != attempt.commit.Ref || !reflect.DeepEqual(backend.attempts[len(backend.attempts)-1], attempt) {
				t.Fatal("retry changed exact proposal")
			}
			if !outcome {
				v, ok := s.Snapshot().RecordAt(3).ModelRequest()
				if !ok || v.ID() != id || string(v.Recipe().Parameters().Copy()) != "original" {
					t.Fatal("frozen request changed")
				}
			}
		}
	}
}

func TestCompleteInteractionTraversal(t *testing.T) {
	for _, axis := range []struct {
		name                  string
		count, field, sibling int
	}{{"small", 1, 32, 32}, {"count", 32, 32, 32}, {"requested-large", 1, 64 << 10, 32}, {"sibling-large", 1, 32, 64 << 10}} {
		t.Run(axis.name, func(t *testing.T) {
			for seam, s := range completeInteractionFixture(t, axis.count, axis.field, axis.sibling) {
				t.Run(seam, func(t *testing.T) {
					snap := s.Snapshot()
					want := interactionTraversal{requests: 3, retries: 1, failures: 1, responses: 1, endings: 2, captures: 1,
						items: 3 * axis.count, contents: 2 * axis.count, parts: 5 * axis.count, itemCalls: axis.count, calls: axis.count,
						inlineMedia: axis.count, remoteMedia: axis.count, providerMedia: axis.count,
						usages: 3, counters: 15, evidence: 3, providers: 4, recipes: 3}
					for _, exports := range []bool{false, true} {
						want.bytes = 0
						if exports {
							want.bytes = (7+3*axis.count)*axis.field + 3*axis.sibling
						}
						for range 2 {
							if got := traverseCompleteInteraction(snap, exports); got != want {
								t.Fatalf("exports=%t visits = %+v; want %+v", exports, got, want)
							}
						}
					}
					assertIteratorNoAllocation(t, func() { interactionTraversalSink = traverseCompleteInteraction(snap, false) })
					// A deliberately retained whole-payload clone makes this allocation
					// assertion sensitive to accidental aggregate exports in traversal.
					payload := snap.RecordAt(6).Format().Payload
					var copied []byte
					if n := testing.AllocsPerRun(10, func() {
						interactionTraversalSink = traverseCompleteInteraction(snap, false)
						copied = bytes.Clone(payload)
					}); n == 0 {
						t.Fatal("clone control optimized away")
					}
					runtime.KeepAlive(copied)
					response, _ := snap.RecordAt(6).ModelResponse()
					failure, _ := snap.RecordAt(4).InvocationFailure()
					ending, _ := snap.RecordAt(10).RunEnded()
					capture, _ := ending.Capture()
					failureEvidence, _ := failure.Evidence()
					responseEvidence, _ := response.Evidence()
					captureEvidence, _ := capture.Evidence()
					for _, evidence := range []session.ObservedEvidenceView{failureEvidence, responseEvidence, captureEvidence} {
						if detail, ok := evidence.Detail(); !ok || detail != string(bytes.Repeat([]byte{0x5a}, axis.sibling)) {
							t.Fatal("full evidence detail value")
						}
					}
					items, calls := response.Items(), response.Calls()
					for index, item := range items.All() {
						if item != items.At(index) {
							t.Fatal("item adapter changed index or backing")
						}
						if content, ok := item.Content(); ok {
							parts := content.Parts()
							for index, part := range parts.All() {
								if part != parts.At(index) {
									t.Fatal("part adapter changed index or backing")
								}
							}
						}
					}
					visits := 0
					items.All()(func(_ int, _ session.ResponseItemView) bool { visits++; return false })
					if visits != 1 {
						t.Fatal("Items ignored false")
					}
					visits = 0
					calls(func(session.RequestedCallView) bool { visits++; return false })
					if visits != 1 {
						t.Fatal("Calls ignored false")
					}
					for range 2 {
						if got := collectView(items); len(got) != 3*axis.count {
							t.Fatal("Items not repeatable")
						}
						got := slices.Collect(calls)
						if len(got) != axis.count {
							t.Fatal("Calls not repeatable")
						}
						for i, call := range got {
							if call.ID() != response.CallAt(i).ID() {
								t.Fatal("interleaved call ordinal")
							}
							itemCall, ok := response.Items().At(3*i + 1).Call()
							if !ok || itemCall.Name() != call.Name() || !bytes.Equal(itemCall.Arguments().Copy(), call.Arguments().Copy()) {
								t.Fatal("item/call paths disagree")
							}
						}
					}
				})
			}
		})
	}
}

func TestInteractionByteExportsCopyOnlyRequestedField(t *testing.T) {
	for _, axis := range []struct {
		name           string
		field, sibling int
	}{{"small", 32, 32}, {"requested-large", 64 << 10, 32}, {"sibling-large", 32, 64 << 10}} {
		t.Run(axis.name, func(t *testing.T) {
			for seam, s := range completeInteractionFixture(t, 2, axis.field, axis.sibling) {
				t.Run(seam, func(t *testing.T) {
					snap := s.Snapshot()
					request, _ := snap.RecordAt(3).ModelRequest()
					failure, _ := snap.RecordAt(4).InvocationFailure()
					response, _ := snap.RecordAt(6).ModelResponse()
					ending, _ := snap.RecordAt(10).RunEnded()
					capture, _ := ending.Capture()
					failureUsage, _ := failure.Usage()
					responseUsage, _ := response.Usage()
					captureUsage, _ := capture.Usage()
					failureRaw, _ := failureUsage.Raw()
					responseRaw, _ := responseUsage.Raw()
					captureRaw, _ := captureUsage.Raw()
					private, _ := response.Private()
					itemCall, _ := response.Items().At(1).Call()
					content, _ := response.Items().At(0).Content()
					media, _ := content.Parts().At(1).Media()
					for _, field := range []struct {
						name   string
						export func() []byte
						parent int
						size   int
					}{
						{"parameters", request.Recipe().Parameters().Copy, 3, axis.field}, {"check", request.Recipe().Check().Copy, 3, axis.sibling},
						{"failure-raw", failureRaw.Data().Copy, 4, axis.field}, {"response-raw", responseRaw.Data().Copy, 6, axis.field},
						{"capture-raw", captureRaw.Data().Copy, 10, axis.field}, {"private", private.Data().Copy, 6, axis.field},
						{"item-call", itemCall.Arguments().Copy, 6, axis.field}, {"call", response.CallAt(0).Arguments().Copy, 6, axis.field},
						{"media", func() []byte { return media.InlineData().Copy() }, 6, axis.field},
					} {
						t.Run(field.name, func(t *testing.T) {
							var copied []byte
							operation := func() { copied = field.export() }
							if n := testing.AllocsPerRun(100, operation); n != 1 {
								t.Fatalf("field export allocations = %g; want one", n)
							}
							if n := allocatedViewBytes(operation); n < uint64(field.size) || n > uint64(field.size+4096) {
								t.Fatalf("field export allocated %d bytes/op for %d-byte field", n, field.size)
							}
							if len(copied) != field.size {
								t.Fatal("wrong byte length")
							}
							other := field.export()
							value := byte(0xa5)
							if field.name == "check" {
								value = 0x5a
							}
							if !bytes.Equal(other, bytes.Repeat([]byte{value}, field.size)) {
								t.Fatal("exported byte values")
							}
							want := bytes.Clone(other)
							clear(copied)
							if !bytes.Equal(other, want) || !bytes.Equal(field.export(), want) {
								t.Fatal("export mutation changed backing or another export")
							}
							if axis.name != "small" && field.size == 32 {
								payload := snap.RecordAt(field.parent).Format().Payload
								control := func() { copied = bytes.Clone(payload) }
								if n := testing.AllocsPerRun(10, control); n != 1 {
									t.Fatal("clone control")
								}
								if n := allocatedViewBytes(control); n <= uint64(field.size+4096) {
									t.Fatalf("whole-payload size control missed sibling: %d", n)
								}
							}
							runtime.KeepAlive(copied)
						})
					}
				})
			}
		})
	}
}

func TestInteractionNestedHandlesPinnedAcrossAppends(t *testing.T) {
	for _, seam := range []string{"Submit", "Load"} {
		t.Run(seam, func(t *testing.T) {
			s := completeInteractionFixture(t, 2, 32, 32)[seam]
			old := s.Snapshot()
			request, _ := old.RecordAt(3).ModelRequest()
			recipe := request.Recipe()
			failure, _ := old.RecordAt(4).InvocationFailure()
			usage, _ := failure.Usage()
			raw, _ := usage.Raw()
			evidence, _ := failure.Evidence()
			response, _ := old.RecordAt(6).ModelResponse()
			private, _ := response.Private()
			itemCall, _ := response.Items().At(1).Call()
			items := response.Items()
			content, _ := items.At(0).Content()
			parts := content.Parts()
			media, _ := parts.At(1).Media()
			inlineData := media.InlineData()
			leaves := []views.Bytes{recipe.Parameters(), recipe.Check(), raw.Data(), private.Data(), itemCall.Arguments(), inlineData}
			call := response.CallAt(0)
			ending, _ := old.RecordAt(10).RunEnded()
			capture, _ := ending.Capture()
			captureUsage, _ := capture.Usage()
			captureEvidence, _ := capture.Evidence()
			want := traverseCompleteInteraction(old, true)
			var wg sync.WaitGroup
			start := make(chan struct{})
			for range 4 {
				wg.Go(func() {
					<-start
					for range 100 {
						for _, leaf := range leaves {
							copied := leaf.Copy()
							first := leaf.At(0)
							clear(copied)
							if leaf.Len() != 32 || leaf.At(0) != first {
								t.Error("retained byte view changed")
							}
						}
						for index, item := range items.All() {
							if item != response.Items().At(index) {
								t.Error("retained collection adapter changed")
							}
						}
						if parts.Len() != 4 || items.Len() != 6 || parts.At(1) != content.Parts().At(1) {
							t.Error("retained collection bounds changed")
						}
						var visits interactionTraversal
						inspectInteractionUsage(usage, true, true, &visits)
						inspectInteractionUsage(captureUsage, true, true, &visits)
						inspectInteractionEvidence(evidence, true, &visits)
						inspectInteractionEvidence(captureEvidence, true, &visits)
						inspectInteractionProvider(raw, "usage", true, &visits)
						inspectInteractionProvider(private, "private", true, &visits)
						if traverseCompleteInteraction(old, true) != want || recipe.Algorithm() != "algorithm" || recipe.Version() != 17 || len(recipe.Parameters().Copy()) != 32 || len(recipe.Check().Copy()) != 32 || itemCall.Name() != "call" || len(itemCall.Arguments().Copy()) != 32 || call.Name() != "call" || call.ID().RequestID() != response.RequestID() || capture.RequestID().RunID() != ending.RunID() {
							t.Error("retained nested handle changed")
						}
					}
				})
			}
			close(start)
			for range 16 {
				b := beginBatch(t, s)
				batchRun(t, b)
				mustBatch(t, b.EndRun(session.RunEndedInterrupted))
				submitBatch(t, s, b)
			}
			wg.Wait()
			if old.RecordCount() != 11 || s.Snapshot().RecordCount() != 43 {
				t.Fatal("pinned record bounds")
			}
		})
	}
}

func TestInteractionNestedAggregatePresence(t *testing.T) {
	for _, kind := range []string{"failure", "response", "capture"} {
		for _, state := range []string{"absent", "empty", "explicit-zero"} {
			t.Run(fmt.Sprintf("%s/%s", kind, state), func(t *testing.T) {
				present, explicit := state != "absent", state == "explicit-zero"
				h, s, p := newIteratorSession(t)
				b := beginBatch(t, s)
				mustBatch(t, b.Create())
				run := batchRun(t, b)
				mustBatch(t, b.ReplaceModelBinding(run.HistoryID(), session.ModelBinding{Route: "r", API: "a", Model: "m", Settings: []byte("{}")}))
				submitBatch(t, s, b)
				id := addInteractionRequest(t, s, run, nil, nil)
				var usage *session.InvocationUsage
				var evidence *session.ObservedEvidence
				if present {
					usage, evidence = &session.InvocationUsage{}, &session.ObservedEvidence{}
				}
				if explicit {
					usage = &session.InvocationUsage{Input: new(int64(0)), Output: new(int64(0)), CacheRead: new(int64(0)), CacheWrite: new(int64(0)), Reasoning: new(int64(0))}
					evidence = &session.ObservedEvidence{Detail: new(""), Correlation: new("")}
				}
				b = beginBatch(t, s)
				switch kind {
				case "failure":
					mustBatch(t, b.AddInvocationFailure(id, session.InvocationFailure{Class: session.InvocationFailureLocal, Usage: usage, Evidence: evidence}))
				case "response":
					mustBatch(t, b.AddModelResponse(id, session.ModelResponse{Terminal: session.ResponseTerminalNormal, Usage: usage, Evidence: evidence}))
				case "capture":
					mustBatch(t, b.AddRunEnded(run, session.RunEnded{Reason: session.RunEndedCaptureFailed, Capture: &session.CaptureFailureEvidence{RequestID: id.RunRelative(), Usage: usage, Evidence: evidence}}))
				}
				submitBatch(t, s, b)
				loaded, err := session.Load(t.Context(), h, s.Snapshot().HistoryID(), p)
				mustBatch(t, err)
				for seam, s := range map[string]*session.Session{"Submit": s, "Load": loaded} {
					t.Run(seam, func(t *testing.T) {
						record := s.Snapshot().RecordAt(4)
						var u session.InvocationUsageView
						var e session.ObservedEvidenceView
						var up, ep bool
						switch kind {
						case "failure":
							v, _ := record.InvocationFailure()
							u, up = v.Usage()
							e, ep = v.Evidence()
						case "response":
							v, _ := record.ModelResponse()
							u, up = v.Usage()
							e, ep = v.Evidence()
							if private, ok := v.Private(); ok || private != (session.OpaqueProviderDataView{}) {
								t.Fatal("absent private")
							}
						case "capture":
							v, _ := record.RunEnded()
							capture, ok := v.Capture()
							if !ok || capture.RequestID() != id {
								t.Fatal("empty capture scope")
							}
							u, up = capture.Usage()
							e, ep = capture.Evidence()
						}
						if up != present || ep != present {
							t.Fatal("aggregate presence collapsed")
						}
						for _, get := range []func() (int64, bool){u.Input, u.Output, u.CacheRead, u.CacheWrite, u.Reasoning} {
							if value, ok := get(); ok != explicit || value != 0 {
								t.Fatal("empty usage counter")
							}
						}
						if raw, ok := u.Raw(); ok || raw != (session.OpaqueProviderDataView{}) {
							t.Fatal("empty raw")
						}
						if detail, ok := e.Detail(); ok != explicit || detail != "" {
							t.Fatal("empty detail")
						}
						if correlation, ok := e.Correlation(); ok != explicit || correlation != "" {
							t.Fatal("empty correlation")
						}
						if !present && (u != (session.InvocationUsageView{}) || e != (session.ObservedEvidenceView{})) {
							t.Fatal("absent child not zero")
						}
					})
				}
			})
		}
	}
	var capture session.CaptureFailureEvidenceView
	if u, ok := capture.Usage(); ok || u != (session.InvocationUsageView{}) {
		t.Fatal("zero capture usage")
	}
	if e, ok := capture.Evidence(); ok || e != (session.ObservedEvidenceView{}) {
		t.Fatal("zero capture evidence")
	}
	var call session.RequestedCallDataView
	if call.Name() != "" || call.Arguments().Copy() != nil {
		t.Fatal("zero identity-free call")
	}
}

func TestInteractionEmptyOpaqueProviderDataPresenceAcrossSubmitAndLoad(t *testing.T) {
	for _, input := range [][]byte{nil, {}} {
		name := "nil"
		if input != nil {
			name = "empty"
		}
		for _, kind := range []string{"failure", "response", "capture"} {
			t.Run(kind+"/"+name, func(t *testing.T) {
				h, s, p := newIteratorSession(t)
				b := beginBatch(t, s)
				mustBatch(t, b.Create())
				run := batchRun(t, b)
				mustBatch(t, b.ReplaceModelBinding(run.HistoryID(), session.ModelBinding{Route: "r", API: "a", Model: "m", Settings: []byte("{}")}))
				submitBatch(t, s, b)
				id := addInteractionRequest(t, s, run, nil, nil)
				raw := &session.OpaqueProviderData{Producer: "producer", Format: "format", Version: 3, Data: input}
				b = beginBatch(t, s)
				switch kind {
				case "failure":
					mustBatch(t, b.AddInvocationFailure(id, session.InvocationFailure{Class: session.InvocationFailureLocal, Usage: &session.InvocationUsage{Raw: raw}}))
				case "response":
					mustBatch(t, b.AddModelResponse(id, session.ModelResponse{Terminal: session.ResponseTerminalNormal, Usage: &session.InvocationUsage{Raw: raw}, Private: &session.OpaqueProviderData{Producer: "private", Format: "envelope", Version: 4, Data: input}, Items: []session.ResponseItem{
						{Call: &session.RequestedCall{Arguments: input}},
						{Content: &session.ResponseContent{Kind: session.ResponseContentOutput, Parts: []session.ContentPart{{Media: &session.Media{Kind: session.MediaKindImage, MIMEType: "image/example", Source: session.MediaSourceInline, InlineData: input}}}}},
					}}))
				case "capture":
					mustBatch(t, b.AddRunEnded(run, session.RunEnded{Reason: session.RunEndedCaptureFailed, Capture: &session.CaptureFailureEvidence{RequestID: id.RunRelative(), Usage: &session.InvocationUsage{Raw: raw}}}))
				}
				submitBatch(t, s, b)
				loaded, err := session.Load(t.Context(), h, s.Snapshot().HistoryID(), p)
				mustBatch(t, err)
				for seam, s := range map[string]*session.Session{"Submit": s, "Load": loaded} {
					t.Run(seam, func(t *testing.T) {
						record := s.Snapshot().RecordAt(4)
						var provider session.OpaqueProviderDataView
						var present bool
						switch kind {
						case "failure":
							failure, _ := record.InvocationFailure()
							usage, ok := failure.Usage()
							if !ok {
								t.Fatal("missing failure usage")
							}
							provider, present = usage.Raw()
						case "response":
							response, _ := record.ModelResponse()
							call, callOK := response.Items().At(0).Call()
							content, contentOK := response.Items().At(1).Content()
							media, mediaOK := content.Parts().At(0).Media()
							inline := media.InlineData()
							if !callOK || !contentOK || !mediaOK || media.Source() != session.MediaSourceInline || inline.Len() != 0 || inline.Copy() == nil || call.Arguments().Len() != 0 || call.Arguments().Copy() == nil {
								t.Fatal("present-empty generated collections/byte leaves collapsed")
							}
							inline.All()(func(int, byte) bool { t.Fatal("empty inline bytes yielded"); return true })
							usage, ok := response.Usage()
							if !ok {
								t.Fatal("missing response usage")
							}
							provider, present = usage.Raw()
							private, privatePresent := response.Private()
							if !privatePresent || private.Producer() != "private" || private.Format() != "envelope" || private.Version() != 4 || len(private.Data().Copy()) != 0 || !bytes.Equal(private.Data().Copy(), input) {
								t.Fatal("empty private presence or fields")
							}
						case "capture":
							ending, _ := record.RunEnded()
							capture, ok := ending.Capture()
							if !ok || capture.RequestID() != id {
								t.Fatal("missing capture")
							}
							usage, ok := capture.Usage()
							if !ok {
								t.Fatal("missing capture usage")
							}
							provider, present = usage.Raw()
						}
						if !present || provider.Producer() != "producer" || provider.Format() != "format" || provider.Version() != 3 || len(provider.Data().Copy()) != 0 || !bytes.Equal(provider.Data().Copy(), input) {
							t.Fatal("empty raw presence or fields")
						}
					})
				}
			})
		}
	}
}

func TestModelInteractionZeroViews(t *testing.T) {
	var request session.ModelRequestView
	var response session.ModelResponseView
	var failure session.InvocationFailureView
	var ending session.RunEndedView
	if request.ID() != (session.ModelRequestID{}) || request.Source() != (session.SourceHeadView{}) || request.Recipe().Algorithm() != "" || response.Items().Len() != 0 || response.CallCount() != 0 || response.RequestID() != (session.ModelRequestID{}) || failure.RequestID() != (session.ModelRequestID{}) || ending.RunID() != (session.RunID{}) || ending.Reason() != "" {
		t.Fatal("zero view scalars")
	}
	if _, ok := request.RetryOf(); ok {
		t.Fatal("zero retry")
	}
	if _, ok := response.Private(); ok {
		t.Fatal("zero private")
	}
	if _, ok := response.Usage(); ok {
		t.Fatal("zero usage")
	}
	if _, ok := failure.Evidence(); ok {
		t.Fatal("zero evidence")
	}
	if _, ok := ending.Capture(); ok {
		t.Fatal("zero capture")
	}
	if len(collectView(response.Items())) != 0 || len(slices.Collect(response.Calls())) != 0 {
		t.Fatal("zero sequences")
	}
}

func TestModelInteractionAcceptedViewsAndReplay(t *testing.T) {
	h, s, p := newIteratorSession(t)
	scope := s.Snapshot().HistoryID()
	b := beginBatch(t, s)
	mustBatch(t, b.Create())
	// All reusable context is legal in create/idle batches, not just active runs.
	mustBatch(t, b.ReplaceInstructions(scope, session.Instructions{}))
	mustBatch(t, b.ReplaceToolCatalog(scope, session.ToolCatalog{}))
	mustBatch(t, b.ReplaceModelBinding(scope, session.ModelBinding{Route: "route", API: "api", Model: "model", Settings: []byte("{}")}))
	submitBatch(t, s, b)
	b = beginBatch(t, s)
	run := batchRun(t, b)
	_, err := b.AddUserInput(run, session.UserInput{})
	mustBatch(t, err)
	submitBatch(t, s, b)
	params := []byte{0, 0xff}
	first := addInteractionRequest(t, s, run, nil, params)
	pinnedRequest := s.Snapshot()
	requestSequence := pinnedRequest.Records()
	rv, ok := pinnedRequest.RecordAt(6).ModelRequest()
	if !ok || rv.ID() != first || !bytes.Equal(rv.Recipe().Parameters().Copy(), params) {
		t.Fatal("request view")
	}
	params[0] = 99
	clear(rv.Recipe().Parameters().Copy())
	clear(rv.Recipe().Check().Copy())
	b = beginBatch(t, s)
	failure := session.InvocationFailure{Class: session.InvocationFailureTransport, Explanation: new(""), Evidence: &session.ObservedEvidence{Correlation: new("safe observed correlation")}, Usage: &session.InvocationUsage{Input: new(int64(0)), Raw: &session.OpaqueProviderData{Producer: "adapter", Format: "usage", Version: 8, Data: []byte{0, 0xff}}}}
	mustBatch(t, b.AddInvocationFailure(first, failure))
	*failure.Usage.Input = 9
	clear(failure.Usage.Raw.Data)
	submitBatch(t, s, b)
	fv, ok := s.Snapshot().RecordAt(7).InvocationFailure()
	if !ok || fv.RequestID() != first || fv.Class() != session.InvocationFailureTransport {
		t.Fatal("failure view")
	}
	if detail, present := fv.Explanation(); !present || detail != "" {
		t.Fatal("failure explanation presence")
	}
	usage, present := fv.Usage()
	raw, rawPresent := usage.Raw()
	if !present || !rawPresent {
		t.Fatal("failure usage presence")
	}
	if input, ok := usage.Input(); !ok || input != 0 {
		t.Fatal("failure input usage")
	}
	if _, ok := usage.Output(); ok || !bytes.Equal(raw.Data().Copy(), []byte{0, 0xff}) {
		t.Fatal("failure usage")
	}
	clear(raw.Data().Copy())
	second := addInteractionRequest(t, s, run, &first, []byte("changed recipe"))
	if second == first || second.RunID() != first.RunID() {
		t.Fatal("retry identity")
	}
	retry, _ := s.Snapshot().RecordAt(8).ModelRequest()
	if got, ok := retry.RetryOf(); !ok || got != first || string(retry.Recipe().Parameters().Copy()) != "changed recipe" {
		t.Fatal("retry correlation/recipe")
	}
	response := session.ModelResponse{Terminal: session.ResponseTerminalError, ProviderStopReason: new("unknown/provider/reason"),
		Items: []session.ResponseItem{
			{Content: &session.ResponseContent{Kind: session.ResponseContentReasoning, Parts: []session.ContentPart{{Text: new("")}, {Media: &session.Media{Kind: session.MediaKindImage, MIMEType: "image/example", Source: session.MediaSourceInline, InlineData: []byte{0xff, 0}}}}}},
			{Call: &session.RequestedCall{Name: "", Arguments: []byte("not JSON")}},
			{Content: &session.ResponseContent{Kind: session.ResponseContentRefusal}},
			{Call: &session.RequestedCall{Name: "unknown", Arguments: []byte{}}},
			{Content: &session.ResponseContent{Kind: session.ResponseContentOutput, Parts: []session.ContentPart{}}},
		}, Usage: &session.InvocationUsage{Output: new(int64(0))}, Private: &session.OpaqueProviderData{Producer: "future/provider", Format: "relations", Version: 7, Data: []byte{0, 0xff}},
	}
	b = beginBatch(t, s)
	mustBatch(t, b.AddModelResponse(second, response))
	response.Items[1].Call.Arguments[0] = 'X'
	clear(response.Private.Data)
	submitBatch(t, s, b)
	accepted := s.Snapshot()
	view, ok := accepted.RecordAt(9).ModelResponse()
	if !ok || view.RequestID() != second || view.Terminal() != session.ResponseTerminalError || view.Items().Len() != 5 || view.CallCount() != 2 {
		t.Fatal("complete response inspection")
	}
	if n := testing.AllocsPerRun(100, func() {
		r, ok := accepted.RecordAt(9).ModelResponse()
		if !ok || r.Items().Len() != 5 || r.CallCount() != 2 || r.CallAt(0).ID() != view.CallAt(0).ID() || r.CallAt(0).ID().RequestID() != second || r.CallAt(0).ID().RequestID().RunID().HistoryID() != scope || r.Items().At(0) != view.Items().At(0) {
			panic("direct response navigation")
		}
		c, ok := r.Items().At(0).Content()
		if !ok || c.Parts().Len() != 2 {
			panic("direct content navigation")
		}
		_, _ = c.Parts().At(0).Text()
	}); n != 0 {
		t.Fatalf("direct interaction navigation allocated: %g", n)
	}
	items := collectView(view.Items())
	calls := slices.Collect(view.Calls())
	if len(items) != 5 || len(calls) != 2 || calls[0].ID().RequestID() != second || calls[0].ID() == calls[1].ID() || calls[1].ID().RequestID() != second || calls[0].Name() != "" || string(calls[0].Arguments().Copy()) != "not JSON" {
		t.Fatal("canonical call ordinals or bytes")
	}
	if view.CallAt(1) != calls[1] {
		t.Fatal("positional call view")
	}
	content, ok := items[0].Content()
	if !ok || content.Parts().Len() != 2 || content.Kind() != session.ResponseContentReasoning {
		t.Fatal("content view")
	}
	parts := collectView(content.Parts())
	if text, present := parts[0].Text(); !present || text != "" {
		t.Fatal("empty text lost")
	}
	for _, i := range []int{2, 4} {
		c, ok := view.Items().At(i).Content()
		if !ok || c.Parts().Len() != 0 {
			t.Fatal("explicit empty content lost")
		}
	}
	clear(calls[0].Arguments().Copy())
	private, ok := view.Private()
	if !ok || !bytes.Equal(private.Data().Copy(), []byte{0, 0xff}) {
		t.Fatal("private evidence")
	}
	clear(private.Data().Copy())
	oldCalls := view.Calls()
	oldParts := content.Parts()
	b = beginBatch(t, s)
	if _, err := b.AddUserInput(run, session.UserInput{}); !errors.Is(err, session.ErrTransition) {
		t.Fatal("unresolved calls admitted input")
	}
	if err := b.EndRun(session.RunEndedCompleted); !errors.Is(err, session.ErrTransition) {
		t.Fatal("unresolved calls completed")
	}
	mustBatch(t, b.AddRunEnded(run, session.RunEnded{Reason: session.RunEndedLimit, BulkToolCloseout: true}))
	submitBatch(t, s, b)
	end, ok := s.Snapshot().RecordAt(10).RunEnded()
	if !ok || end.RunID() != run || end.Reason() != session.RunEndedLimit || !end.BulkToolCloseout() {
		t.Fatal("unified ending")
	}
	// Idle context is a chronological part of the preceding span, not run ownership.
	b = beginBatch(t, s)
	mustBatch(t, b.ReplaceInstructions(scope, session.Instructions{}))
	submitBatch(t, s, b)
	var wg sync.WaitGroup
	wg.Go(func() {
		for range 200 {
			if len(slices.Collect(oldCalls)) != 2 || len(collectView(oldParts)) != 2 || len(slices.Collect(requestSequence)) != 7 || string(view.CallAt(0).Arguments().Copy()) != "not JSON" || !bytes.Equal(rv.Recipe().Parameters().Copy(), []byte{0, 0xff}) {
				t.Error("pinned interaction changed")
			}
		}
	})
	b = beginBatch(t, s)
	next := batchRun(t, b)
	submitBatch(t, s, b)
	third := addInteractionRequest(t, s, next, nil, nil)
	if third == first || third.RunID() != next || third.RunID() == run {
		t.Fatal("successor did not reset request counter")
	}
	b = beginBatch(t, s)
	mustBatch(t, b.AddModelResponse(third, session.ModelResponse{Terminal: session.ResponseTerminalNormal, Private: &session.OpaqueProviderData{Producer: "p", Format: "f", Version: 1}}))
	mustBatch(t, b.EndRun(session.RunEndedCompleted))
	submitBatch(t, s, b)
	wg.Wait()
	if accepted.RecordCount() != 10 || accepted.ActiveRun() != run || accepted.RunAt(0).RecordCount() != 6 || s.Snapshot().RunAt(0).RecordCount() != 8 {
		t.Fatal("pinned bounds or chronological span")
	}
	loaded, err := session.Load(context.Background(), h, scope, p)
	mustBatch(t, err)
	if !reflect.DeepEqual(loaded.Snapshot(), s.Snapshot()) {
		t.Fatal("Submit and Load differ")
	}
	for _, access := range []func(){func() { view.Items().At(-1) }, func() { view.Items().At(view.Items().Len()) }, func() { view.CallAt(-1) }, func() { view.CallAt(2) }, func() { content.Parts().At(2) }} {
		func() {
			defer func() {
				if recover() == nil {
					t.Error("out-of-range did not panic")
				}
			}()
			access()
		}()
	}
}
