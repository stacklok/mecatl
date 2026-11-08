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
	nestedInput := identifiedInput{ID: input, Parts: []ContentPart{{Text: new(oversizeText)}}}
	nestedMedia := identifiedInput{ID: input, Parts: []ContentPart{{Media: &Media{Kind: MediaKind(oversizeText)}}}}
	nestedActor := EffectiveInput{RunID: input.runID, Inputs: []InputItem{{Inline: &AttributedInput{Actor: Actor(oversizeText)}}}}
	nestedInstructions := Instructions{HistoryID: scope, Blocks: []InstructionBlock{{Parts: []ContentPart{{Text: new(oversizeText)}}}}}
	nestedCatalog := ToolCatalog{HistoryID: scope, Tools: []ToolDefinition{{Name: "tool", ArgumentSchema: settings}}}
	nestedEffective := EffectiveInput{RunID: input.runID, Inputs: []InputItem{{Inline: &AttributedInput{Parts: parts}}}}
	requestID := ModelRequestID{runID: input.runID, requestOrdinal: 1}
	oversizeResponse := ModelResponse{RequestID: requestID, Items: make([]ResponseItem, limits.MaxArrayElements+1)}
	privateResponse := ModelResponse{RequestID: requestID, Private: &OpaqueProviderData{Data: settings}}
	evidenceResponse := ModelResponse{RequestID: requestID, Evidence: &ObservedEvidence{Correlation: &oversizeText}}
	usageResponse := ModelResponse{RequestID: requestID, Usage: &InvocationUsage{Raw: &OpaqueProviderData{Data: settings}}}
	nestedResponse := ModelResponse{RequestID: requestID, Items: []ResponseItem{{Content: &ResponseContent{Parts: parts}}}}
	failure := InvocationFailure{RequestID: requestID, Usage: usageResponse.Usage}
	ending := RunEnded{RunID: input.runID, Reason: RunEndedCaptureFailed, Capture: &CaptureFailureEvidence{RequestID: requestID, Usage: usageResponse.Usage}}
	request := identifiedRequest{ID: requestID, Recipe: ConstructionRecipe{Parameters: settings}}
	for _, makeRecord := range []func() (*materializedRecord, error){
		func() (*materializedRecord, error) {
			return makeInputRecord(identifiedInput{ID: input, Parts: parts}, limits)
		},
		func() (*materializedRecord, error) {
			return newInstructionsRecord(scope, Instructions{HistoryID: scope, Blocks: blocks}, limits)
		},
		func() (*materializedRecord, error) {
			return newToolCatalogRecord(scope, ToolCatalog{HistoryID: scope, Tools: tools}, limits)
		},
		func() (*materializedRecord, error) {
			return newEffectiveInputRecord(scope, EffectiveInput{RunID: input.runID, Inputs: items}, limits)
		},
		func() (*materializedRecord, error) {
			return newModelBindingRecord(scope, ModelBinding{HistoryID: scope, Route: "r", API: "a", Model: "m", Settings: settings}, limits)
		},
		func() (*materializedRecord, error) { return makeInputRecord(nestedInput, limits) },
		func() (*materializedRecord, error) { return makeInputRecord(nestedMedia, limits) },
		func() (*materializedRecord, error) { return newInstructionsRecord(scope, nestedInstructions, limits) },
		func() (*materializedRecord, error) { return newToolCatalogRecord(scope, nestedCatalog, limits) },
		func() (*materializedRecord, error) { return newEffectiveInputRecord(scope, nestedEffective, limits) },
		func() (*materializedRecord, error) { return newEffectiveInputRecord(scope, nestedActor, limits) },
		func() (*materializedRecord, error) { return makeRequestRecord(request, limits) },
		func() (*materializedRecord, error) { return newModelResponseRecord(scope, oversizeResponse, limits) },
		func() (*materializedRecord, error) { return newModelResponseRecord(scope, privateResponse, limits) },
		func() (*materializedRecord, error) { return newModelResponseRecord(scope, evidenceResponse, limits) },
		func() (*materializedRecord, error) { return newModelResponseRecord(scope, usageResponse, limits) },
		func() (*materializedRecord, error) { return newModelResponseRecord(scope, nestedResponse, limits) },
		func() (*materializedRecord, error) { return newInvocationFailureRecord(scope, failure, limits) },
		func() (*materializedRecord, error) { return newRunEndedRecord(scope, ending, limits) },
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
	authored := identifiedInput{ID: input, Parts: content}
	instructions := Instructions{HistoryID: scope, Blocks: []InstructionBlock{{Parts: content, Source: source}}}
	catalog := ToolCatalog{HistoryID: scope, Tools: []ToolDefinition{{Name: "read", ArgumentSchema: []byte(" true ")}}}
	binding := ModelBinding{HistoryID: scope, Route: "r", API: "a", Model: "m", Settings: []byte(" {\"x\":1.0} ")}
	effective := EffectiveInput{RunID: input.runID, Inputs: []InputItem{{UserInputID: &input}, {Inline: &AttributedInput{Actor: "harness", Purpose: PromptContext, Parts: []ContentPart{{Text: new("supplement")}}, Source: source}}}}
	manyInputs := EffectiveInput{RunID: input.runID, Inputs: make([]InputItem, limits.MaxArrayElements)}
	for i := range manyInputs.Inputs {
		manyInputs.Inputs[i].UserInputID = &input
	}
	emptyParts := identifiedInput{ID: input, Parts: make([]ContentPart, limits.MaxArrayElements)}
	for i := range emptyParts.Parts {
		emptyParts.Parts[i].Text = new("")
	}
	requestID := ModelRequestID{runID: input.runID, requestOrdinal: 1}
	request := identifiedRequest{ID: requestID, Source: SourceHead{Sequence: 1, CID: contentid.Sum([]byte("source"))}, Recipe: ConstructionRecipe{Algorithm: "a", Version: 1, Parameters: []byte{0xff}, Check: []byte{0}}, RetryOf: &requestID}
	usage := &InvocationUsage{Input: new(int64(0)), Output: new(int64(3)), Raw: &OpaqueProviderData{Producer: "p", Format: "usage", Version: 1, Data: []byte{0xff}}}
	response := ModelResponse{RequestID: requestID, Terminal: ResponseTerminalOther, ProviderStopReason: new(""), Items: []ResponseItem{{Content: &ResponseContent{Kind: ResponseContentOutput, Parts: content}}, {Call: &RequestedCall{Name: "", Arguments: []byte{0xff}}}}, Evidence: &ObservedEvidence{Correlation: new("")}, Usage: usage, Private: &OpaqueProviderData{Producer: "p", Format: "f", Version: 1, Data: []byte{0xff}}}
	failure := InvocationFailure{RequestID: requestID, Class: InvocationFailureTransport, Usage: usage, Evidence: response.Evidence, Explanation: new("")}
	ending := RunEnded{RunID: input.runID, Reason: RunEndedCaptureFailed, Capture: &CaptureFailureEvidence{RequestID: requestID, Evidence: response.Evidence, Usage: usage}}
	for _, tc := range []struct {
		name  string
		make  func(codec.Limits) (*materializedRecord, error)
		check func(codec.Limits) ([]byte, error)
	}{
		{"request", func(l codec.Limits) (*materializedRecord, error) { return makeRequestRecord(request, l) }, func(l codec.Limits) ([]byte, error) { return codec.Encode(request.toFormat(), l) }},
		{"response", func(l codec.Limits) (*materializedRecord, error) { return newModelResponseRecord(scope, response, l) }, func(l codec.Limits) ([]byte, error) { return codec.Encode(response.toFormat(), l) }},
		{"failure", func(l codec.Limits) (*materializedRecord, error) {
			return newInvocationFailureRecord(scope, failure, l)
		}, func(l codec.Limits) ([]byte, error) { return codec.Encode(failure.toFormat(), l) }},
		{"ending", func(l codec.Limits) (*materializedRecord, error) { return newRunEndedRecord(scope, ending, l) }, func(l codec.Limits) ([]byte, error) { return codec.Encode(ending.toFormat(), l) }},
		{"input", func(l codec.Limits) (*materializedRecord, error) { return makeInputRecord(authored, l) }, func(l codec.Limits) ([]byte, error) { return codec.Encode(authored.toFormat(), l) }},
		{"instructions", func(l codec.Limits) (*materializedRecord, error) {
			return newInstructionsRecord(scope, instructions, l)
		}, func(l codec.Limits) ([]byte, error) { return codec.Encode(instructions.toFormat(), l) }},
		{"catalog", func(l codec.Limits) (*materializedRecord, error) { return newToolCatalogRecord(scope, catalog, l) }, func(l codec.Limits) ([]byte, error) { return codec.Encode(catalog.toFormat(), l) }},
		{"binding", func(l codec.Limits) (*materializedRecord, error) { return newModelBindingRecord(scope, binding, l) }, func(l codec.Limits) ([]byte, error) { return codec.Encode(binding.toFormat(), l) }},
		{"effective", func(l codec.Limits) (*materializedRecord, error) { return newEffectiveInputRecord(scope, effective, l) }, func(l codec.Limits) ([]byte, error) { return codec.Encode(effective.toFormat(), l) }},
		{"pointer-heavy", func(l codec.Limits) (*materializedRecord, error) {
			return newEffectiveInputRecord(scope, manyInputs, l)
		}, func(l codec.Limits) ([]byte, error) { return codec.Encode(manyInputs.toFormat(), l) }},
		{"empty-parts", func(l codec.Limits) (*materializedRecord, error) { return makeInputRecord(emptyParts, l) }, func(l codec.Limits) ([]byte, error) { return codec.Encode(emptyParts.toFormat(), l) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			original, err := tc.make(limits)
			if err != nil {
				t.Fatal(err)
			}
			want := materializedRecord{value: original.format()}
			switch tc.name {
			case "request":
				want.request = request.toFormat()
			case "response":
				want.response = response.toFormat()
			case "failure":
				want.failure = failure.toFormat()
			case "ending":
				want.ended = ending.toFormat()
			case "input":
				want.input = authored.toFormat()
			case "instructions":
				want.instructions = instructions.toFormat()
			case "catalog":
				want.catalog = catalog.toFormat()
			case "binding":
				want.binding = binding.toFormat()
			case "effective":
				want.effective = effective.toFormat()
			case "pointer-heavy":
				want.effective = manyInputs.toFormat()
			case "empty-parts":
				want.input = emptyParts.toFormat()
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
	input, err := b.AddUserInput(UserInput{RunID: run})
	if err != nil {
		t.Fatal(err)
	}
	instructions := Instructions{HistoryID: scope, Blocks: []InstructionBlock{{Source: ContentSource{Description: "root"}}}}
	if err := b.ReplaceInstructions(instructions); err != nil {
		t.Fatal(err)
	}
	originalCatalog := ToolCatalog{HistoryID: scope, Tools: []ToolDefinition{{Name: "original", ArgumentSchema: []byte("true")}}}
	originalBinding := ModelBinding{HistoryID: scope, Route: "original", API: "a", Model: "m", Settings: []byte(" true ")}
	if err := b.ReplaceToolCatalog(originalCatalog); err != nil {
		t.Fatal(err)
	}
	if err := b.ReplaceModelBinding(originalBinding); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SubmitCommit(context.Background(), b); err != nil {
		t.Fatal(err)
	}
	before := detachedSessionState(s.state)
	old := s.Snapshot()
	replacement, err := newInstructionsRecord(scope, Instructions{HistoryID: scope}, limits)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := newToolCatalogRecord(scope, ToolCatalog{HistoryID: scope}, limits)
	if err != nil {
		t.Fatal(err)
	}
	binding, err := newModelBindingRecord(scope, ModelBinding{HistoryID: scope, Route: "r", API: "a", Model: "m", Settings: []byte("{}")}, limits)
	if err != nil {
		t.Fatal(err)
	}
	bad, err := newEffectiveInputRecord(scope, EffectiveInput{RunID: run, Inputs: []InputItem{{UserInputID: &input}, {UserInputID: &UserInputID{runID: run, ordinal: 2}}}}, limits)
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
	if !ok || iv.HistoryID() != scope || iv.BlockCount() != 1 || iv.BlockAt(0).PartCount() != 0 || iv.BlockAt(0).Source().Description() != "root" {
		t.Fatal("old selection changed")
	}
	if tv, ok := s.Snapshot().ToolCatalog(); !ok || tv.HistoryID() != scope || tv.ToolCount() != 1 || tv.ToolAt(0).Name() != "original" || string(tv.ToolAt(0).ArgumentSchema()) != "true" {
		t.Fatal("catalog partially published")
	}
	if mv, ok := s.Snapshot().ModelBinding(); !ok || mv.HistoryID() != scope || mv.Route() != "original" || mv.API() != "a" || mv.Model() != "m" || string(mv.Settings()) != " true " {
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
