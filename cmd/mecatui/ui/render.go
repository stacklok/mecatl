package ui

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"unicode"

	"charm.land/bubbles/v2/viewport"
	"charm.land/glamour/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/internal/terminaltext"
	"github.com/stacklok/mecatl/cmd/mecatui/theme"
)

// maxToolResultLines caps how many visible display rows of a tool result are
// shown inline; the rest collapse into a "+N more lines" affordance so a giant
// Read result doesn't drown the scrollback.
const maxToolResultLines = 12

// maxDiffLines caps how many lines of each diff side (Edit old/new, Write
// content) show inline when collapsed; ctrl+t expands to the full diff.
const maxDiffLines = 12

// Compact-card tuning (issue #24): a collapsed tool card summarizes its JSON
// args (and large JSON results) into a few scannable key:value rows instead of
// dumping the full pretty-printed JSON inline — so an MCP call with a huge body
// argument no longer dominates the scrollback. ctrl+t still reveals the full
// JSON (and, for MCP, the raw tool name).
const (
	// maxSummaryRows caps how many key:value rows a collapsed arg/result summary
	// shows; the rest roll up into a "+K more keys · ctrl+t expand" line.
	maxSummaryRows = 8
	// inlinePreviewLen is the rune budget below which a single-line string value is
	// shown inline verbatim ("key: \"value\""); longer/multiline strings collapse
	// to a size + line-count + preview row.
	inlinePreviewLen = 48
	// argPreviewLen is the rune budget for the quoted first-line preview shown for a
	// collapsed long/multiline string value.
	argPreviewLen = 40
	// maxInlineArray is the largest scalar array rendered inline ("[a, b]"); a
	// longer (or non-scalar) array collapses to "N items".
	maxInlineArray = 3
	// resultSummaryByteThreshold is the byte size above which a JSON tool RESULT is
	// considered "large" enough to summarize (in addition to the line-count gate).
	resultSummaryByteThreshold = 600
)

// argPriorityKeys is the deterministic front-of-list ordering for summarized arg
// keys: high-signal "intent" keys first (so a glance reads the call's shape),
// the remaining keys alphabetical after them. Map iteration order is random, so
// this ordering is what makes the collapsed card render stable (golden-safe).
var argPriorityKeys = []string{
	"owner", "repo", "method", "number", "title", "path",
	"query", "state", "branch", "name", "url", "limit", "page",
}

// resultProminentKeys are the fields a large JSON result summary surfaces, in
// render order — the handful a human scans a tool result for (where it landed,
// what it is, its status).
var resultProminentKeys = []string{
	"html_url", "url", "id", "number", "sha", "status", "state",
}

// renderer turns conversation blocks into the viewport string. It owns the
// glamour TermRenderer cache (keyed by wrap width), the active theme, and one
// blockRenderCache component for all cross-frame conversation-block state. Glamour
// is only ever touched from the Bubble Tea update goroutine — never from the
// stream reader. The mutex guards the cache map against the (currently
// single-goroutine) access defensively and documents the invariant; it does not
// make glamour itself concurrency-safe.
type renderer struct {
	th    theme.Theme
	width int

	// traceWidth is the available width of a delegation-inspector card's body. A
	// zero value preserves the transcript renderer's existing trace layout.
	traceWidth int

	// marks carries the LIVE chord markings derived from the model's keyMap at
	// construction (keyMarkings). The inline-card affordances that reference
	// rebindable actions — the ExpandTools chord ("ctrl+t" by default) in the
	// reasoning/subagent/team headers and the collapse/rollup markers, and the
	// Agents chord ("f6") in the team "+N more" roll-up — read them off
	// here so an override propagates to those affordances (issue #457, the
	// #455 liveness pattern extended to inline cards). Set once at construction
	// from keyMarkings; a bare &renderer{th: th} (the width-0 team/fleet focus
	// panes) is seeded with defaultHelpKeys() so its output stays
	// byte-identical to the pre-#457 default.
	marks helpKeys

	// indent is the left margin (in cells) prepended to EVERY conversation block so the
	// history aligns with the 1-col-padded header/footer instead of sitting flush at
	// column 0. It is applied in renderBlock (the cache-miss path) by prefixing each
	// rendered line with `indent` spaces, and SUBTRACTED from the layout budget by
	// contentWidth() so wrapped lines never overflow. The prefixed spaces are REAL
	// content cells, so the selection screen↔content x-mapping (selection.go
	// graphemeColForCellX over the rendered line) stays identity — no viewport XOffset,
	// no mapping adjustment. Set once at construction (defaultBlockIndent); a width-0
	// bare renderer (team/fleet focus panes) leaves it 0.
	indent int

	mu    sync.Mutex
	cache map[int]*glamour.TermRenderer

	// blocks owns every cross-frame conversation-block cache: whole rendered
	// blocks, assistant markdown, fallback frame rows, and the incremental prefix.
	// It is reset when a conversation is rebuilt because its buckets use conversation
	// indices that a fresh conversation can reuse.
	blocks blockRenderCache

	// mdRenders counts REAL glamour invocations (cache misses) — incremented at the
	// tr.Render call site in markdown(), not in markdownAt's hit path. It is the test
	// seam proving the delta-coalescing actually elides per-token renders: N streamed
	// deltas with no frame flush leave it unchanged, and one flush bumps it by exactly
	// one (the live block re-renders once). Touched only on the update goroutine.
	mdRenders int

	// blockRenders counts REAL whole-block renders (blockRenderCache misses) — incremented
	// only when renderBlock has a cache miss, never on a cache hit. It is the test
	// seam proving settled blocks join from cache: a flushed frame of a streaming turn bumps it by exactly one (the live block), regardless of how
	// long the scrollback is. Touched only on the update goroutine.
	blockRenders int

	// cardPrepares counts all migrated functional-card preparations. The counter is
	// deliberately below renderBlock's composite-key guard so tests can prove a
	// settled frame performs no snapshot or preparation.
	cardPrepares int

	// toolCardPrepares counts tool-card preparations. A fresh tool block prepares
	// once for both its rendered output and structural frame provenance; semantic
	// sections are discarded before the cache entry is retained.
	toolCardPrepares int

	// inputKey/inputView/inputValid memoize the rendered INPUT region (the bubbles
	// textarea) — the input-side sibling of blockRenderCache. textarea.View() re-wraps
	// (and SHA-256-keys, even on its internal cache hits) every logical line on
	// every call, and renderInput runs at least twice per reduced message (the
	// relayout chokepoint's chrome() + View's assembleLayout), so an unchanged
	// input was re-wrapped on every streamed-delta frame. The memo is KEYED ON
	// STATE (inputRenderKey — a self-contained signature of every textarea fact
	// the render reads), NOT dirty-flagged: the textarea is mutated from ~30 call
	// sites across the ui (overlay focus/blur, palette/mention SetValue, the
	// reset/insert funnels), and one missed dirty-set at a future site would
	// freeze the input; the key compare is O(len(input)), which the large-paste
	// placeholder staging keeps small. Single entry, refreshed on every render —
	// see renderInput for the correctness argument covering the textarea's hidden
	// state (internal scroll offset, cursor blink phase). The active permission mode
	// controls the input's colour cue, so it is part of the key too. Update-goroutine-only,
	// like the block caches; deliberately NOT dropped by resetBlockCaches (the key
	// is self-validating — it carries no conversation index to alias).
	inputKey   inputRenderKey
	inputView  string
	inputValid bool

	// renderedBlocksScratch is renderer-owned allocation reuse for one block-render
	// pass. Consumers receive the returned slice from walkBlocks explicitly.
	renderedBlocksScratch []string

	// vpViewCache/vpViewValid memoize the rendered VIEWPORT OUTPUT — the OUTERMOST
	// render layer, above blockRenderCache. View() calls vp.View() which runs
	// lipgloss's per-line grapheme-width pad on the full visible window (~40 lines at
	// a time). On a spinner-only frame (no content/scroll/geometry change) this work
	// is pure waste: the viewport output is identical to the previous frame. The memo
	// is DIRTY-FLAGGED (not key-based) because it is invalidated at every site that
	// changes viewport content, scroll offset, or geometry — those sites are fewer and
	// easier to enumerate than reconstructing a key from the viewport's full internal
	// state (vp.View has no stable comparable key exposed). invalidateVPView must be
	// called at every such site; vpView serves from cache otherwise. Update-goroutine-only.
	//
	// One nuance on the "scroll offset" part of that contract: scrollLines invalidates
	// only on OBSERVED YOffset movement (its gate is `m.vp.YOffset() != before`), so a
	// scroll that does not actually move YOffset does not invalidate — a future scroll
	// behaviour that changes the rendered output WITHOUT moving YOffset (e.g. a
	// horizontal/partial-line offset) would need to add its own invalidation site.
	vpViewCache string
	vpViewValid bool

	// frameProvenanceScratch assembles one frame's lockstep rows without a fresh
	// full-scrollback allocation on every streaming tick. It is current-frame
	// allocation reuse, not cross-frame cache state.
	frameProvenanceScratch []renderedRow
}

// inputRenderKey is the validity key of the memoized input render: the complete
// set of textarea facts renderInput's output is a pure function of — the buffer
// content, the cursor position (logical row + soft-wrap row/column offsets, so a
// cursor move inside an unchanged value still re-renders), the selection state
// (active plus normalized logical start/end positions), the focus state (which
// also fully determines the virtual cursor's blink phase — mecatui never routes
// cursor.BlinkMsg to the textarea, so the cursor is static: visible while focused,
// hidden while blurred; if blink routing is ever added, the blink phase must join
// this key), the box dimensions, and the placeholder. The theme and prompt are
// fixed per process and need no key slot.
//
// The placeholder is keyed because it is NOT fixed per process: the newline chord
// it advertises is rewritten if the terminal proves it cannot deliver the chord
// mecatui started on (see settleKeyboardProbe). That correction arrives on a
// timer with no other model change behind it, so without a key slot the corrected
// hint would sit in the model unpainted until unrelated input happened to re-key
// the cache — which is precisely when the user no longer needs it.
type inputRenderKey struct {
	value                              string
	row                                int // cursor's logical line (textarea.Line)
	rowOffset                          int // cursor's soft-wrap row within that line (LineInfo.RowOffset)
	colOffset                          int // cursor's column within that soft-wrap row (LineInfo.ColumnOffset)
	selection                          bool
	selectionFromRow, selectionFromCol int
	selectionToRow, selectionToCol     int
	focused                            bool
	width, height                      int
	mode                               string
	placeholder                        string
}

// defaultBlockIndent is the left margin (cells) every conversation block is indented
// by, so the history aligns with the 1-col-padded header/footer chrome (which use
// Padding(0,1)) instead of sitting flush at column 0. One column matches the chrome
// exactly. The width-0 team/fleet focus renderers (bare &renderer{}) keep indent 0.
const defaultBlockIndent = 1

// assistantBodyHang is the extra left indent (cells) the assistant MESSAGE body hangs
// under its "● mecatl" label, so the body text sits under "mecatl" rather than flush
// under the "●" bullet — matching the user block, whose gold rail + PaddingLeft(1)
// already lands its body under "you". It equals the label marker width
// lipgloss.Width("● ") = 2. Applied ON TOP of the base block indent (renderBlock), to
// the body ONLY (reasoning summary + markdown answer) — never the label, and never the
// non-message blocks (tool cards / notices / turn-stats / errors). The assistant
// markdown wrap budget subtracts it (see markdown) so a wrapped body line + base +
// hang never exceeds r.width.
const assistantBodyHang = 2

// newRenderer builds a renderer for a theme, seeded with the LIVE chord
// markings (hk) so inline-card affordances that reference rebindable actions
// (ExpandTools/Agents) reflect any override (issue #457). With default keys hk
// resolves to exactly the literals the affordances used to hardcode, so the
// goldens stay byte-identical.
func newRenderer(th theme.Theme, hk helpKeys) *renderer {
	return &renderer{
		th:     th,
		marks:  hk,
		indent: defaultBlockIndent,
		cache:  map[int]*glamour.TermRenderer{},
		blocks: blockRenderCache{},
	}
}

// contentWidth is the layout budget available to a block's CONTENT: the viewport
// width minus the left indent. Every per-block width consumer (wrapStyled,
// wrapPrefixed, markdown, the tool card) lays out against this so that, once each
// rendered line is prefixed with `indent` spaces in renderBlock, the total never
// exceeds r.width. A width-0/tiny renderer (indent 0) collapses to r.width unchanged.
func (r *renderer) contentWidth() int {
	if r.width <= r.indent {
		return r.width // unknown/tiny: don't go non-positive; the helpers guard further.
	}
	return r.width - r.indent
}

// indentLines prefixes every line of a rendered block with `indent` spaces — the
// uniform left margin that aligns the conversation history with the 1-col-padded
// header/footer. It is called ONCE per block on the cache-miss path (renderBlock), so
// the indented string is what blockRenderCache stores and every steady-state frame joins the
// already-indented line straight from cache (no per-frame indent cost). The prefixed
// spaces are REAL content cells, so the selection x-mapping stays identity (see the
// `indent` field doc). indent 0 (a bare/width-0 renderer) returns s unchanged with no
// allocation. Blank lines are indented too, so a multi-row block's left edge is straight.
func (r *renderer) indentLines(s string) string {
	return padLines(s, r.indent)
}

// padLines prefixes every line of s (including blank lines, so a multi-row block's left
// edge stays straight) with n spaces. n <= 0 returns s unchanged with no allocation. It
// is the shared core of the base indent (indentLines) and the assistant body hang (the
// assistantBodyHang applied in the blockAssistant arm). The prefixed spaces are real
// content cells, so the selection x-mapping stays identity.
func padLines(s string, n int) string {
	if n <= 0 {
		return s
	}
	pad := strings.Repeat(" ", n)
	var b strings.Builder
	b.Grow(len(s) + n*(strings.Count(s, "\n")+1))
	for i, line := range strings.Split(s, "\n") {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(pad)
		b.WriteString(line)
	}
	return b.String()
}

