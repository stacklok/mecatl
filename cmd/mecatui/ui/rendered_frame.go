package ui

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// regionKind is the closed semantic vocabulary for rows in a rendered conversation.
// It is deliberately independent of rendering style: a frame may reflow while the
// region identity and canonical visible-text offsets remain stable.
type regionKind uint8

const (
	conversationRegionUnknown regionKind = iota
	conversationRegionChrome
	conversationRegionBody
	conversationRegionReasoning
	conversationRegionArguments
	conversationRegionResult
	conversationRegionAppendix
)

// readingAnchor is the renderer-facing subset of ADR 0301's logical coordinate.
// Text rows use sourceOffset (a grapheme offset in ANSI-free visible text); chrome
// and other derived rows retain their local row fallback.
type readingAnchor struct {
	blockID      uint64
	region       regionKind
	sourceOffset int
	row          int
	text         bool
	bias         edgeBias
}

// followMode is the sole auto-follow truth owned by conversationView.
type followMode uint8

const (
	followTail followMode = iota
	anchored
)

type edgeBias uint8

const (
	towardStart edgeBias = iota
	towardEnd
)

// renderedRow is provenance for one frame line. It intentionally has no text
// field: renderedFrame.lines owns the existing viewport strings.
type renderedRow struct {
	blockID      uint64
	region       regionKind
	sourceOffset int
	row          int
	text         bool
	kind         blockKind
	indent       int
	// Tool-card rows retain only their location in the shared prepared card. The
	// card itself is held by renderedFrame, so provenance never retains a second
	// canonical-text transcript.
	section    int
	sectionRow int
	leading    int
}

// renderedFrame keeps the renderer's existing lines and their lockstep row
// metadata. It is not a second transcript: lines share the strings already held
// by blockCache/joinPrefixLines and provenance is O(rows), not O(rendered bytes).
type renderedFrame struct {
	lines      []string
	provenance []renderedRow
	// toolCards pins each tool row's shared prepared card for this frame. Cache
	// entries may be replaced by a later render, but an already-visible frame must
	// continue to resolve its provenance against the card that produced its lines.
	toolCards map[uint64]*preparedToolCard
	// appendixID is present only while the expanded changed-files appendix is
	// eligible. It carries document identity without materialising another text copy.
	appendixID uint64
}

type frameBlockEntry struct {
	rev    int
	width  int
	expand bool
	rows   []renderedRow
}

func (f renderedFrame) hasRegion(blockID uint64, region regionKind) bool {
	return f.firstRegionRow(blockID, region) >= 0
}

func (f renderedFrame) firstRegionRow(blockID uint64, region regionKind) int {
	for i, row := range f.provenance {
		if row.blockID == blockID && row.region == region {
			return i
		}
	}
	return -1
}

// Phase 3 — anchor lookup/fallback: lookup starts from frame provenance;
// conversationView.restore owns the deterministic fallback policy.
func (f renderedFrame) anchorForRow(row int) (readingAnchor, bool) {
	if row < 0 || row >= len(f.provenance) {
		return readingAnchor{}, false
	}
	p := f.provenance[row]
	return readingAnchor{
		blockID: p.blockID, region: p.region, sourceOffset: p.sourceOffset,
		row: p.row, text: p.text,
	}, true
}

func (f renderedFrame) rowForAnchor(anchor readingAnchor) (int, bool) {
	best := -1
	for i, p := range f.provenance {
		if p.blockID != anchor.blockID || p.region != anchor.region {
			continue
		}
		if !anchor.text {
			if p.row == anchor.row {
				return i, true
			}
			if best < 0 {
				best = i
			}
			continue
		}
		if p.text && p.sourceOffset == anchor.sourceOffset {
			return i, true
		}
		if best < 0 || (p.text && p.sourceOffset < anchor.sourceOffset) {
			best = i
		}
	}
	return best, best >= 0
}

