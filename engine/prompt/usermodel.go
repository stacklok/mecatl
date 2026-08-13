package prompt

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

// UserModelSource is a CONSUMER-DEFINED port: the prompt package declares the
// tiny seam it needs (a user-model fragment — durable FACTS about the operator)
// and the memory adapter satisfies it structurally, so prompt never imports the
// memory adapter. It is satisfied by *memory.Store (a SECOND, user-scoped store
// instance), bound at the composition root (internal/app). Its single method
// mirrors MemoryIndexSource.Index / tool.MemoryStore.Index, which prompt already
// has access to via the tool package it imports — so no new package edge is
// introduced.
//
// The entries it returns are tier-0-shaped (key + description + updated-at, value
// omitted), like the memory index — but scoped to the user-model namespace and
// rendered LAST in the turn-0 seam (after the project memory index), so identity
// (soul) → saved project facts (memory index) → who-the-operator-is (user model).
type UserModelSource interface {
	// Index returns the user-model entries (key + description + updated-at, value
	// omitted) for the current operator. A nil slice means "nothing stored".
	Index(ctx context.Context) ([]tool.MemoryEntry, error)
}

// User-model index bounds. Mirrors the memory-index bounds: the block is curated
// and must stay small enough to keep in every prompt. Over the cap the OLDEST
// entries are trimmed.
const (
	// defaultUserModelMaxEntries caps how many entries the user-model block renders.
	defaultUserModelMaxEntries = 100
	// defaultUserModelMaxBytes is a hard ceiling on the rendered body.
	defaultUserModelMaxBytes = 4 * 1024
)

// UserModelAssembler renders the saved user-model as a single user-role message
// recorded once at turn 0 (via the InstructionAssembler seam). It rides AFTER the
// cache-stable system prefix, so it never touches prompt.Build's StablePrefix —
// the prompt-cache invariant (gauntlet #6) is untouched. By the chosen ordering
// (issue #14) it sits LAST: identity (soul) precedes saved project facts (memory
// index) which precede the operator model (this).
//
// It FAILS SOFT: a nil source, a source error, or an empty set yields no message
// and no error — the user model is best-effort context, not correctness, so a
// fault must never abort a run.
type UserModelAssembler struct {
	// Src is the user-model source; nil makes the assembler a no-op (disabled).
	Src UserModelSource
	// MaxEntries caps the rendered entry count; 0 uses the default.
	MaxEntries int
	// MaxBytes caps the rendered body size; 0 uses the default.
	MaxBytes int
}

// Compile-time assertion that UserModelAssembler satisfies the interface.
var _ InstructionAssembler = UserModelAssembler{}

// Assemble renders the capped user-model into one user-role message. The
// workspace is unused (the user model is user-scoped at the adapter, cross-project,
// not workspace files). It fails soft on a nil source, a source error, or an
// empty set.
func (a UserModelAssembler) Assemble(ctx context.Context, _ tool.Workspace) ([]session.Message, error) {
	if a.Src == nil {
		return nil, nil
	}
	entries, err := a.Src.Index(ctx)
	if err != nil {
		// Best-effort context: never fail a run on a user-model fault.
		return nil, nil
	}
	if len(entries) == 0 {
		return nil, nil
	}

	text := renderUserModel(entries, a.maxEntries(), a.maxBytes())
	if text == "" {
		return nil, nil
	}
	return []session.Message{session.NewUserMessage(text)}, nil
}

func (a UserModelAssembler) maxEntries() int {
	if a.MaxEntries > 0 {
		return a.MaxEntries
	}
	return defaultUserModelMaxEntries
}

func (a UserModelAssembler) maxBytes() int {
	if a.MaxBytes > 0 {
		return a.MaxBytes
	}
	return defaultUserModelMaxBytes
}

// Data fence for the user-model body. The entries are DATA the agent curated about
// the operator, not instructions, so they are wrapped in explicit
// <user-model>...</user-model> delimiters (matching the house style of
// <memory-index> and <soul>) and the header tells the model these are FACTS not
// rules — how to behave comes from the soul + system rules, not from this block.
const (
	userModelOpen  = "<user-model>"
	userModelClose = "</user-model>"
)

// renderUserModel renders entries as a bounded "key — description" list, fenced in
// <user-model>...</user-model> so the model treats it unambiguously as DATA. Two
// bounds apply, OLDEST-first (reusing the memory-index helpers):
//
//   - the entry cap (maxEntries): keep the most-recently-updated maxEntries, then
//     re-sort the kept set by key for stable output;
//   - the byte ceiling (maxBytes): stop emitting lines once the body would exceed
//     it.
func renderUserModel(entries []tool.MemoryEntry, maxEntries, maxBytes int) string {
	total := len(entries)

	kept := entries
	if total > maxEntries {
		kept = keepNewest(entries, maxEntries)
	}

	// header is the package-level userModelHeader (turn0.go) — the single source of
	// truth shared with the IsInjectedTurn0Fragment predicate.
	var b strings.Builder
	b.WriteString(userModelHeader)
	b.WriteString(userModelOpen)
	b.WriteByte('\n')

	closeLen := len(userModelClose) + 1 // +1 for the preceding newline

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
		if b.Len()+len(line)+closeLen > maxBytes {
			break
		}
		b.WriteString(line)
		shown++
	}

	if notShown := total - shown; notShown > 0 {
		fmt.Fprintf(&b, "...(%d older entr%s not shown; use SearchUserModel with a topic query to find them)\n", notShown, plural(notShown))
	}
	b.WriteString(userModelClose)
	return b.String()
}