// setWidth records the current wrap width. Width changes are handled by the
// cache key, so no explicit invalidation is needed.
func (r *renderer) setWidth(w int) { r.width = w }

// invalidateVPView marks the vpView cache as stale. Call at every site that
// changes viewport content, scroll offset, or geometry.
func (r *renderer) invalidateVPView() {
	r.vpViewValid = false
}

// vpView returns the rendered viewport output, serving from cache when the
// viewport's content/scroll/geometry haven't changed since the last render.
// This avoids the per-frame lipgloss grapheme-width pad during spinner-only frames.
// The caller passes the current viewport model by value — it is not mutated.
func (r *renderer) vpView(vp viewport.Model) string {
	if r.vpViewValid {
		return r.vpViewCache
	}
	r.vpViewCache = vp.View()
	r.vpViewValid = true
	return r.vpViewCache
}

// markdownWidth renders src to ANSI through a width-cached glamour renderer
// themed by the active theme, wrapping at the caller-provided width w. It applies
// normalizeEmojiWidth (the streaming-scramble fix) and trims trailing spaces.
// When w <= 0 it floors at 80; the final column is reserved (w-- when w > 1).
// On any glamour error it falls back to the raw text so the stream is never lost.
// MUST be called only on the update goroutine.
//
// markdownWidth is the single glamour-render entry-point shared by the
// conversation markdown path (via markdown) and the plan-approval modal — the
// plan modal wraps at the card's content width rather than the transcript budget.
// The glamour cache is width-keyed, so each distinct caller width gets its own
// renderer entry.
func (r *renderer) markdownWidth(src string, w int) string {
	if strings.TrimSpace(src) == "" {
		return ""
	}
	// Normalise emoji presentation BEFORE glamour wraps or renders. This is the
	// real fix for the streaming-scramble bug. The two layers measure cell width
	// with DIFFERENT methods:
	//
	//   - glamour's word-wrap (lipgloss.Wrap → ansi.Wrap) is hard-wired to
	//     GraphemeWidth (clipperhouse/displaywidth): a VS16 (U+FE0F) presentation
	//     selector promotes its base char to a width-2 cluster, a ZWJ sequence is
	//     one cluster.
	//   - Bubble Tea v2's differential renderer (cursedRenderer → ultraviolet)
	//     defaults to WcWidth (mattn/go-runewidth, summing each rune), and only
	//     upgrades to GraphemeWidth if the terminal CONFIRMS DEC mode 2027 — which
	//     Apple Terminal, most SSH sessions, and non-allowlisted terminals never
	//     reply to.
	//
	// So glamour lays a line out on one column grid and the renderer paints/diffs
	// it on another. On a cluster where the two widths differ (e.g. "❤️" is
	// GraphemeWidth 2 / WcWidth 1) every cell to the right is offset — the
	// scramble ("mecatl" → "mec##atl", "1. ✅" losing its ". "). It PERSISTS after
	// the stream settles because the renderer's width method is a fixed terminal
	// property, so the end-of-turn ClearScreen just re-paints the same wrong
	// layout. normalizeEmojiWidth strips VS16 and collapses any residual divergent
	// cluster so WcWidth == GraphemeWidth for every cluster — the two layers then
	// agree without depending on the terminal upgrading the renderer.
	src = normalizeEmojiWidth(src)
	if w <= 0 {
		w = 80
	}
	// Reserve the terminal's FINAL column: word-wrap one column short of the
	// viewport width. Retained as harmless hygiene, NOT as the scramble fix — the
	// width-method disagreement above, not a last-column pending-wrap, is the root
	// cause, and normalizeEmojiWidth is what closes it. trimTrailingSpaces likewise
	// just drops glamour's styled right-padding so rows sit at their natural width.
	if w > 1 {
		w--
	}
	r.mu.Lock()
	if r.cache == nil {
		// Zero-value safety: a bare &renderer{th: th} (see the renderBlock comment)
		// never went through newRenderer; lazy-init so any path reaching glamour on
		// one stays safe.
		r.cache = map[int]*glamour.TermRenderer{}
	}
	tr, ok := r.cache[w]
	if !ok {
		built, err := glamour.NewTermRenderer(
			glamour.WithStyles(r.th.GlamourStyle()),
			glamour.WithWordWrap(w),
		)
		if err != nil {
			r.mu.Unlock()
			return src
		}
		tr = built
		r.cache[w] = tr
	}
	r.mu.Unlock()

	// Count the real glamour invocation (cache miss). markdownAt's hit path returns
	// before reaching here, so this counts only genuine renders — the test seam for
	// the delta-coalescing (see the mdRenders field).
	r.mdRenders++
	out, err := tr.Render(src)
	if err != nil {
		return src
	}
	return trimTrailingSpaces(strings.TrimRight(out, "\n"))
}

// markdown renders src to ANSI through a width-cached glamour renderer at the
// assistant body's wrap budget (content width minus the body hang). It delegates
// to markdownWidth with the caller-independent width already computed.
func (r *renderer) markdown(src string) string {
	w := r.contentWidth() - assistantBodyHang
	return r.markdownWidth(src, w)
}

// markdownAt is the memoized form of markdown used by the conversation render
// path. idx is the block's stable conversation index (blocks are append-only, so
// an index always denotes the same logical block). It returns the cached render
// when the block's (src, width) are unchanged — the common case for every SETTLED
// block on each streamed delta — and otherwise renders fresh and stores the
// result, overwriting the index's entry in place (so the live, growing block
// keeps exactly one entry rather than accumulating one per token). Correctness
// rests on markdown() being pure in (src, width, theme) with a fixed theme; the
// cache therefore can never return a stale render. Update-goroutine-only.
func (r *renderer) markdownAt(idx int, src string) string {
	if out, ok := r.blocks.markdownBlock(idx, src, r.width); ok {
		return out
	}
	out := r.markdown(src)
	r.blocks.storeMarkdown(idx, mdEntry{src: src, width: r.width, out: out})
	return out
}

// trimTrailingSpaces strips the per-line right-padding glamour adds to fill every
// wrapped line out to the full wrap width. Retained as harmless hygiene, NOT as
// the scramble fix: the streaming scramble is a width-method disagreement between
// glamour's GraphemeWidth wrap and the renderer's WcWidth paint (see markdown and
// normalizeEmojiWidth), which trailing-space trimming does not touch. Trimming
// the bare trailing spaces (only the unstyled padding after glamour's final reset;
// an in-band styled space ends before its reset, so TrimRight never touches it)
// keeps each row at its natural width and drops the wasted bytes. The cell
// renderer still pads to the terminal width internally, so the on-screen result is
// unchanged.
func trimTrailingSpaces(s string) string {
	lines := strings.Split(s, "\n")
	for i, ln := range lines {
		lines[i] = strings.TrimRight(ln, " ")
	}
	return strings.Join(lines, "\n")
}

// variationSelector16 is U+FE0F, the emoji-presentation variation selector. It
// carries no text of its own; its only effect is to request the emoji (width-2)
// presentation of the preceding character. GraphemeWidth honours it (promoting the
// base to a width-2 cluster) while WcWidth ignores it, so it is the single biggest
// source of the width-method disagreement that scrambles streamed markdown.
const variationSelector16 = '️'

// widthDivergentPlaceholder replaces any grapheme cluster that still has
// WcWidth != GraphemeWidth after the cheaper rescues (VS16-strip, first-scalar).
// It is U+FFFD REPLACEMENT CHARACTER, verified width-1 under BOTH methods (a
// .scratch probe confirmed WcWidth==GraphemeWidth==1), so substituting it
// GUARANTEES the per-cluster postcondition. The classic trigger is a
// regional-indicator FLAG (🇺🇸): one cluster, GraphemeWidth 2 / WcWidth 1, whose
// first scalar (🇺) is ALSO 2/1 — so first-scalar can't rescue it and emitting a
// partial cluster would both corrupt the flag and still violate the invariant.
const widthDivergentPlaceholder = "�"

// normalizeEmojiWidth rewrites src so that, for every grapheme cluster, the two
// cell-width methods agree (WcWidth == GraphemeWidth). It is the production fix for
// the streaming-scramble bug documented on markdown(): glamour wraps on
// GraphemeWidth and Bubble Tea's renderer paints on WcWidth on terminals that do
// not confirm DEC mode 2027, so any width-divergent cluster offsets every cell to
// its right.
//
// Clustering uses ansi.FirstGraphemeCluster — the SAME segmentation engine glamour
// and lipgloss use for their width math — so the normalizer can never disagree
// with the layout layer about where a cluster begins, which is the exact class of
// disagreement this whole fix is about.
//
// The transform is pure and minimally lossy. Per grapheme cluster, the agreement is
// restored by the FIRST of these steps whose result actually agrees (re-checked
// after each step), so a cluster is never mangled more than necessary:
//
//  1. As-is. Already-agreeing clusters (bare ✅ U+2705 and a letter + combining
//     accent like á) pass through
//     byte-for-byte.
//  2. Strip U+FE0F (VS16). Reconciles the common divergent clusters (❤️, ⚠️, ℹ️
//     all go from WcWidth 1 / GraphemeWidth 2 to a stable width 1) and the keycap
//     form (1️⃣ → 1⃣, width 1 both ways).
//  3. First scalar of the (VS16-stripped) cluster, when that scalar agrees.
//  4. Otherwise, substitute U+FFFD — a width-stable placeholder both methods size
//     identically. This is the only step that guarantees the postcondition for a
//     cluster (like a flag) whose every prefix still diverges; emitting a partial
//     cluster there would re-arm the scramble.
//
// Every already-agreeing rune and all surrounding text, order, and whitespace are
// preserved exactly. The helper short-circuits when src has no clusters needing
// work, so the common all-ASCII / agreeing-emoji case allocates nothing.
func normalizeEmojiWidth(src string) string {
	if !needsEmojiWidthNorm(src) {
		return src
	}
	var b strings.Builder
	b.Grow(len(src))
	rest := src
	for len(rest) > 0 {
		// Cluster boundaries from the same engine glamour/lipgloss use; the width is
		// taken via the StringWidth helpers so both methods are measured consistently.
		cl, _ := ansi.FirstGraphemeCluster(rest, ansi.GraphemeWidth)
		rest = rest[len(cl):]
		b.WriteString(reconcileClusterWidth(cl))
	}
	return b.String()
}

// reconcileClusterWidth returns a rendering of the single grapheme cluster cl for
// which WcWidth == GraphemeWidth, trying the least-lossy rescue first (see
// normalizeEmojiWidth's step list). cl MUST be exactly one cluster.
func reconcileClusterWidth(cl string) string {
	if widthMethodsAgree(cl) {
		return cl
	}
	if stripped := stripVS16(cl); stripped != cl && widthMethodsAgree(stripped) {
		return stripped
	} else if stripped != cl {
		cl = stripped // carry the VS16-stripped form into the first-scalar attempt
	}
	if first := firstScalar(cl); first != "" && widthMethodsAgree(first) {
		return first
	}
	return widthDivergentPlaceholder
}

// widthMethodsAgree reports whether s has the same display width under WcWidth
// (the renderer's paint method) and GraphemeWidth (glamour's wrap method).
func widthMethodsAgree(s string) bool {
	return ansi.StringWidthWc(s) == ansi.StringWidth(s)
}

// firstScalar returns the first Unicode scalar of s as a string (the rune that
// anchors a grapheme cluster), or "" for an empty string.
func firstScalar(s string) string {
	for _, r := range s {
		return string(r)
	}
	return ""
}

// needsEmojiWidthNorm reports whether src contains any rune that could make a
// grapheme cluster's WcWidth differ from its GraphemeWidth — i.e. a VS16 selector
// or any non-ASCII rune (ASCII is always width-1 under both methods). It lets the
// hot path skip the grapheme walk entirely for the overwhelmingly common
// plain-text case.
func needsEmojiWidthNorm(src string) bool {
	for _, r := range src {
		if r == variationSelector16 || r > 0x7F {
			return true
		}
	}
	return false
}

// stripVS16 removes every U+FE0F variation selector from s.
func stripVS16(s string) string {
	if !strings.ContainsRune(s, variationSelector16) {
		return s
	}
	return strings.Map(func(r rune) rune {
		if r == variationSelector16 {
			return -1
		}
		return r
	}, s)
}

// walkBlocks renders every block using the renderer-owned reusable backing slice.
// It returns that render-pass output explicitly with the lowest changed block index.
func (r *renderer) walkBlocks(c *conversation, expand bool) ([]string, int) {
	firstChanged := len(c.blocks)
	r.renderedBlocksScratch = r.renderedBlocksScratch[:0]
	for i := range c.blocks {
		before := r.blockRenders
		r.renderedBlocksScratch = append(r.renderedBlocksScratch, r.renderBlock(i, &c.blocks[i], expand))
		if r.blockRenders != before && i < firstChanged {
			firstChanged = i
		}
	}
	return r.renderedBlocksScratch, firstChanged
}

