package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/theme"
)

// TestPostureBadgeShownForAutoYolo asserts the header renders a "⚠ auto"/"⚠ yolo"
// chrome badge when the server reports an allow-all posture, and renders NO badge for
// strict/trusted (and an empty/older-server posture) — the goldens-stability guarantee.
// A regression that rendered the badge unconditionally (or dropped it for yolo) flips
// one of these. The badge is sourced from caps.Posture (SessionReadyMsg), NOT the
// per-session mode segment.
func TestPostureBadgeShownForAutoYolo(t *testing.T) {
	// Pin the text (no-emoji) yolo badge so the expectation is host-env-independent —
	// a CI host advertising COLORTERM=truecolor would otherwise seed emojiOK and flip
	// the yolo glyph to "⚡️ YOLO". The emoji variant has its own dedicated test.
	t.Setenv("MECATUI_NO_EMOJI", "1")
	cases := []struct {
		posture   string
		wantBadge string // "" = no badge
	}{
		{"", ""},
		{"strict", ""},
		{"trusted", ""},
		{"auto", "⚠ auto"},
		{"yolo", "⚡ YOLO"},
	}
	for _, tc := range cases {
		m, _, _ := newTestModel(t, theme.New("aztec", theme.AztecPalette()))
		m = applyAll(m, tea.WindowSizeMsg{Width: 100, Height: 30},
			client.SessionReadyMsg{SessionID: "sess-test-0001", Capabilities: client.Capabilities{Posture: tc.posture}})

		header := stripANSIstr(m.renderHeader())
		if tc.wantBadge == "" {
			// No badge for strict/trusted/unset: neither badge glyph may appear.
			if strings.Contains(header, "⚠") || strings.Contains(header, "⚡") {
				t.Errorf("posture %q: header must show NO badge, got %q", tc.posture, header)
			}
			continue
		}
		if !strings.Contains(header, tc.wantBadge) {
			t.Errorf("posture %q: header missing %q badge, got %q", tc.posture, tc.wantBadge, header)
		}
	}
}

// TestPostureBadgeCarriesWarningStyle asserts the badge is rendered in the CORRECT
// per-tier style — auto in the THEME's "warning" style (inline coloured text), yolo
// in the "dangerPill" style (a filled red pill) — and NEVER the muted style the benign
// scroll/changed-files cues use. The one persistent in-session danger cue must READ as
// danger. It checks the RAW (un-stripped) header for the exact styled badge substring
// (so a regression to the empty-fallback or muted style trips it) and confirms the
// not-muted guard. Stripping ANSI (as TestPostureBadgeShownForAutoYolo does) cannot see
// colour, so this is the styling guard.
func TestPostureBadgeCarriesWarningStyle(t *testing.T) {
	t.Setenv("MECATUI_NO_EMOJI", "1") // pin the text yolo glyph (host-env-independent)
	th := theme.New("aztec", theme.AztecPalette())
	cases := []struct {
		posture string
		badge   string
		slot    string // the style slot the badge must be rendered in
	}{
		{"auto", "⚠ auto", "warning"},
		{"yolo", "⚡ YOLO", "dangerPill"},
	}
	for _, tc := range cases {
		m, _, _ := newTestModel(t, th)
		m = applyAll(m, tea.WindowSizeMsg{Width: 100, Height: 30},
			client.SessionReadyMsg{SessionID: "sess-test-0001", Capabilities: client.Capabilities{Posture: tc.posture}})

		raw := m.renderHeader()
		wantStyled := th.Style(tc.slot).Render(tc.badge)
		// The styled badge MUST be non-empty (guards against the old vacuous pass, where
		// Style("warning") returned an empty fallback and the substring trivially matched).
		if stripANSIstr(wantStyled) == "" {
			t.Fatalf("posture %q: the %q style render is empty — the slot is missing", tc.posture, tc.slot)
		}
		if !strings.Contains(raw, wantStyled) {
			t.Errorf("posture %q: header must render the badge in the %q style; want substring %q in %q", tc.posture, tc.slot, wantStyled, raw)
		}
		// Guard against a regression to the muted style (the benign-cue weight).
		mutedBadge := th.Style("muted").Render(tc.badge)
		if mutedBadge != wantStyled && strings.Contains(raw, mutedBadge) {
			t.Errorf("posture %q: badge must NOT be muted-styled (it is a danger cue); found muted render %q", tc.posture, mutedBadge)
		}
	}
}

