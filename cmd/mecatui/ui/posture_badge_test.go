package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/theme"
)

// TestPostureBadgeShownForAutoYolo asserts the header renders the auto/yolo chrome
// badge when the server reports an allow-all posture, and NO badge for strict/trusted
// (and an empty/older-server posture) — the goldens-stability guarantee. Under
// MECATUI_NO_EMOJI the yolo badge is the clean " YOLO " pill with NO lightning bolt and
// NO VS16; auto is "⚠ auto". The badge is sourced from caps.Posture (SessionReadyMsg),
// NOT the per-session mode segment.
func TestPostureBadgeShownForAutoYolo(t *testing.T) {
	cases := []struct {
		posture   string
		wantBadge string // "" = no badge
	}{
		{"", ""},
		{"strict", ""},
		{"trusted", ""},
		{"auto", "⚠ auto"},
		{"yolo", "YOLO"},
	}
	for _, tc := range cases {
		m, _, _ := newTestModel(t, theme.New("aztec", theme.AztecPalette()))
		m = applyAll(m, tea.WindowSizeMsg{Width: 100, Height: 30},
			client.SessionReadyMsg{SessionID: "sess-test-0001", Capabilities: client.Capabilities{Posture: tc.posture}})

		raw := m.renderHeader()
		header := stripANSIstr(raw)
		if tc.wantBadge == "" {
			// No badge for strict/trusted/unset: no posture glyph or word may appear.
			if strings.Contains(header, "⚠") || strings.Contains(header, "⚡") || strings.Contains(header, "YOLO") {
				t.Errorf("posture %q: header must show NO badge, got %q", tc.posture, header)
			}
			continue
		}
		if !strings.Contains(header, tc.wantBadge) {
			t.Errorf("posture %q: header missing %q badge, got %q", tc.posture, tc.wantBadge, header)
		}
		if tc.posture == "yolo" {
			// No-emoji yolo: a CLEAN pill — no bolt, no VS16.
			if strings.Contains(header, "⚡") {
				t.Errorf("no-emoji yolo badge must carry NO lightning bolt, got %q", header)
			}
			if strings.Contains(raw, vs16) {
				t.Errorf("no-emoji yolo badge must carry NO VS16, got %q", header)
			}
		}
	}
}

// TestPostureBadgeCarriesWarningStyle asserts the badge is rendered in the CORRECT
// per-tier style — auto in the THEME's "warning" style (inline coloured text), and the
// yolo pill in the "dangerPill" style (the filled alarm-red chip) — and NEVER the muted
// style the benign scroll/changed-files cues use. The one persistent in-session danger
// cue must READ as danger. It checks the RAW (un-stripped) header for the exact styled
// substring (so a regression to the empty-fallback or muted style trips it) and confirms
// the not-muted guard.
func TestPostureBadgeCarriesWarningStyle(t *testing.T) {
	th := theme.New("aztec", theme.AztecPalette())
	cases := []struct {
		posture string
		text    string // the styled segment's content
		slot    string // the style slot it must be rendered in
	}{
		{"auto", autoBadgeText, "warning"},
		{"yolo", yoloPillText, "dangerPill"},
	}
	for _, tc := range cases {
		m, _, _ := newTestModel(t, th)
		m = applyAll(m, tea.WindowSizeMsg{Width: 100, Height: 30},
			client.SessionReadyMsg{SessionID: "sess-test-0001", Capabilities: client.Capabilities{Posture: tc.posture}})

		raw := m.renderHeader()
		wantStyled := th.Style(tc.slot).Render(tc.text)
		// The styled segment MUST be non-empty (guards against an empty-fallback slot).
		if stripANSIstr(wantStyled) == "" {
			t.Fatalf("posture %q: the %q style render is empty — the slot is missing", tc.posture, tc.slot)
		}
		if !strings.Contains(raw, wantStyled) {
			t.Errorf("posture %q: header must render the badge in the %q style; want substring %q in %q", tc.posture, tc.slot, wantStyled, raw)
		}
		// Guard against a regression to the muted style (the benign-cue weight).
		mutedBadge := th.Style("muted").Render(tc.text)
		if mutedBadge != wantStyled && strings.Contains(raw, mutedBadge) {
			t.Errorf("posture %q: badge must NOT be muted-styled (it is a danger cue); found muted render %q", tc.posture, mutedBadge)
		}
	}
}

