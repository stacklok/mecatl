package prompt

import (
	"context"
	"strings"

	"github.com/stacklok/mecatl/engine/session"
)

// SoulSource is a CONSUMER-DEFINED port: the prompt package declares the tiny
// seam it needs (a user-scoped persona/"soul" fragment) and the soul adapter
// satisfies it structurally, so prompt never imports the soul adapter. It is
// satisfied by *soul.Store, bound at the composition root (internal/app). It
// mirrors MemoryIndexSource — a minimal interface declared here, implemented by
// an adapter, wired in the composition layer.
//
// The body is OPAQUE text (no schema, no entry list). A "" return means "no
// usable soul" — the assembler then contributes nothing. All load discipline
// (path resolution, byte cap, injection scan, fail-soft) lives in the ADAPTER;
// the domain only fences and wraps whatever clean body it is handed.
type SoulSource interface {
	// Load returns the soul body (already validated/sanitised by the adapter), or
	// "" when there is no usable soul. It must fail soft: a missing/empty/oversized/
	// flagged/unreadable soul yields ("", nil), never an error that aborts a run.
	Load(ctx context.Context) (string, error)
}

// SoulAssembler renders the user-scoped persona/"soul" as a single user-role
// message recorded once at turn 0 (via the InstructionAssembler seam). It rides
// AFTER the cache-stable system prefix, so it never touches prompt.Build's
// StablePrefix — the prompt-cache invariant (gauntlet #6) is untouched. By the
// chosen ordering (issue #14) it sits BEFORE the memory index: identity ("who you
// are") precedes saved facts ("what you know").
//
// It FAILS SOFT: a nil source, a source error, or an empty body yields no message
// and no error — the persona is best-effort context, not correctness, so a soul
// fault must never abort a run.
type SoulAssembler struct {
	// Src is the soul source; nil makes the assembler a no-op (soul disabled).
	Src SoulSource
}

// Compile-time assertion that SoulAssembler satisfies the interface.
var _ InstructionAssembler = SoulAssembler{}

// Assemble renders the soul body into one user-role message. The workspace is
// unused: the soul is user-scoped (~/.config/mecatl/soul.md), resolved by the
// adapter against the process environment, NOT against the session workspace
// root. It fails soft on a nil source, a source error, or an empty body.
func (a SoulAssembler) Assemble(ctx context.Context) ([]session.Message, error) {
	if a.Src == nil {
		return nil, nil
	}
	body, err := a.Src.Load(ctx)
	if err != nil {
		// Best-effort context: never fail a run on a soul fault.
		return nil, nil
	}
	body = strings.TrimSpace(body)
	if body == "" {
		return nil, nil
	}
	return []session.Message{session.NewUserMessage(renderSoul(body))}, nil
}

// Data fence for the soul body. The body is the OPERATOR'S persona — DATA that
// shapes tone/identity, NOT instructions the model should obey as a controller.
// It is wrapped in explicit <soul>...</soul> delimiters (matching the house style
// of <memory-index> in memoryindex.go and <env> in env.go) and the header tells
// the model to treat the fenced contents as data describing how the operator
// wants it to act, never as a new instruction stream. This is a cheap
// prompt-injection fence; the adapter additionally injection-scans and byte-caps
// the body before it ever reaches here.
const (
	soulOpen  = "<soul>"
	soulClose = "</soul>"
)

// renderSoul wraps the (already-clean) soul body in the header + matching
// <soul>...</soul> fence on their own lines, mirroring renderMemoryIndex's fence
// mechanics. It does no validation or truncation — that is the adapter's job.
func renderSoul(body string) string {
	// header is the package-level soulHeader (turn0.go) — the single source of
	// truth shared with the IsInjectedTurn0Fragment predicate, so a reword here
	// updates the predicate automatically.
	var b strings.Builder
	b.WriteString(soulHeader)
	b.WriteString(soulOpen)
	b.WriteByte('\n')
	b.WriteString(body)
	b.WriteByte('\n')
	b.WriteString(soulClose)
	return b.String()
}
