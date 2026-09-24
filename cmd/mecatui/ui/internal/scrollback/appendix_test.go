package scrollback

import (
	"reflect"
	"testing"
)

func TestMecatuiTypedScrollbackModel_Scenario1_ChangedFilesAppendixIdentityAndFallback(t *testing.T) {
	var c Conversation
	first := c.Messages().AddUser(UserInput{Text: "first"})
	appendixID := c.RecordFileChange("first.go")
	if appendixID == first {
		t.Fatalf("appendix ID = %d, must not reuse block ID %d", appendixID, first)
	}
	second := c.Notices().AddNotice("later")
	if got := c.RecordFileChange("first.go"); got != appendixID {
		t.Fatalf("duplicate change ID = %d, want %d", got, appendixID)
	}
	appendix, ok := c.AppendixSnapshot()
	if !ok || appendix.ID != appendixID || appendix.PrecedingBlockID != second {
		t.Fatalf("appendix = %#v, want ID %d and fallback %d", appendix, appendixID, second)
	}
	if got, want := appendix.Files, []string{"first.go"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("files = %v, want %v", got, want)
	}
}

func TestAppendixSnapshotIsDetached(t *testing.T) {
	var c Conversation
	if got := c.RecordFileChange(""); got != 0 {
		t.Fatalf("empty path ID = %d, want 0", got)
	}
	c.RecordFileChange("first.go")
	c.RecordFileChange("second.go")
	first, ok := c.AppendixSnapshot()
	if !ok {
		t.Fatal("appendix missing")
	}
	first.Files[0] = "mutated.go"
	second, ok := c.AppendixSnapshot()
	if !ok || !reflect.DeepEqual(second.Files, []string{"first.go", "second.go"}) {
		t.Fatalf("detached appendix = %#v, %t", second, ok)
	}
}
