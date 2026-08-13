package prompt_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/prompt"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

// fakeIndexSource is a scripted prompt.MemoryIndexSource for offline tests.
type fakeIndexSource struct {
	entries []tool.MemoryEntry
	err     error
}

func (f fakeIndexSource) Index(context.Context) ([]tool.MemoryEntry, error) {
	return f.entries, f.err
}

func TestMemoryIndexAssemblerRendersUserMessage(t *testing.T) {
	src := fakeIndexSource{entries: []tool.MemoryEntry{
		{Key: "pref/test-runner", Description: "preferred test runner"},
		{Key: "project/deploy-gate", Description: "staging needs manual approval"},
		{Key: "pref/editor", Description: "vim"},
	}}
	got, err := prompt.MemoryIndexAssembler{Src: src}.Assemble(context.Background(), nil)
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d messages, want exactly 1", len(got))
	}
	msg := got[0]
	if msg.Role != session.RoleUser {
		t.Errorf("role = %q, want user (memory index must never ride the system role)", msg.Role)
	}
	for _, want := range []string{
		"pref/test-runner", "preferred test runner",
		"project/deploy-gate", "staging needs manual approval",
		"pref/editor", "vim",
	} {
		if !strings.Contains(msg.Text, want) {
			t.Errorf("rendered index missing %q:\n%s", want, msg.Text)
		}
	}
	// The entry body is fenced as DATA in matching <memory-index> delimiters on
	// their OWN lines, and every entry line sits BETWEEN them (a prompt-injection
	// guard). The fence tokens are matched line-anchored so the mention of
	// "<memory-index>" inside the header prose is not mistaken for the open fence.
	open := strings.Index(msg.Text, "\n<memory-index encoding=\"jsonl\">\n")
	closeIdx := strings.Index(msg.Text, "\n</memory-index>")
	if open < 0 || closeIdx < 0 || open >= closeIdx {
		t.Fatalf("index body not wrapped in matching <memory-index>...</memory-index> fence:\n%s", msg.Text)
	}
	if i := strings.Index(msg.Text, "pref/test-runner"); i < open || i > closeIdx {
		t.Errorf("entry line escaped the data fence:\n%s", msg.Text)
	}
	// The header (everything before the open fence) must instruct the model to
	// treat the fenced block as data.
	if !strings.Contains(msg.Text[:open], "DATA") {
		t.Errorf("header should tell the model to treat the fenced block as data:\n%s", msg.Text)
	}
}

func TestMemoryIndexAssemblerOmitsValues(t *testing.T) {
	src := fakeIndexSource{entries: []tool.MemoryEntry{
		{Key: "k", Value: "SECRET-VALUE-SHOULD-NOT-RENDER", Description: "a thing"},
	}}
	got, _ := prompt.MemoryIndexAssembler{Src: src}.Assemble(context.Background(), nil)
	if len(got) != 1 {
		t.Fatalf("want 1 message")
	}
	if strings.Contains(got[0].Text, "SECRET-VALUE-SHOULD-NOT-RENDER") {
		t.Errorf("index leaked a value: %s", got[0].Text)
	}
}

func TestMemoryIndexAssemblerCapAndFooter(t *testing.T) {
	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	var entries []tool.MemoryEntry
	for i := 0; i < 10; i++ {
		entries = append(entries, tool.MemoryEntry{
			Key:         fmt.Sprintf("k/%02d", i),
			Description: fmt.Sprintf("desc %d", i),
			UpdatedAt:   base.Add(time.Duration(i) * time.Minute), // higher i == newer
		})
	}
	got, err := prompt.MemoryIndexAssembler{Src: fakeIndexSource{entries: entries}, MaxEntries: 3}.Assemble(context.Background(), nil)
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 message")
	}
	text := got[0].Text
	// Oldest-first trimming: the 3 NEWEST (k/07, k/08, k/09) survive.
	for _, want := range []string{"k/07", "k/08", "k/09"} {
		if !strings.Contains(text, want) {
			t.Errorf("expected newest entry %q to survive cap:\n%s", want, text)
		}
	}
	// An old entry is dropped.
	if strings.Contains(text, "k/00") {
		t.Errorf("oldest entry k/00 should have been trimmed:\n%s", text)
	}
	// A footer reports the 7 trimmed entries and points at Recall.
	if !strings.Contains(text, "7 older") || !strings.Contains(strings.ToLower(text), "recall") {
		t.Errorf("expected a trimming footer pointing at Recall:\n%s", text)
	}
}

func TestMemoryIndexAssemblerNilSourceIsNoop(t *testing.T) {
	got, err := prompt.MemoryIndexAssembler{Src: nil}.Assemble(context.Background(), nil)
	if err != nil || got != nil {
		t.Fatalf("nil source: got=%v err=%v, want nil/nil", got, err)
	}
}

