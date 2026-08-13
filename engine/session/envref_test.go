package session

import "testing"

// TestEnvironmentKindConstants pins the well-known labels the in-tree adapters
// mint; composition is the sole constructor site, so a future remote transport
// adds its own label without widening this package.
func TestEnvironmentKindConstants(t *testing.T) {
	for _, c := range []struct {
		k    EnvironmentKind
		want string
	}{
		{EnvKindLocal, "local"},
		{EnvKindMem, "mem"},
		{EnvKindNoFS, "nofs"},
	} {
		if string(c.k) != c.want {
			t.Errorf("EnvironmentKind = %q, want %q", c.k, c.want)
		}
	}
}

// TestEnvironmentRefComparable pins that EnvironmentRef is a plain struct (not a
// pointer) so it is comparable and never escapes to the heap on the hot dispatch
// path.
func TestEnvironmentRefComparable(t *testing.T) {
	a := EnvironmentRef{Kind: EnvKindMem, ID: "r"}
	b := EnvironmentRef{Kind: EnvKindMem, ID: "r"}
	c := EnvironmentRef{Kind: EnvKindLocal, ID: "r"}
	if a != b {
		t.Fatal("equal refs must compare equal")
	}
	if a == c {
		t.Fatal("refs differing in Kind must not compare equal")
	}
}
