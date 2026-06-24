// Package theme is the pure styling layer for mecatui. It owns the semantic
// colour palette, the derived lipgloss styles, and the glamour markdown style
// config — and nothing else. It imports only the charm styling libraries and
// stdlib: NO contracts/gen, NO grpc, NO mecatui/ui, NO internal/... packages.
// This keeps the visual language reusable and the architectural layering clean
// (ui depends on theme; theme depends on nobody in this repo).
package theme

import (
	"image/color"
	"math"
	"strconv"

	"charm.land/lipgloss/v2"
)

// Selection-highlight foreground constants and the luminance cutoff used to pick
// between them. selFgDark/selFgLight are FIXED near-black / near-white values
// (softer than pure #000/#fff to match the palette aesthetic) chosen purely to
// guarantee contrast against the classified background — deliberately NOT
// Text/Bg, which a theme may tune for prose, not for a solid block. The cutoff
// L≥selLumThreshold ⇒ the background is "light" ⇒ pick the dark foreground.
const (
	selFgDark       = "#1a1a1a"
	selFgLight      = "#f0f0f0"
	selLumThreshold = 0.4
)

// dangerPillBg / dangerPillFg are the FIXED, theme-INDEPENDENT colours of the
// "dangerPill" style (the YOLO posture badge). Unlike every other slot they are NOT
// palette-derived: a danger/safety affordance must read identically in every theme, so
// it carries its own alarm-red background and near-white foreground rather than the
// per-theme Error/Bg pair (which also avoided a washed-out Bg-on-Error contrast on a
// light theme). See the "dangerPill" comment in compile().
const (
	dangerPillBg = "#E03131" // alarm red (not the theme's burnt-orange Error)
	dangerPillFg = "#F5F5F5" // near-white
)

// Palette is the raw, semantic colour set a theme is defined by. Every field is
// a hex string ("#rrggbb"). Themes are authored (in Go or JSON) purely as a
// Palette; the lipgloss styles and the glamour StyleConfig are derived from it
// in compile(). The slots are semantic (what a colour means) rather than literal
// (what colour it is), so a new theme only has to answer "what is my accent?",
// not restyle every widget.
type Palette struct {
	// Brand / structural accents.
	Primary   string `json:"primary"`
	Secondary string `json:"secondary"`
	Accent    string `json:"accent"`

	// Surfaces (backgrounds), darkest to lightest.
	Bg        string `json:"bg"`
	BgPanel   string `json:"bgPanel"`
	BgElement string `json:"bgElement"`

	// Borders.
	Border       string `json:"border"`
	BorderActive string `json:"borderActive"`
	BorderSubtle string `json:"borderSubtle"`

	// Foreground text.
	Text      string `json:"text"`
	TextMuted string `json:"textMuted"`

	// In-app text-selection highlight background. Empty ⇒ derived from Accent.
	// The foreground is NOT a palette slot: it is computed from this background's
	// relative luminance (see contrastingText) so the block is legible on any
	// theme, dark or light.
	Selection string `json:"selection"`

	// Status colours.
	Success string `json:"success"`
	Warning string `json:"warning"`
	Error   string `json:"error"`
	Info    string `json:"info"`

	// Speaker / block roles.
	User      string `json:"user"`
	Assistant string `json:"assistant"`
	Tool      string `json:"tool"`

	// Markdown slots (glamour body styling).
	MdText    string `json:"mdText"`
	MdHeading string `json:"mdHeading"`
	MdLink    string `json:"mdLink"`
	MdCode    string `json:"mdCode"`
	MdQuote   string `json:"mdQuote"`

	// Syntax-highlight slots (glamour code-block chroma).
	SynComment     string `json:"synComment"`
	SynKeyword     string `json:"synKeyword"`
	SynFunction    string `json:"synFunction"`
	SynVariable    string `json:"synVariable"`
	SynString      string `json:"synString"`
	SynNumber      string `json:"synNumber"`
	SynType        string `json:"synType"`
	SynOperator    string `json:"synOperator"`
	SynPunctuation string `json:"synPunctuation"`
}

// Theme is a named Palette plus its derived, ready-to-use styles. Construct one
// with New (which calls compile); never build the styles map by hand.
type Theme struct {
	Name    string
	Palette Palette

	styles map[string]lipgloss.Style // derived in compile()
	slots  map[string]string         // slot-name → hex, derived in compile()
}