// renderConversationLines returns the conversation as single newline-free lines
// ready for vp.SetContentLines. It reuses the cached prefix of settled blocks and
// builds only the changed suffix.
//
// Canonical segment framing: block i contributes sep(i) + renderedBlocks[i] +
// "\n", where sep(0)="" and sep(i>0)="\n".
// The prefix is the line-split of segments [0, prefixN); the suffix is the
// line-split of segments [prefixN, n). prefixN = min(firstChanged, n): blocks below
// firstChanged did NOT re-render, so they are byte-identical to last frame.
//
// Correctness invalidation (a stale prefix is worse than the perf cost):
// blockRenderCache.prefix reuses the cached leading range ONLY when it covers
// EXACTLY [0, prefixN) at the SAME (width, expand). Its prefix coverage check is
// the load-bearing guard: a NON-TAIL mutation lowers firstChanged (→ prefixN), a
// block-count shrink lowers prefixN, and a width/expand change re-renders EVERY
// block so firstChanged drops to 0 (→ prefixN). Those paths make the requested
// coverage or joinPrefixState differ; storeRendered independently clears a prefix
// when a freshly rendered block was already covered by it.
//
// The returned slice is freshly allocated EACH call (cached prefix lines appended
// into a new backing array, then suffix lines), so vp.SetContentLines — which
// retains and may mutate the slice it is handed (slices.Insert on embedded
// newlines) — never corrupts blockRenderCache's cached prefix. The slice safety
// rests on TWO facts: the fresh backing array (SetContentLines mutates that array,
// not the cache-owned prefix), and Go string immutability (the prefix STRINGS are
// shared by value but can never be mutated in place). It does NOT rely on the
// prefix lines being newline-free — they are split single lines, but
// SetContentLines is free to re-split them and the fresh array still absorbs the
// result.
func (r *renderer) renderConversationLines(c *conversation, expand bool) []string {
	return r.renderConversationFrame(c, expand).lines
}

// The inter-block separator is written BEFORE every block after the first by the
// string-path join (renderConversation); each block already ends with a trailing "\n",
// so the on-screen gap between two blocks is (trailing "\n") + separator. The lines-path
// (appendSegmentLines) MUST mirror this exactly (the matching blank-"" count before each
// block i>0) or the cache-equivalence oracle (render_cache_test.go) trips.
//
// blockSepAfter / blockBlankLinesAfter encode per-transition spacing rules; both
// paths (string and lines) must use them so the cache-equivalence oracle holds.
//
// Spacing policy (compact throughout):
//   - blockTool → any                : 0 blank lines — tool boxes cluster tight
//   - blockAssistant → blockTurnStat : 0 blank lines — empty turns need no gap before stats
//   - blockTurnStat → any            : 1 blank line  — compact stat annotation
//   - everything else                : 1 blank line  — user↔assistant, assistant→tool, etc.
const (
	interBlockSepCompact        = "\n"
	interBlockBlankLinesCompact = 1 // == strings.Count(trailing-"\n" + interBlockSepCompact, "\n") - 1

	interBlockSepNone        = ""
	interBlockBlankLinesNone = 0 // == strings.Count(trailing-"\n" + interBlockSepNone, "\n") - 1
)

// blockSepAfter returns the inter-block separator to write AFTER block i (i.e.
// before block i+1).
func blockSepAfter(conversationBlocks []block, i int) string {
	switch conversationBlocks[i].kind {
	case blockTool:
		return interBlockSepNone
	case blockTurnStat:
		return interBlockSepCompact
	case blockAssistant:
		if i+1 < len(conversationBlocks) && conversationBlocks[i+1].kind == blockTurnStat {
			return interBlockSepNone
		}
	}
	return interBlockSepCompact
}

// blockBlankLinesAfter is the lines-path mirror of blockSepAfter: it returns the
// number of blank "" lines to insert before block i (i.e. after block i-1).
func blockBlankLinesAfter(conversationBlocks []block, i int) int {
	switch conversationBlocks[i-1].kind {
	case blockTool:
		return interBlockBlankLinesNone
	case blockTurnStat:
		return interBlockBlankLinesCompact
	case blockAssistant:
		if i < len(conversationBlocks) && conversationBlocks[i].kind == blockTurnStat {
			return interBlockBlankLinesNone
		}
	}
	return interBlockBlankLinesCompact
}

// renderBlock is the CACHED per-block entry point: it returns the memoized
// render when the block's revision, the wrap width, and the expand toggle all
// match the cached entry, and otherwise renders fresh, stores the result, and
// bumps blockRenders (the cache-miss test seam). idx is the block's stable conversation index (blocks are append-only within a
// conversation; resetBlockCaches handles index reuse across rebuilds).
// Correctness rests on the block.rev discipline: every post-append mutation of a
// render-visible field bumps rev through a conversation gateway, so a cache hit
// can never be stale. Update-goroutine-only.
func (r *renderer) renderBlock(idx int, b *block, expand bool) string {
	key := r.blockRenderKey(b, expand)
	if entry, ok := r.blocks.renderedBlock(idx, key); ok {
		return entry.out
	}
	var (
		out  string
		rows []renderedRow
	)
	if prepared, ok := r.prepareStructuredBlock(b, expand); ok {
		out = prepared.Text()
		rows = blockProvenanceRows(prepared, b.id, b.kind, r.indent, r.width)
	} else {
		out = r.renderBlockFresh(idx, b, expand)
	}
	// contentWidth preserves a positive width for a tiny renderer. A tool card uses
	// that cell for its frameless fallback, so it cannot also carry the usual indent.
	if b.kind != blockTool || r.width > r.indent {
		out = r.indentLines(out)
	}
	r.blocks.storeRendered(idx, blockEntry{key: key, out: out, rows: rows})
	r.blockRenders++
	return out
}

// renderBlockFresh renders one block per its kind. Assistant text goes through
// glamour; everything else is plain themed lipgloss. idx is the block's stable
// conversation index, used to memoize the (expensive) assistant glamour render
// across the per-delta full-scrollback re-render — see markdownAt (the inner
// memo layer below renderBlock's whole-block cache).
func (r *renderer) renderBlockFresh(idx int, b *block, expand bool) string {
	switch b.kind {
	case blockUser:
		return r.prepareUserBlock(b).Text()
	case blockAssistant:
		// Assistant text is rendered through glamour, which neutralises escape
		// sequences itself — do NOT sanitize here or markdown breaks. The turn's
		// reasoning summary (if any) renders dim and collapsed ABOVE the answer.
		// The label "● mecatl" stays at the base indent; the BODY (reasoning summary +
		// markdown answer) hangs by assistantBodyHang so it sits under "mecatl" — matching
		// the user block's body-under-"you" alignment. The markdown was already wrapped at
		// contentWidth()-hang (see markdown), so hang + base never overflows r.width.
		// ONE blank line separates the label from the body (a little vertical breathing
		// room under "● mecatl") — within-block spacing, distinct from the inter-turn
		// 2-line join gap. The user block is deliberately NOT given this gap: its gold rail
		// visually connects label→body, and a mid-gap would break the rail.
		label := r.th.Style("assistantLabel").Render("● mecatl")
		body := padLines(r.markdownAt(idx, b.raw), assistantBodyHang)
		if reasoning := r.renderReasoning(b, expand); reasoning != "" {
			body = padLines(reasoning, assistantBodyHang) + "\n" + body
		}
		// label + blank line + body. The blank line is unindented (it is empty).
		return label + "\n\n" + body
	case blockTool:
		return r.renderTool(b, expand)
	case blockNotice:
		return r.prepareNoticeBlock(b).Text()
	case blockHook:
		return r.prepareHookBlock(b).Text()
	case blockTurnStat:
		return r.prepareTurnStatBlock(b).Text()
	case blockError:
		if b.permanent {
			return r.preparePermanentErrorBlock(b, expand).Text()
		}
		return r.prepareErrorBlock(b).Text()
	case blockDelivery:
		return r.prepareDeliveryBlock(b).Text()
	default:
		return r.wrapStyled(terminaltext.Sanitize(b.raw), lipgloss.NewStyle())
	}
}

// reasoningCaveat is the dim one-line disclaimer prepended to the EXPANDED
// reasoning. It signals the prose is a lossy summary, not the model's actual
// process — streamed chain-of-thought is often unfaithful and drives
// over-reliance, so it must never read as ground truth.
const reasoningCaveat = "— summary of the model's reasoning; may not reflect its actual process"

// renderReasoning renders the dim, collapsed-by-default reasoning summary that
// belongs to an assistant block. It returns "" when the block carries no
// reasoning. Collapsed (the default) it is a single dim header: while reasoning
// is still streaming and no answer text has begun it reads "reasoning…" (a live
// "the model is working" affordance); otherwise it is the static
// "reasoning summary · N lines · ctrl+t expand". When the global details toggle
// (expand) is on, a dim caveat plus the full summary text are shown with no line
// cap (a long chain-of-thought no longer truncates once the user has explicitly
// asked to see it — matching how resultBody handles tool results). Streamed
// reasoning is never a trust anchor: hidden unless explicitly asked for, and
// clearly labelled as a lossy summary.
func (r *renderer) renderReasoning(b *block, expand bool) string {
	if b.reasoning == "" {
		return ""
	}
	style := r.th.Style("reasoning")
	text := terminaltext.Sanitize(strings.TrimRight(b.reasoning, "\n"))
	n := lineCount(text)
	expandMark := r.marks.expandTools
	if !expand {
		if b.reasoningStreaming {
			return style.Render("reasoning…")
		}
		return style.Render("reasoning summary · " + plural(n, "line") + " · " + expandMark + " expand")
	}
	header := style.Render("reasoning summary · " + plural(n, "line") + " · " + expandMark + " collapse")
	return header + "\n" + r.wrapStyled(reasoningCaveat, style) + "\n" + r.wrapStyled(text, style)
}

// wrapStyled word-wraps s to the CONTENT width (viewport minus the left indent)
// MINUS the style's own horizontal frame (border+padding+margin, via
// GetHorizontalFrameSize — the single source of truth, so the wrap budget tracks
// theme.go edits automatically and never drifts behind a hardcoded inset) and renders
// it through st. A content width at or below the frame (e.g. the width-0 team focus
// renderer, team.go) means "unknown/tiny: do not wrap" and the body renders unwrapped.
// Wrapping against contentWidth (not r.width) keeps the body within budget once
// renderBlock prefixes each line with `indent` spaces.
func (r *renderer) wrapStyled(s string, st lipgloss.Style) string {
	frame := st.GetHorizontalFrameSize()
	cw := r.contentWidth()
	if cw <= frame+1 {
		return st.Render(s)
	}
	// Normalise emoji presentation BEFORE ansi.Wrap, for the same reason the
	// glamour path does (see markdown's long comment): ansi.Wrap measures cells
	// on GraphemeWidth while the viewport paints on WcWidth, so a divergent
	// cluster (e.g. a VS16 emoji) would wrap to a line that then overflows under
	// the paint width — the exact overflow this wrapping exists to prevent.
	return st.Render(ansi.Wrap(normalizeEmojiWidth(s), cw-frame, ""))
}

// plural formats a count with a noun, pluralising with a trailing "s" for any
// count other than 1 (e.g. 0 lines, 1 line, 3 lines).
func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return strconv.Itoa(n) + " " + noun + "s"
}

// toolCardLayout returns the styled card, its outer width, and its usable body
// width. renderTool uses the same body width to wrap and cap collapsed results,
// keeping result-row accounting aligned with the final card layout.
func (r *renderer) toolCardLayout() (card lipgloss.Style, outerWidth, bodyWidth int) {
	card = r.th.Style("toolCard")
	if cw := r.contentWidth(); cw > card.GetHorizontalFrameSize() {
		// Width includes the card's border and padding. Fill the conversation-content
		// budget so the block indent plus card reaches the terminal's final column.
		outerWidth = cw
		card = card.Width(outerWidth)
	} else if cw > 0 {
		// A bordered, padded card has no content column at this width. Start from an
		// unframed style so inherited border and padding settings cannot widen it past
		// the viewport.
		outerWidth = cw
		card = lipgloss.NewStyle().Width(outerWidth)
	}
	if outerWidth > 0 {
		bodyWidth = outerWidth - card.GetHorizontalFrameSize()
	}
	return card, outerWidth, bodyWidth
}

// renderToolHeader applies the status and tool-name styles only after the raw
// header has been wrapped to the card body. The status glyph keeps its local
// style on the first row; wrapped name rows retain the tool-name style.
func renderToolHeader(glyph, glyphText, label string, nameStyle lipgloss.Style, bodyWidth int) string {
	rows := strings.Split(wrapToolCardText(glyphText+" "+label, bodyWidth), "\n")
	for i, row := range rows {
		if i == 0 {
			rows[i] = glyph + nameStyle.Render(strings.TrimPrefix(row, glyphText))
			continue
		}
		rows[i] = nameStyle.Render(row)
	}
	return strings.Join(rows, "\n")
}

// renderToolCardText wraps plain card content before applying one region's
// existing style, so ANSI styling cannot affect width accounting.
func renderToolCardText(style lipgloss.Style, text string, bodyWidth int) string {
	text = strings.TrimRightFunc(terminaltext.Sanitize(text), unicode.IsSpace)
	return renderRawToolCardText(style, text, bodyWidth)
}

// renderToolMetadata bounds Edit/Write metadata without changing its raw whitespace.
func renderToolMetadata(style lipgloss.Style, text string, bodyWidth int) string {
	return renderRawToolCardText(style, terminaltext.Sanitize(text), bodyWidth)
}

func renderRawToolCardText(style lipgloss.Style, text string, bodyWidth int) string {
	rows := strings.Split(wrapToolCardText(text, bodyWidth), "\n")
	for i, row := range rows {
		rows[i] = style.Render(row)
	}
	return strings.Join(rows, "\n")
}

// renderDelegationToolCardText preserves delegation-source whitespace while
// constraining every raw row before styles can add their own layout padding.
func renderDelegationToolCardText(style lipgloss.Style, text string, bodyWidth int) string {
	rows := strings.Split(wrapToolCardText(terminaltext.Sanitize(text), bodyWidth), "\n")
	for i, row := range rows {
		rows[i] = style.Render(row)
	}
	return strings.Join(rows, "\n")
}

// toolCardTabWidth is the fixed displayed-code indentation width. Tool cards expand
// literal tabs before wrapping so ansi.Hardwrap and Lip Gloss measure identical text.
const toolCardTabWidth = 4

func normalizeToolCardTabs(text string) string {
	return strings.ReplaceAll(text, "\t", strings.Repeat(" ", toolCardTabWidth))
}

