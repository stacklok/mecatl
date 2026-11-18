package session_test

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/history/mem"
	"github.com/stacklok/mecatl/engine/kernel/codec"
	historyid "github.com/stacklok/mecatl/engine/kernel/history/id"
	"github.com/stacklok/mecatl/engine/kernel/session"
	sessionformat "github.com/stacklok/mecatl/engine/kernel/session/format"
)

func TestUserInstructionsSubmitLoadReplacementAndOwnership(t *testing.T) {
	h, s, p := newIteratorSession(t)
	scope := s.Snapshot().HistoryID()
	if v, ok := s.Snapshot().UserInstructions(); ok || v != (session.UserInstructionsView{}) {
		t.Fatal("absent prefix present")
	}
	b := beginBatch(t, s)
	prefix := session.UserInstructions{Blocks: []session.InstructionBlock{
		{Key: "operator/persona", Parts: richContent(), Source: session.ContentSource{Description: "prepared prefix", WorkspaceFile: &session.WorkspaceFileSource{Path: "context.md"}}},
		{Key: "operator/empty", Source: session.ContentSource{Description: "empty block"}},
	}}
	want := prefix.Clone()
	if err := b.ReplaceUserInstructions(scope, prefix); !errors.Is(err, session.ErrTransition) {
		t.Fatalf("prefix before creation: %v", err)
	}
	mustBatch(t, b.Create())
	// Prefix replacement needs no active run and remains independent of root instructions.
	mustBatch(t, b.ReplaceRootInstructions(scope, session.RootInstructions{Blocks: []session.InstructionBlock{{Key: "operator/persona", Parts: []session.ContentPart{{Text: new("root")}}, Source: session.ContentSource{Description: "root"}}}}))
	mustBatch(t, b.ReplaceUserInstructions(scope, session.UserInstructions{}))
	mustBatch(t, b.ReplaceUserInstructions(scope, prefix))
	*prefix.Blocks[0].Parts[0].Text = "changed"
	prefix.Blocks[0].Parts[1].Media.InlineData[0] = 0
	prefix.Blocks[0].Source.WorkspaceFile.Path = "changed"
	submitBatch(t, s, b)
	old := s.Snapshot()
	oldPrefix, _ := old.UserInstructions()
	root, _ := old.RootInstructions()
	check := func(snapshot session.Snapshot, want session.UserInstructions) {
		t.Helper()
		v, ok := snapshot.UserInstructions()
		if !ok || v.HistoryID() != scope || v.Blocks().Len() != len(want.Blocks) {
			t.Fatal("prefix presence/scope/blocks")
		}
		for i, block := range v.Blocks().All() {
			w := want.Blocks[i]
			if block.Key() != w.Key || block != v.Blocks().At(i) || !sourceMatches(block.Source(), w.Source) || block.Parts().Len() != len(w.Parts) {
				t.Fatal("block navigation/provenance")
			}
			for j, part := range block.Parts().All() {
				if part != block.Parts().At(j) || !partMatches(part, w.Parts[j]) {
					t.Fatal("prefix content changed")
				}
			}
		}
	}
	for seam, candidate := range iteratorSessions(t, h, s, p) {
		t.Run(seam, func(t *testing.T) {
			snapshot := candidate.Snapshot()
			check(snapshot, want)
			fromRecord, ok := snapshot.RecordAt(3).UserInstructions()
			selected, _ := snapshot.UserInstructions()
			if !ok || fromRecord != selected || snapshot.RecordAt(3).Kind() != sessionformat.KindUserInstructions {
				t.Fatal("record inspection disagrees")
			}
			wire, err := codec.Encode(want, p.PayloadLimits)
			mustBatch(t, err)
			if !bytes.Equal(wire, snapshot.RecordAt(3).MutableClone().Payload) {
				t.Fatal("prefix canonical payload changed")
			}
			mutateMediaBytes(t, selected.Blocks().At(0).Parts().At(1))
			clear(snapshot.RecordAt(3).MutableClone().Payload)
			check(snapshot, want)
			if n := testing.AllocsPerRun(100, func() {
				v, _ := snapshot.UserInstructions()
				if v.Blocks().At(0).Source().Description() != "prepared prefix" {
					panic("prefix scalar")
				}
			}); n != 0 {
				t.Fatalf("prefix navigation allocated %g times", n)
			}
		})
	}
	b = beginBatch(t, s)
	run := batchRun(t, b)
	mustBatch(t, b.EndRun(session.RunEndedInterrupted))
	nextRun := batchRun(t, b)
	if nextRun == run {
		t.Fatal("run negative control")
	}
	mustBatch(t, b.ReplaceRootInstructions(scope, session.RootInstructions{}))
	submitBatch(t, s, b)
	check(s.Snapshot(), want)
	if selected, _ := s.Snapshot().UserInstructions(); selected != oldPrefix {
		t.Fatal("run/root replacement reset prefix")
	}
	b = beginBatch(t, s)
	newRoot := session.RootInstructions{Blocks: []session.InstructionBlock{{Key: "operator/persona", Parts: []session.ContentPart{{Text: new("new root")}}, Source: session.ContentSource{Description: "replacement root"}}}}
	mustBatch(t, b.ReplaceRootInstructions(scope, newRoot))
	mustBatch(t, b.ReplaceUserInstructions(scope, want))
	mustBatch(t, b.ReplaceUserInstructions(scope, session.UserInstructions{}))
	submitBatch(t, s, b)
	loaded, err := session.Load(context.Background(), h, scope, p, testConfig())
	mustBatch(t, err)
	for _, snapshot := range []session.Snapshot{s.Snapshot(), loaded.Snapshot()} {
		check(snapshot, session.UserInstructions{})
		instructions, ok := snapshot.RootInstructions()
		if !ok || !instructionsMatch(instructions, newRoot) {
			t.Fatal("prefix replacement/clear changed root instructions")
		}
		selected, ok := snapshot.RecordAt(snapshot.RecordCount() - 1).UserInstructions()
		if !ok || selected.HistoryID() != scope || selected.Blocks().Len() != 0 {
			t.Fatal("explicit empty replacement lost")
		}
	}
	check(old, want)
	if root.Blocks().Len() != 1 {
		t.Fatal("prefix replacement changed pinned root")
	}
}

