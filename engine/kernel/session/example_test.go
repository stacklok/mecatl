package session_test

import (
	"context"
	"fmt"

	"github.com/stacklok/mecatl/engine/adapter/history/mem"
	"github.com/stacklok/mecatl/engine/kernel/codec"
	"github.com/stacklok/mecatl/engine/kernel/history"
	historyid "github.com/stacklok/mecatl/engine/kernel/history/id"
	"github.com/stacklok/mecatl/engine/kernel/session"
)

func ExampleSession() {
	ctx := context.Background()
	limits := codec.Limits{MaxBytes: 4096, MaxStringBytes: 128, MaxByteStringBytes: 1024, MaxArrayElements: 16, MaxMapPairs: 16, MaxDepth: 8, MaxNodes: 512, MaxCollectionElements: 128}
	policy := session.Policy{CommitLimits: limits, PayloadLimits: limits, MaxRecordsPerCommit: 16, SoftMaxRetainedRecords: 128, SoftMaxPayloadBytes: 32768, SoftMaxDecodedWork: 1 << 24, MaxRetainedRecords: 128, MaxPayloadBytes: 32768, MaxDecodedWork: 1 << 24}
	h, err := history.New(mem.New(), history.Policy{CommitLimits: limits, RetryWindow: 16, MaxPageBytes: limits.MaxBytes, MaxPageCommits: 2})
	if err != nil {
		fmt.Println(err)
		return
	}
	id, err := historyid.ParseHistoryID("HST1111111111111111111111")
	if err != nil {
		fmt.Println(err)
		return
	}
	s, err := session.New(h, id, policy) // No I/O or implicit creation.
	if err != nil {
		fmt.Println(err)
		return
	}
	b, err := s.BeginCommit()
	if err != nil {
		fmt.Println(err)
		return
	}
	defer b.Discard()
	if err := b.Create(); err != nil {
		fmt.Println(err)
		return
	}
	run, err := b.StartRun()
	if err != nil {
		fmt.Println(err)
		return
	}
	input, err := b.AddInput(session.AddInputRequest{RunID: run, Parts: []session.ContentPart{{Kind: session.ContentText, Text: "first"}}})
	if err != nil {
		fmt.Println(err)
		return
	}
	// Preparation supplies retained values; the kernel never opens this source file.
	if err := b.ReplaceInstructions(session.Instructions{HistoryID: id, Blocks: []session.InstructionBlock{{
		Content: []session.ContentPart{{Kind: session.ContentText, Text: "Use the project's vocabulary."}},
		Source:  session.ContentSource{Description: "adopted workspace instructions", WorkspaceFile: &session.WorkspaceFileSource{Path: "AGENTS.md"}},
	}}}); err != nil {
		fmt.Println(err)
		return
	}
	if err := b.AddEffectiveInput(session.EffectiveInput{RunID: run, Inputs: []session.InputItem{
		{UserInput: &input},
		{Inline: &session.AttributedInput{
			Actor:   "harness",
			Purpose: session.PromptContext,
			Parts:   []session.ContentPart{{Kind: session.ContentText, Text: "Retained reference material."}},
			Source:  session.ContentSource{Description: "prepared workspace reference", WorkspaceFile: &session.WorkspaceFileSource{Path: "docs/design.md"}},
		}},
	}}); err != nil {
		fmt.Println(err)
		return
	}
	fmt.Printf("provisional input=%d run=%d; created=%t\n", input.Ordinal, run.Ordinal, s.Snapshot().Created())
	if _, err := s.SubmitCommit(ctx, b); err != nil {
		// Unknown acceptance freezes the batch. Explicitly retry that same handle
		// with a fresh context, or abandon this object and Load; never replace it.
		fmt.Println(err)
		return
	}
	old := s.Snapshot()
	fmt.Printf("accepted head=%d active=%d\n", old.Head().Sequence, old.ActiveRun().Ordinal)
	loaded, err := session.Load(ctx, h, id, policy)
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Printf("loaded active=%d inputs=%d\n", loaded.Snapshot().ActiveRun().Ordinal, loaded.Snapshot().RunAt(0).InputCount())
	instructions, _ := loaded.Snapshot().Instructions()
	fmt.Printf("instruction source=%s\n", instructions.Value().Blocks[0].Source.WorkspaceFile.Path)
	effective, _ := loaded.Snapshot().RecordAt(4).EffectiveInput()
	fmt.Printf("prepared purpose=%s; original=%q\n", effective.Value().Inputs[1].Inline.Purpose, loaded.Snapshot().RunAt(0).InputAt(0).Parts()[0].Text)

	// Closing a run is explicit, never a side effect of loading it.
	closeout, err := loaded.BeginCommit()
	if err != nil {
		fmt.Println(err)
		return
	}
	defer closeout.Discard()
	if err := closeout.Interrupt(); err != nil {
		fmt.Println(err)
		return
	}
	if _, err := loaded.SubmitCommit(ctx, closeout); err != nil {
		fmt.Println(err)
		return
	}
	v := loaded.Snapshot()
	fmt.Printf("interrupted head=%d idle=%t\n", v.Head().Sequence, v.ActiveRun() == (session.RunID{}))
	next, err := loaded.BeginCommit()
	if err != nil {
		fmt.Println(err)
		return
	}
	defer next.Discard()
	successor, err := next.StartRun()
	if err != nil {
		fmt.Println(err)
		return
	}
	second, err := next.AddInput(session.AddInputRequest{RunID: successor, Parts: []session.ContentPart{{Kind: session.ContentText, Text: "second"}}})
	if err != nil {
		fmt.Println(err)
		return
	}
	if _, err := loaded.SubmitCommit(ctx, next); err != nil {
		fmt.Println(err)
		return
	}
	fmt.Printf("successor input=%d run=%d\n", second.Ordinal, successor.Ordinal)
	loaded, err = session.Load(ctx, h, id, policy)
	if err != nil {
		fmt.Println(err)
		return
	}
	v = loaded.Snapshot()
	fmt.Printf("loaded %s head=%d runs=%d active=%d\n", v.HistoryID(), v.Head().Sequence, v.RunCount(), v.ActiveRun().Ordinal)
	owner := v.RunAt(1)
	fmt.Printf("run %d owns input %d: %s\n", owner.ID().Ordinal, owner.InputAt(0).ID().Ordinal, owner.InputAt(0).Parts()[0].Text)
	fmt.Printf("old snapshot head=%d active=%d\n", old.Head().Sequence, old.ActiveRun().Ordinal)
	// Output:
	// provisional input=1 run=1; created=false
	// accepted head=1 active=1
	// loaded active=1 inputs=1
	// instruction source=AGENTS.md
	// prepared purpose=context; original="first"
	// interrupted head=2 idle=true
	// successor input=1 run=2
	// loaded HST1111111111111111111111 head=3 runs=2 active=2
	// run 2 owns input 1: second
	// old snapshot head=1 active=1
}
