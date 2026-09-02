package welcome

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi/kitty"

	"github.com/stacklok/mecatl/cmd/mecatui/theme"
)

func splashTheme() theme.Theme { return theme.New("aztec", theme.AztecPalette()) }

func splashInfo(kittyOn bool) Info {
	return Info{
		Cwd:         "/workspace",
		Model:       "mock-model",
		Provider:    "openai",
		Version:     "v9.9.9-test",
		Tagline:     "your local agentic coding harness",
		Affordances: []string{"  ?   keys & features", "  /   slash commands"},
		MemoryNote:  "  memory is on",
		FullColor:   true,
		Kitty:       kittyOn,
	}
}

// TestSplashKittyEmitsPlaceholderGrid proves the Kitty branch of Splash: with
// Info.Kitty=true the mascot is the U+10EEEE placeholder grid, NOT the half-block
// "▀" render — so the two paths are mutually exclusive and the kitty path is
// actually reachable through Splash (not only via the low-level PlaceholderGrid).
func TestSplashKittyEmitsPlaceholderGrid(t *testing.T) {
	out := Splash(splashTheme(), splashInfo(true), 100, 40)
	if !strings.Contains(out, string(kitty.Placeholder)) {
		t.Fatal("kitty Splash should emit the U+10EEEE placeholder grid")
	}
	// The half-block mascot path must be ABSENT. The half-block render interleaves an
	// SGR escape before every ▀, so a contiguous ▀-run only appears after stripping
	// SGR — check on the stripped text (the wordmark's ▀ glyphs are short, so a long
	// run is the mascot tell).
	if strings.Contains(stripSGR(out), "▀▀▀▀▀▀▀▀") {
		t.Fatal("kitty Splash must NOT also emit the half-block mascot rows")
	}
	// Sanity: the half-block path (Kitty=false) is the opposite — placeholders absent,
	// half-block run present (after SGR strip).
	half := Splash(splashTheme(), splashInfo(false), 100, 40)
	if strings.Contains(half, string(kitty.Placeholder)) {
		t.Fatal("half-block Splash must NOT emit placeholder cells")
	}
	if !strings.Contains(stripSGR(half), "▀▀▀▀▀▀▀▀") {
		t.Fatal("half-block Splash should emit the half-block mascot rows")
	}
}

// TestSplashKittyGridColumnsMatchTier asserts the placeholder grid Splash emits
// has exactly Tier(width,height) rows of Tier cols placeholders each — so the
// IN-CONTENT grid matches the footprint TransmitMascot would bake (the kitty
// virtual placement Columns/Rows), keeping the two mascot paths in lockstep.
func TestSplashKittyGridColumnsMatchTier(t *testing.T) {
	const w, height = 100, 40 // a tier-fitting size (Tier(100,40) != 0)
	wantCols, wantRows := Tier(w, height)
	if wantCols == 0 {
		t.Fatalf("test precondition: Tier(%d,%d) should fit a mascot", w, height)
	}
	out := Splash(splashTheme(), splashInfo(true), w, height)
	ph := string(kitty.Placeholder)
	var phRows, perRow int
	for _, ln := range strings.Split(out, "\n") {
		if c := strings.Count(ln, ph); c > 0 {
			phRows++
			if perRow == 0 {
				perRow = c
			} else if c != perRow {
				t.Fatalf("ragged placeholder grid: row has %d cells, expected %d", c, perRow)
			}
		}
	}
	if perRow != wantCols {
		t.Errorf("placeholder cells per row = %d, want Tier cols %d", perRow, wantCols)
	}
	if phRows != wantRows {
		t.Errorf("placeholder rows = %d, want Tier rows %d", phRows, wantRows)
	}
}

// TestTierFitsWidthAndHeight tripwires the fit-aware footprint: Tier picks the
// largest mascot whose width (cols+margin) AND height (cols/2 + essentialReserve)
// fit, returning (0,0) when none does. Values are computed straight from the
// formula (margin 4, reserve 12).
func TestTierFitsWidthAndHeight(t *testing.T) {
	cases := []struct {
		name string
		w, h int
		want int // expected cols (0 = no mascot)
	}{
		{"large fits", 64, 50, tierLargeCols},                 // 60+4<=64, 30+12<=50
		{"medium (width caps large)", 52, 40, tierMediumCols}, // 60+4>52; 48+4<=52, 24+12<=40
		{"small (width caps medium)", 40, 32, tierSmallCols},  // 48+4>40; 36+4<=40, 18+12<=32
		{"too short for any", 40, 20, 0},                      // 36 needs 18+12=30 > 20
		{"too narrow for any", 38, 60, 0},                     // 36+4=40 > 38
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cols, rows := Tier(tc.w, tc.h)
			if cols != tc.want {
				t.Errorf("Tier(%d,%d) cols = %d, want %d", tc.w, tc.h, cols, tc.want)
			}
			if rows != cols/2 {
				t.Errorf("Tier(%d,%d) rows = %d, want cols/2 = %d", tc.w, tc.h, rows, cols/2)
			}
		})
	}
}