func TestUserInstructionsScopeValidationAndZero(t *testing.T) {
	h := newLifecycleHistory(t, mem.New())
	s := boundSession(t, h, sessionPolicy())
	b := beginBatch(t, s)
	mustBatch(t, b.Create())
	scope := s.Snapshot().HistoryID()
	foreign, err := historyid.ParseHistoryID("HST2222222222222222222222")
	mustBatch(t, err)
	for _, tc := range []struct {
		scope historyid.HistoryID
		value session.UserInstructions
		want  error
	}{
		{foreign, session.UserInstructions{Blocks: []session.InstructionBlock{{Key: "operator/persona"}}}, session.ErrIdentity},
		{historyid.HistoryID{}, session.UserInstructions{}, codec.ErrInvalidData},
		{scope, session.UserInstructions{Blocks: []session.InstructionBlock{{Key: "operator/persona"}}}, codec.ErrInvalidData},
	} {
		if err := b.ReplaceUserInstructions(tc.scope, tc.value); !errors.Is(err, tc.want) {
			t.Fatalf("prefix scope/provenance: %v; want %v", err, tc.want)
		}
	}
	mustBatch(t, b.ReplaceUserInstructions(scope, session.UserInstructions{Blocks: []session.InstructionBlock{{Key: "operator/empty", Source: session.ContentSource{Description: "valid empty block"}}}}))
	submitBatch(t, s, b)
	if s.Snapshot().RecordCount() != 2 {
		t.Fatal("failed additions consumed record slots")
	}
	for _, record := range []session.RecordView{{}, s.Snapshot().RecordAt(0)} {
		if v, ok := record.UserInstructions(); ok || v.HistoryID() != (historyid.HistoryID{}) || v.Blocks().Len() != 0 {
			t.Fatal("zero/wrong-kind prefix")
		}
	}
	if v, ok := (session.Snapshot{}).UserInstructions(); ok || v != (session.UserInstructionsView{}) {
		t.Fatal("zero snapshot prefix")
	}
	fixture := encodeFixture(t, sessionformat.KindUserInstructions, sessionformat.UserInstructions{})
	dto := fixture.MutableClone()
	dto.KindVersion++
	assertRejected(t, h, s, proposedCommit(t, s, fixtureRecord{dto}), session.ErrUnsupportedRecord)
}