// TestPostureBadgeYoloIsRedPill asserts the yolo pill is the FILLED, FIXED-COLOUR alarm
// chip — distinct from the auto badge's inline warning text and from the theme's Error
// colour — the loudest posture must read loudest and identically across themes. It pins
// the dangerPill's theme-independent alarm-red background + near-white foreground.
func TestPostureBadgeYoloIsRedPill(t *testing.T) {
	th := theme.New("aztec", theme.AztecPalette())
	m, _, _ := newTestModel(t, th)
	m = applyAll(m, tea.WindowSizeMsg{Width: 100, Height: 30},
		client.SessionReadyMsg{SessionID: "sess-test-0001", Capabilities: client.Capabilities{Posture: "yolo"}})
	raw := m.renderHeader()

	pill := th.Style("dangerPill").Render(yoloPillText)
	warn := th.Style("warning").Render(yoloPillText)
	if pill == warn {
		t.Fatal("dangerPill and warning must render differently (the pill is filled, the warning is inline)")
	}
	if !strings.Contains(raw, pill) {
		t.Errorf("yolo header must render the danger PILL; want %q in %q", pill, raw)
	}
	// The pill carries the FIXED alarm-red background + near-white foreground RGB SGRs,
	// NOT the theme Error / Bg pair — danger is danger across every theme.
	for _, want := range []string{"\x1b[", "224;49;49", "245;245;245"} { // #E03131 / #F5F5F5 as RGB
		if !strings.Contains(pill, want) {
			t.Errorf("dangerPill render missing fixed alarm colour %q: %q", want, pill)
		}
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

// TestPostureBadgeEmojiVariant pins the yolo badge variants: an emoji-capable
// model renders a PLAIN ⚡️ bolt (with VS16) OUTSIDE the danger pill; an incapable
// model renders just the clean " YOLO " pill. The capability is injected before
// New because detection is evaluated once at construction.
func TestPostureBadgeEmojiVariant(t *testing.T) {
	cases := []struct {
		name     string
		emojiOK  bool
		wantBolt bool
	}{
		{"emoji forced", true, true},
		{"emoji off", false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			th := theme.New("aztec", theme.AztecPalette())
			m, _, _ := newTestModel(t, th, func(deps *Deps) {
				deps.emojiCapable = func() bool { return tc.emojiOK }
			})
			m = applyAll(m, tea.WindowSizeMsg{Width: 100, Height: 30},
				client.SessionReadyMsg{SessionID: "sess-test-0001", Capabilities: client.Capabilities{Posture: "yolo"}})

			raw := m.renderHeader()
			header := stripANSIstr(raw)

			// The clean pill is present (dangerPill-styled " YOLO ") in BOTH variants.
			pill := th.Style("dangerPill").Render(yoloPillText)
			if !strings.Contains(raw, pill) {
				t.Errorf("yolo header must render the clean danger pill %q in %q", pill, raw)
			}
			// The pill itself must NOT contain the bolt — the emoji is decoration OUTSIDE.
			if strings.Contains(pill, "⚡") {
				t.Fatal("the dangerPill render must not contain the lightning bolt")
			}
			if !strings.Contains(header, "YOLO") {
				t.Errorf("yolo header missing YOLO, got %q", header)
			}

			gotBolt := strings.Contains(raw, "⚡")
			gotVS16 := strings.Contains(raw, vs16)
			if gotBolt != tc.wantBolt || gotVS16 != tc.wantBolt {
				t.Errorf("%s: bolt present=%v VS16 present=%v, want both %v", tc.name, gotBolt, gotVS16, tc.wantBolt)
			}
			if tc.wantBolt {
				// The PLAIN bolt prefix appears IMMEDIATELY BEFORE the styled pill — i.e.
				// the raw header contains "<bolt prefix><styled pill>" with the bolt NOT
				// inside the pill's SGR run.
				if !strings.Contains(raw, yoloBoltPrefix+pill) {
					t.Errorf("emoji yolo: plain bolt prefix must immediately precede the pill; want %q before pill in %q", yoloBoltPrefix, raw)
				}
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
		name    string
		emojiOK bool
	}{
		{"text glyph", false},
		{"emoji glyph", true},
	}
	mkHeader := func(t *testing.T, th theme.Theme, posture string, w int, emojiOK bool) string {
		m, _, _ := newTestModel(t, th, func(deps *Deps) {
			deps.emojiCapable = func() bool { return emojiOK }
		})
		m = applyAll(m, tea.WindowSizeMsg{Width: w, Height: 30},
			client.SessionReadyMsg{SessionID: "sess-test-0001", Capabilities: client.Capabilities{Posture: posture}})
		// A changed-files cue gives a non-empty right-aligned tail beside the badge.
		m.recordFileChange("a.go")
		m.recordFileChange("b.go")
		return m.renderHeader()
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			th := theme.New("aztec", theme.AztecPalette())

			for w := 60; w <= 120; w += 5 {
				baseRows := lipgloss.Height(mkHeader(t, th, "strict", w, tc.emojiOK)) // no badge
				header := mkHeader(t, th, "yolo", w, tc.emojiOK)                      // pill + tail
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
