package session

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/stacklok/mecatl/engine/kernel/codec"
	"github.com/stacklok/mecatl/engine/kernel/history"
)

func TestContextHardOnlyReplayDetachedOracle(t *testing.T) {
	s, _ := internalFixture(t)
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
	input, err := b.AddUserInput(AddUserInputRequest{RunID: run, Parts: []ContentPart{}})
	if err != nil {
		t.Fatal(err)
	}
	for _, err := range []error{
		b.ReplaceInstructions(Instructions{HistoryID: scope, Blocks: []InstructionBlock{{Source: ContentSource{Description: "retained root"}}}}),
		b.ReplaceToolCatalog(ToolCatalog{HistoryID: scope, Tools: []ToolDefinition{{Name: "tool", ArgumentSchema: []byte(" true ")}}}),
		b.ReplaceModelBinding(ModelBinding{HistoryID: scope, Route: "r", API: "a", Model: "m", Settings: []byte(" { } ")}),
		b.AddEffectiveInput(EffectiveInput{RunID: run, Inputs: []InputItem{{UserInputID: &input}}}),
	} {
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.SubmitCommit(context.Background(), b); err != nil {
		t.Fatal(err)
	}
	before := detachedSessionState(s.state)
	for _, dimension := range []string{"records", "bytes", "work"} {
		t.Run(dimension, func(t *testing.T) {
			p := s.policy
			switch dimension {
			case "records":
				p.SoftMaxRetainedRecords, p.MaxRetainedRecords = 1, s.state.records
			case "bytes":
				p.SoftMaxPayloadBytes, p.MaxPayloadBytes = 1, s.state.payloadBytes
			case "work":
				units := uint64(p.PayloadLimits.MaxBytes + p.PayloadLimits.MaxNodes + p.PayloadLimits.MaxCollectionElements)
				p.SoftMaxDecodedWork, p.MaxDecodedWork = 1, uint64(s.state.records)*units
			}
			loaded, err := Load(context.Background(), s.history, scope, p)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(before, detachedSessionState(loaded.state)) || loaded.Snapshot().ActiveRun() != run {
				t.Fatal("hard-only replay changed complete state/counters/context")
			}
			// Replay must not demand future interruption room. At this exact hard
			// boundary the active run can be inspected but cannot append closeout.
			b, err := loaded.BeginCommit()
			if err != nil {
				t.Fatal(err)
			}
			if err := b.EndRun(RunEndedInterrupted); err != nil {
				t.Fatal(err)
			}
			if ref, err := loaded.SubmitCommit(context.Background(), b); ref != (history.CommitRef{}) || !errors.Is(err, codec.ErrLimitExceeded) || errors.Is(err, ErrAcceptanceUnknown) {
				t.Fatalf("hard-full closeout: %v, %v", ref, err)
			}
			if !reflect.DeepEqual(before, detachedSessionState(loaded.state)) || !reflect.DeepEqual(before, detachedSessionState(s.state)) {
				t.Fatal("hard rejection changed complete loaded/original state")
			}
			if head, err := s.history.Head(context.Background(), scope); err != nil || head != before.view.head {
				t.Fatal("replay or rejected cleanup appended")
			}
		})
	}
}

func TestValueBudgetMachineIntBoundaries(t *testing.T) {
	maxInt := int(^uint(0) >> 1)
	limits := codec.Limits{MaxBytes: maxInt, MaxNodes: maxInt, MaxCollectionElements: maxInt, MaxArrayElements: maxInt, MaxMapPairs: maxInt, MaxDepth: maxInt, MaxByteStringBytes: maxInt, MaxStringBytes: maxInt}
	for _, pointers := range []bool{false, true} {
		b := newValueBudget(limits)
		b.collection(maxInt / 9)
		b.bytes(maxInt % 9)
		if b.err != nil {
			t.Fatal("exact lower-bound estimate rejected", b.err)
		}
		if pointers {
			b.pointer()
		} else {
			b.collection(1)
		}
		if !errors.Is(b.err, codec.ErrLimitExceeded) {
			t.Fatal("overflowing estimate admitted")
		}
	}
	b := newValueBudget(limits)
	b.collection(maxInt)
	if !errors.Is(b.err, codec.ErrLimitExceeded) || b.nodes != 0 || b.elements != 0 {
		t.Fatal("extreme count was converted before rejection")
	}
}