// wrapToolCardText constrains raw card text before it is styled or framed.
func wrapToolCardText(text string, bodyWidth int) string {
	text = normalizeEmojiWidth(normalizeToolCardTabs(text))
	if text == "" || bodyWidth <= 0 {
		return text
	}
	return ansi.Hardwrap(text, bodyWidth, true)
}

// renderCardChromeSegments packs complete semantic chrome segments into one bounded row.
// It retains only a priority prefix and reserves room for an omission marker, so a live
// rebound key/action label is never split by a generic character truncation.
func renderCardChromeSegments(style lipgloss.Style, segments []string, width int) string {
	clean := make([]string, 0, len(segments))
	for _, segment := range segments {
		segment = strings.Map(func(r rune) rune {
			if unicode.IsSpace(r) {
				return ' '
			}
			return r
		}, terminaltext.Sanitize(segment))
		if segment != "" {
			clean = append(clean, segment)
		}
	}
	if width <= 0 {
		return style.Render(strings.Join(clean, " · "))
	}
	for n := len(clean); n >= 0; n-- {
		line := strings.Join(clean[:n], " · ")
		if n < len(clean) {
			if line != "" {
				line += " · "
			}
			line += "..."
		}
		if lipgloss.Width(line) <= width {
			return style.Render(line)
		}
	}
	return style.Render("")
}

// renderDynamicCardChromeLine renders one bounded chrome row from dynamic text. It
// sanitizes and flattens both inputs before reserving the prefix cells, then truncates
// the remaining text at display-cell boundaries before applying the style.
func renderDynamicCardChromeLine(style lipgloss.Style, prefix, raw string, width int) string {
	oneLine := func(s string) string {
		return strings.Map(func(r rune) rune {
			if unicode.IsSpace(r) {
				return ' '
			}
			return r
		}, terminaltext.Sanitize(s))
	}
	prefix, raw = oneLine(prefix), oneLine(raw)
	if width <= 0 {
		return style.Render(prefix + raw)
	}
	prefixWidth := lipgloss.Width(prefix)
	if prefixWidth >= width {
		return style.Render(ansi.TruncateWc(prefix, width, "..."))
	}
	return style.Render(prefix + ansi.TruncateWc(raw, width-prefixWidth, "..."))
}

// wrapDelegationRow trims display-only right padding, reserves prefix cells, and
// wraps raw text before callers style the completed rows. A continuation keeps the
// prefix's alignment without letting either the prefix or style padding consume a
// second layout pass.
func wrapDelegationRow(prefix, text string, bodyWidth int) []string {
	text = strings.TrimRightFunc(terminaltext.Sanitize(normalizeToolCardTabs(text)), unicode.IsSpace)
	if bodyWidth <= 0 {
		return []string{prefix + text}
	}
	prefixWidth := lipgloss.Width(prefix)
	if prefixWidth >= bodyWidth {
		return strings.Split(ansi.Hardwrap(prefix+text, bodyWidth, true), "\n")
	}
	available := bodyWidth - prefixWidth
	wrapped := ansi.Hardwrap(text, available, true)
	rows := strings.Split(wrapped, "\n")
	continuation := strings.Repeat(" ", prefixWidth)
	for i, row := range rows {
		if i == 0 {
			rows[i] = prefix + row
		} else {
			rows[i] = continuation + row
		}
	}
	return rows
}

func renderDelegationRows(style lipgloss.Style, prefix, text string, bodyWidth int) string {
	rows := wrapDelegationRow(prefix, text, bodyWidth)
	for i, row := range rows {
		rows[i] = style.Render(row)
	}
	return strings.Join(rows, "\n")
}

// wrapToolCardRegion constrains one independently styled tool-card region before it
// joins the card. It deliberately operates per region, never on the assembled card:
// card.Render must only frame already fitting rows.
func wrapToolCardRegion(region string, bodyWidth int) string {
	region = normalizeEmojiWidth(normalizeToolCardTabs(region))
	if region == "" || bodyWidth <= 0 {
		return region
	}
	return ansi.Wrap(region, bodyWidth, "")
}

// renderToolArgs renders the ARGS region of a tool card (everything below the
// head, before the result): the redacted Team/Subagent lanes, the Edit/Write
// diff, or — for an ordinary tool — the compact key:value summary when collapsed
// and the full pretty JSON when expanded. Returns "" when there is nothing to
// show. See renderTool for the per-branch rationale.
func (r *renderer) renderToolArgs(b *block, expand bool, bodyWidth int) string {
	var args string
	switch {
	case b.team:
		// Delegation rows must be bounded while still raw. renderTeam applies styles
		// only after preparing its body-width rows, so no ANSI padding can induce a
		// second wrap below the card frame.
		return r.renderTeam(b, expand, bodyWidth)
	case b.subagent:
		// See the Team path above; Subagent has the same styled metadata/trace body.
		return r.renderSubagent(b, expand, bodyWidth)
	default:
		if diff, ok := r.renderToolDiffAtWidth(b.toolName, b.toolArgs, expand, bodyWidth); ok {
			// Edit/Write render their change as a diff in place of the raw JSON args.
			return diff
		}
		if expand {
			// Expanded: always the FULL pretty-printed JSON (the inspect path; the summary
			// is collapsed-only, so ctrl+t reveals everything). Wrap raw JSON before its
			// style so the card row budget is ANSI-independent.
			if jsonArgs := prettyJSON(b.toolArgs); jsonArgs != "" {
				return renderToolCardText(r.th.Style("toolArgs"), jsonArgs, bodyWidth)
			}
		} else if summary, ok := r.summarizeArgs(b.toolArgs); ok {
			// Collapsed: the compact key:value summary in place of raw JSON (issue #24).
			args = summary
		} else if jsonArgs := prettyJSON(b.toolArgs); jsonArgs != "" {
			// Collapsed but the args aren't a JSON object (bare array/scalar/odd shape):
			// fall back to the existing pretty-JSON behaviour.
			args = r.th.Style("toolArgs").Render(jsonArgs)
		}
	}
	return wrapToolCardRegion(args, bodyWidth)
}

// renderToolResult renders the RESULT region of a resolved tool card. Collapsed,
// a LARGE JSON result is summarized to prominent fields + a size line (issue #24,
// self-styled). An error result, a non-JSON/line-shaped result, or the expanded
// view fall through to the existing styled, display-row-capped (or full) body — Read
// results are unchanged. Returns "" when there is no body.
//
// Typed content blocks (b.resultBlocks) are rendered distinctly IN ADDITION to the
// model-facing text body when present: a resource link shows as "↗ <name> · <uri>"
// and an image as "[image: <mime>]" so a user-audience artifact is not buried
// in/below the text. Text/embedded-resource/structured-content blocks are already
// represented in the model-facing resultBody, so they are not double-rendered. A nil
// resultBlocks (the common text-only case) leaves the existing render path
// byte-unchanged.
func (r *renderer) renderToolResult(b *block, expand bool, bodyWidth int) string {
	lines, hiddenSummaryFields := r.renderToolResultLines(b, expand)
	for _, blk := range b.resultBlocks {
		if line, ok := renderResultBlockLine(blk); ok {
			lines = append(lines, toolResultLine{text: line, style: resultLineArtifact})
		}
	}
	if !expand {
		lines = r.truncateResultDisplayLines(lines, bodyWidth, hiddenSummaryFields)
	} else {
		lines = wrapResultDisplayLines(lines, bodyWidth)
	}
	var out strings.Builder
	for i, line := range lines {
		if i > 0 {
			out.WriteByte('\n')
		}
		out.WriteString(r.renderToolResultLine(line))
	}
	return out.String()
}

type resultLineStyle uint8

const (
	resultLineBody resultLineStyle = iota
	resultLineError
	resultLineArtifact
	resultLineSummary
	resultLineMarker
)

type toolResultLine struct {
	text  string
	style resultLineStyle
}

// renderToolResultLines returns unwrapped, terminal-safe result rows. The enclosing
// renderToolResult combines these with typed artifact rows before applying the shared
// collapsed display-row budget.
func (r *renderer) renderToolResultLines(b *block, expand bool) ([]toolResultLine, int) {
	if summary, hiddenFields, ok := r.summarizeResolvedResultDetail(b, expand); ok {
		return resultLines(summary, resultLineSummary), hiddenFields
	}
	body := terminaltext.Sanitize(strings.TrimRight(b.resultBody, "\n"))
	if body == "" {
		return nil, 0
	}
	style := resultLineBody
	if b.resultError {
		style = resultLineError
	}
	return resultLines(body, style), 0
}

func resultLines(text string, style resultLineStyle) []toolResultLine {
	lines := strings.Split(text, "\n")
	out := make([]toolResultLine, len(lines))
	for i, line := range lines {
		out[i] = toolResultLine{text: line, style: style}
	}
	return out
}

func (r *renderer) renderToolResultLine(line toolResultLine) string {
	switch line.style {
	case resultLineError:
		return r.th.Style("errorText").Render(line.text)
	case resultLineArtifact, resultLineMarker:
		return r.th.Style("muted").Render(line.text)
	case resultLineSummary:
		if key, value, ok := strings.Cut(line.text, ": "); ok {
			return r.th.Style("muted").Render(key+":") + " " + r.th.Style("toolArgs").Render(value)
		}
		return r.th.Style("muted").Render(line.text)
	default:
		return r.th.Style("toolArgs").Render(line.text)
	}
}

// renderResultBlockLine renders ONE typed content block as a single distinct line,
// returning ok=false for blocks already represented in the model-facing text body
// (text / embedded-resource / structured-content) so they are not double-rendered, and
// for an unspecified/unknown kind (forward-compatible: never a crash). resource-link
// renders "↗ <name> · <uri>" (or "<uri>" when the name is empty); image renders
// "[image: <mime>]" (or "[image]" when the mime is empty). All server-derived
// strings are sanitized.
func renderResultBlockLine(blk client.ContentBlock) (string, bool) {
	switch blk.Kind {
	case client.ContentBlockResourceLink:
		name := terminaltext.Sanitize(blk.Name)
		uri := terminaltext.Sanitize(blk.URL)
		if name == "" {
			if uri == "" {
				return "", false
			}
			return "↗ " + uri, true
		}
		if uri == "" {
			return "↗ " + name, true
		}
		return "↗ " + name + " · " + uri, true
	case client.ContentBlockImage:
		mime := terminaltext.Sanitize(blk.MimeType)
		if mime == "" {
			return "[image]", true
		}
		return "[image: " + mime + "]", true
	default:
		// text / embedded-resource / structured-content / unspecified: already in the
		// model-facing body (or absent) — do not double-render.
		return "", false
	}
}

// renderSubagent renders a Subagent card's BOUNDED subagent region (ADR 0079 — the
// previews are bounded, scrubbed, client-only; they never enter the parent
// conversation). It has three states, per the agreed UX:
//
//   - LIVE collapsed (default, not resolved): a calm one-liner under the goal —
//     "subagent · <current tool> · ↑<in> ↓<out> · N tools · ctrl+t trace". The line
//     changes only when the tool actually changes: counts update as events arrive,
//     no ticker.
//   - EXPANDED (ctrl+t, not resolved): the capped child trace in the Team format —
//     glyph+name chips with bounded arg/result previews and capped message lines —
//     under a muted "bounded previews" honesty note.
//   - RESOLVED: a single muted stat line —
//     "subagent · <dur> · ↑<in> ↓<out> · N tools · stop:<reason>".
//
// The goal title always leads (a muted line) so a card is self-contained and
// legible even with several concurrent subagents interleaved. All subagent-derived
// strings (goal, tool names, previews) are terminal-sanitized.
func (r *renderer) renderSubagent(b *block, expand bool, bodyWidth int) string {
	muted := r.th.Style("muted")
	var out strings.Builder
	if b.subGoal != "" {
		out.WriteString(renderDelegationToolCardText(muted, "↳ "+terminaltext.Sanitize(b.subGoal), bodyWidth))
		out.WriteString("\n")
	}
	modelLabel := delegationModelLabel(b.subRoutedCategory, b.subRoutedModel, b.subRoutingReason, b.subModel, b.subRoutingDecision)
	if expand && b.subRoutingDecision != nil {
		modelLabel = ""
	}
	if modelLabel != "" {
		out.WriteString(renderDelegationToolCardText(muted, modelLabel, bodyWidth))
		out.WriteString("\n")
	}
	if expand {
		if detail := routingDecisionDetail(b.subRoutingDecision, b.subModel, b.subRoutingReason); detail != "" {
			out.WriteString(renderDelegationToolCardText(muted, detail, bodyWidth))
			out.WriteString("\n")
		}
	}

	if b.subDone {
		out.WriteString(renderDelegationToolCardText(muted, subagentResolvedLine(b), bodyWidth))
		return strings.TrimRight(out.String(), "\n")
	}

	if expand {
		out.WriteString(renderDelegationToolCardText(muted, "subagent · "+boundedPreviewsSubNote, bodyWidth))
		if trace := r.renderTraceAtWidth(b.subTrace, bodyWidth); trace != "" {
			out.WriteString("\n")
			out.WriteString(trace)
		}
		return strings.TrimRight(out.String(), "\n")
	}

	out.WriteString(renderDelegationToolCardText(muted, r.subagentLiveLine(b), bodyWidth))
	return strings.TrimRight(out.String(), "\n")
}

