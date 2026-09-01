package theme

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
)

// TestBuiltinsSlotCompleteness asserts every built-in theme populates every
// palette slot — a missing slot would render as terminal-default and break the
// visual language. Reflection over the Palette struct keeps this honest as new
// slots are added.
func TestBuiltinsSlotCompleteness(t *testing.T) {
	for name, th := range builtins {
		v := reflect.ValueOf(th.Palette)
		ty := v.Type()
		for i := 0; i < v.NumField(); i++ {
			got := v.Field(i).String()
			if strings.TrimSpace(got) == "" {
				t.Errorf("theme %q: slot %q is empty", name, ty.Field(i).Name)
			}
			if !strings.HasPrefix(got, "#") {
				t.Errorf("theme %q: slot %q = %q, want #rrggbb", name, ty.Field(i).Name, got)
			}
		}
	}
}

// TestStylesCompiled asserts compile() populates every named style slot the ui
// asks for. If a renderer references a slot the theme never compiles, Style()
// silently returns an empty style — this guards against that drift.
func TestStylesCompiled(t *testing.T) {
	want := []string{
		"header", "footer", "viewport", "userBlock", "userLabel",
		"assistantLabel", "toolCard", "toolName", "toolArgs", "toolOk",
		"toolErr", "askCard", "askTitle", "askArgs", "askButton", "askButtonActive",
		"spinner", "muted", "warning", "dangerPill", "errorText", "selection",
	}
	th := New("aztec", aztecPalette)
	for _, slot := range want {
		if _, ok := th.styles[slot]; !ok {
			t.Errorf("style slot %q not compiled", slot)
		}
	}
}

// TestUserBlockRailNoTint pins decision 1 (partial-reverted): the conversation user
// block carries the GOLD LEFT RAIL (BorderLeft, user-coloured) but NO background tint —
// the faint panel tint belongs ONLY to the input box, never the conversation history.
func TestUserBlockRailNoTint(t *testing.T) {
	th := New("aztec", aztecPalette)
	st := th.Style("userBlock")
	// NO background fill (the tint was removed — it read as an off surface in history).
	if _, plain := st.GetBackground().(lipgloss.NoColor); !plain {
		t.Errorf("userBlock must carry NO background tint, got %v", st.GetBackground())
	}
	// The gold rail survives: a left border in the user colour.
	if !st.GetBorderLeft() {
		t.Error("userBlock must keep its left border (the gold rail)")
	}
	if got := st.GetBorderLeftForeground(); got != col(aztecPalette.User) {
		t.Errorf("userBlock left-border colour = %v, want the gold User %q", got, aztecPalette.User)
	}
}

// TestWarningAndDangerPillSlots pins decisions 3+5 (recut): the "warning" slot is
// inline coloured+bold text (warning fg, NO background fill), while "dangerPill" is a
// filled, padded chip with FIXED, theme-INDEPENDENT alarm colours (alarm-red bg,
// near-white fg) — NOT palette-derived. The two must be visually distinct — the pill is
// the louder cue, and danger reads identically in every theme.
func TestWarningAndDangerPillSlots(t *testing.T) {
	th := New("aztec", aztecPalette)

	warn := th.Style("warning")
	if got := warn.GetForeground(); got != col(aztecPalette.Warning) {
		t.Errorf("warning fg = %v, want Warning %q", got, aztecPalette.Warning)
	}
	// Inline text: no filled background and no padding frame (distinct from the pill).
	if _, filled := warn.GetBackground().(lipgloss.NoColor); !filled {
		t.Errorf("warning must be inline text (no background fill), got %v", warn.GetBackground())
	}
	if got := warn.GetHorizontalFrameSize(); got != 0 {
		t.Errorf("warning horizontal frame = %d, want 0 (inline, not a pill)", got)
	}

	pill := th.Style("dangerPill")
	// FIXED alarm colours — NOT the theme's Error / Bg (a safety affordance, not themed).
	if got := pill.GetBackground(); got != col(dangerPillBg) {
		t.Errorf("dangerPill background = %v, want the FIXED alarm red %q", got, dangerPillBg)
	}
	if got := pill.GetForeground(); got != col(dangerPillFg) {
		t.Errorf("dangerPill fg = %v, want the FIXED near-white %q", got, dangerPillFg)
	}
	// The pill's horizontal frame adds the 2 cells the width math in fitHeader
	// compensates for.
	if got := pill.GetHorizontalFrameSize(); got != 2 {
		t.Errorf("dangerPill horizontal frame = %d, want 2 (Padding(0,1))", got)
	}
}