// Phase 1 — cache/render inputs: walk blocks once and pass its render-pass output
// explicitly to frame assembly. renderConversationLines remains the byte-identical
// viewport wrapper.
func (r *renderer) renderConversationFrame(c *conversation, expand bool) renderedFrame {
	renderedBlocks, firstChanged := r.walkBlocks(c, expand)
	n := len(renderedBlocks)
	prefixN := min(firstChanged, n)
	wantKey := joinPrefixState{width: r.width, expand: expand}
	if r.joinPrefixKey != wantKey || r.joinPrefixN != prefixN {
		r.rebuildFramePrefix(c, renderedBlocks, prefixN, expand)
		r.joinPrefixN = prefixN
		r.joinPrefixKey = wantKey
	}

	// Phase 2 — frame/provenance assembly: append cached prefix and changed suffix
	// into lockstep line and provenance slices.
	frame := renderedFrame{
		lines: make([]string, 0, len(r.joinPrefixLines)+(n-prefixN)*2+1),
		// The viewport never retains provenance, so the renderer can reuse this
		// frame-local backing array after the caller projects the current frame.
		provenance: r.frameProvenanceScratch[:0],
		toolCards:  make(map[uint64]*preparedToolCard),
	}
	if expand && len(c.filesChanged) > 0 {
		frame.appendixID = c.changedFilesAppendixID
	}
	// Pin the exact prepared cards whose structural row references are present in
	// this frame. A later cache miss may replace an entry while this frame is still
	// visible.
	for i := range c.blocks {
		b := &c.blocks[i]
		if b.kind != blockTool {
			continue
		}
		if entry, ok := r.blockCache[i]; ok && entry.rev == b.rev && entry.width == r.width && entry.expand == expand && entry.toolCard != nil {
			frame.toolCards[b.id] = entry.toolCard
		}
	}
	frame.lines = append(frame.lines, r.joinPrefixLines...)
	frame.provenance = append(frame.provenance, r.joinPrefixProvenance...)
	for i := prefixN; i < n; i++ {
		r.appendFrameSegment(&frame, c, renderedBlocks, i, expand)
	}
	// The trailing split element is the existing terminal empty viewport line.
	frame.lines = append(frame.lines, "")
	frame.provenance = append(frame.provenance, renderedRow{region: conversationRegionChrome})
	r.frameProvenanceScratch = frame.provenance
	return frame
}

func (r *renderer) rebuildFramePrefix(c *conversation, renderedBlocks []string, prefixN int, expand bool) {
	r.joinPrefixLines = r.joinPrefixLines[:0]
	r.joinPrefixProvenance = r.joinPrefixProvenance[:0]
	for i := 0; i < prefixN; i++ {
		frame := renderedFrame{lines: r.joinPrefixLines, provenance: r.joinPrefixProvenance}
		r.appendFrameSegment(&frame, c, renderedBlocks, i, expand)
		r.joinPrefixLines, r.joinPrefixProvenance = frame.lines, frame.provenance
	}
}

func (r *renderer) appendFrameSegment(frame *renderedFrame, c *conversation, renderedBlocks []string, index int, expand bool) {
	if index > 0 {
		for n := 0; n < blockBlankLinesAfter(c.blocks, index); n++ {
			frame.lines = append(frame.lines, "")
			frame.provenance = append(frame.provenance, renderedRow{region: conversationRegionChrome})
		}
	}
	rendered := renderedBlocks[index]
	rows := r.blockFrameRows(index, &c.blocks[index], rendered, expand)
	for row, line := range strings.Split(rendered, "\n") {
		frame.lines = append(frame.lines, line)
		frame.provenance = append(frame.provenance, rows[row])
	}
}

func (r *renderer) blockFrameRows(index int, b *block, rendered string, expand bool) []renderedRow {
	if entry, ok := r.blockFrameCache[index]; ok && entry.rev == b.rev && entry.width == r.width && entry.expand == expand {
		return entry.rows
	}
	var toolCard *preparedToolCard
	if b.kind == blockTool {
		// walkBlocks has just rendered this block, so the matching entry contains
		// the card that produced rendered. Keep the fallback for direct callers.
		if entry, ok := r.blockCache[index]; ok && entry.rev == b.rev && entry.width == r.width && entry.expand == expand {
			toolCard = entry.toolCard
		}
	}
	rows := r.provenanceRows(b, rendered, expand, toolCard)
	if r.blockFrameCache == nil {
		r.blockFrameCache = map[int]frameBlockEntry{}
	}
	r.blockFrameCache[index] = frameBlockEntry{rev: b.rev, width: r.width, expand: expand, rows: rows}
	return rows
}

