package session_test

import (
	"bytes"
	"reflect"
	"testing"

	"github.com/stacklok/mecatl/engine/kernel/history/format"
	"github.com/stacklok/mecatl/engine/kernel/session"
)

func TestViewMutableCloneAcrossSubmitAndLoad(t *testing.T) {
	for seam, s := range allocationViewFixture(t, 32, 64<<10, 2) {
		t.Run(seam, func(t *testing.T) {
			snapshot := s.Snapshot()
			record := snapshot.RecordAt(0)
			beforeRecord := record.MutableClone()
			beforeRecord.Payload = bytes.Clone(beforeRecord.Payload)
			aRecord, bRecord := record.MutableClone(), record.MutableClone()
			aRecord.Payload[0] = 0
			if !reflect.DeepEqual(record.MutableClone(), beforeRecord) || !reflect.DeepEqual(bRecord, beforeRecord) {
				t.Fatal("record clone aliases accepted backing or another clone")
			}
			if record.Kind() != beforeRecord.Kind {
				t.Fatal("record clone changed borrowed navigation")
			}
			run := snapshot.RunAt(0)
			effective, ok := run.RecordAt(run.RecordCount() - 1).EffectiveInput()
			if !ok {
				t.Fatal("missing effective input")
			}
			items := effective.Inputs()
			reference := items.At(0)
			id, ok := reference.UserInputID()
			if !ok || id.RunID() != effective.RunID() {
				t.Fatal("reference lost enclosing scope")
			}
			// The exported DTO preserves its run-relative component, not the
			// view's full enclosing Session/run identity.
			ref := reference.MutableClone()
			if ref.UserInputID == nil || *ref.UserInputID != id.RunRelative() {
				t.Fatal("reference payload changed")
			}
			*ref.UserInputID = 0
			if got, _ := reference.UserInputID(); got != id {
				t.Fatal("reference clone changed retained scope or payload")
			}

			inline, ok := items.At(1).Inline()
			if !ok {
				t.Fatal("missing inline input")
			}
			before := inline.MutableClone()
			a, b := inline.MutableClone(), inline.MutableClone()
			a.Parts[0].Media.InlineData[0] = 0
			a.Source.WorkspaceFile.Path = "changed"
			if !reflect.DeepEqual(inline.MutableClone(), before) || !reflect.DeepEqual(b, before) {
				t.Fatal("clone aliases accepted backing or another clone")
			}
			if inline.Parts().Len() != 2 || inline.Source().Description() != "prepared reference" {
				t.Fatal("borrowed navigation changed")
			}
			file, ok := inline.Source().WorkspaceFile()
			if !ok || file.Path() != "docs/design.md" || b.Source.WorkspaceFile.Path != "docs/design.md" || !bytes.Equal(b.Parts[0].Media.InlineData, bytes.Repeat([]byte{0xa5}, 32)) {
				t.Fatal("accepted backing or second clone changed from original content")
			}

			// Clone a small leaf with a large sibling still retained, through
			// both admission seams. Detect bytes copied, not just allocations.
			media, _ := inline.Parts().At(0).Media()
			var detached []byte
			if n := allocatedViewBytes(func() { detached = media.MutableClone().InlineData }); n > 4096 {
				t.Fatalf("selected media cloned sibling payload: %d bytes", n)
			}
			detached[0] = 0
			if !bytes.Equal(media.InlineData().Copy(), bytes.Repeat([]byte{0xa5}, 32)) {
				t.Fatal("selected clone changed retained media")
			}
			current, _ := s.Snapshot().RecordAt(snapshot.RecordCount() - 1).EffectiveInput()
			if !reflect.DeepEqual(current.MutableClone(), effective.MutableClone()) {
				t.Fatal("clone mutation changed another snapshot")
			}
		})
	}
}

func TestViewMutableCloneZero(t *testing.T) {
	if got := (session.RecordView{}).MutableClone(); !reflect.DeepEqual(got, format.Record{}) {
		t.Fatal("zero record view did not return zero DTO")
	}
	if got := (session.EffectiveInputView{}).MutableClone(); !reflect.DeepEqual(got, session.EffectiveInput{}) {
		t.Fatal("zero contextual view did not return zero DTO")
	}
	if got := (session.MediaView{}).MutableClone(); !reflect.DeepEqual(got, session.Media{}) {
		t.Fatal("zero child view did not return zero DTO")
	}
}