// TestDangerPillThemeIndependent proves the dangerPill colours do NOT track the palette:
// two DIFFERENT themes (different Error / Bg) must render the pill with the SAME fixed
// alarm-red bg + near-white fg. This is the safety-affordance guarantee — danger reads
// the same everywhere — and the guard against a regression back to palette-derived
// Error/Bg.
func TestDangerPillThemeIndependent(t *testing.T) {
	a := New("aztec", aztecPalette)
	// A contrived second palette with a deliberately different Error and Bg.
	alt := aztecPalette
	alt.Error = "#00FF00"
	alt.Bg = "#123456"
	b := New("other", alt)

	pa, pb := a.Style("dangerPill"), b.Style("dangerPill")
	if pa.GetBackground() != pb.GetBackground() {
		t.Errorf("dangerPill bg must be theme-independent: %v vs %v", pa.GetBackground(), pb.GetBackground())
	}
	if pa.GetForeground() != pb.GetForeground() {
		t.Errorf("dangerPill fg must be theme-independent: %v vs %v", pa.GetForeground(), pb.GetForeground())
	}
	if pa.GetBackground() != col(dangerPillBg) {
		t.Errorf("dangerPill bg = %v, want fixed alarm red %q (not the theme Error)", pa.GetBackground(), dangerPillBg)
	}
}

// TestGlamourCodeDeEmphasised pins decision 6: inline code recedes — its
// foreground is the (palette-derived) quote slot and Faint is set, over the
// element background. No hardcoded hex.
func TestGlamourCodeDeEmphasised(t *testing.T) {
	th := New("aztec", aztecPalette)
	code := th.GlamourStyle().Code.StylePrimitive
	if code.Color == nil || *code.Color != aztecPalette.MdQuote {
		t.Errorf("inline code colour = %v, want the recede MdQuote slot %q", code.Color, aztecPalette.MdQuote)
	}
	if code.Faint == nil || !*code.Faint {
		t.Error("inline code must be Faint (de-emphasised)")
	}
	if code.BackgroundColor == nil || *code.BackgroundColor != aztecPalette.BgElement {
		t.Errorf("inline code background = %v, want BgElement %q (unchanged)", code.BackgroundColor, aztecPalette.BgElement)
	}
}

// TestParseThemeMergeOverBase asserts a partial palette JSON merges over the
// Aztec base: overridden slots change, untouched slots inherit Aztec.
func TestParseThemeMergeOverBase(t *testing.T) {
	raw := []byte(`{"name":"Custom","palette":{"accent":"#FF00FF","error":"#000000"}}`)
	th, err := ParseTheme(raw)
	if err != nil {
		t.Fatalf("ParseTheme: %v", err)
	}
	if th.Name != "custom" {
		t.Errorf("name = %q, want lowercased %q", th.Name, "custom")
	}
	if th.Palette.Accent != "#FF00FF" {
		t.Errorf("accent override = %q, want #FF00FF", th.Palette.Accent)
	}
	if th.Palette.Error != "#000000" {
		t.Errorf("error override = %q, want #000000", th.Palette.Error)
	}
	// Untouched slot must inherit Aztec.
	if th.Palette.Primary != aztecPalette.Primary {
		t.Errorf("primary = %q, want inherited Aztec %q", th.Palette.Primary, aztecPalette.Primary)
	}
	// Back-compat: a partial palette that omits the newer "selection" slot must
	// inherit the Aztec base value (the optional field never breaks an older
	// theme file).
	if th.Palette.Selection != aztecPalette.Selection {
		t.Errorf("selection = %q, want inherited Aztec %q", th.Palette.Selection, aztecPalette.Selection)
	}
}

