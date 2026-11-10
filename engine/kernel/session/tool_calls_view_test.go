package session

import (
	"bytes"
	"fmt"
	"sync"
	"testing"

	"github.com/stacklok/mecatl/engine/kernel/codec"
	historyid "github.com/stacklok/mecatl/engine/kernel/history/id"
	"github.com/stacklok/mecatl/engine/kernel/value"
)

// These fixtures cross typed Submit and the storage-backed Load seam, not just
// DTO construction. Payload siblings remain nonempty even for scalar benchmarks.
func toolCallViewSession(t testing.TB) *Session {
	t.Helper()
	limits := codec.Limits{MaxBytes: 8 << 20, MaxStringBytes: 1 << 20, MaxByteStringBytes: 1 << 20,
		MaxArrayElements: 16384, MaxMapPairs: 128, MaxDepth: 32, MaxNodes: 1 << 20, MaxCollectionElements: 1 << 20}
	id, err := historyid.ParseHistoryID("HST1111111111111111111111")
	interactionCheck(t, err)
	s, err := New(internalTestHistory(t, limits), id, Policy{PayloadLimits: limits, CommitLimits: limits,
		MaxRecordsPerCommit: 1024, SoftMaxRetainedRecords: 32768, MaxRetainedRecords: 32768,
		SoftMaxPayloadBytes: 1 << 30, MaxPayloadBytes: 1 << 30, SoftMaxDecodedWork: 1 << 40, MaxDecodedWork: 1 << 40})
	interactionCheck(t, err)
	interactionSubmit(t, s, func(b *SessionCommit) {
		interactionCheck(t, b.Create())
		_, err := b.StartRun()
		interactionCheck(t, err)
		interactionCheck(t, b.ReplaceModelBinding(id, ModelBinding{Route: "r", API: "a", Model: "m", Settings: []byte("{}")}))
	})
	return s
}

func toolCallViewResponse(t testing.TB, s *Session, count, field, sibling int) []RequestedCallID {
	t.Helper()
	var request ModelRequestID
	interactionSubmit(t, s, func(b *SessionCommit) {
		head := s.Snapshot().Head()
		var err error
		request, err = b.AddModelRequest(s.Snapshot().ActiveRun(), ModelRequest{
			Source: SourceHead{Sequence: head.Sequence, CID: head.CID}, Recipe: ConstructionRecipe{Algorithm: "a", Version: 1, Check: []byte{1}}})
		interactionCheck(t, err)
	})
	response := ModelResponse{Terminal: ResponseTerminalNormal}
	for i := range count {
		response.Items = append(response.Items,
			ResponseItem{Content: &ResponseContent{Kind: ResponseContentReasoning, Parts: []ContentPart{{Text: new(string(bytes.Repeat([]byte{'s'}, sibling)))}}}},
			ResponseItem{Call: &RequestedCall{Name: fmt.Sprintf("requested-%d", i), Arguments: bytes.Repeat([]byte{0xff}, field)}},
			ResponseItem{Content: &ResponseContent{Kind: ResponseContentOutput}})
	}
	interactionSubmit(t, s, func(b *SessionCommit) { interactionCheck(t, b.AddModelResponse(request, response)) })
	var calls []RequestedCallID
	view, _ := s.Snapshot().RecordAt(s.Snapshot().RecordCount() - 1).ModelResponse()
	for call := range view.Calls() {
		calls = append(calls, call.ID())
	}
	return calls
}

func toolCallViewIntent(sibling int) ToolInvocation {
	return ToolInvocation{Name: "effective", Arguments: value.Map{
		"nested":  value.Object(value.Map{"items": value.Array([]value.Value{value.Int64(42), value.Null()})}),
		"sibling": value.String(string(bytes.Repeat([]byte{'a'}, sibling))),
	}}
}

func toolCallViewReturned(field, sibling int) ToolDisposition {
	return ToolDisposition{Diagnostic: new("diagnostic"), Returned: &ToolReturned{Status: ToolReturnError, Output: ToolOutput{
		Capture: ToolCaptureComplete, Parts: []ToolOutputPart{
			{JSON: new(append(append([]byte(` {"x":"`), bytes.Repeat([]byte{'x'}, field)...), []byte(`"} `)...))},
			{Content: &ContentPart{Text: new(string(bytes.Repeat([]byte{'o'}, sibling)))}},
		},
	}}}
}

