package session_test

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/history/mem"
	"github.com/stacklok/mecatl/engine/kernel/codec"
	"github.com/stacklok/mecatl/engine/kernel/history"
	historyid "github.com/stacklok/mecatl/engine/kernel/history/id"
	"github.com/stacklok/mecatl/engine/kernel/session"
	sessionformat "github.com/stacklok/mecatl/engine/kernel/session/format"
)

func canonicalSession(t *testing.T) (*session.Session, *history.History, session.Policy) {
	t.Helper()
	limits := codec.Limits{MaxBytes: 16384, MaxStringBytes: 128, MaxByteStringBytes: 1024, MaxArrayElements: 32, MaxMapPairs: 32, MaxDepth: 16, MaxNodes: 2048, MaxCollectionElements: 512}
	p := session.Policy{CommitLimits: limits, PayloadLimits: limits, MaxRecordsPerCommit: 32, SoftMaxRetainedRecords: 128, SoftMaxPayloadBytes: 1 << 20, SoftMaxDecodedWork: 1 << 30, MaxRetainedRecords: 128, MaxPayloadBytes: 1 << 20, MaxDecodedWork: 1 << 30}
	h, err := history.New(mem.New(), history.Policy{CommitLimits: limits, RetryWindow: 16, MaxPageBytes: limits.MaxBytes, MaxPageCommits: 2})
	if err != nil {
		t.Fatal(err)
	}
	id, err := historyid.ParseHistoryID("HST1111111111111111111111")
	if err != nil {
		t.Fatal(err)
	}
	s, err := session.New(h, id, p)
	if err != nil {
		t.Fatal(err)
	}
	return s, h, p
}

func canonicalBegin(t *testing.T, s *session.Session) *session.SessionCommit {
	t.Helper()
	b, err := s.BeginCommit()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = b.Discard() })
	return b
}

