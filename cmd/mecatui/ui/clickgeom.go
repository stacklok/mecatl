package ui

// clickgeom uses no imports — the shared region vocabulary is stdlib-only.

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

// askButtonRects returns the horizontal cell rects of each rendered button within
// the buttons LINE, in focus order ({0=allow-once, 1=always, 2=deny}; a two-button
// modal yields entries for focus 0 and 2). Each rect is the half-open column span
// [x0, x1) the button's STYLED label occupies.
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