func assertToolCallSettlement(t testing.TB, call ToolCallView, category ToolSettlementCategory, explicit bool, run RunID) {
	t.Helper()
	settlement, ok := call.Settlement()
	if !ok || settlement.Category() != category {
		t.Fatalf("call %v settlement = %v, %t; want %v", call.ID(), settlement.Category(), ok, category)
	}
	disposition, dp := settlement.ExplicitDisposition()
	ending, ep := settlement.BulkEnding()
	raw, rp := call.Disposition()
	if dp != explicit || ep == explicit || rp != explicit || disposition != raw {
		t.Fatal("settlement did not retain exactly the actual provenance")
	}
	if explicit {
		if disposition.CallID() != call.ID() || ending != (RunEndedView{}) {
			t.Fatal("explicit provenance lost scope or acquired ending")
		}
	} else if ending.RunID() != run || !ending.BulkToolCloseout() || disposition != (ToolDispositionView{}) {
		t.Fatal("derived provenance fabricated a disposition or lost ending scope")
	}
}

func TestRunToolCallsAcrossResponses(t *testing.T) {
	for _, end := range []string{"active", "ordinary", "limit", "capture"} {
		t.Run(end, func(t *testing.T) {
			s := toolCallViewSession(t)
			run := s.Snapshot().ActiveRun()
			first := toolCallViewResponse(t, s, 4, 17, 31)
			interactionSubmit(t, s, func(b *SessionCommit) {
				for i := range 3 {
					intent := toolCallViewIntent(31)
					if i == 2 {
						intent.Prerequisites = []uint64{2, 1}
					}
					interactionCheck(t, b.AddToolInvocation(first[i], intent))
				}
			})
			interactionSubmit(t, s, func(b *SessionCommit) {
				for i, outcome := range []ToolDisposition{toolCallViewReturned(17, 31), toolOutcome("returned"), toolOutcome("indeterminate"), toolOutcome("not invoked")} {
					interactionCheck(t, b.AddToolDisposition(first[i], outcome))
				}
			})
			second := toolCallViewResponse(t, s, 3, 17, 31)
			if end == "active" || end == "ordinary" {
				interactionSubmit(t, s, func(b *SessionCommit) {
					interactionCheck(t, b.AddToolDisposition(second[0], toolOutcome("not invoked")))
					interactionCheck(t, b.AddToolInvocation(second[1], toolCallViewIntent(31)))
				})
			}
			if end != "active" {
				interactionSubmit(t, s, func(b *SessionCommit) {
					if end == "ordinary" {
						interactionCheck(t, b.AddToolDisposition(second[1], toolOutcome("indeterminate")))
						interactionCheck(t, b.AddToolDisposition(second[2], toolOutcome("not invoked")))
						interactionCheck(t, b.EndRun(RunEndedCompleted))
					} else {
						interactionCheck(t, b.AddToolDisposition(second[0], toolOutcome("not invoked")))
						interactionCheck(t, b.AddToolInvocation(second[1], toolCallViewIntent(31)))
						reason := RunEndedLimit
						if end == "capture" {
							reason = RunEndedCaptureFailed
						}
						interactionCheck(t, b.AddRunEnded(run, RunEnded{Reason: reason, BulkToolCloseout: true, Explanation: new("")}))
					}
				})
				interactionSubmit(t, s, func(b *SessionCommit) {
					_, err := b.StartRun()
					interactionCheck(t, err)
				})
				toolCallViewResponse(t, s, 1, 5, 7) // Must not enter the past run's collection.
			}
			loaded, err := Load(t.Context(), s.history, run.HistoryID(), s.policy)
			interactionCheck(t, err)
			for seam, inspected := range map[string]*Session{"Submit": s, "Load": loaded} {
				t.Run(seam, func(t *testing.T) {
					collection := inspected.Snapshot().RunAt(0).ToolCalls()
					ids := append(append([]RequestedCallID(nil), first...), second...)
					if collection.Len() != len(ids) {
						t.Fatalf("calls = %d, want %d", collection.Len(), len(ids))
					}
					for range 2 {
						for i, call := range collection.All() {
							found, ok := collection.ByID(ids[i])
							if !ok || found != call || collection.At(i) != call || call.ID() != ids[i] || call.RequestedCall().ID() != ids[i] {
								t.Fatal("ordering, full scoped identity or repeatability")
							}
							if call.RequestedCall().Name() != fmt.Sprintf("requested-%d", ids[i].RequestRelative()-1) || !bytes.Equal(call.RequestedCall().Arguments().Copy(), bytes.Repeat([]byte{0xff}, 17)) {
								t.Fatal("original requested data changed")
							}
							invocation, ip := call.Invocation()
							if ip != (i < 3 || i == 5) || (ip && (invocation.CallID() != call.ID() || invocation.Name() != "effective" || invocation.Arguments().Len() != 2)) {
								t.Fatal("raw invocation association")
							}
							if i == 2 {
								for j, prerequisite := range invocation.Prerequisites().All() {
									if prerequisite != first[1-j] {
										t.Fatal("prerequisite scope/order")
									}
								}
							}
							if i >= 5 && end == "active" {
								if settlement, ok := call.Settlement(); ok || settlement != (ToolSettlement{}) {
									t.Fatal("active unresolved call is settled")
								}
								continue
							}
							categories := []ToolSettlementCategory{ToolSettlementReturned, ToolSettlementReturned, ToolSettlementIndeterminate, ToolSettlementNotInvoked, ToolSettlementNotInvoked, ToolSettlementIndeterminate, ToolSettlementNotInvoked}
							assertToolCallSettlement(t, call, categories[i], i < 5 || end == "ordinary", run)
						}
					}
					visits := 0
					collection.All()(func(int, ToolCallView) bool { visits++; return false })
					if visits != 1 {
						t.Fatal("early stop ignored")
					}
					// Payload pointers are borrowed, not clones made by association.
					for record := range inspected.Snapshot().RunAt(0).Records() {
						if raw, ok := record.ToolInvocation(); ok {
							call, _ := collection.ByID(raw.CallID())
							got, _ := call.Invocation()
							if got != raw {
								t.Fatal("association cloned invocation")
							}
						}
						if raw, ok := record.RunEnded(); ok && raw.BulkToolCloseout() {
							settlement, _ := collection.At(5).Settlement()
							got, _ := settlement.BulkEnding()
							if got != raw {
								t.Fatal("settlement did not borrow actual bulk ending")
							}
						}
						if raw, ok := record.ToolDisposition(); ok {
							call, _ := collection.ByID(raw.CallID())
							got, _ := call.Disposition()
							if got != raw {
								t.Fatal("association cloned disposition")
							}
						}
						if response, ok := record.ModelResponse(); ok {
							for raw := range response.Calls() {
								call, _ := collection.ByID(raw.ID())
								if call.RequestedCall() != raw {
									t.Fatal("association cloned requested payload")
								}
							}
						}
					}
				})
			}
		})
	}
}

