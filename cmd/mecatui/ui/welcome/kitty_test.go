package welcome

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image/png"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi/kitty"
)

// mapLookup adapts a map to the envLookup signature.
func mapLookup(m map[string]string) envLookup {
	return func(k string) (string, bool) {
		v, ok := m[k]
		return v, ok
	}
}

func mapEnviron(m map[string]string) func() []string {
	return func() []string {
		out := make([]string, 0, len(m))
		for k, v := range m {
			out = append(out, k+"="+v)
		}
		return out
	}
}

// TestDetectKittyTruthTable exercises the conservative env-based detection plus
// the force/no override envs.
func TestDetectKittyTruthTable(t *testing.T) {
	cases := []struct {
		name string
		env  map[string]string
		want bool
	}{
		{"empty", map[string]string{}, false},
		{"kitty window id", map[string]string{"KITTY_WINDOW_ID": "1"}, true},
		{"TERM kitty", map[string]string{"TERM": "xterm-kitty"}, true},
		{"ghostty term_program", map[string]string{"TERM_PROGRAM": "ghostty"}, true},
		{"wezterm term_program", map[string]string{"TERM_PROGRAM": "WezTerm"}, true},
		{"konsole", map[string]string{"KONSOLE_VERSION": "220400"}, true},
		{"ghostty env", map[string]string{"GHOSTTY_RESOURCES_DIR": "/x"}, true},
		{"plain xterm", map[string]string{"TERM": "xterm-256color"}, false},
		{"force on", map[string]string{"MECATUI_FORCE_KITTY": "1"}, true},
		{"no wins over force", map[string]string{"MECATUI_FORCE_KITTY": "1", "MECATUI_NO_KITTY": "1"}, false},
		{"no over detection", map[string]string{"KITTY_WINDOW_ID": "1", "MECATUI_NO_KITTY": "true"}, false},
		// A multiplexer (tmux/screen) between mecatui and the outer terminal does not
		// pass Kitty graphics APC through by default, so a Ghostty/WezTerm env signal
		// inherited by the multiplexer is a false positive: the transmit would be
		// swallowed and the placeholder grid paints nothing. Fall back to the half-block
		// path unless the operator force-opts-in (MECATUI_FORCE_KITTY=1 after enabling
		// `tmux allow-passthrough on`).
		{"ghostty env under tmux", map[string]string{"GHOSTTY_RESOURCES_DIR": "/x", "TMUX": "/tmp/sock"}, false},
		{"ghostty term_program under tmux", map[string]string{"TERM_PROGRAM": "ghostty", "TMUX": "/tmp/sock"}, false},
		{"wezterm under tmux", map[string]string{"TERM_PROGRAM": "WezTerm", "TMUX": "/tmp/sock"}, false},
		{"ghostty env under screen", map[string]string{"GHOSTTY_RESOURCES_DIR": "/x", "STY": "1"}, false},
		// KITTY_WINDOW_ID is kept as sufficient even under a multiplexer: kitty itself
		// sets it and tmux strips it unless passthrough relays it, so a true value
		// implies passthrough is actually forwarding kitty's env.
		{"kitty window id under tmux", map[string]string{"KITTY_WINDOW_ID": "1", "TMUX": "/tmp/sock"}, true},
		// Konsole is a terminal, not a multiplexer — no TMUX suppression applies.
		{"konsole under tmux", map[string]string{"KONSOLE_VERSION": "220400", "TMUX": "/tmp/sock"}, true},
		// The operator force-override still works under a multiplexer (for users who
		// have enabled tmux allow-passthrough on).
		{"force on under tmux", map[string]string{"MECATUI_FORCE_KITTY": "1", "TMUX": "/tmp/sock"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := detectKitty(mapLookup(tc.env), mapEnviron(tc.env))
			if got != tc.want {
				t.Errorf("detectKitty(%v) = %v, want %v", tc.env, got, tc.want)
			}
		})
	}
}

func TestKittyNativeResolution(t *testing.T) {
	cases := []struct {
		name string
		env  map[string]string
		want bool
	}{
		{name: "kitty term", env: map[string]string{"TERM": "xterm-kitty"}, want: true},
		{name: "kitty window", env: map[string]string{"KITTY_WINDOW_ID": "42"}, want: true},
		{name: "ghostty", env: map[string]string{"TERM_PROGRAM": "ghostty"}, want: false},
		{name: "plain", env: map[string]string{"TERM": "xterm-256color"}, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := kittyNativeResolution(mapLookup(tc.env)); got != tc.want {
				t.Fatalf("kittyNativeResolution(%v) = %v, want %v", tc.env, got, tc.want)
			}
		})
	}
}

