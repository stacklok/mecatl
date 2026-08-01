package skillfs

import (
	"context"
	"testing"
)

// staticSource is a path-free Source over a fixed slice, for tests that used
// to hand NewTool a []Skill directly. Mirrors the root package's
// internal/adapter/skills/activation_golden_test.go copy byte-for-byte; the
// golden test itself stays in the root package this task (per #328) and Task 3
// flips it to run through the alias.
type staticSource []Skill

func (s staticSource) Skills(context.Context) ([]Skill, []SkipError, error) { return s, nil, nil }

// newToolOver builds the Skill tool over the given skills through the REAL
// seam (NewFSSource snapshot + NewSnapshotActivator). Mirrors the root
// package's helper byte-for-byte.
func newToolOver(t *testing.T, sk []Skill) Tool {
	t.Helper()
	src, skips, err := NewFSSource(context.Background(), staticSource(sk))
	if err != nil {
		t.Fatalf("NewFSSource: %v", err)
	}
	if len(skips) != 0 {
		t.Fatalf("unexpected skips: %v", skips)
	}
	metas, err := src.ListSkills(context.Background())
	if err != nil {
		t.Fatalf("ListSkills: %v", err)
	}
	return NewTool(metas, NewSnapshotActivator(src))
}