func canonicalOK(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func canonicalSubmit(t *testing.T, s *session.Session, b *session.SessionCommit) {
	t.Helper()
	ref, err := s.SubmitCommit(context.Background(), b)
	if err != nil || ref == (history.CommitRef{}) {
		t.Fatalf("submit: %v, %v", ref, err)
	}
}

func canonicalRequest(s *session.Session) sessionformat.ModelRequest {
	head := s.Snapshot().Head()
	return sessionformat.ModelRequest{Source: sessionformat.SourceHead{Sequence: head.Sequence, CID: head.CID}, Recipe: sessionformat.ConstructionRecipe{Algorithm: "fixture", Version: 1, Check: []byte{1}}}
}

func TestCanonicalPayloadOwnershipAndStoredBytes(t *testing.T) {
	s, h, p := canonicalSession(t)
	b := canonicalBegin(t, s)
	canonicalOK(t, b.Create())
	run, err := b.StartRun()
	canonicalOK(t, err)
	scope := run.HistoryID()
	parts := []sessionformat.ContentPart{{Text: new("original")}}
	authored := sessionformat.UserInput{Parts: parts}
	input, err := b.AddUserInput(run, authored)
	canonicalOK(t, err)
	instructions := sessionformat.Instructions{Blocks: []sessionformat.InstructionBlock{{Parts: parts, Source: sessionformat.ContentSource{Description: "source", WorkspaceFile: &sessionformat.WorkspaceFileSource{Path: "AGENTS.md"}}}}}
	catalog := sessionformat.ToolCatalog{Tools: []sessionformat.ToolDefinition{{Name: "tool", ArgumentSchema: []byte("true")}}}
	binding := sessionformat.ModelBinding{Route: "r", API: "a", Model: "m", Settings: []byte("{}")}
	effective := sessionformat.EffectiveInput{Inputs: []sessionformat.InputItem{{UserInputID: new(input.RunRelative())}, {Inline: &sessionformat.AttributedInput{Actor: "open/future-actor", Purpose: sessionformat.PromptContext, Parts: parts, Source: sessionformat.ContentSource{Description: "inline"}}}}}
	canonicalOK(t, b.ReplaceInstructions(scope, instructions))
	canonicalOK(t, b.ReplaceToolCatalog(scope, catalog))
	canonicalOK(t, b.ReplaceModelBinding(scope, binding))
	canonicalOK(t, b.AddEffectiveInput(run, effective))
	// Aliases are assignments, not conversions or a second mutable representation.
	if reflect.TypeFor[session.UserInput]() != reflect.TypeFor[sessionformat.UserInput]() {
		t.Fatal("session payload is not a canonical alias")
	}
	if authored.RunID != 0 || authored.ID != 0 || effective.RunID != 0 {
		t.Fatal("add mutated authoring context")
	}
	wantInput := authored.Clone()
	wantInput.RunID, wantInput.ID = run.HistoryRelative(), input.RunRelative()
	wantEffective := effective.Clone()
	wantEffective.RunID = run.HistoryRelative()
	want := []codec.Marshaler{sessionformat.Created{}, sessionformat.RunStarted{ID: run.HistoryRelative()}, wantInput, instructions.Clone(), catalog.Clone(), binding.Clone(), wantEffective}
	*parts[0].Text = "changed"
	instructions.Blocks[0].Source.WorkspaceFile.Path = "changed"
	catalog.Tools[0].ArgumentSchema[0] = 'X'
	binding.Settings[0] = 'X'
	*effective.Inputs[0].UserInputID = 99
	canonicalSubmit(t, s, b)
	loaded, err := session.Load(context.Background(), h, scope, p)
	canonicalOK(t, err)
	for _, snapshot := range []session.Snapshot{s.Snapshot(), loaded.Snapshot()} {
		created, ok := snapshot.RecordAt(0).Created()
		if !ok || created.HistoryID() != scope {
			t.Fatal("creation identity lost")
		}
		started, ok := snapshot.RecordAt(1).RunStarted()
		if !ok || started.ID() != run {
			t.Fatal("start identity lost")
		}
		if v, ok := snapshot.RecordAt(0).RunStarted(); ok || v.ID() != (session.RunID{}) {
			t.Fatal("wrong-kind start present")
		}
		if v, ok := snapshot.RecordAt(1).Created(); ok || v.HistoryID() != (historyid.HistoryID{}) {
			t.Fatal("wrong-kind creation present")
		}
		for i, value := range want {
			wire, err := codec.Encode(value, p.PayloadLimits)
			canonicalOK(t, err)
			if !bytes.Equal(snapshot.RecordAt(i).Format().Payload, wire) {
				t.Fatalf("record %d canonical bytes changed or borrowed caller storage", i)
			}
		}
		got, ok := snapshot.UserInputByID(input)
		if !ok || got.ID() != input {
			t.Fatal("input identity lost")
		}
	}
	var zero session.RecordView
	if v, ok := zero.Created(); ok || v.HistoryID() != (historyid.HistoryID{}) {
		t.Fatal("zero creation present")
	}
	if v, ok := zero.RunStarted(); ok || v.ID() != (session.RunID{}) {
		t.Fatal("zero start present")
	}
	if (session.CreatedView{}).HistoryID() != (historyid.HistoryID{}) || (session.RunStartedView{}).ID() != (session.RunID{}) {
		t.Fatal("zero handles have identity")
	}
}

func TestCanonicalInteractionBytesAndOwnership(t *testing.T) {
	s, h, p := canonicalSession(t)
	b := canonicalBegin(t, s)
	canonicalOK(t, b.Create())
	run, err := b.StartRun()
	canonicalOK(t, err)
	canonicalOK(t, b.ReplaceModelBinding(run.HistoryID(), sessionformat.ModelBinding{Route: "r", API: "a", Model: "m", Settings: []byte("{}")}))
	canonicalSubmit(t, s, b)

	request := canonicalRequest(s)
	request.Recipe.Parameters = []byte{0xff, 0}
	request.Recipe.Check = []byte{1, 2}
	originalRequest := request.Clone()
	b = canonicalBegin(t, s)
	first, err := b.AddModelRequest(run, request)
	canonicalOK(t, err)
	if !request.Equal(originalRequest) {
		t.Fatal("request addition mutated caller value")
	}
	wantRequest := request.Clone()
	wantRequest.RunID, wantRequest.ID = run.HistoryRelative(), first.RunRelative()
	clear(request.Recipe.Parameters)
	clear(request.Recipe.Check)
	canonicalSubmit(t, s, b)

	failure := sessionformat.InvocationFailure{Class: sessionformat.InvocationFailureTimeout, Explanation: new("original failure"),
		Evidence: &sessionformat.ObservedEvidence{Detail: new(""), Correlation: new("failure correlation")},
		Usage:    &sessionformat.InvocationUsage{Input: new(int64(0)), Raw: &sessionformat.OpaqueProviderData{Producer: "p", Format: "usage", Version: 1, Data: []byte{0xff, 1}}}}
	originalFailure := failure.Clone()
	b = canonicalBegin(t, s)
	canonicalOK(t, b.AddInvocationFailure(first, failure))
	if !failure.Equal(originalFailure) {
		t.Fatal("failure addition mutated caller value")
	}
	wantFailure := failure.Clone()
	wantFailure.RunID, wantFailure.RequestID = run.HistoryRelative(), first.RunRelative()
	*failure.Explanation = "changed"
	*failure.Evidence.Detail = "changed"
	*failure.Evidence.Correlation = "changed"
	*failure.Usage.Input = 99
	clear(failure.Usage.Raw.Data)
	canonicalSubmit(t, s, b)

	retry := canonicalRequest(s)
	retry.RetryOf = new(first.RunRelative())
	retry.Recipe.Parameters = []byte{0xfe, 2}
	originalRetry := retry.Clone()
	b = canonicalBegin(t, s)
	second, err := b.AddModelRequest(run, retry)
	canonicalOK(t, err)
	if !retry.Equal(originalRetry) {
		t.Fatal("retry addition mutated caller value")
	}
	wantRetry := retry.Clone()
	wantRetry.RunID, wantRetry.ID = run.HistoryRelative(), second.RunRelative()
	*retry.RetryOf = 99
	clear(retry.Recipe.Parameters)
	clear(retry.Recipe.Check)
	canonicalSubmit(t, s, b)

	response := sessionformat.ModelResponse{Terminal: sessionformat.ResponseTerminalOther, ProviderStopReason: new(""),
		Items: []sessionformat.ResponseItem{
			{Content: &sessionformat.ResponseContent{Kind: sessionformat.ResponseContentOutput, Parts: []sessionformat.ContentPart{
				{Text: new("response text")}, {Media: &sessionformat.Media{Kind: sessionformat.MediaKindDocument, Source: sessionformat.MediaSourceInline, MIMEType: "application/octet-stream", InlineData: []byte{0xfd, 3}}}}}},
			{Call: &sessionformat.RequestedCall{Name: "tool", Arguments: []byte{0xff, 4}}}},
		Evidence: &sessionformat.ObservedEvidence{Detail: new("response detail"), Correlation: new("")},
		Usage:    &sessionformat.InvocationUsage{Output: new(int64(0)), Raw: &sessionformat.OpaqueProviderData{Producer: "p", Format: "usage", Version: 1, Data: []byte{0xfc, 5}}},
		Private:  &sessionformat.OpaqueProviderData{Producer: "p", Format: "private", Version: 2, Data: []byte{0xfb, 6}}}
	originalResponse := response.Clone()
	b = canonicalBegin(t, s)
	canonicalOK(t, b.AddModelResponse(second, response))
	if !response.Equal(originalResponse) {
		t.Fatal("response addition mutated caller value")
	}
	wantResponse := response.Clone()
	wantResponse.RunID, wantResponse.RequestID = run.HistoryRelative(), second.RunRelative()
	*response.ProviderStopReason = "changed"
	*response.Items[0].Content.Parts[0].Text = "changed"
	clear(response.Items[0].Content.Parts[1].Media.InlineData)
	clear(response.Items[1].Call.Arguments)
	*response.Evidence.Detail = "changed"
	*response.Evidence.Correlation = "changed"
	*response.Usage.Output = 99
	clear(response.Usage.Raw.Data)
	clear(response.Private.Data)
	canonicalOK(t, b.EndRun(session.RunEndedInterrupted))
	canonicalSubmit(t, s, b)

	loaded, err := session.Load(context.Background(), h, run.HistoryID(), p)
	canonicalOK(t, err)
	want := []struct {
		kind  string
		value codec.Marshaler
	}{
		{sessionformat.KindModelRequest, wantRequest},
		{sessionformat.KindInvocationFailure, wantFailure},
		{sessionformat.KindModelRequest, wantRetry},
		{sessionformat.KindModelResponse, wantResponse},
	}
	for _, snapshot := range []session.Snapshot{s.Snapshot(), loaded.Snapshot()} {
		for i, expected := range want {
			wire, err := codec.Encode(expected.value, p.PayloadLimits)
			canonicalOK(t, err)
			record := snapshot.RecordAt(3 + i)
			exported := record.Format()
			if exported.Kind != expected.kind || exported.KindVersion != sessionformat.Version || !bytes.Equal(exported.Payload, wire) {
				t.Fatalf("interaction record %d changed canonical bytes or borrowed caller storage", i)
			}
			clear(exported.Payload)
			if !bytes.Equal(record.Format().Payload, wire) {
				t.Fatal("interaction record exposed retained payload bytes")
			}
		}
		requestView, ok := snapshot.RecordAt(5).ModelRequest()
		if !ok || requestView.ID() != second {
			t.Fatal("retry identity lost")
		}
		if previous, ok := requestView.RetryOf(); !ok || previous != first {
			t.Fatal("relative retry did not reconstruct destination scope")
		}
		responseView, ok := snapshot.RecordAt(6).ModelResponse()
		if !ok || responseView.RequestID() != second || responseView.CallAt(0).ID().RequestID() != second {
			t.Fatal("response/call identity lost")
		}
	}
}

func TestCanonicalRelativeReferences(t *testing.T) {
	s, h, p := canonicalSession(t)
	b := canonicalBegin(t, s)
	canonicalOK(t, b.Create())
	run, err := b.StartRun()
	canonicalOK(t, err)
	foreignScope, err := historyid.ParseHistoryID("HST2222222222222222222222")
	canonicalOK(t, err)
	foreignRun, err := session.NewRunID(foreignScope, run.HistoryRelative())
	canonicalOK(t, err)
	foreignInput, err := session.NewUserInputID(foreignRun, 1)
	canonicalOK(t, err)
	// Positive components have no origin provenance. Missing/future components
	// still fail membership, including before any input exists in the batch.
	if err := b.AddEffectiveInput(run, sessionformat.EffectiveInput{Inputs: []sessionformat.InputItem{{UserInputID: new(foreignInput.RunRelative())}}}); !errors.Is(err, session.ErrTransition) {
		t.Fatalf("missing input: %v", err)
	}
	input, err := b.AddUserInput(run, sessionformat.UserInput{})
	canonicalOK(t, err)
	canonicalOK(t, b.AddEffectiveInput(run, sessionformat.EffectiveInput{Inputs: []sessionformat.InputItem{{UserInputID: new(foreignInput.RunRelative())}}}))
	if err := b.AddEffectiveInput(run, sessionformat.EffectiveInput{Inputs: []sessionformat.InputItem{{UserInputID: new(uint64(2))}}}); !errors.Is(err, session.ErrTransition) {
		t.Fatalf("future input: %v", err)
	}
	canonicalOK(t, b.ReplaceModelBinding(run.HistoryID(), sessionformat.ModelBinding{Route: "r", API: "a", Model: "m", Settings: []byte("{}")}))
	canonicalSubmit(t, s, b)
	b = canonicalBegin(t, s)
	request := canonicalRequest(s)
	request.RetryOf = new(uint64(1))
	if id, err := b.AddModelRequest(run, request); !errors.Is(err, session.ErrTransition) || id != (session.ModelRequestID{}) {
		t.Fatalf("missing retry: %v, %v", id, err)
	}
	request.RetryOf = nil
	first, err := b.AddModelRequest(run, request)
	canonicalOK(t, err)
	if first.RunRelative() != 1 {
		t.Fatal("failed retry consumed identity")
	}
	// Same-batch requests are not accepted pending outcomes/capture targets.
	if err := b.AddModelResponse(first, sessionformat.ModelResponse{Terminal: sessionformat.ResponseTerminalNormal}); !errors.Is(err, session.ErrTransition) {
		t.Fatalf("same-batch response: %v", err)
	}
	if err := b.AddRunEnded(run, sessionformat.RunEnded{Reason: sessionformat.RunEndedCaptureFailed, Capture: &sessionformat.CaptureFailureEvidence{RequestID: first.RunRelative()}}); !errors.Is(err, session.ErrTransition) {
		t.Fatalf("same-batch capture: %v", err)
	}
	canonicalSubmit(t, s, b)
	b = canonicalBegin(t, s)
	canonicalOK(t, b.AddInvocationFailure(first, sessionformat.InvocationFailure{Class: sessionformat.InvocationFailureTimeout, Usage: &sessionformat.InvocationUsage{Input: new(int64(0))}}))
	canonicalSubmit(t, s, b)
	foreignRequest, err := session.NewModelRequestID(foreignRun, first.RunRelative())
	canonicalOK(t, err)
	b = canonicalBegin(t, s)
	request = canonicalRequest(s)
	request.RetryOf = new(uint64(2))
	if _, err := b.AddModelRequest(run, request); !errors.Is(err, session.ErrTransition) {
		t.Fatalf("future retry: %v", err)
	}
	request.RetryOf = new(foreignRequest.RunRelative())
	second, err := b.AddModelRequest(run, request)
	canonicalOK(t, err)
	if second.RunRelative() != 2 {
		t.Fatal("failed future retry consumed identity")
	}
	canonicalSubmit(t, s, b)
	b = canonicalBegin(t, s)
	foreignPending, err := session.NewModelRequestID(foreignRun, second.RunRelative())
	canonicalOK(t, err)
	for _, missing := range []uint64{1, 3} {
		if err := b.AddRunEnded(run, sessionformat.RunEnded{Reason: sessionformat.RunEndedCaptureFailed, Capture: &sessionformat.CaptureFailureEvidence{RequestID: missing}}); !errors.Is(err, session.ErrTransition) {
			t.Fatalf("nonpending capture %d: %v", missing, err)
		}
	}
	ending := sessionformat.RunEnded{Reason: sessionformat.RunEndedCaptureFailed, Capture: &sessionformat.CaptureFailureEvidence{RequestID: foreignPending.RunRelative(), Evidence: &sessionformat.ObservedEvidence{Detail: new("retained")}}}
	canonicalOK(t, b.AddRunEnded(run, ending))
	canonicalSubmit(t, s, b)
	loaded, err := session.Load(context.Background(), h, run.HistoryID(), p)
	canonicalOK(t, err)
	for _, snapshot := range []session.Snapshot{s.Snapshot(), loaded.Snapshot()} {
		effective, ok := snapshot.RecordAt(3).EffectiveInput()
		if !ok {
			t.Fatal("missing effective input")
		}
		ref, ok := effective.InputAt(0).UserInputID()
		if !ok || ref != input || ref == foreignInput {
			t.Fatal("relative input did not reconstruct destination scope")
		}
		retry, ok := snapshot.RecordAt(7).ModelRequest()
		if !ok {
			t.Fatal("missing retry request")
		}
		previous, ok := retry.RetryOf()
		if !ok || previous != first || previous == foreignRequest {
			t.Fatal("relative retry did not reconstruct destination scope")
		}
		ended, ok := snapshot.RecordAt(8).RunEnded()
		if !ok {
			t.Fatal("missing ending")
		}
		capture, ok := ended.Capture()
		if !ok || capture.RequestID() != second || capture.RequestID() == foreignPending {
			t.Fatal("relative capture did not reconstruct destination scope")
		}
		want := ending.Clone()
		want.RunID = run.HistoryRelative()
		wire, err := codec.Encode(want, p.PayloadLimits)
		canonicalOK(t, err)
		if !bytes.Equal(snapshot.RecordAt(8).Format().Payload, wire) {
			t.Fatal("ending bytes changed")
		}
	}
	if !reflect.DeepEqual(request.RetryOf, new(foreignRequest.RunRelative())) || ending.RunID != 0 {
		t.Fatal("caller values mutated")
	}
}

func TestCanonicalExplicitDestinationAndDirtyFieldPrecedence(t *testing.T) {
	s, _, _ := canonicalSession(t)
	b := canonicalBegin(t, s)
	canonicalOK(t, b.Create())
	run, err := b.StartRun()
	canonicalOK(t, err)
	canonicalOK(t, b.ReplaceModelBinding(run.HistoryID(), sessionformat.ModelBinding{Route: "r", API: "a", Model: "m", Settings: []byte("{}")}))
	canonicalSubmit(t, s, b)
	foreignScope, err := historyid.ParseHistoryID("HST2222222222222222222222")
	canonicalOK(t, err)
	foreignRun, err := session.NewRunID(foreignScope, 1)
	canonicalOK(t, err)
	foreignRequest, err := session.NewModelRequestID(foreignRun, 1)
	canonicalOK(t, err)
	destination, err := session.NewModelRequestID(run, 1)
	canonicalOK(t, err)
	b = canonicalBegin(t, s)
	oversized := make([]byte, 1025)
	for _, component := range []uint64{run.HistoryRelative(), 99} {
		// Matching synthesized fields are just as invalid as conflicting ones.
		for _, dirty := range []sessionformat.UserInput{{RunID: component}, {ID: component}} {
			dirty.Parts = []sessionformat.ContentPart{{Media: &sessionformat.Media{InlineData: oversized}}}
			before := dirty.Clone()
			if id, err := b.AddUserInput(run, dirty); !errors.Is(err, codec.ErrInvalidData) || id != (session.UserInputID{}) {
				t.Fatalf("dirty input: %v, %v", id, err)
			}
			if !dirty.Equal(before) {
				t.Fatal("dirty input mutated")
			}
			if _, err := b.AddUserInput(foreignRun, dirty); !errors.Is(err, session.ErrTransition) {
				t.Fatalf("input destination must precede dirty/size: %v", err)
			}
		}
		for _, dirty := range []sessionformat.ModelRequest{{RunID: component}, {ID: component}} {
			dirty.Recipe.Parameters = oversized
			if id, err := b.AddModelRequest(run, dirty); !errors.Is(err, codec.ErrInvalidData) || id != (session.ModelRequestID{}) {
				t.Fatalf("dirty request: %v, %v", id, err)
			}
			if _, err := b.AddModelRequest(foreignRun, dirty); !errors.Is(err, session.ErrTransition) {
				t.Fatalf("request destination must precede dirty/size: %v", err)
			}
		}
		operations := []struct {
			name    string
			add     func() error
			foreign func() error
			invalid func() error
		}{
			{"effective", func() error {
				return b.AddEffectiveInput(run, sessionformat.EffectiveInput{RunID: component, Inputs: []sessionformat.InputItem{{}}})
			}, func() error { return b.AddEffectiveInput(foreignRun, sessionformat.EffectiveInput{RunID: component}) }, func() error {
				return b.AddEffectiveInput(session.RunID{}, sessionformat.EffectiveInput{RunID: component})
			}},
			{"ending", func() error { return b.AddRunEnded(run, sessionformat.RunEnded{RunID: component}) }, func() error { return b.AddRunEnded(foreignRun, sessionformat.RunEnded{RunID: component}) }, func() error { return b.AddRunEnded(session.RunID{}, sessionformat.RunEnded{RunID: component}) }},
			{"response run", func() error {
				return b.AddModelResponse(destination, sessionformat.ModelResponse{RunID: component, Private: &sessionformat.OpaqueProviderData{Data: oversized}})
			}, func() error { return b.AddModelResponse(foreignRequest, sessionformat.ModelResponse{RunID: component}) }, func() error {
				return b.AddModelResponse(session.ModelRequestID{}, sessionformat.ModelResponse{RunID: component})
			}},
			{"response request", func() error {
				return b.AddModelResponse(destination, sessionformat.ModelResponse{RequestID: component})
			}, func() error {
				return b.AddModelResponse(foreignRequest, sessionformat.ModelResponse{RequestID: component})
			}, func() error {
				return b.AddModelResponse(session.ModelRequestID{}, sessionformat.ModelResponse{RequestID: component})
			}},
			{"failure run", func() error {
				return b.AddInvocationFailure(destination, sessionformat.InvocationFailure{RunID: component, Explanation: new(string(oversized))})
			}, func() error {
				return b.AddInvocationFailure(foreignRequest, sessionformat.InvocationFailure{RunID: component})
			}, func() error {
				return b.AddInvocationFailure(session.ModelRequestID{}, sessionformat.InvocationFailure{RunID: component})
			}},
			{"failure request", func() error {
				return b.AddInvocationFailure(destination, sessionformat.InvocationFailure{RequestID: component})
			}, func() error {
				return b.AddInvocationFailure(foreignRequest, sessionformat.InvocationFailure{RequestID: component})
			}, func() error {
				return b.AddInvocationFailure(session.ModelRequestID{}, sessionformat.InvocationFailure{RequestID: component})
			}},
		}
		for _, op := range operations {
			if err := op.add(); !errors.Is(err, codec.ErrInvalidData) {
				t.Fatalf("%s dirty must precede content/size/lifecycle: %v", op.name, err)
			}
			if err := op.foreign(); !errors.Is(err, session.ErrIdentity) {
				t.Fatalf("%s foreign must precede dirty: %v", op.name, err)
			}
			if err := op.invalid(); !errors.Is(err, codec.ErrInvalidData) {
				t.Fatalf("%s invalid destination: %v", op.name, err)
			}
		}
	}
	for _, add := range []func(historyid.HistoryID) error{
		func(scope historyid.HistoryID) error {
			return b.ReplaceInstructions(scope, sessionformat.Instructions{Blocks: []sessionformat.InstructionBlock{{}}})
		},
		func(scope historyid.HistoryID) error {
			return b.ReplaceToolCatalog(scope, sessionformat.ToolCatalog{Tools: []sessionformat.ToolDefinition{{ArgumentSchema: oversized}}})
		},
		func(scope historyid.HistoryID) error {
			return b.ReplaceModelBinding(scope, sessionformat.ModelBinding{Settings: oversized})
		},
	} {
		if err := add(foreignScope); !errors.Is(err, session.ErrIdentity) {
			t.Fatalf("replacement foreign scope: %v", err)
		}
		if err := add(historyid.HistoryID{}); !errors.Is(err, codec.ErrInvalidData) {
			t.Fatalf("replacement invalid scope: %v", err)
		}
	}
	input, err := b.AddUserInput(run, sessionformat.UserInput{})
	canonicalOK(t, err)
	if input.RunRelative() != 1 {
		t.Fatal("failed additions consumed input identity")
	}
	canonicalSubmit(t, s, b)
	if s.Snapshot().RecordCount() != 4 {
		t.Fatal("failed additions consumed record slots")
	}
	b = canonicalBegin(t, s)
	request, err := b.AddModelRequest(run, canonicalRequest(s))
	canonicalOK(t, err)
	if request != destination {
		t.Fatal("failed additions consumed request identity")
	}
	copied := *b
	if _, err := copied.AddUserInput(foreignRun, sessionformat.UserInput{RunID: 1}); !errors.Is(err, session.ErrBatch) {
		t.Fatalf("invalid handle must precede destination: %v", err)
	}
	canonicalSubmit(t, s, b)
	if _, err := b.AddModelRequest(foreignRun, sessionformat.ModelRequest{RunID: 1}); !errors.Is(err, session.ErrBatch) {
		t.Fatalf("completed handle must precede destination: %v", err)
	}
}
