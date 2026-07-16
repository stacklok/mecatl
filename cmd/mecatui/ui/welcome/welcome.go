package welcome

import (
	"image/color"
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/theme"
)

// obsidianBG is the mascot's keyed background — the Aztec obsidian. The mascot's
// white field keys to this so the dog floats on the card; it matches the theme's
// dark panel so the half-block surround blends into the centered card.
const obsidianBG = "#0E1311"

// Info is the presentation-ready context the splash renders, assembled by the
// caller (ui.help.go) from the model + relayed capabilities. The welcome package
// stays decoupled from ui/client: it receives plain strings + bools, not a Model.
type Info struct {
	Cwd      string // workspace path (already display-trimmed by the caller if needed)
	Model    string // active model id ("" → omit the line)
	Provider string // provider name ("" → just the model)
	Version  string // mecatui build version ("" → omit)
	Tagline  string // one-line tagline under the identity ("" → omit)

	// Affordances are the pre-rendered (themed) affordance rows the caller builds
	// from its caps-tailored zeroStateRows(), so the welcome card's chord list stays
	// byte-equivalent to the legacy card. Each entry is one full line.
	Affordances []string

	// MemoryNote is the pre-rendered "memory is on" line (already themed), or "" when
	// memory is off — the single caps-conditional content the card carries.
	MemoryNote string

	// GatewayNote is the pre-rendered "gateway detected" line (already themed), or
	// "" when no intent-driven provider is available-but-not-default. It surfaces a
	// no-API-key alternative at the moment a new operator is most attentive.
	GatewayNote string

	// FullColor is true on a truecolor terminal: the wordmark then gets the
	// jade→gold gradient; otherwise it collapses to the single accent colour.
	FullColor bool

	// Kitty is true when the terminal supports the Kitty graphics protocol and the
	// caller has transmitted the mascot image: the splash then emits the
	// placeholder-cell grid (high-res image) instead of the half-block mascot, at
	// the SAME cols×rows footprint.
	Kitty bool
}

// Layout thresholds for the responsive width tier and the tiny-terminal clamp.
const (
	tierSmallCols  = 36 // short terminals
	tierMediumCols = 48 // the common case
	tierLargeCols  = 60 // tall terminals
	mascotMargin   = 4  // left indent of the mascot block (matches the spike)

	minSplashWidth  = 40 // below this, skip the mascot (and wordmark)
	minSplashHeight = 12

	// cardChrome is the row cost the ui's askCard frame adds around the Splash body:
	// the thick rounded border (1 top + 1 bottom) + Padding(1,2) (1 top + 1 bottom).
	// The body must therefore fit within `height - cardChrome` or centerCard's
	// lipgloss.Place overflows the viewport and the layout truncates the bottom — the
	// clip bug. Splash guarantees lipgloss.Height(body) <= height - cardChrome.
	cardChrome = 4

	// essentialReserve is the height a mascot tier must leave free BELOW itself for
	// the always-present head: card chrome (4) + wordmark (3) + blank + title + blank
	// + hint + blank = cardChrome + 8. Tier only returns a mascot whose cell-rows plus
	// this reserve fit the height budget, so the mascot can never crowd out the head.
	essentialReserve = cardChrome + 8
)

// Tier is the SINGLE mascot-footprint authority, used by Splash (to render the
// mascot) AND by the ui's maybeKittyTransmit (to bake the kitty virtual placement)
// — they MUST agree on (cols, rows) or the transmit and the placeholder grid would
// disagree. It is a pure function of (width, height): it picks the LARGEST mascot
// (60 → 48 → 36) whose width fits (cols + the left margin) AND whose cell-rows
// (cols/2) plus essentialReserve fit the height. It returns (0, 0) when even the
// smallest mascot can't fit — the caller then renders mascot-less (wordmark + head
// + whatever optional content fits). rows is always cols/2 (the square relation:
// cols×cols px sampling → cols/2 half-block cell-rows).
func Tier(width, height int) (cols, rows int) {
	for _, c := range []int{tierLargeCols, tierMediumCols, tierSmallCols} {
		if c+mascotMargin <= width && c/2+essentialReserve <= height {
			return c, c / 2
		}
	}
	return 0, 0
}

// splashSection is one body block. keep is the drop-priority for optional sections
// (lower = kept first when the budget is tight); head sections are mandatory.
type splashSection struct {
	text string
	keep int
}

// sectionCost is the row cost of a section: its rendered height, plus 1 for the
// blank separator that precedes it (the very first emitted section has no leading
// separator, so isFirst drops that 1).
func sectionCost(s splashSection, isFirst bool) int {
	h := lipgloss.Height(s.text)
	if !isFirst {
		h++
	}
	return h
}

// Splash assembles the UN-framed welcome body and GUARANTEES it fits the height
// budget: mascot (top, sized by Tier and dropped entirely when it can't fit) ·
// gradient wordmark · title · hint · then the OPTIONAL info sections (cwd /
// model·provider / version / tagline / affordances / memory) included greedily by
// keep-priority only while they still fit `height - cardChrome`. The caller frames
// it (centerCard, which adds cardChrome rows). The literal "Welcome to mecatui"
// always appears (a test greps it).
//
// width/height are the conversation region dims. On a tiny region (width <
// minSplashWidth or height < minSplashHeight) it degrades to a minimal hint (title
// + prompt hint + affordances) that never panics. Whenever height > 0 the returned
// body satisfies lipgloss.Height(body) <= height - cardChrome, so it never clips.
func Splash(th theme.Theme, in Info, width, height int) string {
	if width > 0 && width < minSplashWidth || height > 0 && height < minSplashHeight {
		return tinySplash(th, in)
	}

	head := splashHead(th, in, width, height)
	optional := splashOptional(th, in)
	include := fitOptional(head, optional, height-cardChrome)

	// Emit in DISPLAY order: head (always), then the included optional sections in
	// their original (display) order, each preceded by a blank separator.
	var b strings.Builder
	first := true
	emit := func(s splashSection) {
		if !first {
			b.WriteString("\n\n")
		}
		b.WriteString(strings.TrimRight(s.text, "\n"))
		first = false
	}
	for _, s := range head {
		emit(s)
	}
	for i, s := range optional {
		if include[i] {
			emit(s)
		}
	}
	return b.String()
}

