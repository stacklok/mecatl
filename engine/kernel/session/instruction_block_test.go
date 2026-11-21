package session_test

import (
	"errors"
	"reflect"
	"testing"

	"github.com/stacklok/mecatl/engine/kernel/codec"
	historyformat "github.com/stacklok/mecatl/engine/kernel/history/format"
	"github.com/stacklok/mecatl/engine/kernel/session"
	sessionformat "github.com/stacklok/mecatl/engine/kernel/session/format"
)

func TestInstructionBlockKeySubmitLoadAndClone(t *testing.T) {
	h, s, p := newIteratorSession(t)
	scope := s.Snapshot().SessionID()
	b := beginBatch(t, s)
	mustBatch(t, b.Create())
	root := session.RootInstructions{Blocks: []session.InstructionBlock{
		{Key: "harness/core", Parts: richContent(), Source: session.ContentSource{Description: "root", WorkspaceFile: &session.WorkspaceFileSource{Path: "root.md"}}},
		{Key: "workspace/root-guidance", Parts: richContent(), Source: session.ContentSource{Description: "sibling"}},
	}}
	// The same key is valid in the independently replaced user collection.
	user := session.UserInstructions{Blocks: []session.InstructionBlock{
		{Key: "harness/core", Parts: richContent(), Source: session.ContentSource{Description: "user", WorkspaceFile: &session.WorkspaceFileSource{Path: "user.md"}}},
	}}
	wantRoot, wantUser := root.Clone(), user.Clone()
	mustBatch(t, b.ReplaceRootInstructions(scope, root))
	mustBatch(t, b.ReplaceUserInstructions(scope, user))
	root.Blocks[0].Key = "changed-before-submit"
	user.Blocks[0].Key = "changed-before-submit"
	submitBatch(t, s, b)
	for seam, candidate := range iteratorSessions(t, h, s, p) {
		t.Run(seam, func(t *testing.T) {
			snapshot := candidate.Snapshot()
			r, _ := snapshot.RootInstructions()
			u, _ := snapshot.UserInstructions()
			if !r.MutableClone().Equal(wantRoot) || !u.MutableClone().Equal(wantUser) {
				t.Fatal("Submit/Load lost exact keys or payload")
			}
			for _, block := range []session.InstructionBlockView{r.Blocks().At(0), u.Blocks().At(0)} {
				if block.Key() != "harness/core" {
					t.Fatal("key getter lost exact bytes")
				}
				clone := block.MutableClone()
				other := block.MutableClone()
				clone.Key = "changed-clone"
				*clone.Parts[0].Text = "changed text"
				clone.Parts[1].Media.InlineData[0] ^= 0xff
				clone.Source.WorkspaceFile.Path = "changed.md"
				if !block.MutableClone().Equal(other) || block.Key() != other.Key {
					t.Fatal("block clone mutation changed backing or another clone")
				}
				if n := testing.AllocsPerRun(100, func() {
					if block.Key() != "harness/core" {
						panic("key changed")
					}
				}); n != 0 {
					t.Fatalf("scalar key getter allocated %g times", n)
				}
			}
		})
	}
	// Reusing a key in later whole replacements neither conflicts nor preserves
	// earlier text/source. There is no lifetime registry or key-driven refresh.
	b = beginBatch(t, s)
	root.Blocks[0] = session.InstructionBlock{Key: "harness/core", Parts: []session.ContentPart{{Text: new("replacement")}}, Source: session.ContentSource{Description: "new source"}}
	mustBatch(t, b.ReplaceRootInstructions(scope, root))
	mustBatch(t, b.ReplaceUserInstructions(scope, wantUser))
	submitBatch(t, s, b)
	for seam, candidate := range iteratorSessions(t, h, s, p) {
		t.Run(seam+"/replacement", func(t *testing.T) {
			r, _ := candidate.Snapshot().RootInstructions()
			u, _ := candidate.Snapshot().UserInstructions()
			if !r.MutableClone().Equal(root) || !u.MutableClone().Equal(wantUser) {
				t.Fatal("key reuse changed replacement semantics")
			}
		})
	}
	zero := session.InstructionBlockView{}
	if zero.Key() != "" || !reflect.DeepEqual(zero.MutableClone(), session.InstructionBlock{}) {
		t.Fatal("zero view must remain safe without becoming a valid block")
	}
}

