package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/theme"
)

// TestTrustBadgeShowsProjectTrust asserts the header's right-aligned badge shows
// the server-reported project trust, independent of posture and permission mode:
// "trusted" in the warning style, "untrusted" muted, and no badge from an older
// server that does not report trust. No posture tier renders a badge of its own.
func TestTrustBadgeShowsProjectTrust(t *testing.T) {
	th := theme.New("aztec", theme.AztecPalette())
	cases := []struct {
		trust   string
		posture string
		slot    string // "" = no badge
	}{
		{"", postureYolo, ""},
		{client.ProjectUntrusted, postureStrict, "muted"},
		{client.ProjectUntrusted, postureYolo, "muted"},
		{client.ProjectTrusted, postureStrict, "warning"},
		{client.ProjectTrusted, postureAuto, "warning"},
	}
	for _, tc := range cases {
		m, _, _ := newTestModel(t, th)
		m = applyAll(m, tea.WindowSizeMsg{Width: 100, Height: 30},
			client.SessionReadyMsg{SessionID: "sess-test-0001", Capabilities: client.Capabilities{Posture: tc.posture, ProjectTrust: tc.trust}})
		raw := m.renderHeader()
		header := stripANSIstr(raw)
		for _, gone := range []string{"YOLO", "⚠ auto", "⚡"} {
			if strings.Contains(header, gone) {
				t.Errorf("trust %q posture %q: header must not carry a posture badge %q: %q", tc.trust, tc.posture, gone, header)
			}
		}
		if tc.slot == "" {
			if strings.Contains(header, "trusted") {
				t.Errorf("unreported trust must render no badge: %q", header)
			}
			continue
		}
		if want := th.Style(tc.slot).Render(tc.trust); !strings.Contains(raw, want) {
			t.Errorf("trust %q: header must render %q in the %q style; got %q", tc.trust, tc.trust, tc.slot, raw)
		}
	}
}

// TestTrustBadgeIsNotModeSegment guards that the badge is DISTINCT from the
// per-session mode segment: on an auto server the header carries the trust badge
// and the mode segment carries the session mode, never the posture.
func TestTrustBadgeIsNotModeSegment(t *testing.T) {
	m, _, _ := newTestModel(t, theme.New("aztec", theme.AztecPalette()))
	m = applyAll(m, tea.WindowSizeMsg{Width: 120, Height: 30},
		client.SessionReadyMsg{SessionID: "sess-test-0001", Capabilities: client.Capabilities{Posture: postureAuto, ProjectTrust: client.ProjectTrusted}})
	header := stripANSIstr(m.renderHeader())
	if !strings.Contains(header, "trusted") {
		t.Fatalf("expected the trust badge; got %q", header)
	}
	if strings.Contains(header, "mode auto") {
		t.Fatalf("posture must not leak into the mode segment; got %q", header)
	}
}

// TestTrustBadgeWithTailFitsOneRow is the fitHeader width-math guard: with the
// widest badge ("untrusted") AND a right-aligned changed-files tail, the header
// must not add a row beyond the no-badge baseline nor exceed the terminal width.
func TestTrustBadgeWithTailFitsOneRow(t *testing.T) {
	th := theme.New("aztec", theme.AztecPalette())
	mkHeader := func(trust string, w int) string {
		m, _, _ := newTestModel(t, th)
		m = applyAll(m, tea.WindowSizeMsg{Width: w, Height: 30},
			client.SessionReadyMsg{SessionID: "sess-test-0001", Capabilities: client.Capabilities{ProjectTrust: trust}})
		m.conv.recordFileChange("a.go")
		m.conv.recordFileChange("b.go")
		return m.renderHeader()
	}
	for w := 60; w <= 120; w += 5 {
		baseRows := lipgloss.Height(mkHeader("", w))
		header := mkHeader(client.ProjectUntrusted, w)
		if rows := lipgloss.Height(header); rows != baseRows {
			t.Errorf("width %d: header is %d rows, want %d (the no-badge baseline)", w, rows, baseRows)
		}
		if gotW := lipgloss.Width(header); gotW > w {
			t.Errorf("width %d: header rendered %d cols, want ≤ %d", w, gotW, w)
		}
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
		// allow-all is now session-mode-driven (not posture-driven), so the summary
		// names the ceiling and trust, not an always-on allow-all flag.
		{"strict", []string{"posture strict", "ceiling default/plan/accept-edits", "project-trust off", "allow-all is mode-driven"}},
		{"trusted", []string{"posture trusted", "project-trust on"}},
		{"auto", []string{"posture auto", "ceiling up to and including auto", "child $()/heredoc auto-run when in yolo mode (injection-defense off) off", "project-trust on"}},
		{"yolo", []string{"posture yolo", "ceiling all modes including yolo", "child $()/heredoc auto-run when in yolo mode (injection-defense off) on", "project-trust on"}},
	}
	for _, tc := range cases {
		got := postureSummary(tc.posture, "")
		for _, sub := range tc.want {
			if !strings.Contains(got, sub) {
				t.Errorf("postureSummary(%q) missing %q; got %q", tc.posture, sub, got)
			}
		}
	}
}