// tinySplash is the minimal, safe body for a terminal too small for the splash:
// the greppable title, a prompt hint, and the affordance rows so discoverability
// survives. Never panics.
func tinySplash(th theme.Theme, in Info) string {
	var b strings.Builder
	b.WriteString(th.Style("askTitle").Render("Welcome to mecatui") + "\n")
	b.WriteString(th.Style("muted").Render("Type a request and press enter."))
	for _, row := range in.Affordances {
		b.WriteString("\n" + row)
	}
	return b.String()
}

// splashHead builds the always-present head: the mascot (kitty placeholder grid or
// half-block fallback, sized by Tier and omitted when Tier returns 0), then the
// wordmark, title, and prompt hint. Tier guarantees the head fits the budget.
func splashHead(th theme.Theme, in Info, width, height int) []splashSection {
	var head []splashSection
	if cols, rows := Tier(width, height); cols > 0 {
		var mascot string
		if in.Kitty {
			mascot = PlaceholderGrid(cols, rows, mascotMargin)
		} else {
			mascot = HalfBlockMascot(cols, hexToRGB(obsidianBG), mascotMargin)
		}
		if mascot != "" {
			// Store WITHOUT a trailing newline so lipgloss.Height == the rows it actually
			// contributes; a stray trailing "\n" would over-count the cost by 1 and silently
			// drop an info line.
			head = append(head, splashSection{text: strings.TrimRight(mascot, "\n")})
		}
	}
	head = append(head,
		splashSection{text: Wordmark(th, in.FullColor)},
		splashSection{text: th.Style("askTitle").Render("Welcome to mecatui")},
		splashSection{text: th.Style("toolArgs").Render("  Type a request below and press enter.")},
	)
	return head
}

// splashOptional builds the optional info sections in DISPLAY order, each tagged
// with its KEEP-priority (the order they're dropped as the budget tightens:
// affordances most valuable, the memory note least). Absent fields are skipped.
func splashOptional(th theme.Theme, in Info) []splashSection {
	muted := th.Style("muted")
	var optional []splashSection
	if in.Cwd != "" {
		optional = append(optional, splashSection{text: muted.Render("  " + in.Cwd), keep: 2})
	}
	if line := modelLine(in); line != "" {
		optional = append(optional, splashSection{text: muted.Render("  " + line), keep: 3})
	}
	if in.Version != "" {
		optional = append(optional, splashSection{text: muted.Render("  mecatui " + in.Version), keep: 4})
	}
	if in.GatewayNote != "" {
		optional = append(optional, splashSection{text: in.GatewayNote, keep: 4})
	}
	if in.Tagline != "" {
		optional = append(optional, splashSection{text: th.Style("toolArgs").Render("  " + in.Tagline), keep: 5})
	}
	if len(in.Affordances) > 0 {
		optional = append(optional, splashSection{text: strings.Join(in.Affordances, "\n"), keep: 1})
	}
	if in.MemoryNote != "" {
		optional = append(optional, splashSection{text: in.MemoryNote, keep: 6})
	}
	return optional
}

// fitOptional decides which optional sections are included: the mandatory head is
// accounted first, then optional sections are greedily included by ascending keep
// priority while they still fit `budget` rows. A budget <= 0 (unknown height) means
// no cap — include everything. Returns a per-optional-index inclusion map.
func fitOptional(head, optional []splashSection, budget int) map[int]bool {
	include := make(map[int]bool, len(optional))
	if budget <= 0 {
		for i := range optional {
			include[i] = true
		}
		return include
	}

	used := 0
	for i, s := range head {
		used += sectionCost(s, i == 0)
	}

	// Insertion-sort the optional indices by keep priority (small slice; no dep).
	order := make([]int, len(optional))
	for i := range optional {
		order[i] = i
	}
	for i := 1; i < len(order); i++ {
		for j := i; j > 0 && optional[order[j]].keep < optional[order[j-1]].keep; j-- {
			order[j], order[j-1] = order[j-1], order[j]
		}
	}
	for _, idx := range order {
		c := sectionCost(optional[idx], false) // never first (head precedes it)
		if used+c <= budget {
			include[idx] = true
			used += c
		}
	}
	return include
}

// modelLine composes the "model · provider" identity line from Info, omitting
// absent halves so a bare model or a bare provider renders cleanly.
func modelLine(in Info) string {
	switch {
	case in.Model != "" && in.Provider != "":
		return in.Model + " · " + in.Provider
	case in.Model != "":
		return in.Model
	case in.Provider != "":
		return in.Provider
	default:
		return ""
	}
}

// ObsidianBG is the exported mascot keyed-background colour, for any caller that
// needs to match the card surround.
func ObsidianBG() color.Color { return lipgloss.Color(obsidianBG) }
