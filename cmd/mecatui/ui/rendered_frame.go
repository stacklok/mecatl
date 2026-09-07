package ui

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// regionID is the closed semantic vocabulary for rows in a rendered conversation.
// It is deliberately independent of rendering style: a frame may reflow while the
// region identity and canonical visible-text offsets remain stable.
type regionID uint8

const (
	conversationRegionUnknown regionID = iota
	conversationRegionChrome
	conversationRegionBody
	conversationRegionReasoning
	conversationRegionArguments
	conversationRegionResult
	conversationRegionArtifact
	conversationRegionAppendix
)

// readingAnchor is the renderer-facing subset of ADR 0301's logical coordinate.
// Text rows use sourceOffset (a grapheme offset in ANSI-free visible text); chrome
// and other derived rows retain their local row fallback.
type readingAnchor struct {
	blockID      uint64
	region       regionID
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
	region       regionID
	sourceOffset int
	row          int
	text         bool
}

// renderedFrame keeps the renderer's existing lines and their lockstep row
// metadata. It is not a second transcript: lines share the strings already held
// by blockCache/joinPrefixLines and provenance is O(rows), not O(rendered bytes).
type renderedFrame struct {
	lines      []string
	provenance []renderedRow
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

func (f renderedFrame) hasRegion(blockID uint64, region regionID) bool {
	return f.firstRegionRow(blockID, region) >= 0
}

func (f renderedFrame) firstRegionRow(blockID uint64, region regionID) int {
	for i, row := range f.provenance {
		if row.blockID == blockID && row.region == region {
			return i
		}
	}
	return -1
}

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

// renderConversationFrame is the provenance-carrying form of the incremental
// lines path. renderConversationLines remains its byte-identical compatibility
// wrapper for the viewport.
func (r *renderer) renderConversationFrame(c *conversation, expand bool) renderedFrame {
	firstChanged := r.walkBlocks(c, expand)
	n := len(r.joinScratch)
	prefixN := min(firstChanged, n)
	wantKey := joinPrefixState{width: r.width, expand: expand}
	if r.joinPrefixKey != wantKey || r.joinPrefixN != prefixN {
		r.rebuildFramePrefix(c, prefixN, expand)
		r.joinPrefixN = prefixN
		r.joinPrefixKey = wantKey
	}

	frame := renderedFrame{
		lines: make([]string, 0, len(r.joinPrefixLines)+(n-prefixN)*2+1),
		// The viewport never retains provenance, so the renderer can reuse this
		// frame-local backing array after the caller projects the current frame.
		provenance: r.frameProvenanceScratch[:0],
	}
	if expand && len(c.filesChanged) > 0 {
		frame.appendixID = c.changedFilesAppendixID
	}
	frame.lines = append(frame.lines, r.joinPrefixLines...)
	frame.provenance = append(frame.provenance, r.joinPrefixProvenance...)
	for i := prefixN; i < n; i++ {
		r.appendFrameSegment(&frame, c, i, expand)
	}
	// The trailing split element is the existing terminal empty viewport line.
	frame.lines = append(frame.lines, "")
	frame.provenance = append(frame.provenance, renderedRow{region: conversationRegionChrome})
	r.frameProvenanceScratch = frame.provenance
	return frame
}

func (r *renderer) rebuildFramePrefix(c *conversation, prefixN int, expand bool) {
	r.joinPrefixLines = r.joinPrefixLines[:0]
	r.joinPrefixProvenance = r.joinPrefixProvenance[:0]
	for i := 0; i < prefixN; i++ {
		frame := renderedFrame{lines: r.joinPrefixLines, provenance: r.joinPrefixProvenance}
		r.appendFrameSegment(&frame, c, i, expand)
		r.joinPrefixLines, r.joinPrefixProvenance = frame.lines, frame.provenance
	}
}

func (r *renderer) appendFrameSegment(frame *renderedFrame, c *conversation, index int, expand bool) {
	if index > 0 {
		for n := 0; n < blockBlankLinesAfter(c.blocks, index); n++ {
			frame.lines = append(frame.lines, "")
			frame.provenance = append(frame.provenance, renderedRow{region: conversationRegionChrome})
		}
	}
	rows := r.blockFrameRows(index, &c.blocks[index], expand)
	for row, line := range strings.Split(r.joinScratch[index], "\n") {
		frame.lines = append(frame.lines, line)
		frame.provenance = append(frame.provenance, rows[row])
	}
}

func (r *renderer) blockFrameRows(index int, b *block, expand bool) []renderedRow {
	if entry, ok := r.blockFrameCache[index]; ok && entry.rev == b.rev && entry.width == r.width && entry.expand == expand {
		return entry.rows
	}
	rows := provenanceRows(b, r.joinScratch[index], expand)
	if r.blockFrameCache == nil {
		r.blockFrameCache = map[int]frameBlockEntry{}
	}
	r.blockFrameCache[index] = frameBlockEntry{rev: b.rev, width: r.width, expand: expand, rows: rows}
	return rows
}

func provenanceRows(b *block, rendered string, expand bool) []renderedRow {
	lines := strings.Split(rendered, "\n")
	rows := make([]renderedRow, len(lines))
	region := conversationRegionChrome
	textStart := 0
	switch b.kind {
	case blockUser:
		textStart, region = 1, conversationRegionBody
	case blockAssistant:
		textStart = 2 // label plus its intentional blank row
		if b.reasoning != "" {
			reasoningRows := len(strings.Split(renderedReasoningForFrame(b, expand), "\n"))
			for i := textStart; i < min(textStart+reasoningRows, len(rows)); i++ {
				rows[i] = renderedRow{blockID: b.id, region: conversationRegionReasoning, row: i - textStart}
			}
			textStart += reasoningRows
		}
		region = conversationRegionBody
	case blockTool:
		region = conversationRegionChrome
		// A tool card's header is chrome. Its remaining rows are split at the
		// first result-text row; this keeps the existing card renderer authoritative.
		textStart = 1
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
	if b.kind == blockTool {
		toolRegions(b, rows, lines)
	}
	if b.kind == blockUser {
		for i, line := range lines {
			if strings.Contains(ansi.Strip(line), "📎 ") {
				rows[i].region = conversationRegionArtifact
			}
		}
	}
	assignVisibleOffsets(rows, lines)
	return rows
}

func renderedReasoningForFrame(b *block, expand bool) string {
	if !expand {
		return "reasoning"
	}
	// Header, caveat, and the transformed visible body are all reasoning rows.
	return "reasoning\n" + reasoningCaveat + "\n" + sanitizeTerminal(strings.TrimRight(b.reasoning, "\n"))
}

func toolRegions(b *block, rows []renderedRow, lines []string) {
	if len(rows) == 0 {
		return
	}
	firstResult := len(rows)
	if b.resolved && b.resultBody != "" {
		firstResult = max(1, len(rows)/2)
	}
	for i := 1; i < len(rows); i++ {
		if i >= firstResult {
			rows[i].region = conversationRegionResult
		} else {
			rows[i].region = conversationRegionArguments
		}
		rows[i].blockID = b.id
		rows[i].row = i
	}
	rows[0] = renderedRow{blockID: b.id, region: conversationRegionChrome}
	for _, block := range b.resultBlocks {
		artifact, ok := renderResultBlockLine(block)
		if !ok {
			continue
		}
		for i, line := range lines {
			if strings.Contains(ansi.Strip(line), artifact) {
				rows[i].region = conversationRegionArtifact
			}
		}
	}
}

func assignVisibleOffsets(rows []renderedRow, lines []string) {
	offsets := map[regionID]int{}
	for i := range rows {
		if rows[i].blockID == 0 || rows[i].region == conversationRegionChrome || rows[i].region == conversationRegionAppendix {
			continue
		}
		rows[i].text = true
		rows[i].sourceOffset = offsets[rows[i].region]
		plain := strings.TrimRight(ansi.Strip(lines[i]), " ")
		offsets[rows[i].region] += graphemeCount(plain)
	}
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