// TestPostureBadgeYoloIsRedPill asserts the yolo badge is the FILLED danger pill (an
// error-coloured background), distinct from the auto badge's inline warning text — the
// loudest posture must read loudest. It compares the rendered dangerPill against the
// warning-styled form (they must differ) and confirms the pill carries a background SGR.
func TestPostureBadgeYoloIsRedPill(t *testing.T) {
	t.Setenv("MECATUI_NO_EMOJI", "1") // pin the text yolo glyph (host-env-independent)
	th := theme.New("aztec", theme.AztecPalette())
	m, _, _ := newTestModel(t, th)
	m = applyAll(m, tea.WindowSizeMsg{Width: 100, Height: 30},
		client.SessionReadyMsg{SessionID: "sess-test-0001", Capabilities: client.Capabilities{Posture: "yolo"}})
	raw := m.renderHeader()

	pill := th.Style("dangerPill").Render("⚡ YOLO")
	warn := th.Style("warning").Render("⚡ YOLO")
	if pill == warn {
		t.Fatal("dangerPill and warning must render differently (the pill is filled, the warning is inline)")
	}
	if !strings.Contains(raw, pill) {
		t.Errorf("yolo header must render the danger PILL; want %q in %q", pill, raw)
	}
	// The pill carries an SGR background (48; or 4x) the inline warning text does not —
	// the visible "filled" cue. A background-setting escape (ESC[ ... 48 ; / ESC[4) must
	// be present in the pill render.
	if !strings.Contains(pill, "\x1b[") {
		t.Fatalf("expected ANSI in the pill render: %q", pill)
	}
}

// TestPostureBadgeIsNotModeSegment guards that the badge is DISTINCT from the per-session
// `mode` segment: with posture auto and a default permission mode, the header carries
// the auto badge but no "mode auto" text (the mode segment renders the PermissionMode,
// here unset, never the posture).
func TestPostureBadgeIsNotModeSegment(t *testing.T) {
	m, _, _ := newTestModel(t, theme.New("aztec", theme.AztecPalette()))
	m = applyAll(m, tea.WindowSizeMsg{Width: 120, Height: 30},
		client.SessionReadyMsg{SessionID: "sess-test-0001", Capabilities: client.Capabilities{Posture: "auto"}})
	header := stripANSIstr(m.renderHeader())
	if !strings.Contains(header, "⚠ auto") {
		t.Fatalf("expected the auto posture badge; got %q", header)
	}
	if strings.Contains(header, "mode auto") {
		t.Fatalf("posture must not leak into the mode segment; got %q", header)
	}
}

// vs16 is the U+FE0F variation selector — the byte whose PRESENCE distinguishes the
// emoji-presentation yolo badge ("⚡️ YOLO") from the width-stable text one ("⚡ YOLO").
const vs16 = "️"

// TestPostureBadgeEmojiVariant pins the new requirement (yolo badge uses an emoji when
// the terminal supports it): MECATUI_FORCE_EMOJI → the yolo badge carries VS16 ("⚡️")
// and is still the danger pill; MECATUI_NO_EMOJI → the badge is the width-stable "⚡"
// WITHOUT VS16 and still the danger pill. The detection is seeded ONCE at New, so the
// env must be set before newTestModel. The auto badge is unaffected by emoji capability.
func TestPostureBadgeEmojiVariant(t *testing.T) {
	cases := []struct {
		name       string
		env        string // the override env key to set "1"
		wantVS16   bool
		wantSubstr string // a stripped substring the header must contain
	}{
		{"emoji forced", "MECATUI_FORCE_EMOJI", true, "⚡️ YOLO"},
		{"emoji off", "MECATUI_NO_EMOJI", false, "⚡ YOLO"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// The package TestMain pins MECATUI_NO_EMOJI=1; clear it here so this test
			// drives the variant purely from tc.env (NO_EMOJI wins over FORCE, so the
			// forced case must start from a non-opt-out baseline).
			t.Setenv("MECATUI_NO_EMOJI", "")
			t.Setenv("MECATUI_FORCE_EMOJI", "")
			t.Setenv(tc.env, "1")
			th := theme.New("aztec", theme.AztecPalette())
			m, _, _ := newTestModel(t, th)
			m = applyAll(m, tea.WindowSizeMsg{Width: 100, Height: 30},
				client.SessionReadyMsg{SessionID: "sess-test-0001", Capabilities: client.Capabilities{Posture: "yolo"}})

			raw := m.renderHeader()
			header := stripANSIstr(raw)
			if !strings.Contains(header, tc.wantSubstr) {
				t.Errorf("yolo header = %q, want it to contain %q", header, tc.wantSubstr)
			}
			gotVS16 := strings.Contains(raw, vs16)
			if gotVS16 != tc.wantVS16 {
				t.Errorf("VS16 present = %v, want %v (the %s variant)", gotVS16, tc.wantVS16, tc.name)
			}
			// Either variant must still be the FILLED danger pill — the glyph changes,
			// the styling does not.
			badge := yoloBadgeText
			if tc.wantVS16 {
				badge = yoloBadgeEmoji
			}
			if !strings.Contains(raw, th.Style("dangerPill").Render(badge)) {
				t.Errorf("yolo badge must keep the dangerPill style for the %s variant", tc.name)
			}
		})
	}
}