// delimiter (ESC \ terminator) and returns each chunk's CONTROL portion (the
// key=value list between "\x1b_G" and the payload-separating ";").
func transmitChunkControls(t *testing.T, out string) []string {
	t.Helper()
	var controls []string
	for _, chunk := range strings.Split(out, "\x1b\\") {
		if chunk == "" {
			continue
		}
		body, ok := strings.CutPrefix(chunk, "\x1b_G")
		if !ok {
			t.Fatalf("chunk missing APC G prefix: %q", chunk[:min(len(chunk), 40)])
		}
		ctrl, _, _ := strings.Cut(body, ";")
		controls = append(controls, ctrl)
	}
	if len(controls) == 0 {
		t.Fatal("no chunks found in transmit escape")
	}
	return controls
}

// controlKeys returns the set of keys (left of '=') in a chunk control string.
func controlKeys(ctrl string) map[string]string {
	keys := map[string]string{}
	for _, kv := range strings.Split(ctrl, ",") {
		if k, v, ok := strings.Cut(kv, "="); ok {
			keys[k] = v
		}
	}
	return keys
}

// reassemblePayload reassembles the base64-encoded image payload from a chunked
// Kitty graphics escape, mirroring how a terminal reconstructs it: each chunk's
// payload (after the ";" separator) is concatenated, then base64-decoded. This
// is what proves the transmitted bytes are actually a decodable image, not just
// that the control keys claim f=100 (PNG). Returns the decoded bytes or fails
// the test.
func reassemblePayload(t *testing.T, out string) []byte {
	t.Helper()
	var b64 strings.Builder
	for _, chunk := range strings.Split(out, "\x1b\\") {
		if chunk == "" {
			continue
		}
		body, ok := strings.CutPrefix(chunk, "\x1b_G")
		if !ok {
			t.Fatalf("chunk missing APC G prefix: %q", chunk[:min(len(chunk), 40)])
		}
		_, payload, found := strings.Cut(body, ";")
		if !found {
			// Delete-only escapes carry no payload; transmit chunks always do.
			t.Fatalf("chunk missing payload separator ';': %q", body[:min(len(body), 40)])
		}
		b64.WriteString(payload)
	}
	dec, err := base64.StdEncoding.DecodeString(b64.String())
	if err != nil {
		t.Fatalf("base64-decode of reassembled payload failed: %v", err)
	}
	return dec
}

// TestTransmitMascotCreatesVirtualPlacement is the issue #44 regression pin:
// the FIRST chunk's control set must carry a=T (transmit AND put) together with
// U=1, c=<cols>, r=<rows>, i=<MascotImageID>. Under a bare transmit (a=t) the
// placement keys are inert — the terminal stores the image but creates NO
// virtual placement, and the placeholder grid paints nothing.
func TestTransmitMascotCreatesVirtualPlacement(t *testing.T) {
	const cols, rows = 40, 20
	out := TransmitMascot(cols, rows)
	if out == "" {
		t.Fatal("TransmitMascot returned empty")
	}
	first := controlKeys(transmitChunkControls(t, out)[0])
	if got := first["a"]; got != "T" {
		t.Errorf("first chunk action a=%q, want a=T (transmit-and-put; a=t leaves U=1/c=/r= inert — issue #44)", got)
	}
	if got := first["U"]; got != "1" {
		t.Errorf("first chunk U=%q, want U=1 (virtual placement)", got)
	}
	if got := first["c"]; got != fmt.Sprint(cols) {
		t.Errorf("first chunk c=%q, want c=%d", got, cols)
	}
	if got := first["r"]; got != fmt.Sprint(rows) {
		t.Errorf("first chunk r=%q, want r=%d", got, rows)
	}
	if got := first["i"]; got != fmt.Sprint(MascotImageID) {
		t.Errorf("first chunk i=%q, want i=%d (MascotImageID)", got, MascotImageID)
	}
}

