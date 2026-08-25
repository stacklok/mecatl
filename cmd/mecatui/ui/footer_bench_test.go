package ui

// Offline microbenchmarks for the footer context-meter render path (issue #65
// regression guard). renderFooter -> fitFooter runs on EVERY View() frame and is
// NOT covered by the per-block / join render caches (those memoize the scrollback
// conversation body; the footer is rebuilt verbatim every frame — see
// assembleLayout in layout.go calling m.renderFooter() unconditionally).
//
// Issue #65 flipped the COMMON case: previously window==0 by default, so the meter
// builders degraded to a cheap "ctx <N>" string; now the server-echoed context
// window is the default denominator, so the FULL bar path (ctxBar glyph repetition
// + ctxFraction + ctxPressureSlot + ctxLabel + lipgloss style.Render) runs every
// frame. These benches quantify that per-frame delta.
//
// Two granularities:
//   - BenchmarkFooterMeter{Bar,Degrade}: the three meter builders in isolation
//     (the exact code the change toggles between), no Model.
//   - BenchmarkFooterFit{Bar,Degrade}: the realistic full per-frame fitFooter path
//     (all three meter tiers + the usage facets + width fitting) over a Model, the
//     way renderFooter actually calls it each frame.
//
// They are Benchmarks, so `task test` (default -run) never runs them. Run with:
//   go test -run '^$' -bench 'BenchmarkFooter' -benchmem ./cmd/mecatui/ui/

import (
	"context"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/theme"
)

// benchTheme is a real (non-nil-style) theme so style.Render does the same ANSI
// work it does in production — a no-op theme would understate the bar-path cost.
func benchTheme() theme.Theme { return theme.New("aztec", theme.AztecPalette()) }

// representative occupancy: ~70% of a 200K window — lands in the "warn" band, so
// ctxBar emits a mix of filled (▓) + empty (░) glyphs (the realistic non-trivial bar).
const (
	benchUsed   int64 = 140_000
	benchWindow int64 = 200_000
)

var footerSink string

// BenchmarkFooterMeterBar: the NEW common-case meter path (window>0) — full bar.
func BenchmarkFooterMeterBar(b *testing.B) {
	th := benchTheme()
	b.ReportAllocs()
	for b.Loop() {
		footerSink = renderContextMeter(th, benchUsed, benchWindow)
		footerSink = renderContextMeterCompact(th, benchUsed, benchWindow)
		footerSink = renderContextMeterMinimal(th, benchUsed, benchWindow)
	}
}

// BenchmarkFooterMeterDegrade: the OLD common-case meter path (window==0) — the
// cheap "ctx <N>" degrade all three builders short-circuit to.
func BenchmarkFooterMeterDegrade(b *testing.B) {
	th := benchTheme()
	b.ReportAllocs()
	for b.Loop() {
		footerSink = renderContextMeter(th, benchUsed, 0)
		footerSink = renderContextMeterCompact(th, benchUsed, 0)
		footerSink = renderContextMeterMinimal(th, benchUsed, 0)
	}
}

// buildFooterModel builds a connected, sized Model with a non-trivial usage so
// renderUsageFacets does real work — the realistic per-frame footer input. The
// window source is chosen by the caller via resolvedSessionModel.ContextWindow.
func buildFooterModel(tb testing.TB, window int64) Model {
	tb.Helper()
	m := New(Deps{
		Theme:       benchTheme(),
		Ctx:         context.Background(),
		NoAltScreen: true,
	})
	m = applyAll(m,
		tea.WindowSizeMsg{Width: 100, Height: 40},
		client.SessionReadyMsg{SessionID: "perf-footer-0001"},
	)
	m.phase = phaseIdle
	m.statusMsg = "ready"
	m.contextTokens = benchUsed
	m.usage = client.Usage{
		InputTokens:      benchUsed,
		OutputTokens:     12_000,
		CacheReadTokens:  90_000,
		CacheWriteTokens: 4_000,
	}
	// The new default source for the denominator: the server-echoed per-model window.
	m.resolvedSessionModel = client.ResolvedModel{ContextWindow: window}
	return m
}

// BenchmarkFooterFitBar: the realistic full per-frame footer path with the NEW
// default (window>0 -> bar). This is what renderFooter pays every View() now.
func BenchmarkFooterFitBar(b *testing.B) {
	m := buildFooterModel(b, benchWindow)
	left := "ready"
	width := m.widthOr()
	b.ReportAllocs()
	for b.Loop() {
		footerSink = m.fitFooter(left, width)
	}
}

// BenchmarkFooterFitDegrade: the realistic full per-frame footer path with the OLD
// default (window==0 -> degrade). Same fitFooter ladder, cheap meter strings.
func BenchmarkFooterFitDegrade(b *testing.B) {
	m := buildFooterModel(b, 0)
	left := "ready"
	width := m.widthOr()
	b.ReportAllocs()
	for b.Loop() {
		footerSink = m.fitFooter(left, width)
	}
}
