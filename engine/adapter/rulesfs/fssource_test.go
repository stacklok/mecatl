package rulesfs

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// TestFSSourceSnapshotStable asserts ListRules is stable across calls (the
// snapshot semantics the port documents).
func TestFSSourceSnapshotStable(t *testing.T) {
	dir := t.TempDir()
	writeRule(t, dir, "b.md", "body b")
	writeRule(t, dir, "a.md", "body a")

	src, skips, err := NewFSSource(context.Background(), DirSource{Dir: dir})
	if err != nil || len(skips) != 0 {
		t.Fatalf("NewFSSource: err=%v skips=%v", err, skips)
	}
	first, err := src.ListRules(context.Background())
	if err != nil {
		t.Fatalf("ListRules #1: %v", err)
	}
	second, err := src.ListRules(context.Background())
	if err != nil {
		t.Fatalf("ListRules #2: %v", err)
	}
	if len(first) != 2 || len(second) != 2 {
		t.Fatalf("want 2 rules each call, got %d / %d", len(first), len(second))
	}
	if first[0].Name != "a" || first[1].Name != "b" {
		t.Fatalf("ListRules not name-sorted: %+v", first)
	}
	for i := range first {
		if !reflect.DeepEqual(first[i], second[i]) {
			t.Fatalf("ListRules not stable across calls: %+v vs %+v", first, second)
		}
	}
}

// TestFSSourceDiscoveredAndDetail asserts the NON-PORT detail channel:
// Discovered/Detail carry the "<label>: <path>" locator the composition
// layer's diagnostics read; the port value carries none.
func TestFSSourceDiscoveredAndDetail(t *testing.T) {
	dir := t.TempDir()
	path := writeRule(t, dir, "rev.md", "body")

	src, _, err := NewFSSource(context.Background(), DirSource{Dir: dir, Label: "user(xdg)"})
	if err != nil {
		t.Fatalf("NewFSSource: %v", err)
	}
	detail, ok := src.Detail("rev")
	if !ok || detail != "user(xdg): "+path {
		t.Errorf("Detail(rev) = %q, %v; want %q", detail, ok, "user(xdg): "+path)
	}
	if _, ok := src.Detail("ghost"); ok {
		t.Error("Detail(unknown) must report false")
	}
	disc := src.Discovered()
	if len(disc) != 1 || disc[0].Rule.Name != "rev" || disc[0].Detail != "user(xdg): "+path {
		t.Errorf("Discovered = %+v, want the rev entry with its detail", disc)
	}
}

// TestFSSourceTierStampingViaResolveSources asserts the tier label flows from
// ResolveSources through DirSource onto the port Origin.
func TestFSSourceTierStampingViaResolveSources(t *testing.T) {
	ws := t.TempDir()
	home := t.TempDir()
	writeRule(t, wsProj(t, ws), "proj.md", "project body")

	sources := resolveSourcesEnv(ResolveOptions{
		Conventional:       true,
		Workspace:          ws,
		IncludeProjectTier: true,
	}, fakeEnv(home, ""))
	src, _, err := NewFSSource(context.Background(), sources...)
	if err != nil {
		t.Fatalf("NewFSSource: %v", err)
	}
	rules, err := src.ListRules(context.Background())
	if err != nil {
		t.Fatalf("ListRules: %v", err)
	}
	if len(rules) != 1 || rules[0].Name != "proj" {
		t.Fatalf("rules = %+v, want the proj rule", rules)
	}
	if rules[0].Origin != "project" {
		t.Errorf("Origin = %q, want project", rules[0].Origin)
	}
	if detail, ok := src.Detail("proj"); !ok || !strings.HasPrefix(detail, "project(.mecatl): ") {
		t.Errorf("Detail(proj) = %q, %v; want a project(.mecatl)-labelled path", detail, ok)
	}
}

// wsProj creates and returns the <ws>/.mecatl/rules dir.
func wsProj(t *testing.T, ws string) string {
	t.Helper()
	dir := filepath.Join(ws, ".mecatl", "rules")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	return dir
}
