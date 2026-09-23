package ui

import "github.com/stacklok/mecatl/cmd/mecatui/ui/internal/blocks"

func (r *renderer) plainBlockLayout(expand bool) blocks.PlainLayout {
	return blocks.SnapshotPlainLayout(blocks.PlainLayoutInput{
		Width: r.contentWidth(), Expanded: expand, ExpandMark: r.marks.expandTools,
	})
}

// blockTheme maps UI stylesheet roles to the concrete semantic styles used by
// structured block preparation.
func (r *renderer) blockTheme() blocks.Theme {
	toolCard, _, _ := r.toolCardLayout()
	return blocks.Theme{
		UserLabel:    r.th.Style("userLabel"),
		UserBody:     r.th.Style("userBlock"),
		Muted:        r.th.Style("muted"),
		Warning:      r.th.Style("warning"),
		Error:        r.th.Style("errorText"),
		HookModified: r.th.Style("hookModified"),
		HookAdvisory: r.th.Style("hookAdvisory"),
		DeliveryHead: r.th.Style("hookModified"),
		ToolCard:     toolCard,
	}
}

func userInputFromBlock(b *block) blocks.UserInput {
	return blocks.UserInput{Text: b.raw, Media: b.media}
}

func noticeInputFromBlock(b *block) blocks.NoticeInput {
	return blocks.NoticeInput{Text: b.raw, Recovery: b.recover}
}

func hookInputFromBlock(b *block) blocks.HookInput {
	return blocks.HookInput{Text: b.raw, Phase: b.hookPhase, Tool: b.hookTool, Decision: b.hookDecision}
}

func turnStatInputFromBlock(b *block) blocks.TurnStatInput { return blocks.TurnStatInput{Text: b.raw} }

func errorInputFromBlock(b *block) blocks.ErrorInput { return blocks.ErrorInput{Text: b.raw} }

func permanentErrorInputFromBlock(b *block) blocks.PermanentErrorInput {
	return blocks.PermanentErrorInput{Text: b.raw}
}

func deliveryInputFromBlock(b *block) blocks.DeliveryInput {
	return blocks.DeliveryInput{ScheduleName: b.toolName, FireID: b.deliveryFireID, Text: b.raw}
}

// prepareStructuredBlock snapshots and prepares every migrated non-Markdown block
// family. It is called only after renderBlock's cheap revision/layout admission
// guard misses; settled cache hits therefore do no snapshot or preparation.
func (r *renderer) prepareStructuredBlock(b *block, expand bool) (blocks.Prepared, bool) {
	switch b.kind {
	case blockTool, blockUser, blockNotice, blockHook, blockTurnStat, blockError, blockDelivery:
		r.cardPrepares++
	default:
		return blocks.Prepared{}, false
	}
	switch b.kind {
	case blockTool:
		return r.prepareToolCard(b, expand).Prepared, true
	case blockUser:
		return r.prepareUserBlock(b), true
	case blockNotice:
		return r.prepareNoticeBlock(b), true
	case blockHook:
		return r.prepareHookBlock(b), true
	case blockTurnStat:
		return r.prepareTurnStatBlock(b), true
	case blockError:
		if b.permanent {
			return r.preparePermanentErrorBlock(b, expand), true
		}
		return r.prepareErrorBlock(b), true
	case blockDelivery:
		return r.prepareDeliveryBlock(b), true
	default:
		return blocks.Prepared{}, false
	}
}

func (r *renderer) prepareUserBlock(b *block) blocks.Prepared {
	return blocks.PrepareUser(blocks.SnapshotUser(userInputFromBlock(b)), r.plainBlockLayout(false), r.blockTheme())
}

func (r *renderer) prepareNoticeBlock(b *block) blocks.Prepared {
	return blocks.PrepareNotice(blocks.SnapshotNotice(noticeInputFromBlock(b)), r.plainBlockLayout(false), r.blockTheme())
}

func (r *renderer) prepareHookBlock(b *block) blocks.Prepared {
	return blocks.PrepareHook(blocks.SnapshotHook(hookInputFromBlock(b)), r.plainBlockLayout(false), r.blockTheme())
}

func (r *renderer) prepareTurnStatBlock(b *block) blocks.Prepared {
	return blocks.PrepareTurnStat(blocks.SnapshotTurnStat(turnStatInputFromBlock(b)), r.plainBlockLayout(false), r.blockTheme())
}

func (r *renderer) prepareErrorBlock(b *block) blocks.Prepared {
	return blocks.PrepareError(blocks.SnapshotError(errorInputFromBlock(b)), r.plainBlockLayout(false), r.blockTheme())
}

func (r *renderer) preparePermanentErrorBlock(b *block, expand bool) blocks.Prepared {
	return blocks.PreparePermanentError(blocks.SnapshotPermanentError(permanentErrorInputFromBlock(b)), r.plainBlockLayout(expand), r.blockTheme())
}

func (r *renderer) prepareDeliveryBlock(b *block) blocks.Prepared {
	return blocks.PrepareDelivery(blocks.SnapshotDelivery(deliveryInputFromBlock(b)), r.plainBlockLayout(false), r.blockTheme())
}