// TestSplashFitsHeightBudget is the core regression guard for the clip bug: across
// a wide range of heights the rendered body must never exceed height-cardChrome
// (the room centerCard leaves), the title must always survive, and the mascot/info
// degrade gracefully (tall: mascot + full info; short: mascot shrinks/drops but the
// wordmark + title + hint remain).
func TestSplashFitsHeightBudget(t *testing.T) {
	const w = 100
	in := splashInfo(false)
	for _, h := range []int{12, 16, 20, 24, 30, 36, 42, 50, 60} {
		body := Splash(splashTheme(), in, w, h)
		got := lipgloss.Height(body)
		if got > h-cardChrome {
			t.Errorf("height %d: body is %d lines, exceeds budget %d (clips):\n%s",
				h, got, h-cardChrome, body)
		}
		plain := stripSGR(body)
		if !strings.Contains(plain, "Welcome to mecatui") {
			t.Errorf("height %d: title missing", h)
		}
		// The wordmark + hint are part of the always-present head above the tiny clamp.
		if h >= minSplashHeight {
			if !strings.Contains(plain, "Type a request") {
				t.Errorf("height %d: prompt hint missing", h)
			}
		}
	}
}

// TestSplashTallShowsMascotAndInfo confirms a generous height shows BOTH the mascot
// and the full info block (affordances + memory), while a constrained height drops
// the mascot but keeps the functional content (the user's reported tradeoff).
func TestSplashTallShowsMascotAndInfo(t *testing.T) {
	in := splashInfo(false) // half-block path
	tall := stripSGR(Splash(splashTheme(), in, 100, 60))
	if !strings.Contains(tall, "▀▀▀▀▀▀▀▀") {
		t.Error("tall splash should show the mascot")
	}
	if !strings.Contains(tall, "█") {
		t.Error("tall splash should show the wordmark")
	}
	if !strings.Contains(tall, "memory is on") {
		t.Error("tall splash should show the memory note")
	}

	// A height that fits the head but not the mascot: no mascot, but wordmark + title
	// + hint survive (Tier returns 0, head leads).
	short := stripSGR(Splash(splashTheme(), in, 100, minSplashHeight))
	if strings.Contains(short, "▀▀▀▀▀▀▀▀") {
		t.Error("a head-only height must drop the mascot")
	}
	if !strings.Contains(short, "█") {
		t.Error("a head-only height must keep the wordmark")
	}
	if !strings.Contains(short, "Welcome to mecatui") {
		t.Error("a head-only height must keep the title")
	}
}

// TestModelLine covers the identity-line permutations: model+provider, model-only,
// provider-only, neither.
func TestModelLine(t *testing.T) {
	cases := []struct {
		name        string
		model, prov string
		want        string
	}{
		{"both", "gpt-5", "openai", "gpt-5 · openai"},
		{"model only", "gpt-5", "", "gpt-5"},
		{"provider only", "", "openai", "openai"},
		{"neither", "", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := modelLine(Info{Model: tc.model, Provider: tc.prov}); got != tc.want {
				t.Errorf("modelLine(model=%q,prov=%q) = %q, want %q", tc.model, tc.prov, got, tc.want)
			}
		})
	}
}

// TestSplashVersionOmittedWhenEmpty asserts a Taskfile source-build Version renders
// unchanged, while an empty Version omits the version line.
func TestSplashVersionOmittedWhenEmpty(t *testing.T) {
	// A generous height so the (low keep-priority) version line is INCLUDED by the
	// greedy fit — the omit-when-empty check below is then unambiguous.
	in := splashInfo(false)
	in.Version = "v0.0.22-28-g40a6b3fc6-dirty"
	with := Splash(splashTheme(), in, 100, 60)
	if !strings.Contains(stripSGR(with), "mecatui v0.0.22-28-g40a6b3fc6-dirty") {
		t.Error("a Taskfile source-build Version should render the version line at a generous height")
	}
	in.Version = ""
	without := stripSGR(Splash(splashTheme(), in, 100, 60))
	if strings.Contains(without, "  mecatui ") {
		t.Error("an empty Version must omit the version line")
	}
}
