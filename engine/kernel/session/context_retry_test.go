package session_test

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/history/mem"
	"github.com/stacklok/mecatl/engine/kernel/codec"
	"github.com/stacklok/mecatl/engine/kernel/history"
	"github.com/stacklok/mecatl/engine/kernel/session"
)

func TestContextFrozenRetryAndConcurrentPinnedReads(t *testing.T) {
	cause := errors.New("lost acknowledgment")
	backend := &batchBackend{Backend: mem.New(), fail: cause, accept: true}
	h := newLifecycleHistory(t, backend)
	p := sessionPolicy()
	p.PayloadLimits.MaxDepth = 16
	p.SoftMaxRetainedRecords = 256
	p.MaxRetainedRecords = 256
	s := boundSession(t, h, p)
	scope := s.Snapshot().HistoryID()
	b := beginBatch(t, s)
	mustBatch(t, b.Create())
	run := batchRun(t, b)
	input, err := b.AddInput(session.AddInputRequest{RunID: run, Parts: []session.ContentPart{{Kind: session.ContentText, Text: "/skill example\n"}}})
	mustBatch(t, err)
	instructions := session.Instructions{HistoryID: scope, Blocks: []session.InstructionBlock{{Content: []session.ContentPart{{Kind: session.ContentText, Text: "root"}}, Source: session.ContentSource{Description: "adopted", WorkspaceFile: &session.WorkspaceFileSource{Path: "AGENTS.md"}}}}}
	catalog := session.ToolCatalog{HistoryID: scope, Tools: []session.ToolDefinition{{Name: "tool", ArgumentSchema: []byte("true")}}}
	binding := session.ModelBinding{HistoryID: scope, Route: "r", API: "a", Model: "m", Settings: []byte(" { } ")}
	effective := session.EffectiveInput{RunID: run, Inputs: []session.InputItem{
		{UserInput: &input},
		{Inline: &session.AttributedInput{Actor: "extension/example", Purpose: session.PromptContext, Parts: richContent(), Source: session.ContentSource{Description: "prepared reference", WorkspaceFile: &session.WorkspaceFileSource{Path: "docs/design.md"}}}},
	}}
	expected := effective.Clone()
	mustBatch(t, b.ReplaceInstructions(instructions))
	mustBatch(t, b.ReplaceToolCatalog(catalog))
	mustBatch(t, b.ReplaceModelBinding(binding))
	mustBatch(t, b.AddEffectiveInput(effective))
	if ref, err := s.SubmitCommit(context.Background(), b); ref != (history.CommitRef{}) || !errors.Is(err, session.ErrAcceptanceUnknown) || !errors.Is(err, cause) {
		t.Fatalf("unknown: %v", err)
	}
	instructions.Blocks[0].Source.WorkspaceFile.Path = "mutated"
	clear(catalog.Tools[0].ArgumentSchema)
	clear(binding.Settings)
	*effective.Inputs[0].UserInput = session.InputID{}
	effective.Inputs[1].Inline.Actor = "changed"
	effective.Inputs[1].Inline.Parts[1].Media.Data[0] = 0
	effective.Inputs[1].Inline.Source.WorkspaceFile.Path = "changed"
	if err := b.ReplaceInstructions(instructions); !errors.Is(err, session.ErrBatch) {
		t.Fatalf("frozen write: %v", err)
	}
	submitBatch(t, s, b)
	if len(backend.attempts) != 2 || !reflect.DeepEqual(backend.attempts[0], backend.attempts[1]) {
		t.Fatal("retry changed exact proposal")
	}
	old := s.Snapshot()
	iv, _ := old.Instructions()
	tv, _ := old.ToolCatalog()
	mv, _ := old.ModelBinding()
	ev, _ := old.RecordAt(6).EffectiveInput()
	if !ev.Value().Equal(expected) {
		t.Fatal("frozen retry retained caller mutations")
	}
	wantI, wantT, wantM, wantE := iv.Value(), tv.Value(), mv.Value(), ev.Value()
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			for range 1000 {
				if !iv.Value().Equal(wantI) || !tv.Value().Equal(wantT) || !mv.Value().Equal(wantM) || !ev.Value().Equal(wantE) || old.RecordCount() != 7 {
					t.Error("pinned context mutated")
					return
				}
			}
		})
	}
	for range 32 {
		b := beginBatch(t, s)
		mustBatch(t, b.ReplaceInstructions(session.Instructions{HistoryID: scope}))
		mustBatch(t, b.ReplaceToolCatalog(session.ToolCatalog{HistoryID: scope}))
		mustBatch(t, b.ReplaceModelBinding(wantM))
		mustBatch(t, b.AddEffectiveInput(session.EffectiveInput{RunID: run}))
		submitBatch(t, s, b)
	}
	wg.Wait()
}

func TestContextSoftAdmissionAndActualInterruptionReserve(t *testing.T) {
	for _, mixedCloseout := range []bool{false, true} {
		p := sessionPolicy()
		p.SoftMaxRetainedRecords = 3
		p.MaxRetainedRecords = 4
		backend := &batchBackend{Backend: mem.New()}
		s := boundSession(t, newLifecycleHistory(t, backend), p)
		b := beginBatch(t, s)
		mustBatch(t, b.Create())
		batchRun(t, b)
		submitBatch(t, s, b)
		b = beginBatch(t, s)
		mustBatch(t, b.ReplaceInstructions(session.Instructions{HistoryID: s.Snapshot().HistoryID()}))
		if mixedCloseout {
			// A mixed context+interruption batch is ordinary admission, not cleanup.
			mustBatch(t, b.Interrupt())
			if _, err := s.SubmitCommit(context.Background(), b); !errors.Is(err, codec.ErrLimitExceeded) {
				t.Fatalf("mixed soft admission: %v", err)
			}
			if len(backend.attempts) != 1 || s.Snapshot().RecordCount() != 2 {
				t.Fatal("soft failure appended or published")
			}
		} else {
			submitBatch(t, s, b)
			// The selected context write leaves exactly the actual closeout record room.
			b = beginBatch(t, s)
			mustBatch(t, b.Interrupt())
			submitBatch(t, s, b)
			if s.Snapshot().RecordCount() != 4 || s.Snapshot().ActiveRun() != (session.RunID{}) {
				t.Fatal("reserved actual interruption did not fit")
			}
		}
	}
}
