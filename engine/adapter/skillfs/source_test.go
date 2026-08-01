package skillfs

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// fakeSource is an in-memory Source for testing MultiSource composition without
// touching the filesystem. It returns its canned skills, skips, and error.
type fakeSource struct {
	skills []Skill
	skips  []SkipError
	err    error
}

func (f fakeSource) Skills(context.Context) ([]Skill, []SkipError, error) {
	return f.skills, f.skips, f.err
}

func TestMultiSourceAggregates(t *testing.T) {
	ms := NewMultiSource(
		fakeSource{skills: []Skill{{Name: "b", Description: "b1", Body: "B", Path: "/p/b"}}},
		fakeSource{skills: []Skill{{Name: "a", Description: "a1", Body: "A", Path: "/p/a"}}},
	)
	got, skips, err := ms.Skills(context.Background())
	if err != nil {
		t.Fatalf("Skills: %v", err)
	}
	if len(skips) != 0 {
		t.Fatalf("unexpected skips: %v", skips)
	}
	if len(got) != 2 {
		t.Fatalf("got %d skills, want 2", len(got))
	}
	// Output is sorted by name regardless of source order.
	if got[0].Name != "a" || got[1].Name != "b" {
		t.Errorf("not sorted by name: %q, %q", got[0].Name, got[1].Name)
	}
}

func TestMultiSourcePrecedenceEarlierWins(t *testing.T) {
	// Two sources both declare "dup"; the EARLIER (higher-precedence) source wins.
	high := fakeSource{skills: []Skill{{Name: "dup", Description: "high", Body: "H", Path: "/high/dup"}}}
	low := fakeSource{skills: []Skill{{Name: "dup", Description: "low", Body: "L", Path: "/low/dup"}}}

	got, skips, err := NewMultiSource(high, low).Skills(context.Background())
	if err != nil {
		t.Fatalf("Skills: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("collision should yield 1 skill, got %d", len(got))
	}
	if got[0].Description != "high" || got[0].Path != "/high/dup" {
		t.Errorf("earlier source should win, got %+v", got[0])
	}
	// The shadowed lower-precedence skill must be reported, naming the kept path.
	if len(skips) != 1 {
		t.Fatalf("expected 1 shadow notice, got %d: %v", len(skips), skips)
	}
	if skips[0].Path != "/low/dup" {
		t.Errorf("shadow notice should point at the dropped skill, got %q", skips[0].Path)
	}
	if !strings.Contains(skips[0].Reason, "shadowed") || !strings.Contains(skips[0].Reason, "/high/dup") {
		t.Errorf("shadow notice should explain it and name the winner, got %q", skips[0].Reason)
	}
}

func TestMultiSourceAggregatesDiagnosticsInOrder(t *testing.T) {
	a := fakeSource{
		skills: []Skill{{Name: "a", Description: "a", Path: "/a"}},
		skips:  []SkipError{{Path: "/a/bad", Reason: "from a"}},
	}
	b := fakeSource{
		skills: []Skill{{Name: "b", Description: "b", Path: "/b"}},
		skips:  []SkipError{{Path: "/b/bad", Reason: "from b"}},
	}
	_, skips, err := NewMultiSource(a, b).Skills(context.Background())
	if err != nil {
		t.Fatalf("Skills: %v", err)
	}
	if len(skips) != 2 {
		t.Fatalf("expected both sources' diagnostics, got %d: %v", len(skips), skips)
	}
	// Diagnostics preserve source order.
	if skips[0].Reason != "from a" || skips[1].Reason != "from b" {
		t.Errorf("diagnostics out of source order: %v", skips)
	}
}

func TestMultiSourceFatalErrorPropagates(t *testing.T) {
	boom := errors.New("io fault")
	ms := NewMultiSource(
		fakeSource{skills: []Skill{{Name: "ok", Path: "/ok"}}},
		fakeSource{err: boom},
	)
	got, _, err := ms.Skills(context.Background())
	if !errors.Is(err, boom) {
		t.Fatalf("a source's fatal error must propagate, got %v", err)
	}
	if got != nil {
		t.Errorf("on fatal error no skills should be returned, got %v", got)
	}
}

func TestNewMultiSourceIgnoresNil(t *testing.T) {
	ms := NewMultiSource(nil, fakeSource{skills: []Skill{{Name: "x", Path: "/x"}}}, nil)
	got, _, err := ms.Skills(context.Background())
	if err != nil {
		t.Fatalf("Skills: %v", err)
	}
	if len(got) != 1 || got[0].Name != "x" {
		t.Fatalf("nil sources should be ignored, got %+v", got)
	}
}
