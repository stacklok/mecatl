package ui

import (
	"strings"

	"github.com/stacklok/mecatl/cmd/mecatui/ui/internal/cards"
)

const structuredCardDialect = 1

func (r *renderer) plainCardLayout(expand bool) cards.PlainLayout {
	return cards.SnapshotPlainLayout(cards.PlainLayoutInput{
		Width: r.contentWidth(), Expanded: expand, ExpandMark: r.marks.expandTools, Dialect: structuredCardDialect,
	})
}

func preparedText(prepared cards.Prepared) string { return strings.Join(prepared.Lines, "\n") }

// prepareStructuredBlock snapshots and prepares every migrated non-Markdown card
// family. It is called only after renderBlock's cheap revision/layout admission
// guard misses; settled cache hits therefore do no snapshot, hashing, or preparation.
func (r *renderer) prepareStructuredBlock(b *block, expand bool) (cards.Prepared, bool) {
	switch b.kind {
	case blockTool, blockUser, blockNotice, blockHook, blockTurnStat, blockError, blockDelivery:
		r.cardPrepares++
	default:
		return cards.Prepared{}, false
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
		return cards.Prepared{}, false
	}
}

func (r *renderer) prepareUserBlock(b *block) cards.Prepared {
	return cards.PrepareUser(cards.SnapshotUser(cards.UserInput{Text: b.raw, Media: b.media}), r.plainCardLayout(false), cards.UserAppearance{
		Label: r.th.Style("userLabel"), Body: r.th.Style("userBlock"), Muted: r.th.Style("muted"),
	})
}

func (r *renderer) prepareNoticeBlock(b *block) cards.Prepared {
	return cards.PrepareNotice(cards.SnapshotNotice(cards.NoticeInput{Text: b.raw, Recovery: b.recover}), r.plainCardLayout(false), cards.NoticeAppearance{
		Muted: r.th.Style("muted"), Warning: r.th.Style("warning"),
	})
}

func (r *renderer) prepareHookBlock(b *block) cards.Prepared {
	return cards.PrepareHook(cards.SnapshotHook(cards.HookInput{Text: b.raw, Phase: b.hookPhase, Tool: b.hookTool, Decision: b.hookDecision}), r.plainCardLayout(false), cards.HookAppearance{
		Muted: r.th.Style("muted"), Error: r.th.Style("errorText"), Modified: r.th.Style("hookModified"), Advisory: r.th.Style("hookAdvisory"),
	})
}

func (r *renderer) prepareTurnStatBlock(b *block) cards.Prepared {
	return cards.PrepareTurnStat(cards.SnapshotTurnStat(cards.TurnStatInput{Text: b.raw}), r.plainCardLayout(false), cards.TurnStatAppearance{Muted: r.th.Style("muted")})
}

func (r *renderer) prepareErrorBlock(b *block) cards.Prepared {
	return cards.PrepareError(cards.SnapshotError(cards.ErrorInput{Text: b.raw}), r.plainCardLayout(false), cards.ErrorAppearance{Error: r.th.Style("errorText")})
}

func (r *renderer) preparePermanentErrorBlock(b *block, expand bool) cards.Prepared {
	return cards.PreparePermanentError(cards.SnapshotPermanentError(cards.PermanentErrorInput{Text: b.raw}), r.plainCardLayout(expand), cards.PermanentErrorAppearance{
		Error: r.th.Style("errorText"), Muted: r.th.Style("muted"),
	})
}

func (r *renderer) prepareDeliveryBlock(b *block) cards.Prepared {
	return cards.PrepareDelivery(cards.SnapshotDelivery(cards.DeliveryInput{ScheduleName: b.toolName, FireID: b.deliveryFireID, Text: b.raw}), r.plainCardLayout(false), cards.DeliveryAppearance{
		Header: r.th.Style("hookModified"), Body: r.th.Style("muted"),
	})
}
