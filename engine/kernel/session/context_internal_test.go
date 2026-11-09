package session

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/stacklok/mecatl/engine/kernel/codec"
	"github.com/stacklok/mecatl/engine/kernel/contentid"
	"github.com/stacklok/mecatl/engine/kernel/history"
	historyformat "github.com/stacklok/mecatl/engine/kernel/history/format"
	sessionformat "github.com/stacklok/mecatl/engine/kernel/session/format"
)

func TestRichConstructorsBudgetBeforeConversion(t *testing.T) {
	s, limits := internalFixture(t)
	scope := s.Snapshot().HistoryID()
	input := UserInputID{runID: RunID{historyID: scope, ordinal: 1}, ordinal: 1}
	// All values are allocated before measuring construction. Rejected collection
	// lengths must not allocate DTO slices, or parse their deliberately invalid JSON.
	parts := make([]ContentPart, limits.MaxArrayElements+1)
	blocks := make([]InstructionBlock, limits.MaxArrayElements+1)
	tools := make([]ToolDefinition, limits.MaxArrayElements+1)
	items := make([]InputItem, limits.MaxArrayElements+1)
	settings := make([]byte, limits.MaxByteStringBytes+1)
	oversizeText := string(make([]byte, limits.MaxStringBytes+1))
	nestedInput := identifiedInput{ID: input, UserInput: UserInput{Parts: []ContentPart{{Text: new(oversizeText)}}}}
	nestedMedia := identifiedInput{ID: input, UserInput: UserInput{Parts: []ContentPart{{Media: &Media{Kind: MediaKind(oversizeText)}}}}}
	nestedActor := EffectiveInput{Inputs: []InputItem{{Inline: &AttributedInput{Actor: Actor(oversizeText)}}}}
	nestedInstructions := Instructions{Blocks: []InstructionBlock{{Parts: []ContentPart{{Text: new(oversizeText)}}}}}
	nestedCatalog := ToolCatalog{Tools: []ToolDefinition{{Name: "tool", ArgumentSchema: settings}}}
	nestedEffective := EffectiveInput{Inputs: []InputItem{{Inline: &AttributedInput{Parts: parts}}}}
	requestID := ModelRequestID{runID: input.runID, requestOrdinal: 1}
	oversizeResponse := ModelResponse{Items: make([]ResponseItem, limits.MaxArrayElements+1)}
	privateResponse := ModelResponse{Private: &OpaqueProviderData{Data: settings}}
	evidenceResponse := ModelResponse{Evidence: &ObservedEvidence{Correlation: &oversizeText}}
	usageResponse := ModelResponse{Usage: &InvocationUsage{Raw: &OpaqueProviderData{Data: settings}}}
	nestedResponse := ModelResponse{Items: []ResponseItem{{Content: &ResponseContent{Parts: parts}}}}
	failure := InvocationFailure{Usage: usageResponse.Usage}
	ending := RunEnded{Reason: RunEndedCaptureFailed, Capture: &CaptureFailureEvidence{RequestID: requestID.RunRelative(), Usage: usageResponse.Usage}}
	request := identifiedRequest{ID: requestID, ModelRequest: ModelRequest{Recipe: ConstructionRecipe{Parameters: settings}}}
	for _, makeRecord := range []func() (*materializedRecord, error){
		func() (*materializedRecord, error) {
			return makeInputRecord(identifiedInput{ID: input, UserInput: UserInput{Parts: parts}}, limits)
		},
		func() (*materializedRecord, error) {
			return newInstructionsRecord(Instructions{Blocks: blocks}, limits)
		},
		func() (*materializedRecord, error) {
			return newToolCatalogRecord(ToolCatalog{Tools: tools}, limits)
		},
		func() (*materializedRecord, error) {
			return newEffectiveInputRecord(EffectiveInput{Inputs: items}, input.runID, limits)
		},
		func() (*materializedRecord, error) {
			return newModelBindingRecord(ModelBinding{Route: "r", API: "a", Model: "m", Settings: settings}, limits)
		},
		func() (*materializedRecord, error) { return makeInputRecord(nestedInput, limits) },
		func() (*materializedRecord, error) { return makeInputRecord(nestedMedia, limits) },
		func() (*materializedRecord, error) { return newInstructionsRecord(nestedInstructions, limits) },
		func() (*materializedRecord, error) { return newToolCatalogRecord(nestedCatalog, limits) },
		func() (*materializedRecord, error) {
			return newEffectiveInputRecord(nestedEffective, input.runID, limits)
		},
		func() (*materializedRecord, error) { return newEffectiveInputRecord(nestedActor, input.runID, limits) },
		func() (*materializedRecord, error) { return makeRequestRecord(request, limits) },
		func() (*materializedRecord, error) {
			return newModelResponseRecord(oversizeResponse, requestID, limits)
		},
		func() (*materializedRecord, error) { return newModelResponseRecord(privateResponse, requestID, limits) },
		func() (*materializedRecord, error) {
			return newModelResponseRecord(evidenceResponse, requestID, limits)
		},
		func() (*materializedRecord, error) { return newModelResponseRecord(usageResponse, requestID, limits) },
		func() (*materializedRecord, error) { return newModelResponseRecord(nestedResponse, requestID, limits) },
		func() (*materializedRecord, error) { return newInvocationFailureRecord(failure, requestID, limits) },
		func() (*materializedRecord, error) { return newRunEndedRecord(ending, input.runID, limits) },
	} {
		if r, err := makeRecord(); !errors.Is(err, codec.ErrLimitExceeded) || r != nil {
			t.Fatalf("bounded failure: %v", err)
		}
		if n := testing.AllocsPerRun(10, func() { _, _ = makeRecord() }); n != 0 {
			t.Fatalf("rejected conversion allocated: %g", n)
		}
	}
}

