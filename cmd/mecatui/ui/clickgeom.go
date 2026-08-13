package ui

import (
	"charm.land/lipgloss/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/theme"
)

// clickgeom.go holds the mouse HIT-TEST geometry for clickable UI elements. Its
// first consumer is the permission-approval surface: the generic modal's and the
// plan-review action bar's buttons respond to a left-click exactly as they respond
// to their key chords (issue #486). The geometry is ARITHMETIC (derived from the
// same layout model and styled-label widths the renderer uses), never a string
// search over the rendered frame — the render path and the hit-test path share ONE
// source for the button row (permissionButtonsLine / planButtonsLine) and ONE
// source for the body's screen position (convTopRow ← chrome()), so they can never
// disagree.

// buttonGap is the cell width of the separator between the styled buttons in the
// buttons line. It is DERIVED from approvalButtonSep (the shared separator the
// JoinHorizontal render calls use), never a parallel literal, so the hit-test
// column arithmetic tracks the render exactly.
var buttonGap = lipgloss.Width(approvalButtonSep)

// askButtonRects returns the horizontal cell rects of each rendered button within
// the buttons LINE, in focus order ({0=allow-once, 1=always, 2=deny}; a two-button
// modal yields entries for focus 0 and 2). Each rect is the half-open column span
// [x0, x1) the button's STYLED label occupies.
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

// contains reports whether column x falls inside the rect.
func (r buttonRect) contains(x int) bool { return x >= r.x0 && x < r.x1 }

// centeredCardOrigin returns the screen cell (x, y) where lipgloss.Place puts the
// top-left of a cardW×cardH card centered in a regionW×regionH region — the exact
// arithmetic of centerCard's lipgloss.Place(width, height, Center, Center, card).
// Both axes floor; a negative result clamps to 0 (a card larger than the region
// still renders from the region's origin).
func centeredCardOrigin(cardW, cardH, regionW, regionH int) (x, y int) {
	x = (regionW - cardW) / 2
	if x < 0 {
		x = 0
	}
	y = (regionH - cardH) / 2
	if y < 0 {
		y = 0
	}
	return x, y
}

// askButtonAt maps a screen cell (x, y) to the focus index of the permission
// button under it, reporting ok=false on a miss. It is the mouse entry point for
// the approval surface: gated on phaseAwaitingApproval, it hit-tests either the
// plan-review action bar (a plan ask — the buttons row is the first row of the
// pinned planReviewFooterHeight-row bar at the bottom of the body region, flush at
// the region's left edge) or the generic centered modal card (any other ask).
//
// The body region's top screen row is convTopRow(m) and its height is m.vp.Height()
// — the SAME chrome()-derived values renderBody() is laid out with, so the card's
// centered position and the bar's flush position land exactly where the pixels are.
// A click that lands on the modal but not on a button returns ok=false (the caller
// swallows it — the modal is a gate, not a form).
func (m Model) askButtonAt(x, y int) (int, bool) {
	if m.phase != phaseAwaitingApproval {
		return 0, false
	}
	top := convTopRow(m)
	if top < 0 {
		return 0, false
	}
	regionW := m.width
	regionH := m.vp.Height()
	if regionW <= 0 || regionH <= 0 {
		return 0, false
	}
	th := m.deps.Theme
	hk := m.helpKeyMarkings()

	if isPlanAsk(m.ask.Tool) {
		// The shared layout measures the rendered viewport, its joining newline,
		// and the three-row Lipgloss buttons. Do not substitute the nominal footer
		// height here: the button box intentionally extends past that reservation.
		layout := m.planReviewLayout(m.ask)
		buttonsScreenY := top + layout.buttonsRow
		if y < buttonsScreenY || y >= buttonsScreenY+layout.buttonsHeight {
			return 0, false
		}
		for _, r := range askButtonRects(th, hk, m.ask, true) {
			if r.contains(x) {
				return r.focus, true
			}
		}
		return 0, false
	}

	// Generic modal: rebuild the body content AND take the button box's top row
	// from the builder that laid it out (permissionModalBodyParts), so the card's
	// rendered size and the buttons row index agree with the frame by construction
	// — never a re-derived offset.
	body, buttonsRow := m.permissionModalBody()
	card := th.Style("askCard").Render(body)
	cardW := lipgloss.Width(card)
	cardH := lipgloss.Height(card)
	originX, originY := centeredCardOrigin(cardW, cardH, regionW, regionH)

	// Work in CARD-RELATIVE coordinates (0,0 = the card's top-left border cell),
	// then translate the button box's top row to a screen row. buttonsRow is the
	// box's top row within the CONTENT (the string centerCard frames); the askCard
	// style adds a 1-row border + 1-row top padding above that content, so content
	// row L renders at card row (borderTop + padTop) + L.
	style := th.Style("askCard")
	topInset := style.GetBorderTopSize() + style.GetPaddingTop()
	buttonsCardRow := topInset + buttonsRow
	buttonsScreenY := top + originY + buttonsCardRow
	buttonsHeight := lipgloss.Height(permissionButtonsLine(th, hk, m.ask))
	// Match the full rendered button box, but not the footnote row below it.
	if y < buttonsScreenY || y >= buttonsScreenY+buttonsHeight {
		return 0, false
	}
	// Horizontal: card's left border+padding insets the content (and the buttons
	// line, which is flush-left in the content) from the card's left edge.
	leftInset := style.GetBorderLeftSize() + style.GetPaddingLeft()
	for _, r := range askButtonRects(th, hk, m.ask, false) {
		if r.contains(x - originX - leftInset) {
			return r.focus, true
		}
	}
	return 0, false
}
