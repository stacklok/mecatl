package welcome

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"strings"

	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/ansi/kitty"
)

// kittyPlaceholderWidth returns the display width of one placeholder cell
// (placeholder rune + a row + a column diacritic). The Kitty spec says the
// placeholder is normal width-1; this verifies the renderer/runewidth agrees, so
// the placeholder grid lays out as a cols-wide block (a guard test reads it).
func kittyPlaceholderWidth() int {
	cluster := string(kitty.Placeholder) + string(kitty.Diacritic(0)) + string(kitty.Diacritic(0))
	return ansi.StringWidth(cluster)
}

// MascotImageID is the fixed Kitty graphics image ID the splash transmits the
// mascot under and references from the placeholder grid. A constant (not a
// per-run counter) is fine: the splash transmits at most one image, and a stable
// id lets a re-transmit on a size-tier change replace the same slot rather than
// leak placements.
const MascotImageID = 0x6D63 // 'm','c' — arbitrary but stable

// KittyCapable is the exported entry point for the ui caller: it reports whether
// the terminal is likely Kitty-graphics-capable (see kittyCapable). Conservative
// and env-based; a miss falls back to the always-correct half-block path.
func KittyCapable() bool { return kittyCapable() }

// kittyCapable reports whether the terminal is likely to support the Kitty
// graphics protocol with Unicode placeholders. It is deliberately CONSERVATIVE
// and ENV-BASED (no terminal round-trip): a miss falls back to the half-block
// path, which is always correct, so a false negative only costs resolution, never
// correctness. A false positive would paint nothing (the placeholder cells render
// as blanks), which is also non-fatal.
//
// A terminal MULTIPLEXER (tmux via $TMUX, screen via $STY) between mecatui and
// the outer terminal does NOT pass Kitty graphics APC through by default, so a
// Ghostty/WezTerm env signal inside a multiplexer is suppressed (fall back to the
// half-block path) unless the operator force-opts-in with MECATUI_FORCE_KITTY=1
// after enabling `tmux allow-passthrough on`. KITTY_WINDOW_ID and TERM=*kitty are
// kept as sufficient even under a multiplexer (kitty sets them; a false positive
// there only costs resolution).
//
// Two override envs gate testing and user control:
//   - MECATUI_FORCE_KITTY=1 forces capable (true) regardless of detection.
//   - MECATUI_NO_KITTY=1 forces incapable (false) and WINS over force.
//
// Detection (any one is sufficient): KITTY_WINDOW_ID set (kitty), TERM contains
// "kitty", TERM_PROGRAM in {ghostty, WezTerm} (NOT under a multiplexer), any
// GHOSTTY_* env present (NOT under a multiplexer), or KONSOLE_VERSION set
// (Konsole's Kitty support).
func kittyCapable() bool {
	return detectKitty(osEnvLookup, osEnviron)
}

// envLookup / environLister abstract the environment for testability — production
// passes the os-backed pair, tests pass maps.
type envLookup func(key string) (string, bool)

func osEnvLookup(key string) (string, bool) { return os.LookupEnv(key) }

func osEnviron() []string { return os.Environ() }

// detectKitty is the pure core of kittyCapable, taking its environment as
// injectable functions so the truth table is unit-testable without mutating the
// process environment.
//
// A terminal MULTIPLEXER (tmux, screen) between mecatui and the outer terminal
// does not pass Kitty graphics APC through by default — it strips the sequences
// it doesn't recognise, so the transmit never reaches the outer Ghostty/WezTerm
// and the placeholder grid paints nothing. The env-based detection below can't
// tell whether passthrough is actually enabled, so a multiplexer session is the
// documented false-positive case: we fall back to the always-correct half-block
// path unless the operator force-opts-in with MECATUI_FORCE_KITTY=1 (set this
// after enabling `tmux allow-passthrough on`). $TMUX is the reliable signal;
// $STY covers GNU screen. KITTY_WINDOW_ID survives a kitty-inside-tmux session
// only when kitty is the outer terminal AND passthrough is on, so it is kept as
// a sufficient signal (a false positive there costs resolution, never
// correctness) — but the Ghostty/WezTerm env heuristics are gated on a
// non-multiplexed session, since those vars are inherited verbatim by tmux.
func detectKitty(look envLookup, environ func() []string) bool {
	if v, ok := look("MECATUI_NO_KITTY"); ok && truthy(v) {
		return false // explicit opt-out wins over everything.
	}
	if v, ok := look("MECATUI_FORCE_KITTY"); ok && truthy(v) {
		return true
	}
	// A multiplexer layer (tmux/screen) does not pass Kitty graphics APC
	// through by default. Fall back to the half-block path unless the operator
	// force-opts-in (after enabling tmux allow-passthrough). KITTY_WINDOW_ID is
	// the one signal kept even under a multiplexer, because kitty itself sets it
	// and it is stripped by tmux unless passthrough relays it.
	_, inMux := look("TMUX")
	if !inMux {
		if _, ok := look("STY"); ok {
			inMux = true
		}
	}
	if _, ok := look("KITTY_WINDOW_ID"); ok {
		return true
	}
	if term, ok := look("TERM"); ok && strings.Contains(strings.ToLower(term), "kitty") {
		return true
	}
	if !inMux {
		switch tp, _ := look("TERM_PROGRAM"); tp {
		case "ghostty", "Ghostty", "WezTerm", "wezterm":
			return true
		}
	}
	if _, ok := look("KONSOLE_VERSION"); ok {
		return true
	}
	if !inMux {
		// Any GHOSTTY_* env present (Ghostty exports several, e.g. GHOSTTY_RESOURCES_DIR)
		// is a strong signal even when TERM_PROGRAM is unset. Gated on a non-multiplexed
		// session: tmux inherits these vars from the Ghostty shell, so under $TMUX they
		// are a false positive (the transmit would be swallowed by the multiplexer).
		for _, kv := range environ() {
			if strings.HasPrefix(kv, "GHOSTTY_") {
				return true
			}
		}
	}
	return false
}