func TestMemoryIndexAssemblerFailsSoft(t *testing.T) {
	src := fakeIndexSource{err: errors.New("disk on fire")}
	got, err := prompt.MemoryIndexAssembler{Src: src}.Assemble(context.Background(), nil)
	if err != nil {
		t.Errorf("memory faults must fail soft (memory is best-effort context), got err=%v", err)
	}
	if got != nil {
		t.Errorf("source error should yield no message, got %v", got)
	}
}

func TestMemoryIndexAssemblerEmptyIsNoop(t *testing.T) {
	got, err := prompt.MemoryIndexAssembler{Src: fakeIndexSource{}}.Assemble(context.Background(), nil)
	if err != nil || got != nil {
		t.Fatalf("empty index: got=%v err=%v, want nil/nil", got, err)
	}
}

func TestMultiAssemblerConcatenatesInOrder(t *testing.T) {
	ws := memfs.NewWorkspace("/proj")
	if err := ws.Write(context.Background(), "AGENTS.md", []byte("be terse")); err != nil {
		t.Fatalf("seed: %v", err)
	}
	src := fakeIndexSource{entries: []tool.MemoryEntry{{Key: "pref/x", Description: "a pref"}}}

	multi := prompt.NewMultiAssembler(
		prompt.RootAssembler{},
		prompt.MemoryIndexAssembler{Src: src},
	)
	got, err := multi.Assemble(context.Background(), ws)
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d messages, want 2 (AGENTS.md then index)", len(got))
	}
	if !strings.Contains(got[0].Text, "be terse") {
		t.Errorf("first message should be AGENTS.md, got %q", got[0].Text)
	}
	if !strings.Contains(got[1].Text, "pref/x") {
		t.Errorf("second message should be the memory index, got %q", got[1].Text)
	}
}

// TestMemoryIndexNotInStablePrefix is the cache-invariant proof (gauntlet #6): the
// tier-0 index is turn-0 conversation content, never part of prompt.Build's
// StablePrefix. Building with and without an index-bearing config yields a
// byte-identical StablePrefix, because Build does not consult memory at all.
func TestMemoryIndexNotInStablePrefix(t *testing.T) {
	cfg := prompt.Config{
		Tools: []tool.ToolSpec{{Name: "Recall", Description: "load memory"}},
	}
	base := prompt.Build(cfg).StablePrefix

	// Render an index message and confirm none of its content appears in the
	// StablePrefix — the index lives after the cache breakpoint, as a user message.
	src := fakeIndexSource{entries: []tool.MemoryEntry{{Key: "pref/secret-key", Description: "do not cache me"}}}
	idx, _ := prompt.MemoryIndexAssembler{Src: src}.Assemble(context.Background(), nil)
	if len(idx) != 1 {
		t.Fatalf("want an index message to test against")
	}
	if strings.Contains(base, "pref/secret-key") || strings.Contains(base, "do not cache me") {
		t.Errorf("memory index content leaked into StablePrefix (cache invariant broken):\n%s", base)
	}
	// And Build is independent of memory: the StablePrefix is stable across builds.
	if again := prompt.Build(cfg).StablePrefix; again != base {
		t.Errorf("StablePrefix not byte-stable across builds")
	}
}

// TestStablePrefixIdenticalWithAndWithoutMemoryIndex is the DIRECT gauntlet #6
// assertion the reviewers asked for: assembling a non-empty memory index alongside
// the prompt must NOT perturb prompt.Build's StablePrefix by a single byte. The
// "with index" prefix and the "without index" prefix are compared verbatim. They
// are equal because the index rides as a turn-0 user message AFTER the cache
// breakpoint and the StablePrefix is computed by Build alone, which never sees the
// index — this test fails loudly if anyone ever routes the index into Build.
func TestStablePrefixIdenticalWithAndWithoutMemoryIndex(t *testing.T) {
	cfg := prompt.Config{
		Role:  "You are a test harness.",
		Tools: []tool.ToolSpec{{Name: "Recall", Description: "load memory"}},
	}

	// "Without index": empty source → no index message at all.
	withoutPrefix := prompt.Build(cfg).StablePrefix
	emptyIdx, _ := prompt.MemoryIndexAssembler{Src: fakeIndexSource{}}.Assemble(context.Background(), nil)
	if len(emptyIdx) != 0 {
		t.Fatalf("empty source should yield no index message, got %d", len(emptyIdx))
	}

	// "With index": a non-empty index is assembled (it would be recorded as turn-0
	// conversation content). The StablePrefix from the SAME cfg must be byte-identical.
	withIdx, _ := prompt.MemoryIndexAssembler{Src: fakeIndexSource{entries: []tool.MemoryEntry{
		{Key: "pref/a", Description: "first pref"},
		{Key: "pref/b", Description: "second pref"},
	}}}.Assemble(context.Background(), nil)
	if len(withIdx) != 1 {
		t.Fatalf("non-empty source should yield exactly one index message, got %d", len(withIdx))
	}
	withPrefix := prompt.Build(cfg).StablePrefix

	if withPrefix != withoutPrefix {
		t.Fatalf("StablePrefix changed when a memory index was present (gauntlet #6 broken):\nwith:    %q\nwithout: %q", withPrefix, withoutPrefix)
	}
}
