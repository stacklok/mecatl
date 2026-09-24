package ui

import (
	"strings"

	"charm.land/lipgloss/v2"
)

// layout.go is the SINGLE SOURCE OF TRUTH for the TUI's vertical region stack. The
// frame is a column of stacked regions joined with "\n": a header on top, the
// conversation body, zero or more transient inline regions (slash palette, @-mention
// menu, queued-follow-ups card), then the input and footer chrome. THREE consumers
// derive from the SAME model: View() (which renders it), the relayout step in
// update.go (which sizes the viewport so the footer is never pushed off-screen), and
// screenToContent/convTopRow in selection.go (which map a mouse cell to the body).
// Before this model each of those had its OWN copy of the stack: View() hand-joined a
// regions slice, onResize subtracted the magic numbers taH=4/footerH=2 plus the
// header height, and convTopRow ASSUMED the body was regions[1] (header height alone).
// Deriving all three from chrome() kills those duplicated assumptions.
//
// The layout is DERIVED ON DEMAND (never cached on the Model): chrome heights change
// WITHOUT a resize — the header re-wraps when the active model id changes, and the
// palette/mention/queue regions toggle as the user types or a run streams — so a
// cached height would go stale between resizes. The regions are a few small lipgloss
// renders and mouse/relayout events are not hot, so recomputing is cheap.

// regionRole identifies a vertical region's slot in the frame, top to bottom. The
// order of the consts is the on-screen order; sumHeight over the regions ABOVE a
// given role yields that role's top screen row.
type regionRole int

const (
	regionHeader regionRole = iota
	// regionBody is the conversation region. Its content is EITHER the live
	// conversation viewport (m.vp.View()) OR a full-frame takeover string rendered at
	// the viewport's dimensions (a centred overlay / help / fatal screen — see the
	// switch in View()). A takeover body is sized to the viewport too, so the region
	// heights still hold; but relayout's SetHeight and convTopRow's offset are only
	// MEANINGFUL for the viewport-body case (they steer scrolling and click→content
	// mapping, which only apply to the real viewport). A SELECTION must therefore never
	// start over a takeover body — selectable() enforces that, and
	// TestSelectableBodyIsViewport pins the invariant.
	regionBody
	regionPalette
	regionMention
	regionQueue
	// regionInputSpacer is a single blank row directly ABOVE the input box, so the input
	// isn't jammed against the conversation/transient area. It sits in the `below` slice
	// (consumed by relayout's body-height subtraction), so the body shrinks by its one
	// row; convTopRow (which sums only the regions ABOVE the body) is unaffected.
	regionInputSpacer
	regionInput
	regionFooter
)

// inputSpacerRow is the content of regionInputSpacer: a single blank row
// (lipgloss.Height("") == 1) giving the input box one row of top padding.
const inputSpacerRow = ""

// region is one rendered vertical slice of the frame: its role and the exact styled
// string View() will place at that slot. height() is its on-screen row count.
type region struct {
	role    regionRole
	content string
}

// height is the number of screen rows the region's rendered content occupies,
// measured the SAME way every other height in the TUI is (lipgloss.Height) so the
// layout can never disagree with what View() renders.
func (r region) height() int {
	return lipgloss.Height(r.content)
}

// layout is the assembled, ordered region stack for one frame. View() joins it;
// onResize/relayout sum the non-body regions to size the body; convTopRow sums the
// regions above the body to find its top screen row.
type layout struct {
	regions []region
}

// sumHeight totals the on-screen rows of a region slice.
func sumHeight(rs []region) int {
	total := 0
	for _, r := range rs {
		total += r.height()
	}
	return total
}

// chrome renders every NON-body region in the current frame, split into the regions
// ABOVE the body (above) and BELOW it (below). It mirrors View()'s exact ordering and
// non-empty conditions so the assembled frame is byte-identical to the old hand-joined
// one:
//
//	above = [header]
//	below = [palette?, mention?, queue?, input, footer]
//
// The palette/mention/queue regions are appended ONLY when their renderer returns a
// non-empty string — the same gate View() used — so a no-transient frame produces
// exactly [header] + body + [input, footer], identical to before. This is the one
// place the transient conditions live; View(), relayout, and convTopRow all consume
// the result rather than re-checking them.
func (m Model) chrome() (above, below []region) {
	above = []region{{role: regionHeader, content: m.renderHeader()}}

	// Render non-palette regions first so the palette receives only rows left after
	// preserving the prompt, footer, queue, and at least one conversation row.
	var transients []region
	if men := renderMention(m.deps.Theme, m.mention, m.width); men != "" {
		transients = append(transients, region{role: regionMention, content: men})
	}
	if q := m.renderQueue(); q != "" {
		transients = append(transients, region{role: regionQueue, content: q})
	}
	if s := m.renderSteer(); s != "" {
		transients = append(transients, region{role: regionQueue, content: s})
	}
	fixed := []region{
		{role: regionInputSpacer, content: inputSpacerRow},
		{role: regionInput, content: m.renderInput()},
		{role: regionFooter, content: m.renderFooter()},
	}

	card := m.deps.Theme.Style("askCard")
	paletteRows := m.height - sumHeight(above) - sumHeight(transients) - sumHeight(fixed) - 1 -
		card.GetVerticalFrameSize() - 2 // header and key hint inside the card
	paletteRows = min(maxPaletteRows, max(0, paletteRows))
	if pal := renderPaletteSized(m.deps.Theme, m.palette, m.caps, m.prompt.Value(), m.width, paletteRows); pal != "" {
		below = append(below, region{role: regionPalette, content: pal})
	}
	below = append(below, transients...)
	below = append(below, fixed...)
	return above, below
}

// assembleLayout builds the full ordered region stack for a frame whose conversation
// body is the given string. The body region is sandwiched between chrome's above and
// below slices, so the result is the verbatim equivalent of View()'s old
// [header, body, palette?, mention?, queue?, input, footer] join order.
//
// The body parameter MUST be either the live conversation viewport (m.vp.View()) or a
// full-frame string rendered at the viewport's dimensions (an overlay/help/fatal
// takeover). relayout's SetHeight and convTopRow's offset are only valid for the
// viewport-body case — see the regionBody doc and TestSelectableBodyIsViewport.
func (m Model) assembleLayout(body string) layout {
	above, below := m.chrome()
	regions := make([]region, 0, len(above)+1+len(below))
	regions = append(regions, above...)
	regions = append(regions, region{role: regionBody, content: body})
	regions = append(regions, below...)
	return layout{regions: regions}
}

// join concatenates the region contents with "\n" — byte-identical to View()'s old
// strings.Join(regions, "\n").
func (l layout) join() string {
	parts := make([]string, len(l.regions))
	for i, r := range l.regions {
		parts[i] = r.content
	}
	return strings.Join(parts, "\n")
}
