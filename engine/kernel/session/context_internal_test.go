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
	input := UserInputID{RunID: RunID{HistoryID: scope, Ordinal: 1}, Ordinal: 1}
	// All values are allocated before measuring construction. Rejected collection
	// lengths must not allocate DTO slices, or parse their deliberately invalid JSON.
	parts := make([]ContentPart, limits.MaxArrayElements+1)
	blocks := make([]InstructionBlock, limits.MaxArrayElements+1)
	tools := make([]ToolDefinition, limits.MaxArrayElements+1)
	items := make([]InputItem, limits.MaxArrayElements+1)
	settings := make([]byte, limits.MaxByteStringBytes+1)
	oversizeText := string(make([]byte, limits.MaxStringBytes+1))
	nestedInput := UserInput{ID: input, Parts: []ContentPart{{Text: new(oversizeText)}}}
	nestedMedia := UserInput{ID: input, Parts: []ContentPart{{Media: &Media{Kind: MediaKind(oversizeText)}}}}
	nestedActor := EffectiveInput{RunID: input.RunID, Inputs: []InputItem{{Inline: &AttributedInput{Actor: Actor(oversizeText)}}}}
	nestedInstructions := Instructions{HistoryID: scope, Blocks: []InstructionBlock{{Parts: []ContentPart{{Text: new(oversizeText)}}}}}
	nestedCatalog := ToolCatalog{HistoryID: scope, Tools: []ToolDefinition{{Name: "tool", ArgumentSchema: settings}}}
	nestedEffective := EffectiveInput{RunID: input.RunID, Inputs: []InputItem{{Inline: &AttributedInput{Parts: parts}}}}
	for _, makeRecord := range []func() (Record, error){
		func() (Record, error) { return NewUserInputRecord(UserInput{ID: input, Parts: parts}, limits) },
		func() (Record, error) {
			return NewInstructionsRecord(Instructions{HistoryID: scope, Blocks: blocks}, limits)
		},
		func() (Record, error) {
			return NewToolCatalogRecord(ToolCatalog{HistoryID: scope, Tools: tools}, limits)
		},
		func() (Record, error) {
			return NewEffectiveInputRecord(EffectiveInput{RunID: input.RunID, Inputs: items}, limits)
		},
		func() (Record, error) {
			return NewModelBindingRecord(ModelBinding{HistoryID: scope, Route: "r", API: "a", Model: "m", Settings: settings}, limits)
		},
		func() (Record, error) { return NewUserInputRecord(nestedInput, limits) },
		func() (Record, error) { return NewUserInputRecord(nestedMedia, limits) },
		func() (Record, error) { return NewInstructionsRecord(nestedInstructions, limits) },
		func() (Record, error) { return NewToolCatalogRecord(nestedCatalog, limits) },
		func() (Record, error) { return NewEffectiveInputRecord(nestedEffective, limits) },
		func() (Record, error) { return NewEffectiveInputRecord(nestedActor, limits) },
	} {
		if r, err := makeRecord(); !errors.Is(err, codec.ErrLimitExceeded) || !reflect.DeepEqual(r, Record{}) {
			t.Fatalf("bounded failure: %v", err)
		}
		if n := testing.AllocsPerRun(10, func() { _, _ = makeRecord() }); n != 0 {
			t.Fatalf("rejected conversion allocated: %g", n)
		}
	}
}