// New builds a Theme from a name and palette, compiling the derived styles. It
// is the only constructor: it guarantees the styles map is populated so Style()
// never returns a zero value for a known slot.
func New(name string, p Palette) Theme {
	t := Theme{Name: name, Palette: p}
	t.compile()
	return t
}

// col turns a hex string into a color.Color. Empty strings yield nil, which
// lipgloss treats as "no colour" (terminal default) — safe for partial themes.
func col(hex string) color.Color {
	if hex == "" {
		return nil
	}
	return lipgloss.Color(hex)
}

// relLuminance returns the WCAG relative luminance (0..1) of a "#rrggbb" hex
// colour, plus ok=false when the string is empty or malformed. It is the
// colour-blind-safe basis for choosing a contrasting foreground: luminance is a
// brightness measure, independent of hue. The formula is the standard
// sRGB→linear gamma expansion (c≤0.04045 ? c/12.92 : ((c+0.055)/1.055)^2.4)
// weighted 0.2126·R + 0.7152·G + 0.0722·B.
func relLuminance(hex string) (float64, bool) {
	if len(hex) != 7 || hex[0] != '#' {
		return 0, false
	}
	chans := [3]float64{}
	for i := 0; i < 3; i++ {
		v, err := strconv.ParseUint(hex[1+i*2:3+i*2], 16, 8)
		if err != nil {
			return 0, false
		}
		c := float64(v) / 255.0
		if c <= 0.04045 {
			c /= 12.92
		} else {
			c = math.Pow((c+0.055)/1.055, 2.4)
		}
		chans[i] = c
	}
	return 0.2126*chans[0] + 0.7152*chans[1] + 0.0722*chans[2], true
}

// contrastingText picks the selection-highlight FOREGROUND for a given
// background hex: a near-black fg on a light background, a near-white fg on a
// dark one, decided by relative luminance (contrast, not hue → colour-blind
// safe). A malformed/empty background falls back to the light foreground (the
// common dark-theme case). The result feeds the "selection" style only.
func contrastingText(bgHex string) color.Color {
	l, ok := relLuminance(bgHex)
	if !ok {
		return col(selFgLight)
	}
	if l >= selLumThreshold {
		return col(selFgDark)
	}
	return col(selFgLight)
}

// Color returns the color.Color for a semantic slot name (e.g. "accent",
// "error", "user"). Unknown slots return nil (terminal default). The lookup is
// by the palette's JSON field name so callers and JSON authors share one
// vocabulary.
func (t Theme) Color(slot string) color.Color {
	return col(t.hex(slot))
}

// Style returns the derived lipgloss.Style for a named UI element. Known names:
// header, footer, viewport, userBlock, userLabel, assistantLabel, toolCard,
// toolName, toolArgs, toolOk, toolErr, askCard, askTitle, askButton,
// askButtonActive, spinner, muted, warning, dangerPill, errorText. Unknown names
// return an empty style so callers degrade gracefully rather than panic.
func (t Theme) Style(name string) lipgloss.Style {
	if s, ok := t.styles[name]; ok {
		return s
	}
	return lipgloss.NewStyle()
}

// hex resolves a palette slot name to its raw hex string via the slot map built
// in compile(). Unknown slots return "". Centralised so Color and JSON authors
// share one slot vocabulary.
func (t Theme) hex(slot string) string {
	return t.slots[slot]
}

// buildSlots returns the slot-name → hex map for a palette. The slot names match
// the Palette JSON tags so config authors and Color() callers use one vocabulary.
// Kept as data (not a switch) so adding a slot is a one-line change and the
// linter's cyclomatic budget is not a factor.
func buildSlots(p Palette) map[string]string {
	return map[string]string{
		"primary":        p.Primary,
		"secondary":      p.Secondary,
		"accent":         p.Accent,
		"bg":             p.Bg,
		"bgPanel":        p.BgPanel,
		"bgElement":      p.BgElement,
		"border":         p.Border,
		"borderActive":   p.BorderActive,
		"borderSubtle":   p.BorderSubtle,
		"text":           p.Text,
		"textMuted":      p.TextMuted,
		"selection":      p.Selection,
		"success":        p.Success,
		"warning":        p.Warning,
		"error":          p.Error,
		"info":           p.Info,
		"user":           p.User,
		"assistant":      p.Assistant,
		"tool":           p.Tool,
		"mdText":         p.MdText,
		"mdHeading":      p.MdHeading,
		"mdLink":         p.MdLink,
		"mdCode":         p.MdCode,
		"mdQuote":        p.MdQuote,
		"synComment":     p.SynComment,
		"synKeyword":     p.SynKeyword,
		"synFunction":    p.SynFunction,
		"synVariable":    p.SynVariable,
		"synString":      p.SynString,
		"synNumber":      p.SynNumber,
		"synType":        p.SynType,
		"synOperator":    p.SynOperator,
		"synPunctuation": p.SynPunctuation,
	}
}