// subagentModelLabel renders the model surface for a delegation as a muted one-line
// cue. It shows the OPT-IN router's bare metadata as "routed: <category> → <model>"
// when the router classified the delegation (ADR 0031); otherwise it shows the
// concrete model the child ACTUALLY ran on as "model: <model>" (issue #112 / ADR 0035)
// — inherited default, agent-def pin, or per-call override — annotated with WHY the
// router did not classify as " · not routed: <reason>" when the server supplied a
// reason (issue #397 / ADR 0083). It returns "" when no model is known and the router
// did not fire. The category/model/reason are server-derived bare metadata (sanitized)
// — never child content — so gauntlet #7 holds. When routed, model == routedModel and
// the reason is empty, so the routed cue is shown (not duplicated as a model: line).
func subagentModelLabel(category, routedModel, routingReason, model string) string {
	category = terminaltext.Sanitize(category)
	routedModel = terminaltext.Sanitize(routedModel)
	routingReason = terminaltext.Sanitize(routingReason)
	model = terminaltext.Sanitize(model)
	// Router fired: show the routed cue (category + the routed model).
	if category != "" || routedModel != "" {
		if routedModel == "" {
			return "routed: " + category
		}
		if category == "" {
			return "routed: " + routedModel
		}
		return "routed: " + category + " → " + routedModel
	}
	if model != "" {
		if routingReason != "" {
			return "model: " + model + " · not routed: " + routingReason
		}
		return "model: " + model
	}
	if routingReason != "" {
		return "not routed: " + routingReason
	}
	return ""
}

// delegationModelLabel preserves the historical model line when decision is nil.
// A fallback may add one candidate line, but the actual model always comes from
// the existing authoritative model field rather than the rejected candidate.
func delegationModelLabel(category, routedModel, routingReason, model string, decision *client.RoutingDecision) string {
	label := subagentModelLabel(category, routedModel, routingReason, model)
	if decision == nil || decision.Outcome != "fallback" {
		return label
	}
	if terminaltext.Sanitize(model) != "" && terminaltext.Sanitize(routingReason) != "" {
		label = "model: " + terminaltext.Sanitize(model) + " · fallback: " + terminaltext.Sanitize(routingReason)
	}
	candidate := routingCandidateCue(decision)
	if candidate == "" {
		return label
	}
	if label == "" {
		return candidate
	}
	return label + "\n" + candidate
}

func routingCandidateCue(decision *client.RoutingDecision) string {
	candidate := terminaltext.Sanitize(decision.CandidateCategory)
	candidateModel := terminaltext.Sanitize(decision.CandidateModel)
	if candidate == "" && candidateModel == "" {
		return ""
	}
	line := "candidate: " + candidate
	if candidate != "" && candidateModel != "" {
		line += " → " + candidateModel
	} else if candidateModel != "" {
		line += candidateModel
	}
	if decision.Confidence != nil {
		line += fmt.Sprintf(" · confidence %.2f", *decision.Confidence)
		if decision.MinimumConfidence != nil && *decision.MinimumConfidence > 0 && *decision.Confidence < *decision.MinimumConfidence {
			line += fmt.Sprintf(" < threshold %.2f", *decision.MinimumConfidence)
		}
	}
	return line
}

// routingDecisionDetail renders the complete bounded decision snapshot for an
// expanded card or F6 focus pane. Optional numeric presence is explicit.
func routingDecisionDetail(decision *client.RoutingDecision, actualModel, reason string) string {
	if decision == nil {
		return ""
	}
	confidence := unavailableText
	if decision.Confidence != nil {
		confidence = fmt.Sprintf("%.2f", *decision.Confidence)
	}
	threshold := unavailableText
	if decision.MinimumConfidence != nil {
		threshold = fmt.Sprintf("%.2f", *decision.MinimumConfidence)
		if *decision.MinimumConfidence == 0 {
			threshold = "disabled (0.00)"
		}
	}
	candidate := terminaltext.Sanitize(decision.CandidateCategory)
	candidateModel := terminaltext.Sanitize(decision.CandidateModel)
	if candidate == "" {
		candidate = unavailableText
	}
	if candidateModel != "" {
		candidate += " → " + candidateModel
	}
	actualModel = terminaltext.Sanitize(actualModel)
	if actualModel == "" {
		actualModel = unavailableText
	}
	reason = terminaltext.Sanitize(reason)
	if reason == "" {
		if decision.Outcome == "routed" {
			reason = "accepted"
		} else {
			reason = unavailableText
		}
	}
	breaker := "closed"
	if decision.BreakerOpen {
		breaker = "open"
	}
	return fmt.Sprintf("backend: %s · classifier: %s · outcome: %s\n"+
		"candidate: %s · confidence: %s · threshold: %s\n"+
		"actual model: %s · reason: %s\n"+
		"breaker: %d/%d misses · %s",
		terminaltext.Sanitize(decision.Backend), terminaltext.Sanitize(decision.ClassifierModel), terminaltext.Sanitize(decision.Outcome),
		candidate, confidence, threshold, actualModel, reason,
		decision.ConsecutiveMisses, decision.MissLimit, breaker)
}

// subagentLiveLine is the calm, monotonic collapsed status line: the child's live
// current-tool name (when one has run — "…" while it is still working), the token
// totals, a running tool count, and the expand-tools trace affordance. No elapsed clock
// and no heartbeat ticker, so the line changes only when the tool actually changes
// (ADR 0079 AC3.1). The tool name is sanitized (server-derived). The trace chord
// reads the LIVE ExpandTools marking (r.marks.expandTools) so an override propagates
// (issue #457).
func (r *renderer) subagentLiveLine(b *block) string {
	current := "…"
	if b.subCurrent != "" {
		current = terminaltext.Sanitize(b.subCurrent)
	}
	return fmt.Sprintf("subagent · %s · ↑%s ↓%s · %s · %s trace",
		current,
		humanizeTokens(b.subUsage.InputTokens),
		humanizeTokens(b.subUsage.OutputTokens),
		plural(b.subToolCount, "tool"),
		r.marks.expandTools)
}

// subagentResolvedLine is the muted one-line summary shown once the child run has
// finished: duration, token totals, final tool count, and the stop reason.
func subagentResolvedLine(b *block) string {
	return fmt.Sprintf("subagent · %s · ↑%s ↓%s · %s · stop:%s",
		humanizeDuration(b.subDurationMs),
		humanizeTokens(b.subUsage.InputTokens),
		humanizeTokens(b.subUsage.OutputTokens),
		plural(b.subToolCount, "tool"),
		subagentStopLabel(b.subStop))
}

// chipSep is the two-space gap between adjacent child-tool chips in the expanded
// trace row.
const chipSep = "  "

// wrapChips packs already-rendered chips into rows separated by chipSep, breaking
// to a new line BETWEEN chips when the next chip would overflow width (measured by
// visible width via lipgloss.Width, which ignores ANSI). A width <= 0 disables
// wrapping (all chips on one row). A chip wider than width gets its own row;
// inspector renderers additionally wrap that row through wrapTraceLine.
func wrapChips(chips []string, width int) string {
	if len(chips) == 0 {
		return ""
	}
	if width <= 0 {
		return strings.Join(chips, chipSep)
	}
	sepW := lipgloss.Width(chipSep)
	var b strings.Builder
	lineW := 0
	for i, chip := range chips {
		cw := lipgloss.Width(chip)
		switch {
		case i == 0:
			b.WriteString(chip)
			lineW = cw
		case lineW+sepW+cw > width:
			b.WriteString("\n")
			b.WriteString(chip)
			lineW = cw
		default:
			b.WriteString(chipSep)
			b.WriteString(chip)
			lineW += sepW + cw
		}
	}
	return b.String()
}

// maxTraceMessageLen caps how many runes of a forwarded child message line show in
// a delegation lane's expanded trace (Subagent / Team / Parallel — shared per ADR
// 0079); the server already bounds previews, this is a belt-and-braces clamp so one
// verbose child can't dominate the card.
const maxTraceMessageLen = 200

// maxTraceToolNameLen bounds a tool name in delegation rows and traces before it
// joins other metadata. The inspector still wraps it to the card width.
const maxTraceToolNameLen = 20

// maxTraceDetailLen caps how many runes of a tool chip's arg/result preview show
// next to it in the expanded trace. Server-bounded already (≤200 runes); this keeps
// a single chip line scannable. Shared by the Subagent/Team/Parallel trace
// renderers per ADR 0079 — the engine cap + this cap is the intentional
// double-truncation defense-in-depth.
const maxTraceDetailLen = 80

// boundedPreviewsSubNote / boundedPreviewsParNote are the honesty notes every
// Subagent / Parallel trace surface carries (ADR 0079): the previews are BOUNDED —
// clamped + scrubbed server-side, capped again on render, client-only — so the
// note states the accurate posture instead of the pre-ADR-0079 "content hidden"
// claim. The parent conversation stays clean (gauntlet #7 is about the
// conversation, not what a client may observe).
const (
	boundedPreviewsSubNote = "bounded previews — child content is clamped + scrubbed, never in the parent conversation"
	boundedPreviewsParNote = "bounded previews — branch content is clamped + scrubbed, never in the parent conversation"
)

// maxTeamLanes caps how many member lanes render inline on the card. A larger
// roster collapses the overflow into a "· +K more" roll-up line so a big team can
// never grow the card without limit (a DoS-by-output guard) and stays legible.
// The remaining members are not lost — they live in the conversation block and a
// future f6 overlay can surface them all.
const maxTeamLanes = 6

// maxTeamNameWidth caps the column width member names are padded to for the
// collapsed lane lines, so the "· <state> · ↑in ↓out" columns line up without one
// very long name blowing out the gutter.
const maxTeamNameWidth = 16

// teamGlyph is the per-member state glyph (glyph-not-colour-only): a "✗" for a member
// that STOPPED non-resumably (team ended and the lane carries a terminal disposition),
// a "✓" for a clean TERMINAL member (the team has ended — b.teamDone), a hollow "○" for
// an IDLE member (finished its current round, awaiting the next round or synthesis),
// and a filled "◆" for one actively working. The stopped state is checked first so the
// overlay no longer flips a stopped member to "✓ done" and contradicts the supervisor.
func teamGlyph(ln *teamLane, teamDone bool) string {
	switch {
	case teamDone && ln.stopped:
		return "✗"
	case teamDone:
		return "✓"
	case ln.idle:
		return "○"
	default:
		return "◆"
	}
}

// teamLaneOrder returns lane indices in render order: the lead member(s) first,
// then the rest in roster (arrival) order. It is a stable sort over an index slice
// so teamLanes itself is never reordered (event routing stays by name). The lead
// is thus always anchored at the top regardless of the order the server sent the
// roster.
func teamLaneOrder(lanes []teamLane) []int {
	order := make([]int, len(lanes))
	for i := range lanes {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool {
		return lanes[order[a]].lead && !lanes[order[b]].lead
	})
	return order
}

// renderTeam renders a Team card's BOUNDED per-member region. It has three states,
// mirroring the subagent card's live/expanded/resolved language, and shows only
// server-bounded member content (never the raw member transcript):
//
//   - LIVE collapsed (default, team not ended): a team header ("N members") plus a
//     calm one-line-per-member lane (lead first, columns aligned) —
//     "◆ <name>[lead] · <current tool or state>… · ↑<in> ↓<out>". Counts are
//     monotonic and update only as events arrive (no ticker), so a lane never
//     flickers; an active member's state carries a trailing "…" heartbeat. At most
//     maxTeamLanes lanes render, with a "· +K more" roll-up for the rest.
//   - EXPANDED (ctrl+t, same toggle): per member, the capped lane trace — message
//     lines (clamped) and tool chips (✓/✗ name) with their bounded arg/result
//     preview — separated by a blank line between members so boundaries are clear.
//   - RESOLVED compact (team ended): a muted stat line
//     "team · <rounds> rounds · ↑<in> ↓<out> · stop:<reason>". Expanded resolved
//     cards retain that terminal summary and show the same bounded per-member detail
//     as a live expanded card. The Team tool's joined summary renders below via the
//     normal result body path.
//
// All member-derived text (names, message lines, tool names, previews) is
// terminal-sanitized before it reaches lipgloss.
func (r *renderer) renderTeam(b *block, expand bool, bodyWidth int) string {
	muted := r.th.Style("muted")
	var out strings.Builder

	if b.teamDone {
		out.WriteString(renderDelegationToolCardText(muted, teamResolvedLine(b), bodyWidth))
		if !expand {
			return out.String()
		}
	} else {
		out.WriteString(renderDelegationToolCardText(muted, r.teamHeader(b, expand), bodyWidth))
	}

	order := teamLaneOrder(b.teamLanes)
	shown := order
	if len(shown) > maxTeamLanes {
		shown = order[:maxTeamLanes]
	}
	nameW := teamNameWidth(b.teamLanes, shown)
	for n, idx := range shown {
		ln := &b.teamLanes[idx]
		if expand && n > 0 {
			// A blank line between members' blocks so boundaries read clearly at 3+.
			out.WriteString("\n")
		}
		out.WriteString("\n")
		out.WriteString(renderDelegationToolCardText(muted, teamLaneLine(ln, nameW, b.teamDone), bodyWidth))
		if expand {
			if detail := routingDecisionDetail(ln.routingDecision, ln.model, ln.routingReason); detail != "" {
				out.WriteString("\n")
				out.WriteString(renderDelegationToolCardText(muted, detail, bodyWidth))
			}
			if tr := r.renderTraceAtWidth(ln.trace, bodyWidth); tr != "" {
				out.WriteString("\n")
				out.WriteString(tr)
			}
		}
	}
	if extra := len(order) - len(shown); extra > 0 {
		// The inline card caps at maxTeamLanes; the rest live in the agents overlay.
		// Advertise it on the roll-up so a capped card is the discovery point for the
		// full, windowed roster. The chord reads the LIVE Agents marking so an override
		// propagates (issue #457).
		out.WriteString("\n")
		out.WriteString(renderDelegationToolCardText(muted, fmt.Sprintf("  · +%d more · %s", extra, r.marks.agents), bodyWidth))
	}
	return out.String()
}

