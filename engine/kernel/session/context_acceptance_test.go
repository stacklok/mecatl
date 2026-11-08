package session_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/history/mem"
	"github.com/stacklok/mecatl/engine/kernel/history"
	"github.com/stacklok/mecatl/engine/kernel/session"
)

func TestContextIndependentReplacementsAndFixtureFreeLoad(t *testing.T) {
	backend := mem.New()
	h := newLifecycleHistory(t, backend)
	p := sessionPolicy()
	p.PayloadLimits.MaxDepth = 16
	p.PayloadLimits.MaxCollectionElements = 512
	p.PayloadLimits.MaxNodes = 2048
	s := boundSession(t, h, p)
	scope := s.Snapshot().HistoryID()
	dir := t.TempDir()
	paths := []string{"skill.md", "AGENTS.md", "settings.json"}
	prepared := []string{"expanded skill\n", "adopted root\n", " {\"z\":1.00, \"a\":false} "}
	for i, path := range paths {
		mustBatch(t, os.WriteFile(filepath.Join(dir, path), []byte(prepared[i]), 0600))
		data, err := os.ReadFile(filepath.Join(dir, path))
		mustBatch(t, err)
		prepared[i] = string(data)
	}
	instructions := session.Instructions{Blocks: []session.InstructionBlock{{Parts: []session.ContentPart{{Text: new(prepared[1])}}, Source: session.ContentSource{Description: "caller adopted root", WorkspaceFile: &session.WorkspaceFileSource{Path: paths[1]}}}}}
	catalog := session.ToolCatalog{Tools: []session.ToolDefinition{{Name: "second", ArgumentSchema: []byte(" {\"z\":1.00, \"a\":true} ")}, {Name: "first", ArgumentSchema: []byte("false")}}}
	binding := session.ModelBinding{Route: "route", API: "dialect/v1", Model: "alias", Settings: []byte(prepared[2])}
	b := beginBatch(t, s)
	mustBatch(t, b.Create())
	run := batchRun(t, b)
	input, err := b.AddUserInput(run, session.UserInput{Parts: richContent()})
	mustBatch(t, err)
	effective := session.EffectiveInput{Inputs: []session.InputItem{{UserInputID: new(input.RunRelative())}, {Inline: &session.AttributedInput{Actor: "skill/example", Purpose: session.PromptInstructions, Parts: []session.ContentPart{{Text: new(prepared[0])}}, Source: session.ContentSource{Description: "caller expanded skill", WorkspaceFile: &session.WorkspaceFileSource{Path: paths[0]}}}}}}
	mustBatch(t, b.ReplaceInstructions(scope, instructions))
	mustBatch(t, b.ReplaceToolCatalog(scope, catalog))
	mustBatch(t, b.ReplaceModelBinding(scope, binding))
	mustBatch(t, b.AddEffectiveInput(run, effective))
	submitBatch(t, s, b)
	snapshots := []session.Snapshot{s.Snapshot()}
	wantI := []session.Instructions{instructions.Clone()}
	wantT := []session.ToolCatalog{catalog.Clone()}
	wantM := []session.ModelBinding{binding.Clone()}
	authored := snapshots[0].RecordAt(2)

	// Each component changes independently. Multiple replacements in one commit
	// retain chronology while the last fact selects the current component.
	for component := range 3 {
		previous := s.Snapshot()
		iv, _ := previous.Instructions()
		tv, _ := previous.ToolCatalog()
		mv, _ := previous.ModelBinding()
		b = beginBatch(t, s)
		switch component {
		case 0:
			mustBatch(t, b.ReplaceInstructions(scope, instructions))
			instructions = session.Instructions{}
			mustBatch(t, b.ReplaceInstructions(scope, instructions))
			// Two increments preserve their separate boundaries, including
			// one inline item with empty parts versus zero items.
			emptyPrompt := session.EffectiveInput{Inputs: []session.InputItem{{Inline: &session.AttributedInput{Actor: "harness", Purpose: session.PromptContext, Source: session.ContentSource{Description: "explicit empty context"}}}}}
			mustBatch(t, b.AddEffectiveInput(run, emptyPrompt))
			mustBatch(t, b.AddEffectiveInput(run, session.EffectiveInput{}))
		case 1:
			mustBatch(t, b.ReplaceToolCatalog(scope, catalog))
			catalog = session.ToolCatalog{}
			mustBatch(t, b.ReplaceToolCatalog(scope, catalog))
		case 2:
			mustBatch(t, b.ReplaceModelBinding(scope, binding))
			binding.Settings = []byte("{}")
			mustBatch(t, b.ReplaceModelBinding(scope, binding))
		}
		submitBatch(t, s, b)
		next := s.Snapshot()
		ni, _ := next.Instructions()
		nt, _ := next.ToolCatalog()
		nm, _ := next.ModelBinding()
		if (component != 0 && ni != iv) || (component != 1 && nt != tv) || (component != 2 && nm != mv) || next.RecordAt(2) != authored {
			t.Fatal("independent replacement copied or rewrote unchanged facts")
		}
		wantCount := previous.RecordCount() + 2
		if component == 0 {
			wantCount += 2
		}
		if next.RecordCount() != wantCount {
			t.Fatal("same-commit replacement chronology was coalesced")
		}
		snapshots = append(snapshots, next)
		wantI = append(wantI, instructions.Clone())
		wantT = append(wantT, catalog.Clone())
		wantM = append(wantM, binding.Clone())
	}
	oneEmpty, ok := snapshots[1].RecordAt(9).EffectiveInput()
	noPrompts, okEmpty := snapshots[1].RecordAt(10).EffectiveInput()
	if !ok || !okEmpty || oneEmpty.InputCount() != 1 || inlineAt(t, oneEmpty, 0).Parts().Len() != 0 || noPrompts.InputCount() != 0 || oneEmpty.RunID() != run || noPrompts.RunID() != run {
		t.Fatal("same-commit effective revisions collapsed")
	}

	// Mutate every mutable export at every captured head, then recheck all heads.
	// In particular, exports from the newest snapshot cannot corrupt old handles.
	for _, snapshot := range snapshots {
		tv, _ := snapshot.ToolCatalog()
		if tv.Tools().Len() != 0 {
			clear(tv.Tools().At(0).ArgumentSchema().Copy())
		}
		mv, _ := snapshot.ModelBinding()
		clear(mv.Settings().Copy())
		uv, _ := snapshot.RecordAt(2).UserInput()
		mutateMediaBytes(t, uv.Parts().At(1))
		clear(snapshot.RecordAt(6).Format().Payload)
	}
	for i, snapshot := range snapshots {
		iv, _ := snapshot.Instructions()
		tv, _ := snapshot.ToolCatalog()
		mv, _ := snapshot.ModelBinding()
		uv, _ := snapshot.RecordAt(2).UserInput()
		ev, _ := snapshot.RecordAt(6).EffectiveInput()
		if !instructionsMatch(iv, wantI[i]) || !catalogMatches(tv, wantT[i]) || !bindingMatches(mv, wantM[i]) || !userInputMatches(uv, session.UserInput{Parts: richContent()}) || !effectiveMatches(ev, effective) {
			t.Fatalf("snapshot %d changed after export mutation", i)
		}
	}

	// A fresh Load has only read-only history access. The caller's contributing
	// files have changed or disappeared; their retained labels are not resolvers.
	mustBatch(t, os.WriteFile(filepath.Join(dir, paths[0]), []byte("different expansion"), 0600))
	changed, err := os.ReadFile(filepath.Join(dir, paths[0]))
	mustBatch(t, err)
	if string(changed) == prepared[0] {
		t.Fatal("fixture negative control did not change")
	}
	for _, path := range paths[1:] {
		mustBatch(t, os.Remove(filepath.Join(dir, path)))
		if _, err := os.ReadFile(filepath.Join(dir, path)); !os.IsNotExist(err) {
			t.Fatal("fixture deletion negative control")
		}
	}
	readOnly := newLifecycleHistory(t, readOnlyHistoryBackend{Backend: backend, t: t})
	loaded, err := session.Load(context.Background(), readOnly, scope, p)
	mustBatch(t, err)
	if !reflect.DeepEqual(loaded.Snapshot(), s.Snapshot()) || loaded.Snapshot().ActiveRun() != run {
		t.Fatal("fresh load re-resolved content or changed retained history")
	}
	assertHistoryHead(t, h, s.Snapshot().Head())
}

