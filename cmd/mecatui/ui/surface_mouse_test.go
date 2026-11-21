package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

func clickFrameHit(m Model, hit renderedHitRegion, end bool) (Model, tea.Cmd) {
	x := hit.rect.x0
	if end {
		x = hit.rect.x1 - 1
	}
	x, y := m.metrics.localToGlobal(x, hit.rect.y0)
	updated, cmd := m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: x, Y: y})
	return updated.(Model), cmd
}

func offerCardBody(m Model, width, height int) Model {
	style := m.deps.Theme.Style("askCard")
	m = applyAll(m, tea.WindowSizeMsg{Width: width + style.GetHorizontalFrameSize(), Height: 100})
	above, below := m.chrome()
	return applyAll(m, tea.WindowSizeMsg{Width: m.width, Height: height + style.GetVerticalFrameSize() + sumHeight(above) + sumHeight(below)})
}

func currentFrameHit(m Model, match func(renderedHitRegion) bool) (renderedHitRegion, bool) {
	if m.hits == nil {
		return renderedHitRegion{}, false
	}
	for _, hit := range m.hits.frame {
		if match(hit) {
			return hit, true
		}
	}
	return renderedHitRegion{}, false
}

func assertFrameHitLabel(t *testing.T, frame string, metrics renderedSurfaceMetrics, hit renderedHitRegion, label string) {
	t.Helper()
	if height := hit.rect.y1 - hit.rect.y0; height != 1 {
		t.Fatalf("hit label height = %d, want 1", height)
	}
	if width := hit.rect.x1 - hit.rect.x0; width != ansi.StringWidth(label) {
		t.Fatalf("hit label width = %d, want %d for %q", width, ansi.StringWidth(label), label)
	}
	x, y := metrics.localToGlobal(hit.rect.x0, hit.rect.y0)
	lines := strings.Split(stripANSIstr(frame), "\n")
	if y < 0 || y >= len(lines) {
		t.Fatalf("hit label row %d outside rendered frame (%d rows)", y, len(lines))
	}
	if got := ansi.Cut(lines[y], x, x+hit.rect.x1-hit.rect.x0); got != label {
		t.Fatalf("hit label = %q, want %q", got, label)
	}
}

func TestCurrentFrameHitUsesPublishedFrameOnly(t *testing.T) {
	m, _ := openedEffort(t, "medium", false)
	if _, ok := currentFrameHit(m, func(renderedHitRegion) bool { return true }); ok || len(m.hits.frame) != 0 {
		t.Fatal("lookup published a hit without a View frame")
	}

	_ = m.View()
	if _, ok := currentFrameHit(m, func(renderedHitRegion) bool { return true }); !ok {
		t.Fatal("lookup missed a live published hit")
	}

	(&m).prepareSurfaceInput()
	if _, ok := currentFrameHit(m, func(renderedHitRegion) bool { return true }); ok || len(m.hits.frame) != 0 {
		t.Fatal("lookup republished an invalidated frame")
	}
}
