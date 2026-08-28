package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/colorprofile"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/ui/welcome"
)

// splashModel builds a connected, idle, empty model at the given size with the
// given Deps overlaid (NoBanner, Version). It feeds a ColorProfileMsg so the
// wordmark gradient path is exercised when fullColor is requested.
func splashModel(t *testing.T, w, h int, fullColor, noBanner bool) Model {
	t.Helper()
	recv := &fakeRecver{gate: make(chan struct{})}
	caps := embeddedCaps()
	conv := &fakeConv{recv: recv, send: &fakeSender{}, caps: caps}
	m := newTestModelFromDeps(Deps{
		Session:      conv,
		Conv:         conv,
		Theme:        aztec(),
		Server:       "127.0.0.1:8080",
		Workspace:    "/workspace",
		Mode:         "default",
		Model:        "mock-model",
		Version:      "v9.9.9-test",
		NoBanner:     noBanner,
		Ctx:          t.Context(),
		NoAltScreen:  true,
		kittyCapable: func() bool { return false },
	})
	profile := colorprofile.ANSI256
	if fullColor {
		profile = colorprofile.TrueColor
	}
	m = applyAll(m,
		tea.ColorProfileMsg{Profile: profile},
		tea.WindowSizeMsg{Width: w, Height: h},
		client.SessionReadyMsg{SessionID: "sess-test-0001", Capabilities: caps},
	)
	return m
}

// TestSplashRendersWithoutPanic drives the splash across responsive tiers and a
// tiny terminal, asserting it never panics and always carries the greppable title.
func TestSplashRendersWithoutPanic(t *testing.T) {
	cases := []struct {
		name string
		w, h int
	}{
		{"small", 36, 20},
		{"medium", 100, 30},
		{"large", 120, 50},
		{"tiny", 10, 6},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := splashModel(t, tc.w, tc.h, true, false)
			out := m.View().Content // must not panic
			plain := stripANSIstr(out)
			if !strings.Contains(plain, "Welcome to mecatui") {
				t.Errorf("%dx%d splash missing the welcome title:\n%s", tc.w, tc.h, plain)
			}
			// The tiny tier must DEGRADE: no wordmark "█", no mascot half-block run.
			// (A negative assertion only here pins the clamp branch — the larger tiers
			// DO carry these glyphs, asserted elsewhere.)
			if tc.name == "tiny" {
				if strings.Contains(plain, "█") {
					t.Errorf("tiny %dx%d must drop the wordmark glyph █:\n%s", tc.w, tc.h, plain)
				}
				if strings.Contains(plain, "▀▀▀▀") {
					t.Errorf("tiny %dx%d must drop the mascot half-blocks:\n%s", tc.w, tc.h, plain)
				}
			}
		})
	}
}

// TestSplashWordmarkSurvivesStrip asserts the block-char wordmark letters survive
// an ANSI strip at the larger tiers (the gradient is colour-only; the glyphs are
// real text), so a reviewer eyeballing a stripped golden sees "mecatl".
func TestSplashWordmarkSurvivesStrip(t *testing.T) {
	for _, h := range []int{30, 50} {
		m := splashModel(t, 100, h, true, false)
		plain := stripANSIstr(m.View().Content)
		// The wordmark block glyphs use █ — present only via the wordmark on the splash.
		if !strings.Contains(plain, "█") {
			t.Errorf("height %d: wordmark block glyphs missing after ANSI strip:\n%s", h, plain)
		}
	}
}

// TestSplashTruecolorGated proves the wordmark gradient is gated on the colour
// profile END-TO-END (onColorProfile → m.fullColor → Splash), not just via
// Wordmark(th,false) in isolation. The wordmark is isolated by the lines that
// carry the full-block "█" (the half-block mascot path never emits "█", only
// "▀"): the truecolor wordmark spans many distinct foregrounds (a gradient), the
// ANSI256 wordmark collapses to exactly ONE distinct foreground (single accent).
func TestSplashTruecolorGated(t *testing.T) {
	tc := wordmarkRegion(splashModel(t, 100, 40, true, false).View().Content)
	if distinctFG(tc) < 4 {
		t.Errorf("truecolor wordmark should be a multi-stop gradient (>=4 distinct fg), got %d", distinctFG(tc))
	}

	ansi256 := wordmarkRegion(splashModel(t, 100, 40, false, false).View().Content)
	if got := distinctFG(ansi256); got != 1 {
		t.Errorf("ANSI256 wordmark should collapse to ONE accent fg, got %d:\n%q", got, ansi256)
	}
}

// wordmarkRegion returns the concatenation of the rendered lines that carry the
// full-block "█" glyph — i.e. the wordmark rows. The half-block mascot path emits
// only "▀", so this isolates the wordmark from the mascot/info colours.
func wordmarkRegion(content string) string {
	var b strings.Builder
	for _, ln := range strings.Split(content, "\n") {
		if strings.Contains(ln, "█") {
			b.WriteString(ln)
			b.WriteByte('\n')
		}
	}
	return b.String()
}