func TestToolCallsZeroAndScopedLookup(t *testing.T) {
	var zero ToolCallsView
	if zero.Len() != 0 || (RunView{}).ToolCalls().Len() != 0 {
		t.Fatal("zero collection not empty")
	}
	zero.All()(func(int, ToolCallView) bool { t.Fatal("zero yielded"); return true })
	s := toolCallViewSession(t)
	ids := toolCallViewResponse(t, s, 2, 3, 5)
	collection := s.Snapshot().RunAt(0).ToolCalls()
	foreign, err := historyid.ParseHistoryID("HST2222222222222222222222")
	interactionCheck(t, err)
	for _, edit := range []func(*RequestedCallID){
		func(id *RequestedCallID) { *id = RequestedCallID{} },
		func(id *RequestedCallID) { id.requestID.runID.historyID = foreign },
		func(id *RequestedCallID) { id.requestID.runID.ordinal++ },
		func(id *RequestedCallID) { id.requestID.requestOrdinal++ },
		func(id *RequestedCallID) { id.ordinal = 3 },
		func(id *RequestedCallID) { id.ordinal = 0 },
	} {
		id := ids[0]
		edit(&id)
		for _, calls := range []ToolCallsView{zero, collection} {
			if call, ok := calls.ByID(id); ok || call != (ToolCallView{}) {
				t.Fatal("invalid/foreign/absent identity matched")
			}
		}
	}
	var call ToolCallView
	if call.ID() != (RequestedCallID{}) || call.RequestedCall() != (RequestedCallView{}) {
		t.Fatal("zero call has requested data")
	}
	if v, ok := call.Invocation(); ok || v != (ToolInvocationView{}) {
		t.Fatal("zero invocation")
	}
	if v, ok := call.Disposition(); ok || v != (ToolDispositionView{}) {
		t.Fatal("zero disposition")
	}
	settlement, ok := call.Settlement()
	if ok || settlement.Category() != 0 {
		t.Fatal("zero settlement")
	}
	if v, ok := settlement.ExplicitDisposition(); ok || v != (ToolDispositionView{}) {
		t.Fatal("zero explicit provenance")
	}
	if v, ok := settlement.BulkEnding(); ok || v != (RunEndedView{}) {
		t.Fatal("zero bulk provenance")
	}
}

