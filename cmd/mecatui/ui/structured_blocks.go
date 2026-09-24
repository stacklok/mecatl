package ui

import (
	"github.com/stacklok/mecatl/cmd/mecatui/ui/internal/blocks"
	"github.com/stacklok/mecatl/cmd/mecatui/ui/internal/scrollback"
)

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

func (r *renderer) renderPreparedSnapshot(index int, id scrollback.BlockID, revision uint64, kind scrollback.Kind, expand bool, prepare func() blocks.Prepared) string {
	return r.renderCachedSnapshot(index, uint64(id), rendererRevision(revision), expand, func(blockID uint64) blockRenderOutput {
		r.cardPrepares++
		prepared := prepare()
		return blockRenderOutput{
			text: r.indentLines(prepared.Text()),
			rows: blockProvenanceRows(prepared, blockID, kind, r.indent, r.width),
		}
	})
}

func (r *renderer) renderUserSnapshot(index int, s scrollback.BlockSnapshot, p scrollback.UserCardSnapshot, expand bool) string {
	return r.renderPreparedSnapshot(index, s.ID, s.Revision, scrollback.KindUser, expand, func() blocks.Prepared { return r.prepareUserSnapshot(p) })
}

func (r *renderer) renderNoticeSnapshot(index int, s scrollback.BlockSnapshot, p scrollback.NoticeCardSnapshot, expand bool) string {
	return r.renderPreparedSnapshot(index, s.ID, s.Revision, scrollback.KindNotice, expand, func() blocks.Prepared { return r.prepareNoticeSnapshot(p) })
}

func (r *renderer) renderHookSnapshot(index int, s scrollback.BlockSnapshot, p scrollback.HookCardSnapshot, expand bool) string {
	return r.renderPreparedSnapshot(index, s.ID, s.Revision, scrollback.KindHook, expand, func() blocks.Prepared { return r.prepareHookSnapshot(p) })
}

func (r *renderer) renderTurnStatSnapshot(index int, s scrollback.BlockSnapshot, p scrollback.TurnStatCardSnapshot, expand bool) string {
	return r.renderPreparedSnapshot(index, s.ID, s.Revision, scrollback.KindTurnStat, expand, func() blocks.Prepared { return r.prepareTurnStatSnapshot(p) })
}

func (r *renderer) renderErrorSnapshot(index int, s scrollback.BlockSnapshot, p scrollback.ErrorCardSnapshot, expand bool) string {
	return r.renderPreparedSnapshot(index, s.ID, s.Revision, scrollback.KindError, expand, func() blocks.Prepared { return r.prepareErrorSnapshot(p, expand) })
}

func (r *renderer) renderDeliverySnapshot(index int, s scrollback.BlockSnapshot, p scrollback.DeliveryCardSnapshot, expand bool) string {
	return r.renderPreparedSnapshot(index, s.ID, s.Revision, scrollback.KindDelivery, expand, func() blocks.Prepared { return r.prepareDeliverySnapshot(p) })
}

func (r *renderer) prepareUserSnapshot(p scrollback.UserCardSnapshot) blocks.Prepared {
	return blocks.PrepareUser(blocks.SnapshotUser(blocks.UserInput{Text: p.Text, Media: p.Media}), r.plainBlockLayout(false), r.blockTheme())
}

func (r *renderer) prepareNoticeSnapshot(p scrollback.NoticeCardSnapshot) blocks.Prepared {
	return blocks.PrepareNotice(blocks.SnapshotNotice(blocks.NoticeInput{Text: p.Text, Recovery: p.Recover}), r.plainBlockLayout(false), r.blockTheme())
}

func (r *renderer) prepareHookSnapshot(p scrollback.HookCardSnapshot) blocks.Prepared {
	return blocks.PrepareHook(blocks.SnapshotHook(blocks.HookInput{Text: p.Text, Phase: p.Phase, Tool: p.Tool, Decision: p.Decision}), r.plainBlockLayout(false), r.blockTheme())
}

func (r *renderer) prepareTurnStatSnapshot(p scrollback.TurnStatCardSnapshot) blocks.Prepared {
	return blocks.PrepareTurnStat(blocks.SnapshotTurnStat(blocks.TurnStatInput{Text: p.Text}), r.plainBlockLayout(false), r.blockTheme())
}

func (r *renderer) prepareErrorSnapshot(p scrollback.ErrorCardSnapshot, expand bool) blocks.Prepared {
	if p.Permanent {
		return blocks.PreparePermanentError(blocks.SnapshotPermanentError(blocks.PermanentErrorInput{Text: p.Text}), r.plainBlockLayout(expand), r.blockTheme())
	}
	return blocks.PrepareError(blocks.SnapshotError(blocks.ErrorInput{Text: p.Text}), r.plainBlockLayout(false), r.blockTheme())
}

func (r *renderer) prepareDeliverySnapshot(p scrollback.DeliveryCardSnapshot) blocks.Prepared {
	return blocks.PrepareDelivery(blocks.SnapshotDelivery(blocks.DeliveryInput{ScheduleName: p.ScheduleName, FireID: p.FireID, Text: p.Text}), r.plainBlockLayout(false), r.blockTheme())
}
