package buildinfo

import (
	"bytes"
	"testing"
)

func TestVersionActionIsExactAndSideEffectFree(t *testing.T) {
	for _, args := range [][]string{{"mecated", "--version"}, {"mecak8s", "--version"}, {"mecatequi", "--version"}, {"mecademo", "--version"}, {"mecatui", "--version"}} {
		if !IsVersion(args) {
			t.Fatalf("IsVersion(%q) = false, want true", args)
		}
	}
	for _, args := range [][]string{{"mecated"}, {"mecated", "--version", "extra"}, {"mecated", "--VERSION"}, {"mecated", "run", "--version"}} {
		if IsVersion(args) {
			t.Errorf("IsVersion(%q) = true, want false", args)
		}
	}

	old := BuildID
	BuildID = "test-build"
	t.Cleanup(func() { BuildID = old })
	for _, name := range []string{"mecated", "mecak8s", "mecatequi", "mecademo", "mecatui"} {
		var out bytes.Buffer
		PrintVersion(&out, name)
		if got, want := out.String(), name+" test-build\n"; got != want {
			t.Errorf("PrintVersion(%q) = %q, want %q", name, got, want)
		}
	}
}
