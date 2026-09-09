package main

import (
	"context"
	"log/slog"
	"strings"

	"github.com/stacklok/mecatl/cmd/mecatui/ui"
)

const selectionTraceEnv = "MECATUI_SELECTION_TRACE"

// selectionTraceEnabled deliberately accepts only an explicit true value. This
// keeps the privacy-sensitive, high-frequency trace opt-in rather than treating
// an accidental non-empty environment variable as consent.
func selectionTraceEnabled(getenv func(string) string) bool {
	switch strings.ToLower(strings.TrimSpace(getenv(selectionTraceEnv))) {
	case "1", "true":
		return true
	default:
		return false
	}
}

// newSelectionTrace adapts the UI's content-free record to the already-configured
// mecatui diagnostic logger. A nil callback is the disabled fast path.
func newSelectionTrace(logger *slog.Logger, enabled bool) ui.SelectionTrace {
	if !enabled {
		return nil
	}
	return func(r ui.SelectionTraceRecord) {
		logger.LogAttrs(context.Background(), slog.LevelInfo, "mecatui selection trace",
			slog.String("event", r.Event),
			slog.Bool("view_dirty", r.ViewDirty),
			slog.Bool("selection_active", r.SelectionActive),
			slog.Int("anchor_line", r.AnchorLine), slog.Int("anchor_column", r.AnchorColumn),
			slog.Int("head_line", r.HeadLine), slog.Int("head_column", r.HeadColumn),
			slog.Bool("follow", r.Follow), slog.Int("y_offset", r.YOffset), slog.Bool("at_bottom", r.AtBottom),
			slog.Int("viewport_bytes", r.ViewportBytes),
			slog.Int("frame_lines", r.FrameLines),
			slog.Int("selection_base_bytes", r.SelectionBaseBytes))
	}
}