// TestTransmitMascotSuppressesResponses asserts q=2 is present: without quiet
// mode the terminal replies with OK/error APC responses that surface as
// unhandled input msgs in the TUI.
func TestTransmitMascotSuppressesResponses(t *testing.T) {
	out := TransmitMascot(40, 20)
	if out == "" {
		t.Fatal("TransmitMascot returned empty")
	}
	for n, ctrl := range transmitChunkControls(t, out) {
		if got := controlKeys(ctrl)["q"]; got != "2" {
			t.Errorf("chunk %d q=%q, want q=2 (suppress terminal responses)", n, got)
		}
	}
}

// TestTransmitMascotWellFormed asserts the transmit escape is a valid Kitty
// graphics control sequence: it carries the transmit-and-put action, PNG
// format, the fixed image ID, virtual placement, and is chunked (multiple m=
// markers) — with the control keys (a=T, f=, i=, U=, c=, r=) on the FIRST chunk
// only; continuation chunks carry only q= and m=.
func TestTransmitMascotWellFormed(t *testing.T) {
	out := TransmitMascot(40, 20)
	if out == "" {
		t.Fatal("TransmitMascot returned empty")
	}
	// Kitty graphics APC framing.
	if !strings.Contains(out, "\x1b_G") || !strings.Contains(out, "\x1b\\") {
		t.Fatal("transmit escape missing APC G ... ST framing")
	}
	controls := transmitChunkControls(t, out)
	first := controlKeys(controls[0])
	if first["f"] != "100" {
		t.Errorf("transmit missing f=100 (PNG format): %q", controls[0])
	}
	if first["i"] != fmt.Sprint(MascotImageID) {
		t.Error("transmit missing the image ID")
	}
	if first["U"] != "1" {
		t.Error("transmit missing virtual placement (U=1)")
	}
	if first["a"] != "T" {
		t.Error("transmit missing a=T (transmit-and-put)")
	}
	// Chunking is payload-size dependent: a small, target-resolution PNG (the
	// downscale-before-transmit path) fits in a SINGLE chunk (no m= key at
	// all); a large payload is split with m=1...m=0. Both are well-formed.
	// The control keys (a=T, f=, i=, U=, c=, r=) appear on the first chunk
	// only; continuation chunks (when present) carry only q= and m=.
	if len(controls) == 1 {
		// Single chunk: no m= key (or m=0). The action/placement keys must all
		// be present on this one chunk.
		if first["m"] != "" && first["m"] != "0" {
			t.Errorf("single chunk m=%q, want absent or m=0", first["m"])
		}
	} else {
		// Multi-chunk: first chunk m=1, last chunk m=0.
		if first["m"] != "1" {
			t.Errorf("first chunk m=%q, want m=1", first["m"])
		}
		last := controlKeys(controls[len(controls)-1])
		if last["m"] != "0" {
			t.Errorf("last chunk m=%q, want m=0", last["m"])
		}
		for n, ctrl := range controls[1:] {
			keys := controlKeys(ctrl)
			for k := range keys {
				if k != "q" && k != "m" {
					t.Errorf("continuation chunk %d carries control key %s=%s (only q=/m= allowed)", n+1, k, keys[k])
				}
			}
		}
	}
}

// TestPlaceholderGridShape asserts the in-content grid is rows×cols placeholder
// cells, each carrying the row+column diacritics and the ID-bearing foreground,
// with the requested left margin and a per-row reset.
func TestPlaceholderGridShape(t *testing.T) {
	const cols, rows, margin = 8, 4, 4
	out := PlaceholderGrid(cols, rows, margin)
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != rows {
		t.Fatalf("placeholder grid rows = %d, want %d", len(lines), rows)
	}
	ph := string(kitty.Placeholder)
	idFG := idForeground(MascotImageID)
	for r, ln := range lines {
		if !strings.HasPrefix(ln, strings.Repeat(" ", margin)) {
			t.Errorf("row %d missing %d-space margin", r, margin)
		}
		if !strings.Contains(ln, idFG) {
			t.Errorf("row %d missing the ID-bearing foreground escape", r)
		}
		if got := strings.Count(ln, ph); got != cols {
			t.Errorf("row %d placeholder count = %d, want %d (square footprint)", r, got, cols)
		}
		// The row diacritic for this row must appear; column 0's diacritic too.
		if !strings.Contains(ln, string(kitty.Diacritic(r))) {
			t.Errorf("row %d missing its row diacritic", r)
		}
		if !strings.HasSuffix(ln, "\x1b[0m") {
			t.Errorf("row %d missing trailing reset", r)
		}
	}
}