// distinctFG counts distinct foreground SGR escapes (truecolor "38;2;…m" and
// indexed "38;5;…m") in s — the colour-variety measure the gradient assertion uses.
func distinctFG(s string) int {
	seen := map[string]struct{}{}
	for _, prefix := range []string{"38;2;", "38;5;"} {
		rest := s
		for {
			i := strings.Index(rest, prefix)
			if i < 0 {
				break
			}
			rest = rest[i+len(prefix):]
			end := strings.IndexByte(rest, 'm')
			if end < 0 {
				break
			}
			seen[prefix+rest[:end]] = struct{}{}
			rest = rest[end:]
		}
	}
	return len(seen)
}

// TestNoBannerSuppressesSplash asserts --no-banner (Deps.NoBanner) renders the
// LEGACY plain card: no mascot half-blocks, no wordmark block glyphs, but the
// prompt hint and affordances still present.
func TestNoBannerSuppressesSplash(t *testing.T) {
	m := splashModel(t, 100, 30, true, true)
	plain := stripANSIstr(m.View().Content)
	if strings.Contains(plain, "█") {
		t.Errorf("--no-banner splash must NOT carry the wordmark block glyphs:\n%s", plain)
	}
	if strings.Contains(plain, "▀▀▀▀▀▀▀▀") {
		t.Errorf("--no-banner splash must NOT carry the mascot half-blocks:\n%s", plain)
	}
	if !strings.Contains(plain, "Welcome to mecatui") {
		t.Errorf("--no-banner splash should still carry the title:\n%s", plain)
	}
	if !strings.Contains(plain, "Type a request below and press enter.") {
		t.Errorf("--no-banner splash should still carry the prompt hint:\n%s", plain)
	}
	if !strings.Contains(plain, "slash commands") {
		t.Errorf("--no-banner splash should still carry the affordance rows:\n%s", plain)
	}
}

// TestSplashShowsProvider asserts the active selection's provider is rendered on
// the identity line ("model · provider") when set — renderZeroState now threads
// m.createModelSelection.ProviderID into welcome.Info.Provider.
func TestSplashShowsProvider(t *testing.T) {
	recv := &fakeRecver{gate: make(chan struct{})}
	caps := embeddedCaps()
	conv := &fakeConv{recv: recv, send: &fakeSender{}, caps: caps}
	m := newTestModelFromDeps(Deps{
		Session: conv, Conv: conv, Theme: aztec(),
		Server: "127.0.0.1:8080", Workspace: "/workspace", Mode: "default",
		// A persisted selection seeds m.createModelSelection (ProviderID + ModelID).
		InitialModel: client.ModelSelection{ProviderID: "anthropic", ModelID: "claude-opus-4"},
		Ctx:          t.Context(), NoAltScreen: true,
		kittyCapable: func() bool { return false },
	})
	// A tall terminal so the (low keep-priority) model·provider line is included by
	// the greedy fit — on a short terminal it is correctly traded away for the head.
	m = applyAll(m,
		tea.WindowSizeMsg{Width: 100, Height: 70},
		client.SessionReadyMsg{SessionID: "sess-test-0001", Capabilities: caps},
	)
	plain := stripANSIstr(m.renderZeroState())
	if !strings.Contains(plain, "claude-opus-4 · anthropic") {
		t.Errorf("splash should show 'model · provider' when a selection is set:\n%s", plain)
	}
}

