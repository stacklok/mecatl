package ui

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"

	"charm.land/bubbles/v2/viewport"
	"charm.land/glamour/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/theme"
)

// maxToolResultLines caps how many lines of a tool result are shown inline; the
// rest collapse into a "+N more lines" affordance so a giant Read result doesn't
// drown the scrollback.
const maxToolResultLines = 12

// maxDiffLines caps how many lines of each diff side (Edit old/new, Write
// content) show inline when collapsed; ctrl+t expands to the full diff.
const maxDiffLines = 12

// toolCardMaxWidth caps a tool card's column width on a wide terminal: past this
// the card stops growing with the viewport so a long line stays at a readable
// measure instead of stretching edge to edge. On a narrow terminal the existing
// r.width-2 inset wins (the card never exceeds the viewport).
const toolCardMaxWidth = 100

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
// glamour TermRenderer cache (keyed by wrap width), the active theme, and two
// per-block memo layers: blockCache (whole rendered blocks, keyed on
// rev/width/expand — see renderBlock) and blockMD (the assistant glamour step,
// keyed on src/width — see markdownAt). glamour is NOT thread-safe, so renderer
// is only ever touched from the Bubble Tea update goroutine — never from the
// stream reader. The mutex guards the cache map against the (currently
// single-goroutine) access defensively and documents the invariant; it does not
// make glamour itself concurrency-safe.
type renderer struct {
	th    theme.Theme
	width int

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

	// blockMD memoizes the glamour render of each assistant block, keyed by the
	// block's (stable, append-only) conversation index. It is the INNER of the two
	// per-block memo layers: blockCache (below) memoizes the whole rendered block
	// (any kind) keyed on (rev, width, expand), while blockMD memoizes only the
	// glamour markdown step of an assistant block keyed on (src, width) — so an
	// assistant block whose rev changed for a non-text reason (e.g. the turn-end
	// endReasoningStream) misses the outer cache but still reuses its glamour
	// render here. refreshView re-renders the WHOLE scrollback on every flushed
	// frame (deltas are coalesced to frame cadence; see update.go's renderTickMsg).
	// Without this, every prior assistant turn is re-parsed through glamour on
	// every flushed frame of the live turn — O(turns × frames) glamour work that
	// grows with session length. Memoising collapses each SETTLED block to one
	// render: only the live (last) block, whose src grows each frame, misses and
	// re-renders. markdown() is a pure function of (src, width, theme) and the
	// theme is fixed for the renderer's life, so the cached entry is valid whenever
	// its (src, width) still match — index is just the bucket that bounds memory to
	// one entry per block and lets the live block overwrite in place. Touched only
	// on the Bubble Tea update goroutine (same invariant as the glamour cache), so
	// it needs no lock. Dropped (with blockCache) by resetBlockCaches when the
	// conversation is rebuilt, since a fresh conversation reuses the same indices.
	blockMD map[int]mdEntry

	// blockCache memoizes the FULL rendered string of every block (all kinds, not
	// just assistant markdown), keyed by the block's stable conversation index. An
	// entry is valid while the block's render revision (block.rev — bumped by the
	// conversation's mutation gateways), the wrap width, and the global expand
	// toggle all match, so on each flushed frame every SETTLED block joins the
	// conversation string straight from cache and only mutated blocks (in practice
	// the live tail) re-render through renderBlockFresh. Correctness rests on the
	// same purity argument as blockMD — a block's render is a pure function of
	// (block fields, width, expand, theme) with the theme fixed per process — plus
	// the rev discipline documented on block.rev. Update-goroutine-only; dropped by
	// resetBlockCaches when the conversation is rebuilt (index reuse).
	blockCache map[int]blockEntry

	// mdRenders counts REAL glamour invocations (cache misses) — incremented at the
	// tr.Render call site in markdown(), not in markdownAt's hit path. It is the test
	// seam proving the delta-coalescing actually elides per-token renders: N streamed
	// deltas with no frame flush leave it unchanged, and one flush bumps it by exactly
	// one (the live block re-renders once). Touched only on the update goroutine.
	mdRenders int

	// blockRenders counts REAL whole-block renders (blockCache misses) — incremented
	// only when renderBlock falls through to renderBlockFresh, never on a cache hit.
	// It is the test seam proving settled blocks join from cache: a flushed frame of
	// a streaming turn bumps it by exactly one (the live block), regardless of how
	// long the scrollback is. Touched only on the update goroutine.
	blockRenders int

	// inputKey/inputView/inputValid memoize the rendered INPUT region (the bubbles
	// textarea) — the input-side sibling of blockCache. textarea.View() re-wraps
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

	// joinCache/joinValid/joinKey memoize the WHOLE joined conversation string —
	// the OUTERMOST of the render memo layers, above blockCache. renderConversation
	// is called once per flushed frame and, even when every block hits blockCache,
	// re-joins all cached block strings into a fresh strings.Builder every time —
	// O(scrollback) byte copying that profiling showed dominated the per-frame cost
	// on a long scrollback (the per-block cache had already eliminated the styling
	// cost). This memo skips the rebuild entirely when nothing changed since the
	// last frame: the join is reused verbatim. Validity rests on the SAME purity
	// argument as blockCache — the joined string is a pure function of the per-block
	// renders, which are themselves keyed on (rev, width, expand) — so the key is a
	// signature that a frame leaving blockRenders untouched (no block re-rendered)
	// at the same (block count, width, expand) produced byte-identical block
	// strings and therefore a byte-identical join. Update-goroutine-only, like the
	// block caches; dropped by resetBlockCaches (the block-index reuse that invalidates
	// blockCache equally invalidates a join built over it).
	joinCache string
	joinValid bool
	joinKey   joinRenderKey

	// joinScratch is the per-frame buffer of per-block render strings, REUSED across
	// frames (truncated to [:0] and re-appended each call) so the block walk adds no
	// per-frame allocation on the steady-state join hit. The strings it holds are the
	// same ones blockCache already retains, so it pins nothing extra.
	joinScratch []string

	// vpViewCache/vpViewValid memoize the rendered VIEWPORT OUTPUT — the OUTERMOST
	// render layer, above blockCache and joinCache. View() calls vp.View() which runs
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

	// joinPrefixLines / joinPrefixN / joinPrefixKey are the INCREMENTAL-join state
	// powering renderConversationLines: the streaming-frame fast path that skips the
	// O(scrollback) rejoin the whole-join memo (joinCache, above) cannot help with —
	// during streaming the live tail block re-renders every token, so blockRenders
	// bumps every frame and the joinCache fast path always misses, forcing a full
	// Builder copy of the entire scrollback per frame.
	//
	// The canonical per-block segment framing is: block i contributes
	// sep(i) + scratch[i] + "\n", where sep(0)="" and sep(i>0)="\n". The full join is
	// the concatenation of all segments, byte-for-byte identical to the monolithic
	// loop below — so the PREFIX (segments [0, joinPrefixN)) plus a freshly built
	// SUFFIX (segments [joinPrefixN, n)) is exactly today's output.
	//
	// joinPrefixLines holds the prefix ALREADY SPLIT into single (newline-free) lines,
	// so renderConversationLines can hand it straight to vp.SetContentLines (no Split,
	// no Builder copy of the settled scrollback) and only the suffix is built fresh
	// each frame. joinPrefixN is the number of blocks the cached prefix covers;
	// joinPrefixKey pins the (width, expand) it was built at (the prefix blocks did NOT
	// re-render — that is precisely why they are in the prefix — so no per-block rev
	// is needed; width/expand are the global axes that would change every block's
	// render at once). A NON-TAIL mutation (resolveTool/subagentBlock/teamBlock bump a
	// block BEHIND the tail) lowers firstChanged below joinPrefixN, which truncates the
	// prefix before the changed index — never serving it stale.
	joinPrefixLines []string
	joinPrefixN     int
	joinPrefixKey   joinPrefixState
}

// joinPrefixState is the validity key of the cached incremental-join prefix: the
// width and expand toggle it was built under. (The prefix blocks did not re-render
// this frame — they are in the prefix BECAUSE they were unchanged — so a per-block
// rev is unnecessary; a width or expand change re-renders every block and must drop
// the prefix.)
type joinPrefixState struct {
	width  int
	expand bool
}

