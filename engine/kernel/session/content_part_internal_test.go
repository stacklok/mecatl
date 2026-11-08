package session

import (
	"errors"
	"reflect"
	"testing"

	"github.com/stacklok/mecatl/engine/kernel/codec"
	sessionformat "github.com/stacklok/mecatl/engine/kernel/session/format"
)

func TestContentTextRecordOwnership(t *testing.T) {
	for _, text := range []string{"", " exact e\u0301\r\n尾 "} {
		t.Run(text, func(t *testing.T) {
			s, limits := internalFixture(t)
			scope := s.Snapshot().HistoryID()
			run := RunID{historyID: scope, ordinal: 1}
			input := UserInputID{runID: run, ordinal: 1}
			callerText := text
			parts := []ContentPart{{Text: &callerText}}
			source := ContentSource{Description: "retained"}
			created, err := newCreatedRecord(Created{HistoryID: scope}, limits)
			if err != nil {
				t.Fatal(err)
			}
			started, err := newRunStartedRecord(RunStarted{ID: run}, limits)
			if err != nil {
				t.Fatal(err)
			}
			authored, err := makeInputRecord(identifiedInput{ID: input, Parts: parts}, limits)
			if err != nil {
				t.Fatal(err)
			}
			instructions, err := newInstructionsRecord(Instructions{HistoryID: scope, Blocks: []InstructionBlock{{Parts: parts, Source: source}}}, limits)
			if err != nil {
				t.Fatal(err)
			}
			effective, err := newEffectiveInputRecord(EffectiveInput{RunID: run, Inputs: []InputItem{{Inline: &AttributedInput{Actor: "harness", Purpose: PromptContext, Parts: parts, Source: source}}}}, limits)
			if err != nil {
				t.Fatal(err)
			}
			callerText = "mutated after record construction"
			if effective.effective.Inputs[0].Inline.Parts[0].Text == &callerText || *effective.effective.Inputs[0].Inline.Parts[0].Text != text {
				t.Fatal("record backing retained caller text pointer")
			}
			internalApply(t, s, created, started, authored, instructions, effective)
			view := s.Snapshot()
			inputView, _ := view.UserInputByID(input)
			instructionView, _ := view.Instructions()
			effectiveView, _ := view.RecordAt(4).EffectiveInput()
			inline, _ := effectiveView.InputAt(0).Inline()
			for _, part := range []ContentPartView{inputView.PartAt(0), instructionView.BlockAt(0).PartAt(0), inline.PartAt(0)} {
				if got, ok := part.Text(); !ok || got != text {
					t.Fatalf("record/admission text = %q, %t; want %q", got, ok, text)
				}
			}
			export, err := codec.Decode[sessionformat.EffectiveInput](view.RecordAt(4).Format().Payload, limits)
			if err != nil {
				t.Fatal(err)
			}
			clone := export.Clone()
			*export.Inputs[0].Inline.Parts[0].Text = "mutated export"
			if *clone.Inputs[0].Inline.Parts[0].Text != text {
				t.Fatal("replay export clone shared text pointer")
			}
			if got, _ := inline.PartAt(0).Text(); got != text {
				t.Fatal("replay export changed pinned text")
			}
		})
	}
}

func TestContentTextBudgetBeforeConversion(t *testing.T) {
	s, limits := internalFixture(t)
	id := UserInputID{runID: RunID{historyID: s.Snapshot().HistoryID(), ordinal: 1}, ordinal: 1}
	input := identifiedInput{ID: id, Parts: make([]ContentPart, 8)}
	for i := range input.Parts {
		input.Parts[i].Text = new("")
	}
	if _, err := makeInputRecord(input, limits); err != nil {
		t.Fatalf("positive control: %v", err)
	}
	for _, axis := range []string{"nodes", "bytes"} {
		t.Run(axis, func(t *testing.T) {
			bounded := limits
			if axis == "nodes" {
				bounded.MaxNodes = len(input.Parts)
			} else {
				bounded.MaxBytes = 9 * len(input.Parts)
			}
			var record *materializedRecord
			var err error
			if n := testing.AllocsPerRun(10, func() {
				record, err = makeInputRecord(input, bounded)
			}); n != 0 {
				t.Fatalf("text-pointer rejection allocated %g times", n)
			}
			if !errors.Is(err, codec.ErrLimitExceeded) || record != nil {
				t.Fatalf("bounded failure: %#v, %v", record, err)
			}
			// Bypassing preflight still rejects in bounded codec traversal.
			if n := testing.AllocsPerRun(10, func() {
				_, err = codec.Encode(input.toFormat(), bounded)
			}); n == 0 || !errors.Is(err, codec.ErrLimitExceeded) {
				t.Fatalf("late-rejection control: %g allocations, %v", n, err)
			}
		})
	}
}

func TestContentBranchAdmissionIsAtomic(t *testing.T) {
	s, _ := internalFixture(t)
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
	before := s.Snapshot()
	for _, part := range []ContentPart{{}, {Text: new(""), Media: &Media{Kind: MediaKindImage, Source: MediaSourceRemote, RemoteURI: "urn:a"}}} {
		parts := []ContentPart{part}
		count := len(b.records)
		if id, err := b.AddUserInput(UserInput{RunID: run, Parts: parts}); !errors.Is(err, codec.ErrInvalidData) || id != (UserInputID{}) {
			t.Fatalf("invalid user content: %v, %v", id, err)
		}
		if err := b.ReplaceInstructions(Instructions{HistoryID: run.historyID, Blocks: []InstructionBlock{{Parts: parts, Source: ContentSource{Description: "source"}}}}); !errors.Is(err, codec.ErrInvalidData) {
			t.Fatalf("invalid instructions: %v", err)
		}
		if err := b.AddEffectiveInput(EffectiveInput{RunID: run, Inputs: []InputItem{{Inline: &AttributedInput{Actor: "harness", Purpose: PromptContext, Parts: parts, Source: ContentSource{Description: "source"}}}}}); !errors.Is(err, codec.ErrInvalidData) {
			t.Fatalf("invalid effective input: %v", err)
		}
		if len(b.records) != count || !reflect.DeepEqual(before, s.Snapshot()) {
			t.Fatal("invalid branches changed staged records or accepted state")
		}
	}
	id, err := b.AddUserInput(UserInput{RunID: run, Parts: []ContentPart{{Text: new("")}}})
	if err != nil || id.ordinal != 1 {
		t.Fatalf("rejection consumed input ordinal: %v, %v", id, err)
	}
	if err := b.Discard(); err != nil {
		t.Fatal(err)
	}
}