// TestKittyTransmitLifecycle proves maybeKittyTransmit's lifecycle on a (forced)
// kitty-capable terminal: it activates after connect at the height-derived tier,
// does NOT re-transmit on a same-tier resize, DOES re-fire on a tier-crossing
// resize, and never activates under NoBanner. Asserted on model fields, not output.
func TestKittyTransmitLifecycle(t *testing.T) {

	newM := func(noBanner bool) Model {
		recv := &fakeRecver{gate: make(chan struct{})}
		caps := embeddedCaps()
		conv := &fakeConv{recv: recv, send: &fakeSender{}, caps: caps}
		return newTestModelFromDeps(Deps{
			Session: conv, Conv: conv, Theme: aztec(),
			Server: "127.0.0.1:8080", Workspace: "/workspace", Mode: "default",
			NoBanner: noBanner, Ctx: t.Context(), NoAltScreen: true,
			kittyCapable: func() bool { return true },
		})
	}

	// (a) activates after connect at the (width,height) tier — and the transmit's
	// footprint matches welcome.Tier(width, vpHeight), the SAME function Splash uses.
	m := newM(false)
	m = applyAll(m,
		tea.WindowSizeMsg{Width: 120, Height: 70},
		client.SessionReadyMsg{SessionID: "s1", Capabilities: embeddedCaps()},
	)
	if !m.kittyActive {
		t.Fatal("kitty should be active after connect on a forced-kitty terminal")
	}
	wantCols, _ := welcome.Tier(m.width, m.vp.Height())
	if wantCols == 0 {
		t.Fatalf("test precondition: a 120x70 terminal should fit a mascot (vp=%d)", m.vp.Height())
	}
	if m.kittyTier != wantCols {
		t.Fatalf("kittyTier = %d, want %d (Tier of width %d, vp %d)", m.kittyTier, wantCols, m.width, m.vp.Height())
	}

	// (b) a SAME-tier resize (still wide+tall enough for the same cols) must NOT
	// re-transmit (tier unchanged, cmd nil). 120→125 keeps the large tier.
	prevTier := m.kittyTier
	mm, cmd := m.Update(tea.WindowSizeMsg{Width: 125, Height: 70})
	m = mm.(Model)
	if m.kittyTier != prevTier {
		t.Errorf("same-tier resize changed kittyTier %d→%d", prevTier, m.kittyTier)
	}
	if cmd != nil {
		t.Error("same-tier resize should not fire a (re-)transmit cmd")
	}

	// (c) a tier-CROSSING resize (still mascot-bearing, smaller tier) re-fires and
	// updates the tier. A height that drops to a smaller-but-nonzero tier.
	mm, cmd = m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	m = mm.(Model)
	crossCols, _ := welcome.Tier(m.width, m.vp.Height())
	if crossCols == 0 {
		t.Fatalf("test precondition: 100x40 should still fit a (smaller) mascot (vp=%d)", m.vp.Height())
	}
	if m.kittyTier != crossCols {
		t.Errorf("tier-crossing resize: kittyTier = %d, want %d", m.kittyTier, crossCols)
	}
	if crossCols != prevTier && cmd == nil {
		t.Error("tier-crossing resize should fire a re-transmit cmd")
	}

	// (c2) shrinking below ANY mascot tier deactivates kitty (Tier returns 0 → no
	// image to transmit; the splash renders mascot-less).
	mm, _ = m.Update(tea.WindowSizeMsg{Width: 100, Height: 14})
	m = mm.(Model)
	if cols, _ := welcome.Tier(m.width, m.vp.Height()); cols == 0 && m.kittyActive {
		// kittyActive may stay true from a prior tier until the next transmit attempt,
		// but maybeKittyTransmit must not (re-)activate when Tier is 0. Re-fire it:
		if c := (&m).maybeKittyTransmit(); c != nil {
			t.Error("maybeKittyTransmit should not transmit when no mascot tier fits")
		}
	}

	// (d) NoBanner never activates.
	mb := newM(true)
	mb = applyAll(mb,
		tea.WindowSizeMsg{Width: 120, Height: 70},
		client.SessionReadyMsg{SessionID: "s2", Capabilities: embeddedCaps()},
	)
	if mb.kittyActive {
		t.Error("NoBanner must never activate the kitty path")
	}
}

// collectRaw recursively flattens a (possibly batched) cmd and returns the string
// payloads of every tea.RawMsg it produces — the out-of-band escapes.
func collectRaw(cmd tea.Cmd) []string {
	if cmd == nil {
		return nil
	}
	var out []string
	switch v := cmd().(type) {
	case tea.BatchMsg:
		for _, c := range v {
			out = append(out, collectRaw(c)...)
		}
	case tea.RawMsg:
		if s, ok := v.Msg.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// TestKittyDeleteOnFirstBlock proves the empty→non-empty transition retires the
// kitty mascot: the reducer emits exactly one DeleteMascot escape (via tea.Raw)
// and resets kittyActive/kittyTier, so a later /clear back to the zero-state
// re-transmits.
func TestKittyDeleteOnFirstBlock(t *testing.T) {
	recv := &fakeRecver{gate: make(chan struct{})}
	caps := embeddedCaps()
	conv := &fakeConv{recv: recv, send: &fakeSender{}, caps: caps}
	m := newTestModelFromDeps(Deps{
		Session: conv, Conv: conv, Theme: aztec(),
		Server: "127.0.0.1:8080", Workspace: "/workspace", Mode: "default",
		Ctx: t.Context(), NoAltScreen: true,
		kittyCapable: func() bool { return true },
	})
	m = applyAll(m,
		tea.WindowSizeMsg{Width: 100, Height: 50},
		client.SessionReadyMsg{SessionID: "s1", Capabilities: caps},
	)
	if !m.kittyActive {
		t.Fatal("precondition: kitty should be active on the empty zero-state")
	}

	// Type a prompt and submit — the first user block makes the conversation
	// non-empty, the splash leaves, the mascot is retired.
	m.ta.Rewrite("do something")
	mm, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = mm.(Model)

	if m.kittyActive {
		t.Error("kittyActive should reset to false after the first block")
	}
	if m.kittyTier != 0 {
		t.Errorf("kittyTier should reset to 0, got %d", m.kittyTier)
	}
	wantDel := welcome.DeleteMascot()
	var deletes int
	for _, raw := range collectRaw(cmd) {
		if raw == wantDel {
			deletes++
		}
	}
	if deletes != 1 {
		t.Errorf("expected exactly one DeleteMascot escape on the transition, got %d", deletes)
	}
}