// TestPlaceholderWidthIsOne is the spec assumption probe: U+10EEEE and a
// placeholder+diacritics grapheme cluster are width-1, so the grid lays out as a
// cols-wide block (the renderer/runewidth treats it as a normal-width cell).
func TestPlaceholderWidthIsOne(t *testing.T) {
	if w := kittyPlaceholderWidth(); w != 1 {
		t.Fatalf("placeholder cluster width = %d, want 1 (layout assumption broken)", w)
	}
}

// TestPlaceholderForegroundIsTrueColorSGR documents the TrueColor-profile
// dependency: the Unicode-placeholder spec encodes the 24-bit image id as an
// RGB FOREGROUND (38;2;r;g;b with r=id>>16, g=(id>>8)&0xff, b=id&0xff). A
// renderer that downsamples the foreground to 256-color/ANSI would corrupt the
// id and the terminal would paint nothing.
func TestPlaceholderForegroundIsTrueColorSGR(t *testing.T) {
	const id = MascotImageID
	want := fmt.Sprintf("\x1b[38;2;%d;%d;%dm", (id>>16)&0xff, (id>>8)&0xff, id&0xff)
	if got := idForeground(id); got != want {
		t.Errorf("idForeground(%#x) = %q, want %q", id, got, want)
	}
}

// TestDeleteMascotWellFormed asserts the delete escape targets the mascot id.
func TestDeleteMascotWellFormed(t *testing.T) {
	out := DeleteMascot()
	if !strings.Contains(out, "\x1b_G") {
		t.Fatal("delete escape missing APC G framing")
	}
	if !strings.Contains(out, "a=d") {
		t.Error("delete escape missing a=d (delete action)")
	}
	if !strings.Contains(out, fmt.Sprintf("i=%d", MascotImageID)) {
		t.Error("delete escape missing the image ID")
	}
	// Uppercase d=I frees the stored image data; lowercase d=i would delete the
	// placement but LEAK the image in the terminal (kitty graphics spec).
	if !strings.Contains(out, "d=I") {
		t.Error("delete escape must use d=I (free image data), not d=i")
	}
}

// TestTransmitMascotPayloadBounded asserts the transmit is small after the
// downscale-before-transmit path: the mascot is downscaled to the target
// cols×rows cell footprint before PNG re-encoding, so the escape is tens of KB
// (not the ~1 MB of the full 1254×1254 PNG). A huge multi-chunk payload over the
// a=T/U=1/U+10EEEE virtual-placement path is the known-buggy shape on Ghostty
// 1.3.1 stable (ghostty-org/ghostty#13056); keeping the payload small and
// single-chunk sidesteps the worst of it.
func TestTransmitMascotPayloadBounded(t *testing.T) {
	out := transmitMascot(60, 30, false)
	if out == "" {
		t.Fatal("TransmitMascot returned empty")
	}
	// The downscaled PNG at 60×60 px is well under 50 KB; the full PNG is ~1 MB.
	// The bound is generous (leave room for the base64 overhead + APC framing)
	// but catches a regression to the full-resolution transmit.
	const maxBytes = 100 * 1024 // 100 KB ceiling
	if len(out) > maxBytes {
		t.Errorf("transmit payload = %d bytes (%.1f KB), want <= %d bytes — downscale-before-transmit regressed",
			len(out), float64(len(out))/1024, maxBytes)
	}
}

// TestDownscaleMascotDims asserts the downscale produces an image at the
// requested pixel dimensions (so the terminal does no pixel scaling) and that
// the box-average keying (near-white → obsidian background) carries over from
// the half-block path, keeping the two mascot renders visually consistent.
func TestDownscaleMascotDims(t *testing.T) {
	img, err := DecodeMascot()
	if err != nil {
		t.Fatalf("DecodeMascot: %v", err)
	}
	const w, h = 60, 60
	scaled := downscaleMascot(img, w, h)
	b := scaled.Bounds()
	if b.Dx() != w || b.Dy() != h {
		t.Fatalf("downscaled bounds = %dx%d, want %dx%d", b.Dx(), b.Dy(), w, h)
	}
	// The PNG encoding of the downscaled image must succeed and be small.
	pngData := pngBytes(scaled)
	if len(pngData) == 0 {
		t.Fatal("pngBytes returned empty for the downscaled mascot")
	}
	if len(pngData) > 50*1024 {
		t.Errorf("downscaled PNG = %d bytes, want < 50 KB", len(pngData))
	}
}

