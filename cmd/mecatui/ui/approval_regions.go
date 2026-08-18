package ui

import (
	"charm.land/lipgloss/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/theme"
)

// approval_regions.go owns the approval modal's mouse hit-test geometry (issue
// #555 Phase 1): regions are ALWAYS emitted from the same layout the render
// path consumed, so render and hit-test can never drift. The shared region
// vocabulary (cellRect, ClickAction, ClickableRegion) and clickRegionsForAsk
// stay in clickgeom.go — clickRegionsForAsk is the every-surface emitter.

func askButtonRects(th theme.Theme, hk helpKeys, ask pendingAsk, plan bool) []buttonRect {
	buttons := approvalButtons(th, hk, ask, plan)
	rects := make([]buttonRect, 0, len(buttons))
	x := 0
	for _, button := range buttons {
		w := lipgloss.Width(button.label)
		rects = append(rects, buttonRect{x0: x, x1: x + w, focus: button.focus})
		x += w + buttonGap
	}
	return rects
}

// buttonRect is one button's half-open horizontal cell span [x0, x1) within the
// buttons line, tagged with the focus index it resolves to.
type buttonRect struct {
	x0, x1 int
	focus  int
}

// ---------------------------------------------------------------------------
// Clickable regions (the region-emitting hit-test registry, issue #555)
// ---------------------------------------------------------------------------

// cellRect is a half-open screen-cell rectangle [x0,x1)×[y0,y1). It is the 2-D
// generalisation of buttonRect (which is a single-row span).
type cellRect struct {
	x0, x1, y0, y1 int
}

func (m Model) approvalClickRegions() []ClickableRegion {
	if m.phase != phaseAwaitingApproval {
		return nil
	}
	top := convTopRow(m)
	if top < 0 || m.width <= 0 || m.vp.Height() <= 0 {
		return nil
	}
	th := m.deps.Theme
	hk := m.helpKeyMarkings()

	switch {
	case isPlanAsk(m.approval.ask.Tool):
		// The shared layout measures the rendered viewport, its joining newline,
		// and the three-row Lipgloss buttons.
		layout := m.planReviewLayout(m.approval.ask)
		return clickRegionsForAsk(top+layout.buttonsRow, layout.buttonsHeight, askButtonRects(th, hk, m.approval.ask, true), 0)

	case m.approval.argsViewOpen:
		// The full-screen ask-args view pins the GENERIC permission buttons at
		// the bottom of the body region (issue #488).
		layout := m.argsReviewLayout(m.approval.ask)
		return clickRegionsForAsk(top+layout.buttonsRow, layout.buttonsHeight, askButtonRects(th, hk, m.approval.ask, false), 0)

	default:
		// Generic modal: rebuild the body content AND take the button box's top
		// row from the builder that laid it out (permissionModalBodyParts), so
		// the card's rendered size and the buttons row index agree with the
		// frame by construction.
		cardRect, buttonsRow, ok := m.approvalCardRect()
		if !ok {
			return nil
		}
		style := th.Style("askCard")
		// buttonsRow is the box's top row within the CONTENT; the askCard style
		// adds its border+padding above, and the buttons line is flush-left in
		// the content, inset by the same left frame.
		topInset := style.GetBorderTopSize() + style.GetPaddingTop()
		leftInset := style.GetBorderLeftSize() + style.GetPaddingLeft()
		buttonsScreenY := cardRect.y0 + topInset + buttonsRow
		buttonsHeight := lipgloss.Height(permissionButtonsLine(th, hk, m.approval.ask))
		return clickRegionsForAsk(buttonsScreenY, buttonsHeight, askButtonRects(th, hk, m.approval.ask, false), cardRect.x0+leftInset)
	}
}

// approvalCardRect returns the generic permission modal card's screen rect
// (top + origin + size) and the buttonsRow it measured — the SINGLE source for
// both the click hit-test (approvalClickRegions' default arm) and the
// wheel-over-card gate (askArgsWheelOverCard), so those two consumers can
// never drift on where the card is. ok=false on a zero-size layout.
func (m Model) approvalCardRect() (rect cellRect, buttonsRow int, ok bool) {
	top := convTopRow(m)
	if top < 0 || m.width <= 0 || m.vp.Height() <= 0 {
		return cellRect{}, 0, false
	}
	body, buttonsRow := m.permissionModalBody()
	style := m.deps.Theme.Style("askCard")
	card := style.Render(body)
	cardW := lipgloss.Width(card)
	cardH := lipgloss.Height(card)
	originX, originY := centeredCardOrigin(cardW, cardH, m.width, m.vp.Height())
	return cellRect{
		x0: originX, x1: originX + cardW,
		y0: top + originY, y1: top + originY + cardH,
	}, buttonsRow, true
}

// clickAt maps a screen cell to the ClickAction of the clickable region under
// it, reporting ok=false on a miss. It is the SINGLE hit-test entry point: a
// lookup over the regions the current surface's layout emitted — never a
// re-derived layout. A click that lands on the modal but not on a button
// returns ok=false (the caller swallows it — the modal is a gate, not a form).
func (m Model) clickAt(x, y int) (ClickAction, bool) {
	for _, region := range m.approvalClickRegions() {
		if region.rect.contains(x, y) {
			return region.action, true
		}
	}
	return ClickAction{}, false
}

// askButtonAt maps a screen cell (x, y) to the focus index of the permission
// button under it, reporting ok=false on a miss. It is the focus-shaped
// convenience wrapper over clickAt for callers that still speak focus indices.
func (m Model) askButtonAt(x, y int) (int, bool) {
	act, ok := m.clickAt(x, y)
	if !ok || act.kind != clickAskVerdict {
		return 0, false
	}
	return act.focus, true
}

var buttonGap = lipgloss.Width(approvalButtonSep)