func (r *renderer) provenanceRows(b *block, rendered string, expand bool, toolCard *preparedToolCard) []renderedRow {
	lines := strings.Split(rendered, "\n")
	if b.kind == blockTool {
		if toolCard == nil {
			prepared := r.prepareToolCard(b, expand)
			toolCard = &prepared
		}
		rows := toolCard.provenanceRows(b.id, r.indent, r.width)
		r.assignVisibleOffsets(b, rows, lines, toolCard)
		for i := range rows {
			rows[i].kind = b.kind
			rows[i].indent = r.indent
		}
		return rows
	}
	rows := make([]renderedRow, len(lines))
	region := conversationRegionChrome
	textStart := 0
	switch b.kind {
	case blockUser:
		textStart, region = 1, conversationRegionBody
	case blockAssistant:
		textStart = 2 // label plus its intentional blank row
		if b.reasoning != "" {
			reasoningRows := len(strings.Split(r.renderReasoning(b, expand), "\n"))
			for i := textStart; i < min(textStart+reasoningRows, len(rows)); i++ {
				rows[i] = renderedRow{blockID: b.id, region: conversationRegionReasoning, row: i - textStart}
			}
			textStart += reasoningRows
		}
		region = conversationRegionBody
	default:
		textStart, region = 0, conversationRegionBody
	}
	for i := range rows {
		if rows[i].region == conversationRegionReasoning {
			continue
		}
		rows[i] = renderedRow{blockID: b.id, region: conversationRegionChrome, row: i}
		if i >= textStart {
			rows[i].region = region
		}
	}
	r.assignVisibleOffsets(b, rows, lines, nil)
	for i := range rows {
		rows[i].kind = b.kind
		rows[i].indent = r.indent
	}
	return rows
}

// assignVisibleOffsets maps rows to canonical semantic text, not the final panel
// strings. Indentation, hanging assistant layout, and card framing are presentation
// only; counting them would make the same text acquire a different offset on reflow.
func (r *renderer) assignVisibleOffsets(b *block, rows []renderedRow, lines []string, toolCard *preparedToolCard) {
	offsets := map[regionKind]int{}
	for i := range rows {
		if rows[i].blockID == 0 || rows[i].region == conversationRegionChrome || rows[i].region == conversationRegionAppendix {
			continue
		}
		rows[i].text = true
		rows[i].sourceOffset = offsets[rows[i].region]
		plain := ansi.Strip(lines[i])
		if b.kind == blockTool {
			plain = toolCardRowText(toolCard, rows[i])
		} else {
			plain = canonicalRowText(b.kind, plain, r.indent)
		}
		offsets[rows[i].region] += graphemeCount(plain)
	}
}

func canonicalRowText(kind blockKind, line string, indent int) string {
	if indent > 0 {
		line = strings.TrimPrefix(line, strings.Repeat(" ", indent))
	}
	if kind == blockAssistant {
		line = strings.TrimPrefix(line, strings.Repeat(" ", assistantBodyHang))
	}
	return strings.TrimRight(line, " ")
}

// frameWithAppendix extends the render-owned frame only while the appendix is
// visible. Its rows identify the appendix without retaining another text copy.
func frameWithAppendix(frame renderedFrame, content string, appendixID uint64) renderedFrame {
	oldN := len(frame.lines)
	frame.lines = strings.Split(content, "\n")
	provenance := make([]renderedRow, len(frame.lines))
	copy(provenance, frame.provenance)
	offset := 0
	for i := oldN; i < len(provenance); i++ {
		provenance[i] = renderedRow{
			blockID: appendixID, region: conversationRegionAppendix,
			sourceOffset: offset, row: i - oldN, text: true,
		}
		plain := ansi.Strip(frame.lines[i])
		_, n := ansi.ByteToGraphemeRange(plain, 0, len(plain))
		offset += n
	}
	frame.provenance = provenance
	frame.appendixID = appendixID
	return frame
}
