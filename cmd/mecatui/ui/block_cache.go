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

// joinPrefixState is the validity key of the cached incremental-join prefix.
// Width and expand are the global presentation axes that change every block's
// render. Per-block revisions are unnecessary because a prefix contains only
// blocks that did not re-render in the current frame.
type joinPrefixState struct {
	width  int
	expand bool
}

// blockRenderCache owns all conversation-block state that survives a rendered
// frame. Its entries are bucketed by append-only conversation index and reset
// whenever a conversation is rebuilt, because a rebuilt conversation can reuse
// an index for unrelated content. It is renderer-private and used only on the
// Bubble Tea update goroutine.
//
// Whole-block entries are valid only when their blockRenderKey matches. Markdown
// entries are valid only when source and width match. Frame-row entries use the
// same whole-block key. The incremental prefix is valid only when its coverage
// count and joinPrefixState match. Storing a whole-block entry at an index inside
// that prefix invalidates it: both string and frame render paths share the cache,
// so a fresh entry must never leave a previously assembled prefix stale.
type blockRenderCache struct {
	// rendered holds complete block output and any structured-card provenance,
	// keyed by the stable conversation index and validated by blockEntry.key.
	rendered map[int]blockEntry
	// markdown holds assistant glamour output, keyed by conversation index and
	// validated by mdEntry.src and mdEntry.width.
	markdown map[int]mdEntry
	// frameRows holds provenance for fallback-rendered blocks, keyed by index and
	// validated by frameBlockEntry.key. Structured rows stay in rendered instead.
	frameRows map[int]frameBlockEntry
	// prefixLines, prefixN, and prefixKey cache the unchanged prefix for
	// incremental frame assembly. A live-tail re-render rebuilds only the suffix.
	//
	// Canonical per-block segment framing is: block i contributes
	// sep(i) + renderedBlocks[i] + "\n", where sep(0)="" and sep(i>0)="\n".
	// The cached prefix plus its freshly built suffix is byte-identical output.
	//
	// prefixLines is already split into single newline-free lines, so frame assembly
	// can hand its result directly to vp.SetContentLines without splitting or copying
	// the settled scrollback. prefixN is the number of covered blocks. prefixKey pins
	// the width/expand context used to assemble them. Per-block revisions are not in
	// prefixKey because a prefix contains only blocks that did not re-render; a
	// non-tail mutation lowers firstChanged below prefixN and storeRendered clears a
	// prefix that covers its refreshed block, so stale assembled output is never served.
	prefixLines []string
	// prefixProvenance is prefixLines' lockstep row metadata and contains no text.
	prefixProvenance []renderedRow
	// prefixN is the exclusive block index covered by prefixLines and provenance.
	prefixN int
	// prefixKey is the width/expand identity under which the prefix was assembled.
	prefixKey joinPrefixState
}

// renderedBlock returns a whole-block entry only when its complete validity key
// matches the caller's current block revision and presentation context.
func (c *blockRenderCache) renderedBlock(index int, key blockRenderKey) (blockEntry, bool) {
	entry, ok := c.rendered[index]
	return entry, ok && entry.key == key
}

// storeRendered records a freshly rendered whole block. A store within the
// cached prefix discards that prefix, because otherwise the shared cache could
// let the next frame reuse stale assembled lines after the other render path
// refreshed this block.
func (c *blockRenderCache) storeRendered(index int, entry blockEntry) {
	if c.rendered == nil {
		c.rendered = map[int]blockEntry{}
	}
	c.rendered[index] = entry
	c.invalidatePrefixAt(index)
}

// markdownBlock returns assistant markdown only when the source text and wrap
// width are unchanged. The index is a bounded storage bucket, not a validity key.
func (c *blockRenderCache) markdownBlock(index int, src string, width int) (string, bool) {
	entry, ok := c.markdown[index]
	return entry.out, ok && entry.src == src && entry.width == width
}

// storeMarkdown replaces the one assistant markdown entry for an index. A live
// growing assistant block overwrites its bucket instead of retaining every delta.
func (c *blockRenderCache) storeMarkdown(index int, entry mdEntry) {
	if c.markdown == nil {
		c.markdown = map[int]mdEntry{}
	}
	c.markdown[index] = entry
}

// frameRowsFor returns fallback frame provenance when its whole-block validity
// key matches. Structured-card provenance is intentionally owned by renderedBlock.
func (c *blockRenderCache) frameRowsFor(index int, key blockRenderKey) ([]renderedRow, bool) {
	entry, ok := c.frameRows[index]
	return entry.rows, ok && entry.key == key
}

// storeFrameRows records fallback frame provenance for one block index and its
// whole-block validity key. It does not affect the assembled prefix: callers use
// it only while assembling the current frame from an already selected render.
func (c *blockRenderCache) storeFrameRows(index int, entry frameBlockEntry) {
	if c.frameRows == nil {
		c.frameRows = map[int]frameBlockEntry{}
	}
	c.frameRows[index] = entry
}

// prefix returns the cached unchanged leading range only when both its coverage
// and its global presentation identity match the requested frame.
func (c *blockRenderCache) prefix(key joinPrefixState, n int) ([]string, []renderedRow, bool) {
	return c.prefixLines, c.prefixProvenance, c.prefixKey == key && c.prefixN == n
}

// replacePrefix installs a newly assembled unchanged leading range. The caller
// owns the supplied slices until this call; afterward the cache owns and reuses
// them until replacement, invalidation, or reset.
func (c *blockRenderCache) replacePrefix(lines []string, provenance []renderedRow, n int, key joinPrefixState) {
	c.prefixLines = lines
	c.prefixProvenance = provenance
	c.prefixN = n
	c.prefixKey = key
}

// invalidatePrefixAt drops the prefix when index falls within its covered range.
// The exclusive bound preserves the streaming tail fast path, whose fresh block
// is normally immediately after the cached prefix.
func (c *blockRenderCache) invalidatePrefixAt(index int) {
	if index < c.prefixN {
		c.prefixLines = c.prefixLines[:0]
		c.prefixProvenance = c.prefixProvenance[:0]
		c.prefixN = 0
		c.prefixKey = joinPrefixState{}
	}
}

// reset drops every cross-frame conversation-block entry while retaining no
// index-bound data. It must run when a conversation is rebuilt; width changes
// instead self-invalidate through the entries' validity keys.
func (c *blockRenderCache) reset() {
	c.rendered = map[int]blockEntry{}
	c.markdown = map[int]mdEntry{}
	c.frameRows = map[int]frameBlockEntry{}
	c.prefixLines = c.prefixLines[:0]
	c.prefixProvenance = c.prefixProvenance[:0]
	c.prefixN = 0
	c.prefixKey = joinPrefixState{}
}

// resetBlockCaches is the renderer lifecycle hook for a rebuilt conversation.
// The block cache owns all cross-frame conversation state; the renderer retains
// ownership of its independent viewport-output cache.
func (r *renderer) resetBlockCaches() {
	r.blocks.reset()
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
	}
}

func (r *renderer) blockRenderKey(b *block, expanded bool) blockRenderKey {
	return blockRenderKey{revision: b.rev, context: r.renderContext(expanded)}
}