func TestContextIndependentReplacementDoesNotMergeStaleHead(t *testing.T) {
	h := newLifecycleHistory(t, mem.New())
	s := boundSession(t, h, sessionPolicy())
	b := beginBatch(t, s)
	mustBatch(t, b.Create())
	batchRun(t, b)
	submitBatch(t, s, b)
	scope := s.Snapshot().HistoryID()
	other, err := session.Load(context.Background(), h, scope, sessionPolicy())
	mustBatch(t, err)
	before := captureRejectedSnapshot(other.Snapshot())
	winner := beginBatch(t, s)
	instructions := session.Instructions{Blocks: []session.InstructionBlock{{Source: session.ContentSource{Description: "winning replacement"}}}}
	mustBatch(t, winner.ReplaceInstructions(scope, instructions))
	loser := beginBatch(t, other)
	mustBatch(t, loser.ReplaceToolCatalog(scope, session.ToolCatalog{}))
	head := submitBatch(t, s, winner)
	if ref, err := other.SubmitCommit(context.Background(), loser); ref != (history.CommitRef{}) || !errors.Is(err, session.ErrConflict) || errors.Is(err, session.ErrAcceptanceUnknown) {
		t.Fatalf("stale independent replacement: %v, %v", ref, err)
	}
	if !reflect.DeepEqual(before, captureRejectedSnapshot(other.Snapshot())) {
		t.Fatal("conflicting catalog published partial state")
	}
	if _, err := other.BeginCommit(); !errors.Is(err, session.ErrReloadRequired) {
		t.Fatalf("stale owner reused without Load: %v", err)
	}
	loaded, err := session.Load(context.Background(), h, scope, sessionPolicy())
	mustBatch(t, err)
	iv, ok := loaded.Snapshot().Instructions()
	if !ok || !instructionsMatch(iv, instructions) || loaded.Snapshot().Head() != head {
		t.Fatal("fresh load lost the winning independent component")
	}
	if _, present := loaded.Snapshot().ToolCatalog(); present {
		t.Fatal("stale explicit-empty catalog was merged despite conflict")
	}
}