func TestToolCallsPinnedConcurrentReaders(t *testing.T) {
	s := toolCallViewSession(t)
	ids := toolCallViewResponse(t, s, 3, 17, 31)
	run := s.Snapshot().RunAt(0)
	uninvoked := run.ToolCalls()
	oldCall := uninvoked.At(1)
	interactionSubmit(t, s, func(b *SessionCommit) {
		interactionCheck(t, b.AddToolInvocation(ids[1], toolCallViewIntent(31)))
	})
	invoked := s.Snapshot().RunAt(0).ToolCalls()
	var readers sync.WaitGroup
	for range 4 {
		readers.Go(func() {
			for range 100 {
				for _, collection := range []ToolCallsView{uninvoked, invoked, run.ToolCalls()} {
					for _, call := range collection.All() {
						if _, ok := call.Settlement(); ok {
							t.Error("pinned call changed settlement")
						}
					}
				}
				if _, ok := oldCall.Invocation(); ok {
					t.Error("old call acquired invocation")
				}
			}
		})
	}
	interactionSubmit(t, s, func(b *SessionCommit) {
		interactionCheck(t, b.AddToolDisposition(ids[0], toolOutcome("not invoked")))
		interactionCheck(t, b.AddRunEnded(run.ID(), RunEnded{Reason: RunEndedLimit, BulkToolCloseout: true}))
	})
	ended := s.Snapshot().RunAt(0).ToolCalls()
	interactionSubmit(t, s, func(b *SessionCommit) {
		_, err := b.StartRun()
		interactionCheck(t, err)
	})
	toolCallViewResponse(t, s, 1, 5, 7)
	readers.Wait()
	if _, ok := invoked.At(1).Invocation(); !ok {
		t.Fatal("pinned invocation lost")
	}
	for _, collection := range []ToolCallsView{ended, s.Snapshot().RunAt(0).ToolCalls()} {
		assertToolCallSettlement(t, collection.At(0), ToolSettlementNotInvoked, true, run.ID())
		assertToolCallSettlement(t, collection.At(1), ToolSettlementIndeterminate, false, run.ID())
		assertToolCallSettlement(t, collection.At(2), ToolSettlementNotInvoked, false, run.ID())
	}
}