// joinRenderKey is the validity key of the memoized conversation join. blockRenders
// is the cache-miss counter captured AFTER the per-block walk: if it is unchanged
// between two frames AND the block count / width / expand all match, no block
// re-rendered, every block string is byte-identical to last frame, and the joined
// string can be reused verbatim. (blockRenders is monotonic and only ever bumped on
// a real renderBlockFresh, so an equal value across frames is a sound "nothing
// changed" signal.)
type joinRenderKey struct {
	blockRenders int
	nBlocks      int
	width        int
	expand       bool
}

// inputRenderKey is the validity key of the memoized input render: the complete
// set of textarea facts renderInput's output is a pure function of — the buffer
// content, the cursor position (logical row + soft-wrap row/column offsets, so a
// cursor move inside an unchanged value still re-renders), the focus state (which
// also fully determines the virtual cursor's blink phase — mecatui never routes
// cursor.BlinkMsg to the textarea, so the cursor is static: visible while focused,
// hidden while blurred; if blink routing is ever added, the blink phase must join
// this key), and the box dimensions. The theme, placeholder, and prompt are fixed
// per process and need no key slot.
type inputRenderKey struct {
	value         string
	row           int // cursor's logical line (textarea.Line)
	rowOffset     int // cursor's soft-wrap row within that line (LineInfo.RowOffset)
	colOffset     int // cursor's column within that soft-wrap row (LineInfo.ColumnOffset)
	focused       bool
	width, height int
	mode          string
}

// mdEntry is one memoized assistant-block render: the source text and wrap width
// it was produced from (the validity key) plus the rendered ANSI output.
type mdEntry struct {
	src   string
	width int
	out   string
}

