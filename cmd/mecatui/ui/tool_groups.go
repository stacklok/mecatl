package ui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/x/ansi"

	"github.com/stacklok/mecatl/cmd/mecatui/theme"
	"github.com/stacklok/mecatl/cmd/mecatui/ui/internal/scrollback"
)

func toolFamily(kind scrollback.Kind) bool {
	return kind == scrollback.KindTool || kind == scrollback.KindSubagent || kind == scrollback.KindTeam
}

// assembleToolGroups changes only display passes, never the conversation or card
// caches. Prefix coverage remains in source indices, always outside a whole group.
func (r *renderer) assembleToolGroups(passes []renderPass, firstChanged int, expand bool) (int, map[uint64]uint64) {
	var summaries map[uint64]uint64
	for start := 0; start < len(passes); {
		if passes[start].text == "" || !toolFamily(passes[start].kind) {
			start++
			continue
		}
		end := start + 1
		for end < len(passes) && (passes[end].text == "" || toolFamily(passes[end].kind)) {
			end++
		}
		// Appending or revealing a boundary can also change the preceding group.
		if firstChanged < len(passes) && firstChanged <= end {
			firstChanged = min(firstChanged, start)
		}
		if !expand {
			settled, failed := 0, 0
			var summaryID uint64
			var active []renderPass
			for i := start; i < end; i++ {
				pass := passes[i]
				if pass.text == "" {
					continue
				}
				if (toolcallProjection{state: pass.toolState}).settled() {
					settled++
					if pass.toolState == toolcallFailed {
						failed++
					}
					if summaryID == 0 {
						summaryID = pass.id
					}
				} else {
					active = append(active, pass)
				}
			}
			if settled > 0 {
				if summaries == nil {
					summaries = make(map[uint64]uint64)
				}
				for i := start; i < end; i++ {
					pass := passes[i]
					if pass.text != "" && (toolcallProjection{state: pass.toolState}).settled() {
						summaries[pass.id] = summaryID
					}
					passes[i] = renderPass{}
				}
				output := r.renderToolAggregate(summaryID, settled, failed)
				passes[start] = renderPass{id: summaryID, kind: scrollback.KindTool, text: output.text, rows: output.rows}
				copy(passes[start+1:end], active)
			}
		}
		start = end
	}
	return firstChanged, summaries
}

func (r *renderer) renderToolAggregate(blockID uint64, count, failed int) blockRenderOutput {
	noun := "tools"
	if count == 1 {
		noun = "tool"
	}
	out := r.th.Style("transcriptToolMarker").Render(theme.TranscriptToolGlyph) + r.th.Style("transcriptToolHeading").Render(fmt.Sprintf(" %d %s run", count, noun))
	if failed > 0 {
		out += r.th.Style("toolErr").Render(fmt.Sprintf(" · %d failed", failed))
	}
	out += r.th.Style("muted").Render(" · " + r.marks.expandConversation + " detailed · " + r.marks.toolcalls + " Toolcalls")
	out = ansi.Truncate(out, r.contentWidth(), "…")
	indent := 0
	if r.width > r.indent {
		indent = r.indent
	}
	out = padLines(out, indent)
	return blockRenderOutput{
		text: out,
		rows: []renderedRow{{
			blockID: blockID, region: conversationRegionToolSummary, text: true,
			kind: scrollback.KindTool, indent: indent, leading: indent,
			span: graphemeCount(ansi.Strip(strings.TrimPrefix(out, strings.Repeat(" ", indent)))),
		}},
	}
}
