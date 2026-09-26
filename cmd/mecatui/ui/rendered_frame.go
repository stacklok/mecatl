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
	kind         scrollback.Kind
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
	passes, firstChanged := r.renderPasses(c, expand)
	n := len(passes)
	prefixN := min(firstChanged, n)
	key := joinPrefixState{width: r.width, expand: expand}
	prefixLines, prefixProvenance, ok := r.blocks.prefix(key, prefixN)
	if !ok {
		prefixLines, prefixProvenance = r.rebuildFramePrefix(passes, prefixN)
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
		r.appendFrameSegment(&frame, passes, i)
	}
	frame.lines = append(frame.lines, "")
	frame.provenance = append(frame.provenance, renderedRow{region: conversationRegionChrome})
	r.frameProvenanceScratch = frame.provenance
	return frame
}

func (r *renderer) rebuildFramePrefix(passes []renderPass, prefixN int) ([]string, []renderedRow) {
	lines := make([]string, 0, prefixN*2)
	provenance := make([]renderedRow, 0, prefixN*2)
	for i := 0; i < prefixN; i++ {
		frame := renderedFrame{lines: lines, provenance: provenance}
		r.appendFrameSegment(&frame, passes, i)
		lines, provenance = frame.lines, frame.provenance
	}
	return lines, provenance
}

func (*renderer) appendFrameSegment(frame *renderedFrame, passes []renderPass, index int) {
	if index > 0 {
		for n := 0; n < blockBlankLinesAfterPasses(passes, index); n++ {
			frame.lines = append(frame.lines, "")
			frame.provenance = append(frame.provenance, renderedRow{region: conversationRegionChrome, separator: true})
		}
	}
	pass := passes[index]
	for row, line := range strings.Split(pass.text, "\n") {
		frame.lines = append(frame.lines, line)
		frame.provenance = append(frame.provenance, pass.rows[row])
	}
}

func blockBlankLinesAfterPasses(passes []renderPass, i int) int {
	previous := passes[i-1].kind
	current := passes[i].kind
	switch previous {
	case scrollback.KindTool, scrollback.KindSubagent, scrollback.KindTeam:
		return interBlockBlankLinesNone
	case scrollback.KindTurnStat:
		return interBlockBlankLinesCompact
	case scrollback.KindAssistant:
		if current == scrollback.KindTurnStat {
			return interBlockBlankLinesNone
		}
	}
	return interBlockBlankLinesCompact
}

func (r *renderer) assistantProvenanceRows(blockID uint64, p scrollback.AssistantCardSnapshot, rendered string, expand bool) []renderedRow {
	return r.snapshotProvenanceRows(blockID, scrollback.KindAssistant, p, rendered, expand)
}

func (r *renderer) snapshotProvenanceRows(blockID uint64, kind scrollback.Kind, assistant scrollback.AssistantCardSnapshot, rendered string, expand bool) []renderedRow {
	lines := strings.Split(rendered, "\n")
	rows := make([]renderedRow, len(lines))
	region := conversationRegionChrome
	textStart := 0
	switch kind {
	case scrollback.KindUser:
		textStart, region = 1, conversationRegionBody
	case scrollback.KindAssistant:
		textStart = 2 // label plus its intentional blank row
		if assistant.Reasoning != "" {
			reasoning := r.renderReasoningSnapshot(assistant, expand)
			reasoningRows := len(strings.Split(reasoning, "\n"))
			for i := textStart; i < min(textStart+reasoningRows, len(rows)); i++ {
				rows[i] = renderedRow{blockID: blockID, region: conversationRegionReasoning, row: i - textStart}
			}
			if expand {
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
		rows[i] = renderedRow{blockID: blockID, region: conversationRegionChrome, row: i}
		if i >= textStart {
			rows[i].region = region
		}
	}
	r.assignVisibleOffsets(kind, rows, lines)
	for i := range rows {
		rows[i].kind = kind
		rows[i].indent = r.indent
	}
	return rows
}

// assignVisibleOffsets maps rows to canonical semantic text, not the final panel
// strings. Indentation, hanging assistant layout, and card framing are presentation
// only; counting them would make the same text acquire a different offset on reflow.
func (r *renderer) assignVisibleOffsets(kind scrollback.Kind, rows []renderedRow, lines []string) {
	offsets := map[regionKind]int{}
	for i := range rows {
		if rows[i].blockID == 0 || rows[i].region == conversationRegionChrome || rows[i].region == conversationRegionAppendix ||
			(rows[i].region == conversationRegionReasoning && !rows[i].text) {
			continue
		}
		rows[i].text = true
		rows[i].sourceOffset = offsets[rows[i].region]
		plain := canonicalRowText(kind, ansi.Strip(lines[i]), r.indent)
		offsets[rows[i].region] += graphemeCount(plain)
	}
}

func canonicalRowText(kind scrollback.Kind, line string, indent int) string {
	if indent > 0 {
		line = strings.TrimPrefix(line, strings.Repeat(" ", indent))
	}
	if kind == scrollback.KindAssistant {
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
