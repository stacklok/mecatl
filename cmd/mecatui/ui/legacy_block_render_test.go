package ui

import (
	"charm.land/lipgloss/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/internal/terminaltext"
	"github.com/stacklok/mecatl/cmd/mecatui/ui/internal/blocks"
	"github.com/stacklok/mecatl/cmd/mecatui/ui/internal/scrollback"
)

// These adapters keep historical renderer unit fixtures independent while the
// production path consumes typed scrollback snapshots directly.
func (r *renderer) blockRenderKey(b *block, expanded bool) blockRenderKey {
	return blockRenderKey{revision: b.rev, context: r.renderContext(expanded)}
}

func (r *renderer) renderBlock(idx int, b *block, expand bool) string {
	return r.renderCachedSnapshot(idx, b.id, b.rev, expand, func(blockID uint64) blockRenderOutput {
		var (
			out      string
			prepared blocks.Prepared
		)
		switch b.kind {
		case blockTool:
			r.cardPrepares++
			prepared = r.prepareToolCard(b, expand).Prepared
		case blockUser:
			r.cardPrepares++
			prepared = r.prepareUserSnapshot(scrollback.UserCardSnapshot{Text: b.raw, Media: b.media})
		case blockNotice:
			r.cardPrepares++
			prepared = r.prepareNoticeSnapshot(scrollback.NoticeCardSnapshot{Text: b.raw, Recover: b.recover})
		case blockHook:
			r.cardPrepares++
			prepared = r.prepareHookSnapshot(scrollback.HookCardSnapshot{Text: b.raw, Phase: b.hookPhase, Tool: b.hookTool, Decision: b.hookDecision})
		case blockTurnStat:
			r.cardPrepares++
			prepared = r.prepareTurnStatSnapshot(scrollback.TurnStatCardSnapshot{Text: b.raw})
		case blockError:
			r.cardPrepares++
			prepared = r.prepareErrorSnapshot(scrollback.ErrorCardSnapshot{Text: b.raw, Permanent: b.permanent}, expand)
		case blockDelivery:
			r.cardPrepares++
			prepared = r.prepareDeliverySnapshot(scrollback.DeliveryCardSnapshot{ScheduleName: b.toolName, FireID: b.deliveryFireID, Text: b.raw})
		default:
			out = r.renderBlockFresh(idx, b, expand)
		}
		var rows []renderedRow
		if len(prepared.Rows) > 0 {
			out = prepared.Text()
			rows = blockProvenanceRows(prepared, blockID, scrollbackKindFromBlock(b.kind), r.indent, r.width)
		}
		if b.kind != blockTool || r.width > r.indent {
			out = r.indentLines(out)
		}
		if len(rows) == 0 {
			rows = r.provenanceRows(b, out, expand)
		}
		return blockRenderOutput{text: out, rows: rows}
	})
}

// renderBlockFresh renders one block per its kind. Assistant text goes through
// glamour; everything else is plain themed lipgloss. idx is the block's stable
// conversation index, used to memoize the (expensive) assistant glamour render
// across the per-delta full-scrollback re-render — see markdownAt (the inner
// memo layer below renderBlock's whole-block cache).
func (r *renderer) renderBlockFresh(idx int, b *block, expand bool) string {
	switch b.kind {
	case blockUser:
		return r.prepareUserSnapshot(scrollback.UserCardSnapshot{Text: b.raw, Media: b.media}).Text()
	case blockAssistant:
		return r.renderAssistantSnapshotFresh(idx, scrollback.AssistantCardSnapshot{Text: b.raw, Reasoning: b.reasoning, ReasoningStreaming: b.reasoningStreaming}, expand)
	case blockTool:
		return r.renderTool(b, expand)
	case blockNotice:
		return r.prepareNoticeSnapshot(scrollback.NoticeCardSnapshot{Text: b.raw, Recover: b.recover}).Text()
	case blockHook:
		return r.prepareHookSnapshot(scrollback.HookCardSnapshot{Text: b.raw, Phase: b.hookPhase, Tool: b.hookTool, Decision: b.hookDecision}).Text()
	case blockTurnStat:
		return r.prepareTurnStatSnapshot(scrollback.TurnStatCardSnapshot{Text: b.raw}).Text()
	case blockError:
		if b.permanent {
			return r.prepareErrorSnapshot(scrollback.ErrorCardSnapshot{Text: b.raw, Permanent: true}, expand).Text()
		}
		return r.prepareErrorSnapshot(scrollback.ErrorCardSnapshot{Text: b.raw}, expand).Text()
	case blockDelivery:
		return r.prepareDeliverySnapshot(scrollback.DeliveryCardSnapshot{ScheduleName: b.toolName, FireID: b.deliveryFireID, Text: b.raw}).Text()
	default:
		return r.wrapStyled(terminaltext.Sanitize(b.raw), lipgloss.NewStyle())
	}
}

func (r *renderer) renderReasoning(b *block, expand bool) string {
	return r.renderReasoningSnapshot(scrollback.AssistantCardSnapshot{Reasoning: b.reasoning, ReasoningStreaming: b.reasoningStreaming}, expand)
}

func scrollbackKindFromBlock(kind blockKind) scrollback.Kind {
	switch kind {
	case blockUser:
		return scrollback.KindUser
	case blockAssistant:
		return scrollback.KindAssistant
	case blockNotice:
		return scrollback.KindNotice
	case blockTurnStat:
		return scrollback.KindTurnStat
	case blockError:
		return scrollback.KindError
	case blockHook:
		return scrollback.KindHook
	case blockDelivery:
		return scrollback.KindDelivery
	default:
		return scrollback.KindTool
	}
}

func (r *renderer) provenanceRows(b *block, rendered string, expand bool) []renderedRow {
	kind := scrollbackKindFromBlock(b.kind)
	return r.snapshotProvenanceRows(b.id, kind, scrollback.AssistantCardSnapshot{
		Reasoning: b.reasoning, ReasoningStreaming: b.reasoningStreaming,
	}, rendered, expand)
}