func TestNewRecordMetadataAndGeneratedLimitsAgree(t *testing.T) {
	s, limits := internalFixture(t)
	scope := s.Snapshot().HistoryID()
	input := UserInputID{RunID: RunID{HistoryID: scope, Ordinal: 1}, Ordinal: 1}
	content := []ContentPart{{Text: new(" exact ")}, {Media: &Media{Kind: MediaKindDocument, MIMEType: "raw", Source: MediaSourceInline, InlineData: []byte{0xff, 0}}}}
	source := ContentSource{Description: "adopted", WorkspaceFile: &WorkspaceFileSource{Path: "AGENTS.md"}}
	authored := UserInput{ID: input, Parts: content}
	instructions := Instructions{HistoryID: scope, Blocks: []InstructionBlock{{Parts: content, Source: source}}}
	catalog := ToolCatalog{HistoryID: scope, Tools: []ToolDefinition{{Name: "read", ArgumentSchema: []byte(" true ")}}}
	binding := ModelBinding{HistoryID: scope, Route: "r", API: "a", Model: "m", Settings: []byte(" {\"x\":1.0} ")}
	effective := EffectiveInput{RunID: input.RunID, Inputs: []InputItem{{UserInput: &input}, {Inline: &AttributedInput{Actor: "harness", Purpose: PromptContext, Parts: []ContentPart{{Text: new("supplement")}}, Source: source}}}}
	manyInputs := EffectiveInput{RunID: input.RunID, Inputs: make([]InputItem, limits.MaxArrayElements)}
	for i := range manyInputs.Inputs {
		manyInputs.Inputs[i].UserInput = &input
	}
	emptyParts := UserInput{ID: input, Parts: make([]ContentPart, limits.MaxArrayElements)}
	for i := range emptyParts.Parts {
		emptyParts.Parts[i].Text = new("")
	}
	for _, tc := range []struct {
		name  string
		make  func(codec.Limits) (Record, error)
		check func(codec.Limits) ([]byte, error)
	}{
		{"input", func(l codec.Limits) (Record, error) { return NewUserInputRecord(authored, l) }, func(l codec.Limits) ([]byte, error) { return codec.Encode(authored.toFormat(), l) }},
		{"instructions", func(l codec.Limits) (Record, error) { return NewInstructionsRecord(instructions, l) }, func(l codec.Limits) ([]byte, error) { return codec.Encode(instructions.toFormat(), l) }},
		{"catalog", func(l codec.Limits) (Record, error) { return NewToolCatalogRecord(catalog, l) }, func(l codec.Limits) ([]byte, error) { return codec.Encode(catalog.toFormat(), l) }},
		{"binding", func(l codec.Limits) (Record, error) { return NewModelBindingRecord(binding, l) }, func(l codec.Limits) ([]byte, error) { return codec.Encode(binding.toFormat(), l) }},
		{"effective", func(l codec.Limits) (Record, error) { return NewEffectiveInputRecord(effective, l) }, func(l codec.Limits) ([]byte, error) { return codec.Encode(effective.toFormat(), l) }},
		{"pointer-heavy", func(l codec.Limits) (Record, error) { return NewEffectiveInputRecord(manyInputs, l) }, func(l codec.Limits) ([]byte, error) { return codec.Encode(manyInputs.toFormat(), l) }},
		{"empty-parts", func(l codec.Limits) (Record, error) { return NewUserInputRecord(emptyParts, l) }, func(l codec.Limits) ([]byte, error) { return codec.Encode(emptyParts.toFormat(), l) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			original, err := tc.make(limits)
			if err != nil {
				t.Fatal(err)
			}
			decoded, err := DecodeRecord(scope, original.Format(), limits)
			if err != nil {
				t.Fatal(err)
			}
			// Every scalar used by the reducer agrees between construction and decoding.
			if original.value.Kind == sessionformat.KindUserInput && (original.userInput != decoded.userInput) {
				t.Fatal("input metadata")
			}
			if original.value.Kind == sessionformat.KindEffectiveInput && (!original.effectiveInput.Equal(decoded.effectiveInput)) {
				t.Fatal("effective metadata")
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
						if !reflect.DeepEqual(got.Format().Payload, want) {
							t.Fatal("admitted constructor changed canonical payload")
						}
					} else if !reflect.DeepEqual(got, Record{}) {
						t.Fatal("rejected constructor returned partial record")
					}
					if err := original.checkPayload(l); !errors.Is(err, wantErr) {
						t.Fatalf("preflight %s=%d: %v; want %v", reflect.TypeOf(l).Field(i).Name, ceiling, err, wantErr)
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
	input, err := b.AddUserInput(AddUserInputRequest{RunID: run})
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
	replacement, err := NewInstructionsRecord(Instructions{HistoryID: scope}, limits)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := NewToolCatalogRecord(ToolCatalog{HistoryID: scope}, limits)
	if err != nil {
		t.Fatal(err)
	}
	binding, err := NewModelBindingRecord(ModelBinding{HistoryID: scope, Route: "r", API: "a", Model: "m", Settings: []byte("{}")}, limits)
	if err != nil {
		t.Fatal(err)
	}
	bad, err := NewEffectiveInputRecord(EffectiveInput{RunID: run, Inputs: []InputItem{{UserInput: &input}, {UserInput: &UserInputID{RunID: run, Ordinal: 2}}}}, limits)
	if err != nil {
		t.Fatal(err)
	}
	// Construct an external accepted assertion independently of local preflight.
	c := history.Commit{Commit: historyformat.Commit{FormatVersion: 1, Parent: new(old.Head().CID), Sequence: old.Head().Sequence + 1, Records: []historyformat.Record{replacement.Format(), catalog.Format(), binding.Format(), bad.Format()}}}
	wire, err := codec.Encode(c.Commit, limits)
	if err != nil {
		t.Fatal(err)
	}
	c.Ref = history.CommitRef{Sequence: c.Commit.Sequence, CID: contentid.Sum(wire)}
	if err := s.Apply(c); !errors.Is(err, ErrTransition) {
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
	user, err := NewUserInputRecord(UserInput{ID: input}, limits)
	if err != nil {
		t.Fatal(err)
	}
	for _, record := range []Record{replacement, catalog, binding, bad, user} {
		for _, failure := range []string{"malformed", "version", "oversize"} {
			t.Run(record.value.Kind+"/"+failure, func(t *testing.T) {
				external := c.Commit.Clone()
				last := record.Format()
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
				if err := s.Apply(accepted); !errors.Is(err, wantErr) {
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