// TestThemeJSONRoundTrip asserts a theme marshals to the fileTheme shape and
// parses back to an equal palette (after the merge, which is a no-op for a full
// palette).
func TestThemeJSONRoundTrip(t *testing.T) {
	orig := New("aztec", aztecPalette)
	b, err := json.Marshal(orig)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got, err := ParseTheme(b)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !reflect.DeepEqual(got.Palette, orig.Palette) {
		t.Errorf("round-trip palette mismatch:\n got %+v\nwant %+v", got.Palette, orig.Palette)
	}
	if got.Name != orig.Name {
		t.Errorf("round-trip name = %q, want %q", got.Name, orig.Name)
	}
}

// TestRegistryResolve covers the resolution contract: empty → default, known →
// itself, unknown → default with ok=false.
func TestRegistryResolve(t *testing.T) {
	r := NewRegistry()
	if th, ok := r.Resolve(""); !ok || th.Name != "aztec" {
		t.Errorf("Resolve(\"\") = %q,%v; want aztec,true", th.Name, ok)
	}
	if th, ok := r.Resolve("mono"); !ok || th.Name != "mono" {
		t.Errorf("Resolve(mono) = %q,%v; want mono,true", th.Name, ok)
	}
	if th, ok := r.Resolve("nope"); ok || th.Name != "aztec" {
		t.Errorf("Resolve(nope) = %q,%v; want aztec,false", th.Name, ok)
	}
}

// TestRegistryCaseInsensitive asserts Get/Resolve match regardless of the
// requested case (keys are stored lowercase), so "--theme Aztec" resolves rather
// than silently falling back to the default.
func TestRegistryCaseInsensitive(t *testing.T) {
	r := NewRegistry()
	if th, ok := r.Resolve("Aztec"); !ok || th.Name != "aztec" {
		t.Errorf("Resolve(Aztec) = %q,%v; want aztec,true", th.Name, ok)
	}
	if th, ok := r.Get("MONO"); !ok || th.Name != "mono" {
		t.Errorf("Get(MONO) = %q,%v; want mono,true", th.Name, ok)
	}
}

// TestSolarReturnsBuiltinLightTheme asserts theme.Solar() (the light-theme
// auto-detect fallback, ADR 0280) always returns the built-in "solar" theme —
// byte-identical to resolving "solar" through a fresh registry — regardless of
// any user override loaded under the same name.
func TestSolarReturnsBuiltinLightTheme(t *testing.T) {
	got := Solar()
	if got.Name != "solar" {
		t.Fatalf("Solar().Name = %q, want %q", got.Name, "solar")
	}
	if got.Palette != solarPalette {
		t.Errorf("Solar().Palette diverged from the built-in solarPalette")
	}

	// A registered override under the same name must not change Solar()'s
	// result: it always returns the compiled built-in, not whatever a registry
	// currently holds.
	r := NewRegistry()
	r.Register(New("solar", aztecPalette))
	if again := Solar(); again.Palette != solarPalette {
		t.Error("Solar() returned an overridden theme after Register(\"solar\", ...)")
	}
}

// TestGlamourStyleColoured asserts the glamour config is driven by the palette:
// heading colour matches mdHeading and a code chroma colour matches a syntax
// slot. This locks the "markdown obeys the theme" contract.
func TestGlamourStyleColoured(t *testing.T) {
	th := New("aztec", aztecPalette)
	gs := th.GlamourStyle()
	if gs.Heading.Color == nil || *gs.Heading.Color != aztecPalette.MdHeading {
		t.Errorf("heading colour = %v, want %q", gs.Heading.Color, aztecPalette.MdHeading)
	}
	if gs.CodeBlock.Chroma == nil {
		t.Fatal("chroma not set")
	}
	if c := gs.CodeBlock.Chroma.Keyword.Color; c == nil || *c != aztecPalette.SynKeyword {
		t.Errorf("chroma keyword = %v, want %q", c, aztecPalette.SynKeyword)
	}
}

// TestColorUnknownSlot asserts an unknown slot yields nil (terminal default),
// not a panic.
func TestColorUnknownSlot(t *testing.T) {
	th := New("aztec", aztecPalette)
	if c := th.Color("does-not-exist"); c != nil {
		t.Errorf("unknown slot colour = %v, want nil", c)
	}
}

