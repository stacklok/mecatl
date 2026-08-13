package prompt

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

// MemoryIndexSource is a CONSUMER-DEFINED port: the prompt package declares the
// tiny seam it needs (a tier-0 memory index) and the memory adapter satisfies it
// structurally, so prompt never imports the memory adapter. It is satisfied by
// *memory.Store, bound at the composition root (internal/app). Its single method
// mirrors tool.MemoryStore.Index, which prompt already has access to via the
// tool package it imports — so no new package edge is introduced.
type MemoryIndexSource interface {
	// Index returns the tier-0 entries (key + description + updated-at, value
	// omitted) for the current project. A nil slice means "nothing stored".
	Index(ctx context.Context) ([]tool.MemoryEntry, error)
}

// Tier-0 index bounds (decision D4): the index is curated and must stay small
// enough to keep in every prompt. Over the cap, the OLDEST entries are trimmed
// and a footer points the model at Recall.
const (
	// defaultMemoryIndexMaxEntries caps how many entries the index renders.
	defaultMemoryIndexMaxEntries = 200
	// defaultMemoryIndexMaxBytes is a hard ceiling on the rendered index body, so
	// pathological descriptions cannot bloat the prompt even under the entry cap.
	defaultMemoryIndexMaxBytes = 8 * 1024
	// memoryIndexDescriptionMax caps a single rendered description line so one
	// long derived description cannot dominate the index.
	memoryIndexDescriptionMax = 120
)

// MemoryIndexAssembler renders the tier-0 memory index as a single user-role
// message recorded once at turn 0 (via the InstructionAssembler seam). It rides
// AFTER the cache-stable system prefix, so it never touches prompt.Build's
// StablePrefix — the prompt-cache invariant (gauntlet #6) is untouched.
//
// It FAILS SOFT: a nil source, or a source returning an error, yields no message
// and no error — memory is best-effort context, not correctness, so a memory
// fault must never abort a run.
type MemoryIndexAssembler struct {
	// Src is the tier-0 index source; nil makes the assembler a no-op (memory
	// disabled).
	Src MemoryIndexSource
	// MaxEntries caps the rendered entry count; 0 uses the default.
	MaxEntries int
	// MaxBytes caps the rendered body size; 0 uses the default.
	MaxBytes int
}

// Compile-time assertion that MemoryIndexAssembler satisfies the interface.
var _ InstructionAssembler = MemoryIndexAssembler{}

// Assemble renders the capped tier-0 index into one user-role message. The
// workspace is unused (memory is project-scoped at the adapter, not workspace
// files). It fails soft on a nil source or a source error.
func (a MemoryIndexAssembler) Assemble(ctx context.Context, _ tool.Workspace) ([]session.Message, error) {
	if a.Src == nil {
		return nil, nil
	}
	entries, err := a.Src.Index(ctx)
	if err != nil {
		// Best-effort context: never fail a run on a memory fault.
		return nil, nil
	}
	if len(entries) == 0 {
		return nil, nil
	}

	text := renderMemoryIndex(entries, a.maxEntries(), a.maxBytes())
	if text == "" {
		return nil, nil
	}
	return []session.Message{session.NewUserMessage(text)}, nil
}

func (a MemoryIndexAssembler) maxEntries() int {
	if a.MaxEntries > 0 {
		return a.MaxEntries
	}
	return defaultMemoryIndexMaxEntries
}

func (a MemoryIndexAssembler) maxBytes() int {
	if a.MaxBytes > 0 {
		return a.MaxBytes
	}
	return defaultMemoryIndexMaxBytes
}

// Data fence for the index body. The entry list is DATA the model stored, not an
// instruction, so it is wrapped in explicit <memory-index>...</memory-index>
// delimiters (matching the house style of the <env> block in env.go) and the
// header tells the model to treat anything inside as data, never as instructions.
// This is a cheap prompt-injection fence; see the Trust model note in
// docs/adr/0009-tiered-memory.md for the single-user / single-trust-zone assumption.
const (
	memoryIndexOpen  = `<memory-index encoding="jsonl">`
	memoryIndexClose = "</memory-index>"
)

// renderMemoryIndex renders entries as a bounded "key — description" list, fenced
// in <memory-index>...</memory-index> so the model treats it unambiguously as
// data. Two bounds apply, OLDEST-first:
//
//   - the entry cap (maxEntries): keep the most-recently-updated maxEntries, then
//     re-sort the kept set by key for stable output;
//   - the byte ceiling (maxBytes): stop emitting lines once the body would exceed
//     it.
//
// Any entries not shown (by either bound) are reported in a footer that points at
// Recall, so the model knows there is more behind the index.
func renderMemoryIndex(entries []tool.MemoryEntry, maxEntries, maxBytes int) string {
	total := len(entries)

	// Apply the entry cap oldest-first: keep the maxEntries most-recently-updated.
	kept := entries
	if total > maxEntries {
		kept = keepNewest(entries, maxEntries)
	}

	// header is the package-level memoryIndexHeader (turn0.go) — the single source
	// of truth shared with the IsInjectedTurn0Fragment predicate.
	var b strings.Builder
	b.WriteString(memoryIndexHeader)
	b.WriteString(memoryIndexOpen)
	b.WriteByte('\n')

	// Reserve room for the closing fence so the byte ceiling is honoured WITH the
	// fence accounted for, never overflowing once it is appended.
	closeLen := len(memoryIndexClose) + 1 // +1 for the preceding newline

	shown := 0
	for _, e := range kept {
		description := e.Description
		if tool.SecretShapedMemoryValue(e.Key, description) {
			description = "[withheld: secret-shaped memory description]"
		}
		encoded, _ := json.Marshal(struct {
			Key         string `json:"key"`
			Description string `json:"description"`
		}{Key: validUTF8(e.Key), Description: validUTF8(clampLine(description))})
		line := string(encoded) + "\n"
		// Honour the byte ceiling: stop before the body (plus the close fence we
		// still owe) would overflow.
		if b.Len()+len(line)+closeLen > maxBytes {
			break
		}
		b.WriteString(line)
		shown++
	}

	if notShown := total - shown; notShown > 0 {
		fmt.Fprintf(&b, "...(%d older entr%s not shown; use SearchMemory with a topic query to find them by topic, then Recall a key to load it)\n", notShown, plural(notShown))
	}
	b.WriteString(memoryIndexClose)
	return b.String()
}

// keepNewest returns the n entries with the latest UpdatedAt, re-sorted by key for
// stable rendering. It does not mutate the input.
func keepNewest(entries []tool.MemoryEntry, n int) []tool.MemoryEntry {
	cp := make([]tool.MemoryEntry, len(entries))
	copy(cp, entries)
	// Newest first; key as a deterministic tiebreaker for equal timestamps.
	sort.Slice(cp, func(i, j int) bool {
		if !cp[i].UpdatedAt.Equal(cp[j].UpdatedAt) {
			return cp[i].UpdatedAt.After(cp[j].UpdatedAt)
		}
		return cp[i].Key < cp[j].Key
	})
	cp = cp[:n]
	sort.Slice(cp, func(i, j int) bool { return cp[i].Key < cp[j].Key })
	return cp
}

// clampLine trims a description to a single line and at most
// memoryIndexDescriptionMax runes, appending an ellipsis when it trims.
func clampLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	s = strings.TrimSpace(s)
	r := []rune(s)
	if len(r) > memoryIndexDescriptionMax {
		return string(r[:memoryIndexDescriptionMax]) + "…"
	}
	return s
}

func plural(n int) string {
	if n == 1 {
		return "y"
	}
	return "ies"
}
