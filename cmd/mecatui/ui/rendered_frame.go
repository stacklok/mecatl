package ui

import (
	"strings"

	"github.com/charmbracelet/x/ansi"

	"github.com/stacklok/mecatl/cmd/mecatui/ui/internal/scrollback"
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
	// Functional-card rows retain only their structural location in the rendered
	// line: canonical source offset, leading semantic grapheme, and visible span.
	leading int
	span    int
	// separator identifies a derived blank line between conversation blocks. It has
	// no logical position of its own; observing it anchors to an adjacent block.
	separator bool
}

// renderedFrame keeps the renderer's existing lines and their lockstep row
// metadata. It is not a second transcript: lines share the strings already held
// by blockRenderCache's rendered entries and prefix lines; provenance is O(rows), not O(rendered bytes).
type renderedFrame struct {
	lines      []string
	provenance []renderedRow
	// Functional-card provenance is structural only; prepared semantic inputs are
	// discarded at the cache boundary.
	appendixID uint64
}

type frameBlockEntry struct {
	key  blockRenderKey
	rows []renderedRow
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

// observedAnchorForRow translates a derived inter-block separator to its adjacent
// logical content. The terminal blank row remains its own ordinary chrome anchor.
func (f renderedFrame) observedAnchorForRow(row int, bias edgeBias) (readingAnchor, bool) {
	if row < 0 || row >= len(f.provenance) {
		return readingAnchor{}, false
	}
	if !f.provenance[row].separator {
		return f.anchorForRow(row)
	}
	start, end, step := row-1, -1, -1
	if bias == towardEnd {
		start, end, step = row+1, len(f.provenance), 1
	}
	for i := start; i != end; i += step {
		if f.provenance[i].blockID != 0 {
			return f.anchorForRow(i)
		}
	}
	return readingAnchor{}, false
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
func (r *renderer) renderConversationFrame(c *scrollback.Conversation, expand bool) renderedFrame {
	renderedBlocks, metadata, prepared, firstChanged := r.walkConversation(c, expand)
	n := len(renderedBlocks)
	prefixN := min(firstChanged, n)
	key := joinPrefixState{width: r.width, expand: expand}
	prefixLines, prefixProvenance, ok := r.blocks.prefix(key, prefixN)
	if !ok {
		prefixLines, prefixProvenance = r.rebuildFramePrefix(c, metadata, prepared, renderedBlocks, prefixN, expand)
		r.blocks.replacePrefix(prefixLines, prefixProvenance, prefixN, key)
	}

	frame := renderedFrame{
		lines:      make([]string, 0, len(prefixLines)+(n-prefixN)*2+1),
		provenance: r.frameProvenanceScratch[:0],
	}
	if expand {
		if appendix, ok := c.AppendixSnapshot(); ok && len(appendix.Files) > 0 {
			frame.appendixID = uint64(appendix.ID)
		}
	}
	frame.lines = append(frame.lines, prefixLines...)
	frame.provenance = append(frame.provenance, prefixProvenance...)
	for i := prefixN; i < n; i++ {
		r.appendFrameSegment(&frame, c, metadata, prepared, renderedBlocks, i, expand)
	}
	frame.lines = append(frame.lines, "")
	frame.provenance = append(frame.provenance, renderedRow{region: conversationRegionChrome})
	r.frameProvenanceScratch = frame.provenance
	return frame
}

func (r *renderer) rebuildFramePrefix(c *scrollback.Conversation, metadata []scrollback.BlockMetadata, prepared []*block, renderedBlocks []string, prefixN int, expand bool) ([]string, []renderedRow) {
	lines := make([]string, 0, prefixN*2)
	provenance := make([]renderedRow, 0, prefixN*2)
	for i := 0; i < prefixN; i++ {
		frame := renderedFrame{lines: lines, provenance: provenance}
		r.appendFrameSegment(&frame, c, metadata, prepared, renderedBlocks, i, expand)
		lines, provenance = frame.lines, frame.provenance
	}
	return lines, provenance
}

func (r *renderer) appendFrameSegment(frame *renderedFrame, c *scrollback.Conversation, metadata []scrollback.BlockMetadata, prepared []*block, renderedBlocks []string, index int, expand bool) {
	if index > 0 {
		for n := 0; n < blockBlankLinesAfterMetadata(metadata, index); n++ {
			frame.lines = append(frame.lines, "")
			frame.provenance = append(frame.provenance, renderedRow{region: conversationRegionChrome, separator: true})
		}
	}
	rendered := renderedBlocks[index]
	rows := r.blockFrameRowsTyped(c, index, metadata[index], prepared[index], rendered, expand)
	for row, line := range strings.Split(rendered, "\n") {
		frame.lines = append(frame.lines, line)
		frame.provenance = append(frame.provenance, rows[row])
	}
}

func (r *renderer) blockFrameRowsTyped(c *scrollback.Conversation, index int, meta scrollback.BlockMetadata, prepared *block, rendered string, expand bool) []renderedRow {
	key := blockRenderKey{revision: rendererRevision(meta.Revision), context: r.renderContext(expand)}
	if rows, ok := r.blocks.frameRowsFor(index, key); ok {
		return rows
	}
	if entry, ok := r.blocks.renderedBlock(index, key); ok && len(entry.rows) > 0 {
		return entry.rows
	}
	if prepared == nil {
		var ok bool
		r.snapshotLoads++
		b, ok := blockFromSnapshot(c.SnapshotAt(index))
		if !ok {
			return nil
		}
		prepared = &b
	}
	rows := r.provenanceRows(prepared, rendered, expand)
	r.blocks.storeFrameRows(index, frameBlockEntry{key: key, rows: rows})
	return rows
}

func blockKindFromMetadata(kind scrollback.Kind) blockKind {
	switch kind {
	case scrollback.KindUser:
		return blockUser
	case scrollback.KindAssistant:
		return blockAssistant
	case scrollback.KindNotice:
		return blockNotice
	case scrollback.KindTurnStat:
		return blockTurnStat
	case scrollback.KindError:
		return blockError
	case scrollback.KindHook:
		return blockHook
	case scrollback.KindDelivery:
		return blockDelivery
	default:
		return blockTool
	}
}

func blockBlankLinesAfterMetadata(metadata []scrollback.BlockMetadata, i int) int {
	previous := blockKindFromMetadata(metadata[i-1].Kind)
	current := blockKindFromMetadata(metadata[i].Kind)
	switch previous {
	case blockTool:
		return interBlockBlankLinesNone
	case blockTurnStat:
		return interBlockBlankLinesCompact
	case blockAssistant:
		if current == blockTurnStat {
			return interBlockBlankLinesNone
		}
	}
	return interBlockBlankLinesCompact
}

func (r *renderer) provenanceRows(b *block, rendered string, expand bool) []renderedRow {
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
			reasoning := r.renderReasoning(b, expand)
			reasoningRows := len(strings.Split(reasoning, "\n"))
			for i := textStart; i < min(textStart+reasoningRows, len(rows)); i++ {
				rows[i] = renderedRow{blockID: b.id, region: conversationRegionReasoning, row: i - textStart}
			}
			if expand {
				// The header and caveat describe the presentation. Only the
				// expanded reasoning body is semantic text that can survive a
				// reflow by its grapheme offset.
				caveatRows := len(strings.Split(r.wrapStyled(reasoningCaveat, r.th.Style("reasoning")), "\n"))
				for i := textStart + 1 + caveatRows; i < min(textStart+reasoningRows, len(rows)); i++ {
					rows[i].text = true
				}
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
	r.assignVisibleOffsets(b, rows, lines)
	for i := range rows {
		rows[i].kind = b.kind
		rows[i].indent = r.indent
	}
	return rows
}

// assignVisibleOffsets maps rows to canonical semantic text, not the final panel
// strings. Indentation, hanging assistant layout, and card framing are presentation
// only; counting them would make the same text acquire a different offset on reflow.
func (r *renderer) assignVisibleOffsets(b *block, rows []renderedRow, lines []string) {
	offsets := map[regionKind]int{}
	for i := range rows {
		if rows[i].blockID == 0 || rows[i].region == conversationRegionChrome || rows[i].region == conversationRegionAppendix ||
			(rows[i].region == conversationRegionReasoning && !rows[i].text) {
			continue
		}
		rows[i].text = true
		rows[i].sourceOffset = offsets[rows[i].region]
		plain := canonicalRowText(b.kind, ansi.Strip(lines[i]), r.indent)
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
