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
	input, err := b.AddUserInput(session.UserInput{RunID: run, Parts: []session.ContentPart{{Text: new("first")}}})
	if err != nil {
		fmt.Println(err)
		return
	}
	// Preparation supplies retained values; the kernel never opens this source file.
	if err := b.ReplaceInstructions(session.Instructions{HistoryID: id, Blocks: []session.InstructionBlock{{
		Parts:  []session.ContentPart{{Text: new("Use the project's vocabulary.")}},
		Source: session.ContentSource{Description: "adopted workspace instructions", WorkspaceFile: &session.WorkspaceFileSource{Path: "AGENTS.md"}},
	}}}); err != nil {
		fmt.Println(err)
		return
	}
	if err := b.AddEffectiveInput(session.EffectiveInput{RunID: run, Inputs: []session.InputItem{
		{UserInputID: &input},
		{Inline: &session.AttributedInput{
			Actor:   "harness",
			Purpose: session.PromptContext,
			Parts:   []session.ContentPart{{Text: new("Retained reference material.")}},
			Source:  session.ContentSource{Description: "prepared workspace reference", WorkspaceFile: &session.WorkspaceFileSource{Path: "docs/design.md"}},
		}},
	}}); err != nil {
		fmt.Println(err)
		return
	}
	fmt.Printf("provisional owner matches=%t; created=%t\n", input.RunID() == run, s.Snapshot().Created())
	if _, err := s.SubmitCommit(ctx, b); err != nil {
		// Unknown acceptance freezes the batch. Explicitly retry that same handle
		// with a fresh context, or abandon this object and Load; never replace it.
		fmt.Println(err)
		return
	}
	old := s.Snapshot()
	fmt.Printf("accepted head=%d active matches=%t\n", old.Head().Sequence, old.ActiveRun() == run)
	loaded, err := session.Load(ctx, h, id, policy)
	if err != nil {
		fmt.Println(err)
		return
	}
	loadedView := loaded.Snapshot()
	inputs := 0
	for i := 0; i < loadedView.RecordCount(); i++ {
		if _, ok := loadedView.RecordAt(i).UserInput(); ok {
			inputs++
		}
	}
	original, ok := loadedView.UserInputByID(input)
	if !ok {
		fmt.Println("missing accepted input")
		return
	}
	fmt.Printf("loaded active matches=%t inputs=%d\n", loadedView.ActiveRun() == run, inputs)
	instructions, _ := loaded.Snapshot().Instructions()
	file, _ := instructions.BlockAt(0).Source().WorkspaceFile()
	fmt.Printf("instruction source=%s\n", file.Path())
	effective, _ := loaded.Snapshot().RecordAt(4).EffectiveInput()
	inline, _ := effective.InputAt(1).Inline()
	text, _ := original.PartAt(0).Text()
	fmt.Printf("prepared purpose=%s; original=%q\n", inline.Purpose(), text)

	// Closing a run is explicit, never a side effect of loading it.
	closeout, err := loaded.BeginCommit()
	if err != nil {
		fmt.Println(err)
		return
	}
	defer closeout.Discard()
	if err := closeout.EndRun(session.RunEndedInterrupted); err != nil {
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
	second, err := next.AddUserInput(session.UserInput{RunID: successor, Parts: []session.ContentPart{{Text: new("second")}}})
	if err != nil {
		fmt.Println(err)
		return
	}
	if _, err := loaded.SubmitCommit(ctx, next); err != nil {
		fmt.Println(err)
		return
	}
	fmt.Printf("successor owner matches=%t; distinct run=%t\n", second.RunID() == successor, successor != run)
	loaded, err = session.Load(ctx, h, id, policy)
	if err != nil {
		fmt.Println(err)
		return
	}
	v = loaded.Snapshot()
	fmt.Printf("loaded %s head=%d runs=%d active matches=%t\n", v.HistoryID(), v.Head().Sequence, v.RunCount(), v.ActiveRun() == successor)
	accepted, ok := v.UserInputByID(second)
	if !ok {
		fmt.Println("missing successor input")
		return
	}
	text, _ = accepted.PartAt(0).Text()
	fmt.Printf("successor owns input=%t: %s\n", accepted.ID().RunID() == successor, text)
	fmt.Printf("old snapshot head=%d active matches=%t\n", old.Head().Sequence, old.ActiveRun() == run)
	// Output:
	// provisional owner matches=true; created=false
	// accepted head=1 active matches=true
	// loaded active matches=true inputs=1
	// instruction source=AGENTS.md
	// prepared purpose=context; original="first"
	// interrupted head=2 idle=true
	// successor owner matches=true; distinct run=true
	// loaded HST1111111111111111111111 head=3 runs=2 active matches=true
	// successor owns input=true: second
	// old snapshot head=1 active matches=true
}
