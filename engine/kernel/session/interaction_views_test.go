package session_test

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"slices"
	"sync"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/history/mem"
	"github.com/stacklok/mecatl/engine/kernel/codec"
	"github.com/stacklok/mecatl/engine/kernel/history"
	"github.com/stacklok/mecatl/engine/kernel/session"
)

func addInteractionRequest(t testing.TB, s *session.Session, run session.RunID, retry *session.ModelRequestID, parameters []byte) session.ModelRequestID {
	t.Helper()
	head := s.Snapshot().Head()
	b := beginBatch(t, s)
	id, err := b.AddModelRequest(session.AddModelRequestRequest{RunID: run, Source: session.SourceHead{Sequence: head.Sequence, CID: head.CID}, Recipe: session.ConstructionRecipe{Algorithm: "future/algorithm", Version: 17, Parameters: parameters, Check: []byte{0xff, 0, 1}}, RetryOf: retry})
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
			mustBatch(t, b.ReplaceModelBinding(session.ModelBinding{HistoryID: run.HistoryID, Route: "r", API: "a", Model: "m", Settings: []byte("{}")}))
			submitBatch(t, s, b)
			head := s.Snapshot().Head()
			b = beginBatch(t, s)
			add := session.AddModelRequestRequest{RunID: run, Source: session.SourceHead{Sequence: head.Sequence, CID: head.CID}, Recipe: session.ConstructionRecipe{Algorithm: "a", Version: 1, Parameters: []byte("original"), Check: []byte{1}}}
			stale := add
			stale.Source.Sequence++
			if id, err := b.AddModelRequest(stale); id != (session.ModelRequestID{}) || !errors.Is(err, session.ErrIdentity) {
				t.Fatalf("stale request: %v", err)
			}
			id, err := b.AddModelRequest(add)
			mustBatch(t, err)
			if id.Ordinal != 1 {
				t.Fatal("failed addition consumed identity")
			}
			if outcome {
				submitBatch(t, s, b)
				b = beginBatch(t, s)
				mustBatch(t, b.AddModelResponse(session.ModelResponse{RequestID: id, Terminal: session.ResponseTerminalNormal}))
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
			if err := b.AddInvocationFailure(session.InvocationFailure{RequestID: id, Class: session.InvocationFailureTimeout}); !errors.Is(err, session.ErrBatch) {
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
				if !ok || v.ID() != id || string(v.Recipe().Parameters) != "original" {
					t.Fatal("frozen request changed")
				}
			}
		}
	}
}