func TestNewRecordMaterializationAndGeneratedLimitsAgree(t *testing.T) {
	s, limits := internalFixture(t)
	scope := s.Snapshot().HistoryID()
	input := UserInputID{runID: RunID{historyID: scope, ordinal: 1}, ordinal: 1}
	content := []ContentPart{{Text: new(" exact ")}, {Media: &Media{Kind: MediaKindDocument, MIMEType: "raw", Source: MediaSourceInline, InlineData: []byte{0xff, 0}}}}
	source := ContentSource{Description: "adopted", WorkspaceFile: &WorkspaceFileSource{Path: "AGENTS.md"}}
	authored := identifiedInput{ID: input, UserInput: UserInput{Parts: content}}
	instructions := Instructions{Blocks: []InstructionBlock{{Parts: content, Source: source}}}
	catalog := ToolCatalog{Tools: []ToolDefinition{{Name: "read", ArgumentSchema: []byte(" true ")}}}
	binding := ModelBinding{Route: "r", API: "a", Model: "m", Settings: []byte(" {\"x\":1.0} ")}
	effective := EffectiveInput{Inputs: []InputItem{{UserInputID: new(input.ordinal)}, {Inline: &AttributedInput{Actor: "harness", Purpose: PromptContext, Parts: []ContentPart{{Text: new("supplement")}}, Source: source}}}}
	manyInputs := EffectiveInput{Inputs: make([]InputItem, limits.MaxArrayElements)}
	for i := range manyInputs.Inputs {
		manyInputs.Inputs[i].UserInputID = new(input.ordinal)
	}
	emptyParts := identifiedInput{ID: input, UserInput: UserInput{Parts: make([]ContentPart, limits.MaxArrayElements)}}
	for i := range emptyParts.Parts {
		emptyParts.Parts[i].Text = new("")
	}
	requestID := ModelRequestID{runID: input.runID, requestOrdinal: 1}
	request := identifiedRequest{ID: requestID, ModelRequest: ModelRequest{Source: SourceHead{Sequence: 1, CID: contentid.Sum([]byte("source"))}, Recipe: ConstructionRecipe{Algorithm: "a", Version: 1, Parameters: []byte{0xff}, Check: []byte{0}}, RetryOf: new(requestID.RunRelative())}}
	usage := &InvocationUsage{Input: new(int64(0)), Output: new(int64(3)), Raw: &OpaqueProviderData{Producer: "p", Format: "usage", Version: 1, Data: []byte{0xff}}}
	response := ModelResponse{Terminal: ResponseTerminalOther, ProviderStopReason: new(""), Items: []ResponseItem{{Content: &ResponseContent{Kind: ResponseContentOutput, Parts: content}}, {Call: &RequestedCall{Name: "", Arguments: []byte{0xff}}}}, Evidence: &ObservedEvidence{Correlation: new("")}, Usage: usage, Private: &OpaqueProviderData{Producer: "p", Format: "f", Version: 1, Data: []byte{0xff}}}
	failure := InvocationFailure{Class: InvocationFailureTransport, Usage: usage, Evidence: response.Evidence, Explanation: new("")}
	ending := RunEnded{Reason: RunEndedCaptureFailed, Capture: &CaptureFailureEvidence{RequestID: requestID.RunRelative(), Evidence: response.Evidence, Usage: usage}}
	storedRequest := request.ModelRequest
	storedRequest.RunID, storedRequest.ID = input.runID.ordinal, requestID.requestOrdinal
	storedResponse := response
	storedResponse.RunID, storedResponse.RequestID = input.runID.ordinal, requestID.requestOrdinal
	storedFailure := failure
	storedFailure.RunID, storedFailure.RequestID = input.runID.ordinal, requestID.requestOrdinal
	storedEnding := ending
	storedEnding.RunID = input.runID.ordinal
	storedInput := authored.UserInput
	storedInput.RunID, storedInput.ID = input.runID.ordinal, input.ordinal
	storedEmpty := emptyParts.UserInput
	storedEmpty.RunID, storedEmpty.ID = input.runID.ordinal, input.ordinal
	storedEffective, storedMany := effective, manyInputs
	storedEffective.RunID, storedMany.RunID = input.runID.ordinal, input.runID.ordinal
	for _, tc := range []struct {
		name  string
		make  func(codec.Limits) (*materializedRecord, error)
		check func(codec.Limits) ([]byte, error)
	}{
		{"request", func(l codec.Limits) (*materializedRecord, error) { return makeRequestRecord(request, l) }, func(l codec.Limits) ([]byte, error) { return codec.Encode(storedRequest, l) }},
		{"response", func(l codec.Limits) (*materializedRecord, error) {
			return newModelResponseRecord(response, requestID, l)
		}, func(l codec.Limits) ([]byte, error) { return codec.Encode(storedResponse, l) }},
		{"failure", func(l codec.Limits) (*materializedRecord, error) {
			return newInvocationFailureRecord(failure, requestID, l)
		}, func(l codec.Limits) ([]byte, error) { return codec.Encode(storedFailure, l) }},
		{"ending", func(l codec.Limits) (*materializedRecord, error) { return newRunEndedRecord(ending, input.runID, l) }, func(l codec.Limits) ([]byte, error) { return codec.Encode(storedEnding, l) }},
		{"input", func(l codec.Limits) (*materializedRecord, error) { return makeInputRecord(authored, l) }, func(l codec.Limits) ([]byte, error) { return codec.Encode(storedInput, l) }},
		{"instructions", func(l codec.Limits) (*materializedRecord, error) {
			return newInstructionsRecord(instructions, l)
		}, func(l codec.Limits) ([]byte, error) { return codec.Encode(instructions, l) }},
		{"catalog", func(l codec.Limits) (*materializedRecord, error) { return newToolCatalogRecord(catalog, l) }, func(l codec.Limits) ([]byte, error) { return codec.Encode(catalog, l) }},
		{"binding", func(l codec.Limits) (*materializedRecord, error) { return newModelBindingRecord(binding, l) }, func(l codec.Limits) ([]byte, error) { return codec.Encode(binding, l) }},
		{"effective", func(l codec.Limits) (*materializedRecord, error) {
			return newEffectiveInputRecord(effective, input.runID, l)
		}, func(l codec.Limits) ([]byte, error) { return codec.Encode(storedEffective, l) }},
		{"pointer-heavy", func(l codec.Limits) (*materializedRecord, error) {
			return newEffectiveInputRecord(manyInputs, input.runID, l)
		}, func(l codec.Limits) ([]byte, error) { return codec.Encode(storedMany, l) }},
		{"empty-parts", func(l codec.Limits) (*materializedRecord, error) { return makeInputRecord(emptyParts, l) }, func(l codec.Limits) ([]byte, error) { return codec.Encode(storedEmpty, l) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			original, err := tc.make(limits)
			if err != nil {
				t.Fatal(err)
			}
			want := materializedRecord{value: original.format()}
			switch tc.name {
			case "request":
				want.request = storedRequest
			case "response":
				want.response = storedResponse
			case "failure":
				want.failure = storedFailure
			case "ending":
				want.ended = storedEnding
			case "input":
				want.input = storedInput
			case "instructions":
				want.instructions = instructions
			case "catalog":
				want.catalog = catalog
			case "binding":
				want.binding = binding
			case "effective":
				want.effective = storedEffective
			case "pointer-heavy":
				want.effective = storedMany
			case "empty-parts":
				want.input = storedEmpty
			}
			if !reflect.DeepEqual(*original, want) {
				t.Fatal("backing must contain the complete selected DTO and zero inactive variants")
			}
			decoded, err := decodeMaterialized(original.format(), limits)
			if err != nil {
				t.Fatal(err)
			}
			// Complete materialization agrees, not just reducer-relevant scalars.
			if !reflect.DeepEqual(original, decoded) {
				t.Fatal("constructed and decoded backing differ")
			}
			// Find each exact generated admission boundary independently, including
			// MaxBytes' conservative nine-byte node estimate, not just wire length.
			wide := limits
			maxInt := int(^uint(0) >> 1)
			for i := 0; i < reflect.TypeOf(wide).NumField(); i++ {
				reflect.ValueOf(&wide).Elem().Field(i).SetInt(int64(maxInt))
			}
			for i := 0; i < reflect.TypeOf(limits).NumField(); i++ {
				lo, hi := 1, 1
				for {
					l := wide
					reflect.ValueOf(&l).Elem().Field(i).SetInt(int64(hi))
					if _, err := tc.check(l); err == nil {
						break
					}
					hi *= 2
				}
				for lo < hi {
					mid := lo + (hi-lo)/2
					l := wide
					reflect.ValueOf(&l).Elem().Field(i).SetInt(int64(mid))
					if _, err := tc.check(l); err == nil {
						hi = mid
					} else {
						lo = mid + 1
					}
				}
				for _, ceiling := range []int{0, lo - 1, lo, lo + 1, maxInt} {
					l := wide
					reflect.ValueOf(&l).Elem().Field(i).SetInt(int64(ceiling))
					got, gotErr := tc.make(l)
					want, wantErr := tc.check(l)
					if errors.Is(wantErr, codec.ErrLimitExceeded) {
						wantErr = codec.ErrLimitExceeded
					}
					if !errors.Is(gotErr, wantErr) {
						t.Fatalf("constructor %s=%d: %v; codec.Encode: %v", reflect.TypeOf(l).Field(i).Name, ceiling, gotErr, wantErr)
					}
					if wantErr == nil {
						if !reflect.DeepEqual(got.format().Payload, want) {
							t.Fatal("admitted constructor changed canonical payload")
						}
					} else if got != nil {
						t.Fatal("rejected constructor returned partial record")
					}
					if err := original.checkPayload(l); !errors.Is(err, wantErr) {
						t.Fatalf("preflight %s=%d: %v; want %v", reflect.TypeOf(l).Field(i).Name, ceiling, err, wantErr)
					}
					imported, decodeErr := decodeMaterialized(original.format(), l)
					if !errors.Is(decodeErr, wantErr) {
						t.Fatalf("decode %s=%d: %v; want %v", reflect.TypeOf(l).Field(i).Name, ceiling, decodeErr, wantErr)
					}
					if decodeErr != nil && imported != nil {
						t.Fatal("bounded decode returned partial record")
					}
				}
			}
		})
	}
}

