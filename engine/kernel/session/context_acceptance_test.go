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
	instructions := session.Instructions{HistoryID: scope, Blocks: []session.InstructionBlock{{Content: session.Content{{Kind: session.ContentText, Text: prepared[1]}}, Source: session.ContentSource{Description: "caller adopted root", WorkspaceFile: &session.WorkspaceFileSource{Path: paths[1]}}}}}
	catalog := session.ToolCatalog{HistoryID: scope, Tools: []session.ToolDefinition{{Name: "second", ArgumentSchema: []byte(" {\"z\":1.00, \"a\":true} ")}, {Name: "first", ArgumentSchema: []byte("false")}}}
	binding := session.ModelBinding{HistoryID: scope, Route: "route", API: "dialect/v1", Model: "alias", Settings: []byte(prepared[2])}
	b := beginBatch(t, s)
	mustBatch(t, b.Create())
	run := batchRun(t, b)
	input, err := b.AddInput(session.AddInputRequest{RunID: run, Messages: []session.AuthoredMessage{{Content: richContent()}, {}}})
	mustBatch(t, err)
	effective := session.EffectiveInput{InputID: input, Prompts: []session.PreparedPrompt{{AuthoredMessage: new(uint64(1))}, {Harness: &session.HarnessPrompt{Purpose: session.PromptInstructions, Content: session.Content{{Kind: session.ContentText, Text: prepared[0]}}, Source: session.ContentSource{Description: "caller expanded skill", WorkspaceFile: &session.WorkspaceFileSource{Path: paths[0]}}}}}}
	mustBatch(t, b.ReplaceInstructions(instructions))
	mustBatch(t, b.ReplaceToolCatalog(catalog))
	mustBatch(t, b.ReplaceModelBinding(binding))
	mustBatch(t, b.AddEffectiveInput(effective))
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
			mustBatch(t, b.ReplaceInstructions(instructions))
			instructions = session.Instructions{HistoryID: scope}
			mustBatch(t, b.ReplaceInstructions(instructions))
			// Two complete revisions preserve their separate boundaries, including
			// one harness prompt with empty content versus zero prompts.
			emptyPrompt := session.EffectiveInput{InputID: input, Prompts: []session.PreparedPrompt{{Harness: &session.HarnessPrompt{Purpose: session.PromptContext, Source: session.ContentSource{Description: "explicit empty context"}}}}}
			mustBatch(t, b.AddEffectiveInput(emptyPrompt))
			mustBatch(t, b.AddEffectiveInput(session.EffectiveInput{InputID: input}))
		case 1:
			mustBatch(t, b.ReplaceToolCatalog(catalog))
			catalog = session.ToolCatalog{HistoryID: scope}
			mustBatch(t, b.ReplaceToolCatalog(catalog))
		case 2:
			mustBatch(t, b.ReplaceModelBinding(binding))
			binding.Settings = []byte("{}")
			mustBatch(t, b.ReplaceModelBinding(binding))
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
	if !ok || !okEmpty || len(oneEmpty.Value().Prompts) != 1 || len(oneEmpty.Value().Prompts[0].Harness.Content) != 0 || len(noPrompts.Value().Prompts) != 0 || oneEmpty.InputID() != input || noPrompts.InputID() != input {
		t.Fatal("same-commit effective revisions collapsed")
	}

	// Mutate every mutable export at every captured head, then recheck all heads.
	// In particular, exports from the newest snapshot cannot corrupt old handles.
	for _, snapshot := range snapshots {
		iv, _ := snapshot.Instructions()
		if len(iv.Value().Blocks) != 0 {
			iv.Value().Blocks[0].Content[0].Text = "export changed"
			iv.Format().Blocks[0].Source.WorkspaceFile.Path = "changed"
		}
		tv, _ := snapshot.ToolCatalog()
		if len(tv.Value().Tools) != 0 {
			clear(tv.Value().Tools[0].ArgumentSchema)
			clear(tv.Format().Tools[0].ArgumentSchema)
		}
		mv, _ := snapshot.ModelBinding()
		clear(mv.Value().Settings)
		clear(mv.Format().Settings)
		uv, _ := snapshot.RecordAt(2).UserInput()
		uv.Messages()[0].Content[1].Media.Source.Inline.Data[0] = 0
		uv.Messages()[0].Content[2].Media.Source.Remote.URI = "urn:changed"
		uv.Messages()[0].Content[3].Media.Source.Provider.ID = "changed"
		uv.Messages()[0].Content[5].Media.Source.Inline.Data = []byte("changed")
		uv.Format().Messages[0].Content[1].Media.Source.Inline.Data[0] = 0
		uv.Format().Messages[0].Content[2].Media.Source.Remote.URI = "urn:changed"
		uv.Format().Messages[0].Content[3].Media.Source.Provider.Scope = "changed"
		uv.Format().Messages[0].Content[5].Media.Source.Inline.Data = []byte("changed")
		ev, _ := snapshot.RecordAt(6).EffectiveInput()
		*ev.Value().Prompts[0].AuthoredMessage = 99
		ev.Format().Prompts[1].Harness.Content[0].Text = "changed"
	}
	for i, snapshot := range snapshots {
		iv, _ := snapshot.Instructions()
		tv, _ := snapshot.ToolCatalog()
		mv, _ := snapshot.ModelBinding()
		uv, _ := snapshot.RecordAt(2).UserInput()
		ev, _ := snapshot.RecordAt(6).EffectiveInput()
		if !iv.Value().Equal(wantI[i]) || !tv.Value().Equal(wantT[i]) || !mv.Value().Equal(wantM[i]) || !uv.Messages()[0].Content.Equal(richContent()) || len(uv.Messages()) != 2 || !ev.Value().Equal(effective) {
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
	instructions := session.Instructions{HistoryID: scope, Blocks: []session.InstructionBlock{{Source: session.ContentSource{Description: "winning replacement"}}}}
	mustBatch(t, winner.ReplaceInstructions(instructions))
	loser := beginBatch(t, other)
	mustBatch(t, loser.ReplaceToolCatalog(session.ToolCatalog{HistoryID: scope}))
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
	if !ok || !iv.Value().Equal(instructions) || loaded.Snapshot().Head() != head {
		t.Fatal("fresh load lost the winning independent component")
	}
	if _, present := loaded.Snapshot().ToolCatalog(); present {
		t.Fatal("stale explicit-empty catalog was merged despite conflict")
	}
}