// teamHeader is the muted lead line summarising the team's shape: the member count
// and the expand-tools affordance, whose verb tracks the toggle (trace when collapsed,
// collapse when expanded). The round count is carried only on team.end, so it is
// shown on the resolved line rather than fabricated live. The chord reads the LIVE
// ExpandTools marking (r.marks.expandTools) so an override propagates (issue #457).
func (r *renderer) teamHeader(b *block, expand bool) string {
	verb := r.marks.expandTools + " trace"
	if expand {
		verb = r.marks.expandTools + " collapse"
	}
	return "team · " + plural(len(b.teamLanes), "member") + " · " + verb
}

// teamNameWidth is the column width member BARE names are padded to on the
// collapsed lane lines: the longest shown bare name, capped at maxTeamNameWidth,
// so the state/usage columns line up across members. The "[lead]" tag is appended
// AFTER this padded column (never truncated away), so the lead is always
// unambiguous even when its name is long.
func teamNameWidth(lanes []teamLane, shown []int) int {
	w := 0
	for _, idx := range shown {
		if n := len([]rune(truncate(terminaltext.Sanitize(lanes[idx].name), maxTeamNameWidth))); n > w {
			w = n
		}
	}
	return w
}

// teamLaneLine is one member's calm, monotonic collapsed status line: a state
// glyph, a persistent mutating cue, the bare member name (truncated + column-
// padded) with the "[lead]" tag appended after the column, then the current tool
// or a derived state label (with a "…" heartbeat while active) and running token
// totals. No elapsed clock, so it updates only as events arrive.
func teamLaneLine(ln *teamLane, nameW int, teamDone bool) string {
	name := truncate(terminaltext.Sanitize(ln.name), maxTeamNameWidth)
	if pad := nameW - len([]rune(name)); pad > 0 {
		name += strings.Repeat(" ", pad)
	}
	if ln.lead {
		name += " [lead]"
	}
	return fmt.Sprintf("%s %s %s · %s · ↑%s ↓%s",
		teamGlyph(ln, teamDone),
		teamMutCue(ln),
		name,
		teamLaneState(ln, teamDone),
		humanizeTokens(ln.usage.InputTokens),
		humanizeTokens(ln.usage.OutputTokens))
}

// teamMutCue is the PERSISTENT per-member mutating cue (stable roster metadata): a
// "✎" for a mutating member (one running in an isolated fork with workspace-writing
// tools) and a space-matched "·" for a read-only member, so a mutating member stays
// visually distinct even while a tool name fills its state column. It is a fixed
// glyph-not-colour cue, never derived from the transient state label.
func teamMutCue(ln *teamLane) string {
	if ln.mutating {
		return "✎"
	}
	return "·"
}

// teamLaneState derives a member's current state label for the collapsed line:
// "stopped — <reason>" when the team has ended and the lane STOPPED non-resumably
// (terminal, distinct from a clean finish so the overlay does not contradict the
// supervisor), "done (retried)" when it FINISHED but survived at least one recovered
// run-level failure (issue #318 — the same do-not-contradict-the-supervisor rule),
// "done" when the team has ended cleanly (teamDone — terminal, wins over
// everything), "idle" when the member finished its round and is awaiting the next
// round / synthesis, else the running tool name (when one is active) or "working".
// Only the WORKING state gets a trailing "…" heartbeat so a quiet card reads as
// in-flight rather than stalled (mirroring the "reasoning…" affordance); idle, stopped
// and done members are genuinely quiet and get no ellipsis. The tool name is
// sanitized (server-derived). The mutating signal lives in teamMutCue, not here, so
// it persists once a tool name fills this label.
func teamLaneState(ln *teamLane, teamDone bool) string {
	switch {
	case teamDone && ln.stopped:
		if label := teamStopReasonLabel(ln.stopReason); label != "" {
			return "stopped — " + label
		}
		return "stopped"
	case teamDone && ln.errorRounds > 0:
		// Finished, but not cleanly: a bounded retry (issue #318) recovered this member
		// from at least one run-level failure. A bare "done" here would tell the operator
		// the opposite of what the supervisor reported.
		return "done (retried)"
	case teamDone:
		return "done"
	case ln.idle:
		return "idle"
	}
	label := "working"
	if ln.current != "" {
		label = truncate(terminaltext.Sanitize(ln.current), maxTraceToolNameLen)
	}
	return label + "…"
}

// teamStopReasonLabel maps a member's closed stop reason to a calm one-word label
// ("error" / "cancelled" / "budget"). An empty or unknown reason returns "" (the
// caller then renders a bare "stopped"). The reason is a closed supervisor enum
// already mapped to a known string by the client, so no sanitization is needed.
func teamStopReasonLabel(reason string) string {
	switch reason {
	case teamStopReasonError, teamStopReasonCancelled, teamStopReasonBudget:
		return reason
	default:
		return ""
	}
}

// renderTraceAtWidth prepares styled trace rows against a tool card's body before
// they join the card. It leaves the shared renderer untouched for other regions.
func (r *renderer) renderTraceAtWidth(trace []teamTrace, bodyWidth int) string {
	return (&renderer{th: r.th, traceWidth: bodyWidth}).renderTrace(trace)
}

// renderTrace renders a delegation lane's expanded trace — the SHARED format for
// the Team member lanes, the Subagent inline/fleet lanes, and the Parallel branch
// lanes (ADR 0079: one trace shape, one renderer). Message lines (clamped, dim,
// prefixed "  ") interleave with tool chips (✓/✗ name) carrying their bounded
// arg/result preview, in arrival order. A chip with a preview gets its own line
// ("  ✓ Grep — pattern: foo"); bare chips coalesce onto one wrapped row. Returns
// "" for an empty trace. All text is sanitized; the previews are capped again here
// (maxTraceDetailLen / maxTraceMessageLen) on top of the server clamp — the
// intentional double-truncation defense-in-depth.
func (r *renderer) renderTrace(trace []teamTrace) string {
	if len(trace) == 0 {
		return ""
	}
	muted := r.th.Style("muted")
	okStyle := r.th.Style("toolOk")
	errStyle := r.th.Style("toolErr")
	nameStyle := r.th.Style("toolName")

	var b strings.Builder
	var chips []string
	flush := func() {
		if len(chips) == 0 {
			return
		}
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		for i, row := range wrapDelegationRow("  ", strings.Join(chips, chipSep), r.traceWidth) {
			if i > 0 {
				b.WriteString("\n")
			}
			b.WriteString(nameStyle.Render(row))
		}
		chips = nil
	}
	writeLine := func(prefix, text string, style lipgloss.Style) {
		flush()
		for _, row := range wrapDelegationRow(prefix, text, r.traceWidth) {
			if b.Len() > 0 {
				b.WriteString("\n")
			}
			b.WriteString(style.Render(row))
		}
	}
	writeToolLine := func(glyph, name, detail string, glyphStyle lipgloss.Style) {
		flush()
		rows := wrapDelegationRow("  ", glyph+" "+name+" — "+detail, r.traceWidth)
		regularPrefix := r.traceWidth <= 0 || r.traceWidth > 2
		offset, glyphStart := 0, 0
		if !regularPrefix {
			glyphStart = 2
		}
		for _, row := range rows {
			if b.Len() > 0 {
				b.WriteString("\n")
			}
			if regularPrefix {
				b.WriteString(row[:2])
				row = row[2:]
			}
			b.WriteString(renderTraceToolRow(row, offset, glyphStart, name, glyphStyle, nameStyle, muted))
			offset += len([]rune(row))
		}
	}
	for i := range trace {
		t := &trace[i]
		switch t.kind {
		case teamTraceTool:
			glyph := "✓"
			style := okStyle
			if t.isError {
				glyph = "✗"
				style = errStyle
			}
			name := truncate(terminaltext.Sanitize(t.name), maxTraceToolNameLen)
			if detail := terminaltext.Sanitize(oneLine(t.detail)); detail != "" {
				writeToolLine(glyph, name, truncate(detail, maxTraceDetailLen), style)
			} else {
				chips = append(chips, glyph+" "+name)
			}
		case teamTraceMessage:
			writeLine("  ", truncate(terminaltext.Sanitize(oneLine(t.text)), maxTraceMessageLen), muted)
		}
	}
	flush()
	return b.String()
}

// renderTraceToolRow restores a trace tool row's semantic styles after its raw
// text has been wrapped: status glyph, tool name, then muted preview. offset and
// glyphStart are rune offsets in the unwrapped text, letting a style boundary fall
// on either side of a wrapped row.
func renderTraceToolRow(row string, offset, glyphStart int, name string, glyphStyle, nameStyle, muted lipgloss.Style) string {
	nameStart := glyphStart + 2 // glyph plus its following space
	detailStart := nameStart + len([]rune(name))

	var b strings.Builder
	var runes []rune
	style := -1
	write := func(next int) {
		if len(runes) == 0 {
			style = next
			return
		}
		text := string(runes)
		switch style {
		case 0:
			b.WriteString(glyphStyle.Render(text))
		case 1:
			b.WriteString(nameStyle.Render(text))
		case 2:
			b.WriteString(muted.Render(text))
		default:
			b.WriteString(text)
		}
		runes = runes[:0]
		style = next
	}
	for i, r := range []rune(row) {
		position := offset + i
		next := -1
		switch {
		case position == glyphStart:
			next = 0
		case position >= nameStart && position < detailStart:
			next = 1
		case position >= detailStart:
			next = 2
		}
		if next != style {
			write(next)
		}
		runes = append(runes, r)
	}
	write(-1)
	return b.String()
}

// teamResolvedLine is the muted one-line summary shown once the team run has
// ended: the round count, summed team token totals, the stop reason (reusing the
// subagent stop-label mapping so labels stay consistent), and — when any member
// stopped non-resumably — a "N stopped" count tell. The count is the calm inline
// card's only signal of a stopped member (the per-member glyph lives in the modal
// overlay), so it appears only when stopped > 0.
func teamResolvedLine(b *block) string {
	line := fmt.Sprintf("team · %s · ↑%s ↓%s · stop:%s",
		plural(b.teamRounds, "round"),
		humanizeTokens(b.teamUsage.InputTokens),
		humanizeTokens(b.teamUsage.OutputTokens),
		subagentStopLabel(b.teamStop))
	if n := teamStoppedCount(b); n > 0 {
		line += fmt.Sprintf(" · %d stopped", n)
	}
	return line
}

// teamStoppedCount reports how many member lanes ended STOPPED (non-resumable /
// budget-exhausted). It drives the inline-card "N stopped" tell and the overlay
// roster sub-header count.
func teamStoppedCount(b *block) int {
	n := 0
	for i := range b.teamLanes {
		if b.teamLanes[i].stopped {
			n++
		}
	}
	return n
}

// oneLine collapses any internal newlines/tabs in a member message preview to
// single spaces so a multi-line forwarded fragment stays a single lane line.
func oneLine(s string) string {
	return strings.Join(strings.FieldsFunc(s, func(r rune) bool {
		return r == '\n' || r == '\r' || r == '\t'
	}), " ")
}

// subagentStopLabel maps a child run's raw stop reason to the compact label shown
// on the resolved subagent line (done / max-tools / max-turns / error). An unknown
// or empty reason passes through verbatim so a new stop reason is never hidden.
func subagentStopLabel(stop string) string {
	switch stop {
	case "end_turn", "":
		return "done"
	case "max_tool_calls":
		return "max-tools"
	case "max_turns":
		return "max-turns"
	case "max_consecutive_failures":
		return "max-failures"
	case "cancelled":
		return "cancelled"
	case stopError:
		return "error"
	// The newer Subagent/Team terminal reasons (Package A/B) ride the same string `stop`
	// field on the wire (no proto enum) — map them to compact labels so a subagent
	// that ended via a budget / structured-output / no-progress terminal renders a
	// sensible label here and in the fleet roster, never a blank or the raw token.
	case "budget":
		return "budget"
	case "structured_output":
		return "schema"
	case "no_progress":
		return "no-progress"
	default:
		return terminaltext.Sanitize(stop)
	}
}

// humanizeDuration renders a millisecond wall-clock duration compactly: sub-second
// as "Nms", under a minute as "N.Ns", else "Nm Ns". A non-positive duration (no
// clock) renders as "0ms".
func humanizeDuration(ms int64) string {
	if ms <= 0 {
		return "0ms"
	}
	if ms < 1000 {
		return strconv.FormatInt(ms, 10) + "ms"
	}
	secs := float64(ms) / 1000.0
	if secs < 60 {
		return trimDecimal(secs) + "s"
	}
	m := int64(secs) / 60
	s := int64(secs) % 60
	return strconv.FormatInt(m, 10) + "m " + strconv.FormatInt(s, 10) + "s"
}

// resultBody renders a tool result body with the legacy logical-line cap for
// callers without card geometry.
func (r *renderer) resultBody(body string, expand bool) string {
	return r.resultBodyAtWidth(body, expand, 0)
}

// resultBodyAtWidth renders a tool result body: full when expanded; otherwise it
// hard-wraps to the card body width before capping visible display rows. This keeps
// the overflow count honest and prevents the final card wrap from growing the
// collapsed body after its cap.
func (r *renderer) resultBodyAtWidth(body string, expand bool, bodyWidth int) string {
	body = normalizeToolCardTabs(terminaltext.Sanitize(strings.TrimRight(body, "\n")))
	if expand || body == "" {
		return body
	}
	if bodyWidth > 0 {
		body = ansi.Hardwrap(body, bodyWidth, true)
	}
	return truncateLinesTailMark(body, maxToolResultLines, "", r.marks.expandTools)
}

func (r *renderer) truncateResultDisplayLines(lines []toolResultLine, bodyWidth, hiddenSummaryFields int) []toolResultLine {
	wrapped := wrapResultDisplayLines(lines, bodyWidth)
	if len(wrapped) > maxToolResultLines {
		return append(wrapped[:maxToolResultLines], toolResultLine{
			text:  r.collapseMarker(len(wrapped) - maxToolResultLines),
			style: resultLineMarker,
		})
	}
	if hiddenSummaryFields > 0 {
		return append(wrapped, toolResultLine{text: r.argRollupMarker(hiddenSummaryFields), style: resultLineMarker})
	}
	if hiddenSummaryFields < 0 {
		return append(wrapped, toolResultLine{text: r.argRollupMarker(0), style: resultLineMarker})
	}
	return wrapped
}