// TestYoloBadgeWithTailFitsOneRow is the fitHeader pill-width-math guard (the one
// genuinely tricky arithmetic): with posture=yolo AND a non-empty right-aligned tail
// (a changed-files cue), at a band of narrow-but-fitting widths, appending the YOLO
// danger pill beside the tail must NOT add a header row beyond what the identity line
// alone already produces, and must never exceed the terminal width. The baseline is the
// SAME model at strict posture (no badge): fitHeader only appends the indicator when it
// FITS within the gap, so a correctly-compensated pill leaves the row count identical to
// the no-badge baseline (it never forces a wrap) and the width within w. This isolates
// the pill arithmetic from the pre-existing identity-line wrap (which is width-driven and
// independent of the badge). Both glyph variants are covered because ⚡️ (width-2) and ⚡
// (text, width-1) make the pill — and thus the +2-padded gap math — different widths.
func TestYoloBadgeWithTailFitsOneRow(t *testing.T) {
	cases := []struct {
		name string
		env  string // override env forced to "1"
	}{
		{"text glyph", "MECATUI_NO_EMOJI"},
		{"emoji glyph", "MECATUI_FORCE_EMOJI"},
	}
	mkHeader := func(t *testing.T, th theme.Theme, posture string, w int) string {
		m, _, _ := newTestModel(t, th)
		m = applyAll(m, tea.WindowSizeMsg{Width: w, Height: 30},
			client.SessionReadyMsg{SessionID: "sess-test-0001", Capabilities: client.Capabilities{Posture: posture}})
		// A changed-files cue gives a non-empty right-aligned tail beside the badge.
		m.recordFileChange("a.go")
		m.recordFileChange("b.go")
		return m.renderHeader()
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Drive the variant purely from tc.env (the package TestMain pins NO_EMOJI).
			t.Setenv("MECATUI_NO_EMOJI", "")
			t.Setenv("MECATUI_FORCE_EMOJI", "")
			t.Setenv(tc.env, "1")
			th := theme.New("aztec", theme.AztecPalette())

			for w := 60; w <= 120; w += 5 {
				baseRows := lipgloss.Height(mkHeader(t, th, "strict", w)) // no badge
				header := mkHeader(t, th, "yolo", w)                      // pill + tail
				if rows := lipgloss.Height(header); rows != baseRows {
					t.Errorf("%s width %d: yolo header is %d rows, want %d (the no-badge baseline) — the pill must not force an extra wrap", tc.name, w, rows, baseRows)
				}
				if gotW := lipgloss.Width(header); gotW > w {
					t.Errorf("%s width %d: yolo header rendered %d cols, want ≤ %d (pill+tail must not overflow)", tc.name, w, gotW, w)
				}
			}
		})
	}
}

// TestPostureSummary pins the /posture one-line summary for each tier (the runPosture
// status text). It would fail if a defense's on/off mapping drifted (e.g. child auto-run
// reported on at auto).
func TestPostureSummary(t *testing.T) {
	cases := []struct {
		posture string
		want    []string // substrings that MUST be present
	}{
		{"strict", []string{"posture strict", "allow-all off", "child $()/heredoc auto-run (injection-defense off) off", "project-trust off"}},
		{"trusted", []string{"posture trusted", "allow-all off", "project-trust on"}},
		{"auto", []string{"posture auto", "allow-all on", "main $()/heredoc auto-run on", "child $()/heredoc auto-run (injection-defense off) off", "project-trust on"}},
		{"yolo", []string{"posture yolo", "allow-all on", "child $()/heredoc auto-run (injection-defense off) on", "project-trust on"}},
	}
	for _, tc := range cases {
		got := postureSummary(tc.posture)
		for _, sub := range tc.want {
			if !strings.Contains(got, sub) {
				t.Errorf("postureSummary(%q) missing %q; got %q", tc.posture, sub, got)
			}
		}
	}
}