// blockEntry is one memoized whole-block render: the block revision, wrap width,
// and expand state it was produced under (the validity key) plus the rendered
// ANSI output.
type blockEntry struct {
	rev    int
	width  int
	expand bool
	out    string
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

// newRenderer builds a renderer for a theme.
func newRenderer(th theme.Theme) *renderer {
	return &renderer{
		th:         th,
		indent:     defaultBlockIndent,
		cache:      map[int]*glamour.TermRenderer{},
		blockMD:    map[int]mdEntry{},
		blockCache: map[int]blockEntry{},
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
// the indented string is what blockCache stores and every steady-state frame joins the
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

// resetBlockCaches drops BOTH per-block memo layers (blockCache and blockMD).
// It MUST be called whenever the conversation is rebuilt from scratch (/clear,
// the /models restart-now handoff — see Model.resetSession): both caches key on
// the block's conversation INDEX, and a fresh conversation reuses indices 0..n
// for entirely different blocks whose rev/src could coincidentally match a stale
// entry, which would alias an old block's render onto a new one.
func (r *renderer) resetBlockCaches() {
	r.blockCache = map[int]blockEntry{}
	r.blockMD = map[int]mdEntry{}
	// Drop the whole-conversation join memo too: it is built over blockCache, so the
	// index reuse that aliases a stale block entry would equally alias a stale join.
	r.joinValid = false
	r.joinKey = joinRenderKey{}
	// And the incremental-join prefix, for the same index-reuse reason: the prefix is
	// the cached render of blocks [0, joinPrefixN), so a rebuilt conversation reusing
	// those indices would otherwise serve a stale prefix.
	r.joinPrefixLines = r.joinPrefixLines[:0]
	r.joinPrefixN = 0
	r.joinPrefixKey = joinPrefixState{}
	// Drop the viewport-output memo too (defense-in-depth): every CURRENT resetSession
	// caller calls refreshView() afterwards (which invalidateVPView()s), but clearing it
	// here makes that ordering non-load-bearing — a future caller that forgets refreshView
	// can never serve a stale vpView against a reset/empty conversation.
	r.vpViewValid = false
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
	// viewport width. Retained as harmless hygiene (and to mirror the two-column
	// inset tool cards get from Width(r.width-2)), NOT as the scramble fix — the
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
	if e, ok := r.blockMD[idx]; ok && e.src == src && e.width == r.width {
		return e.out
	}
	out := r.markdown(src)
	if r.blockMD == nil {
		// Zero-value safety: a bare &renderer{th: th} (see the renderBlock comment)
		// never went through newRenderer; lazy-init so an assistant block rendered
		// through one stays safe.
		r.blockMD = map[int]mdEntry{}
	}
	r.blockMD[idx] = mdEntry{src: src, width: r.width, out: out}
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
//  1. As-is. Already-agreeing clusters (bare ✅ U+2705, the ZWJ family 👨‍👩‍👧,
//     a letter + combining accent like á — all width-stable) pass through
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

// renderConversation joins every block into the viewport content string. expand
// is the global tool-output toggle (ctrl+t): when true, tool result bodies and
// Edit/Write diffs render in full instead of line-capped.
//
// It is called on every flushed frame (frame-coalesced during streaming; see
// update.go's renderTickMsg). TWO memo layers keep the per-frame cost off the
// scrollback length: each SETTLED block joins from the per-block cache via
// renderBlock (only blocks whose (rev, width, expand) changed render fresh), and
// the WHOLE joined string is itself memoized in joinCache. A frame that re-renders
// no block (blockRenders unchanged) at the same block count / width / expand
// reuses the previous join verbatim, skipping the O(scrollback) Builder copy that
// profiling showed dominated the per-frame cost on a long scrollback. The block
// walk below still runs every frame (cheap on cache hits) so the live tail block
// re-renders and bumps blockRenders — the signal that drives join invalidation.
// (The viewport's SetContent line split is a separate, smaller cost, out of scope
// here.)
func (r *renderer) renderConversation(c *conversation, expand bool) string {
	before := r.blockRenders
	r.walkBlocks(c, expand)

	// Fast path: no block re-rendered this frame and the join signature is
	// unchanged, so the previously joined string is still byte-identical — reuse it
	// without rebuilding the Builder.
	//
	// `r.blockRenders == before` is THE load-bearing invalidation signal: every
	// render-visible mutation bumps the block's rev → blockCache miss →
	// renderBlockFresh → blockRenders++, so any change to a block's output during
	// this frame's walk trips this check (the cache-invalidation tests all exercise
	// it). The joinKey {nBlocks, width, expand} fields are belt-and-suspenders, NOT
	// dead code: they guard a future change that could alter the JOINED output
	// WITHOUT re-rendering any block — e.g. a width- or expand-dependent join
	// separator, or a block-count-dependent header — a case today's tests cannot
	// reach (so they prove blockRenders, not the key). Keep them.
	if r.joinValid && r.blockRenders == before &&
		r.joinKey == (joinRenderKey{blockRenders: before, nBlocks: len(c.blocks), width: r.width, expand: expand}) {
		return r.joinCache
	}

	var b strings.Builder
	for i, s := range r.joinScratch {
		if i > 0 {
			b.WriteString(blockSepAfter(c.blocks, i-1))
		}
		b.WriteString(s)
		b.WriteString("\n")
	}
	result := b.String()
	// Record the POST-walk blockRenders so the NEXT frame compares against the value
	// this join was built at: an intervening re-render bumps blockRenders past it and
	// correctly misses the fast path.
	r.joinCache = result
	r.joinValid = true
	r.joinKey = joinRenderKey{blockRenders: r.blockRenders, nBlocks: len(c.blocks), width: r.width, expand: expand}
	return result
}

// walkBlocks renders every block (warming the per-block cache, re-rendering the
// live tail) into the reused joinScratch buffer and returns firstChanged: the
// LOWEST block index that re-rendered this frame (sentinel len(c.blocks) = nothing
// changed). It detects a re-render by comparing blockRenders before/after each
// renderBlock call — the same monotonic cache-miss counter the join memo keys on —
// so it stays decoupled from renderBlock itself. firstChanged is the truncation
// point the incremental-join prefix relies on: every block below it hit the cache
// and is byte-identical to last frame, so the prefix [0, firstChanged) is reusable;
// a NON-TAIL mutation lowers firstChanged to the mutated index, truncating the
// prefix before it.
func (r *renderer) walkBlocks(c *conversation, expand bool) (firstChanged int) {
	firstChanged = len(c.blocks)
	r.joinScratch = r.joinScratch[:0]
	for i := range c.blocks {
		before := r.blockRenders
		r.joinScratch = append(r.joinScratch, r.renderBlock(i, &c.blocks[i], expand))
		if r.blockRenders != before && i < firstChanged {
			firstChanged = i
		}
	}
	return firstChanged
}

// renderConversationLines is the line-slice sibling of renderConversation: it
// returns the conversation as a slice of single (newline-free) lines ready for
// vp.SetContentLines, REUSING the cached prefix of settled blocks and only building
// the changed suffix fresh each frame. This is the streaming-frame fast path — the
// live tail re-renders every token, so the whole-join memo (joinCache) always
// misses during streaming and a full O(scrollback) Builder copy fires every frame;
// here only the (small) suffix is rebuilt.
//
// Canonical segment framing (identical to renderConversation's monolithic loop):
// block i contributes sep(i) + scratch[i] + "\n" with sep(0)="" and sep(i>0)="\n".
// The prefix is the line-split of segments [0, prefixN); the suffix is the
// line-split of segments [prefixN, n). prefixN = min(firstChanged, n): blocks below
// firstChanged did NOT re-render, so they are byte-identical to last frame.
//
// Correctness invalidation (a stale prefix is worse than the perf cost): the cached
// prefix is reused ONLY when it covers EXACTLY [0, prefixN) — i.e. joinPrefixN ==
// prefixN — at the SAME (width, expand). The joinPrefixN == prefixN check is THE
// load-bearing guard: a NON-TAIL mutation lowers firstChanged (→ prefixN), a block
// count shrink lowers prefixN, and a width/expand change re-renders EVERY block so
// firstChanged drops to 0 (→ prefixN 0) — all three move prefixN away from the cached
// joinPrefixN and force a rebuild. The joinPrefixKey (width, expand) compare is
// therefore belt-and-suspenders, NOT separately load-bearing — exactly like the
// joinKey {width, expand} fields on the whole-join memo: it guards a hypothetical
// future where the prefix could change WITHOUT firstChanged dropping to 0 (e.g. a
// width-dependent inter-block separator), a case today's render cannot reach. Keep
// it for the same reason joinKey keeps its fields; removing it is not observable
// because firstChanged already subsumes it (see the report's mutation note).
//
// The returned slice is freshly allocated EACH call (prefix lines appended into a
// new backing array, then suffix lines), so vp.SetContentLines — which retains and
// may mutate the slice it is handed (slices.Insert on embedded newlines) — never
// corrupts the cached joinPrefixLines. The slice safety rests on TWO facts: the
// fresh backing array (SetContentLines mutates that array, not joinPrefixLines'),
// and Go string immutability (the prefix STRINGS are shared by value but can never
// be mutated in place). It does NOT rely on the prefix lines being newline-free —
// they are split single lines, but SetContentLines is free to re-split them and the
// fresh array still absorbs the result.
func (r *renderer) renderConversationLines(c *conversation, expand bool) []string {
	firstChanged := r.walkBlocks(c, expand)
	n := len(r.joinScratch)
	prefixN := min(firstChanged, n)

	// Decide whether the cached prefix still covers exactly [0, prefixN) at the
	// current width/expand. If not, rebuild it from the settled segments.
	wantKey := joinPrefixState{width: r.width, expand: expand}
	if r.joinPrefixKey != wantKey || r.joinPrefixN != prefixN {
		r.rebuildPrefix(c.blocks, prefixN)
		r.joinPrefixN = prefixN
		r.joinPrefixKey = wantKey
	}

	// Assemble the frame: a fresh slice = cached prefix lines + freshly-split suffix
	// segments + the ONE terminal "" element. Pre-size generously to keep the suffix
	// appends allocation-light.
	lines := make([]string, 0, len(r.joinPrefixLines)+(n-prefixN)*2+1)
	lines = append(lines, r.joinPrefixLines...)
	for i := prefixN; i < n; i++ {
		appendSegmentLines(&lines, c.blocks, i, r.joinScratch[i])
	}
	// The full join ends with the last segment's trailing "\n", whose split tail is a
	// single trailing "" element — the same one strings.Split(join, "\n") produces.
	// It belongs to no segment's prefix-cacheable lines (when a suffix exists, an
	// inter-block "\n\n" boundary owns the blank line via the next segment's leading
	// ""), so it is appended once here, per frame, at the absolute end — covering the
	// empty conversation too (n==0 → just [""], which SetContentLines maps to nil).
	lines = append(lines, "")
	return lines
}

// rebuildPrefix rebuilds joinPrefixLines as the line-split of segments [0, prefixN),
// reusing the existing backing array (truncate-and-append). prefixN==0 leaves it
// empty. blocks is the conversation's block slice, threaded through so
// appendSegmentLines can apply per-kind separator widths.
func (r *renderer) rebuildPrefix(blocks []block, prefixN int) {
	r.joinPrefixLines = r.joinPrefixLines[:0]
	for i := 0; i < prefixN; i++ {
		appendSegmentLines(&r.joinPrefixLines, blocks, i, r.joinScratch[i])
	}
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
func blockSepAfter(blocks []block, i int) string {
	switch blocks[i].kind {
	case blockTool:
		return interBlockSepNone
	case blockTurnStat:
		return interBlockSepCompact
	case blockAssistant:
		if i+1 < len(blocks) && blocks[i+1].kind == blockTurnStat {
			return interBlockSepNone
		}
	}
	return interBlockSepCompact
}

// blockBlankLinesAfter is the lines-path mirror of blockSepAfter: it returns the
// number of blank "" lines to insert before block i (i.e. after block i-1).
func blockBlankLinesAfter(blocks []block, i int) int {
	switch blocks[i-1].kind {
	case blockTool:
		return interBlockBlankLinesNone
	case blockTurnStat:
		return interBlockBlankLinesCompact
	case blockAssistant:
		if i < len(blocks) && blocks[i].kind == blockTurnStat {
			return interBlockBlankLinesNone
		}
	}
	return interBlockBlankLinesCompact
}

// appendSegmentLines appends block i's content lines to dst, modelling the canonical
// segment sep(i) + scratch + "\n" (sep(0)="", sep(i>0)=blockSepAfter(blocks,i-1))
// MINUS its trailing "\n" — that terminal "\n"'s split tail is handled once, at the
// absolute end of the frame, by renderConversationLines. The leading inter-block
// separator of block i>0 becomes blockBlankLinesAfter(blocks,i) blank "" lines BEFORE
// the block's content; the content itself is scratch split on "\n". Concatenated across
// all blocks this yields strings.Split(fullJoin, "\n") exactly, modulo that single
// terminal "".
func appendSegmentLines(dst *[]string, blocks []block, i int, scratch string) {
	if i > 0 {
		// The inter-block separator produces blank lines before this block; the count
		// depends on the previous block's kind (must match blockSepAfter byte-for-byte).
		for n := 0; n < blockBlankLinesAfter(blocks, i); n++ {
			*dst = append(*dst, "")
		}
	}
	// scratch may be empty (an empty block render); SplitSeq still yields one ""
	// element for it, matching strings.Split over the full join.
	for line := range strings.SplitSeq(scratch, "\n") {
		*dst = append(*dst, line)
	}
}

// renderBlock is the CACHED per-block entry point: it returns the memoized
// render when the block's revision, the wrap width, and the expand toggle all
// match the cached entry, and otherwise renders fresh via renderBlockFresh,
// stores the result, and bumps blockRenders (the cache-miss test seam). idx is
// the block's stable conversation index (blocks are append-only within a
// conversation; resetBlockCaches handles index reuse across rebuilds).
// Correctness rests on the block.rev discipline: every post-append mutation of a
// render-visible field bumps rev through a conversation gateway, so a cache hit
// can never be stale. Update-goroutine-only.
func (r *renderer) renderBlock(idx int, b *block, expand bool) string {
	if e, ok := r.blockCache[idx]; ok && e.rev == b.rev && e.width == r.width && e.expand == expand {
		return e.out
	}
	out := r.indentLines(r.renderBlockFresh(idx, b, expand))
	if r.blockCache == nil {
		// Zero-value safety: a bare &renderer{th: th} never calls newRenderer. Two
		// production sites construct one — the width-0 team focus renderer
		// (team.go, renderTeamFocus) and the fleet focus renderer (agents_overlay.go,
		// renderSubagentFocus) — plus direct test construction. Those sites only call
		// the trace/chip helpers today, but the safety must be uniform: markdown and
		// markdownAt carry matching lazy-inits for their maps (r.cache / r.blockMD),
		// so ANY render path on a bare renderer is safe, not just non-assistant
		// blocks through here.
		r.blockCache = map[int]blockEntry{}
	}
	r.blockCache[idx] = blockEntry{rev: b.rev, width: r.width, expand: expand, out: out}
	r.blockRenders++
	// CORRECTNESS CHOKEPOINT (shared by BOTH render paths): a fresh render of a block
	// inside the cached incremental-join prefix invalidates that prefix. The prefix
	// (joinPrefixLines, maintained by renderConversationLines) covers [0, joinPrefixN),
	// but blockCache/blockRenders are mutated by renderConversation (the string path
	// taken under selection/expand) TOO — so without this, a string-path frame could
	// re-render a non-tail block (resolveTool/subagentBlock/teamBlock/an
	// ex-tail assistant), update blockCache, and leave joinPrefixLines stale; the next
	// lines-path frame would then HIT blockCache for that block (no blockRenders bump,
	// firstChanged stays high, joinPrefixN matches) and serve the STALE prefix.
	// Couple the invalidation to this single shared re-render point — not the
	// lines-path walk — so either path drops the prefix the instant a covered block
	// re-renders. Invalidate-to-0 (full rebuild next lines frame) is the simplest
	// correct choice; the cost is paid only on the rare non-tail-mutation/path-switch
	// frame, never on the streaming hot path (where the tail block is at index n-1 ≥
	// joinPrefixN, so this never fires). INVARIANT: after any frame on either path,
	// joinPrefixLines covers exactly [0, joinPrefixN) and no block in [0, joinPrefixN)
	// has re-rendered since the prefix was built.
	if idx < r.joinPrefixN {
		r.joinPrefixLines = r.joinPrefixLines[:0]
		r.joinPrefixN = 0
		r.joinPrefixKey = joinPrefixState{}
	}
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
		// The user block is the gold left rail + the "▌ you" label over the plain
		// viewport surface — NO background tint (the faint panel tint belongs only to the
		// input box, never the conversation history). The body wraps through the normal
		// wrapStyled path, which owns the width-guard (no per-call Width needed now that
		// there is no background to fill out to the column).
		label := r.th.Style("userLabel").Render("▌ you")
		body := r.wrapStyled(sanitizeTerminal(b.raw), r.th.Style("userBlock"))
		out := label + "\n" + body
		// Render one muted placeholder line per attached media part, so a multimodal
		// prompt is never silently shown as text-only. Media is attached via the
		// @-mention menu (type "@" then a path; an image/audio file becomes a part),
		// gated on the server's advertised image/audio caps — see mention.go and
		// client.ExpandMentions.
		for _, m := range b.media {
			out += "\n" + r.wrapPrefixed("📎 ", sanitizeTerminal(m), r.th.Style("muted"))
		}
		return out
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
		if b.recover {
			// A recover-notice is an actionable WARNING, not a muted compaction
			// bullet: render it with the warning style + ⚠ so it stands out.
			return r.wrapPrefixed("⚠ ", sanitizeTerminal(b.raw), r.th.Style("warning"))
		}
		return r.wrapPrefixed("• ", sanitizeTerminal(b.raw), r.th.Style("muted"))
	case blockHook:
		return r.renderHook(b)
	case blockTurnStat:
		return r.wrapStyled(sanitizeTerminal(b.raw), r.th.Style("muted"))
	case blockError:
		if b.permanent {
			return r.renderPermanentError(b, expand)
		}
		return r.wrapPrefixed("✗ ", sanitizeTerminal(b.raw), r.th.Style("errorText"))
	case blockDelivery:
		return r.renderDelivery(b)
	default:
		return r.wrapStyled(sanitizeTerminal(b.raw), lipgloss.NewStyle())
	}
}

// renderPermanentError renders a PERMANENT error block: a one-line human summary
// derived from the provider error, with the raw payload available on expand (ctrl+t).
func (r *renderer) renderPermanentError(b *block, expand bool) string {
	summary := permanentErrorSummary(b.raw)
	errStyle := r.th.Style("errorText")
	line := r.wrapPrefixed("✗ ", summary, errStyle)
	if !expand {
		return line
	}
	// Expanded: the summary line + the raw error under a dim header.
	raw := r.wrapStyled(sanitizeTerminal(b.raw), r.th.Style("muted"))
	return line + "\n" + r.th.Style("muted").Render("raw payload:") + "\n" + raw
}

// permanentErrorSummary derives a one-line human-readable summary from a provider
// error string. The anthropic/openaichat adapters format translated EVENT errors as
// "code: message" — take the first line, truncate sanely, and append the "retrying
// won't help" advisory. The openai/openaichat SDK transport error (stream.Err, an
// HTTP-level rejection before any SSE event) comes through as a raw
// 'POST "<url>": 400 Bad Request {"error":{…json…}}' string; collapseErrorSummary
// strips the 'POST "…"' prefix and trailing JSON, extracting the embedded message so
// the summary is not a truncated-JSON wall (matching the "code: message" shape). The
// summary is TERMINAL-SANITIZED (control/ANSI sequences scrubbed) so a hostile
// provider/gateway can't inject escapes into the scrollback — the blockError path
// already sanitizes via sanitizeTerminal, and the permanent path must too. Falls
// back to a generic message when the error is unparseable or blank.
//
// The extraction lives in the TUI (not the adapter Error()) because the adapters'
// Error() text is a deliberate byte-identical invariant (TestResponseStreamErrorMessageUnchanged);
// the SDK transport error is forwarded verbatim and has no such invariant, but the
// render layer is the single chokepoint that covers both the translated-event and the
// transport-error shapes without touching either adapter's contract.
func permanentErrorSummary(raw string) string {
	// Collapse the SDK transport shape (POST "…" + trailing JSON) into a clean
	// token on the FULL raw string BEFORE first-line truncation: a single-line
	// SDK error like 'POST "<url>": 400 Bad Request {"error":{…json…}}' would
	// otherwise be truncated mid-JSON by firstLineCap, leaving a fragment
	// extractJSONMessage cannot parse. Collapsing first yields a clean first
	// line that firstLineCap then caps sanely.
	first := firstLineCap(collapseErrorSummary(raw), 120)
	if first == "" {
		return "permanent provider error — retrying won't help; the request is rejected. Start a new session."
	}
	return sanitizeTerminal(first) + " — retrying won't help; the request is rejected. Start a new session."
}

// collapseErrorSummary rewrites a provider/SDK error first-line into a cleaner
// human-readable token. It strips a leading 'POST "<url>"' SDK-transport prefix and
// a trailing JSON object ('{"error":{…}}'), so an openai-go error like
// 'POST "https://api.openai.com/v1/responses": 400 Bad Request {"error":{"message":"…"}}'
// collapses to '400 Bad Request' plus any extracted message. A plain 'code: message'
// (anthropic / the translated event error) is returned unchanged.
func collapseErrorSummary(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	// Strip a leading SDK transport prefix: 'POST "<url>": ' (or any
	// '<METHOD> "<url>": ' shape the openai-go SDK emits). Keep everything after it.
	if i := strings.Index(s, `": `); i >= 0 && strings.HasPrefix(s, `POST "`) {
		s = strings.TrimSpace(s[i+len(`": `):])
	}
	// A trailing JSON object ('{"error":{…}}' or bare '{…}') is opaque in a one-line
	// summary — replace it with its embedded "message" field if present, else drop it.
	if i := strings.IndexByte(s, '{'); i >= 0 {
		head := strings.TrimRight(s[:i], " :")
		tail := s[i:]
		if msg := extractJSONMessage(tail); msg != "" {
			if head != "" {
				return head + ": " + msg
			}
			return msg
		}
		return head
	}
	return s
}

// extractJSONMessage best-effort extracts the "message" string field from a leading
// JSON object (an OpenAI error envelope like {"error":{"code":"…","message":"…"}}).
// It returns "" if the JSON cannot be parsed or carries no string "message" field.
func extractJSONMessage(s string) string {
	var env map[string]json.RawMessage
	if err := json.Unmarshal([]byte(s), &env); err != nil {
		return ""
	}
	// An OpenAI envelope nests {"error": {...}}; unwrap one level.
	if raw, ok := env["error"]; ok {
		if err := json.Unmarshal(raw, &env); err != nil {
			return ""
		}
	}
	if raw, ok := env["message"]; ok {
		var msg string
		if err := json.Unmarshal(raw, &msg); err == nil {
			return strings.TrimSpace(msg)
		}
	}
	return ""
}

// firstLineCap returns the first line of s (up to a newline or maxRunes runes,
// whichever is shorter). Returns "" for an empty/blank string.
func firstLineCap(s string, maxRunes int) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if first, _, ok := strings.Cut(s, "\n"); ok {
		s = first
	}
	// Rune-safe truncation: keep at most maxRunes runes.
	if rs := []rune(s); len(rs) > maxRunes {
		s = string(rs[:maxRunes])
	}
	return s
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
	text := sanitizeTerminal(strings.TrimRight(b.reasoning, "\n"))
	n := lineCount(text)
	if !expand {
		if b.reasoningStreaming {
			return style.Render("reasoning…")
		}
		return style.Render("reasoning summary · " + plural(n, "line") + " · ctrl+t expand")
	}
	header := style.Render("reasoning summary · " + plural(n, "line") + " · ctrl+t collapse")
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

// wrapPrefixed word-wraps body to the live width while reserving columns for a
// leading marker (e.g. "• "/"✗ ") that is CONTENT, not style frame: the marker
// sits on the first line and continuation lines hang-indent under the text so a
// wrapped multi-line notice/error reads as one bulleted item. width at or below
// the marker width means no wrap. Renders through st.
func (r *renderer) wrapPrefixed(prefix, body string, st lipgloss.Style) string {
	pw := lipgloss.Width(prefix)
	cw := r.contentWidth()
	if cw <= pw+1 {
		return st.Render(prefix + body)
	}
	// Normalise the body's emoji presentation before ansi.Wrap (see wrapStyled);
	// the marker prefix is a fixed literal, so its width is taken as-is. Wrap against
	// the content width so the bullet+body fits after the renderBlock indent.
	wrapped := ansi.Wrap(normalizeEmojiWidth(body), cw-pw, "")
	lines := strings.Split(wrapped, "\n")
	for i, ln := range lines {
		if i == 0 {
			lines[i] = prefix + ln
		} else {
			lines[i] = strings.Repeat(" ", pw) + ln
		}
	}
	return st.Render(strings.Join(lines, "\n"))
}

// plural formats a count with a noun, pluralising with a trailing "s" for any
// count other than 1 (e.g. 0 lines, 1 line, 3 lines).
func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return strconv.Itoa(n) + " " + noun + "s"
}

// renderHook renders a structured hook notice as a distinct one-liner: a hook
// glyph + the lifecycle phase (and the related tool, for per-tool phases) + the
// hook's message, with the OUTCOME driving colour and a leading severity glyph.
// A blocked hook (which can abort a run) renders in the error style with a "✗"
// so it is visually distinct from a benign informational/modified notice (a dim
// "•" hook glyph) — never indistinguishable from a compaction notice. All text
// is server-derived, so it is sanitized before reaching lipgloss.
func (r *renderer) renderHook(b *block) string {
	// Lead label: a phase tag, falling back to a generic "hook" when no phase.
	// Every server-derived field (phase, tool, message) is sanitized before it
	// reaches lipgloss — see the file's CWE-150 invariant.
	label := "hook"
	if b.hookPhase != "" {
		label = "hook " + sanitizeTerminal(b.hookPhase)
	}
	if b.hookTool != "" {
		label += " · " + sanitizeTerminal(b.hookTool)
	}

	switch b.hookDecision {
	case string(client.HookBlocked):
		// Blocked: error style + "✗", matching the error-block severity cue so an
		// aborting hook can't be mistaken for a benign notice. The decision VERB is
		// owned client-side ("blocked"), and the server Text rides as the trailing
		// reason only — a redundant leading phase/verb echo is stripped so the phase
		// appears exactly once (on the label).
		return r.wrapPrefixed("✗ ", label+": blocked"+hookReason(b.raw, b.hookPhase), r.th.Style("errorText"))
	case string(client.HookModified):
		// Modified: info-coloured "✎" — an action was rewritten, notable but benign.
		return r.wrapPrefixed("✎ ", label+": modified"+hookReason(b.raw, b.hookPhase), r.th.Style("hookModified"))
	case string(client.HookAdvisory):
		// Advisory: warning-coloured "⚠" — a guardrail flagged content but did not
		// alter the call/result (client-visible, model-invisible). Reads as a
		// warning notice, distinct from the benign info/modified and the error block.
		return r.wrapPrefixed("⚠ ", label+": advisory"+hookReason(b.raw, b.hookPhase), r.th.Style("hookAdvisory"))
	default:
		// Info (the baseline): dim "•" hook notice — the server Text is the body.
		line := label
		if b.raw != "" {
			line += ": " + sanitizeTerminal(b.raw)
		}
		return r.wrapPrefixed("• ", line, r.th.Style("muted"))
	}
}

// hookReason normalises a hook's server Text into a trailing " — <reason>" tail
// for the client-owned verb (blocked/modified), stripping a redundant leading
// phase/verb echo so the phase is never doubled. It drops boilerplate that adds
// nothing beyond the label+verb (e.g. "blocked by PreToolUse hook",
// "PreToolUse hook rewrote …") and otherwise appends the sanitized text as the
// reason. An empty/fully-redundant Text yields "" (label + verb stand alone).
func hookReason(raw, phase string) string {
	reason := strings.TrimSpace(stripPhaseEcho(raw, phase))
	if reason == "" {
		return ""
	}
	return " — " + sanitizeTerminal(reason)
}

// stripPhaseEcho removes a leading phase/verb echo from a hook message so the
// phase isn't repeated once on the label and again in the body. It folds away
// the loop's own boilerplate forms:
//
//	"blocked by <Phase> hook"            → ""        (pure echo)
//	"<Phase> hook rewrote tool arguments…" → "rewrote tool arguments…"
//	"<Phase> hook returned a malformed…"   → "returned a malformed…"
//
// A leading "<Phase> hook " or "<Phase> " prefix is trimmed; anything else is
// returned unchanged. Phase-free messages pass through verbatim.
func stripPhaseEcho(raw, phase string) string {
	s := strings.TrimSpace(raw)
	if phase == "" {
		return s
	}
	// The "blocked by <Phase> hook" form is a pure echo of label+verb → drop it.
	if strings.EqualFold(s, "blocked by "+phase+" hook") {
		return ""
	}
	// Trim a leading "<Phase> hook " or "<Phase> " prefix (case-insensitive).
	for _, prefix := range []string{phase + " hook ", phase + " "} {
		if len(s) >= len(prefix) && strings.EqualFold(s[:len(prefix)], prefix) {
			return strings.TrimSpace(s[len(prefix):])
		}
	}
	return s
}

// renderDelivery renders a fire-result delivery note block: a scheduled-task
// affordance (⏰) + the schedule name + the fire id + the outcome body. It is
// visually distinct from a user prompt (gold rail + "▌ you"), the model's text
// (● mecatl), and a muted notice (•). The schedule name + fire id sit on a
// leading label line (dim colour); the outcome body renders below it with a
// muted prefix, keeping the delivery card compact but recognisable.
//
// The recorded note is fenced-untrusted (renderFireDelivery) with a provenance
// header — both are MACHINE markers for the model, not content for the
// operator. The card already carries the provenance in its label, so the
// renderer strips the fence markers + the redundant header line and shows only
// the fire's outcome body. The operator-facing transcript and the model's
// history legitimately differ here: the model needs the fence (trust boundary),
// the operator needs the readable result.
func (r *renderer) renderDelivery(b *block) string {
	// Leading label: ⏰ scheduled task <name> — delivery · fire <id>
	label := "⏰ scheduled task " + sanitizeTerminal(b.toolName) + " — delivery"
	if b.deliveryFireID != "" {
		label += " · fire " + sanitizeTerminal(b.deliveryFireID)
	}
	header := r.wrapPrefixed("", label, r.th.Style("hookModified")) // model-adapted emerald, same as modified hook
	// Body: strip the fence markers + the redundant provenance header, keeping
	// only the fire's outcome text; the "│" prefix keeps it subordinate to the
	// header.
	body := r.wrapPrefixed("│ ", sanitizeTerminal(deliveryBodyForDisplay(b.raw)), r.th.Style("muted"))
	return header + "\n" + body
}

// deliveryBodyForDisplay strips the untrusted-fence markers and the
// "[scheduled task … completed with stop reason: …]" provenance header from a
// recorded delivery note, returning only the fire's outcome body for display.
// The fence + header are machine markers (the trust boundary the model reads);
// the delivery card already shows the provenance in its label, so echoing them
// in the body is noise. A note that does not match the fenced shape is returned
// VERBATIM (fail-soft — never drop content the transform can't prove is a
// delivery note).
func deliveryBodyForDisplay(raw string) string {
	const fence = "<<<UNTRUSTED"
	// Require the fence opener; a non-fenced note is not a delivery note we
	// recognise, so return it untouched.
	s, ok := strings.CutPrefix(raw, fence)
	if !ok {
		return raw
	}
	s = strings.TrimPrefix(s, "\n")
	// Strip the trailing fence closer (the last fence marker on its own line).
	if idx := strings.LastIndex(s, "\n"+fence); idx >= 0 {
		s = s[:idx]
	}
	// Strip the redundant provenance header line (the first line, which the
	// label already carries). Only strip when it IS the provenance header —
	// otherwise this is a fenced note with no header and the body is line 1.
	if nl := strings.IndexByte(s, '\n'); nl >= 0 && strings.HasPrefix(s, "[scheduled task ") {
		s = s[nl+1:]
	}
	return s
}

// renderTool renders a tool-call card: status glyph + name + body, and, once
// resolved, a truncated result body beneath it. For Edit/Write the args are
// shown as a colourised diff instead of raw JSON (falling back to pretty JSON if
// the args don't parse as the expected shape). expand removes the line cap on
// the result body and the diff.
func (r *renderer) renderTool(b *block, expand bool) string {
	var glyph string
	switch {
	case !b.resolved:
		glyph = r.th.Style("toolName").Render("…")
	case b.resultError:
		glyph = r.th.Style("toolErr").Render("✗")
	default:
		glyph = r.th.Style("toolOk").Render("✓")
	}

	// An MCP tool name (mcp__<server>__<tool>) renders a friendly "<Server> · <Tool>"
	// head instead of the raw, noisy identifier (issue #24); the raw name is never
	// lost — it reappears as a muted line when the card is expanded (ctrl+t), so the
	// exact tool is always recoverable. A non-MCP/core tool keeps its plain head.
	mcpName, isMCP := mcpTitle(b.toolName)
	headLabel := sanitizeTerminal(b.toolName)
	if isMCP {
		headLabel = mcpName
	}
	head := glyph + " " + r.th.Style("toolName").Render(headLabel)
	if isMCP && expand {
		head += "\n" + r.th.Style("muted").Render(sanitizeTerminal(b.toolName))
	}

	if args := r.renderToolArgs(b, expand); args != "" {
		head += "\n" + args
	}
	if b.resolved {
		if res := r.renderToolResult(b, expand); res != "" {
			head += "\n" + res
		}
	}

	card := r.th.Style("toolCard")
	if cw := r.contentWidth(); cw > 4 {
		// Lay the card out within the CONTENT width (viewport minus the left indent)
		// minus its own 2-cell border, capped at toolCardMaxWidth, so card + indent
		// never exceeds the viewport.
		card = card.Width(min(cw-2, toolCardMaxWidth))
	}
	return card.Render(head)
}

// renderToolArgs renders the ARGS region of a tool card (everything below the
// head, before the result): the redacted Team/Subagent lanes, the Edit/Write
// diff, or — for an ordinary tool — the compact key:value summary when collapsed
// and the full pretty JSON when expanded. Returns "" when there is nothing to
// show. See renderTool for the per-branch rationale.
func (r *renderer) renderToolArgs(b *block, expand bool) string {
	switch {
	case b.team:
		// A Team card renders its BOUNDED per-member lanes in place of raw JSON args:
		// a team header plus a live/expanded/resolved region. Member content is
		// server-bounded and never enters the parent conversation.
		return r.renderTeam(b, expand)
	case b.subagent:
		// A Subagent card renders its REDACTED child activity in place of raw JSON
		// args. The child's interior (args/results/message text) is isolated by design
		// and never shown — only metadata.
		return r.renderSubagent(b, expand)
	}
	if diff, ok := r.renderToolDiff(b.toolName, b.toolArgs, expand); ok {
		// Edit/Write render their change as a diff in place of the raw JSON args.
		return diff
	}
	if expand {
		// Expanded: always the FULL pretty-printed JSON (the inspect path; the summary
		// is collapsed-only, so ctrl+t reveals everything).
		if args := prettyJSON(b.toolArgs); args != "" {
			return r.th.Style("toolArgs").Render(args)
		}
		return ""
	}
	if summary, ok := r.summarizeArgs(b.toolArgs); ok {
		// Collapsed: the compact key:value summary in place of raw JSON (issue #24).
		return summary
	}
	// Collapsed but the args aren't a JSON object (bare array/scalar/odd shape):
	// fall back to the existing pretty-JSON behaviour.
	if args := prettyJSON(b.toolArgs); args != "" {
		return r.th.Style("toolArgs").Render(args)
	}
	return ""
}

// renderToolResult renders the RESULT region of a resolved tool card. Collapsed,
// a LARGE JSON result is summarized to prominent fields + a size line (issue #24,
// self-styled). An error result, a non-JSON/line-shaped result, or the expanded
// view fall through to the existing styled, line-capped (or full) body — Read
// results are unchanged. Returns "" when there is no body.
//
// Typed content blocks (b.resultBlocks) are rendered distinctly IN ADDITION to the
// model-facing text body when present: a resource link shows as "↗ <name> · <uri>"
// and an image as "[image: <mime>]" so a user-audience artifact is not buried
// in/below the text. Text/embedded-resource/structured-content blocks are already
// represented in the model-facing resultBody, so they are not double-rendered. A nil
// resultBlocks (the common text-only case) leaves the existing render path
// byte-unchanged.
func (r *renderer) renderToolResult(b *block, expand bool) string {
	body := r.renderToolResultBody(b, expand)
	artifacts := r.renderResultBlocks(b.resultBlocks)
	if artifacts == "" {
		return body
	}
	if body == "" {
		return artifacts
	}
	return body + "\n" + artifacts
}

// renderToolResultBody renders the model-facing text result body (the legacy path),
// unchanged: summarizeResolvedResult for a collapsed large JSON result, else the
// styled, line-capped/full body. Returns "" when there is no body.
func (r *renderer) renderToolResultBody(b *block, expand bool) string {
	if summary, ok := r.summarizeResolvedResult(b, expand); ok {
		return summary
	}
	body := resultBody(b.resultBody, expand)
	if body == "" {
		return ""
	}
	style := r.th.Style("toolArgs")
	if b.resultError {
		style = r.th.Style("errorText")
	}
	return style.Render(body)
}

// renderResultBlocks surfaces user-audience typed content blocks distinctly from the
// model-facing text body. Only resource-link and image blocks render here: text,
// embedded-resource, and structured-content blocks are already represented in the
// model-facing resultBody, so rendering them again would double up. Returns "" when
// there is nothing distinct to surface (no blocks, or only text-bearing blocks).
// All server-derived strings are terminal-sanitized before they reach lipgloss
// (CWE-150), the same guard the rest of the card uses.
func (r *renderer) renderResultBlocks(blocks []client.ContentBlock) string {
	if len(blocks) == 0 {
		return ""
	}
	muted := r.th.Style("muted")
	var out strings.Builder
	for _, blk := range blocks {
		line, ok := renderResultBlockLine(blk)
		if !ok {
			continue
		}
		if out.Len() > 0 {
			out.WriteString("\n")
		}
		out.WriteString(muted.Render(line))
	}
	return out.String()
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
		name := sanitizeTerminal(blk.Name)
		uri := sanitizeTerminal(blk.URL)
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
		mime := sanitizeTerminal(blk.MimeType)
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
func (r *renderer) renderSubagent(b *block, expand bool) string {
	muted := r.th.Style("muted")
	var out strings.Builder
	if b.subGoal != "" {
		out.WriteString(muted.Render("↳ " + sanitizeTerminal(b.subGoal)))
		out.WriteString("\n")
	}
	if routed := subagentModelLabel(b.subRoutedCategory, b.subRoutedModel, b.subModel); routed != "" {
		out.WriteString(muted.Render(routed))
		out.WriteString("\n")
	}

	if b.subDone {
		out.WriteString(muted.Render(subagentResolvedLine(b)))
		return strings.TrimRight(out.String(), "\n")
	}

	if expand {
		out.WriteString(muted.Render("subagent · " + boundedPreviewsSubNote))
		if trace := r.renderTrace(b.subTrace); trace != "" {
			out.WriteString("\n")
			out.WriteString(trace)
		}
		return strings.TrimRight(out.String(), "\n")
	}

	out.WriteString(muted.Render(subagentLiveLine(b)))
	return strings.TrimRight(out.String(), "\n")
}

// subagentModelLabel renders the model surface for a delegation as a muted one-line
// cue. It shows the OPT-IN router's bare metadata as "routed: <category> → <model>"
// when the router classified the delegation (ADR 0031); otherwise it shows the
// concrete model the child ACTUALLY ran on as "model: <model>" (issue #112 / ADR 0035)
// — inherited default, agent-def pin, or per-call override. It returns "" when no
// model is known and the router did not fire. The category/model are server-derived
// bare metadata (sanitized) — never child content — so gauntlet #7 holds. When routed,
// model == routedModel, so the routed cue is shown (not duplicated as a model: line).
func subagentModelLabel(category, routedModel, model string) string {
	category = sanitizeTerminal(category)
	routedModel = sanitizeTerminal(routedModel)
	model = sanitizeTerminal(model)
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
	// Plain case: show the concrete model the child ran on.
	if model != "" {
		return "model: " + model
	}
	return ""
}

// subagentLiveLine is the calm, monotonic collapsed status line: the child's live
// current-tool name (when one has run — "…" while it is still working), the token
// totals, a running tool count, and the ctrl+t trace affordance. No elapsed clock
// and no heartbeat ticker, so the line changes only when the tool actually changes
// (ADR 0079 AC3.1). The tool name is sanitized (server-derived).
func subagentLiveLine(b *block) string {
	current := "…"
	if b.subCurrent != "" {
		current = sanitizeTerminal(b.subCurrent)
	}
	return fmt.Sprintf("subagent · %s · ↑%s ↓%s · %s · ctrl+t trace",
		current,
		humanizeTokens(b.subUsage.InputTokens),
		humanizeTokens(b.subUsage.OutputTokens),
		plural(b.subToolCount, "tool"))
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

// chipContentWidth is the visible width available for the chip row inside the tool
// card, accounting for the card's border (2) and horizontal padding (2). It floors
// at a small positive value so a single chip per line is always attempted rather
// than degenerating when the width is unknown/tiny (r.width 0 → no wrap).
func (r *renderer) chipContentWidth() int {
	cw := r.contentWidth()
	if cw <= 4 {
		return 0 // width unknown/tiny: no wrapping (single row, as before)
	}
	w := cw - 2 - 4 // card.Width(contentWidth-2) minus border(2)+padding(2)
	if w < 1 {
		w = 1
	}
	return w
}

// wrapChips packs already-rendered chips into rows separated by chipSep, breaking
// to a new line BETWEEN chips when the next chip would overflow width (measured by
// visible width via lipgloss.Width, which ignores ANSI). A width <= 0 disables
// wrapping (all chips on one row). A chip wider than width still gets its own row
// rather than being split.
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
// future ctrl+a overlay can surface them all.
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
//   - RESOLVED (team ended): a muted stat line
//     "team · <rounds> rounds · ↑<in> ↓<out> · stop:<reason>". The Team tool's
//     joined summary renders below via the normal result body path.
//
// All member-derived text (names, message lines, tool names, previews) is
// terminal-sanitized before it reaches lipgloss.
func (r *renderer) renderTeam(b *block, expand bool) string {
	muted := r.th.Style("muted")
	var out strings.Builder

	if b.teamDone {
		out.WriteString(muted.Render(teamResolvedLine(b)))
		return out.String()
	}

	out.WriteString(muted.Render(teamHeader(b, expand)))

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
		out.WriteString(muted.Render(teamLaneLine(ln, nameW, false)))
		if expand {
			if tr := r.renderTrace(ln.trace); tr != "" {
				out.WriteString("\n")
				out.WriteString(tr)
			}
		}
	}
	if extra := len(order) - len(shown); extra > 0 {
		// The inline card caps at maxTeamLanes; the rest live in the ctrl+a overlay.
		// Advertise it on the roll-up so a capped card is the discovery point for the
		// full, windowed roster.
		out.WriteString("\n")
		out.WriteString(muted.Render(fmt.Sprintf("  · +%d more · ctrl+a", extra)))
	}
	return out.String()
}

// teamHeader is the muted lead line summarising the team's shape: the member count
// and the ctrl+t affordance, whose verb tracks the toggle (trace when collapsed,
// collapse when expanded). The round count is carried only on team.end, so it is
// shown on the resolved line rather than fabricated live.
func teamHeader(b *block, expand bool) string {
	verb := "ctrl+t trace"
	if expand {
		verb = "ctrl+t collapse"
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
		if n := len([]rune(truncate(sanitizeTerminal(lanes[idx].name), maxTeamNameWidth))); n > w {
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
	name := truncate(sanitizeTerminal(ln.name), maxTeamNameWidth)
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
		label = sanitizeTerminal(ln.current)
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
		b.WriteString("  " + wrapChips(chips, r.chipContentWidth()))
		chips = nil
	}
	writeLine := func(s string) {
		flush()
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString(s)
	}
	for i := range trace {
		t := &trace[i]
		switch t.kind {
		case teamTraceTool:
			glyph := okStyle.Render("✓")
			if t.isError {
				glyph = errStyle.Render("✗")
			}
			chip := glyph + " " + nameStyle.Render(sanitizeTerminal(t.name))
			if detail := sanitizeTerminal(oneLine(t.detail)); detail != "" {
				// A chip with a preview gets a dedicated line so its detail is readable.
				writeLine("  " + chip + muted.Render(" — "+truncate(detail, maxTraceDetailLen)))
			} else {
				chips = append(chips, chip)
			}
		case teamTraceMessage:
			writeLine("  " + muted.Render(truncate(sanitizeTerminal(oneLine(t.text)), maxTraceMessageLen)))
		}
	}
	flush()
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
		return sanitizeTerminal(stop)
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

// resultBody renders a tool result body: full when expanded, else line-capped.
func resultBody(body string, expand bool) string {
	if expand {
		return sanitizeTerminal(strings.TrimRight(body, "\n"))
	}
	return truncateLines(body, maxToolResultLines)
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
		b.WriteString(style.Render("  " + sanitizeTerminal(p)))
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
	switch name {
	case "Edit":
		return r.renderEditDiff(rawArgs, expand)
	case "Write":
		return r.renderWriteDiff(rawArgs, expand)
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
func (r *renderer) renderEditDiff(rawArgs string, expand bool) (string, bool) {
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
	b.WriteString(r.th.Style("diffMeta").Render(sanitizeTerminal(header)))
	b.WriteString("\n")
	b.WriteString(r.diffSide(args.OldString, "-", "diffRemove", expand))
	b.WriteString(r.diffSide(args.NewString, "+", "diffAdd", expand))
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
func (r *renderer) renderWriteDiff(rawArgs string, expand bool) (string, bool) {
	var args writeDiffArgs
	if err := json.Unmarshal([]byte(strings.TrimSpace(rawArgs)), &args); err != nil {
		return "", false
	}
	if args.Path == "" {
		return "", false
	}
	header := fmt.Sprintf("%s · %s (overwrites if it exists)", args.Path, plural(lineCount(args.Content), "line"))
	var b strings.Builder
	b.WriteString(r.th.Style("diffMeta").Render(sanitizeTerminal(header)))
	if args.Content != "" {
		b.WriteString("\n")
		b.WriteString(r.diffSide(args.Content, "+", "diffAdd", expand))
	}
	return strings.TrimRight(b.String(), "\n"), true
}

// diffSide renders one side of a diff (all-removed or all-added): every line of
// text gets the prefix and the themed style, line-capped unless expanded. An
// empty side renders nothing. The text is sanitized (these go through lipgloss).
func (r *renderer) diffSide(text, prefix, slot string, expand bool) string {
	text = sanitizeTerminal(strings.TrimRight(text, "\n"))
	if text == "" {
		return ""
	}
	lines := strings.Split(text, "\n")
	var marker string
	if !expand && len(lines) > maxDiffLines {
		extra := len(lines) - maxDiffLines
		lines = lines[:maxDiffLines]
		marker = collapseMarker(extra)
	}
	style := r.th.Style(slot)
	var b strings.Builder
	for _, ln := range lines {
		b.WriteString(style.Render(prefix + " " + ln))
		b.WriteString("\n")
	}
	if marker != "" {
		// The collapse marker is muted, not coloured as a diff line.
		b.WriteString(lipgloss.NewStyle().Render(marker))
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
		return sanitizeTerminal(raw)
	}
	return sanitizeTerminal(buf.String())
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
// passes through sanitizeTerminal (the summary is plain lipgloss, never glamour —
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
		b.WriteString(muted.Render(sanitizeTerminal(k) + ":"))
		b.WriteString(" ")
		b.WriteString(valStyle.Render(text))
	}
	// Advertise ctrl+t whenever ANYTHING was hidden: a key overflow OR a per-value
	// collapse (long string, big array/object). The marker mirrors collapseMarker's
	// shape so adjacent collapsed cards/results read consistently.
	if extra := len(keys) - len(shown); extra > 0 {
		b.WriteString("\n")
		b.WriteString(muted.Render(argRollupMarker(extra)))
	} else if valueCollapsed {
		b.WriteString("\n")
		b.WriteString(muted.Render(argRollupMarker(0)))
	}
	return b.String(), true
}

// argRollupMarker formats the collapsed-args affordance footer, matching
// collapseMarker's "  … <…> · ctrl+t expand" shape (leading "…", indented) so an
// arg roll-up and a line-capped result/diff don't show two different "there's
// more" idioms. n>0 names the hidden-key count ("+K more keys"); n==0 (a pure
// per-value collapse, no key overflow) shows just the expand hint.
func argRollupMarker(n int) string {
	if n <= 0 {
		return "  … ctrl+t expand"
	}
	noun := "keys"
	if n == 1 {
		noun = "key"
	}
	return "  … +" + strconv.Itoa(n) + " more " + noun + " · ctrl+t expand"
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
// All branches sanitizeTerminal their output, since the summary is plain
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
			return sanitizeTerminal(trimmed), false
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
			return sanitizeTerminal(trimmed), false
		}
		return plural(len(obj), "key"), true
	default:
		// number / bool / null — render the verbatim JSON token.
		return sanitizeTerminal(trimmed), false
	}
}

// summarizeStringValue renders a string arg value: inline-quoted when short and
// single-line, else a size + line-count + first-line preview. Sanitized. When the
// value spans more than one line the preview always carries a trailing "…" (even
// if the first line itself fit under the budget) to signal "more below".
func summarizeStringValue(s string) string {
	lines := lineCount(s)
	if lines <= 1 && len([]rune(s)) <= inlinePreviewLen {
		return sanitizeTerminal(strconv.Quote(s))
	}
	first := firstLine(s)
	preview := truncate(first, argPreviewLen)
	if lines > 1 && !strings.HasSuffix(preview, "…") {
		preview += "…"
	}
	preview = sanitizeTerminal(preview)
	return fmt.Sprintf("%s / %s · %q", humanizeBytes(len(s)), plural(lines, "line"), preview)
}

// summarizeArrayValue renders a JSON array value and reports whether it collapsed:
// "[a, b]" inline (collapsed=false) when it has at most maxInlineArray SCALAR
// elements (no nested array/object), else "N items" (collapsed=true). Sanitized.
func summarizeArrayValue(raw json.RawMessage) (string, bool) {
	var elems []json.RawMessage
	if err := json.Unmarshal(raw, &elems); err != nil {
		return sanitizeTerminal(strings.TrimSpace(string(raw))), false
	}
	if len(elems) == 0 {
		return "[]", false
	}
	if len(elems) <= maxInlineArray && allScalars(elems) {
		parts := make([]string, len(elems))
		for i, e := range elems {
			parts[i] = scalarText(e)
		}
		return sanitizeTerminal("[" + strings.Join(parts, ", ") + "]"), false
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
// "N.N KB"/"N.N MB" with one decimal (trailing ".0" trimmed). Sibling of the
// humanizeTokens/humanizeDuration formatters; used for the collapsed long-string
// arg row size signal. The math is SI/decimal (1 KB = 1000 B, 1 MB = 1e6 B) so a
// human-facing size reconciles with how file/content sizes are reported
// everywhere — the labels stay "KB"/"MB" (now honest, not mislabelled KiB/MiB).
func humanizeBytes(n int) string {
	if n < 0 {
		n = 0
	}
	switch {
	case n < 1000:
		return strconv.Itoa(n) + " B"
	case n < 1000*1000:
		return trimDecimal(float64(n)/1000.0) + " KB"
	default:
		return trimDecimal(float64(n)/(1000.0*1000.0)) + " MB"
	}
}

// parseMCPName splits an MCP tool name "mcp__<server>__<tool>" into its server
// and tool parts (the tool half may itself contain "__", so the split is on the
// FIRST "__" after the prefix). It returns ok=false for any non-MCP name, so a
// core tool (Read, Bash, …) keeps its plain head.
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
	return sanitizeTerminal(humanizeMCPServer(server) + " · " + humanizeMCPTool(tool)), true
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
func (r *renderer) summarizeResult(body string) (string, bool) {
	trimmed := strings.TrimSpace(body)
	if trimmed == "" {
		return "", false
	}
	large := lineCount(body) > maxToolResultLines || len(body) > resultSummaryByteThreshold
	if !large {
		return "", false
	}
	muted := r.th.Style("muted")
	valStyle := r.th.Style("toolArgs")
	switch trimmed[0] {
	case '{':
		var obj map[string]json.RawMessage
		if err := json.Unmarshal([]byte(trimmed), &obj); err != nil {
			return "", false
		}
		var b strings.Builder
		for _, k := range resultProminentKeys {
			raw, ok := obj[k]
			if !ok {
				continue
			}
			if b.Len() > 0 {
				b.WriteString("\n")
			}
			b.WriteString(muted.Render(sanitizeTerminal(k) + ":"))
			b.WriteString(" ")
			b.WriteString(valStyle.Render(summarizeValue(raw)))
		}
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString(muted.Render("· " + plural(len(obj), "key")))
		return b.String(), true
	case '[':
		var elems []json.RawMessage
		if err := json.Unmarshal([]byte(trimmed), &elems); err != nil {
			return "", false
		}
		return muted.Render("· " + plural(len(elems), "item")), true
	default:
		return "", false
	}
}

// summarizeResolvedResult is the renderTool gate around summarizeResult: it
// engages only for a COLLAPSED, non-error result, returning the self-styled
// compact summary when summarizeResult accepts the body (a large JSON
// object/array). The expanded view, an error result, and a non-JSON/line-shaped
// result all return ok=false so renderTool falls through to the existing styled,
// line-capped/full body path (Read and prose results unchanged).
func (r *renderer) summarizeResolvedResult(b *block, expand bool) (string, bool) {
	if expand || b.resultError {
		return "", false
	}
	return r.summarizeResult(b.resultBody)
}

// truncateLines clamps s to max lines, appending a "+N more lines · ctrl+t
// expand" affordance when it overflows. Used for tool results and diff sides,
// where ctrl+t is the way to see the rest.
func truncateLines(s string, maxLines int) string {
	return truncateLinesTail(s, maxLines, "")
}

// truncateLinesTail clamps s to maxLines lines, appending an overflow tail when
// it overflows. An empty tail uses the default "+N more lines · ctrl+t expand"
// collapse marker (the ctrl+t-referencing form for collapsible content); a
// non-empty tail is used verbatim instead — e.g. a neutral "…(truncated)" for
// already-expanded reasoning, which must NOT reference the toggle that revealed
// it. The (server-derived) body is terminal-sanitized.
func truncateLinesTail(s string, maxLines int, tail string) string {
	s = sanitizeTerminal(strings.TrimRight(s, "\n"))
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
		marker = collapseMarker(len(lines) - maxLines)
	}
	return strings.Join(kept, "\n") + "\n" + lipgloss.NewStyle().Render(marker)
}

// collapseMarker formats the "+N more line(s) · ctrl+t expand" affordance shown
// when a tool result or diff side is line-capped. The verb matches the footer
// help line's collapsed-state hint ("ctrl+t expand") — the expand/collapse pair
// is used consistently across help line, keybinding help, and this marker.
func collapseMarker(n int) string {
	noun := "lines"
	if n == 1 {
		noun = "line"
	}
	return "  … +" + strconv.Itoa(n) + " more " + noun + " · ctrl+t expand"
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