// compile derives the lipgloss style set from the palette. Done once at New so
// View() never allocates styles in the hot path.
func (t *Theme) compile() {
	p := t.Palette
	t.slots = buildSlots(p)
	rounded := lipgloss.RoundedBorder()
	thick := lipgloss.ThickBorder()

	// In-app selection highlight: a SOLID flat block. The viewport's
	// lipgloss.StyleRanges STRIPS the selected span's own ANSI and re-renders it
	// with ONLY this style, so an explicit Background + computed Foreground paint a
	// legible block — reverse video (SGR 7) was invisible over already-coloured
	// content. The background is the palette's Selection slot, falling back to the
	// Accent when unset; the foreground is luminance-derived for contrast on any
	// palette (dark or light).
	selBg := p.Selection
	if selBg == "" {
		selBg = p.Accent
	}
	selFg := contrastingText(selBg)

	t.styles = map[string]lipgloss.Style{
		// Top header bar: session/model/mode, primary border bottom.
		"header": lipgloss.NewStyle().
			Foreground(col(p.Primary)).
			Bold(true).
			Padding(0, 1).
			BorderStyle(lipgloss.NormalBorder()).
			BorderBottom(true).
			BorderForeground(col(p.Border)),

		// Footer/status bar: muted, top border.
		"footer": lipgloss.NewStyle().
			Foreground(col(p.TextMuted)).
			Padding(0, 1).
			BorderStyle(lipgloss.NormalBorder()).
			BorderTop(true).
			BorderForeground(col(p.Border)),

		// Conversation viewport surface.
		"viewport": lipgloss.NewStyle().
			Foreground(col(p.Text)),

		// User prompt block: gold left bar. NO background tint — the faint panel tint
		// belongs ONLY to the input box we type in, never the conversation history (the
		// tinted history block read as a distinct, off surface). Conversation user turns
		// are the gold rail + the "▌ you" label over the plain viewport surface.
		"userBlock": lipgloss.NewStyle().
			Foreground(col(p.Text)).
			BorderStyle(lipgloss.NormalBorder()).
			BorderLeft(true).
			BorderForeground(col(p.User)).
			PaddingLeft(1),
		"userLabel": lipgloss.NewStyle().
			Foreground(col(p.User)).
			Bold(true),
		"assistantLabel": lipgloss.NewStyle().
			Foreground(col(p.Assistant)).
			Bold(true),

		// Tool-call card.
		"toolCard": lipgloss.NewStyle().
			BorderStyle(rounded).
			BorderForeground(col(p.Tool)).
			Padding(0, 1),
		"toolName": lipgloss.NewStyle().
			Foreground(col(p.Tool)).
			Bold(true),
		"toolArgs": lipgloss.NewStyle().
			Foreground(col(p.TextMuted)),
		"toolOk": lipgloss.NewStyle().
			Foreground(col(p.Success)).
			Bold(true),
		"toolErr": lipgloss.NewStyle().
			Foreground(col(p.Error)).
			Bold(true),

		// Permission-ask modal card: unmissable warning border.
		"askCard": lipgloss.NewStyle().
			BorderStyle(thick).
			BorderForeground(col(p.Warning)).
			Padding(1, 2),
		"askTitle": lipgloss.NewStyle().
			Foreground(col(p.Warning)).
			Bold(true),
		"askButton": lipgloss.NewStyle().
			Foreground(col(p.Text)).
			Padding(0, 2).
			BorderStyle(rounded).
			BorderForeground(col(p.Border)),
		"askButtonActive": lipgloss.NewStyle().
			Foreground(col(p.Bg)).
			Background(col(p.BorderActive)).
			Bold(true).
			Padding(0, 2).
			BorderStyle(rounded).
			BorderForeground(col(p.BorderActive)),

		// Misc.
		"spinner": lipgloss.NewStyle().
			Foreground(col(p.Accent)),
		"muted": lipgloss.NewStyle().
			Foreground(col(p.TextMuted)).
			Italic(true),
		// Reasoning summary block: dim and subordinate to the answer. Muted +
		// italic like a notice; it reads as secondary transparency, never a
		// trust anchor.
		"reasoning": lipgloss.NewStyle().
			Foreground(col(p.TextMuted)).
			Italic(true),
		"errorText": lipgloss.NewStyle().
			Foreground(col(p.Error)).
			Bold(true),

		// Warning: inline warning-coloured text (e.g. the auto-posture header badge)
		// — coloured + bold but NOT a filled pill, so it reads as an alert without
		// claiming the visual weight a danger pill does.
		"warning": lipgloss.NewStyle().
			Foreground(col(p.Warning)).
			Bold(true),
		// Danger pill: a FILLED, padded chip for the loudest persistent posture cue
		// (yolo). Its colours are DELIBERATELY FIXED, NOT palette-derived: an alarm-red
		// background (dangerPillBg) with near-white text (dangerPillFg). This is a SAFETY
		// affordance, not themed decoration — "danger is danger" must read the same in
		// every theme, and fixing the pair also removes the per-theme Bg-on-Error contrast
		// fragility (a light theme's Bg-on-burnt-orange could wash out). The 1-cell
		// horizontal padding is part of the grammar (a pill, not inline text) — callers
		// that width-fit a dangerPill render must account for the +2 visible cells
		// lipgloss.Width on the PLAIN text does not see (see view.go fitHeader).
		"dangerPill": lipgloss.NewStyle().
			Foreground(col(dangerPillFg)).
			Background(col(dangerPillBg)).
			Bold(true).
			Padding(0, 1),

		// Hook notice — "modified" outcome: info-coloured (an action was
		// rewritten by a hook — notable but benign, distinct from the muted info
		// notice and the error-coloured blocked notice). Blocked hooks reuse
		// errorText; informational hooks reuse muted.
		"hookModified": lipgloss.NewStyle().
			Foreground(col(p.Info)),

		// Hook notice — "advisory" outcome: warning-coloured + bold (an advisory
		// guardrail finding — client-visible, model-invisible; distinct from the
		// muted info notice, the info-coloured modified notice, and the error-
		// coloured blocked notice). Reuses the palette Warning slot.
		"hookAdvisory": lipgloss.NewStyle().
			Foreground(col(p.Warning)).
			Bold(true),

		// Unified-diff slots for Edit/Write tool cards: added lines green,
		// removed lines red, meta (path / "replace all") muted. Reuses the
		// status palette so a new theme restyles diffs for free.
		"diffAdd": lipgloss.NewStyle().
			Foreground(col(p.Success)),
		"diffRemove": lipgloss.NewStyle().
			Foreground(col(p.Error)),
		"diffMeta": lipgloss.NewStyle().
			Foreground(col(p.TextMuted)).
			Bold(true),

		// In-app text selection highlight (mouse-drag select + copy): a solid
		// high-contrast block (selBg / luminance-derived selFg above), NOT reverse
		// video. The viewport StyleRanges-strips the selected span and re-renders it
		// with ONLY this style, so a flat Background+Foreground reads on any palette
		// where reverse video over coloured content did not. It survives an
		// ANSI-strip cleanly in the stripped View goldens, and is applied only while
		// a selection is active — so the steady-state goldens are unaffected.
		"selection": lipgloss.NewStyle().Background(col(selBg)).Foreground(selFg),

		// Context-window pressure slots for the footer meter: success when the
		// context is comfortably below the compaction band, warning approaching
		// it, danger once over it.
		"ctxOk": lipgloss.NewStyle().
			Foreground(col(p.Success)),
		"ctxWarn": lipgloss.NewStyle().
			Foreground(col(p.Warning)),
		"ctxDanger": lipgloss.NewStyle().
			Foreground(col(p.Error)).
			Bold(true),
	}
}