// wrapResultDisplayLines normalizes each raw source row before it is styled or
// framed. Whitespace-only source rows remain one intentional blank paragraph;
// right padding and wrapper-created blank fragments never become display rows.
func wrapResultDisplayLines(lines []toolResultLine, bodyWidth int) []toolResultLine {
	wrapped := make([]toolResultLine, 0, len(lines))
	for _, line := range lines {
		text := normalizeEmojiWidth(normalizeToolCardTabs(line.text))
		if strings.TrimSpace(text) == "" {
			// Preserve an intentional blank source line as one display row, without
			// retaining width-exceeding padding that could wrap into more rows.
			wrapped = append(wrapped, toolResultLine{style: line.style})
			continue
		}
		// Trailing whitespace is display padding, not result content: discard it
		// before wrapping so it cannot become a blank continuation row. Leading
		// indentation remains part of every meaningful source line.
		text = strings.TrimRightFunc(text, unicode.IsSpace)
		if bodyWidth > 0 {
			text = ansi.Hardwrap(text, bodyWidth, true)
		}
		for _, fragment := range resultLines(text, line.style) {
			// A leading indent wider than the card can make Hardwrap produce an
			// empty-looking fragment before the text. It is wrapper-created, not a
			// source row, so it neither renders nor consumes the collapsed budget.
			if strings.TrimSpace(fragment.text) != "" {
				wrapped = append(wrapped, fragment)
			}
		}
	}
	return wrapped
}

// renderChangedFiles renders the session's changed-files summary as a muted,
// insertion-ordered list under a "✎ N files this session" header — the ctrl+t
// expansion of the header indicator (sharing its "✎" pencil glyph). Returns ""
// for an empty set. Paths are terminal-sanitized (they originate from
// server-relayed tool args).
func (r *renderer) renderChangedFiles(paths []string) string {
	if len(paths) == 0 {
		return ""
	}
	style := r.th.Style("muted")
	var b strings.Builder
	b.WriteString(style.Render("✎ " + plural(len(paths), "file") + " changed this session"))
	for _, p := range paths {
		b.WriteString("\n")
		b.WriteString(style.Render("  " + terminaltext.Sanitize(p)))
	}
	return b.String()
}

// mutatedPath returns the workspace path a file-MUTATING tool call touches, and
// ok=false for any read-only or unrecognised tool. It keys off the SAME arg
// shapes the diff renderer mirrors (editDiffArgs/writeDiffArgs, both carrying a
// "path" field — see the TestEditWriteArgKeysAreStable drift guard in
// internal/adapter/tools). The set of mutating tools is intentionally explicit
// (Edit, Write): adding a future mutating tool means adding a case here, not
// blanket-trusting every tool's "path" arg. Malformed args / empty path yield
// ("", false) so a garbled call never pollutes the changed-files set.
func mutatedPath(name, rawArgs string) (string, bool) {
	switch name {
	case "Edit":
		var args editDiffArgs
		if err := json.Unmarshal([]byte(strings.TrimSpace(rawArgs)), &args); err != nil || args.Path == "" {
			return "", false
		}
		return args.Path, true
	case "Write":
		var args writeDiffArgs
		if err := json.Unmarshal([]byte(strings.TrimSpace(rawArgs)), &args); err != nil || args.Path == "" {
			return "", false
		}
		return args.Path, true
	default:
		return "", false
	}
}

// renderToolDiff renders a colourised diff for the Edit and Write tools. It
// returns (rendered, true) when name is a diff-capable tool AND its args parse
// into the expected shape; otherwise (",", false) so the caller falls back to
// the existing pretty-JSON rendering. All server-derived text is sanitized
// before it reaches lipgloss.
func (r *renderer) renderToolDiff(name, rawArgs string, expand bool) (string, bool) {
	_, _, bodyWidth := r.toolCardLayout()
	return r.renderToolDiffAtWidth(name, rawArgs, expand, bodyWidth)
}

// renderToolDiffAtWidth prepares a diff for one tool card's body budget before
// applying its independently styled rows.
func (r *renderer) renderToolDiffAtWidth(name, rawArgs string, expand bool, bodyWidth int) (string, bool) {
	switch name {
	case "Edit":
		return r.renderEditDiff(rawArgs, expand, bodyWidth)
	case "Write":
		return r.renderWriteDiff(rawArgs, expand, bodyWidth)
	default:
		return "", false
	}
}

// editDiffArgs mirrors internal/adapter/tools/edit.go's editArgs JSON shape.
type editDiffArgs struct {
	Path       string `json:"path"`
	OldString  string `json:"old_string"`
	NewString  string `json:"new_string"`
	ReplaceAll bool   `json:"replace_all"`
}

// renderEditDiff renders an Edit as a red/green unified-style diff:
// removed (old_string) lines prefixed "-", added (new_string) lines prefixed
// "+", under a muted path header (with a "(replace all)" tag when set). Returns
// false on malformed/empty args so the caller falls back to JSON.
func (r *renderer) renderEditDiff(rawArgs string, expand bool, bodyWidth int) (string, bool) {
	var args editDiffArgs
	if err := json.Unmarshal([]byte(strings.TrimSpace(rawArgs)), &args); err != nil {
		return "", false
	}
	if args.Path == "" || (args.OldString == "" && args.NewString == "") {
		return "", false
	}

	// Size signal: removed/added line counts (empty side = 0 lines).
	removed := lineCount(args.OldString)
	added := lineCount(args.NewString)
	header := fmt.Sprintf("%s  -%d +%d", args.Path, removed, added)
	if args.ReplaceAll {
		header += " (replace all)"
	}
	var b strings.Builder
	b.WriteString(renderToolMetadata(r.th.Style("diffMeta"), header, bodyWidth))
	b.WriteString("\n")
	b.WriteString(r.diffSide(args.OldString, "-", "diffRemove", expand, bodyWidth))
	b.WriteString(r.diffSide(args.NewString, "+", "diffAdd", expand, bodyWidth))
	return strings.TrimRight(b.String(), "\n"), true
}

// writeDiffArgs mirrors internal/adapter/tools/write.go's writeArgs JSON shape.
type writeDiffArgs struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

// renderWriteDiff renders a Write as an all-green "new content" block under a
// muted path header. Returns false on malformed args so the caller falls back.
//
// The header does NOT claim "new file": at ask time the harness doesn't know
// whether the path already exists, and a silent overwrite is MORE dangerous than
// a create — asserting "new file" would understate the risk at the approval gate.
// So it says "(overwrites if it exists)" instead, which holds in both cases.
func (r *renderer) renderWriteDiff(rawArgs string, expand bool, bodyWidth int) (string, bool) {
	var args writeDiffArgs
	if err := json.Unmarshal([]byte(strings.TrimSpace(rawArgs)), &args); err != nil {
		return "", false
	}
	if args.Path == "" {
		return "", false
	}
	header := fmt.Sprintf("%s · %s (overwrites if it exists)", args.Path, plural(lineCount(args.Content), "line"))
	var b strings.Builder
	b.WriteString(renderToolMetadata(r.th.Style("diffMeta"), header, bodyWidth))
	if args.Content != "" {
		b.WriteString("\n")
		b.WriteString(r.diffSide(args.Content, "+", "diffAdd", expand, bodyWidth))
	}
	return strings.TrimRight(b.String(), "\n"), true
}

// diffSide renders one side of a diff (all-removed or all-added): every line of
// text gets the prefix and the themed style, line-capped unless expanded. An
// empty side renders nothing. The text is sanitized (these go through lipgloss).
func (r *renderer) diffSide(text, prefix, slot string, expand bool, bodyWidth int) string {
	text = terminaltext.Sanitize(strings.TrimRight(text, "\n"))
	if text == "" {
		return ""
	}
	lines := strings.Split(text, "\n")
	var marker string
	if !expand && len(lines) > maxDiffLines {
		extra := len(lines) - maxDiffLines
		lines = lines[:maxDiffLines]
		marker = r.collapseMarker(extra)
	}
	style := r.th.Style(slot)
	var b strings.Builder
	for _, ln := range lines {
		// Prefix before wrapping so the source's diff marker and leading whitespace
		// remain attached to this source line, rather than being reconstructed after
		// a styled-card wrap.
		b.WriteString(style.Render(wrapToolCardRegion(prefix+" "+ln, bodyWidth)))
		b.WriteString("\n")
	}
	if marker != "" {
		// The collapse marker is muted, not coloured as a diff line.
		b.WriteString(lipgloss.NewStyle().Render(wrapToolCardRegion(marker, bodyWidth)))
		b.WriteString("\n")
	}
	return b.String()
}

// prettyJSON indents a raw JSON args string for display; non-JSON is returned
// as-is (single line). The result is terminal-sanitized since the payload is
// server-derived and rendered via lipgloss (not glamour). Empty/blank yields "".
func prettyJSON(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "{}" || raw == "null" {
		return ""
	}
	var buf bytes.Buffer
	if err := json.Indent(&buf, []byte(raw), "", "  "); err != nil {
		return terminaltext.Sanitize(raw)
	}
	return terminaltext.Sanitize(buf.String())
}

// summarizeArgs turns a JSON-object args string into a compact, scannable block
// of "key: value" rows in place of the full pretty-printed JSON (issue #24). It
// returns (summary, true) only for a JSON OBJECT; a bare array, a scalar, or
// malformed JSON returns ("", false) so renderTool falls back to prettyJSON and
// the current behaviour is preserved for odd shapes.
//
// Keys are ordered deterministically (argPriorityKeys first, then the rest
// alphabetical) — map iteration is random, so this is what makes the collapsed
// card golden-stable. At most maxSummaryRows rows render. EVERY rendered value
// passes through terminaltext.Sanitize (the summary is plain lipgloss, never glamour —
// see the CWE-150 invariant in sanitize.go). Keys are styled "muted", values
// "toolArgs".
//
// ctrl+t is never the only path to the data: the collapsed summary always sits
// behind the full prettyJSON expansion, advertised by an argRollupMarker footer
// (collapseMarker's shape) whenever ANYTHING was hidden — a key overflow
// ("… +K more keys · ctrl+t expand") OR a per-value collapse with no overflow
// ("… ctrl+t expand"). A card whose args are all short scalars hides nothing and
// shows no footer.
func (r *renderer) summarizeArgs(rawArgs string) (string, bool) {
	raw := strings.TrimSpace(rawArgs)
	if raw == "" {
		return "", false
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &obj); err != nil {
		// Not a JSON object (bare array/scalar) or malformed — fall back to prettyJSON.
		return "", false
	}
	if len(obj) == 0 {
		return "", false
	}
	keys := sortedArgKeys(obj)
	shown := keys
	if len(shown) > maxSummaryRows {
		shown = keys[:maxSummaryRows]
	}
	muted := r.th.Style("muted")
	valStyle := r.th.Style("toolArgs")
	var b strings.Builder
	valueCollapsed := false
	for i, k := range shown {
		if i > 0 {
			b.WriteString("\n")
		}
		text, collapsed := summarizeValueCollapsed(obj[k])
		valueCollapsed = valueCollapsed || collapsed
		b.WriteString(muted.Render(terminaltext.Sanitize(k) + ":"))
		b.WriteString(" ")
		b.WriteString(valStyle.Render(text))
	}
	// Advertise ctrl+t whenever ANYTHING was hidden: a key overflow OR a per-value
	// collapse (long string, big array/object). The marker mirrors collapseMarker's
	// shape so adjacent collapsed cards/results read consistently.
	if extra := len(keys) - len(shown); extra > 0 {
		b.WriteString("\n")
		b.WriteString(muted.Render(r.argRollupMarker(extra)))
	} else if valueCollapsed {
		b.WriteString("\n")
		b.WriteString(muted.Render(r.argRollupMarker(0)))
	}
	return b.String(), true
}

// argRollupMarker formats the collapsed-args affordance footer, matching
// collapseMarker's "  … <…> · <expand> expand" shape (leading "…", indented) so an
// arg roll-up and a line-capped result/diff don't show two different "there's
// more" idioms. n>0 names the hidden-key count ("+K more keys"); n==0 (a pure
// per-value collapse, no key overflow) shows just the expand hint. The chord
// reads the LIVE ExpandTools marking (r.marks.expandTools) so an override
// propagates (issue #457).
func (r *renderer) argRollupMarker(n int) string {
	if n <= 0 {
		return "  … " + r.marks.expandTools + " expand"
	}
	noun := "keys"
	if n == 1 {
		noun = "key"
	}
	return "  … +" + strconv.Itoa(n) + " more " + noun + " · " + r.marks.expandTools + " expand"
}

// sortedArgKeys returns obj's keys in deterministic render order: the keys in
// argPriorityKeys first (in that fixed order, only when present), then every
// remaining key alphabetical. This is the single source of the collapsed card's
// stable row order.
func sortedArgKeys(obj map[string]json.RawMessage) []string {
	out := make([]string, 0, len(obj))
	seen := make(map[string]bool, len(obj))
	for _, k := range argPriorityKeys {
		if _, ok := obj[k]; ok {
			out = append(out, k)
			seen[k] = true
		}
	}
	rest := make([]string, 0, len(obj))
	for k := range obj {
		if !seen[k] {
			rest = append(rest, k)
		}
	}
	sort.Strings(rest)
	return append(out, rest...)
}

// summarizeValue renders ONE arg value compactly for a collapsed card row,
// returning sanitized single-line text. It is a thin wrapper over
// summarizeValueCollapsed that drops the per-value "collapsed" signal, for callers
// (e.g. the result summary) that don't surface a ctrl+t affordance.
func summarizeValue(raw json.RawMessage) string {
	text, _ := summarizeValueCollapsed(raw)
	return text
}

