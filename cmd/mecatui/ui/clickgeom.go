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

// buttonGap is the exact two-space separator lipgloss.JoinHorizontal inserts
// between the styled buttons in permissionButtonsLine / planButtonsLine.
const buttonGap = 2

// askButtonRects returns the horizontal cell rects of each rendered button within
// the buttons LINE, in focus order ({0=allow-once, 1=always, 2=deny}; a two-button
// modal yields entries for focus 0 and 2 only). Each rect is the half-open column
// span [x0, x1) the button's STYLED label occupies — its width measured with
// lipgloss.Width on the SAME styled label the renderer places, so a rebound chord's
// wider standalone form is hit-tested at its true width. Buttons are single-row
// tall in the JoinHorizontal row; the caller adds the row's screen Y and the card's
// left inset.
//
// It takes the theme and builds the un-focused label of each button directly
// (rather than re-splitting the joined line) so the rect widths are exact even
// though JoinHorizontal pads each cell to the row's tallest — every button is
// already the same height (askButton/askButtonActive share Padding(0,2)+rounded
// border), so no inter-cell padding is actually added.
func askButtonRects(th theme.Theme, hk helpKeys, ask pendingAsk, plan bool) []buttonRect {
	labelFor := func(focus int) string {
		style := th.Style("askButton")
		if ask.focus == focus {
			style = th.Style("askButtonActive")
		}
		if plan {
			switch focus {
			case 0:
				return style.Render(planApprovalButtonLabel(hk.allow, "Allow", "approve & run"))
			case 1:
				return style.Render(planApprovalButtonLabel(hk.allowAlways, "Always", "auto-accept edits"))
			default:
				return style.Render(planApprovalButtonLabel(hk.deny, "Deny", "iterate"))
			}
		}
		switch focus {
		case 0:
			return style.Render(approvalButtonLabel(hk.allow, "Allow", "allow"))
		case 1:
			return style.Render(approvalButtonLabel(hk.allowAlways, "Always", "always allow"))
		default:
			return style.Render(approvalButtonLabel(hk.deny, "Deny", "deny"))
		}
	}
	foci := []int{0, 2}
	if ask.offerAlways {
		foci = []int{0, 1, 2}
	}
	rects := make([]buttonRect, 0, len(foci))
	x := 0
	for _, f := range foci {
		w := lipgloss.Width(labelFor(f))
		rects = append(rects, buttonRect{x0: x, x1: x + w, focus: f})
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

// buttonsLineRow locates the buttons row within the modal body's CONTENT (the
// string passed to centerCard): it is the last line, minus one more when the
// always-allow footnote follows it. It must mirror the b.WriteString ordering in
// renderPermissionModal (… + "\n" + buttons [+ "\n" + footnote]).
func buttonsLineRow(bodyContent string, offerAlways bool) int {
	n := lipgloss.Height(bodyContent)
	if offerAlways {
		return n - 2
	}
	return n - 1
}

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
		// Plan review: the pinned action bar occupies the last planReviewFooterHeight
		// rows of the body region; the buttons row is its FIRST row. The plan view is
		// NOT centered — the buttons line starts at the region's left edge (x=0).
		barRow := top + regionH - planReviewFooterHeight
		// The 3-row button box (border top + label + border bottom) OVERFLOWS the
		// 3-row bar (which reserves only 1 row for the buttons + footnote + hint):
		// the box's bottom border row lands on the footnote row. Clamp the hit band
		// to the bar so a click on the footnote/scroll-hint rows does NOT resolve a
		// button — only the 2 rows that are unambiguously the button box count.
		btnH := lipgloss.Height(th.Style("askButton").Render("x"))
		if maxH := planReviewFooterHeight - 1; btnH > maxH {
			btnH = maxH
		}
		if y < barRow || y >= barRow+btnH {
			return 0, false
		}
		for _, r := range askButtonRects(th, hk, m.ask, true) {
			if r.contains(x) {
				return r.focus, true
			}
		}
		return 0, false
	}

	// Generic modal: rebuild the body content exactly as renderPermissionModal does
	// so the card's rendered size and the buttons row index agree with the frame.
	body := m.permissionModalBody()
	card := th.Style("askCard").Render(body)
	cardW := lipgloss.Width(card)
	cardH := lipgloss.Height(card)
	originX, originY := centeredCardOrigin(cardW, cardH, regionW, regionH)

	// Work in CARD-RELATIVE coordinates (0,0 = the card's top-left border cell),
	// then translate the buttons row to a screen row. buttonsLineRow returns the
	// buttons line's index within the CONTENT (the string centerCard frames); the
	// askCard style adds a 1-row border above that content, so the content's row L
	// renders at card row 1+L. (The card's padding is INSIDE the content's own
	// leading/trailing blank lines, so it does not shift the content index.)
	style := th.Style("askCard")
	buttonsCardRow := style.GetBorderTopSize() + buttonsLineRow(body, m.ask.offerAlways)
	buttonsScreenY := top + originY + buttonsCardRow
	// A button is a styled box buttonHeight rows tall (rounded border top + label +
	// border bottom), starting at the buttons row. Match the full box, not just the
	// label row, so a click on any of the button's three rows resolves it.
	btnH := lipgloss.Height(th.Style("askButton").Render("x"))
	if y < buttonsScreenY || y >= buttonsScreenY+btnH {
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