func TestInstructionBlockKeyAdmissionIsAtomic(t *testing.T) {
	h, s, _ := newIteratorSession(t)
	scope := s.Snapshot().SessionID()
	b := beginBatch(t, s)
	mustBatch(t, b.Create())
	submitBatch(t, s, b)
	b = beginBatch(t, s)
	mustBatch(t, b.ReplaceRootInstructions(scope, session.RootInstructions{}))
	before := captureCommit(t, b)
	accepted := captureRejectedSnapshot(s.Snapshot())
	for _, blocks := range [][]session.InstructionBlock{
		{{Source: session.ContentSource{Description: "missing key"}}},
		{{Key: "bad key", Source: session.ContentSource{Description: "bad key"}}},
		{{Key: "harness/core", Source: session.ContentSource{Description: "empty"}}, {Key: "harness/core", Source: session.ContentSource{Description: "empty"}}},
		{{Key: "harness/core", Parts: []session.ContentPart{{Text: new("same")}}, Source: session.ContentSource{Description: "same"}}, {Key: "harness/core", Parts: []session.ContentPart{{Text: new("same")}}, Source: session.ContentSource{Description: "same"}}},
	} {
		if err := b.ReplaceRootInstructions(scope, session.RootInstructions{Blocks: blocks}); !errors.Is(err, codec.ErrInvalidData) {
			t.Fatalf("root admission: %v", err)
		}
		if err := b.ReplaceUserInstructions(scope, session.UserInstructions{Blocks: blocks}); !errors.Is(err, codec.ErrInvalidData) {
			t.Fatalf("user admission: %v", err)
		}
		if !reflect.DeepEqual(before, captureCommit(t, b)) || !reflect.DeepEqual(accepted, captureRejectedSnapshot(s.Snapshot())) {
			t.Fatal("rejected keys changed batch or accepted state")
		}
	}
	mustBatch(t, b.ReplaceUserInstructions(scope, session.UserInstructions{}))
	submitBatch(t, s, b)
	if s.Snapshot().RecordCount() != 3 {
		t.Fatal("rejected keys consumed record slots")
	}
	assertHistoryHead(t, h, s.Snapshot().Head())
}

func TestInstructionBlockDuplicateKeysStoredAcceptance(t *testing.T) {
	h := newSessionHistory(t)
	s := boundSession(t, h, sessionPolicy())
	b := beginBatch(t, s)
	mustBatch(t, b.Create())
	submitBatch(t, s, b)
	blocks := []sessionformat.InstructionBlock{
		{Key: "harness/core", Source: sessionformat.ContentSource{Description: "empty"}},
		{Key: "harness/core", Source: sessionformat.ContentSource{Description: "empty"}},
	}
	for _, tc := range []struct {
		kind  string
		value codec.Marshaler
	}{
		{sessionformat.KindRootInstructions, sessionformat.RootInstructions{Blocks: blocks}},
		{sessionformat.KindUserInstructions, sessionformat.UserInstructions{Blocks: blocks}},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			// An independent writer can store opaque payload bytes in History. Load
			// must reject the entire continuation, including its valid first record.
			invalid := fixtureRecord{historyformat.Record{Timestamp: 0, Kind: tc.kind, KindVersion: sessionformat.Version, Payload: tc.value.AppendCBOR(nil)}}
			prefix := encodeFixture(t, sessionformat.KindRootInstructions, sessionformat.RootInstructions{})
			assertRejected(t, h, s, proposedCommit(t, s, prefix, invalid), codec.ErrInvalidData)
		})
	}
}
