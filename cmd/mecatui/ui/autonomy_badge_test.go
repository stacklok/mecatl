package ui

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	customization "github.com/stacklok/mecatl/cmd/mecatui/customization"
	"github.com/stacklok/mecatl/cmd/mecatui/theme"
)

// renderTemplateHeader renders the header through a template source with the
// given header template, at width, for a session in mode on a server with caps.
func renderTemplateHeader(t *testing.T, header string, width int, mode string, caps client.Capabilities) string {
	t.Helper()
	s := customization.NewTemplateSource(customization.TemplateSet{Header: customization.SurfaceTemplates{Full: header, Compact: header, Minimal: header}}, 0)
	t.Cleanup(func() { _ = s.Close(context.Background()) })
	m, _, _ := newTestModel(t, theme.New("aztec", theme.AztecPalette()))
	m.deps.StatusSource = s
	m.sessionID = "session-autonomy-badge"
	m.sessionTitle = "Autonomy"
	m.phase = phaseIdle
	m.activeMode = mode
	m.caps = caps
	updated, _ := m.Update(tea.WindowSizeMsg{Width: width, Height: 24})
	m = updated.(Model)
	updated, _ = m.update(waitStatusMessage(t, m.statusLineWaitCmd()))
	return updated.(Model).renderHeader()
}

const minimalHeaderTemplate = `<header><primary>mecatui{{if .Session.Title}} · {{elide 12 .Session.Title}}{{end}}</primary></header>`

// TestAutonomyBadgeSurvivesHeaderTemplates pins the renderer-owned auto/yolo
// indicator: a Minimal or custom header without a mode segment still shows it
// for a confirmed auto or yolo session, and no other mode shows one.
func TestAutonomyBadgeSurvivesHeaderTemplates(t *testing.T) {
	th := theme.New("aztec", theme.AztecPalette())
	pill := th.Style("dangerPill").Render(yoloPillText)
	modern := client.Capabilities{Posture: postureYolo, SessionPermissionModes: true}
	for _, tmpl := range map[string]string{
		"minimal": minimalHeaderTemplate,
		"custom":  `<header><accent>my header</accent></header>`,
	} {
		if raw := renderTemplateHeader(t, tmpl, 100, "yolo", modern); !strings.Contains(raw, pill) {
			t.Errorf("yolo session under %q must show the alarm pill: %q", tmpl, raw)
		}
		if header := stripANSIstr(renderTemplateHeader(t, tmpl, 100, "auto", modern)); !strings.Contains(header, autoBadgeText) {
			t.Errorf("auto session under %q must show %q: %q", tmpl, autoBadgeText, header)
		}
		for _, mode := range []string{"default", "plan", "accept-edits"} {
			raw := renderTemplateHeader(t, tmpl, 100, mode, modern)
			if strings.Contains(raw, "YOLO") || strings.Contains(raw, autoBadgeText) {
				t.Errorf("%s session must show no auto/yolo indicator: %q", mode, stripANSIstr(raw))
			}
		}
	}
}

// TestAutonomyBadgeShowsLegacyServerPosture pins that on a server without
// session_permission_modes the indicator follows the process-wide posture,
// which applies to a session reporting default too.
func TestAutonomyBadgeShowsLegacyServerPosture(t *testing.T) {
	th := theme.New("aztec", theme.AztecPalette())
	legacyYolo := client.Capabilities{Posture: postureYolo}
	if raw := renderTemplateHeader(t, minimalHeaderTemplate, 100, "default", legacyYolo); !strings.Contains(raw, th.Style("dangerPill").Render(yoloPillText)) {
		t.Fatalf("legacy yolo server must show the alarm pill for a default session: %q", raw)
	}
	legacyAuto := client.Capabilities{Posture: postureAuto}
	if header := stripANSIstr(renderTemplateHeader(t, minimalHeaderTemplate, 100, "default", legacyAuto)); !strings.Contains(header, autoBadgeText) {
		t.Fatalf("legacy auto server must show %q for a default session: %q", autoBadgeText, header)
	}
	legacyStrict := client.Capabilities{Posture: postureStrict}
	if raw := renderTemplateHeader(t, minimalHeaderTemplate, 100, "default", legacyStrict); strings.Contains(raw, "YOLO") || strings.Contains(raw, autoBadgeText) {
		t.Fatalf("legacy strict server must show no indicator: %q", stripANSIstr(raw))
	}
}

// TestAutonomyBadgeKeptOnNarrowHeader pins that a header too narrow for every
// indicator sheds the trust badge and the tail, and truncates the identity
// line, but keeps the auto/yolo indicator on one row within the width.
func TestAutonomyBadgeKeptOnNarrowHeader(t *testing.T) {
	th := theme.New("aztec", theme.AztecPalette())
	pill := th.Style("dangerPill").Render(yoloPillText)
	for _, width := range []int{12, 16, 24} {
		m, _, _ := newTestModel(t, th)
		m = applyAll(m, tea.WindowSizeMsg{Width: width, Height: 30},
			client.SessionReadyMsg{SessionID: "sess-test-0001-with-a-long-handle", Mode: "yolo",
				Capabilities: client.Capabilities{Posture: postureYolo, ProjectTrust: client.ProjectUntrusted, SessionPermissionModes: true}})
		m.conv.recordFileChange("a.go")
		raw := m.renderHeader()
		if !strings.Contains(raw, pill) {
			t.Fatalf("width %d: the yolo indicator must survive: %q", width, stripANSIstr(raw))
		}
		row := strings.Split(raw, "\n")[0]
		if w := lipgloss.Width(row); w > width {
			t.Fatalf("width %d: header row is %d cells wide: %q", width, w, stripANSIstr(row))
		}
	}
}
