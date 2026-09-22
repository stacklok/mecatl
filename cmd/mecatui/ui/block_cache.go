package ui

// mdEntry is one memoized assistant-block render: the source text and wrap width
// it was produced from (the validity key) plus the rendered ANSI output.
type mdEntry struct {
	src   string
	width int
	out   string
}

// renderContextKey is the shared comparable presentation identity for every
// whole-block render. Keep all view/presentation cache axes here so cache
// admission remains one comparison rather than a collection of partial guards.
type renderContextKey struct {
	width    int
	expanded bool
	dialect  uint32
}

// blockRenderKey combines conversation-owned content identity with the shared
// presentation identity.
type blockRenderKey struct {
	revision int
	context  renderContextKey
}

// blockEntry is one memoized whole-block render and its structural provenance.
// Prepared inputs and semantic strings are discarded after producing both outputs.
type blockEntry struct {
	key  blockRenderKey
	out  string
	rows []renderedRow
}

// resetBlockCaches drops BOTH per-block memo layers (blockCache and blockMD).
// It MUST be called whenever the conversation is rebuilt from scratch (/clear,
// the /models restart-now handoff — see Model.resetSession): both caches key on
// the block's conversation INDEX, and a fresh conversation reuses indices 0..n
// for entirely different blocks whose rev/src could coincidentally match a stale
// entry, which would alias an old block's render onto a new one.
func (r *renderer) resetBlockCaches() {
	r.blockCache = map[int]blockEntry{}
	r.blockMD = map[int]mdEntry{}
	r.blockFrameCache = map[int]frameBlockEntry{}
	r.resetBlockRenderPrefix()
	// Drop the viewport-output memo too (defense-in-depth): every CURRENT resetSession
	// caller calls refreshView() afterwards (which invalidateVPView()s), but clearing it
	// here makes that ordering non-load-bearing — a future caller that forgets refreshView
	// can never serve a stale vpView against a reset/empty conversation.
	r.vpViewValid = false
}

func (r *renderer) renderContext(expanded bool) renderContextKey {
	return renderContextKey{
		width:    r.width,
		expanded: expanded,
		dialect:  r.renderDialect,
	}
}

func (r *renderer) blockRenderKey(b *block, expanded bool) blockRenderKey {
	return blockRenderKey{revision: b.rev, context: r.renderContext(expanded)}
}