func TestToolCallsSubmitLoadRetainsRichFacts(t *testing.T) {
	s := toolCallViewSession(t)
	ids := toolCallViewResponse(t, s, 3, 17, 31)
	invocation := ToolInvocation{Name: "effective", Arguments: value.Map{
		"nested":  value.Object(value.Map{"items": value.Array([]value.Value{value.Int64(42), value.Null()})}),
		"sibling": value.String("retained sibling"),
	}}
	invocationWant := invocation.Clone()
	b, err := s.BeginCommit()
	interactionCheck(t, err)
	interactionCheck(t, b.AddToolInvocation(ids[0], invocation))
	interactionCheck(t, b.AddToolInvocation(ids[2], ToolInvocation{Name: "uncertain", Arguments: value.Map{"present-empty": value.String("")}}))
	invocation.Name = "mutated"
	invocation.Arguments["sibling"] = value.String("mutated")
	_, err = s.SubmitCommit(t.Context(), b)
	interactionCheck(t, err)

	returned := ToolDisposition{Diagnostic: new(""), Returned: &ToolReturned{Status: ToolReturnError, Output: toolFactOutput()}}
	indeterminateOutput := toolFactOutput()
	indeterminateOutput.Capture = ToolCaptureTruncated
	indeterminate := ToolDisposition{Diagnostic: new("uncertain diagnostic"), Indeterminate: &ToolIndeterminate{Reason: ToolIndeterminateExecutorLost, Output: indeterminateOutput}}
	returnedWant, indeterminateWant := returned.Clone(), indeterminate.Clone()
	b, err = s.BeginCommit()
	interactionCheck(t, err)
	interactionCheck(t, b.AddToolDisposition(ids[0], returned))
	interactionCheck(t, b.AddToolDisposition(ids[1], ToolDisposition{NotInvoked: &ToolNotInvoked{Reason: ToolNotInvokedPreparationFailed}}))
	interactionCheck(t, b.AddToolDisposition(ids[2], indeterminate))
	*returned.Diagnostic = "mutated"
	*returned.Returned.Output.Parts[0].Content.Text = "mutated"
	(*returned.Returned.Output.Parts[2].JSON)[0] = 'x'
	returned.Returned.Output.Parts[3].ResourceLink.URI = "urn:mutated"
	(*returned.Returned.Output.Parts[5].Resource.Blob)[0] = 0
	*indeterminate.Diagnostic = "mutated"
	*indeterminate.Indeterminate.Output.Parts[4].Resource.Text = "mutated"
	_, err = s.SubmitCommit(t.Context(), b)
	interactionCheck(t, err)

	loaded, err := Load(t.Context(), s.history, s.Snapshot().HistoryID(), s.policy)
	interactionCheck(t, err)
	for seam, inspected := range map[string]*Session{"Submit": s, "Load": loaded} {
		t.Run(seam, func(t *testing.T) {
			calls := inspected.Snapshot().RunAt(0).ToolCalls()
			if calls.Len() != 3 || calls.At(0).ID() != ids[0] || calls.At(1).ID() != ids[1] || calls.At(2).ID() != ids[2] {
				t.Fatal("requested-call identities changed")
			}
			invocationView, ok := calls.At(0).Invocation()
			if !ok || invocationView.Name() != invocationWant.Name || invocationView.CallID() != ids[0] || invocationView.Arguments().Len() != 2 {
				t.Fatal("invocation identity or fields changed")
			}
			nested, ok := invocationView.Arguments().Get("nested")
			object, objectOK := nested.Object()
			items, itemsOK := object.Get("items")
			array, arrayOK := items.Array()
			answer, answerOK := array.At(0).Int64()
			sibling, siblingOK := invocationView.Arguments().Get("sibling")
			siblingText, siblingTextOK := sibling.String()
			if !siblingOK || !siblingTextOK || siblingText != "retained sibling" {
				t.Fatal("sibling effective argument changed")
			}
			uncertainInvocation, uncertainInvocationOK := calls.At(2).Invocation()
			presentEmpty, presentEmptyOK := uncertainInvocation.Arguments().Get("present-empty")
			presentEmptyText, presentEmptyTextOK := presentEmpty.String()
			if !uncertainInvocationOK || uncertainInvocation.Name() != "uncertain" || uncertainInvocation.Arguments().Len() != 1 || !presentEmptyOK || !presentEmptyTextOK || presentEmptyText != "" {
				t.Fatal("present-empty effective argument lost")
			}
			if !ok || !objectOK || !itemsOK || !arrayOK || array.Len() != 2 || !answerOK || answer != 42 || array.At(1).Kind() != value.NullKind {
				t.Fatal("structured arguments changed")
			}
			returnedView, ok := calls.At(0).Disposition()
			if !ok || returnedView.CallID() != ids[0] {
				t.Fatal("returned disposition identity")
			}
			diagnostic, present := returnedView.Diagnostic()
			if !present || diagnostic != "" {
				t.Fatal("present-empty diagnostic lost")
			}
			returnedBranch, ok := returnedView.Returned()
			if !ok || returnedBranch.Status() != ToolReturnError {
				t.Fatal("returned branch changed")
			}
			checkToolFactOutputView(t, returnedBranch.Output(), returnedWant.Returned.Output)
			json, _ := returnedBranch.Output().Parts().At(2).JSON()
			blob, _ := returnedBranch.Output().Parts().At(5).Resource()
			blobBytes, _ := blob.Blob()
			jsonCopy := json.Copy()
			jsonCopy[0] = 'x'
			if bytes.Equal(jsonCopy, json.Copy()) || !bytes.Equal(blobBytes.Copy(), *returnedWant.Returned.Output.Parts[5].Resource.Blob) {
				t.Fatal("selected JSON Copy changed a sibling or backing")
			}
			notInvoked, ok := calls.At(1).Disposition()
			branch, branchOK := notInvoked.NotInvoked()
			if !ok || !branchOK || branch.Reason() != ToolNotInvokedPreparationFailed {
				t.Fatal("not-invoked branch changed")
			}
			if _, present := notInvoked.Diagnostic(); present {
				t.Fatal("omitted diagnostic became present")
			}
			indeterminateView, ok := calls.At(2).Disposition()
			indeterminateDiagnostic, indeterminateDiagnosticPresent := indeterminateView.Diagnostic()
			indeterminateBranch, branchOK := indeterminateView.Indeterminate()
			if !ok || !branchOK || indeterminateBranch.Reason() != ToolIndeterminateExecutorLost || !indeterminateDiagnosticPresent || indeterminateDiagnostic != "uncertain diagnostic" {
				t.Fatal("indeterminate branch changed")
			}
			checkToolFactOutputView(t, indeterminateBranch.Output(), indeterminateWant.Indeterminate.Output)
		})
	}
}