func TestContextApplyStagesSelectionsAndSourceBounds(t *testing.T) {
	s, limits := internalFixture(t)
	scope := s.Snapshot().HistoryID()
	b, err := s.BeginCommit()
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Create(); err != nil {
		t.Fatal(err)
	}
	run, err := b.StartRun()
	if err != nil {
		t.Fatal(err)
	}
	input, err := b.AddUserInput(run, UserInput{})
	if err != nil {
		t.Fatal(err)
	}
	instructions := Instructions{Blocks: []InstructionBlock{{Source: ContentSource{Description: "root"}}}}
	if err := b.ReplaceInstructions(scope, instructions); err != nil {
		t.Fatal(err)
	}
	originalCatalog := ToolCatalog{Tools: []ToolDefinition{{Name: "original", ArgumentSchema: []byte("true")}}}
	originalBinding := ModelBinding{Route: "original", API: "a", Model: "m", Settings: []byte(" true ")}
	if err := b.ReplaceToolCatalog(scope, originalCatalog); err != nil {
		t.Fatal(err)
	}
	if err := b.ReplaceModelBinding(scope, originalBinding); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SubmitCommit(context.Background(), b); err != nil {
		t.Fatal(err)
	}
	before := detachedSessionState(s.state)
	old := s.Snapshot()
	replacement, err := newInstructionsRecord(Instructions{}, limits)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := newToolCatalogRecord(ToolCatalog{}, limits)
	if err != nil {
		t.Fatal(err)
	}
	binding, err := newModelBindingRecord(ModelBinding{Route: "r", API: "a", Model: "m", Settings: []byte("{}")}, limits)
	if err != nil {
		t.Fatal(err)
	}
	bad, err := newEffectiveInputRecord(EffectiveInput{Inputs: []InputItem{{UserInputID: new(input.ordinal)}, {UserInputID: new(uint64(2))}}}, run, limits)
	if err != nil {
		t.Fatal(err)
	}
	// Construct an external accepted assertion independently of local preflight.
	c := history.Commit{Commit: historyformat.Commit{FormatVersion: 1, Parent: new(old.Head().CID), Sequence: old.Head().Sequence + 1, Records: []historyformat.Record{replacement.format(), catalog.format(), binding.format(), bad.format()}}}
	wire, err := codec.Encode(c.Commit, limits)
	if err != nil {
		t.Fatal(err)
	}
	c.Ref = history.CommitRef{Sequence: c.Commit.Sequence, CID: contentid.Sum(wire)}
	if err := s.apply(c); !errors.Is(err, ErrTransition) {
		t.Fatalf("source bound: %v", err)
	}
	if !reflect.DeepEqual(before, detachedSessionState(s.state)) {
		t.Fatal("failed final source published selections/counters/storage")
	}
	iv, ok := old.Instructions()
	if !ok || iv.HistoryID() != scope || iv.Blocks().Len() != 1 || iv.Blocks().At(0).Parts().Len() != 0 || iv.Blocks().At(0).Source().Description() != "root" {
		t.Fatal("old selection changed")
	}
	if tv, ok := s.Snapshot().ToolCatalog(); !ok || tv.HistoryID() != scope || tv.Tools().Len() != 1 || tv.Tools().At(0).Name() != "original" || string(tv.Tools().At(0).ArgumentSchema().Copy()) != "true" {
		t.Fatal("catalog partially published")
	}
	if mv, ok := s.Snapshot().ModelBinding(); !ok || mv.HistoryID() != scope || mv.Route() != "original" || mv.API() != "a" || mv.Model() != "m" || string(mv.Settings().Copy()) != " true " {
		t.Fatal("binding partially published")
	}
	user, err := makeInputRecord(identifiedInput{ID: input}, limits)
	if err != nil {
		t.Fatal(err)
	}
	for _, record := range []*materializedRecord{replacement, catalog, binding, bad, user} {
		for _, failure := range []string{"malformed", "version", "oversize"} {
			t.Run(record.value.Kind+"/"+failure, func(t *testing.T) {
				external := c.Commit.Clone()
				last := record.format()
				wantErr := codec.ErrInvalidData
				switch failure {
				case "version":
					last.KindVersion++
					wantErr = ErrUnsupportedRecord
				case "malformed":
					last.Payload = []byte{0xff}
				case "oversize":
					key := map[string]byte{sessionformat.KindInstructions: 'b', sessionformat.KindToolCatalog: 't', sessionformat.KindModelBinding: 's', sessionformat.KindEffectiveInput: 'i', sessionformat.KindUserInput: 'p'}[last.Kind]
					last.Payload = []byte{0xa1, 0x61, key, 0x9b, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff}
					if last.Kind == sessionformat.KindModelBinding {
						last.Payload[3] = 0x5b // Declared byte length rather than array count.
					}
					wantErr = codec.ErrLimitExceeded
				}
				external.Records[len(external.Records)-1] = last
				wire, err := codec.Encode(external, limits)
				if err != nil {
					t.Fatal(err)
				}
				accepted := history.Commit{Commit: external, Ref: history.CommitRef{Sequence: external.Sequence, CID: contentid.Sum(wire)}}
				if err := s.apply(accepted); !errors.Is(err, wantErr) {
					t.Fatalf("external final record: %v; want %v", err, wantErr)
				}
				oldState := before
				oldState.view = old
				if !reflect.DeepEqual(before, detachedSessionState(s.state)) || !reflect.DeepEqual(before, detachedSessionState(oldState)) {
					t.Fatal("malformed/unknown final record changed full state or old handles")
				}
				// History accepts opaque session payloads. Replay must reject the
				// same malformed/unknown final record after a real verified read,
				// returning nil rather than a partially reconstructed Session.
				h := internalTestHistory(t, limits)
				page, err := s.history.Read(context.Background(), scope, history.ReadRequest{Through: old.Head(), MaxCommits: 1})
				if err != nil {
					t.Fatal(err)
				}
				if _, err := h.Append(context.Background(), scope, page.Commits[0].Commit); err != nil {
					t.Fatal(err)
				}
				if _, err := h.Append(context.Background(), scope, external); err != nil {
					t.Fatal(err)
				}
				if loaded, err := Load(context.Background(), h, scope, s.policy); loaded != nil || !errors.Is(err, wantErr) {
					t.Fatalf("malformed/unknown Load: %v, %v", loaded, err)
				}
				if head, err := h.Head(context.Background(), scope); err != nil || head != accepted.Ref {
					t.Fatal("failed replay appended or changed history")
				}
			})
		}
	}
}