// summarizeValueCollapsed renders ONE arg value compactly for a collapsed card
// row and reports whether the rendering HID anything (so summarizeArgs can decide
// to advertise the ctrl+t affordance even when the key cap did not trip):
//
//   - string: inline ("\"value\"") when single-line AND ≤ inlinePreviewLen runes
//     (collapsed=false); otherwise a size + line-count + quoted first-line preview
//     ("4.2 KB / 72 lines · \"## Context…\"") (collapsed=true).
//   - number/bool/null: the verbatim JSON token (collapsed=false).
//   - array: "[a, b]" inline when ≤ maxInlineArray scalar elements (collapsed=
//     false), else "N items" (collapsed=true).
//   - object: "N keys" (collapsed=true).
//
// All branches terminaltext.Sanitize their output, since the summary is plain
// lipgloss (never glamour).
func summarizeValueCollapsed(raw json.RawMessage) (string, bool) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		return "", false
	}
	switch trimmed[0] {
	case '"':
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return terminaltext.Sanitize(trimmed), false
		}
		text := summarizeStringValue(s)
		// A long/multiline string collapses to the size+preview form; the inline
		// quoted form keeps the whole value, so nothing is hidden.
		collapsed := lineCount(s) > 1 || len([]rune(s)) > inlinePreviewLen
		return text, collapsed
	case '[':
		return summarizeArrayValue(raw)
	case '{':
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(raw, &obj); err != nil {
			return terminaltext.Sanitize(trimmed), false
		}
		return plural(len(obj), "key"), true
	default:
		// number / bool / null — render the verbatim JSON token.
		return terminaltext.Sanitize(trimmed), false
	}
}

// summarizeStringValue renders a string arg value: inline-quoted when short and
// single-line, else a size + line-count + first-line preview. Sanitized. When the
// value spans more than one line the preview always carries a trailing "…" (even
// if the first line itself fit under the budget) to signal "more below".
func summarizeStringValue(s string) string {
	lines := lineCount(s)
	if lines <= 1 && len([]rune(s)) <= inlinePreviewLen {
		return terminaltext.Sanitize(strconv.Quote(s))
	}
	first := firstLine(s)
	preview := truncate(first, argPreviewLen)
	if lines > 1 && !strings.HasSuffix(preview, "…") {
		preview += "…"
	}
	preview = terminaltext.Sanitize(preview)
	return fmt.Sprintf("%s / %s · %q", humanizeBytes(int64(len(s))), plural(lines, "line"), preview)
}

// summarizeArrayValue renders a JSON array value and reports whether it collapsed:
// "[a, b]" inline (collapsed=false) when it has at most maxInlineArray SCALAR
// elements (no nested array/object), else "N items" (collapsed=true). Sanitized.
func summarizeArrayValue(raw json.RawMessage) (string, bool) {
	var elems []json.RawMessage
	if err := json.Unmarshal(raw, &elems); err != nil {
		return terminaltext.Sanitize(strings.TrimSpace(string(raw))), false
	}
	if len(elems) == 0 {
		return "[]", false
	}
	if len(elems) <= maxInlineArray && allScalars(elems) {
		parts := make([]string, len(elems))
		for i, e := range elems {
			parts[i] = scalarText(e)
		}
		return terminaltext.Sanitize("[" + strings.Join(parts, ", ") + "]"), false
	}
	return plural(len(elems), "item"), true
}

// allScalars reports whether every element is a JSON scalar (not an array or
// object) — the gate for inlining an array.
func allScalars(elems []json.RawMessage) bool {
	for _, e := range elems {
		t := strings.TrimSpace(string(e))
		if t == "" || t[0] == '[' || t[0] == '{' {
			return false
		}
	}
	return true
}

// scalarText renders a single scalar array element for the inline "[a, b]" form:
// a string element drops its JSON quotes (so labels read "[enhancement, bug]"),
// every other scalar is its verbatim token.
func scalarText(e json.RawMessage) string {
	t := strings.TrimSpace(string(e))
	if len(t) > 0 && t[0] == '"' {
		var s string
		if err := json.Unmarshal(e, &s); err == nil {
			return s
		}
	}
	return t
}

// firstLine returns the first line of s (up to the first newline), with a
// trailing carriage return trimmed.
func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return strings.TrimRight(s, "\r")
}

// humanizeBytes renders a byte count compactly: bytes verbatim under 1 KB, then
// "N.N KB"/"N.N MB"/"N.N GB"/"N.N TB" with one decimal (trailing ".0" trimmed).
// Sibling of the humanizeTokens/humanizeDuration formatters; used for the
// collapsed long-string arg row size signal AND the session-storage byte
// counts (which can run into the GB range). The math is SI/decimal (1 KB =
// 1000 B, 1 MB = 1e6 B, …) so a human-facing size reconciles with how
// file/content sizes are reported everywhere — the labels stay "KB"/"MB"/…
// (honest, not mislabelled KiB/MiB).
func humanizeBytes(n int64) string {
	if n < 0 {
		n = 0
	}
	const unit = 1000
	if n < unit {
		return strconv.FormatInt(n, 10) + " B"
	}
	div, exp := int64(unit), 0
	for value := n / unit; value >= unit && exp < 3; value /= unit {
		div *= unit
		exp++
	}
	return trimDecimal(float64(n)/float64(div)) + " " + [...]string{"KB", "MB", "GB", "TB"}[exp]
}

// parseMCPName splits an MCP tool name "mcp__<server>__<tool>" into its server
// and tool parts (the tool half may itself contain "__", so the split is on the
// FIRST "__" after the prefix). It returns ok=false for any non-MCP name, so a
// core tool (Read, Shell, …) keeps its plain head.
func parseMCPName(name string) (server, tool string, ok bool) {
	const prefix = "mcp__"
	if !strings.HasPrefix(name, prefix) {
		return "", "", false
	}
	rest := name[len(prefix):]
	i := strings.Index(rest, "__")
	if i <= 0 || i+2 >= len(rest) {
		return "", "", false
	}
	return rest[:i], rest[i+2:], true
}

// mcpServerNames maps well-known MCP server tokens to a display name; an unlisted
// server is humanized (title-cased).
var mcpServerNames = map[string]string{
	"github": "GitHub",
	"slack":  "Slack",
	"fetch":  "Fetch",
}

// mcpToolNames maps high-traffic MCP tool tokens to a polished verb phrase; an
// unlisted tool is humanized (underscores → spaces, first word capitalized).
var mcpToolNames = map[string]string{
	"issue_write":         "Issue write",
	"issue_read":          "Issue read",
	"create_pull_request": "Create pull request",
	"search_code":         "Search code",
	"get_file_contents":   "Get file contents",
}

// mcpTitle turns an MCP tool name into a friendly "<Server> · <Tool>" header
// (issue #24), e.g. "mcp__github__issue_write" → "GitHub · Issue write". It
// returns ok=false for any non-MCP name so renderTool keeps the plain head. Both
// halves are sanitized (defence-in-depth — the name is server-derived).
func mcpTitle(name string) (string, bool) {
	server, tool, ok := parseMCPName(name)
	if !ok {
		return "", false
	}
	return terminaltext.Sanitize(humanizeMCPServer(server) + " · " + humanizeMCPTool(tool)), true
}

// maxTitleCaseServer is the rune budget above which a server token is shown raw
// rather than title-cased — a long namespaced token ("io-github-stacklok-…")
// reads worse capitalized, so the raw token is the honest choice.
const maxTitleCaseServer = 20

// humanizeMCPServer renders an MCP server token: the known display name (the
// high-quality path), else the RAW token when title-casing would mangle it — a
// hyphenated token ("io-github-stacklok-playwright" → don't capitalize just the
// first letter) or an implausibly long one. A plain short token is title-cased.
func humanizeMCPServer(server string) string {
	if name, ok := mcpServerNames[server]; ok {
		return name
	}
	if strings.ContainsRune(server, '-') || len([]rune(server)) > maxTitleCaseServer {
		return server
	}
	return titleWord(server)
}

// humanizeMCPTool renders an MCP tool token: the known verb phrase, else the
// token with underscores turned to spaces and the first word capitalized
// ("bar_baz" → "Bar baz").
func humanizeMCPTool(tool string) string {
	if name, ok := mcpToolNames[tool]; ok {
		return name
	}
	words := strings.Split(tool, "_")
	if len(words) > 0 {
		words[0] = titleWord(words[0])
	}
	return strings.Join(words, " ")
}

// titleWord upper-cases the first rune of s, leaving the rest untouched (a light
// title-case for a single token; it never lower-cases an already-capped word).
func titleWord(s string) string {
	if s == "" {
		return ""
	}
	rs := []rune(s)
	rs[0] = []rune(strings.ToUpper(string(rs[0])))[0]
	return string(rs)
}

// summarizeResult renders a compact summary of a LARGE JSON tool result body
// (issue #24): a few prominent "key: value" rows (resultProminentKeys, in order)
// plus a size line ("· N keys" / "N items"). It engages only when body parses as
// a JSON object/array AND is large (more than maxToolResultLines lines OR over
// resultSummaryByteThreshold bytes); otherwise it returns ("", false) and the
// caller falls back to the existing line-capped truncateLines (so a Read result
// or a small/odd result is unchanged). All values are sanitized (plain lipgloss).
// summarizeResultDetail returns the summary plus the number of omitted object
// fields (or -1 for an abbreviated array) so its caller can advertise expansion
// when visual row truncation does not already do so.
func summarizeResultDetail(body string) (string, int, bool) {
	trimmed := strings.TrimSpace(body)
	if trimmed == "" {
		return "", 0, false
	}
	large := lineCount(body) > maxToolResultLines || len(body) > resultSummaryByteThreshold
	if !large {
		return "", 0, false
	}
	switch trimmed[0] {
	case '{':
		var obj map[string]json.RawMessage
		if err := json.Unmarshal([]byte(trimmed), &obj); err != nil {
			return "", 0, false
		}
		shown := 0
		var b strings.Builder
		for _, k := range resultProminentKeys {
			raw, ok := obj[k]
			if !ok {
				continue
			}
			shown++
			if b.Len() > 0 {
				b.WriteString("\n")
			}
			b.WriteString(terminaltext.Sanitize(k) + ":")
			b.WriteString(" ")
			b.WriteString(summarizeValue(raw))
		}
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString("· " + plural(len(obj), "key"))
		return b.String(), len(obj) - shown, true
	case '[':
		var elems []json.RawMessage
		if err := json.Unmarshal([]byte(trimmed), &elems); err != nil {
			return "", 0, false
		}
		hidden := 0
		if len(elems) > 0 {
			hidden = -1
		}
		return "· " + plural(len(elems), "item"), hidden, true
	default:
		return "", 0, false
	}
}

// summarizeResult preserves the summary-only API for callers that do not need
// the omitted-field expansion signal.
func (*renderer) summarizeResult(body string) (string, bool) {
	summary, _, ok := summarizeResultDetail(body)
	return summary, ok
}

// summarizeResolvedResult is the renderTool gate around summarizeResult: it
// engages only for a COLLAPSED, non-error result, returning the self-styled
// compact summary when summarizeResult accepts the body (a large JSON
// object/array). The expanded view, an error result, and a non-JSON/line-shaped
// result all return ok=false so renderTool falls through to the existing styled,
// line-capped/full body path (Read and prose results unchanged).
func (r *renderer) summarizeResolvedResult(b *block, expand bool) (string, bool) {
	summary, _, ok := r.summarizeResolvedResultDetail(b, expand)
	return summary, ok
}

func (*renderer) summarizeResolvedResultDetail(b *block, expand bool) (string, int, bool) {
	if expand || b.resultError {
		return "", 0, false
	}
	return summarizeResultDetail(b.resultBody)
}

// collapseMarker formats the "+N more line(s) · <expand> expand" affordance shown
// when a tool result or diff side is line-capped. The verb matches the footer
// help line's collapsed-state hint ("<expand> expand") — the expand/collapse pair
// is used consistently across help line, keybinding help, and this marker. The
// chord reads the LIVE ExpandTools marking (r.marks.expandTools) so an override
// propagates (issue #457).
func (r *renderer) collapseMarker(n int) string {
	return collapseMarkerMark(n, r.marks.expandTools)
}

// truncateLinesTailMark is the free-function core of truncateLinesTail, taking the
// expand chord explicitly so non-renderer callers (the MCP resource preview, which
// has no *renderer) can thread the LIVE ExpandTools marking through (issue #457).
func truncateLinesTailMark(s string, maxLines int, tail, expandMark string) string {
	s = terminaltext.Sanitize(strings.TrimRight(s, "\n"))
	if s == "" {
		return ""
	}
	lines := strings.Split(s, "\n")
	if len(lines) <= maxLines {
		return s
	}
	kept := lines[:maxLines]
	marker := tail
	if marker == "" {
		marker = collapseMarkerMark(len(lines)-maxLines, expandMark)
	}
	return strings.Join(kept, "\n") + "\n" + lipgloss.NewStyle().Render(marker)
}

// collapseMarkerMark is the free-function core of collapseMarker, taking the
// expand chord explicitly (issue #457).
func collapseMarkerMark(n int, expandMark string) string {
	noun := "lines"
	if n == 1 {
		noun = "line"
	}
	return "  … +" + strconv.Itoa(n) + " more " + noun + " · " + expandMark + " expand"
}

// lineCount returns the number of text lines in s (0 for empty, otherwise one
// more than the number of newlines, ignoring a single trailing newline). Used
// for the diff header size signals.
func lineCount(s string) int {
	s = strings.TrimRight(s, "\n")
	if s == "" {
		return 0
	}
	return strings.Count(s, "\n") + 1
}
