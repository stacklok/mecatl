package format_test

import (
	"reflect"
	"testing"

	"github.com/stacklok/mecatl/engine/kernel/contentid"
	"github.com/stacklok/mecatl/engine/kernel/history/format"
)

// These assignments preserve the facade's original public signatures.
var (
	_ func(string) (format.CID, error)                               = format.ParseCID
	_ func(format.CID) string                                        = format.CID.String
	_ func(format.Commit, format.Limits) ([]byte, format.CID, error) = format.EncodeCommit
	_ func([]byte, format.Limits) (format.Commit, format.CID, error) = format.DecodeCommit
)

func TestCIDFacadeTypeIdentity(t *testing.T) {
	typ := reflect.TypeFor[format.CID]()
	if typ.Name() != "CID" || typ.PkgPath() != "github.com/stacklok/mecatl/engine/kernel/history/format" || !typ.Comparable() || typ.Kind() != reflect.Struct {
		t.Fatalf("facade type identity changed: %v", typ)
	}
	if typ == reflect.TypeFor[contentid.CID]() {
		t.Fatal("history CID must remain a distinct defined public type, not an alias")
	}
	if typ.NumMethod() != 1 {
		t.Fatal("history CID method surface changed")
	}
	root, successor := wireFixtures(t)
	for _, wire := range [][]byte{root, successor} {
		commit, got, err := format.DecodeCommit(wire, testLimits())
		if err != nil {
			t.Fatal(err)
		}
		identity := contentid.Sum(wire)
		if got.String() != identity.String() {
			t.Fatal("history identity differs from exact-byte identity")
		}
		parsed, err := format.ParseCID(identity.String())
		if err != nil || parsed != got || !map[format.CID]bool{got: true}[parsed] {
			t.Fatal("facade canonical text or map-key behavior changed")
		}
		if err := contentid.Verify(wire, identity); err != nil {
			t.Fatal(err)
		}
		if commit.Parent != (format.CID{}) {
			parent, err := contentid.Parse(commit.Parent.String())
			if err != nil || parent != contentid.Sum(nil) {
				t.Fatal("successor parent link changed")
			}
		}
	}
}