// truthy reports whether an env value means "on".
func truthy(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// kittyNativeResolution identifies the Kitty terminal itself, as opposed to a
// terminal that merely advertises the Kitty graphics protocol. Kitty can resample
// the original mascot cleanly; the Ghostty/WezTerm compatibility path deliberately
// remains target-sized because of its virtual-placement bug.
func kittyNativeResolution(look envLookup) bool {
	if _, ok := look("KITTY_WINDOW_ID"); ok {
		return true
	}
	term, ok := look("TERM")
	return ok && strings.Contains(strings.ToLower(term), "kitty")
}

// TransmitMascot builds the OUT-OF-BAND Kitty escape that transmits the mascot
// image data to the terminal AND creates a virtual placement (a=T + U=1) under
// MascotImageID at cols×rows cells. The action MUST be transmit-and-put (a=T):
// placement keys (U=1, c=, r=) are only honoured by a put-style action — under a
// bare transmit (a=t) they are inert, the terminal stores the image with no
// placement, and the placeholder grid paints nothing (issue #44). A VIRTUAL
// placement never paints at the cursor, so a=T here still produces no visible
// output and no cursor movement. It is a control sequence: it MUST be written
// via tea.Raw (NOT placed in View content, where the ultraviolet renderer would
// parse it into cells and desync the cursor), so interleaving it with frames is
// harmless.
//
// The mascot is encoded at the source resolution for Kitty itself. Kitty's image
// scaler has the real cell-size information and can resample the 1254px source
// cleanly; pre-sampling to a 60px-ish cell footprint makes the result visibly
// pixelated (especially on high-DPI Kitty windows). Ghostty still uses the small
// target-resolution path because its virtual-placement implementation has a known
// large-transmit bug (ghostty-org/ghostty#13056). The data is chunked at
// kitty.MaxChunkSize.
// Returns "" if the image can't be decoded (caller then keeps the half-block
// path).
func TransmitMascot(cols, rows int) string {
	return transmitMascot(cols, rows, kittyNativeResolution(osEnvLookup))
}

func transmitMascot(cols, rows int, nativeResolution bool) string {
	if cols <= 0 || rows <= 0 {
		return ""
	}
	if len(rawMascotPNG()) == 0 {
		return ""
	}
	img, err := DecodeMascot()
	if err != nil {
		return ""
	}
	scaled := downscaleMascot(img, cols, rows*2)
	if nativeResolution {
		// Kitty supports PNG alpha directly. Keep the original image here so
		// transparency and anti-aliased edges reach the terminal unchanged; the
		// terminal performs the final cell-size resampling.
		scaled = img
	}
	var buf bytes.Buffer
	// f=100 (PNG), a=T (transmit AND put — a bare a=t would make U=1/c=/r= inert and
	// create no placement, issue #44), i=ID, virtual placement (U=1), c=cols r=rows so
	// the terminal scales the image into the placeholder grid footprint. q=2 suppresses
	// the terminal's OK/error responses (stray response bytes would otherwise surface
	// as unhandled input). Transmission is Direct (the image is re-encoded to PNG and
	// rides the escape, base64+chunked) — no temp file, no os/exec.
	opts := &kitty.Options{
		Action:           kitty.TransmitAndPut,
		Quite:            2, // q=2 — upstream x/ansi's (typo'd) field name for quiet mode
		Format:           kitty.PNG,
		Transmission:     kitty.Direct,
		ID:               MascotImageID,
		VirtualPlacement: true,
		Columns:          cols,
		Rows:             rows,
		Chunk:            true,
	}
	if err := kitty.EncodeGraphics(&buf, scaled, opts); err != nil {
		return ""
	}
	return buf.String()
}

// downscaleMascot box-downscales src to a w×h pixel image, matching the
// alpha-weighted area-average the half-block path uses (buildGrid), so the
// high-res kitty render and the half-block fallback sample the mascot the same
// way. The result is encoded and returned as an image.Image suitable for
// kitty.EncodeGraphics. A near-white / near-transparent box is keyed to the
// obsidian background so the dog floats on the card identically to the
// half-block path.
func downscaleMascot(src image.Image, w, h int) image.Image {
	if w <= 0 || h <= 0 {
		return src
	}
	b := src.Bounds()
	sw, sh := b.Dx(), b.Dy()
	bg := hexToRGB(obsidianBG)
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		y0 := b.Min.Y + y*sh/h
		y1 := b.Min.Y + (y+1)*sh/h
		if y1 <= y0 {
			y1 = y0 + 1
		}
		for x := 0; x < w; x++ {
			x0 := b.Min.X + x*sw/w
			x1 := b.Min.X + (x+1)*sw/w
			if x1 <= x0 {
				x1 = x0 + 1
			}
			var sr, sg, sb, sa, n float64
			for yy := y0; yy < y1; yy++ {
				for xx := x0; xx < x1; xx++ {
					r, g, bl, a := src.At(xx, yy).RGBA()
					af := float64(a) / 65535
					sr += float64(r) / 65535 * 255 * af
					sg += float64(g) / 65535 * 255 * af
					sb += float64(bl) / 65535 * 255 * af
					sa += af
					n++
				}
			}
			var c rgb
			if n == 0 || sa/n < 0.35 {
				c = bg
			} else {
				c = rgb{
					uint8(clamp(sr / sa)),
					uint8(clamp(sg / sa)),
					uint8(clamp(sb / sa)),
				}
				if isWhiteish(c) {
					c = bg
				}
			}
			dst.SetRGBA(x, y, color.RGBA{R: c.r, G: c.g, B: c.b, A: 0xFF})
		}
	}
	return dst
}