func TestModelInteractionZeroViews(t *testing.T) {
	var request session.ModelRequestView
	var response session.ModelResponseView
	var failure session.InvocationFailureView
	var ending session.RunEndedView
	if request.ID() != (session.ModelRequestID{}) || request.Source() != (session.SourceHead{}) || request.Recipe().Algorithm != "" || response.ItemCount() != 0 || response.CallCount() != 0 || response.RequestID() != (session.ModelRequestID{}) || failure.RequestID() != (session.ModelRequestID{}) || ending.RunID() != (session.RunID{}) || ending.Reason() != 0 {
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
	if len(slices.Collect(response.Items())) != 0 || len(slices.Collect(response.Calls())) != 0 {
		t.Fatal("zero sequences")
	}
}

func TestModelInteractionAcceptedViewsAndReplay(t *testing.T) {
	h, s, p := newIteratorSession(t)
	scope := s.Snapshot().HistoryID()
	b := beginBatch(t, s)
	mustBatch(t, b.Create())
	// All reusable context is legal in create/idle batches, not just active runs.
	mustBatch(t, b.ReplaceInstructions(session.Instructions{HistoryID: scope}))
	mustBatch(t, b.ReplaceToolCatalog(session.ToolCatalog{HistoryID: scope}))
	mustBatch(t, b.ReplaceModelBinding(session.ModelBinding{HistoryID: scope, Route: "route", API: "api", Model: "model", Settings: []byte("{}")}))
	submitBatch(t, s, b)
	b = beginBatch(t, s)
	run := batchRun(t, b)
	_, err := b.AddUserInput(session.AddUserInputRequest{RunID: run})
	mustBatch(t, err)
	submitBatch(t, s, b)
	params := []byte{0, 0xff}
	first := addInteractionRequest(t, s, run, nil, params)
	pinnedRequest := s.Snapshot()
	requestSequence := pinnedRequest.Records()
	rv, ok := pinnedRequest.RecordAt(6).ModelRequest()
	if !ok || rv.ID() != first || !bytes.Equal(rv.Recipe().Parameters, params) {
		t.Fatal("request view")
	}
	params[0] = 99
	clear(rv.Recipe().Parameters)
	clear(rv.Recipe().Check)
	b = beginBatch(t, s)
	failure := session.InvocationFailure{RequestID: first, Class: session.InvocationFailureTransport, Explanation: new(""), Evidence: &session.ObservedEvidence{Correlation: new("safe observed correlation")}, Usage: &session.InvocationUsage{Input: new(int64(0)), Raw: &session.OpaqueProviderData{Producer: "adapter", Format: "usage", Version: 8, Data: []byte{0, 0xff}}}}
	mustBatch(t, b.AddInvocationFailure(failure))
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
	if !present || usage.Input == nil || *usage.Input != 0 || usage.Output != nil || !bytes.Equal(usage.Raw.Data, []byte{0, 0xff}) {
		t.Fatal("failure usage")
	}
	*usage.Input = 42
	clear(usage.Raw.Data)
	second := addInteractionRequest(t, s, run, &first, []byte("changed recipe"))
	if second.Ordinal != 2 || second.RunID != first.RunID {
		t.Fatal("retry identity")
	}
	retry, _ := s.Snapshot().RecordAt(8).ModelRequest()
	if got, ok := retry.RetryOf(); !ok || got != first || string(retry.Recipe().Parameters) != "changed recipe" {
		t.Fatal("retry correlation/recipe")
	}
	response := session.ModelResponse{RequestID: second, Terminal: session.ResponseTerminalError, ProviderReason: new("unknown/provider/reason"),
		Items: []session.ResponseItem{
			{Content: &session.ResponseContent{Kind: session.ResponseContentReasoning, Parts: []session.ContentPart{{Text: new("")}, {Media: &session.Media{Kind: session.MediaKindImage, MIMEType: "image/example", Source: session.MediaSourceInline, InlineData: []byte{0xff, 0}}}}}},
			{Call: &session.RequestedCall{Name: "", Arguments: []byte("not JSON")}},
			{Content: &session.ResponseContent{Kind: session.ResponseContentRefusal}},
			{Call: &session.RequestedCall{Name: "unknown", Arguments: []byte{}}},
			{Content: &session.ResponseContent{Kind: session.ResponseContentOutput, Parts: []session.ContentPart{}}},
		}, Usage: &session.InvocationUsage{Output: new(int64(0))}, Private: &session.OpaqueProviderData{Producer: "future/provider", Format: "relations", Version: 7, Data: []byte{0, 0xff}},
	}
	b = beginBatch(t, s)
	mustBatch(t, b.AddModelResponse(response))
	response.Items[1].Call.Arguments[0] = 'X'
	clear(response.Private.Data)
	submitBatch(t, s, b)
	accepted := s.Snapshot()
	view, ok := accepted.RecordAt(9).ModelResponse()
	if !ok || view.RequestID() != second || view.Terminal() != session.ResponseTerminalError || view.ItemCount() != 5 || view.CallCount() != 2 {
		t.Fatal("complete response inspection")
	}
	if n := testing.AllocsPerRun(100, func() {
		r, ok := accepted.RecordAt(9).ModelResponse()
		if !ok || r.ItemCount() != 5 || r.CallCount() != 2 || r.CallAt(0).ID().Ordinal != 1 || r.ItemAt(0) != view.ItemAt(0) {
			panic("direct response navigation")
		}
		c, ok := r.ItemAt(0).Content()
		if !ok || c.PartCount() != 2 {
			panic("direct content navigation")
		}
		_, _ = c.PartAt(0).Text()
	}); n != 0 {
		t.Fatalf("direct interaction navigation allocated: %g", n)
	}
	items := slices.Collect(view.Items())
	calls := slices.Collect(view.Calls())
	if len(items) != 5 || len(calls) != 2 || calls[0].ID().RequestID != second || calls[0].ID().Ordinal != 1 || calls[1].ID().Ordinal != 2 || calls[0].Name() != "" || string(calls[0].Arguments()) != "not JSON" {
		t.Fatal("canonical call ordinals or bytes")
	}
	if view.CallAt(1) != calls[1] {
		t.Fatal("positional call view")
	}
	content, ok := items[0].Content()
	if !ok || content.PartCount() != 2 || content.Kind() != session.ResponseContentReasoning {
		t.Fatal("content view")
	}
	parts := slices.Collect(content.Parts())
	if text, present := parts[0].Text(); !present || text != "" {
		t.Fatal("empty text lost")
	}
	for _, i := range []int{2, 4} {
		c, ok := view.ItemAt(i).Content()
		if !ok || c.PartCount() != 0 {
			t.Fatal("explicit empty content lost")
		}
	}
	clear(calls[0].Arguments())
	private, ok := view.Private()
	if !ok || !bytes.Equal(private.Data, []byte{0, 0xff}) {
		t.Fatal("private evidence")
	}
	clear(private.Data)
	oldCalls := view.Calls()
	oldParts := content.Parts()
	b = beginBatch(t, s)
	if _, err := b.AddUserInput(session.AddUserInputRequest{RunID: run}); !errors.Is(err, session.ErrTransition) {
		t.Fatal("unresolved calls admitted input")
	}
	if err := b.EndRun(session.RunEndedCompleted); !errors.Is(err, session.ErrTransition) {
		t.Fatal("unresolved calls completed")
	}
	mustBatch(t, b.EndRun(session.RunEndedInterrupted))
	submitBatch(t, s, b)
	end, ok := s.Snapshot().RecordAt(10).RunEnded()
	if !ok || end.RunID() != run || end.Reason() != session.RunEndedInterrupted {
		t.Fatal("unified ending")
	}
	// Idle context is a chronological part of the preceding span, not run ownership.
	b = beginBatch(t, s)
	mustBatch(t, b.ReplaceInstructions(session.Instructions{HistoryID: scope}))
	submitBatch(t, s, b)
	var wg sync.WaitGroup
	wg.Go(func() {
		for range 200 {
			if len(slices.Collect(oldCalls)) != 2 || len(slices.Collect(oldParts)) != 2 || len(slices.Collect(requestSequence)) != 7 || string(view.CallAt(0).Arguments()) != "not JSON" || !bytes.Equal(rv.Recipe().Parameters, []byte{0, 0xff}) {
				t.Error("pinned interaction changed")
			}
		}
	})
	b = beginBatch(t, s)
	next := batchRun(t, b)
	submitBatch(t, s, b)
	third := addInteractionRequest(t, s, next, nil, nil)
	if third.Ordinal != 1 || third.RunID == run {
		t.Fatal("successor did not reset request counter")
	}
	b = beginBatch(t, s)
	mustBatch(t, b.AddModelResponse(session.ModelResponse{RequestID: third, Terminal: session.ResponseTerminalNormal, Private: &session.OpaqueProviderData{Producer: "p", Format: "f", Version: 1}}))
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
	applied := boundSession(t, h, p)
	head := s.Snapshot().Head()
	page, err := h.Read(context.Background(), scope, history.ReadRequest{Through: head, MaxCommits: 1})
	mustBatch(t, err)
	// Read policies may cap pages, so continue through the accepted endpoint.
	for {
		for _, c := range page.Commits {
			mustBatch(t, applied.Apply(c))
		}
		if page.Next == head {
			break
		}
		request := history.ReadRequest{Through: head, MaxCommits: 1}
		request.After = page.Next
		page, err = h.Read(context.Background(), scope, request)
		mustBatch(t, err)
	}
	if !reflect.DeepEqual(applied.Snapshot(), s.Snapshot()) {
		t.Fatal("Apply and Submit differ")
	}
	for _, access := range []func(){func() { view.ItemAt(-1) }, func() { view.ItemAt(view.ItemCount()) }, func() { view.CallAt(-1) }, func() { view.CallAt(2) }, func() { content.PartAt(2) }} {
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