// TestContrastingTextLuminance locks the luminance-driven foreground pick: a
// LIGHT selection background gets the near-black fg, a DARK one the near-white
// fg, and a malformed/empty background falls back to the light fg (the common
// dark-theme case). The two straddling hexes prove the threshold flips.
func TestContrastingTextLuminance(t *testing.T) {
	cases := []struct {
		name string
		bg   string
		want string // expected fg hex
	}{
		{"solar bg light", "#FDF6E3", selFgDark},
		{"plain light", "#f0f0f0", selFgDark},
		{"solar selection sand", "#CFC8B0", selFgDark},
		{"aztec bg dark", "#0E1311", selFgLight},
		{"aztec selection jade", "#2A4D45", selFgLight},
		{"pure black", "#000000", selFgLight},
		{"malformed", "not-a-hex", selFgLight},
		{"empty", "", selFgLight},
		// Straddle the L=0.4 threshold (grey ramp: L≈0.4 near c≈0.72): #999999
		// (L≈0.32) sits just below → light fg; #BBBBBB (L≈0.49) just above → dark
		// fg. This is the flip point.
		{"just below threshold", "#999999", selFgLight},
		{"just above threshold", "#BBBBBB", selFgDark},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := contrastingText(c.bg)
			want := col(c.want)
			if got != want {
				t.Errorf("contrastingText(%q) = %v, want %v", c.bg, got, want)
			}
		})
	}
}

// TestSelectionStyleIsSolidBlock asserts the compiled "selection" style is a
// solid flat block — a non-nil Background AND Foreground, NOT reverse video — and
// that the foreground obeys the luminance rule per theme (solar's light block →
// dark fg; aztec/mono's dark block → light fg). The rendered probe must carry a
// background SGR (48;…), never the reverse SGR (\x1b[7m).
func TestSelectionStyleIsSolidBlock(t *testing.T) {
	cases := []struct {
		theme  string
		pal    Palette
		wantFg string
	}{
		{"aztec", aztecPalette, selFgLight},
		{"mono", monoPalette, selFgLight},
		{"solar", solarPalette, selFgDark},
	}
	for _, c := range cases {
		t.Run(c.theme, func(t *testing.T) {
			th := New(c.theme, c.pal)
			st := th.Style("selection")
			if st.GetBackground() == nil {
				t.Errorf("%s selection: background is nil, want a solid block bg", c.theme)
			}
			if st.GetForeground() == nil {
				t.Errorf("%s selection: foreground is nil, want a computed fg", c.theme)
			}
			if fg := st.GetForeground(); fg != col(c.wantFg) {
				t.Errorf("%s selection fg = %v, want %v", c.theme, fg, col(c.wantFg))
			}
			probe := st.Render("X")
			if strings.Contains(probe, "\x1b[7m") {
				t.Errorf("%s selection must NOT be reverse video, got %q", c.theme, probe)
			}
			// A background SGR is "48;" (truecolor) in the rendered output.
			if !strings.Contains(probe, "48;") {
				t.Errorf("%s selection should render a background SGR, got %q", c.theme, probe)
			}
		})
	}
}

// TestSelectionFallbackToAccent asserts that a palette with NO selection slot
// derives the block background from Accent, with a luminance-correct fg. This is
// the partial-theme path: a user theme that never sets "selection" still gets a
// legible block.
func TestSelectionFallbackToAccent(t *testing.T) {
	p := aztecPalette
	p.Selection = "" // force the accent fallback
	th := New("partial", p)
	st := th.Style("selection")
	if st.GetBackground() != col(p.Accent) {
		t.Errorf("fallback selection bg = %v, want accent %v", st.GetBackground(), col(p.Accent))
	}
	// aztecGold (#E9B949) is a light-ish accent → dark fg expected.
	if fg, want := st.GetForeground(), contrastingText(p.Accent); fg != want {
		t.Errorf("fallback selection fg = %v, want luminance-of-accent %v", fg, want)
	}
}