// pngBytes encodes img as PNG and returns the bytes (or nil on error), used by
// the bounded-payload test to assert the transmit is small after downscaling.
func pngBytes(img image.Image) []byte {
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil
	}
	return buf.Bytes()
}

// DeleteMascot builds the escape that deletes the mascot image (a=d, by id) so the
// terminal frees the held image once the splash leaves. The ui reducer fires it on
// the empty→non-empty (splash→first-block) transition, via tea.Raw like the
// transmit. The image would stop PAINTING regardless once the placeholder cells
// leave the content (Unicode placeholders only paint where their cells are), so
// this is about freeing the terminal-side resource and keeping the
// transmit/delete lifecycle symmetric, not about hiding a lingering image.
func DeleteMascot() string {
	// d=I (uppercase), not x/ansi's kitty.DeleteID ('i'): per the kitty graphics
	// spec the lowercase form deletes placements WITHOUT freeing the stored image
	// data — only the uppercase form frees it, which is the whole point here.
	return ansi.KittyGraphics(nil,
		fmt.Sprintf("a=%c", kitty.Delete),
		"d=I",
		fmt.Sprintf("i=%d", MascotImageID),
	)
}

// PlaceholderGrid builds the IN-CONTENT placeholder cell grid that paints the
// already-transmitted virtual image. It is rows×cols cells of kitty.Placeholder
// (U+10EEEE, a width-1 rune — verified), each cell carrying:
//   - the image ID in its FOREGROUND colour (the Kitty Unicode-placeholder spec
//     encodes the 24-bit image id as an RGB foreground: r=id>>16, g=id>>8, b=id),
//   - a ROW diacritic and a COLUMN diacritic (kitty.Diacritic(row)/(col)) so the
//     terminal knows which image cell each placeholder maps to.
//
// margin spaces indent each row to match the half-block path's placement, so the
// splash layout is identical whichever mascot path is active (same cols×rows
// footprint). The grid is what welcome.Splash emits INSTEAD of the half-block
// mascot when kitty is active.
func PlaceholderGrid(cols, rows, margin int) string {
	if cols <= 0 || rows <= 0 {
		return ""
	}
	fg := idForeground(MascotImageID)
	pad := strings.Repeat(" ", margin)
	ph := string(kitty.Placeholder)
	var b strings.Builder
	for r := 0; r < rows; r++ {
		b.WriteString(pad)
		// One SGR run per row (the id fg is constant across the row); each cell is the
		// placeholder rune followed by its row+column diacritics.
		b.WriteString(fg)
		rowDia := string(kitty.Diacritic(r))
		for c := 0; c < cols; c++ {
			b.WriteString(ph)
			b.WriteString(rowDia)
			b.WriteString(string(kitty.Diacritic(c)))
		}
		b.WriteString("\x1b[0m\n")
	}
	return b.String()
}

// idForeground returns the SGR truecolor foreground escape that encodes a 24-bit
// Kitty image id, per the Unicode-placeholder spec (the placeholder cell's
// foreground colour IS the image id). Only the low 24 bits are used.
//
//nolint:unparam // intentionally general; the one caller passes MascotImageID today.
func idForeground(id int) string {
	r := (id >> 16) & 0xFF
	g := (id >> 8) & 0xFF
	bl := id & 0xFF
	return fmt.Sprintf("\x1b[38;2;%d;%d;%dm", r, g, bl)
}