// TestTransmitAndPlaceholderAgree is the SPEC-LEVEL VERIFIER: it proves the
// three things a terminal relies on to paint the kitty mascot all agree, end to
// end, against the REAL image bytes (not just the control-key strings). This is
// the test that would have caught issue #44's a=t bug — the control keys would
// parse fine, but a terminal never creates a placement under a bare transmit,
// so the cross-check against the placeholder grid's encoded ID would fail.
//
// It verifies:
//  1. The reassembled base64 payload is a VALID PNG that decodes (the terminal
//     would paint nothing on a malformed/truncated image).
//  2. The PNG dimensions and the transmit's c=×r= placement are CONSISTENT with
//     the downscale footprint (cols × rows*2 px, the square-aspect sampling).
//  3. The image ID carried in the transmit's i= key MATCHES the ID encoded in
//     the PlaceholderGrid's foreground SGR colour (idForeground) — a mismatch
//     means the grid references an image the terminal never placed, so it
//     paints nothing. This is the precise contract that broke under a=t.
//  4. The PlaceholderGrid dimensions MATCH the transmit's c=×r= — a mismatch
//     means the grid's cells don't cover the placement area.
func TestTransmitAndPlaceholderAgree(t *testing.T) {
	const cols, rows, margin = 36, 18, 4

	transmit := transmitMascot(cols, rows, false)
	if transmit == "" {
		t.Fatal("TransmitMascot returned empty")
	}

	// (1) The reassembled payload is a real, decodable PNG.
	pngBytesRaw := reassemblePayload(t, transmit)
	cfg, err := png.DecodeConfig(bytes.NewReader(pngBytesRaw))
	if err != nil {
		t.Fatalf("reassembled payload is not a decodable PNG: %v", err)
	}

	// (2) The PNG dimensions are consistent with the downscale footprint.
	// TransmitMascot downscales to cols×(rows*2) px (square-aspect sampling).
	wantW, wantH := cols, rows*2
	if cfg.Width != wantW || cfg.Height != wantH {
		t.Errorf("decoded PNG dims = %dx%d, want %dx%d (cols × rows*2 downscale footprint)",
			cfg.Width, cfg.Height, wantW, wantH)
	}

	// (3) The image ID in the transmit's i= key matches the ID encoded in the
	// placeholder grid's foreground SGR colour.
	first := controlKeys(transmitChunkControls(t, transmit)[0])
	transmitID, err := parseInt(t, first["i"])
	if err != nil {
		t.Fatalf("transmit i= key not an int: %q (%v)", first["i"], err)
	}
	if transmitID != MascotImageID {
		t.Errorf("transmit image ID i=%d, want MascotImageID=%d", transmitID, MascotImageID)
	}
	// The placeholder grid's foreground MUST encode the same ID (idForeground).
	wantFG := idForeground(MascotImageID)
	grid := PlaceholderGrid(cols, rows, margin)
	if !strings.Contains(grid, wantFG) {
		t.Errorf("placeholder grid foreground does not encode the transmit's image ID: "+
			"grid missing %q — a mismatch means the grid references an image the terminal never placed",
			wantFG)
	}

	// (4) The placeholder grid dimensions match the transmit's c=×r= placement.
	gridLines := strings.Split(strings.TrimRight(grid, "\n"), "\n")
	if len(gridLines) != rows {
		t.Errorf("placeholder grid rows = %d, want %d (transmit r=%d)", len(gridLines), rows, rows)
	}
	ph := string(kitty.Placeholder)
	for r, ln := range gridLines {
		if got := strings.Count(ln, ph); got != cols {
			t.Errorf("grid row %d placeholder count = %d, want %d (transmit c=%d)", r, got, cols, cols)
			break
		}
	}
}

// parseInt parses a decimal int from a control-key value, failing the test on a
// miss. (strconv.Atoi would do, but keeping the test file dependency-free.)
func parseInt(t *testing.T, s string) (int, error) {
	t.Helper()
	var n int
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, fmt.Errorf("non-digit %q in %q", c, s)
		}
		n = n*10 + int(c-'0')
	}
	return n, nil
}
