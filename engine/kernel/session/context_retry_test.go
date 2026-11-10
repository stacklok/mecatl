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
	input, err := b.AddUserInput(run, session.UserInput{Parts: []session.ContentPart{{Text: new("/skill example\n")}}})
	mustBatch(t, err)
	instructions := session.Instructions{Blocks: []session.InstructionBlock{{Parts: []session.ContentPart{{Text: new("root")}}, Source: session.ContentSource{Description: "adopted", WorkspaceFile: &session.WorkspaceFileSource{Path: "AGENTS.md"}}}}}
	catalog := session.ToolCatalog{Tools: []session.ToolDefinition{{Name: "tool", ArgumentSchema: []byte("true")}}}
	binding := session.ModelBinding{Route: "r", API: "a", Model: "m", Settings: []byte(" { } ")}
	effective := session.EffectiveInput{Inputs: []session.InputItem{
		{UserInputID: new(input.RunRelative())},
		{Inline: &session.AttributedInput{Actor: "extension/example", Purpose: session.PromptContext, Parts: richContent(), Source: session.ContentSource{Description: "prepared reference", WorkspaceFile: &session.WorkspaceFileSource{Path: "docs/design.md"}}}},
	}}
	expected := effective.Clone()
	mustBatch(t, b.ReplaceInstructions(scope, instructions))
	mustBatch(t, b.ReplaceToolCatalog(scope, catalog))
	mustBatch(t, b.ReplaceModelBinding(scope, binding))
	mustBatch(t, b.AddEffectiveInput(run, effective))
	if ref, err := s.SubmitCommit(context.Background(), b); ref != (history.CommitRef{}) || !errors.Is(err, session.ErrAcceptanceUnknown) || !errors.Is(err, cause) {
		t.Fatalf("unknown: %v", err)
	}
	instructions.Blocks[0].Source.WorkspaceFile.Path = "mutated"
	clear(catalog.Tools[0].ArgumentSchema)
	clear(binding.Settings)
	*effective.Inputs[0].UserInputID = 0
	effective.Inputs[1].Inline.Actor = "changed"
	effective.Inputs[1].Inline.Parts[1].Media.InlineData[0] = 0
	*effective.Inputs[1].Inline.Parts[0].Text = "changed"
	*instructions.Blocks[0].Parts[0].Text = "changed"
	effective.Inputs[1].Inline.Source.WorkspaceFile.Path = "changed"
	if err := b.ReplaceInstructions(scope, instructions); !errors.Is(err, session.ErrBatch) {
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
	if !effectiveMatches(ev, expected) {
		t.Fatal("frozen retry retained caller mutations")
	}
	wantI := session.Instructions{Blocks: []session.InstructionBlock{{Parts: []session.ContentPart{{Text: new("root")}}, Source: session.ContentSource{Description: "adopted", WorkspaceFile: &session.WorkspaceFileSource{Path: "AGENTS.md"}}}}}
	wantT := session.ToolCatalog{Tools: []session.ToolDefinition{{Name: "tool", ArgumentSchema: []byte("true")}}}
	wantM := session.ModelBinding{Route: "r", API: "a", Model: "m", Settings: []byte(" { } ")}
	wantE := expected
	// Child handles captured independently retain their own immutable backing.
	inline := inlineAt(t, ev, 1)
	media := mediaAt(t, inline.Parts().At(1))
	file, present := inline.Source().WorkspaceFile()
	if !present {
		t.Fatal("missing pinned file label")
	}
	block, definition := iv.Blocks().At(0), tv.Tools().At(0)
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			for range 1000 {
				if inline.Actor() != "extension/example" || media.Title() != "Notes" || file.Path() != "docs/design.md" || block.Source().Description() != "adopted" || definition.Name() != "tool" {
					t.Error("pinned child mutated")
					return
				}
				if !instructionsMatch(iv, wantI) || !catalogMatches(tv, wantT) || !bindingMatches(mv, wantM) || !effectiveMatches(ev, wantE) || old.RecordCount() != 7 {
					t.Error("pinned context mutated")
					return
				}
			}
		})
	}
	for range 32 {
		b := beginBatch(t, s)
		mustBatch(t, b.ReplaceInstructions(scope, session.Instructions{}))
		mustBatch(t, b.ReplaceToolCatalog(scope, session.ToolCatalog{}))
		mustBatch(t, b.ReplaceModelBinding(scope, wantM))
		mustBatch(t, b.AddEffectiveInput(run, session.EffectiveInput{}))
		submitBatch(t, s, b)
	}
	wg.Wait()
}

func TestContextSoftAdmissionAndActualBulkReserve(t *testing.T) {
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
		mustBatch(t, b.ReplaceInstructions(s.Snapshot().HistoryID(), session.Instructions{}))
		if mixedCloseout {
			// A mixed context+bulk ending is ordinary resource admission, not cleanup.
			mustBatch(t, b.AddRunEnded(s.Snapshot().ActiveRun(), session.RunEnded{Reason: session.RunEndedCaptureFailed, BulkToolCloseout: true}))
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
			mustBatch(t, b.AddRunEnded(s.Snapshot().ActiveRun(), session.RunEnded{Reason: session.RunEndedCaptureFailed, BulkToolCloseout: true}))
			submitBatch(t, s, b)
			if s.Snapshot().RecordCount() != 4 || s.Snapshot().ActiveRun() != (session.RunID{}) {
				t.Fatal("reserved actual bulk ending did not fit")
			}
		}
	}
}
