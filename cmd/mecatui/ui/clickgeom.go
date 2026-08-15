package ui

import (
	"charm.land/lipgloss/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/theme"
)

// clickgeom.go holds the mouse HIT-TEST geometry for clickable UI elements
// (issue #486) and, since issue #555, the region-emitting registry that is the
// SINGLE hit-test entry point (clickAt ← approvalClickRegions). The geometry is
// ARITHMETIC derived from the same layout model the renderer uses — never a
// string search over the rendered frame. The button row shares ONE source
// (permissionButtonsLine / planButtonsLine) and the body's screen position ONE
// source (convTopRow ← chrome()), so render and hit-test can never disagree:
// each surface's regions are emitted from the SAME layout its render path
// consumed (approvalClickRegions is the only place per-flavour geometry lives),
// not re-derived by a parallel hit-test arm. A new clickable surface emits its
// regions from its layout — it never adds an arm here.

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

// ---------------------------------------------------------------------------
// Clickable regions (the region-emitting hit-test registry, issue #555)
// ---------------------------------------------------------------------------

// cellRect is a half-open screen-cell rectangle [x0,x1)×[y0,y1). It is the 2-D
// generalisation of buttonRect (which is a single-row span).
type cellRect struct {
	x0, x1, y0, y1 int
}

func (r cellRect) contains(x, y int) bool { return x >= r.x0 && x < r.x1 && y >= r.y0 && y < r.y1 }

// ClickAction is the SEMANTIC payload a clickable region resolves to — a sum
// type, never a bare func: funcs do not survive the Elm value-Model copies and
// cannot be asserted on in tests. One dispatcher (Model.dispatchClick) maps an
// action to the same path its key chord drives.
type ClickAction struct {
	kind clickActionKind
	// focus is the approval-button focus index (0=allow-once, 1=always, 2=deny)
	// for clickAskVerdict — the same index the keyboard's focus ring uses, so
	// resolveAsk(focusVerdict(focus)) stays the ONE verdict path.
	focus int
}

type clickActionKind int

const (
	// clickAskVerdict resolves the open permission/plan ask to the clicked
	// button's verdict (focus 0/1/2 → Verdict via focusVerdict). The zero value
	// of clickActionKind is deliberately unnamed: clickAt reports a miss with
	// ok=false, never a zero-kind action.
	clickAskVerdict clickActionKind = iota + 1
)

// ClickableRegion is one clickable cell rectangle with its semantic action.
// HONEST PROVENANCE: regions are computed by re-running the surface's layout
// builders ON CLICK (approvalClickRegions), not captured from the render pass
// — the Elm value-Model has no side channel from View() to stash them. What
// makes this structurally better than the old parallel hit-test arms is that
// render and hit-test share the SAME builders (and the buttons row travels
// inside the builder's return value), so the two derivations consume one
// source and cannot skew: the failure mode the arms encoded was each arm
// OWNING its own geometry copy.
type ClickableRegion struct {
	rect   cellRect
	action ClickAction
}

// clickRegionsForAsk builds the approval button regions for the CURRENT front
// ask from the layout that rendered it: buttonsScreenY is the button box's top
// screen row and leftInset the column the buttons LINE starts at, both taken
// from the SAME layout the render path consumed so the regions sit exactly
// where the buttons were drawn. rects are the buttons' horizontal spans within
// the buttons LINE (askButtonRects), lifted to screen rows here.
func clickRegionsForAsk(buttonsScreenY, buttonsHeight int, rects []buttonRect, leftInset int) []ClickableRegion {
	regions := make([]ClickableRegion, 0, len(rects))
	for _, r := range rects {
		regions = append(regions, ClickableRegion{
			rect: cellRect{
				x0: leftInset + r.x0, x1: leftInset + r.x1,
				y0: buttonsScreenY, y1: buttonsScreenY + buttonsHeight,
			},
			action: ClickAction{kind: clickAskVerdict, focus: r.focus},
		})
	}
	return regions
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

// approvalClickRegions returns the CURRENT front ask's clickable regions. It
// re-runs the SAME layout builders the render path uses (shared builders, not
// a shared pass — see ClickableRegion's provenance note), so the hit-test and
// the frame derive from one source and cannot skew. It returns nil outside
// phaseAwaitingApproval (or on a zero-size region) so a click resolves nothing.
//
// The three ask flavours (plan review / full-screen args view / generic modal)
// each produce their regions from their own layout here — the ONLY place the
// per-flavour geometry lives. A NEW approval surface adds one case here and
// emits regions from its layout; it never grows a parallel hit-test arm.
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
	case isPlanAsk(m.ask.Tool):
		// The shared layout measures the rendered viewport, its joining newline,
		// and the three-row Lipgloss buttons.
		layout := m.planReviewLayout(m.ask)
		return clickRegionsForAsk(top+layout.buttonsRow, layout.buttonsHeight, askButtonRects(th, hk, m.ask, true), 0)

	case m.argsViewOpen:
		// The full-screen ask-args view pins the GENERIC permission buttons at
		// the bottom of the body region (issue #488).
		layout := m.argsReviewLayout(m.ask)
		return clickRegionsForAsk(top+layout.buttonsRow, layout.buttonsHeight, askButtonRects(th, hk, m.ask, false), 0)

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
		buttonsHeight := lipgloss.Height(permissionButtonsLine(th, hk, m.ask))
		return clickRegionsForAsk(buttonsScreenY, buttonsHeight, askButtonRects(th, hk, m.ask, false), cardRect.x0+leftInset)
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
